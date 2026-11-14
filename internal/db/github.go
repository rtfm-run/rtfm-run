package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Installation is a GitHub App installation on a user or org account.
type Installation struct {
	ID           int64  `json:"id"`
	AccountID    int64  `json:"account_id"`
	AccountLogin string `json:"account_login"`
	AccountType  string `json:"account_type"`
	Suspended    bool   `json:"suspended"`
}

// Repo is a repository the App can access.
type Repo struct {
	ID             int64     `json:"id"`
	InstallationID int64     `json:"installation_id"`
	FullName       string    `json:"full_name"`
	DefaultBranch  string    `json:"default_branch"`
	Private        bool      `json:"private"`
	HTMLURL        string    `json:"html_url"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// UpsertInstallation records an installation.
func (d *DB) UpsertInstallation(ctx context.Context, in Installation) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO installations (id, account_id, account_login, account_type, suspended)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET account_id = EXCLUDED.account_id, account_login = EXCLUDED.account_login,
			account_type = EXCLUDED.account_type, suspended = EXCLUDED.suspended, updated_at = now()`,
		in.ID, in.AccountID, in.AccountLogin, in.AccountType, in.Suspended)
	return err
}

// DeleteInstallation removes an installation and (by cascade) its repos,
// quickstarts and runs.
func (d *DB) DeleteInstallation(ctx context.Context, id int64) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM installations WHERE id = $1`, id)
	return err
}

// SetInstallationSuspended marks an installation (un)suspended.
func (d *DB) SetInstallationSuspended(ctx context.Context, id int64, suspended bool) error {
	_, err := d.Pool.Exec(ctx, `UPDATE installations SET suspended = $2, updated_at = now() WHERE id = $1`, id, suspended)
	return err
}

// SetUserInstallations replaces which installations a user can see. Unknown
// installations must be upserted first.
func (d *DB) SetUserInstallations(ctx context.Context, userID int64, ids []int64) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM user_installations WHERE user_id = $1`, userID); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec(ctx, `INSERT INTO user_installations (user_id, installation_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// UserInstallations lists installations a user can see.
func (d *DB) UserInstallations(ctx context.Context, userID int64) ([]Installation, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT i.id, i.account_id, i.account_login, i.account_type, i.suspended
		FROM installations i JOIN user_installations ui ON ui.installation_id = i.id
		WHERE ui.user_id = $1 ORDER BY lower(i.account_login)`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Installation, error) {
		var i Installation
		err := r.Scan(&i.ID, &i.AccountID, &i.AccountLogin, &i.AccountType, &i.Suspended)
		return i, err
	})
}

// SyncRepos makes an installation's repos exactly repos (adding, updating,
// and removing repos that are no longer accessible).
func (d *DB) SyncRepos(ctx context.Context, installationID int64, repos []Repo) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		keep := make([]int64, 0, len(repos))
		for _, r := range repos {
			if err := upsertRepo(ctx, tx, installationID, r); err != nil {
				return err
			}
			keep = append(keep, r.ID)
		}
		_, err := tx.Exec(ctx, `DELETE FROM repos WHERE installation_id = $1 AND NOT (id = ANY($2))`, installationID, keep)
		return err
	})
}

// AddRepos adds or updates repos for an installation.
func (d *DB) AddRepos(ctx context.Context, installationID int64, repos []Repo) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		for _, r := range repos {
			if err := upsertRepo(ctx, tx, installationID, r); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveRepos removes repos from an installation.
func (d *DB) RemoveRepos(ctx context.Context, installationID int64, ids []int64) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM repos WHERE installation_id = $1 AND id = ANY($2)`, installationID, ids)
	return err
}

func upsertRepo(ctx context.Context, tx pgx.Tx, installationID int64, r Repo) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO repos (id, installation_id, full_name, default_branch, private, html_url)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET installation_id = EXCLUDED.installation_id, full_name = EXCLUDED.full_name,
			default_branch = CASE WHEN EXCLUDED.default_branch = '' THEN repos.default_branch ELSE EXCLUDED.default_branch END,
			private = EXCLUDED.private,
			html_url = CASE WHEN EXCLUDED.html_url = '' THEN repos.html_url ELSE EXCLUDED.html_url END,
			updated_at = now()`,
		r.ID, installationID, r.FullName, r.DefaultBranch, r.Private, r.HTMLURL)
	return err
}

const repoCols = `r.id, r.installation_id, r.full_name, r.default_branch, r.private, r.html_url, r.updated_at`

func scanRepo(row pgx.Row) (Repo, error) {
	var r Repo
	err := row.Scan(&r.ID, &r.InstallationID, &r.FullName, &r.DefaultBranch, &r.Private, &r.HTMLURL, &r.UpdatedAt)
	return r, err
}

// visible is the access check: the user belongs to the repo's installation.
const visible = `EXISTS (SELECT 1 FROM user_installations ui WHERE ui.user_id = $1 AND ui.installation_id = r.installation_id)`

// ReposForUser lists repos a user can see.
func (d *DB) ReposForUser(ctx context.Context, userID int64) ([]Repo, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+repoCols+` FROM repos r WHERE `+visible+` ORDER BY lower(r.full_name)`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Repo, error) { return scanRepo(r) })
}

// RepoForUser returns a repo if the user can see it.
func (d *DB) RepoForUser(ctx context.Context, userID, repoID int64) (Repo, error) {
	r, err := scanRepo(d.Pool.QueryRow(ctx, `SELECT `+repoCols+` FROM repos r WHERE r.id = $2 AND `+visible, userID, repoID))
	return r, notFound(err)
}

// RepoByID returns a repo without an access check (webhooks, workers).
func (d *DB) RepoByID(ctx context.Context, repoID int64) (Repo, error) {
	r, err := scanRepo(d.Pool.QueryRow(ctx, `SELECT `+repoCols+` FROM repos r WHERE r.id = $1`, repoID))
	return r, notFound(err)
}

// RecordDelivery records a webhook delivery; false means it was seen before.
func (d *DB) RecordDelivery(ctx context.Context, deliveryID, event string) (bool, error) {
	tag, err := d.Pool.Exec(ctx, `INSERT INTO webhook_deliveries (delivery_id, event) VALUES ($1, $2) ON CONFLICT DO NOTHING`, deliveryID, event)
	return tag.RowsAffected() == 1, err
}

// DeliveryError records why processing a delivery failed.
func (d *DB) DeliveryError(ctx context.Context, deliveryID, msg string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE webhook_deliveries SET error = $2 WHERE delivery_id = $1`, deliveryID, msg)
	return err
}

// AddUserInstallationByGitHubID lets a known user see an installation (e.g.
// the user who just installed the App). Unknown users are ignored.
func (d *DB) AddUserInstallationByGitHubID(ctx context.Context, githubUserID, installationID int64) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO user_installations (user_id, installation_id)
		SELECT id, $2 FROM users WHERE github_id = $1 ON CONFLICT DO NOTHING`, githubUserID, installationID)
	return err
}

// InstallationExists reports whether an installation is known.
func (d *DB) InstallationExists(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM installations WHERE id = $1)`, id).Scan(&ok)
	return ok, err
}
