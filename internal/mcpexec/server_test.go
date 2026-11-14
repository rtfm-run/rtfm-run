package mcpexec

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"vanguard.run/internal/devbox"
)

type fakeExec struct{ got []string }

func (f *fakeExec) Exec(_ context.Context, command, cwd string, _ time.Duration) (devbox.Result, error) {
	f.got = append(f.got, command)
	if strings.HasPrefix(command, "gemini") {
		return devbox.Result{ExitCode: 127, Output: "bash: gemini: command not found\n"}, nil
	}
	return devbox.Result{ExitCode: 0, Output: "hello\n"}, nil
}

func TestServe(t *testing.T) {
	fe := &fakeExec{}
	var logBuf bytes.Buffer
	s := &Server{Exec: fe, Log: &logBuf, Version: "test"}

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"exec","arguments":{"command":"echo hello"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"exec","arguments":{"command":"gemini --version"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	}, "\n")
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 { // the notification gets no response
		t.Fatalf("got %d responses:\n%s", len(lines), out.String())
	}
	var resps []map[string]any
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		resps = append(resps, m)
	}
	if v := resps[0]["result"].(map[string]any)["protocolVersion"]; v != "2025-06-18" {
		t.Errorf("protocolVersion = %v", v)
	}
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "exec" {
		t.Errorf("tools = %v", tools)
	}
	ok := resps[2]["result"].(map[string]any)
	if ok["isError"] != false || !strings.Contains(textOf(ok), "exited with code 0") {
		t.Errorf("call ok = %v", ok)
	}
	bad := resps[3]["result"].(map[string]any)
	if bad["isError"] != true || !strings.Contains(textOf(bad), "code 127") {
		t.Errorf("call bad = %v", bad)
	}
	if resps[4]["error"] == nil {
		t.Errorf("unknown method should error: %v", resps[4])
	}

	if len(fe.got) != 2 {
		t.Errorf("executed %v", fe.got)
	}
	recs := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(recs) != 2 || !strings.Contains(recs[1], `"exit_code":127`) {
		t.Errorf("log = %s", logBuf.String())
	}
}

func textOf(result map[string]any) string {
	c := result["content"].([]any)
	return c[0].(map[string]any)["text"].(string)
}
