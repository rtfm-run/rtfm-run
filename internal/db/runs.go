package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Run statuses (the job's lifecycle; the quickstart's result is Verdict).
const (
	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded" // the job completed; see Verdict
	RunFailed    = "failed"    // the job itself failed (infrastructure, timeout)
	RunCanceled  = "canceled"
)

// Run is a job and its outcome.
type Run struct {
	ID           int64      `json:"id"`
	QuickstartID int64      `json:"quickstart_id"`
	RepoID       int64      `json:"repo_id"`
	Trigger      string     `json:"trigger"`
	TriggeredBy  *int64     `json:"triggered_by,omitempty"`
	Ref          string     `json:"ref"`
	CommitSHA    string     `json:"commit_sha"`
	Status       string     `json:"status"`
	Verdict      string     `json:"verdict"` // works | broken | blocked | error
	Issues       int        `json:"issues"`
	DocIssues    int        `json:"doc_issues"`
	FixStatus    string     `json:"fix_status"`
	PRURL        string     `json:"pr_url,omitempty"`
	ReportID     string     `json:"report_id,omitempty"`
	Error        string     `json:"error,omitempty"`
	Tokens       int        `json:"tokens"`
	Attempt      int        `json:"attempt"`
	QueuedAt     time.Time  `json:"queued_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

const runCols = `x.id, x.quickstart_id, x.repo_id, x.trigger, x.triggered_by, x.ref, x.commit_sha, x.status, x.verdict,
	x.issues, x.doc_issues, x.fix_status, x.pr_url, x.report_id, x.error, x.tokens, x.attempt, x.queued_at, x.started_at, x.finished_at`

func scanRun(row pgx.Row, extra ...any) (Run, error) {
	var r Run
	dst := []any{&r.ID, &r.QuickstartID, &r.RepoID, &r.Trigger, &r.TriggeredBy, &r.Ref, &r.CommitSHA, &r.Status, &r.Verdict,
		&r.Issues, &r.DocIssues, &r.FixStatus, &r.PRURL, &r.ReportID, &r.Error, &r.Tokens, &r.Attempt, &r.QueuedAt, &r.StartedAt, &r.FinishedAt}
	err := row.Scan(append(dst, extra...)...)
	return r, err
}

// Enqueue queues a run. commitSHA may be empty (test the branch head).
func (d *DB) Enqueue(ctx context.Context, q Quickstart, trigger string, userID *int64, ref, commitSHA string) (Run, error) {
	r, err := scanRun(d.Pool.QueryRow(ctx, `
		INSERT INTO runs AS x (quickstart_id, repo_id, trigger, triggered_by, ref, commit_sha, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'queued') RETURNING `+runCols,
		q.ID, q.RepoID, trigger, userID, ref, commitSHA))
	if err == nil {
		_ = d.AddEvent(ctx, r.ID, "queued", map[string]any{"trigger": trigger, "ref": ref, "commit": commitSHA})
	}
	return r, err
}

// Job is a claimed run with what the worker needs.
type Job struct {
	Run        Run
	Quickstart Quickstart
	Repo       Repo
}

// Claim takes the oldest queued run for worker, or returns ErrNotFound.
func (d *DB) Claim(ctx context.Context, worker string) (Job, error) {
	var j Job
	err := pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		r, err := scanRun(tx.QueryRow(ctx, `
			UPDATE runs x SET status = 'running', started_at = now(), worker = $1, attempt = attempt + 1
			WHERE x.id = (SELECT id FROM runs WHERE status = 'queued' ORDER BY queued_at FOR UPDATE SKIP LOCKED LIMIT 1)
			RETURNING `+runCols, worker))
		if err != nil {
			return notFound(err)
		}
		j.Run = r
		if j.Quickstart, err = scanQS(tx.QueryRow(ctx, `SELECT `+qsCols+` FROM quickstarts q WHERE q.id = $1`, r.QuickstartID)); err != nil {
			return err
		}
		j.Repo, err = scanRepo(tx.QueryRow(ctx, `SELECT `+repoCols+` FROM repos r WHERE r.id = $1`, r.RepoID))
		return err
	})
	if err == nil {
		_ = d.AddEvent(ctx, j.Run.ID, "started", map[string]any{"worker": worker, "attempt": j.Run.Attempt})
	}
	return j, err
}

// Requeue returns running runs owned by a worker to the queue (e.g. after a
// crash and restart of that worker).
func (d *DB) Requeue(ctx context.Context, worker string) (int64, error) {
	tag, err := d.Pool.Exec(ctx, `UPDATE runs SET status = 'queued', started_at = NULL, worker = ''
		WHERE status = 'running' AND worker = $1`, worker)
	return tag.RowsAffected(), err
}

// Step is one quickstart step of one stage of a run.
type Step struct {
	Stage        string `json:"stage"`
	Idx          int    `json:"index"`
	StepKey      string `json:"step_key"`
	DocRef       string `json:"doc_ref"`
	Instruction  string `json:"instruction"`
	DocCommand   string `json:"doc_command"`
	Command      string `json:"command"`
	ExitCode     int    `json:"exit_code"`
	Status       string `json:"status"`
	Failure      string `json:"failure"`
	Diagnosis    string `json:"diagnosis"`
	Note         string `json:"note"`
	Output       string `json:"output"`
	ObservedExit *int   `json:"observed_exit"`
	ChecksOut    bool   `json:"checks_out"`
}

// Outcome is what a worker records when a run completes.
type Outcome struct {
	CommitSHA    string
	Verdict      string
	Issues       int
	DocIssues    int
	FixStatus    string
	ReportID     string
	Tokens       int
	Report       any // marshaled to JSON
	VerifyReport any // may be nil
	Steps        []Step
	// Canceled records the run as canceled (a user stopped it partway)
	// while keeping whatever it found before stopping.
	Canceled bool
}

// Finish records a completed run and its steps.
func (d *DB) Finish(ctx context.Context, runID int64, o Outcome) error {
	rep, err := json.Marshal(o.Report)
	if err != nil {
		return err
	}
	var ver []byte
	if o.VerifyReport != nil {
		if ver, err = json.Marshal(o.VerifyReport); err != nil {
			return err
		}
	}
	status := RunSucceeded
	if o.Canceled {
		status = RunCanceled
	}
	err = pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE runs SET status = $11, finished_at = now(), commit_sha = CASE WHEN $2 = '' THEN commit_sha ELSE $2 END,
				verdict = $3, issues = $4, doc_issues = $5, fix_status = $6, report_id = $7, tokens = $8, report = $9, verify_report = $10
			WHERE id = $1`, runID, o.CommitSHA, o.Verdict, o.Issues, o.DocIssues, o.FixStatus, o.ReportID, o.Tokens, rep, ver, status); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM run_steps WHERE run_id = $1`, runID); err != nil {
			return err
		}
		for _, s := range o.Steps {
			if _, err := tx.Exec(ctx, `
				INSERT INTO run_steps (run_id, stage, idx, step_key, doc_ref, instruction, doc_command, command, exit_code,
					status, failure, diagnosis, note, output, observed_exit, checks_out)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
				runID, s.Stage, s.Idx, s.StepKey, s.DocRef, s.Instruction, s.DocCommand, s.Command, s.ExitCode,
				s.Status, s.Failure, s.Diagnosis, s.Note, s.Output, s.ObservedExit, s.ChecksOut); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		if o.Canceled {
			_ = d.AddEvent(ctx, runID, "canceled", map[string]any{"verdict": o.Verdict, "issues": o.Issues})
		} else {
			_ = d.AddEvent(ctx, runID, "finished", map[string]any{"verdict": o.Verdict, "issues": o.Issues, "fix": o.FixStatus})
		}
	}
	return err
}

