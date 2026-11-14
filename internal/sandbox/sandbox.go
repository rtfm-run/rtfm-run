// Package sandbox prepares a fresh, throwaway copy of a target to run in.
//
// Isolation today is a temp directory plus agy's own --sandbox terminal
// restrictions. Container isolation is a later milestone (see PLAN.md §12).
package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ProjectDirName is the subdirectory of a workspace holding the checkout.
// Run scaffolding (agent config, MCP registration) lives next to it in the
// workspace root, so the project tree stays exactly as the docs expect.
const ProjectDirName = "project"

// Workspace is a prepared checkout on the host:
//
//	Root/project  the pristine checkout (copied into the devbox)
//	Root/agent    the agent's own cwd: only its config, never project files
//	Root/*.sock   run plumbing (credential broker)
type Workspace struct {
	Root     string
	Dir      string // Root/project
	AgentDir string // Root/agent
	// Commit is the checked-out commit (git rev-parse HEAD), if a git repo.
	Commit string
	keep   bool
}

// Cleanup removes the workspace unless it was created with keep=true.
func (w *Workspace) Cleanup() error {
	if w == nil || w.keep || w.Root == "" {
		return nil
	}
	return os.RemoveAll(w.Root)
}

// IsRemote reports whether src should be cloned rather than copied.
func IsRemote(src string) bool {
	return strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") ||
		strings.HasPrefix(src, "git@") || strings.HasPrefix(src, "ssh://")
}

// Prepare creates a fresh workspace under root containing src: a shallow git
// clone for remote URLs (at ref, if set), or a copy for local directories.
// githubToken, if set, authenticates clones of https://github.com URLs so
// private repositories work.
func Prepare(ctx context.Context, root, src, ref string, keep bool, githubToken string) (*Workspace, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	wsRoot, err := os.MkdirTemp(root, "ws-")
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	dir := filepath.Join(wsRoot, ProjectDirName)
	ws := &Workspace{Root: wsRoot, Dir: dir, AgentDir: filepath.Join(wsRoot, "agent"), keep: keep}
	fail := func(err error) (*Workspace, error) {
		_ = os.RemoveAll(wsRoot)
		return nil, err
	}
	if err := os.MkdirAll(ws.AgentDir, 0o755); err != nil {
		return fail(fmt.Errorf("sandbox: %w", err))
	}

	if IsRemote(src) {
		args := []string{"clone", "--depth", "1", "--quiet"}
		if ref != "" {
			args = append(args, "--branch", ref)
		}
		args = append(args, "--", src, dir)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		cmd.Env = append(cmd.Env, gitAuthEnv(src, githubToken)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fail(fmt.Errorf("sandbox: git clone %s: %v: %s", src, err, strings.TrimSpace(string(out))))
		}
		ws.Commit = headCommit(ctx, dir)
		return ws, nil
	}

	abs, err := filepath.Abs(src)
	if err != nil {
		return fail(fmt.Errorf("sandbox: %w", err))
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("sandbox: %s is not a directory or remote URL", src))
	}
	if err := os.CopyFS(dir, os.DirFS(abs)); err != nil {
		return fail(fmt.Errorf("sandbox: copy %s: %w", src, err))
	}
	ws.Commit = headCommit(ctx, dir)
	return ws, nil
}

// gitAuthEnv passes a GitHub token to git as an HTTP header through git's
// environment-variable config (GIT_CONFIG_*), scoped to github.com. The token
// never appears on a command line or in the clone's .git/config.
func gitAuthEnv(src, token string) []string {
	if token == "" || !strings.HasPrefix(src, "https://github.com/") {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + basic,
	}
}

// headCommit returns the commit checked out in dir, or "" if not a git repo.
func headCommit(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
