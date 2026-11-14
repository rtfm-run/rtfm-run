package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	home, defaultWorkdir = t.TempDir(), t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /exec", handleExec)
	mux.HandleFunc("GET /file", handleReadFile)
	mux.HandleFunc("PUT /file", handleWriteFile)
	mux.HandleFunc("PUT /tar", handleTar)
	srv := httptest.NewServer(auth("tok", mux))
	t.Cleanup(srv.Close)
	return srv
}

func req(t *testing.T, srv *httptest.Server, method, path, token string, body []byte) *http.Response {
	t.Helper()
	r, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestAuth(t *testing.T) {
	srv := newTestServer(t)
	if res := req(t, srv, "GET", "/health", "", nil); res.StatusCode != 200 {
		t.Errorf("health without token = %d", res.StatusCode)
	}
	if res := req(t, srv, "POST", "/exec", "wrong", []byte(`{"command":"true"}`)); res.StatusCode != 401 {
		t.Errorf("bad token = %d", res.StatusCode)
	}
}

func TestExecAndFiles(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()

	res := req(t, srv, "POST", "/exec", "tok", []byte(`{"command":"echo $V; exit 4","cwd":"`+dir+`","env":{"V":"hi"}}`))
	var out execResp
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.ExitCode != 4 || strings.TrimSpace(out.Output) != "hi" {
		t.Errorf("exec = %+v", out)
	}
	res = req(t, srv, "POST", "/exec", "tok", []byte(`{"command":"sleep 20","timeout_ms":300}`))
	out = execResp{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if !out.TimedOut || out.ExitCode != 124 {
		t.Errorf("timeout = %+v", out)
	}

	p := filepath.Join(dir, "x", "y.txt")
	if res := req(t, srv, "PUT", "/file?path="+p, "tok", []byte("data")); res.StatusCode != 204 {
		t.Fatalf("write = %d", res.StatusCode)
	}
	if b, _ := os.ReadFile(p); string(b) != "data" {
		t.Errorf("file = %q", b)
	}
	if res := req(t, srv, "GET", "/file?path="+filepath.Join(dir, "nope"), "tok", nil); res.StatusCode != 404 {
		t.Errorf("missing = %d", res.StatusCode)
	}
}

func TestTarRejectsEscapes(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "ok.txt", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("ok"))
	_ = tw.Close()
	if res := req(t, srv, "PUT", "/tar?dir="+dir, "tok", buf.Bytes()); res.StatusCode != 204 {
		t.Fatalf("tar = %d", res.StatusCode)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ok.txt")); string(b) != "ok" {
		t.Errorf("extracted = %q", b)
	}

	// "../evil" is cleaned to stay inside dir rather than escaping it.
	buf.Reset()
	tw = tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	req(t, srv, "PUT", "/tar?dir="+dir, "tok", buf.Bytes())
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Error("tar entry escaped the target directory")
	}
}

func TestParseCmdline(t *testing.T) {
	m := parseCmdline("console=ttyS0 rtfm.token=abc rtfm.dns=1.1.1.1,8.8.8.8 quiet")
	if m["rtfm.token"] != "abc" || m["rtfm.dns"] != "1.1.1.1,8.8.8.8" {
		t.Errorf("parsed = %v", m)
	}
}
