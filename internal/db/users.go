package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

// User is a signed-in GitHub user.
type User struct {
	ID        int64     `json:"id"`
	GitHubID  int64     `json:"github_id"`
	Login     string    `json:"login"`
	Name      string    `json:"name"`
	Email     string    `json:"email,omitempty"`
	AvatarURL string    `json:"avatar_url"`
	CreatedAt time.Time `json:"created_at"`
}

// UserTokens are a user's GitHub App user-to-server tokens, already encrypted.
type UserTokens struct {
	AccessEnc  []byte
	RefreshEnc []byte
	ExpiresAt  *time.Time
}

// UpsertUser creates or updates a user by GitHub id.
func (d *DB) UpsertUser(ctx context.Context, u User, t UserTokens) (User, error) {
	err := d.Pool.QueryRow(ctx, `
		INSERT INTO users (github_id, login, name, email, avatar_url, access_token_enc, refresh_token_enc, token_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (github_id) DO UPDATE SET login = EXCLUDED.login, name = EXCLUDED.name, email = EXCLUDED.email,
			avatar_url = EXCLUDED.avatar_url, access_token_enc = EXCLUDED.access_token_enc,
			refresh_token_enc = EXCLUDED.refresh_token_enc, token_expires_at = EXCLUDED.token_expires_at, updated_at = now()
		RETURNING id, github_id, login, name, email, avatar_url, created_at`,
		u.GitHubID, u.Login, u.Name, u.Email, u.AvatarURL, t.AccessEnc, t.RefreshEnc, t.ExpiresAt,
	).Scan(&u.ID, &u.GitHubID, &u.Login, &u.Name, &u.Email, &u.AvatarURL, &u.CreatedAt)
	return u, err
}

// UserTokensFor returns a user's encrypted tokens.
func (d *DB) UserTokensFor(ctx context.Context, userID int64) (UserTokens, error) {
	var t UserTokens
	err := d.Pool.QueryRow(ctx, `SELECT access_token_enc, refresh_token_enc, token_expires_at FROM users WHERE id = $1`, userID).
		Scan(&t.AccessEnc, &t.RefreshEnc, &t.ExpiresAt)
	return t, notFound(err)
}

// SetUserTokens replaces a user's encrypted tokens (after a refresh).
func (d *DB) SetUserTokens(ctx context.Context, userID int64, t UserTokens) error {
	_, err := d.Pool.Exec(ctx, `UPDATE users SET access_token_enc = $2, refresh_token_enc = $3, token_expires_at = $4, updated_at = now() WHERE id = $1`,
		userID, t.AccessEnc, t.RefreshEnc, t.ExpiresAt)
	return err
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// CreateSession starts a session and returns the raw token for the cookie.
// Only its hash is stored.
func (d *DB) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	tok, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashToken(tok), userID, time.Now().Add(ttl))
	return tok, err
}

// UserBySession resolves a session token to its user.
func (d *DB) UserBySession(ctx context.Context, token string) (User, error) {
	var u User
	err := d.Pool.QueryRow(ctx, `
		SELECT u.id, u.github_id, u.login, u.name, u.email, u.avatar_url, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, hashToken(token),
	).Scan(&u.ID, &u.GitHubID, &u.Login, &u.Name, &u.Email, &u.AvatarURL, &u.CreatedAt)
	return u, notFound(err)
}

// DeleteSession ends a session.
func (d *DB) DeleteSession(ctx context.Context, token string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}

// CreateOAuthState stores a one-time OAuth state value.
func (d *DB) CreateOAuthState(ctx context.Context) (string, error) {
	st, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO oauth_states (state) VALUES ($1)`, st)
	return st, err
}

// ConsumeOAuthState deletes a state value; it is valid only once and for 10 minutes.
func (d *DB) ConsumeOAuthState(ctx context.Context, state string) (bool, error) {
	tag, err := d.Pool.Exec(ctx, `DELETE FROM oauth_states WHERE state = $1 AND created_at > now() - interval '10 minutes'`, state)
	return tag.RowsAffected() == 1, err
}

// PurgeExpired removes expired sessions and stale OAuth states.
func (d *DB) PurgeExpired(ctx context.Context) error {
	if _, err := d.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
		return err
	}
	_, err := d.Pool.Exec(ctx, `DELETE FROM oauth_states WHERE created_at < now() - interval '10 minutes'`)
	return err
}
