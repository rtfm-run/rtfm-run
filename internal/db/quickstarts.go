package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Quickstart is one doc in one repo that vanguard tests.
type Quickstart struct {
	ID           int64     `json:"id"`
	RepoID       int64     `json:"repo_id"`
	Name         string    `json:"name"`
	DocPath      string    `json:"doc_path"`
	Goal         string    `json:"goal"`
	SuccessCheck string    `json:"success_check"`
	Branch       string    `json:"branch"` // "" = the repo's default branch
	Backend      string    `json:"backend"`
	Model        string    `json:"model"`
	Fix          bool      `json:"fix"`
	Verify       bool      `json:"verify"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Trigger runs a quickstart automatically.
type Trigger struct {
	ID           int64     `json:"id"`
	QuickstartID int64     `json:"quickstart_id"`
	Kind         string    `json:"kind"`     // push
	Branches     []string  `json:"branches"` // globs; empty = the quickstart's branch
	Paths        []string  `json:"paths"`    // globs; empty = any change
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const qsCols = `q.id, q.repo_id, q.name, q.doc_path, q.goal, q.success_check, q.branch, q.backend, q.model, q.fix, q.verify, q.created_at, q.updated_at`

func scanQS(row pgx.Row) (Quickstart, error) {
	var q Quickstart
	err := row.Scan(&q.ID, &q.RepoID, &q.Name, &q.DocPath, &q.Goal, &q.SuccessCheck, &q.Branch, &q.Backend, &q.Model, &q.Fix, &q.Verify, &q.CreatedAt, &q.UpdatedAt)
	return q, err
}

// qsVisible joins a quickstart to its repo and checks the user can see it.
const qsVisible = `FROM quickstarts q JOIN repos r ON r.id = q.repo_id WHERE ` + visible

// CreateQuickstart adds a quickstart to a repo the user can see.
func (d *DB) CreateQuickstart(ctx context.Context, userID int64, q Quickstart) (Quickstart, error) {
	if _, err := d.RepoForUser(ctx, userID, q.RepoID); err != nil {
		return q, err
	}
	return scanQS(d.Pool.QueryRow(ctx, `
		INSERT INTO quickstarts AS q (repo_id, name, doc_path, goal, success_check, branch, backend, model, fix, verify, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+qsCols,
		q.RepoID, q.Name, q.DocPath, q.Goal, q.SuccessCheck, q.Branch, q.Backend, q.Model, q.Fix, q.Verify, userID))
}

// QuickstartsForRepo lists a repo's quickstarts.
func (d *DB) QuickstartsForRepo(ctx context.Context, userID, repoID int64) ([]Quickstart, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+qsCols+` `+qsVisible+` AND q.repo_id = $2 ORDER BY q.id`, userID, repoID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Quickstart, error) { return scanQS(r) })
}

// QuickstartForUser returns a quickstart the user can see.
func (d *DB) QuickstartForUser(ctx context.Context, userID, id int64) (Quickstart, error) {
	q, err := scanQS(d.Pool.QueryRow(ctx, `SELECT `+qsCols+` `+qsVisible+` AND q.id = $2`, userID, id))
	return q, notFound(err)
}

// QuickstartByID returns a quickstart without an access check (workers).
func (d *DB) QuickstartByID(ctx context.Context, id int64) (Quickstart, error) {
	q, err := scanQS(d.Pool.QueryRow(ctx, `SELECT `+qsCols+` FROM quickstarts q WHERE q.id = $1`, id))
	return q, notFound(err)
}

// UpdateQuickstart saves editable fields.
func (d *DB) UpdateQuickstart(ctx context.Context, userID int64, q Quickstart) (Quickstart, error) {
	if _, err := d.QuickstartForUser(ctx, userID, q.ID); err != nil {
		return q, err
	}
	return scanQS(d.Pool.QueryRow(ctx, `
		UPDATE quickstarts q SET name = $2, doc_path = $3, goal = $4, success_check = $5, branch = $6, backend = $7,
			model = $8, fix = $9, verify = $10, updated_at = now()
		WHERE q.id = $1 RETURNING `+qsCols,
		q.ID, q.Name, q.DocPath, q.Goal, q.SuccessCheck, q.Branch, q.Backend, q.Model, q.Fix, q.Verify))
}

