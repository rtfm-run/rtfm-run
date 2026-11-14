package ghapp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"rtfm.run/internal/github"
)

// Manifest is a GitHub App manifest: everything GitHub needs to register the
// App in one click (https://docs.github.com/apps/sharing-github-apps/registering-a-github-app-from-a-manifest).
type Manifest struct {
	Name                  string            `json:"name"`
	URL                   string            `json:"url"`
	Description           string            `json:"description,omitempty"`
	HookAttributes        HookAttributes    `json:"hook_attributes"`
	RedirectURL           string            `json:"redirect_url"`
	CallbackURLs          []string          `json:"callback_urls"`
	SetupURL              string            `json:"setup_url,omitempty"`
	SetupOnUpdate         bool              `json:"setup_on_update"`
	RequestOAuthOnInstall bool              `json:"request_oauth_on_install"`
	Public                bool              `json:"public"`
	DefaultPermissions    map[string]string `json:"default_permissions"`
	DefaultEvents         []string          `json:"default_events"`
}

// HookAttributes configures the App's webhook.
type HookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// Permissions rtfm needs on installed repos: read the docs at a commit
// (contents), push fix branches and open PRs (contents, pull_requests).
// installation and installation_repositories events are always delivered.
var (
	DefaultPermissions = map[string]string{"metadata": "read", "contents": "write", "pull_requests": "write"}
	DefaultEvents      = []string{"push"}
)

// NewManifest describes rtfm's App for a service at baseURL (sign-in
// callbacks; may be localhost in development, since they're browser
// redirects). webhookURL must be reachable from GitHub; "" means
// baseURL/webhooks/github. redirectURL receives the one-time conversion code
// after the App is created.
func NewManifest(name, baseURL, webhookURL, redirectURL string, public bool) Manifest {
	base := strings.TrimRight(baseURL, "/")
	if webhookURL == "" {
		webhookURL = base + "/webhooks/github"
	}
	return Manifest{
		Name:           name,
		URL:            base,
		Description:    "Tests your quickstart docs like a new developer would, and proposes verified fixes.",
		HookAttributes: HookAttributes{URL: webhookURL, Active: true},
		RedirectURL:    redirectURL,
		CallbackURLs:   []string{base + "/auth/github/callback"},
		// After installing (or changing repos), sign in again so the
		// dashboard picks up the new installation right away.
		SetupURL:           base + "/auth/github/login",
		SetupOnUpdate:      true,
		Public:             public,
		DefaultPermissions: DefaultPermissions,
		DefaultEvents:      DefaultEvents,
	}
}

// ManifestFormURL is where the manifest is POSTed (as the form field
// "manifest"): the user's account, or an organization they administer.
func ManifestFormURL(webBase, org, state string) string {
	web := strings.TrimRight(orDefault(webBase, "https://github.com"), "/")
	path := "/settings/apps/new"
	if org != "" {
		path = "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return web + path + "?state=" + url.QueryEscape(state)
}

// Created is the registered App's credentials, returned once by the
// manifest conversion.
type Created struct {
	ID            int64   `json:"id"`
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	Owner         Account `json:"owner"`
	HTMLURL       string  `json:"html_url"`
	ClientID      string  `json:"client_id"`
	ClientSecret  string  `json:"client_secret"`
	WebhookSecret string  `json:"webhook_secret"`
	PEM           string  `json:"pem"`
}

// ConvertManifest exchanges the code GitHub sent to the redirect URL for
// the new App's credentials. The code is single-use and expires in an hour.
func ConvertManifest(ctx context.Context, apiBase string, hc *http.Client, code string) (Created, error) {
	var out Created
	if code == "" || strings.ContainsAny(code, "/?#") {
		return out, fmt.Errorf("ghapp: invalid manifest code")
	}
	c := &github.Client{BaseURL: orDefault(apiBase, "https://api.github.com"), HTTP: hc}
	if err := c.Do(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", nil, &out); err != nil {
		return out, fmt.Errorf("ghapp: manifest conversion: %w", err)
	}
	if out.ID == 0 || out.PEM == "" || out.ClientSecret == "" {
		return out, fmt.Errorf("ghapp: manifest conversion returned incomplete credentials")
	}
	if _, err := ParseKey([]byte(out.PEM)); err != nil {
		return out, err
	}
	return out, nil
}

// SetWebhookURL points the App's webhook somewhere else (e.g. a restarted
// dev tunnel). The secret and content type are left unchanged.
func (a *App) SetWebhookURL(ctx context.Context, hookURL string) error {
	c, err := a.appClient()
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPatch, "/app/hook/config", map[string]string{"url": hookURL, "content_type": "json"}, nil)
}
