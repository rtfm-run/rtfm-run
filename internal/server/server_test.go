package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"rtfm.run/internal/db"
	"rtfm.run/internal/ghapp"
	"rtfm.run/internal/secretbox"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	u := os.Getenv("RTFM_TEST_DATABASE_URL")
	if u == "" {
		t.Skip("RTFM_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", mrand.Int64N(1<<40))
	conn, err := pgx.Connect(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	conn.Exec(ctx, "CREATE SCHEMA "+schema)
	t.Cleanup(func() {
		conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(context.Background())
	})
	d, err := db.Open(ctx, u+"?search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

// fakeGitHub serves the OAuth, user and installation endpoints. extra, if
// set, gets the first chance at each request (returning true if it handled it).
func fakeGitHub(t *testing.T, extra func(http.ResponseWriter, *http.Request) bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extra != nil && extra(w, r) {
			return
		}
		auth := r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/login/oauth/access_token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "ghu_alice", "refresh_token": "ghr", "expires_in": 28800})
		case r.URL.Path == "/user" && auth == "Bearer ghu_alice":
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "login": "alice", "name": "Alice"})
		case r.URL.Path == "/user/installations" && auth == "Bearer ghu_alice":
			json.NewEncoder(w).Encode(map[string]any{"installations": []map[string]any{{"id": 100, "account": map[string]any{"id": 1, "login": "alice", "type": "User"}}}})
		case r.URL.Path == "/app/installations/100/access_tokens":
			json.NewEncoder(w).Encode(map[string]any{"token": "ghs_100", "expires_at": time.Now().Add(time.Hour)})
		case r.URL.Path == "/installation/repositories" && auth == "Bearer ghs_100":
			json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []map[string]any{
				{"id": 500, "full_name": "alice/app", "default_branch": "main", "private": true}}})
		default:
			t.Logf("fake github: unhandled %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
}