// DeleteQuickstart removes a quickstart and its runs.
func (d *DB) DeleteQuickstart(ctx context.Context, userID, id int64) error {
	if _, err := d.QuickstartForUser(ctx, userID, id); err != nil {
		return err
	}
	_, err := d.Pool.Exec(ctx, `DELETE FROM quickstarts WHERE id = $1`, id)
	return err
}

const trCols = `t.id, t.quickstart_id, t.kind, t.branches, t.paths, t.enabled, t.created_at, t.updated_at`

func scanTrigger(row pgx.Row) (Trigger, error) {
	var t Trigger
	err := row.Scan(&t.ID, &t.QuickstartID, &t.Kind, &t.Branches, &t.Paths, &t.Enabled, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// CreateTrigger adds a trigger to a quickstart the user can see.
func (d *DB) CreateTrigger(ctx context.Context, userID int64, t Trigger) (Trigger, error) {
	if _, err := d.QuickstartForUser(ctx, userID, t.QuickstartID); err != nil {
		return t, err
	}
	return scanTrigger(d.Pool.QueryRow(ctx, `
		INSERT INTO triggers AS t (quickstart_id, kind, branches, paths, enabled) VALUES ($1, $2, $3, $4, $5)
		RETURNING `+trCols, t.QuickstartID, t.Kind, nonNil(t.Branches), nonNil(t.Paths), t.Enabled))
}

// TriggersForQuickstart lists a quickstart's triggers.
func (d *DB) TriggersForQuickstart(ctx context.Context, userID, qsID int64) ([]Trigger, error) {
	if _, err := d.QuickstartForUser(ctx, userID, qsID); err != nil {
		return nil, err
	}
	rows, err := d.Pool.Query(ctx, `SELECT `+trCols+` FROM triggers t WHERE t.quickstart_id = $1 ORDER BY t.id`, qsID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Trigger, error) { return scanTrigger(r) })
}

// TriggerForUser returns a trigger the user can see.
func (d *DB) TriggerForUser(ctx context.Context, userID, id int64) (Trigger, error) {
	t, err := scanTrigger(d.Pool.QueryRow(ctx, `SELECT `+trCols+` FROM triggers t
		JOIN quickstarts q ON q.id = t.quickstart_id JOIN repos r ON r.id = q.repo_id
		WHERE t.id = $2 AND `+visible, userID, id))
	return t, notFound(err)
}

// UpdateTrigger saves editable fields.
func (d *DB) UpdateTrigger(ctx context.Context, userID int64, t Trigger) (Trigger, error) {
	if _, err := d.TriggerForUser(ctx, userID, t.ID); err != nil {
		return t, err
	}
	return scanTrigger(d.Pool.QueryRow(ctx, `
		UPDATE triggers t SET branches = $2, paths = $3, enabled = $4, updated_at = now() WHERE t.id = $1
		RETURNING `+trCols, t.ID, nonNil(t.Branches), nonNil(t.Paths), t.Enabled))
}

// DeleteTrigger removes a trigger.
func (d *DB) DeleteTrigger(ctx context.Context, userID, id int64) error {
	if _, err := d.TriggerForUser(ctx, userID, id); err != nil {
		return err
	}
	_, err := d.Pool.Exec(ctx, `DELETE FROM triggers WHERE id = $1`, id)
	return err
}

// PushTrigger is an enabled push trigger with its quickstart, for matching
// a push event.
type PushTrigger struct {
	Trigger    Trigger
	Quickstart Quickstart
}

// PushTriggersForRepo lists enabled push triggers on a repo's quickstarts.
func (d *DB) PushTriggersForRepo(ctx context.Context, repoID int64) ([]PushTrigger, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+trCols+`, `+qsCols+`
		FROM triggers t JOIN quickstarts q ON q.id = t.quickstart_id
		WHERE q.repo_id = $1 AND t.kind = 'push' AND t.enabled ORDER BY t.id`, repoID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PushTrigger, error) {
		var p PushTrigger
		t, q := &p.Trigger, &p.Quickstart
		err := r.Scan(&t.ID, &t.QuickstartID, &t.Kind, &t.Branches, &t.Paths, &t.Enabled, &t.CreatedAt, &t.UpdatedAt,
			&q.ID, &q.RepoID, &q.Name, &q.DocPath, &q.Goal, &q.SuccessCheck, &q.Branch, &q.Backend, &q.Model, &q.Fix, &q.Verify, &q.CreatedAt, &q.UpdatedAt)
		return p, err
	})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
