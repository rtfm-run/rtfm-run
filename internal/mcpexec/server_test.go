package mcpexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"vanguard.run/internal/creds"
	"vanguard.run/internal/devbox"
)

// fakeBox is an in-memory box.
type fakeBox struct {
	files map[string]string
	ran   []string
}

func newFakeBox() *fakeBox { return &fakeBox{files: map[string]string{}} }

func (f *fakeBox) Workdir() string { return "/work/project" }

func (f *fakeBox) Exec(_ context.Context, command, _ string, _ time.Duration, env map[string]string) (devbox.Result, error) {
	f.ran = append(f.ran, command)
	switch {
	case command == "env":
		var b strings.Builder
		for k, v := range env {
			b.WriteString(k + "=" + v + "\n")
		}
		return devbox.Result{Output: b.String()}, nil
	case strings.HasPrefix(command, "gemini"):
		return devbox.Result{ExitCode: 127, Output: "bash: gemini: command not found\n"}, nil
	}
	return devbox.Result{Output: "hello\n"}, nil
}

func (f *fakeBox) ReadFile(_ context.Context, p string) ([]byte, error) {
	v, ok := f.files[devbox.Resolve(f.Workdir(), p)]
	if !ok {
		return nil, fmt.Errorf("read %s: No such file or directory", p)
	}
	return []byte(v), nil
}

func (f *fakeBox) WriteFile(_ context.Context, p string, data []byte) error {
	f.files[devbox.Resolve(f.Workdir(), p)] = string(data)
	return nil
}

func (f *fakeBox) ListDir(_ context.Context, p string) (string, error) {
	var names []string
	dir := devbox.Resolve(f.Workdir(), p) + "/"
	for k := range f.files {
		if strings.HasPrefix(k, dir) {
			names = append(names, strings.TrimPrefix(k, dir))
		}
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}

func serve(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")), &out); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		resps = append(resps, m)
	}
	return resps
}

func call(id int, tool string, args string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, args)
}

func textOf(resp map[string]any) string {
	r := resp["result"].(map[string]any)
	return r["content"].([]any)[0].(map[string]any)["text"].(string)
}

func isErr(resp map[string]any) bool { return resp["result"].(map[string]any)["isError"] == true }

func TestServeProtocolAndExec(t *testing.T) {
	fb := newFakeBox()
	var logBuf bytes.Buffer
	s := &Server{Box: fb, Log: &logBuf, Version: "test"}
	r := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call(3, "exec", `{"command":"echo hello"}`),
		call(4, "exec", `{"command":"gemini --version"}`),
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
		call(6, "request_credential", `{"name":"X"}`), // not offered without Creds
	)
	if len(r) != 6 { // the notification gets no response
		t.Fatalf("got %d responses", len(r))
	}
	if v := r[0]["result"].(map[string]any)["protocolVersion"]; v != "2025-06-18" {
		t.Errorf("protocolVersion = %v", v)
	}
	var names []string
	for _, tl := range r[1]["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "exec,read_file,write_file,edit_file,list_dir" {
		t.Errorf("tools = %v", names)
	}
	if isErr(r[2]) || !strings.Contains(textOf(r[2]), "exited with code 0") {
		t.Errorf("exec ok = %v", r[2])
	}
	if !isErr(r[3]) || !strings.Contains(textOf(r[3]), "code 127") {
		t.Errorf("exec fail = %v", r[3])
	}
	if r[4]["error"] == nil || r[5]["error"] == nil {
		t.Errorf("unknown method/tool should error: %v %v", r[4], r[5])
	}
	recs := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(recs) != 2 || !strings.Contains(recs[1], `"exit_code":127`) || !strings.Contains(recs[0], `"kind":"exec"`) {
		t.Errorf("log = %s", logBuf.String())
	}
}