// Fail records that the job itself failed.
func (d *DB) Fail(ctx context.Context, runID int64, msg string, report any) error {
	var rep []byte
	if report != nil {
		rep, _ = json.Marshal(report)
	}
	_, err := d.Pool.Exec(ctx, `UPDATE runs SET status = 'failed', finished_at = now(), verdict = 'error', error = $2,
		report = COALESCE($3, report) WHERE id = $1`, runID, msg, rep)
	if err == nil {
		_ = d.AddEvent(ctx, runID, "failed", map[string]any{"error": msg})
	}
	return err
}

// CancelRun cancels a queued run, or asks a running one to stop.
func (d *DB) CancelRun(ctx context.Context, userID, runID int64) (Run, error) {
	if _, err := d.RunForUser(ctx, userID, runID); err != nil {
		return Run{}, err
	}
	r, err := scanRun(d.Pool.QueryRow(ctx, `
		UPDATE runs x SET
			status = CASE WHEN status = 'queued' THEN 'canceled' ELSE status END,
			finished_at = CASE WHEN status = 'queued' THEN now() ELSE finished_at END,
			cancel_requested = status IN ('queued', 'running')
		WHERE x.id = $1 RETURNING `+runCols, runID))
	if err == nil {
		_ = d.AddEvent(ctx, runID, "cancel_requested", nil)
	}
	return r, err
}

