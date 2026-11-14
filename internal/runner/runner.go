// Package runner drives a coding agent through a quickstart.
package runner

import (
	"context"

	"vanguard.run/internal/stream"
)

// Request is one agent run in a prepared workspace.
type Request struct {
	// Workdir is the workspace root: the agent's cwd.
	Workdir string
	// ProjectDir is the checkout inside Workdir (Workdir/project).
	ProjectDir string
	// DocPath is the quickstart doc relative to ProjectDir.
	DocPath string
	Goal    string
	// RawLog, if set, receives the agent's raw output stream (stream-json).
	RawLog string
	// BoxID, if set, is a running devbox (of backend BoxBackend). The
	// agent's shell and file tools are replaced by the devbox MCP tools, and
	// the project lives at BoxWorkdir inside the box; nothing touches the host.
	BoxBackend string
	BoxID      string
	BoxWorkdir string
	// ExecLog is where the devbox MCP server logs each command (JSONL).
	ExecLog string
	// BrokerSocket, if set, enables the request_credential tool: the MCP
	// server asks vanguard's credential broker on this unix socket.
	BrokerSocket string
	// NoTools denies every agent tool: for pure text tasks (e.g. applying
	// contribution guidelines) that must not touch anything.
	NoTools bool
}

// Runner runs agents and returns the parsed stream.
type Runner interface {
	Name() string
	// Run runs the newhire persona on req's quickstart.
	Run(ctx context.Context, req Request) (*stream.Summary, error)
	// RunPrompt runs any prompt with the same isolation; schema constrains
	// the final answer (read it from Summary.Result.StructuredOutput).
	RunPrompt(ctx context.Context, req Request, prompt, schema string) (*stream.Summary, error)
}
