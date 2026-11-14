package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PR draft statuses.
const (
	PRReady   = "ready"
	PROpening = "opening"
	PROpened  = "opened"
)

// PRDraft is a run's fix prepared as a pull request.
type PRDraft struct {
	RunID      int64
	Proposal   json.RawMessage
	Guidelines json.RawMessage // nil if none were read
	Decision   json.RawMessage // nil if none was made
	Partial    bool
	Status     string
	Error      string
	PRURL      string
	OpenedBy   *int64
	OpenedAt   *time.Time
	CreatedAt  time.Time
}

// ErrPRBusy means the draft is being opened, or already was.
var ErrPRBusy = errors.New("this pull request is already being opened or was opened")

// SavePRDraft stores (or replaces) a run's draft. Opened drafts are kept.
func (d *DB) SavePRDraft(ctx context.Context, p PRDraft) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO pr_drafts (run_id, proposal, guidelines, decision, partial)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (run_id) DO UPDATE SET proposal = $2, guidelines = $3, decision = $4, partial = $5, error = ''
		WHERE pr_drafts.status = 'ready'`,
		p.RunID, p.Proposal, nullJSON(p.Guidelines), nullJSON(p.Decision), p.Partial)
	return err
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

// PRDraftForUser returns a run's draft if the user can see the run.
func (d *DB) PRDraftForUser(ctx context.Context, userID, runID int64) (PRDraft, error) {
	var p PRDraft
	err := d.Pool.QueryRow(ctx, `
		SELECT p.run_id, p.proposal, p.guidelines, p.decision, p.partial, p.status, p.error, p.pr_url, p.opened_by, p.opened_at, p.created_at
		FROM pr_drafts p JOIN runs x ON x.id = p.run_id JOIN repos r ON r.id = x.repo_id
		WHERE p.run_id = $2 AND `+visible, userID, runID).Scan(
		&p.RunID, &p.Proposal, &p.Guidelines, &p.Decision, &p.Partial, &p.Status, &p.Error, &p.PRURL, &p.OpenedBy, &p.OpenedAt, &p.CreatedAt)
	return p, notFound(err)
}

// ClaimPROpen marks a ready draft as being opened, so two clicks can't open
// two pull requests (an attempt that died more than 10 minutes ago no longer
// holds it). It fails with ErrPRBusy otherwise.
func (d *DB) ClaimPROpen(ctx context.Context, userID, runID int64) error {
	tag, err := d.Pool.Exec(ctx, `
		UPDATE pr_drafts p SET status = 'opening', opening_at = now(), error = ''
		FROM runs x JOIN repos r ON r.id = x.repo_id
		WHERE p.run_id = $2 AND x.id = p.run_id AND `+visible+`
		  AND (p.status = 'ready' OR (p.status = 'opening' AND p.opening_at < now() - interval '10 minutes'))`, userID, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPRBusy
	}
	return nil
}

// FinishPROpen records the opened pull request on the draft and the run.
// userID is who opened it (nil when it was opened automatically).
func (d *DB) FinishPROpen(ctx context.Context, runID int64, userID *int64, url string) error {
	err := pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pr_drafts SET status = 'opened', pr_url = $2, opened_by = $3, opened_at = now(), error = ''
			WHERE run_id = $1`, runID, url, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE runs SET pr_url = $2 WHERE id = $1`, runID, url)
		return err
	})
	if err == nil {
		_ = d.AddEvent(ctx, runID, "pr_opened", map[string]any{"url": url, "automatic": userID == nil})
	}
	return err
}

// FailPROpen releases a draft after a failed attempt, keeping the reason.
func (d *DB) FailPROpen(ctx context.Context, runID int64, msg string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE pr_drafts SET status = 'ready', error = $2 WHERE run_id = $1 AND status = 'opening'`, runID, msg)
	return err
}

// RunReports returns a run with its raw reports, without access checks (for
// workers and operator tools).
func (d *DB) RunReports(ctx context.Context, runID int64) (Run, json.RawMessage, json.RawMessage, error) {
	var rep, ver []byte
	r, err := scanRun(d.Pool.QueryRow(ctx, `SELECT `+runCols+`, x.report, x.verify_report FROM runs x WHERE x.id = $1`, runID), &rep, &ver)
	return r, rep, ver, notFound(err)
}

// PRSummary is one pull request (drafted or opened) in a quickstart's list.
type PRSummary struct {
	RunID     int64      `json:"run_id"`
	Status    string     `json:"status"` // ready | opening | opened
	Title     string     `json:"title"`
	Partial   bool       `json:"partial"`
	PRURL     string     `json:"pr_url,omitempty"`
	OpenedAt  *time.Time `json:"opened_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	CommitSHA string     `json:"commit_sha"`
}

// PRsForQuickstart lists a quickstart's pull requests, newest first.
func (d *DB) PRsForQuickstart(ctx context.Context, userID, quickstartID int64) ([]PRSummary, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT p.run_id, p.status, COALESCE(NULLIF(p.decision->>'title', ''), p.proposal->>'Title', ''), p.partial,
			p.pr_url, p.opened_at, p.created_at, x.commit_sha
		FROM pr_drafts p JOIN runs x ON x.id = p.run_id JOIN repos r ON r.id = x.repo_id
		WHERE x.quickstart_id = $2 AND `+visible+`
		ORDER BY p.run_id DESC LIMIT 50`, userID, quickstartID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PRSummary, error) {
		var p PRSummary
		err := r.Scan(&p.RunID, &p.Status, &p.Title, &p.Partial, &p.PRURL, &p.OpenedAt, &p.CreatedAt, &p.CommitSHA)
		return p, err
	})
}

// PRDraftByRun returns a run's draft without an access check (workers).
func (d *DB) PRDraftByRun(ctx context.Context, runID int64) (PRDraft, error) {
	var p PRDraft
	err := d.Pool.QueryRow(ctx, `
		SELECT run_id, proposal, guidelines, decision, partial, status, error, pr_url, opened_by, opened_at, created_at
		FROM pr_drafts WHERE run_id = $1`, runID).Scan(
		&p.RunID, &p.Proposal, &p.Guidelines, &p.Decision, &p.Partial, &p.Status, &p.Error, &p.PRURL, &p.OpenedBy, &p.OpenedAt, &p.CreatedAt)
	return p, notFound(err)
}

// ClaimPROpenAny is ClaimPROpen without an access check (workers).
func (d *DB) ClaimPROpenAny(ctx context.Context, runID int64) error {
	tag, err := d.Pool.Exec(ctx, `
		UPDATE pr_drafts SET status = 'opening', opening_at = now(), error = ''
		WHERE run_id = $1 AND (status = 'ready' OR (status = 'opening' AND opening_at < now() - interval '10 minutes'))`, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPRBusy
	}
	return nil
}