// MarkCanceled finishes a running run that was canceled.
func (d *DB) MarkCanceled(ctx context.Context, runID int64) error {
	_, err := d.Pool.Exec(ctx, `UPDATE runs SET status = 'canceled', finished_at = now() WHERE id = $1`, runID)
	if err == nil {
		_ = d.AddEvent(ctx, runID, "canceled", nil)
	}
	return err
}

// CancelRequested reports whether a run should stop.
func (d *DB) CancelRequested(ctx context.Context, runID int64) (bool, error) {
	var c bool
	err := d.Pool.QueryRow(ctx, `SELECT cancel_requested FROM runs WHERE id = $1`, runID).Scan(&c)
	return c, notFound(err)
}

// RunFilter narrows ListRuns.
type RunFilter struct {
	RepoID       int64
	QuickstartID int64
	Status       string
	Before       int64 // cursor: runs with id < Before
	Limit        int
}

// ListRuns lists runs the user can see, newest first.
func (d *DB) ListRuns(ctx context.Context, userID int64, f RunFilter) ([]Run, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := d.Pool.Query(ctx, `SELECT `+runCols+` FROM runs x JOIN repos r ON r.id = x.repo_id
		WHERE `+visible+`
		  AND ($2 = 0 OR x.repo_id = $2) AND ($3 = 0 OR x.quickstart_id = $3) AND ($4 = '' OR x.status = $4)
		  AND ($5 = 0 OR x.id < $5)
		ORDER BY x.id DESC LIMIT $6`, userID, f.RepoID, f.QuickstartID, f.Status, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Run, error) { return scanRun(r) })
}

// RunForUser returns a run the user can see.
func (d *DB) RunForUser(ctx context.Context, userID, runID int64) (Run, error) {
	r, err := scanRun(d.Pool.QueryRow(ctx, `SELECT `+runCols+` FROM runs x JOIN repos r ON r.id = x.repo_id
		WHERE x.id = $2 AND `+visible, userID, runID))
	return r, notFound(err)
}

// RunDetail is a run with its reports and steps.
type RunDetail struct {
	Run
	Report       json.RawMessage `json:"report,omitempty"`
	VerifyReport json.RawMessage `json:"verify_report,omitempty"`
	Steps        []Step          `json:"steps"`
}