type env struct {
	t      *testing.T
	d      *db.DB
	api    *httptest.Server
	client *http.Client
	app    *ghapp.App
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

func newEnvWith(t *testing.T, extra func(http.ResponseWriter, *http.Request) bool) *env {
	d := testDB(t)
	gh := fakeGitHub(t, extra)
	t.Cleanup(gh.Close)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	app := &ghapp.App{ID: 1, Slug: "rtfm-test", ClientID: "Iv1", ClientSecret: "s", WebhookSecret: "whsec", Key: key, APIBase: gh.URL, WebBase: gh.URL}
	k := make([]byte, 32)
	rand.Read(k)
	box, _ := secretbox.New(base64.StdEncoding.EncodeToString(k))
	s := &Server{DB: d, App: app, Secrets: box, DashboardURL: "https://dash.example", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	api := httptest.NewServer(nil)
	s.BaseURL = api.URL
	api.Config.Handler = s.Handler()
	t.Cleanup(api.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &env{t: t, d: d, api: api, client: client, app: app}
}

func (e *env) do(method, path, ctype string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.api.URL+path, rd)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (e *env) signIn() {
	e.t.Helper()
	res, err := e.client.Get(e.api.URL + "/auth/github/login")
	if err != nil || res.StatusCode != 302 {
		e.t.Fatalf("login: %v %v", res.StatusCode, err)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	state := loc.Query().Get("state")
	if state == "" || loc.Query().Get("client_id") != "Iv1" {
		e.t.Fatalf("authorize url = %s", loc)
	}
	res, err = e.client.Get(e.api.URL + "/auth/github/callback?code=c&state=" + url.QueryEscape(state))
	if err != nil || res.StatusCode != 302 || res.Header.Get("Location") != "https://dash.example" {
		e.t.Fatalf("callback: %v %v %s", res.StatusCode, err, res.Header.Get("Location"))
	}
	// The state is single-use; failures go back to the dashboard's sign-in
	// page with a reason rather than showing API JSON.
	res, _ = e.client.Get(e.api.URL + "/auth/github/callback?code=c&state=" + url.QueryEscape(state))
	if loc := res.Header.Get("Location"); res.StatusCode != 302 || !strings.HasPrefix(loc, "https://dash.example/login?error=") {
		e.t.Errorf("reused state: %d %s", res.StatusCode, loc)
	}
	res, _ = e.client.Get(e.api.URL + "/")
	if res.StatusCode != 302 || res.Header.Get("Location") != "https://dash.example" {
		e.t.Errorf("api root: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func (e *env) webhook(event, delivery string, payload any, secret string) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	req, _ := http.NewRequest("POST", e.api.URL+"/webhooks/github", bytes.NewReader(b))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func push(ref, after string, files ...string) map[string]any {
	return map[string]any{"ref": ref, "after": after,
		"repository":   map[string]any{"id": 500, "full_name": "alice/app", "default_branch": "main"},
		"installation": map[string]any{"id": 100},
		"commits":      []map[string]any{{"modified": files}}}
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do("GET", "/api/me", "", nil); code != 401 {
		t.Fatalf("unauthenticated /api/me = %d", code)
	}
	e.signIn()

	code, me := e.do("GET", "/api/me", "", nil)
	if code != 200 || me["user"].(map[string]any)["login"] != "alice" || !strings.Contains(me["install_url"].(string), "/apps/rtfm-test/") {
		t.Fatalf("me = %d %v", code, me)
	}
	_, repos := e.do("GET", "/api/repos", "", nil)
	rl := repos["repos"].([]any)
	if len(rl) != 1 || rl[0].(map[string]any)["full_name"] != "alice/app" {
		t.Fatalf("repos = %v", repos)
	}

	// CSRF guard: cookie-authenticated writes must be JSON.
	if code, _ := e.do("POST", "/api/repos/500/quickstarts", "text/plain", map[string]any{}); code != 415 {
		t.Errorf("non-JSON write = %d, want 415", code)
	}
	if code, out := e.do("POST", "/api/repos/500/quickstarts", "application/json", map[string]any{"doc_path": "../etc/passwd", "goal": "x"}); code != 422 {
		t.Errorf("bad doc path = %d %v", code, out)
	}
	if code, out := e.do("POST", "/api/repos/500/quickstarts", "application/json", map[string]any{"doc_path": "docs/other.md"}); code != 201 || out["goal"] != DefaultGoal {
		t.Errorf("missing goal should get the default = %d %v", code, out)
	}
	code, qs := e.do("POST", "/api/repos/500/quickstarts", "application/json", map[string]any{"doc_path": "README.md", "goal": "start the server", "success_check": "curl -fsS localhost:8000"})
	if code != 201 || qs["backend"] != "auto" || qs["fix"] != true {
		t.Fatalf("create quickstart = %d %v", code, qs)
	}
	qsID := int64(qs["id"].(float64))
	if code, _ := e.do("POST", "/api/repos/500/quickstarts", "application/json", map[string]any{"doc_path": "README.md", "goal": "again"}); code != 409 {
		t.Errorf("duplicate quickstart = %d", code)
	}
	if code, out := e.do("PATCH", fmt.Sprintf("/api/quickstarts/%d", qsID), "application/json", map[string]any{"backend": "vmware"}); code != 422 {
		t.Errorf("bad backend = %d %v", code, out)
	}
	code, tr := e.do("POST", fmt.Sprintf("/api/quickstarts/%d/triggers", qsID), "application/json", map[string]any{"branches": []string{"main"}, "paths": []string{"README.md", "docs/**"}})
	if code != 201 {
		t.Fatalf("create trigger = %d %v", code, tr)
	}

	// Webhooks.
	if code, _ := e.webhook("push", "d0", push("refs/heads/main", "aaaa1111", "README.md"), "wrong-secret"); code != 401 {
		t.Errorf("bad signature = %d", code)
	}
	if code, out := e.webhook("push", "d1", push("refs/heads/main", "aaaa1111", "docs/install.md"), "whsec"); code != 202 || out["enqueued"].(float64) != 1 {
		t.Errorf("matching push = %d %v", code, out)
	}
	if _, out := e.webhook("push", "d1", push("refs/heads/main", "aaaa1111", "docs/install.md"), "whsec"); out["status"] != "duplicate delivery" {
		t.Errorf("redelivery = %v", out)
	}
	if _, out := e.webhook("push", "d2", push("refs/heads/main", "bbbb2222", "src/server.go"), "whsec"); out["enqueued"].(float64) != 0 {
		t.Errorf("unrelated paths enqueued: %v", out)
	}
	if _, out := e.webhook("push", "d3", push("refs/heads/feature", "cccc3333", "README.md"), "whsec"); out["enqueued"].(float64) != 0 {
		t.Errorf("other branch enqueued: %v", out)
	}
	if _, out := e.webhook("push", "d4", push("refs/heads/main", "aaaa1111", "README.md"), "whsec"); out["enqueued"].(float64) != 0 {
		t.Errorf("same commit enqueued twice: %v", out)
	}
	if _, out := e.webhook("push", "d5", push("refs/tags/v1", "dddd4444", "README.md"), "whsec"); out["enqueued"].(float64) != 0 {
		t.Errorf("tag push enqueued: %v", out)
	}
	if code, _ := e.webhook("ping", "d6", map[string]any{"zen": "hi"}, "whsec"); code != 202 {
		t.Errorf("ping = %d", code)
	}

	// A manual run, then a worker finishing the push run.
	if code, out := e.do("POST", fmt.Sprintf("/api/quickstarts/%d/runs", qsID), "application/json", map[string]any{"commit": "not-hex"}); code != 422 {
		t.Errorf("bad commit = %d %v", code, out)
	}
	code, manual := e.do("POST", fmt.Sprintf("/api/quickstarts/%d/runs", qsID), "application/json", map[string]any{})
	if code != 202 || manual["status"] != "queued" || manual["trigger"] != "manual" {
		t.Fatalf("manual run = %d %v", code, manual)
	}
	ctx := context.Background()
	job, err := e.d.Claim(ctx, "test-worker")
	if err != nil || job.Run.Trigger != "push" || job.Run.CommitSHA != "aaaa1111" {
		t.Fatalf("claimed %+v %v (want the push run first)", job.Run, err)
	}
	zero := 0
	if err := e.d.Finish(ctx, job.Run.ID, db.Outcome{CommitSHA: "aaaa1111", Verdict: "works", Report: map[string]any{"id": "r"},
		Steps: []db.Step{{Stage: "newhire", Idx: 1, StepKey: "k1", DocCommand: "npm start", Command: "npm start", Status: "pass", ObservedExit: &zero, ChecksOut: true}}}); err != nil {
		t.Fatal(err)
	}
	code, det := e.do("GET", fmt.Sprintf("/api/runs/%d", job.Run.ID), "", nil)
	if code != 200 || det["verdict"] != "works" || len(det["steps"].([]any)) != 1 {
		t.Errorf("run detail = %d %v", code, det)
	}
	_, health := e.do("GET", fmt.Sprintf("/api/quickstarts/%d/steps", qsID), "", nil)
	hs := health["steps"].([]any)
	if len(hs) != 1 || hs[0].(map[string]any)["last_verified_commit"] != "aaaa1111" {
		t.Errorf("step health = %v", health)
	}
	_, runs := e.do("GET", fmt.Sprintf("/api/runs?quickstart=%d", qsID), "", nil)
	if len(runs["runs"].([]any)) != 2 {
		t.Errorf("runs = %v", runs)
	}

	// The event stream replays the run's events and ends.
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/runs/%d/events", e.api.URL, job.Run.ID), nil)
	res, err := e.client.Do(req)
	if err != nil || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %v %v", err, res.Header)
	}
	var kinds []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if k, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			kinds = append(kinds, k)
		}
	}
	res.Body.Close()
	if strings.Join(kinds, ",") != "queued,started,finished,end" {
		t.Errorf("event kinds = %v", kinds)
	}

	// Another user can't see alice's data, even with a valid session.
	bob, _ := e.d.UpsertUser(ctx, db.User{GitHubID: 2, Login: "bob"}, db.UserTokens{})
	tok, _ := e.d.CreateSession(ctx, bob.ID, time.Hour)
	for _, p := range []string{"/api/repos/500", fmt.Sprintf("/api/quickstarts/%d", qsID), fmt.Sprintf("/api/runs/%d", job.Run.ID), fmt.Sprintf("/api/quickstarts/%d/steps", qsID)} {
		req, _ := http.NewRequest("GET", e.api.URL+p, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, _ := http.DefaultClient.Do(req)
		if res.StatusCode != 404 {
			t.Errorf("bob GET %s = %d, want 404", p, res.StatusCode)
		}
		res.Body.Close()
	}

	// Uninstalling the App removes the repo and everything under it.
	if code, _ := e.webhook("installation", "d7", map[string]any{"action": "deleted", "installation": map[string]any{"id": 100, "account": map[string]any{"id": 1, "login": "alice", "type": "User"}}}, "whsec"); code != 202 {
		t.Errorf("uninstall = %d", code)
	}
	if _, repos := e.do("GET", "/api/repos", "", nil); len(repos["repos"].([]any)) != 0 {
		t.Errorf("repos after uninstall = %v", repos)
	}
}
