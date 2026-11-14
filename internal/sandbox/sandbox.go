// Package sandbox prepares a fresh, throwaway copy of a target to run in.
//
// Isolation today is a temp directory plus agy's own --sandbox terminal
// restrictions. Container isolation is a later milestone (see PLAN.md §12).
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Workspace is a prepared checkout.
type Workspace struct {
	Dir  string
	keep bool
}

// Cleanup removes the workspace unless it was created with keep=true.
func (w *Workspace) Cleanup() error {
	if w == nil || w.keep || w.Dir == "" {
		return nil
	}
	return os.RemoveAll(w.Dir)
}

// IsRemote reports whether src should be cloned rather than copied.
func IsRemote(src string) bool {
	return strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") ||
		strings.HasPrefix(src, "git@") || strings.HasPrefix(src, "ssh://")
}

// Prepare creates a fresh workspace under root containing src: a shallow git
// clone for remote URLs (at ref, if set), or a copy for local directories.
func Prepare(ctx context.Context, root, src, ref string, keep bool) (*Workspace, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	dir, err := os.MkdirTemp(root, "ws-")
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	ws := &Workspace{Dir: dir, keep: keep}

	if IsRemote(src) {
		args := []string{"clone", "--depth", "1", "--quiet"}
		if ref != "" {
			args = append(args, "--branch", ref)
		}
		args = append(args, "--", src, dir)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("sandbox: git clone %s: %v: %s", src, err, strings.TrimSpace(string(out)))
		}
		return ws, nil
	}

	abs, err := filepath.Abs(src)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("sandbox: %s is not a directory or remote URL", src)
	}
	// MkdirTemp created dir; CopyFS needs the destination tree to not clash,
	// which holds for an empty directory.
	if err := os.CopyFS(dir, os.DirFS(abs)); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("sandbox: copy %s: %w", src, err)
	}
	return ws, nil
}
