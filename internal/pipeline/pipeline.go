// Package pipeline runs one target end to end:
//
//	newhire run   fresh workspace + devbox → agent follows the doc → report
//	fix           fresh workspace + devbox → evidence substitutions + fixer
//	              agent edits the doc → patch
//	verify        fresh workspace + devbox → newhire follows the FIXED doc
//
// Every stage gets its own throwaway checkout and container.
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

	"vanguard.run/internal/creds"
	"vanguard.run/internal/devbox"
	"vanguard.run/internal/diagnose"
	"vanguard.run/internal/fix"
	"vanguard.run/internal/persona"
	"vanguard.run/internal/report"
	"vanguard.run/internal/runner"
	"vanguard.run/internal/sandbox"
	"vanguard.run/internal/stream"
	"vanguard.run/internal/targets"
)

// Config controls a pipeline run.
type Config struct {
	Runner runner.Runner
	// Backend provides a fresh devbox per stage; the project is copied into
	// it and the agent works only there. Nil means host mode: the agent works
	// in a host checkout under agy's terminal sandbox only.
	Backend devbox.Backend
	// WorkRoot is where throwaway workspaces are created.
	WorkRoot string
	// OutDir is where run artifacts are written: <OutDir>/<slug>/<id>.*
	OutDir string
	// Keep leaves workspaces and containers for inspection.
	Keep bool
	// SuccessTimeout bounds the target's ground-truth success command.
	SuccessTimeout time.Duration
	// Model is recorded in the report when the stream doesn't say.
	Model string
	// Fix proposes a doc fix when the newhire hit issues.
	Fix bool
	// Verify reruns the newhire against the fixed doc (requires Fix).
	Verify bool
	// Broker answers the agent's credential requests (devbox mode only).
	// Nil disables the request_credential tool.
	Broker *creds.Broker
}

// secrets accumulates credential values handed out while processing one
// target, so every artifact can be redacted. Values are never serialized.
type secrets map[string]string

func (s secrets) add(m map[string]string) {
	for k, v := range m {
		s[k] = v
	}
}

// Execute runs one target, then fixes and verifies if configured. A non-nil
// *report.Run is returned whenever the run got far enough to record
// anything, even alongside an error.
func Execute(ctx context.Context, cfg Config, t targets.Target) (*report.Run, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	outDir, err := filepath.Abs(filepath.Join(cfg.OutDir, t.Slug()))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	sec := secrets{}
	run, err := newhire(ctx, cfg, t, outDir, nil, true, sec)
	// Blocked runs are fixed too: the doc may still be missing install or
	// setup steps; verification then reports whether it is still blocked.
	if err != nil || !cfg.Fix || run.Issues() == 0 {
		return run, err
	}
	run.Fix = fixAndVerify(ctx, cfg, t, run, outDir, sec)
	return run, save(outDir, run, true, sec)
}

// env is a prepared stage: a fresh checkout plus (optionally) a devbox and
// a credential broker session.
type env struct {
	ws   *sandbox.Workspace
	box  devbox.Box
	sess *creds.Session
	req  runner.Request
}

// finish collects the stage's credential values and redacts its raw agent
// log (the exec log is redacted by the MCP server as it writes).
func (e *env) finish(sec secrets) {
	if e.sess == nil {
		return
	}
	sec.add(e.sess.Values())
	_ = creds.RedactFile(e.req.RawLog, sec)
	_ = creds.RedactFile(e.req.ExecLog, sec)
}

