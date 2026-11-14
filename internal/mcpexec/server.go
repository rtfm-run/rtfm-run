// Package mcpexec is a minimal MCP server (stdio, JSON-RPC 2.0) that is the
// agent's only interface to its developer machine (the devbox). Tools:
//
//   - exec: run a shell command in the box
//   - read_file, write_file, edit_file, list_dir: files in the box
//   - request_credential: ask the user (via rtfm's credential broker)
//     for a credential; if provided, it is injected into the environment of
//     every later exec call and redacted from all output. The agent never
//     sees the value.
//
// agy launches it as an MCP server for the run. agy's own shell and file
// tools are denied, so nothing the agent does touches the host; that also
// keeps the box backend swappable (container, microVM, remote sandbox).
// Commands and file writes are appended to a JSONL log that rtfm reads
// as ground-truth evidence.
package mcpexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"rtfm.run/internal/creds"
	"rtfm.run/internal/devbox"
)

// ServerName is the key used in mcp_config.json; agy permission rules
// refer to it as mcp(devbox/*).
const ServerName = "devbox"

// Box is the part of devbox.Box the server uses.
type Box interface {
	Workdir() string
	Exec(ctx context.Context, command, cwd string, timeout time.Duration, env map[string]string) (devbox.Result, error)
	ReadFile(ctx context.Context, p string) ([]byte, error)
	WriteFile(ctx context.Context, p string, data []byte) error
	ListDir(ctx context.Context, p string) (string, error)
}

// CredentialSource answers credential requests (creds.Client in production).
type CredentialSource interface {
	Request(ctx context.Context, req creds.Request) (creds.Answer, error)
}

// Server handles MCP requests.
type Server struct {
	Box     Box
	Creds   CredentialSource // nil = request_credential is not offered
	Log     io.Writer        // JSONL ExecRecords; may be nil
	Version string

	mu      sync.Mutex        // guards log writes and secrets
	secrets map[string]string // provided credentials, injected into exec
}

// maxRead caps read_file output.
const maxRead = 256 << 10

type tool struct {
	name, description string
	schema            map[string]any
}

func obj(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "properties": props}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func (s *Server) tools() []tool {
	ts := []tool{
		{"exec", `Run a shell command on your development machine: a fresh Linux box (Ubuntu) where you are root. It has git, build tools, Python 3 (with an active default environment), Node.js and Go, and nothing specific to this project. Commands start in the project directory. This is the only terminal you have. Each call starts a new bash login shell, so use absolute paths or 'cd dir && cmd', and persist environment variables in files rather than with export. For long-running servers, start them in the background (nohup cmd > server.log 2>&1 &) and check on them with later calls.`,
			obj([]string{"command"}, map[string]any{
				"command":         str("Shell command to run with bash -lc."),
				"cwd":             str("Working directory (default: the project directory)."),
				"timeout_seconds": integer("Timeout in seconds (default 600, max 1800)."),
			})},
		{"read_file", "Read a text file on your development machine. Relative paths are inside the project directory.",
			obj([]string{"path"}, map[string]any{
				"path":       str("File path."),
				"start_line": integer("First line to return, 1-based (optional)."),
				"max_lines":  integer("Maximum number of lines to return (optional)."),
			})},
		{"write_file", "Create or overwrite a file on your development machine, creating parent directories. Relative paths are inside the project directory.",
			obj([]string{"path", "content"}, map[string]any{
				"path":    str("File path."),
				"content": str("The complete new file contents."),
			})},
		{"edit_file", "Replace text in a file on your development machine. old_string must match exactly (including whitespace) and, unless replace_all is true, occur exactly once; include surrounding lines to make it unique.",
			obj([]string{"path", "old_string", "new_string"}, map[string]any{
				"path":        str("File path."),
				"old_string":  str("Exact text to replace."),
				"new_string":  str("Replacement text."),
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence (default false)."},
			})},
		{"list_dir", "List a directory on your development machine (like ls -la). Relative paths are inside the project directory.",
			obj([]string{}, map[string]any{"path": str("Directory (default: the project directory).")})},
		{"report_progress", `Post a one-line status update that the person watching this run sees live, e.g. "Step 2 fails: pip can't import the protobuf-generated modules; checking which protobuf version works". Call it when you start a new step, when something fails, and when you change approach: roughly every few commands, never in place of doing the work. Plain language, under 200 characters, no secrets.`,
			obj([]string{"message"}, map[string]any{"message": str("The update, one sentence.")})},
	}
	if s.Creds != nil {
		ts = append(ts, tool{"request_credential", `Ask the user for a credential (API key, token, password) that you need to keep following the quickstart, instead of using a placeholder. Name it with the environment variable the docs or the error message use (e.g. GEMINI_API_KEY). If the user provides it, it is set in the environment of every command you run from then on; you never see the value, and it appears as [REDACTED:NAME] in output. If it isn't provided, continue without it and report the steps that need it as failed.`,
			obj([]string{"name", "description"}, map[string]any{
				"name":        str("Environment variable name, e.g. GEMINI_API_KEY."),
				"description": str("What it is for, in the user's terms (which step needs it and why)."),
				"url":         str("Where to get it, if the docs or error say (optional)."),
			})})
	}
	return ts
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
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
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
		var list []any
		for _, t := range s.tools() {
			list = append(list, map[string]any{"name": t.name, "description": t.description, "inputSchema": t.schema})
		}
		return reply(map[string]any{"tools": list})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(-32602, "invalid params")
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		res, ok := s.call(ctx, p.Name, p.Arguments)
		if !ok {
			return fail(-32602, fmt.Sprintf("unknown tool %q", p.Name))
		}
		return reply(res)
	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return nil
		}
		return fail(-32601, "method not found: "+req.Method)
	}
}

