package creds

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// Request is what the agent asks for. It never contains a value.
type Request struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url,omitempty"`
}

// Request outcomes.
const (
	StatusProvided    = "provided"
	StatusUnavailable = "unavailable" // nobody could answer (non-interactive, or skipped)
	StatusInvalid     = "invalid"
)

// Answer is the broker's reply to the MCP server. Value is set only when
// Status is StatusProvided.
type Answer struct {
	Status  string `json:"status"`
	Value   string `json:"value,omitempty"`
	Source  string `json:"source,omitempty"` // "store" or "prompt"
	Message string `json:"message,omitempty"`
}

// Prompter asks the user for a credential. ok=false means the user skipped.
type Prompter interface {
	Prompt(ctx context.Context, target string, req Request) (value string, save bool, ok bool, err error)
}

// Source is where saved credentials come from: the user's local Store, or
// (in the service) a quickstart's encrypted credentials in the database.
type Source interface {
	Get(name string) (string, bool, error)
	Set(name, value string) error
}

// Broker answers credential requests for runs. One Broker is shared by all
// runs in a process; prompts are serialized so parallel runs never ask the
// user two things at once.
type Broker struct {
	Store    Source   // may be nil
	Prompter Prompter // nil = never prompt
	promptMu sync.Mutex
}

// Resolve answers one request.
func (b *Broker) Resolve(ctx context.Context, target string, req Request) Answer {
	if !ValidName(req.Name) {
		return Answer{Status: StatusInvalid, Message: fmt.Sprintf("%q is not a valid environment variable name", req.Name)}
	}
	if b.Store != nil {
		if v, ok, err := b.Store.Get(req.Name); err == nil && ok {
			return Answer{Status: StatusProvided, Value: v, Source: "store"}
		}
	}
	if b.Prompter == nil {
		return Answer{Status: StatusUnavailable, Message: "no saved value and prompting is disabled"}
	}
	b.promptMu.Lock()
	defer b.promptMu.Unlock()
	// Another run may have saved it while we waited for the prompt.
	if b.Store != nil {
		if v, ok, err := b.Store.Get(req.Name); err == nil && ok {
			return Answer{Status: StatusProvided, Value: v, Source: "store"}
		}
	}
	v, save, ok, err := b.Prompter.Prompt(ctx, target, req)
	if err != nil {
		return Answer{Status: StatusUnavailable, Message: "prompt failed: " + err.Error()}
	}
	if !ok || v == "" {
		return Answer{Status: StatusUnavailable, Message: "the user skipped it"}
	}
	if save && b.Store != nil {
		_ = b.Store.Set(req.Name, v)
	}
	return Answer{Status: StatusProvided, Value: v, Source: "prompt"}
}

// Record is one request as it appears in a report: no value.
type Record struct {
	Request
	Status string `json:"status"`
	Source string `json:"source,omitempty"`
}

// Session serves one stage's MCP server over a unix socket and remembers
// what was requested and which values were handed out (for redaction).
type Session struct {
	broker *Broker
	target string
	ln     net.Listener
	path   string

	mu      sync.Mutex
	records []Record
	values  map[string]string
	wg      sync.WaitGroup
}

// Listen starts a session on a unix socket at path.
func (b *Broker) Listen(ctx context.Context, target, path string) (*Session, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("creds: listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	s := &Session{broker: b, target: target, ln: ln, path: path, values: map[string]string{}}
	s.wg.Add(1)
	go s.accept(ctx)
	return s, nil
}

func (s *Session) accept(ctx context.Context) {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // closed
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			s.handle(ctx, conn)
		}()
	}
}

func (s *Session) handle(ctx context.Context, conn net.Conn) {
	sc := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		var req Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(Answer{Status: StatusInvalid, Message: "bad request"})
			continue
		}
		req.Name = strings.TrimSpace(req.Name)
		ans := s.broker.Resolve(ctx, s.target, req)
		s.mu.Lock()
		s.records = append(s.records, Record{Request: req, Status: ans.Status, Source: ans.Source})
		if ans.Status == StatusProvided {
			s.values[req.Name] = ans.Value
		}
		s.mu.Unlock()
		if err := enc.Encode(ans); err != nil {
			return
		}
	}
}

// Path is the socket path to give the MCP server.
func (s *Session) Path() string { return s.path }

// Records returns what was requested, without values.
func (s *Session) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.records...)
}

// Values returns the credentials handed out in this session, for redaction
// and for commands vanguard itself runs in the devbox (the success check).
func (s *Session) Values() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]string, len(s.values))
	for k, v := range s.values {
		m[k] = v
	}
	return m
}

// Close stops the session.
func (s *Session) Close() error {
	err := s.ln.Close()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	_ = os.Remove(s.path)
	return err
}

// Client is the MCP server's side of the socket.
type Client struct{ Path string }

// Request asks the broker for a credential.
func (c Client) Request(ctx context.Context, req Request) (Answer, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return Answer{}, fmt.Errorf("creds: broker unavailable: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Answer{}, err
	}
	sc := bufio.NewScanner(conn)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return Answer{}, err
		}
		return Answer{}, errors.New("creds: broker closed the connection")
	}
	var ans Answer
	if err := json.Unmarshal(sc.Bytes(), &ans); err != nil {
		return Answer{}, err
	}
	return ans, nil
}
