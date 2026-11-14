// Package mcpexec is a minimal MCP server (stdio, JSON-RPC 2.0) exposing one
// tool, `exec`, that runs shell commands in the devbox container.
//
// agy launches it as an MCP server for the run. agy's own shell tool is
// denied for the run, so this is the only way the agent can execute
// anything, and every command lands in the devbox, never on the host. Each
// call is also appended to a JSONL log that vanguard reads as ground-truth
// evidence.
package mcpexec

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"vanguard.run/internal/devbox"
)

// ServerName is the key used in mcp_config.json; agy permission rules
// refer to it as mcp(devbox/*).
const ServerName = "devbox"

// Executor runs a command in the devbox.
type Executor interface {
	Exec(ctx context.Context, command, cwd string, timeout time.Duration) (devbox.Result, error)
}

// Server handles MCP requests.
type Server struct {
	Exec    Executor
	Workdir string
	Log     io.Writer // JSONL ExecRecords; may be nil
	Version string

	mu sync.Mutex // serializes log writes
}

const toolDescription = `Run a shell command on your development machine: a fresh Linux box (Ubuntu) where you are root. It has git, build tools, Python 3, Node.js and Go, and nothing specific to this project. The project is checked out at the default working directory. Use this tool for every shell command. Each call starts a new bash login shell, so use absolute paths or 'cd dir && cmd', and persist environment variables in files rather than with export. For long-running servers, start them in the background (nohup cmd > server.log 2>&1 &) and check on them with later calls.`

var execSchema = map[string]any{
	"type":     "object",
	"required": []string{"command"},
	"properties": map[string]any{
		"command":         map[string]any{"type": "string", "description": "Shell command to run with bash -lc."},
		"cwd":             map[string]any{"type": "string", "description": "Working directory (default: project root)."},
		"timeout_seconds": map[string]any{"type": "integer", "description": "Timeout in seconds (default 600, max 1800)."},
	},
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads newline-delimited JSON-RPC messages from r and writes
// responses to w until r is closed. Requests are handled sequentially.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		resp := s.handle(ctx, &req)
		if resp == nil { // notification
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req *request) *response {
	isNotification := len(req.ID) == 0
	reply := func(result any) *response {
		if isNotification {
			return nil
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
	}
	fail := func(code int, msg string) *response {
		if isNotification {
			return nil
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{code, msg}}
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return reply(map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ServerName, "version": s.Version},
		})
	case "ping":
		return reply(map[string]any{})
	case "tools/list":
		return reply(map[string]any{"tools": []any{map[string]any{
			"name":        "exec",
			"description": toolDescription,
			"inputSchema": execSchema,
		}}})
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments struct {
				Command        string `json:"command"`
				Cwd            string `json:"cwd"`
				TimeoutSeconds int    `json:"timeout_seconds"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(-32602, "invalid params")
		}
		if p.Name != "exec" {
			return fail(-32602, fmt.Sprintf("unknown tool %q", p.Name))
		}
		if strings.TrimSpace(p.Arguments.Command) == "" {
			return reply(toolText("error: command is required", true))
		}
		return reply(s.call(ctx, p.Arguments.Command, p.Arguments.Cwd, p.Arguments.TimeoutSeconds))
	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return nil
		}
		return fail(-32601, "method not found: "+req.Method)
	}
}

func (s *Server) call(ctx context.Context, command, cwd string, timeoutSec int) map[string]any {
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	timeoutSec = min(timeoutSec, 1800)
	start := time.Now()
	res, err := s.Exec.Exec(ctx, command, cwd, time.Duration(timeoutSec)*time.Second)
	rec := devbox.ExecRecord{
		Time:       start.UTC(),
		Command:    command,
		Cwd:        cwd,
		ExitCode:   res.ExitCode,
		TimedOut:   res.TimedOut,
		DurationMS: time.Since(start).Milliseconds(),
		Output:     res.Output,
	}
	if err != nil {
		rec.ExitCode = -1
		rec.Output = err.Error()
	}
	s.log(rec)

	if err != nil {
		return toolText("error: could not run command on the development machine: "+err.Error(), true)
	}
	var b strings.Builder
	if res.TimedOut {
		fmt.Fprintf(&b, "Command timed out after %ds (exit code 124).\n", timeoutSec)
	} else {
		fmt.Fprintf(&b, "Command exited with code %d.\n", res.ExitCode)
	}
	if res.Output != "" {
		b.WriteString("\nOutput:\n")
		b.WriteString(res.Output)
	}
	return toolText(b.String(), res.ExitCode != 0)
}

func (s *Server) log(rec devbox.ExecRecord) {
	if s.Log == nil {
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.Log.Write(append(b, '\n'))
	if f, ok := s.Log.(*os.File); ok {
		_ = f.Sync()
	}
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isError,
	}
}