// RunDetailForUser returns everything about a run the user can see.
func (d *DB) RunDetailForUser(ctx context.Context, userID, runID int64) (RunDetail, error) {
	var det RunDetail
	var rep, ver []byte
	r, err := scanRun(d.Pool.QueryRow(ctx, `SELECT `+runCols+`, x.report, x.verify_report FROM runs x JOIN repos r ON r.id = x.repo_id
		WHERE x.id = $2 AND `+visible, userID, runID), &rep, &ver)
	if err != nil {
		return det, notFound(err)
	}
	det.Run, det.Report, det.VerifyReport = r, rep, ver
	rows, err := d.Pool.Query(ctx, `SELECT stage, idx, step_key, doc_ref, instruction, doc_command, command, exit_code,
		status, failure, diagnosis, note, output, observed_exit, checks_out
		FROM run_steps WHERE run_id = $1 ORDER BY stage = 'verify', idx`, runID)
	if err != nil {
		return det, err
	}
	det.Steps, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Step, error) {
		var s Step
		err := r.Scan(&s.Stage, &s.Idx, &s.StepKey, &s.DocRef, &s.Instruction, &s.DocCommand, &s.Command, &s.ExitCode,
			&s.Status, &s.Failure, &s.Diagnosis, &s.Note, &s.Output, &s.ObservedExit, &s.ChecksOut)
		return s, err
	})
	if det.Steps == nil {
		det.Steps = []Step{}
	}
	return det, err
}

// StepHealth is the current standing of one quickstart step across runs.
type StepHealth struct {
	StepKey     string `json:"step_key"`
	Idx         int    `json:"index"`
	DocRef      string `json:"doc_ref"`
	Instruction string `json:"instruction"`
	DocCommand  string `json:"doc_command"`
	// The most recent run that reached this step.
	Status    string    `json:"status"`
	ChecksOut bool      `json:"checks_out"`
	Failure   string    `json:"failure"`
	Diagnosis string    `json:"diagnosis"`
	RunID     int64     `json:"run_id"`
	CommitSHA string    `json:"commit_sha"`
	CheckedAt time.Time `json:"checked_at"`
	// The most recent run where it checked out (followed as written, and the
	// commands really succeeded).
	LastVerifiedRunID  *int64     `json:"last_verified_run_id"`
	LastVerifiedCommit *string    `json:"last_verified_commit"`
	LastVerifiedAt     *time.Time `json:"last_verified_at"`
}

// StepHealthForQuickstart returns per-step health from completed runs of
// the doc as published (the newhire stage; verify runs test proposed fixes).
func (d *DB) StepHealthForQuickstart(ctx context.Context, userID, qsID int64) ([]StepHealth, error) {
	if _, err := d.QuickstartForUser(ctx, userID, qsID); err != nil {
		return nil, err
	}
	rows, err := d.Pool.Query(ctx, `
		WITH s AS (
			SELECT rs.*, x.id AS rid, x.commit_sha, x.finished_at
			FROM run_steps rs JOIN runs x ON x.id = rs.run_id
			WHERE x.quickstart_id = $1 AND x.status = 'succeeded' AND rs.stage = 'newhire'
		),
		latest AS (SELECT DISTINCT ON (step_key) * FROM s ORDER BY step_key, finished_at DESC, rid DESC),
		verified AS (SELECT DISTINCT ON (step_key) step_key, rid, commit_sha, finished_at FROM s WHERE checks_out
			ORDER BY step_key, finished_at DESC, rid DESC)
		SELECT l.step_key, l.idx, l.doc_ref, l.instruction, l.doc_command, l.status, l.checks_out, l.failure, l.diagnosis,
			l.rid, l.commit_sha, l.finished_at, v.rid, v.commit_sha, v.finished_at
		FROM latest l LEFT JOIN verified v ON v.step_key = l.step_key
		ORDER BY l.finished_at DESC, l.idx`, qsID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StepHealth, error) {
		var h StepHealth
		err := r.Scan(&h.StepKey, &h.Idx, &h.DocRef, &h.Instruction, &h.DocCommand, &h.Status, &h.ChecksOut, &h.Failure, &h.Diagnosis,
			&h.RunID, &h.CommitSHA, &h.CheckedAt, &h.LastVerifiedRunID, &h.LastVerifiedCommit, &h.LastVerifiedAt)
		return h, err
	})
	if out == nil {
		out = []StepHealth{}
	}
	return out, err
}

