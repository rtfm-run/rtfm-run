// Package devbox runs quickstart commands inside a fresh Docker container:
// a blank-slate developer machine with a standard toolchain and nothing
// project-specific.
//
// The workspace (a fresh clone on the host) is bind-mounted into the
// container at the same absolute path, so the agent's file tools on the host
// and shell commands in the container see the same files. With rootless
// Docker, root in the container maps to the invoking user on the host.
package devbox

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

//go:embed Dockerfile
var dockerfile []byte

// DefaultImage is the devbox image tag built by Build.
const DefaultImage = "vanguard-devbox:latest"

// Config controls container creation.
type Config struct {
	Image     string // default DefaultImage
	Memory    string // e.g. "4g"
	CPUs      string // e.g. "2"
	PidsLimit int    // e.g. 1024
	Docker    string // docker binary, default "docker"
}

func (c Config) docker() string {
	if c.Docker == "" {
		return "docker"
	}
	return c.Docker
}

func (c Config) image() string {
	if c.Image == "" {
		return DefaultImage
	}
	return c.Image
}

// Build builds the devbox image from the embedded Dockerfile.
func Build(ctx context.Context, cfg Config, out *os.File) error {
	cmd := exec.CommandContext(ctx, cfg.docker(), "build", "-t", cfg.image(), "-")
	cmd.Stdin = bytes.NewReader(dockerfile)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("devbox: docker build: %w", err)
	}
	return nil
}

// ImageExists reports whether the devbox image is present locally.
func ImageExists(ctx context.Context, cfg Config) bool {
	return exec.CommandContext(ctx, cfg.docker(), "image", "inspect", cfg.image()).Run() == nil
}

// Box is a running devbox container.
type Box struct {
	Name    string
	Workdir string
	docker  string
}

// Start launches a container named name with workdir bind-mounted at the
// same path. The container has normal outbound network access (quickstarts
// need to download things) and resource limits.
func Start(ctx context.Context, cfg Config, name, workdir string) (*Box, error) {
	args := []string{"run", "-d",
		"--name", name,
		"--hostname", "devbox",
		"--label", "vanguard.run=devbox",
		"--security-opt", "no-new-privileges",
		"-v", workdir + ":" + workdir,
		"-w", workdir,
	}
	if cfg.Memory != "" {
		args = append(args, "--memory", cfg.Memory)
	}
	if cfg.CPUs != "" {
		args = append(args, "--cpus", cfg.CPUs)
	}
	if cfg.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprint(cfg.PidsLimit))
	}
	args = append(args, cfg.image(), "sleep", "infinity")
	out, err := exec.CommandContext(ctx, cfg.docker(), args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("devbox: docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return &Box{Name: name, Workdir: workdir, docker: cfg.docker()}, nil
}

// Attach returns a handle to an already-running container (used by the MCP
// server process, which doesn't start the container itself).
func Attach(name, workdir, docker string) *Box {
	if docker == "" {
		docker = "docker"
	}
	return &Box{Name: name, Workdir: workdir, docker: docker}
}

// Stop removes the container. Safe to call more than once.
func (b *Box) Stop(ctx context.Context) error {
	if b == nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, b.docker, "rm", "-f", b.Name).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("devbox: docker rm: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Result is the outcome of one command.
type Result struct {
	ExitCode int
	Output   string // combined stdout+stderr, tail-truncated
	TimedOut bool
}

// MaxOutput caps the output kept per command.
const MaxOutput = 32 << 10

// Exec runs command with bash -lc in the container, in cwd (default: the
// workspace). Exit code 124 means the timeout was hit.
func (b *Box) Exec(ctx context.Context, command, cwd string, timeout time.Duration) (Result, error) {
	if cwd == "" {
		cwd = b.Workdir
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.docker, "exec", "-w", cwd, b.Name, "bash", "-lc", command)
	cmd.WaitDelay = 5 * time.Second
	var buf tailBuffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	res := Result{Output: buf.String()}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.ExitCode, res.TimedOut = 124, true
		return res, nil
	case err == nil:
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, fmt.Errorf("devbox: docker exec: %w", err)
}

// ExecRecord is one logged command, written as JSONL by the MCP server.
type ExecRecord struct {
	Time       time.Time `json:"time"`
	Command    string    `json:"command"`
	Cwd        string    `json:"cwd,omitempty"`
	ExitCode   int       `json:"exit_code"`
	TimedOut   bool      `json:"timed_out,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Output     string    `json:"output,omitempty"`
}

// ReadLog loads an exec log. A missing file yields no records.
func ReadLog(path string) ([]ExecRecord, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []ExecRecord
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r ExecRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// tailBuffer keeps the last MaxOutput bytes written to it.
type tailBuffer struct {
	b       []byte
	dropped bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > MaxOutput {
		t.b = append([]byte(nil), t.b[len(t.b)-MaxOutput:]...)
		t.dropped = true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	if t.dropped {
		return "…(truncated)\n" + string(t.b)
	}
	return string(t.b)
}