// prepare creates a fresh workspace and devbox. logPrefix names the stage's
// artifacts in outDir. The returned close func tears everything down.
func prepare(ctx context.Context, cfg Config, t targets.Target, containerName, outDir, logPrefix string) (*env, func(), error) {
	ws, err := sandbox.Prepare(ctx, cfg.WorkRoot, t.Repo, t.Ref, cfg.Keep)
	if err != nil {
		return nil, func() {}, err
	}
	e := &env{ws: ws, req: runner.Request{
		Workdir:    ws.Dir, // host mode; replaced by the agent dir with a devbox
		ProjectDir: ws.Dir,
		DocPath:    t.DocPath,
		Goal:       t.Goal,
		RawLog:     filepath.Join(outDir, logPrefix+".jsonl"),
	}}
	closeFn := func() {
		if e.sess != nil {
			_ = e.sess.Close()
		}
		if e.box != nil && !cfg.Keep {
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = e.box.Stop(stopCtx)
		}
		_ = ws.Cleanup()
	}
	if _, err := os.Stat(filepath.Join(ws.Dir, t.DocPath)); err != nil {
		closeFn()
		return nil, func() {}, fmt.Errorf("doc %s not found in %s", t.DocPath, t.Repo)
	}
	if cfg.Backend != nil {
		box, err := cfg.Backend.Start(ctx, containerName)
		if err != nil {
			closeFn()
			return nil, func() {}, err
		}
		e.box = box
		if err := box.CopyIn(ctx, ws.Dir, box.Workdir()); err != nil {
			closeFn()
			return nil, func() {}, err
		}
		e.req.Workdir = ws.AgentDir
		e.req.BoxBackend = box.Backend()
		e.req.BoxID = box.ID()
		e.req.BoxWorkdir = box.Workdir()
		e.req.ExecLog = filepath.Join(outDir, logPrefix+".exec.jsonl")
		if cfg.Broker != nil {
			// The socket lives in the host workspace root, outside the box.
			sock := filepath.Join(ws.Root, "creds.sock")
			if len(sock) > 100 { // unix socket paths are limited to ~108 bytes
				sock = filepath.Join(os.TempDir(), "vg-"+filepath.Base(ws.Root)+".sock")
			}
			sess, err := cfg.Broker.Listen(ctx, t.Slug(), sock)
			if err != nil {
				closeFn()
				return nil, func() {}, err
			}
			e.sess = sess
			e.req.BrokerSocket = sess.Path()
		}
	}
	return e, closeFn, nil
}

// readDoc returns the quickstart doc as the agent sees it (in the box, or
// the host checkout in host mode).
func (e *env) readDoc(ctx context.Context, doc string) ([]byte, error) {
	if e.box != nil {
		return e.box.ReadFile(ctx, doc)
	}
	return os.ReadFile(filepath.Join(e.ws.Dir, doc))
}

// writeDoc replaces the quickstart doc where the agent will see it.
func (e *env) writeDoc(ctx context.Context, doc string, data []byte) error {
	if e.box != nil {
		return e.box.WriteFile(ctx, doc, data)
	}
	return os.WriteFile(filepath.Join(e.ws.Dir, doc), data, 0o644)
}

