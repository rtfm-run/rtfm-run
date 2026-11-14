// Command rtfm-agent runs inside a devbox microVM and gives rtfm
// (on the host) exec and file access to the guest over HTTP.
//
// It reads its bearer token and DNS servers from the kernel command line
// (rtfm.token=…, rtfm.dns=a,b) and listens on :7000. Every request
// except /health must carry "Authorization: Bearer <token>".
//
//	GET  /health
//	POST /exec        {"command","cwd","timeout_ms","env"} → {"exit_code","output","timed_out"}
//	GET  /file?path=  file bytes (404 if missing)
//	PUT  /file?path=  body → file (parents created)
//	GET  /list?path=  ls -la listing
//	PUT  /tar?dir=    tar stream extracted into dir
//
// Standard library only, so it builds as a static binary.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxOutput = 32 << 10

// Guest layout; variables so tests can run the handlers on a dev machine.
var (
	defaultWorkdir = "/work/project"
	home           = "/root"
)

func main() {
	cmdline, _ := os.ReadFile("/proc/cmdline")
	params := parseCmdline(string(cmdline))
	token := params["rtfm.token"]
	if token == "" {
		log.Fatal("rtfm-agent: no rtfm.token on the kernel command line")
	}
	if dns := params["rtfm.dns"]; dns != "" {
		var b strings.Builder
		for _, ns := range strings.Split(dns, ",") {
			if ns = strings.TrimSpace(ns); ns != "" {
				fmt.Fprintf(&b, "nameserver %s\n", ns)
			}
		}
		_ = os.Remove("/etc/resolv.conf") // may be a dangling symlink
		if err := os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644); err != nil {
			log.Printf("rtfm-agent: resolv.conf: %v", err)
		}
	}
	_ = os.MkdirAll(defaultWorkdir, 0o755)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("POST /exec", handleExec)
	mux.HandleFunc("GET /file", handleReadFile)
	mux.HandleFunc("PUT /file", handleWriteFile)
	mux.HandleFunc("GET /list", handleList)
	mux.HandleFunc("PUT /tar", handleTar)

	srv := &http.Server{Addr: ":7000", Handler: auth(token, mux), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("rtfm-agent: listening on :7000")
	log.Fatal(srv.ListenAndServe())
}

func parseCmdline(s string) map[string]string {
	m := map[string]string{}
	for _, f := range strings.Fields(s) {
		if k, v, ok := strings.Cut(f, "="); ok {
			m[k] = v
		}
	}
	return m
}

func auth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func resolve(p string) string {
	if p == "" {
		return defaultWorkdir
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(defaultWorkdir, p)
}

type execReq struct {
	Command   string            `json:"command"`
	Cwd       string            `json:"cwd"`
	TimeoutMS int64             `json:"timeout_ms"`
	Env       map[string]string `json:"env"`
}

type execResp struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	TimedOut bool   `json:"timed_out"`
}

// baseEnv is a login-like environment; the login shell adds /etc/profile.
func baseEnv() []string {
	keep := []string{}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "PATH" || k == "LANG" {
			keep = append(keep, kv)
		}
	}
	return append(keep, "HOME="+home, "USER=root", "LOGNAME=root", "SHELL=/bin/bash", "TERM=dumb")
}

func handleExec(w http.ResponseWriter, r *http.Request) {
	var req execReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	cmd := exec.Command("bash", "-lc", req.Command)
	cmd.Dir = resolve(req.Cwd)
	cmd.Env = baseEnv()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Own process group so a timeout kills the whole tree, but background
	// jobs started with nohup/setsid survive normal completion.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf tailBuffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	cmd.WaitDelay = 5 * time.Second

	resp := execResp{}
	if err := cmd.Start(); err != nil {
		resp.ExitCode, resp.Output = 127, err.Error()
		writeJSON(w, resp)
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err = <-done
		resp.TimedOut = true
	}
	resp.Output = buf.String()
	var ee *exec.ExitError
	switch {
	case resp.TimedOut:
		resp.ExitCode = 124
	case errors.As(err, &ee):
		resp.ExitCode = ee.ExitCode()
	case err != nil:
		resp.ExitCode, resp.Output = 1, resp.Output+err.Error()
	}
	writeJSON(w, resp)
}

func handleReadFile(w http.ResponseWriter, r *http.Request) {
	p := resolve(r.URL.Query().Get("path"))
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "read "+p+": No such file or directory", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.IsDir() {
		http.Error(w, "read "+p+": is a directory", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, f)
}

func handleWriteFile(w http.ResponseWriter, r *http.Request) {
	p := resolve(r.URL.Query().Get("path"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm()
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(f, r.Body); err != nil {
		f.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := f.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleList(w http.ResponseWriter, r *http.Request) {
	p := resolve(r.URL.Query().Get("path"))
	out, err := exec.CommandContext(r.Context(), "ls", "-la", "--group-directories-first", "--", p).CombinedOutput()
	if err != nil {
		http.Error(w, strings.TrimSpace(string(out)), http.StatusNotFound)
		return
	}
	_, _ = w.Write(out)
}

// handleTar extracts a tar stream into dir. Entries may not escape dir.
func handleTar(w http.ResponseWriter, r *http.Request) {
	dir := resolve(r.URL.Query().Get("dir"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tr := tar.NewReader(r.Body)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		target := filepath.Join(dir, filepath.Clean("/"+h.Name))
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(os.PathSeparator)) && target != filepath.Clean(dir) {
			http.Error(w, "tar entry escapes dir: "+h.Name, http.StatusBadRequest)
			return
		}
		mode := os.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, mode|0o700)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				var f *os.File
				if f, err = os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode); err == nil {
					_, err = io.Copy(f, tr)
					if cerr := f.Close(); err == nil {
						err = cerr
					}
				}
			}
		case tar.TypeSymlink:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				_ = os.Remove(target)
				err = os.Symlink(h.Linkname, target)
			}
		default:
			continue // skip devices, hardlinks, fifos
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// tailBuffer keeps the last maxOutput bytes written to it. Safe for the
// concurrent stdout/stderr writers exec uses when they are the same buffer.
type tailBuffer struct {
	mu      sync.Mutex
	b       []byte
	dropped bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > maxOutput {
		t.b = append([]byte(nil), t.b[len(t.b)-maxOutput:]...)
		t.dropped = true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := string(bytes.ToValidUTF8(t.b, []byte("?")))
	if t.dropped {
		return "…(truncated)\n" + s
	}
	return s
}
