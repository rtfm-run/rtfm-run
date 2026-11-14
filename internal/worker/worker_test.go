package worker

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"rtfm.run/internal/db"
	"rtfm.run/internal/devbox"
	"rtfm.run/internal/ghapp"
	"rtfm.run/internal/runner"
	"rtfm.run/internal/secretbox"
)

// TestProcessRealRun runs a real quickstart through the worker: real
// Postgres, real pipeline, real devbox. The GitHub App is faked by an
// endpoint that hands out RTFM_TEST_GITHUB_TOKEN as the installation
// token. Set RTFM_TEST_REPO (owner/name) to the repo to test.
func TestProcessRealRun(t *testing.T) {
	dbURL, tok, repoName := os.Getenv("RTFM_TEST_DATABASE_URL"), os.Getenv("RTFM_TEST_GITHUB_TOKEN"), os.Getenv("RTFM_TEST_REPO")
	if dbURL == "" || tok == "" || repoName == "" || testing.Short() {
		t.Skip("set RTFM_TEST_DATABASE_URL, RTFM_TEST_GITHUB_TOKEN and RTFM_TEST_REPO")
	}
	ctx := context.Background()
	// Safety nets: never more than 2 boxes, and the MCP server must be a
	// real rtfm binary (a test binary there re-runs this test).
	t.Setenv("RTFM_MAX_BOXES", "2")
	bin := filepath.Join(t.TempDir(), "rtfm")
	if out, err := exec.Command("go", "build", "-o", bin, "rtfm.run/cmd/rtfm").CombinedOutput(); err != nil {
		t.Fatalf("build rtfm: %v: %s", err, out)
	}
	schema := fmt.Sprintf("test_%d", mrand.Int64N(1<<40))
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	conn.Exec(ctx, "CREATE SCHEMA "+schema)
	t.Cleanup(func() {
		conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(context.Background())
	})
	d, err := db.Open(ctx, dbURL+"?search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour)})
	}))
	defer fake.Close()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	app := &ghapp.App{ID: 1, Key: key, APIBase: fake.URL}
	k := make([]byte, 32)
	rand.Read(k)
	box, _ := secretbox.New(base64.StdEncoding.EncodeToString(k))

	u, _ := d.UpsertUser(ctx, db.User{GitHubID: 9, Login: "tester"}, db.UserTokens{})
	if err := d.UpsertInstallation(ctx, db.Installation{ID: 1, AccountID: 9, AccountLogin: "tester", AccountType: "User"}); err != nil {
		t.Fatal(err)
	}
	d.SetUserInstallations(ctx, u.ID, []int64{1})
	d.SyncRepos(ctx, 1, []db.Repo{{ID: 77, FullName: repoName, DefaultBranch: "main", Private: true}})
	q, err := d.CreateQuickstart(ctx, u.ID, db.Quickstart{RepoID: 77, DocPath: "README.md", Goal: "install the CLI and greet 'newhire'",
		SuccessCheck: `.venv/bin/hello --name newhire | grep -q "Hello, newhire!"`, Backend: "auto", Fix: true, Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := d.Enqueue(ctx, q, "manual", &u.ID, "", "")
	job, err := d.Claim(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{DB: d, App: app, Secrets: box, Log: slog.Default(), Name: "test", WorkRoot: t.TempDir(), OutDir: t.TempDir(),
		DefaultModel: "gemini-3.8-flash-medium", Timeout: 10 * time.Minute,
		NewBackend: func(ctx context.Context, name string) (devbox.Backend, error) {
			fc := devbox.Firecracker{}
			if fc.Ready(ctx) == nil {
				return fc, nil
			}
			return devbox.Docker{}, devbox.Docker{}.Ready(ctx)
		},
		NewRunner: func(model string) runner.Runner {
			return runner.Agy{Model: model, Timeout: 10 * time.Minute, SelfBin: bin}
		},
	}
	Process(ctx, cfg, job, slog.Default())

	det, err := d.RunDetailForUser(ctx, u.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%s verdict=%s commit=%s issues=%d fix=%s tokens=%d error=%q steps=%d", det.Status, det.Verdict, det.CommitSHA, det.Issues, det.FixStatus, det.Tokens, det.Error, len(det.Steps))
	for _, s := range det.Steps {
		obs := "-"
		if s.ObservedExit != nil {
			obs = fmt.Sprint(*s.ObservedExit)
		}
		t.Logf("  %-7s #%d %-11s checks_out=%-5v observed_exit=%s doc=%q ran=%q", s.Stage, s.Idx, s.Status, s.ChecksOut, obs, s.DocCommand, s.Command)
	}
	if det.Status != db.RunSucceeded || det.CommitSHA == "" || len(det.Steps) == 0 {
		t.Fatalf("run did not complete properly: %+v", det.Run)
	}
	health, err := d.StepHealthForQuickstart(ctx, u.ID, q.ID)
	if err != nil || len(health) == 0 {
		t.Fatalf("step health = %v %v", health, err)
	}
	evs, _ := d.EventsSince(ctx, u.ID, run.ID, 0)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	t.Logf("events: %v", kinds)
}
