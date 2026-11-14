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

// Run runs the newhire persona.
func (a Agy) Run(ctx context.Context, req Request) (*stream.Summary, error) {
	prompt, err := persona.Render(persona.Input{Project: ProjectRel(req), DocPath: req.DocPath, Goal: req.Goal,
		Devbox: req.BoxID != "", Credentials: req.BoxID != "" && req.BrokerSocket != ""})
	if err != nil {
		return nil, err
	}
	return a.RunPrompt(ctx, req, prompt, persona.Schema())
}

// ProjectRel is where the agent finds the project: the path inside the box
// in devbox mode, otherwise the checkout relative to the agent's cwd.
func ProjectRel(req Request) string {
	if req.BoxID != "" {
		return req.BoxWorkdir
	}
	if req.ProjectDir == "" {
		return "."
	}
	rel, err := filepath.Rel(req.Workdir, req.ProjectDir)
	if err != nil {
		return "."
	}
	return rel
}

// RunPrompt runs an arbitrary prompt with the same isolation (devbox MCP,
// denied native shell, accept-edits mode) and a JSON schema for the final
// answer.
func (a Agy) RunPrompt(ctx context.Context, req Request, prompt, schema string) (*stream.Summary, error) {
	devbox := req.BoxID != ""
	bin := a.Bin
	if bin == "" {
		bin = "agy"
	}
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--json-schema", schema,
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

// devboxSettings are per-session agy settings (never saved): deny agy's
// native shell and file tools and allow only the devbox MCP server, so every
// action happens inside the box.
func devboxSettings() (string, error) {
	b, err := json.Marshal(map[string]any{
		"permissions": map[string]any{
			"deny":  []string{"command(*)", "read_file(*)", "write_file(*)"},
			"allow": []string{"mcp(" + mcpexec.ServerName + "/*)"},
		},
	})
	return string(b), err
}

// installMCP registers the devbox MCP server for this run via
// <agent cwd>/.agents/mcp_config.json (agy has no per-session MCP flag). The
// agent's cwd holds nothing else; the project lives in the box.
func (a Agy) installMCP(req Request) error {
	self := a.SelfBin
	if self == "" {
		var err error
		if self, err = os.Executable(); err != nil {
			return fmt.Errorf("agy: locate vanguard binary: %w", err)
		}
	}
	mcpArgs := []string{"devbox-mcp", "--backend", req.BoxBackend, "--box", req.BoxID, "--workdir", req.BoxWorkdir}
	if req.ExecLog != "" {
		mcpArgs = append(mcpArgs, "--log", req.ExecLog)
	}
	if a.Docker != "" {
		mcpArgs = append(mcpArgs, "--docker", a.Docker)
	}
	if req.BrokerSocket != "" {
		mcpArgs = append(mcpArgs, "--broker", req.BrokerSocket)
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
	return os.WriteFile(filepath.Join(dir, "mcp_config.json"), b, 0o644)
}