// Event is a progress event on a run.
type Event struct {
	ID    int64           `json:"id"`
	RunID int64           `json:"run_id"`
	At    time.Time       `json:"at"`
	Kind  string          `json:"kind"`
	Data  json.RawMessage `json:"data"`
}

// AddEvent appends an event to a run.
func (d *DB) AddEvent(ctx context.Context, runID int64, kind string, data any) error {
	if data == nil {
		data = map[string]any{}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO run_events (run_id, kind, data) VALUES ($1, $2, $3)`, runID, kind, b)
	return err
}

// EventsSince returns a run's events with id > after (the user must be able
// to see the run).
func (d *DB) EventsSince(ctx context.Context, userID, runID, after int64) ([]Event, error) {
	if _, err := d.RunForUser(ctx, userID, runID); err != nil {
		return nil, err
	}
	rows, err := d.Pool.Query(ctx, `SELECT id, run_id, at, kind, data FROM run_events WHERE run_id = $1 AND id > $2 ORDER BY id`, runID, after)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		err := r.Scan(&e.ID, &e.RunID, &e.At, &e.Kind, &e.Data)
		return e, err
	})
	if out == nil {
		out = []Event{}
	}
	return out, err
}

// CredentialNames lists a quickstart's credential names (never values).
func (d *DB) CredentialNames(ctx context.Context, userID, qsID int64) ([]string, error) {
	if _, err := d.QuickstartForUser(ctx, userID, qsID); err != nil {
		return nil, err
	}
	rows, err := d.Pool.Query(ctx, `SELECT name FROM credentials WHERE quickstart_id = $1 ORDER BY name`, qsID)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if names == nil {
		names = []string{}
	}
	return names, err
}

// SetCredential stores an encrypted credential for a quickstart.
func (d *DB) SetCredential(ctx context.Context, userID, qsID int64, name string, valueEnc []byte) error {
	if _, err := d.QuickstartForUser(ctx, userID, qsID); err != nil {
		return err
	}
	_, err := d.Pool.Exec(ctx, `INSERT INTO credentials (quickstart_id, name, value_enc, created_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (quickstart_id, name) DO UPDATE SET value_enc = EXCLUDED.value_enc, updated_at = now()`, qsID, name, valueEnc, userID)
	return err
}

// DeleteCredential removes a credential.
func (d *DB) DeleteCredential(ctx context.Context, userID, qsID int64, name string) error {
	if _, err := d.QuickstartForUser(ctx, userID, qsID); err != nil {
		return err
	}
	_, err := d.Pool.Exec(ctx, `DELETE FROM credentials WHERE quickstart_id = $1 AND name = $2`, qsID, name)
	return err
}

// CredentialValue returns an encrypted credential (workers).
func (d *DB) CredentialValue(ctx context.Context, qsID int64, name string) ([]byte, bool, error) {
	var v []byte
	err := d.Pool.QueryRow(ctx, `SELECT value_enc FROM credentials WHERE quickstart_id = $1 AND name = $2`, qsID, name).Scan(&v)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	return v, err == nil, err
}

// HasActiveRun reports whether a quickstart already has a queued or running
// run for commit (so duplicate webhook pushes don't stack up).
func (d *DB) HasActiveRun(ctx context.Context, qsID int64, commit string) (bool, error) {
	var ok bool
	err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE quickstart_id = $1 AND commit_sha = $2
		AND status IN ('queued', 'running'))`, qsID, commit).Scan(&ok)
	return ok, err
}
