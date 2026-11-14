// Package runner drives a coding agent through a quickstart.
package runner

import (
	"context"

	"vanguard.run/internal/stream"
)

// Request is one agent run in a prepared workspace.
type Request struct {
	Workdir string
	DocPath string
	Goal    string
	// RawLog, if set, receives the agent's raw output stream (stream-json).
	RawLog string
	// Container, if set, is a running devbox; the agent's shell is replaced
	// by the devbox MCP exec tool and every command runs in the container.
	Container string
	// ExecLog is where the devbox MCP server logs each command (JSONL).
	ExecLog string
}

// Runner runs the newhire agent and returns the parsed stream.
type Runner interface {
	Name() string
	Run(ctx context.Context, req Request) (*stream.Summary, error)
}