func (s *Server) call(ctx context.Context, name string, raw json.RawMessage) (map[string]any, bool) {
	bad := func(err error) map[string]any { return toolText("error: invalid arguments: "+err.Error(), true) }
	switch name {
	case "exec":
		var a struct {
			Command        string `json:"command"`
			Cwd            string `json:"cwd"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return bad(err), true
		}
		if strings.TrimSpace(a.Command) == "" {
			return toolText("error: command is required", true), true
		}
		return s.exec(ctx, a.Command, a.Cwd, a.TimeoutSeconds), true
	case "read_file":
		var a struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			MaxLines  int    `json:"max_lines"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return bad(err), true
		}
		return s.readFile(ctx, a.Path, a.StartLine, a.MaxLines), true
	case "write_file":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return bad(err), true
		}
		return s.writeFile(ctx, a.Path, a.Content), true
	case "edit_file":
		var a struct {
			Path       string `json:"path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return bad(err), true
		}
		return s.editFile(ctx, a.Path, a.OldString, a.NewString, a.ReplaceAll), true
	case "list_dir":
		var a struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return bad(err), true
		}
		out, err := s.Box.ListDir(ctx, a.Path)
		if err != nil {
			return toolText("error: "+err.Error(), true), true
		}
		return toolText(creds.Redact(out, s.secretsSnapshot()), false), true
	case "report_progress":
		var a struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return bad(err), true
		}
		msg := strings.Join(strings.Fields(a.Message), " ")
		if msg == "" {
			return toolText("error: message is required", true), true
		}
		if len(msg) > 500 {
			msg = strings.ToValidUTF8(msg[:500], "")
		}
		s.log(devbox.ExecRecord{Time: time.Now().UTC(), Kind: "progress", Command: creds.Redact(msg, s.secretsSnapshot())})
		return toolText("Posted.", false), true
	case "request_credential":
		if s.Creds == nil {
			return nil, false
		}
		var a creds.Request
		if err := json.Unmarshal(raw, &a); err != nil {
			return bad(err), true
		}
		a.Name = strings.TrimSpace(a.Name)
		return s.requestCredential(ctx, a), true
	}
	return nil, false
}

func (s *Server) exec(ctx context.Context, command, cwd string, timeoutSec int) map[string]any {
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	timeoutSec = min(timeoutSec, 1800)
	start := time.Now()
	secrets := s.secretsSnapshot()
	res, err := s.Box.Exec(ctx, command, cwd, time.Duration(timeoutSec)*time.Second, secrets)
	res.Output = creds.Redact(res.Output, secrets)
	rec := devbox.ExecRecord{
		Time:       start.UTC(),
		Kind:       "exec",
		Command:    command,
		Cwd:        cwd,
		ExitCode:   res.ExitCode,
		TimedOut:   res.TimedOut,
		DurationMS: time.Since(start).Milliseconds(),
		Output:     res.Output,
	}
	if err != nil {
		rec.ExitCode = -1
		rec.Output = creds.Redact(err.Error(), secrets)
	}
	s.log(rec)

	if err != nil {
		return toolText("error: could not run command on the development machine: "+rec.Output, true)
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

func (s *Server) readFile(ctx context.Context, p string, start, max int) map[string]any {
	if strings.TrimSpace(p) == "" {
		return toolText("error: path is required", true)
	}
	data, err := s.Box.ReadFile(ctx, p)
	if err != nil {
		return toolText("error: "+err.Error(), true)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return toolText(fmt.Sprintf("%s is a binary file (%d bytes).", p, len(data)), false)
	}
	text := string(data)
	if start > 1 || max > 0 {
		lines := strings.SplitAfter(text, "\n")
		if start < 1 {
			start = 1
		}
		if start > len(lines) {
			return toolText(fmt.Sprintf("%s has only %d lines.", p, len(lines)), true)
		}
		lines = lines[start-1:]
		if max > 0 && max < len(lines) {
			lines = lines[:max]
		}
		text = strings.Join(lines, "")
	}
	truncated := ""
	if len(text) > maxRead {
		text = text[:maxRead]
		truncated = fmt.Sprintf("\n…(truncated at %d bytes; use start_line/max_lines)", maxRead)
	}
	return toolText(creds.Redact(text, s.secretsSnapshot())+truncated, false)
}

func (s *Server) writeFile(ctx context.Context, p, content string) map[string]any {
	if strings.TrimSpace(p) == "" {
		return toolText("error: path is required", true)
	}
	start := time.Now()
	err := s.Box.WriteFile(ctx, p, []byte(content))
	s.logFileOp("write_file", p, start, err)
	if err != nil {
		return toolText("error: "+err.Error(), true)
	}
	return toolText(fmt.Sprintf("Wrote %d bytes to %s.", len(content), devbox.Resolve(s.Box.Workdir(), p)), false)
}

func (s *Server) editFile(ctx context.Context, p, oldS, newS string, all bool) map[string]any {
	if strings.TrimSpace(p) == "" || oldS == "" {
		return toolText("error: path and old_string are required", true)
	}
	data, err := s.Box.ReadFile(ctx, p)
	if err != nil {
		return toolText("error: "+err.Error(), true)
	}
	text := string(data)
	n := strings.Count(text, oldS)
	switch {
	case n == 0:
		return toolText("error: old_string not found in "+p+"; read the file and copy the text exactly", true)
	case n > 1 && !all:
		return toolText(fmt.Sprintf("error: old_string occurs %d times in %s; add surrounding lines to make it unique, or set replace_all", n, p), true)
	}
	replaced := 1
	if all {
		text = strings.ReplaceAll(text, oldS, newS)
		replaced = n
	} else {
		text = strings.Replace(text, oldS, newS, 1)
	}
	start := time.Now()
	err = s.Box.WriteFile(ctx, p, []byte(text))
	s.logFileOp("edit_file", p, start, err)
	if err != nil {
		return toolText("error: "+err.Error(), true)
	}
	return toolText(fmt.Sprintf("Replaced %d occurrence(s) in %s.", replaced, p), false)
}

func (s *Server) secretsSnapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]string, len(s.secrets))
	for k, v := range s.secrets {
		m[k] = v
	}
	return m
}

func (s *Server) requestCredential(ctx context.Context, req creds.Request) map[string]any {
	if !creds.ValidName(req.Name) {
		return toolText(fmt.Sprintf("error: %q is not a valid environment variable name (use e.g. GEMINI_API_KEY)", req.Name), true)
	}
	s.mu.Lock()
	_, have := s.secrets[req.Name]
	s.mu.Unlock()
	if have {
		return toolText(req.Name+" is already set in the environment of every command you run.", false)
	}
	ans, err := s.Creds.Request(ctx, req)
	if err != nil {
		return toolText("The credential could not be requested ("+err.Error()+"). Continue without it and report the steps that need it as failed.", true)
	}
	if ans.Status != creds.StatusProvided {
		msg := fmt.Sprintf("%s was not provided", req.Name)
		if ans.Message != "" {
			msg += " (" + ans.Message + ")"
		}
		return toolText(msg+". Continue without it and report the steps that need it as failed, noting that "+req.Name+" was unavailable.", true)
	}
	s.mu.Lock()
	if s.secrets == nil {
		s.secrets = map[string]string{}
	}
	s.secrets[req.Name] = ans.Value
	s.mu.Unlock()
	return toolText(req.Name+" is now set in the environment of every command you run. Its value is hidden from you and shows as [REDACTED:"+req.Name+"] in output. Don't write it into files the docs don't ask for.", false)
}

func (s *Server) logFileOp(kind, p string, start time.Time, err error) {
	rec := devbox.ExecRecord{Time: start.UTC(), Kind: kind, Command: devbox.Resolve(s.Box.Workdir(), p),
		DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		rec.ExitCode = 1
		rec.Output = err.Error()
	}
	s.log(rec)
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
