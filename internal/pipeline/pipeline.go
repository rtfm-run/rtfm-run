// Package pipeline runs one target end to end:
// workspace → devbox container → newhire agent → success check → report.
package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"vanguard.run/internal/devbox"
	"vanguard.run/internal/report"
	"vanguard.run/internal/runner"
	"vanguard.run/internal/sandbox"
	"vanguard.run/internal/stream"
	"vanguard.run/internal/targets"
)

// Config controls a pipeline run.
type Config struct {
	Runner runner.Runner
	// Devbox runs every command in a fresh container. Nil means host mode:
	// commands run on this machine under agy's terminal sandbox only.
	Devbox *devbox.Config
	// WorkRoot is where throwaway workspaces are created.
	WorkRoot string
	// OutDir is where run artifacts are written: <OutDir>/<slug>/<id>.*
	OutDir string
	// Keep leaves the workspace and container for inspection.
	Keep bool
	// SuccessTimeout bounds the target's ground-truth success command.
	SuccessTimeout time.Duration
	// Model is recorded in the report when the stream doesn't say.
	Model string
}

// Execute runs one target. A non-nil *report.Run is returned whenever the
// run got far enough to record anything, even alongside an error.
func Execute(ctx context.Context, cfg Config, t targets.Target) (*report.Run, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	slug := t.Slug()
	run := &report.Run{
		ID:      newID(),
		Target:  slug,
		Repo:    t.Repo,
		DocPath: t.DocPath,
		Goal:    t.Goal,
		Runner:  cfg.Runner.Name(),
		Model:   cfg.Model,
		Sandbox: "host",
		Started: time.Now().UTC(),
	}
	if cfg.Devbox != nil {
		img := cfg.Devbox.Image
		if img == "" {
			img = devbox.DefaultImage
		}
		run.Sandbox = "devbox:" + img
	}
	outDir, err := filepath.Abs(filepath.Join(cfg.OutDir, slug))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	finish := func(err error) (*report.Run, error) {
		run.Ended = time.Now().UTC()
		if err != nil && run.Error == "" {
			run.Error = err.Error()
		}
		if serr := save(outDir, run); serr != nil {
			err = errors.Join(err, serr)
		}
		return run, err
	}

	ws, err := sandbox.Prepare(ctx, cfg.WorkRoot, t.Repo, t.Ref, cfg.Keep)
	if err != nil {
		return finish(err)
	}
	defer ws.Cleanup()

	if _, err := os.Stat(filepath.Join(ws.Dir, t.DocPath)); err != nil {
		return finish(fmt.Errorf("doc %s not found in %s", t.DocPath, t.Repo))
	}

	req := runner.Request{
		Workdir: ws.Dir,
		DocPath: t.DocPath,
		Goal:    t.Goal,
		RawLog:  filepath.Join(outDir, run.ID+".jsonl"),
	}

	var box *devbox.Box
	if cfg.Devbox != nil {
		box, err = devbox.Start(ctx, *cfg.Devbox, "vg-"+run.ID, ws.Dir)
		if err != nil {
			return finish(err)
		}
		if !cfg.Keep {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_ = box.Stop(stopCtx)
			}()
		}
		req.Container = box.Name
		req.ExecLog = filepath.Join(outDir, run.ID+".exec.jsonl")
	}

	sum, runErr := cfg.Runner.Run(ctx, req)
	merge(run, sum)
	if box != nil {
		if err := loadExecLog(run, req.ExecLog); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		return finish(runErr)
	}

	if t.Success != "" {
		run.SuccessCheck = successCheck(ctx, box, ws.Dir, t.Success, cfg.SuccessTimeout)
	}
	return finish(nil)
}

func merge(run *report.Run, sum *stream.Summary) {
	if sum == nil {
		return
	}
	run.ConversationID = sum.ConversationID
	if sum.Model != "" {
		run.Model = sum.Model
	}
	run.Commands = sum.Commands
	run.Searches = sum.Searches
	run.URLsRead = sum.URLsRead
	if sum.Result != nil {
		run.Usage = sum.Result.Usage
	}
	if sum.Report != nil {
		run.Succeeded = sum.Report.Succeeded
		run.GaveUpReason = sum.Report.GaveUpReason
		run.Steps = sum.Report.Steps
	}
}

// loadExecLog replaces stream-scraped commands with the devbox MCP server's
// own log: the authoritative record of what ran in the container.
func loadExecLog(run *report.Run, path string) error {
	recs, err := devbox.ReadLog(path)
	if err != nil {
		return fmt.Errorf("read exec log: %w", err)
	}
	cmds := make([]report.Command, 0, len(recs))
	for _, r := range recs {
		cmds = append(cmds, report.Command{Line: r.Command, ExitCode: r.ExitCode, Output: tail(r.Output, 2000)})
	}
	run.Commands = append(run.Commands, cmds...)
	return nil
}

// successCheck runs the target's ground-truth command: in the devbox when
// there is one, otherwise on the host (host mode only; use commands you wrote).
func successCheck(ctx context.Context, box *devbox.Box, dir, line string, timeout time.Duration) *report.Command {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if box != nil {
		res, err := box.Exec(ctx, line, "", timeout)
		if err != nil {
			return &report.Command{Line: line, ExitCode: -1, Output: err.Error()}
		}
		return &report.Command{Line: line, ExitCode: res.ExitCode, Output: tail(strings.TrimSpace(res.Output), 2000)}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", line)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	return &report.Command{Line: line, ExitCode: code, Output: tail(strings.TrimSpace(string(out)), 2000)}
}

func save(dir string, run *report.Run) error {
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, run.ID+".json"), b, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, run.ID+".md"), []byte(run.Markdown()), 0o644)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func newID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}
