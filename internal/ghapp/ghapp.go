// Package ghapp is rtfm's GitHub App: app JWTs, cached installation
// tokens, the user sign-in (user-to-server OAuth) flow, and webhook
// verification.
package ghapp

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rtfm.run/internal/github"
)

// App holds the GitHub App's credentials.
type App struct {
	ID            int64
	Slug          string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	Key           *rsa.PrivateKey

	APIBase string // default https://api.github.com
	WebBase string // default https://github.com
	HTTP    *http.Client

	mu     sync.Mutex
	tokens map[int64]cachedToken
}

type cachedToken struct {
	token string
	exp   time.Time
}

// ParseKey parses a PEM-encoded RSA private key (PKCS#1 or PKCS#8), as
// downloaded from the App's settings.
func ParseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("ghapp: no PEM block in private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ghapp: parse private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("ghapp: private key is not RSA")
	}
	return rk, nil
}

func (a *App) api() string {
	return strings.TrimRight(orDefault(a.APIBase, "https://api.github.com"), "/")
}
func (a *App) web() string { return strings.TrimRight(orDefault(a.WebBase, "https://github.com"), "/") }
func (a *App) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// JWT returns a short-lived app JWT (RS256) for app-level API calls.
func (a *App) JWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(), // allow for clock drift
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": fmt.Sprint(a.ID),
	})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + enc.EncodeToString(sig), nil
}

func (a *App) appClient() (*github.Client, error) {
	jwt, err := a.JWT(time.Now())
	if err != nil {
		return nil, err
	}
	return &github.Client{Token: jwt, BaseURL: a.api(), HTTP: a.HTTP}, nil
}

// InstallationToken returns a token for an installation, cached until
// shortly before it expires (they last an hour).
func (a *App) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	a.mu.Lock()
	if t, ok := a.tokens[installationID]; ok && time.Until(t.exp) > 5*time.Minute {
		a.mu.Unlock()
		return t.token, nil
	}
	a.mu.Unlock()
	c, err := a.appClient()
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.Do(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installationID), map[string]any{}, &out); err != nil {
		return "", fmt.Errorf("ghapp: installation token: %w", err)
	}
	a.mu.Lock()
	if a.tokens == nil {
		a.tokens = map[int64]cachedToken{}
	}
	a.tokens[installationID] = cachedToken{out.Token, out.ExpiresAt}
	a.mu.Unlock()
	return out.Token, nil
}

// InstallationClient returns an API client acting as the installation.
func (a *App) InstallationClient(ctx context.Context, installationID int64) (*github.Client, error) {
	tok, err := a.InstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return &github.Client{Token: tok, BaseURL: a.api(), HTTP: a.HTTP}, nil
}

// Account is a GitHub user or organization.
type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

// Installation is an App installation.
type Installation struct {
	ID          int64   `json:"id"`
	Account     Account `json:"account"`
	SuspendedAt *string `json:"suspended_at"`
}

// Repository is a repo as GitHub returns it.
type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
}

// InstallationRepos lists every repo an installation can access.
func (a *App) InstallationRepos(ctx context.Context, installationID int64) ([]Repository, error) {
	c, err := a.InstallationClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var all []Repository
	for page := 1; page <= 50; page++ {
		var out struct {
			TotalCount   int          `json:"total_count"`
			Repositories []Repository `json:"repositories"`
		}
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Repositories...)
		if len(out.Repositories) < 100 || len(all) >= out.TotalCount {
			break
		}
	}
	return all, nil
}

// InstallURL is where a user installs the App on their repos.
func (a *App) InstallURL() string { return a.web() + "/apps/" + a.Slug + "/installations/new" }

// AuthorizeURL starts the user sign-in flow.
func (a *App) AuthorizeURL(state, redirectURI string) string {
	q := url.Values{"client_id": {a.ClientID}, "state": {state}, "redirect_uri": {redirectURI}}
	return a.web() + "/login/oauth/authorize?" + q.Encode()
}

// UserToken is a user-to-server token pair.
type UserToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    *time.Time // nil if the App doesn't expire user tokens
}

func (a *App) tokenRequest(ctx context.Context, form url.Values) (UserToken, error) {
	form.Set("client_id", a.ClientID)
	form.Set("client_secret", a.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.web()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return UserToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := a.client().Do(req)
	if err != nil {
		return UserToken{}, err
	}
	defer res.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err := json.Unmarshal(body, &out); err != nil {
		return UserToken{}, fmt.Errorf("ghapp: token response: %w", err)
	}
	if out.Error != "" || out.AccessToken == "" {
		return UserToken{}, fmt.Errorf("ghapp: token exchange failed: %s %s", out.Error, out.ErrorDesc)
	}
	t := UserToken{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken}
	if out.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
		t.ExpiresAt = &exp
	}
	return t, nil
}

// ExchangeCode completes sign-in.
func (a *App) ExchangeCode(ctx context.Context, code, redirectURI string) (UserToken, error) {
	return a.tokenRequest(ctx, url.Values{"code": {code}, "redirect_uri": {redirectURI}})
}

// Refresh renews an expiring user token.
func (a *App) Refresh(ctx context.Context, refreshToken string) (UserToken, error) {
	return a.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
}

// User is a GitHub user profile.
type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

// UserProfile fetches the signed-in user.
func (a *App) UserProfile(ctx context.Context, userToken string) (User, error) {
	var u User
	err := (&github.Client{Token: userToken, BaseURL: a.api(), HTTP: a.HTTP}).Do(ctx, http.MethodGet, "/user", nil, &u)
	return u, err
}

// UserInstallations lists installations of this App the user can access.
func (a *App) UserInstallations(ctx context.Context, userToken string) ([]Installation, error) {
	c := &github.Client{Token: userToken, BaseURL: a.api(), HTTP: a.HTTP}
	var all []Installation
	for page := 1; page <= 20; page++ {
		var out struct {
			Installations []Installation `json:"installations"`
		}
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Installations...)
		if len(out.Installations) < 100 {
			break
		}
	}
	return all, nil
}

// VerifySignature checks a webhook's X-Hub-Signature-256 header.
func (a *App) VerifySignature(body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok || a.WebhookSecret == "" {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.WebhookSecret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