// newhire runs the newhire agent once. docOverride, if non-nil, replaces the
// quickstart doc in the fresh checkout (used to verify a fix). primary runs
// also update latest.json.
func newhire(ctx context.Context, cfg Config, t targets.Target, outDir string, docOverride []byte, primary bool, sec secrets) (*report.Run, error) {
	run := &report.Run{
		ID:      newID(),
		Target:  t.Slug(),
		Repo:    t.Repo,
		DocPath: t.DocPath,
		Goal:    t.Goal,
		Runner:  cfg.Runner.Name(),
		Model:   cfg.Model,
		Sandbox: "host",
		Started: time.Now().UTC(),
	}
	if cfg.Backend != nil {
		run.Sandbox = "devbox:" + cfg.Backend.Describe()
	}
	finish := func(err error) (*report.Run, error) {
		run.Ended = time.Now().UTC()
		diagnose.Apply(run)
		if err != nil && run.Error == "" {
			run.Error = err.Error()
		}
		if serr := save(outDir, run, primary, sec); serr != nil {
			err = errors.Join(err, serr)
		}
		return run, err
	}

	e, closeEnv, err := prepare(ctx, cfg, t, "vg-"+run.ID, outDir, run.ID)
	if err != nil {
		return finish(err)
	}
	defer closeEnv()
	if docOverride != nil {
		if err := e.writeDoc(ctx, t.DocPath, docOverride); err != nil {
			return finish(err)
		}
	}

	sum, runErr := cfg.Runner.Run(ctx, e.req)
	e.finish(sec)
	merge(run, sum)
	if e.box != nil {
		if err := loadExecLog(run, e.req.ExecLog); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if e.sess != nil {
		run.Credentials = credentialRecords(e.sess.Records())
	}
	if runErr != nil {
		return finish(runErr)
	}
	if t.Success != "" {
		var env map[string]string
		if e.sess != nil {
			env = e.sess.Values()
		}
		run.SuccessCheck = successCheck(ctx, e.box, e.ws.Dir, t.Success, cfg.SuccessTimeout, env)
	}
	return finish(nil)
}

type fixerReport struct {
	Changes []struct {
		StepIndex int    `json:"step_index"`
		Summary   string `json:"summary"`
		Evidence  string `json:"evidence"`
	} `json:"changes"`
	Unresolved []report.Unresolved `json:"unresolved"`
}

// fixAndVerify proposes a doc fix for run's issues in a fresh workspace,
// then proves it by rerunning the newhire against the fixed doc.
func fixAndVerify(ctx context.Context, cfg Config, t targets.Target, run *report.Run, outDir string, sec secrets) *report.FixResult {
	fr := &report.FixResult{DocPath: t.DocPath, IssuesBefore: run.DocIssues()}
	fail := func(err error) *report.FixResult {
		fr.Status, fr.Error = report.FixError, err.Error()
		return fr
	}

	e, closeEnv, err := prepare(ctx, cfg, t, "vg-"+run.ID+"-fix", outDir, run.ID+".fix")
	if err != nil {
		return fail(err)
	}
	origB, err := e.readDoc(ctx, t.DocPath)
	if err != nil {
		closeEnv()
		return fail(err)
	}
	orig := string(origB)

	// 1. Evidence-backed substitutions: the command that actually worked.
	text, applied := fix.FromRun(run, orig)
	if text != orig {
		if err := e.writeDoc(ctx, t.DocPath, []byte(text)); err != nil {
			closeEnv()
			return fail(err)
		}
	}

	// 2. The fixer agent investigates the rest and edits the doc.
	pa := make([]persona.AppliedFix, len(applied))
	for i, a := range applied {
		pa[i] = persona.AppliedFix{StepIndex: a.StepIndex, Original: a.Original, Proposed: a.Proposed}
	}
	prompt, err := persona.RenderFixer(persona.FixerInput{
		Project:     runner.ProjectRel(e.req),
		DocPath:     t.DocPath,
		Problems:    fix.Problems(run),
		Applied:     pa,
		Devbox:      e.box != nil,
		Credentials: e.sess != nil,
	})
	if err != nil {
		closeEnv()
		return fail(err)
	}
	sum, fixErr := cfg.Runner.RunPrompt(ctx, e.req, prompt, persona.FixerSchema())
	e.finish(sec)
	finalB, readErr := e.readDoc(ctx, t.DocPath) // only the doc is taken; other edits are ignored
	closeEnv()
	if readErr != nil {
		return fail(readErr)
	}
	final := string(finalB)

	var rep fixerReport
	if sum != nil {
		fr.ConversationID = sum.ConversationID
		if sum.Result != nil {
			addUsage(&fr.Usage, sum.Result.Usage)
			if len(sum.Result.StructuredOutput) > 0 {
				_ = json.Unmarshal(sum.Result.StructuredOutput, &rep)
			}
		}
	}
	covered := map[int]bool{}
	for _, c := range rep.Changes {
		covered[c.StepIndex] = true
		fr.Changes = append(fr.Changes, report.Change{StepIndex: c.StepIndex, Summary: c.Summary, Evidence: c.Evidence, Source: "fixer"})
	}
	for _, a := range applied {
		if !covered[a.StepIndex] && strings.Contains(final, a.Proposed) {
			fr.Changes = append(fr.Changes, report.Change{StepIndex: a.StepIndex, Source: "run",
				Summary:  fmt.Sprintf("Replace `%s` with `%s`.", a.Original, a.Proposed),
				Evidence: "The newcomer ran this command successfully in a fresh devbox."})
		}
	}
	fr.Unresolved = rep.Unresolved
	if fixErr != nil {
		fr.Error = "fixer: " + fixErr.Error()
	}
	if final == orig {
		fr.Changes = nil // anything changed along the way was reverted
		fr.Status = report.FixNoChange
		if fixErr != nil {
			fr.Status = report.FixError
		}
		return fr
	}

	patch, err := fix.UnifiedDiff(ctx, t.DocPath, orig, final)
	if err != nil {
		return fail(err)
	}
	fr.Patch = creds.Redact(patch, sec)
	patch = fr.Patch
	if err := os.WriteFile(filepath.Join(outDir, run.ID+".patch"), []byte(patch), 0o644); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, run.ID+".fixed"+filepath.Ext(t.DocPath)), []byte(creds.Redact(final, sec)), 0o644); err != nil {
		return fail(err)
	}
	if !cfg.Verify {
		fr.Status = report.FixUnverified
		return fr
	}

	// 3. Verify: a fresh newcomer follows the fixed doc.
	vrun, verr := newhire(ctx, cfg, t, outDir, finalB, false, sec)
	if vrun != nil {
		fr.VerifyRunID = vrun.ID
		fr.IssuesAfter = vrun.DocIssues()
		fr.BlockedAfter = vrun.CredentialBlockedSteps()
		fr.BlockedOn = vrun.MissingCredentials()
		addUsage(&fr.Usage, vrun.Usage)
	}
	switch {
	case verr != nil:
		fr.Status = report.FixError
		fr.Error = strings.TrimPrefix(fr.Error+"; ", "; ") + "verify: " + verr.Error()
	case vrun.Blocked():
		fr.Status = report.FixBlocked
	case vrun.Works() && vrun.Issues() == 0:
		fr.Status = report.FixVerified
	case fr.IssuesAfter < fr.IssuesBefore || (vrun.Works() && !run.Works()):
		fr.Status = report.FixImproved
	default:
		fr.Status = report.FixNotFixed
	}
	return fr
}