func TestFileTools(t *testing.T) {
	fb := newFakeBox()
	fb.files["/work/project/README.md"] = "# Demo\n\nRun `hello --user x`.\n\nRun `hello --user x` again.\n"
	var logBuf bytes.Buffer
	s := &Server{Box: fb, Log: &logBuf}
	r := serve(t, s,
		call(1, "read_file", `{"path":"README.md"}`),
		call(2, "read_file", `{"path":"README.md","start_line":3,"max_lines":1}`),
		call(3, "edit_file", `{"path":"README.md","old_string":"--user","new_string":"--name"}`),
		call(4, "edit_file", `{"path":"README.md","old_string":"--user","new_string":"--name","replace_all":true}`),
		call(5, "edit_file", `{"path":"README.md","old_string":"nope","new_string":"x"}`),
		call(6, "write_file", `{"path":"notes/a.txt","content":"hi"}`),
		call(7, "list_dir", `{}`),
		call(8, "read_file", `{"path":"missing.md"}`),
	)
	if !strings.HasPrefix(textOf(r[0]), "# Demo") {
		t.Errorf("read = %q", textOf(r[0]))
	}
	if textOf(r[1]) != "Run `hello --user x`.\n" {
		t.Errorf("read slice = %q", textOf(r[1]))
	}
	if !isErr(r[2]) || !strings.Contains(textOf(r[2]), "occurs 2 times") {
		t.Errorf("ambiguous edit = %v", r[2])
	}
	if isErr(r[3]) || strings.Contains(fb.files["/work/project/README.md"], "--user") {
		t.Errorf("replace_all edit = %v; file = %q", r[3], fb.files["/work/project/README.md"])
	}
	if !isErr(r[4]) || !strings.Contains(textOf(r[4]), "not found") {
		t.Errorf("missing edit = %v", r[4])
	}
	if fb.files["/work/project/notes/a.txt"] != "hi" {
		t.Errorf("write_file did not write: %v", fb.files)
	}
	if !strings.Contains(textOf(r[6]), "notes/a.txt") {
		t.Errorf("list_dir = %q", textOf(r[6]))
	}
	if !isErr(r[7]) {
		t.Errorf("missing read should error")
	}
	if !strings.Contains(logBuf.String(), `"kind":"edit_file","command":"/work/project/README.md"`) ||
		!strings.Contains(logBuf.String(), `"kind":"write_file","command":"/work/project/notes/a.txt"`) {
		t.Errorf("file ops not logged: %s", logBuf.String())
	}
}

type fakeCreds struct{ asked []string }

func (f *fakeCreds) Request(_ context.Context, r creds.Request) (creds.Answer, error) {
	f.asked = append(f.asked, r.Name)
	if r.Name == "GEMINI_API_KEY" {
		return creds.Answer{Status: creds.StatusProvided, Value: "AIza-super-secret-1234", Source: "prompt"}, nil
	}
	return creds.Answer{Status: creds.StatusUnavailable, Message: "the user skipped it"}, nil
}

func TestRequestCredential(t *testing.T) {
	fc := &fakeCreds{}
	fb := newFakeBox()
	var logBuf bytes.Buffer
	s := &Server{Box: fb, Creds: fc, Log: &logBuf}
	r := serve(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		call(2, "request_credential", `{"name":"GEMINI_API_KEY","description":"Gemini CLI auth","url":"https://aistudio.google.com/apikey"}`),
		call(3, "exec", `{"command":"env"}`),
		call(4, "exec", `{"command":"env > .env"}`),
		call(5, "request_credential", `{"name":"OTHER_TOKEN","description":"x"}`),
		call(6, "request_credential", `{"name":"bad name","description":"x"}`),
	)
	b, _ := json.Marshal(r)
	all := string(b)
	if !strings.Contains(all, `request_credential`) {
		t.Error("request_credential not listed")
	}
	if strings.Contains(all, "AIza-super-secret-1234") || strings.Contains(logBuf.String(), "AIza-super-secret-1234") {
		t.Fatalf("secret leaked to agent or log:\n%s\n%s", all, logBuf.String())
	}
	if !strings.Contains(textOf(r[2]), "GEMINI_API_KEY=[REDACTED:GEMINI_API_KEY]") {
		t.Errorf("env output not redacted or secret not injected: %q", textOf(r[2]))
	}
	// A file containing the secret is redacted when the agent reads it.
	fb.files["/work/project/.env"] = "GEMINI_API_KEY=AIza-super-secret-1234\n"
	r2 := serve(t, s, call(7, "read_file", `{"path":".env"}`))
	if strings.Contains(textOf(r2[0]), "AIza-super-secret-1234") {
		t.Errorf("read_file leaked the secret: %q", textOf(r2[0]))
	}
	if !strings.Contains(textOf(r[4]), "OTHER_TOKEN was not provided") || !strings.Contains(textOf(r[5]), "not a valid environment variable name") {
		t.Errorf("unavailable/invalid handling: %q / %q", textOf(r[4]), textOf(r[5]))
	}
	if len(fc.asked) != 2 {
		t.Errorf("asked = %v", fc.asked)
	}
}
