// Package worker runs queued quickstart runs: claim a job, clone the repo
// with an installation token, run the pipeline (newhire → fix → verify in
// devboxes), and store the outcome with its per-step results.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"vanguard.run/internal/creds"
	"vanguard.run/internal/db"
	"vanguard.run/internal/devbox"
	"vanguard.run/internal/ghapp"
	"vanguard.run/internal/pipeline"
	"vanguard.run/internal/report"
	"vanguard.run/internal/runner"
	"vanguard.run/internal/secretbox"
	"vanguard.run/internal/steps"
	"vanguard.run/internal/targets"
)

// Config configures a worker process.
type Config struct {
	DB      *db.DB
	App     *ghapp.App
	Secrets *secretbox.Box
	Log     *slog.Logger

	Name        string // unique per process, e.g. hostname-pid
	Concurrency int    // runs at once
	Poll        time.Duration

	WorkRoot     string
	OutDir       string // run artifacts: <OutDir>/run-<id>/...
	DefaultModel string
	Timeout      time.Duration // per agent stage

	// NewBackend returns a ready devbox backend by name (auto, docker,
	// firecracker).
	NewBackend func(ctx context.Context, name string) (devbox.Backend, error)
	// NewRunner returns the agent runner for a model.
	NewRunner func(model string) runner.Runner
}

// Run processes jobs until ctx is canceled.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 2 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	done := make(chan struct{}, cfg.Concurrency)
	for i := 0; i < cfg.Concurrency; i++ {
		slot := fmt.Sprintf("%s/%d", cfg.Name, i)
		// A previous process with this name died mid-run: put its jobs back.
		if n, err := cfg.DB.Requeue(ctx, slot); err != nil {
			return err
		} else if n > 0 {
			cfg.Log.Info("requeued interrupted runs", "worker", slot, "count", n)
		}
		go func() {
			defer func() { done <- struct{}{} }()
			loop(ctx, cfg, slot)
		}()
	}
	for i := 0; i < cfg.Concurrency; i++ {
		<-done
	}
	return ctx.Err()
}

func loop(ctx context.Context, cfg Config, slot string) {
	for ctx.Err() == nil {
		j, err := cfg.DB.Claim(ctx, slot)
		if err != nil {
			if !errors.Is(err, db.ErrNotFound) && ctx.Err() == nil {
				cfg.Log.Error("claim failed", "worker", slot, "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(cfg.Poll):
			}
			continue
		}
		log := cfg.Log.With("run", j.Run.ID, "repo", j.Repo.FullName, "doc", j.Quickstart.DocPath, "worker", slot)
		log.Info("run started", "trigger", j.Run.Trigger, "commit", j.Run.CommitSHA)
		Process(ctx, cfg, j, log)
	}
}

// dbSource serves a quickstart's encrypted credentials to the broker.
type dbSource struct {
	db      *db.DB
	secrets *secretbox.Box
	qsID    int64
}

func (s dbSource) Get(name string) (string, bool, error) {
	enc, ok, err := s.db.CredentialValue(context.Background(), s.qsID, name)
	if err != nil || !ok {
		return "", false, err
	}
	v, err := s.secrets.Open(enc)
	if err != nil {
		return "", false, err
	}
	return string(v), true, nil
}

func (s dbSource) Set(string, string) error {
	return errors.New("credentials are managed in the dashboard")
}

// Process runs one claimed job to completion and records the outcome.
func Process(parent context.Context, cfg Config, j db.Job, log *slog.Logger) {
	q, repo, run := j.Quickstart, j.Repo, j.Run
	stages := 1
	if q.Fix {
		stages = 3
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(stages)*cfg.Timeout+5*time.Minute)
	defer cancel()

	// Stop if a user cancels the run.
	canceled := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if c, _ := cfg.DB.CancelRequested(context.Background(), run.ID); c {
					close(canceled)
					cancel()
					return
				}
			}
		}
	}()
	record := context.Background() // outcomes are recorded even if ctx was canceled
	fail := func(err error, rep *report.Run) {
		select {
		case <-canceled:
			_ = cfg.DB.MarkCanceled(record, run.ID)
			log.Info("run canceled")
			return
		default:
		}
		log.Error("run failed", "err", err)
		var r any
		if rep != nil {
			r = rep
		}
		_ = cfg.DB.Fail(record, run.ID, err.Error(), r)
	}

	token, err := cfg.App.InstallationToken(ctx, repo.InstallationID)
	if err != nil {
		fail(fmt.Errorf("github access: %w", err), nil)
		return
	}
	backend, err := cfg.NewBackend(ctx, q.Backend)
	if err != nil {
		fail(fmt.Errorf("devbox backend: %w", err), nil)
		return
	}
	model := q.Model
	if model == "" {
		model = cfg.DefaultModel
	}
	branch := run.Ref
	if branch == "" {
		branch = q.Branch
	}
	t := targets.Target{
		Name:    fmt.Sprintf("qs-%d", q.ID),
		Repo:    "https://github.com/" + repo.FullName,
		Ref:     branch,
		Commit:  run.CommitSHA,
		DocPath: q.DocPath,
		Goal:    q.Goal,
		Success: q.SuccessCheck,
	}
	outDir := filepath.Join(cfg.OutDir, fmt.Sprintf("run-%d", run.ID))
	pc := pipeline.Config{
		Runner:      cfg.NewRunner(model),
		Backend:     backend,
		WorkRoot:    cfg.WorkRoot,
		OutDir:      outDir,
		Model:       model,
		Fix:         q.Fix,
		Verify:      q.Fix && q.Verify,
		GitHubToken: token,
		Broker:      &creds.Broker{Store: dbSource{cfg.DB, cfg.Secrets, q.ID}},
		OnEvent: func(kind string, data map[string]any) {
			_ = cfg.DB.AddEvent(record, run.ID, kind, data)
		},
	}

	rep, err := pipeline.Execute(ctx, pc, t)
	switch {
	case rep == nil:
		fail(err, nil)
		return
	case rep.Error != "":
		fail(errors.New(rep.Error), rep)
		return
	}

	var vrep *report.Run
	if rep.Fix != nil && rep.Fix.VerifyRunID != "" {
		vrep = loadRun(filepath.Join(outDir, t.Slug(), rep.Fix.VerifyRunID+".json"))
	}
	out := db.Outcome{
		CommitSHA: rep.BaseCommit,
		Verdict:   steps.Verdict(rep),
		Issues:    rep.Issues(),
		DocIssues: rep.DocIssues(),
		ReportID:  rep.ID,
		Tokens:    rep.Usage.TotalTokens,
		Report:    rep,
		Steps:     steps.FromRun(rep, "newhire"),
	}
	if rep.Fix != nil {
		out.FixStatus = rep.Fix.Status
		out.Tokens += rep.Fix.Usage.TotalTokens
	}
	if vrep != nil {
		out.VerifyReport = vrep
		out.Steps = append(out.Steps, steps.FromRun(vrep, "verify")...)
	}
	if err := cfg.DB.Finish(record, run.ID, out); err != nil {
		log.Error("recording the run failed", "err", err)
		return
	}
	log.Info("run finished", "verdict", out.Verdict, "issues", out.Issues, "fix", out.FixStatus, "commit", out.CommitSHA)
}

func loadRun(path string) *report.Run {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r report.Run
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}