func addUsage(a *report.Usage, b report.Usage) {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.ThinkingTokens += b.ThinkingTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.TotalTokens += b.TotalTokens
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

// loadExecLog appends the devbox MCP server's own log: the authoritative
// record of what ran in the container.
func loadExecLog(run *report.Run, path string) error {
	recs, err := devbox.ReadLog(path)
	if err != nil {
		return fmt.Errorf("read exec log: %w", err)
	}
	for _, r := range recs {
		if r.Kind != "" && r.Kind != "exec" {
			continue // file operations are logged too, but aren't commands
		}
		run.Commands = append(run.Commands, report.Command{Line: r.Command, ExitCode: r.ExitCode, Output: tail(r.Output, 2000)})
	}
	return nil
}

// successCheck runs the target's ground-truth command: in the devbox when
// there is one, otherwise on the host (host mode only; use commands you wrote).
func successCheck(ctx context.Context, box devbox.Box, dir, line string, timeout time.Duration, env map[string]string) *report.Command {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if box != nil {
		res, err := box.Exec(ctx, line, "", timeout, env)
		if err != nil {
			return &report.Command{Line: line, ExitCode: -1, Output: creds.Redact(err.Error(), env)}
		}
		return &report.Command{Line: line, ExitCode: res.ExitCode, Output: tail(strings.TrimSpace(creds.Redact(res.Output, env)), 2000)}
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

// save writes run artifacts with any handed-out credential values redacted.
func save(dir string, run *report.Run, primary bool, sec secrets) error {
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	b := []byte(creds.Redact(string(raw), sec))
	if err := os.WriteFile(filepath.Join(dir, run.ID+".json"), b, 0o644); err != nil {
		return err
	}
	if primary {
		if err := os.WriteFile(filepath.Join(dir, "latest.json"), b, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, run.ID+".md"), []byte(creds.Redact(run.Markdown(), sec)), 0o644)
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

// credentialRecords converts broker records for a report, one per name: the
// last outcome wins, keeping the most informative description and URL.
func credentialRecords(recs []creds.Record) []report.Credential {
	var out []report.Credential
	idx := map[string]int{}
	for _, r := range recs {
		c := report.Credential{Name: r.Name, Description: r.Description, URL: r.URL, Status: r.Status, Source: r.Source}
		i, seen := idx[r.Name]
		if !seen {
			idx[r.Name] = len(out)
			out = append(out, c)
			continue
		}
		prev := out[i]
		if c.Description == "" {
			c.Description = prev.Description
		}
		if c.URL == "" {
			c.URL = prev.URL
		}
		out[i] = c
	}
	return out
}
