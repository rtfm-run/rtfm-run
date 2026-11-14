// Package devbox provides blank-slate developer machines ("boxes") that
// quickstarts run in. A box is a fresh, isolated Linux machine with a
// standard toolchain and nothing project-specific.
//
// The agent never touches the host: the project is copied into the box,
// and every command and file operation goes through the box (via the
// devbox MCP server). That keeps the backend swappable: a Docker container
// today, a microVM or a remote sandbox later.
package devbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"time"
)

// DefaultWorkdir is where the project is checked out inside a box.
const DefaultWorkdir = "/work/project"

// Box is a running developer machine.
type Box interface {
	// Backend names the implementation ("docker").
	Backend() string
	// ID is the backend-specific handle, used to Attach from another process.
	ID() string
	// Workdir is the project directory inside the box.
	Workdir() string

	// Exec runs command with bash -lc in cwd (default Workdir) with env
	// added to its environment. Exit code 124 means the timeout was hit.
	Exec(ctx context.Context, command, cwd string, timeout time.Duration, env map[string]string) (Result, error)
	// ReadFile returns a file's contents. Relative paths are under Workdir.
	ReadFile(ctx context.Context, p string) ([]byte, error)
	// WriteFile creates or replaces a file, creating parent directories.
	WriteFile(ctx context.Context, p string, data []byte) error
	// ListDir returns a human-readable listing of a directory.
	ListDir(ctx context.Context, p string) (string, error)
	// CopyIn copies a host directory's contents to boxDir.
	CopyIn(ctx context.Context, hostDir, boxDir string) error

	// Stop destroys the box. Safe to call more than once.
	Stop(ctx context.Context) error
}

// Backend starts boxes.
type Backend interface {
	Name() string
	// Describe identifies the environment for reports, e.g. "docker:rtfm-devbox:latest".
	Describe() string
	// Start launches a fresh box called name.
	Start(ctx context.Context, name string) (Box, error)
	// Ready reports whether the backend can start boxes (e.g. image built).
	Ready(ctx context.Context) error
}

// Attach returns a handle to a running box from another process (the
// devbox MCP server, launched by the agent).
func Attach(backend, id, workdir, dockerBin string) (Box, error) {
	if workdir == "" {
		workdir = DefaultWorkdir
	}
	switch backend {
	case "docker", "":
		return &dockerBox{name: id, workdir: workdir, bin: orDefault(dockerBin, "docker")}, nil
	case "firecracker":
		return attachVM(context.Background(), orDefault(dockerBin, "docker"), id, workdir)
	default:
		return nil, fmt.Errorf("devbox: unknown backend %q", backend)
	}
}

// Resolve makes p absolute inside a box: relative paths are under workdir.
func Resolve(workdir, p string) string {
	if p == "" {
		return workdir
	}
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(workdir, p)
}

// Result is the outcome of one command.
type Result struct {
	ExitCode int
	Output   string // combined stdout+stderr, tail-truncated
	TimedOut bool
}

// MaxOutput caps the output kept per command.
const MaxOutput = 32 << 10

// ExecRecord is one logged operation, written as JSONL by the MCP server.
type ExecRecord struct {
	Time time.Time `json:"time"`
	// Kind is "exec" (default), a file operation ("write_file",
	// "edit_file"), or "progress" (an update the agent posted for the person
	// watching; Command holds the message).
	Kind       string `json:"kind,omitempty"`
	Command    string `json:"command"` // the command, or the file path for file ops
	Cwd        string `json:"cwd,omitempty"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"`
}

// ReadLog loads an exec log. A missing file yields no records.
func ReadLog(p string) ([]ExecRecord, error) {
	b, err := os.ReadFile(p)
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

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
