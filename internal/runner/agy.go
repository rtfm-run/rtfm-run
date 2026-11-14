package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"vanguard.run/internal/mcpexec"
	"vanguard.run/internal/persona"
	"vanguard.run/internal/stream"
)

// Agy runs the newhire persona with Antigravity CLI in headless mode:
//
//	agy -p <prompt> --output-format stream-json --json-schema <schema> \
//	    --sandbox --mode accept-edits --settings <permissions> --model <m>
//
// With a devbox (the default), agy's own shell tool is denied and the agent
// gets a single MCP tool, devbox/exec, served by this binary, that runs
// commands with `docker exec` in the run's container. Nothing executes on the
// host. --mode accept-edits keeps headless runs out of Auto mode, whose
// reviewer can approve commands to run outside the sandbox.
//
// We never pass --dangerously-skip-permissions.
type Agy struct {
	Bin     string        // default "agy"
	Model   string        // e.g. gemini-3.8-flash-medium; empty = agy default
	Effort  string        // low|medium|high|xhigh|max; empty = default
	Timeout time.Duration // agy --print-timeout; 0 = none
	// SelfBin is this vanguard binary, launched by agy as the devbox MCP
	// server. Defaults to os.Executable().
	SelfBin string
	Docker  string   // docker binary passed to the MCP server
	Extra   []string // extra flags, appended last
}

func (a Agy) Name() string { return "agy" }

func (a Agy) Run(ctx context.Context, req Request) (*stream.Summary, error) {
	devbox := req.Container != ""
	prompt, err := persona.Render(persona.Input{DocPath: req.DocPath, Goal: req.Goal, Devbox: devbox})
	if err != nil {
		return nil, err
	}
	bin := a.Bin
	if bin == "" {
		bin = "agy"
	}
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--json-schema", persona.Schema(),
		"--sandbox",
		"--mode", "accept-edits",
	}
	if devbox {
		if err := a.installMCP(req); err != nil {
			return nil, err
		}
		settings, err := devboxSettings()
		if err != nil {
			return nil, err
		}
		args = append(args, "--settings", settings)
	}
	if a.Timeout > 0 {
		args = append(args, "--print-timeout", a.Timeout.String())
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	if a.Effort != "" {
		args = append(args, "--effort", a.Effort)
	}
	args = append(args, a.Extra...)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = req.Workdir
	cmd.WaitDelay = 10 * time.Second

	var stdout, stderr bytes.Buffer
	var out io.Writer = &stdout
	if req.RawLog != "" {
		f, err := os.Create(req.RawLog)
		if err != nil {
			return nil, fmt.Errorf("agy: raw log: %w", err)
		}
		defer f.Close()
		out = io.MultiWriter(&stdout, f)
	}
	cmd.Stdout = out
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	sum, parseErr := stream.Parse(&stdout)
	if sum != nil {
		sum.Stderr = strings.TrimSpace(stderr.String())
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			return sum, fmt.Errorf("agy: %w", ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return sum, fmt.Errorf("agy exited %d: %s", ee.ExitCode(), msg)
		}
		return sum, fmt.Errorf("agy: %w", runErr)
	}
	if parseErr != nil {
		return sum, parseErr
	}
	if sum.Result == nil {
		return sum, fmt.Errorf("agy: stream ended without a result event: %s", strings.TrimSpace(stderr.String()))
	}
	if sum.Result.Status != "SUCCESS" {
		return sum, fmt.Errorf("agy: result status %s", sum.Result.Status)
	}
	return sum, nil
}

// devboxSettings are per-session agy settings (never saved): deny every
// native shell command and allow only the devbox MCP server.
func devboxSettings() (string, error) {
	b, err := json.Marshal(map[string]any{
		"permissions": map[string]any{
			"deny":  []string{"command(*)"},
			"allow": []string{"mcp(" + mcpexec.ServerName + "/*)"},
		},
	})
	return string(b), err
}

// installMCP registers the devbox MCP server for this workspace via
// <workspace>/.agents/mcp_config.json (agy has no per-session MCP flag) and
// hides it from git so the checkout still looks pristine.
func (a Agy) installMCP(req Request) error {
	self := a.SelfBin
	if self == "" {
		var err error
		if self, err = os.Executable(); err != nil {
			return fmt.Errorf("agy: locate vanguard binary: %w", err)
		}
	}
	mcpArgs := []string{"devbox-mcp", "--container", req.Container, "--workdir", req.Workdir}
	if req.ExecLog != "" {
		mcpArgs = append(mcpArgs, "--log", req.ExecLog)
	}
	if a.Docker != "" {
		mcpArgs = append(mcpArgs, "--docker", a.Docker)
	}
	cfg := map[string]any{"mcpServers": map[string]any{
		mcpexec.ServerName: map[string]any{"command": self, "args": mcpArgs},
	}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(req.Workdir, ".agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp_config.json"), b, 0o644); err != nil {
		return err
	}
	if info := filepath.Join(req.Workdir, ".git", "info"); dirExists(filepath.Join(req.Workdir, ".git")) {
		if err := os.MkdirAll(info, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(info, "exclude"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.WriteString("\n/.agents/\n"); err != nil {
			return err
		}
	}
	return nil
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
