package db

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testDB returns a migrated database in a fresh schema. Set
// RTFM_TEST_DATABASE_URL (e.g. postgres://rtfm:rtfm@127.0.0.1:55432/rtfm).
func testDB(t *testing.T) *DB {
	t.Helper()
	url := os.Getenv("RTFM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RTFM_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", rand.Int64N(1<<40))
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(context.Background())
	})
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	d, err := Open(ctx, url+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	return d
}

type world struct {
	alice, bob User
	repo       Repo
	qs         Quickstart
}

func setup(t *testing.T, d *DB) world {
	t.Helper()
	ctx := context.Background()
	var w world
	var err error
	if w.alice, err = d.UpsertUser(ctx, User{GitHubID: 1, Login: "alice"}, UserTokens{}); err != nil {
		t.Fatal(err)
	}
	if w.bob, err = d.UpsertUser(ctx, User{GitHubID: 2, Login: "bob"}, UserTokens{}); err != nil {
		t.Fatal(err)
	}
	must(t, d.UpsertInstallation(ctx, Installation{ID: 100, AccountID: 1, AccountLogin: "alice", AccountType: "User"}))
	must(t, d.SetUserInstallations(ctx, w.alice.ID, []int64{100}))
	must(t, d.SyncRepos(ctx, 100, []Repo{{ID: 500, FullName: "alice/app", DefaultBranch: "main"}, {ID: 501, FullName: "alice/old"}}))
	must(t, d.SyncRepos(ctx, 100, []Repo{{ID: 500, FullName: "alice/app", DefaultBranch: "main"}})) // alice/old removed
	if w.repo, err = d.RepoForUser(ctx, w.alice.ID, 500); err != nil {
		t.Fatal(err)
	}
	if w.qs, err = d.CreateQuickstart(ctx, w.alice.ID, Quickstart{RepoID: 500, DocPath: "README.md", Goal: "run it", Backend: "auto", Fix: true, Verify: true}); err != nil {
		t.Fatal(err)
	}
	return w
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionsAndAccess(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	w := setup(t, d)

	tok, err := d.CreateSession(ctx, w.alice.ID, time.Hour)
	must(t, err)
	if u, err := d.UserBySession(ctx, tok); err != nil || u.Login != "alice" {
		t.Fatalf("session: %v %v", u, err)
	}
	must(t, d.DeleteSession(ctx, tok))
	if _, err := d.UserBySession(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted session still valid: %v", err)
	}
	st, err := d.CreateOAuthState(ctx)
	must(t, err)
	if ok, _ := d.ConsumeOAuthState(ctx, st); !ok {
		t.Error("state should be valid once")
	}
	if ok, _ := d.ConsumeOAuthState(ctx, st); ok {
		t.Error("state reused")
	}

	repos, _ := d.ReposForUser(ctx, w.alice.ID)
	if len(repos) != 1 || repos[0].FullName != "alice/app" {
		t.Errorf("alice repos = %+v", repos)
	}
	// Bob is not in alice's installation: he sees nothing and can't touch anything.
	if repos, _ := d.ReposForUser(ctx, w.bob.ID); len(repos) != 0 {
		t.Errorf("bob sees %+v", repos)
	}
	if _, err := d.QuickstartForUser(ctx, w.bob.ID, w.qs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob read alice's quickstart: %v", err)
	}
	if _, err := d.CreateQuickstart(ctx, w.bob.ID, Quickstart{RepoID: 500, DocPath: "x.md", Goal: "g"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob created a quickstart in alice's repo: %v", err)
	}
	if err := d.DeleteQuickstart(ctx, w.bob.ID, w.qs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob deleted alice's quickstart: %v", err)
	}
	run, _ := d.Enqueue(ctx, w.qs, "manual", &w.alice.ID, "main", "")
	if _, err := d.RunDetailForUser(ctx, w.bob.ID, run.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob read alice's run: %v", err)
	}
	if runs, _ := d.ListRuns(ctx, w.bob.ID, RunFilter{}); len(runs) != 0 {
		t.Errorf("bob lists %d runs", len(runs))
	}
}

func TestTriggers(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	w := setup(t, d)
	tr, err := d.CreateTrigger(ctx, w.alice.ID, Trigger{QuickstartID: w.qs.ID, Kind: "push", Branches: []string{"main"}, Paths: []string{"README.md", "src/**"}, Enabled: true})
	must(t, err)
	pts, err := d.PushTriggersForRepo(ctx, 500)
	must(t, err)
	if len(pts) != 1 || pts[0].Quickstart.ID != w.qs.ID || len(pts[0].Trigger.Paths) != 2 {
		t.Fatalf("push triggers = %+v", pts)
	}
	tr.Enabled = false
	_, err = d.UpdateTrigger(ctx, w.alice.ID, tr)
	must(t, err)
	if pts, _ := d.PushTriggersForRepo(ctx, 500); len(pts) != 0 {
		t.Error("disabled trigger still matched")
	}
}

func TestQueueClaimIsExclusive(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	w := setup(t, d)
	for i := 0; i < 5; i++ {
		_, err := d.Enqueue(ctx, w.qs, "manual", &w.alice.ID, "main", "")
		must(t, err)
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for {
				j, err := d.Claim(ctx, fmt.Sprintf("w%d", i))
				if errors.Is(err, ErrNotFound) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				seen[j.Run.ID]++
				mu.Unlock()
				if j.Repo.FullName != "alice/app" || j.Quickstart.DocPath != "README.md" {
					t.Errorf("job = %+v", j)
				}
			}
		})
	}
	wg.Wait()
	if len(seen) != 5 {
		t.Errorf("claimed %d distinct runs, want 5", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("run %d claimed %d times", id, n)
		}
	}
}

func TestFinishStepsAndHealth(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	w := setup(t, d)
	zero, two := 0, 2
	steps := func(flagOK bool) []Step {
		s3 := Step{Stage: "newhire", Idx: 3, StepKey: "k-hello", DocCommand: "hello --user x", Command: "hello --name x",
			Status: "unsupported", Failure: "stale_command", ObservedExit: &two, ChecksOut: false}
		if flagOK {
			s3 = Step{Stage: "newhire", Idx: 3, StepKey: "k-hello", DocCommand: "hello --user x", Command: "hello --user x",
				Status: "pass", ObservedExit: &zero, ChecksOut: true}
		}
		return []Step{
			{Stage: "newhire", Idx: 1, StepKey: "k-venv", DocCommand: "python3 -m venv .venv", Status: "pass", ObservedExit: &zero, ChecksOut: true},
			s3,
			{Stage: "verify", Idx: 3, StepKey: "k-hello", DocCommand: "hello --name x", Status: "pass", ChecksOut: true},
		}
	}
	run1, _ := d.Enqueue(ctx, w.qs, "manual", &w.alice.ID, "main", "")
	j, err := d.Claim(ctx, "w")
	must(t, err)
	must(t, d.Finish(ctx, j.Run.ID, Outcome{CommitSHA: "aaa111", Verdict: "works", Issues: 1, FixStatus: "verified", Report: map[string]any{"id": "r1"}, Steps: steps(false)}))

	run2, _ := d.Enqueue(ctx, w.qs, "push", nil, "main", "bbb222")
	_, err = d.Claim(ctx, "w")
	must(t, err)
	must(t, d.Finish(ctx, run2.ID, Outcome{CommitSHA: "bbb222", Verdict: "broken", Issues: 1, Report: map[string]any{"id": "r2"}, Steps: []Step{
		{Stage: "newhire", Idx: 1, StepKey: "k-venv", DocCommand: "python3 -m venv .venv", Status: "fail", Failure: "missing_prereq", ObservedExit: &two},
	}}))

	det, err := d.RunDetailForUser(ctx, w.alice.ID, run1.ID)
	must(t, err)
	if det.CommitSHA != "aaa111" || det.Verdict != "works" || len(det.Steps) != 3 || string(det.Report) == "" {
		t.Fatalf("detail = %+v", det)
	}
	if det.Steps[2].Stage != "verify" {
		t.Errorf("verify steps should come last: %+v", det.Steps)
	}

	health, err := d.StepHealthForQuickstart(ctx, w.alice.ID, w.qs.ID)
	must(t, err)
	byKey := map[string]StepHealth{}
	for _, h := range health {
		byKey[h.StepKey] = h
	}
	venv := byKey["k-venv"]
	if venv.Status != "fail" || venv.CommitSHA != "bbb222" || venv.LastVerifiedCommit == nil || *venv.LastVerifiedCommit != "aaa111" {
		t.Errorf("venv health = %+v", venv)
	}
	hello := byKey["k-hello"]
	if hello.Status != "unsupported" || hello.LastVerifiedRunID != nil {
		t.Errorf("hello health = %+v (verify-stage passes must not count)", hello)
	}

	evs, err := d.EventsSince(ctx, w.alice.ID, run1.ID, 0)
	must(t, err)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "queued,started,finished" {
		t.Errorf("events = %v", kinds)
	}

	// A run canceled partway keeps its findings, is recorded as canceled,
	// and doesn't count toward step health.
	run3, _ := d.Enqueue(ctx, w.qs, "manual", &w.alice.ID, "main", "")
	_, err = d.Claim(ctx, "w")
	must(t, err)
	must(t, d.Finish(ctx, run3.ID, Outcome{CommitSHA: "ccc333", Verdict: "broken", Issues: 1, FixStatus: "error", Report: map[string]any{"id": "r3"}, Canceled: true,
		Steps: []Step{{Stage: "newhire", Idx: 1, StepKey: "k-venv", DocCommand: "python3 -m venv .venv", Status: "pass", ObservedExit: &zero, ChecksOut: true}}}))
	det3, err := d.RunDetailForUser(ctx, w.alice.ID, run3.ID)
	must(t, err)
	if det3.Status != RunCanceled || det3.Verdict != "broken" || len(det3.Steps) != 1 {
		t.Errorf("canceled run = %+v", det3.Run)
	}
	health, _ = d.StepHealthForQuickstart(ctx, w.alice.ID, w.qs.ID)
	for _, h := range health {
		if h.StepKey == "k-venv" && h.CommitSHA == "ccc333" {
			t.Errorf("canceled run counted in step health: %+v", h)
		}
	}
	evs, _ = d.EventsSince(ctx, w.alice.ID, run3.ID, 0)
	if last := evs[len(evs)-1].Kind; last != "canceled" {
		t.Errorf("last event = %s", last)
	}
}

func TestCancelAndCredentials(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	w := setup(t, d)
	run, _ := d.Enqueue(ctx, w.qs, "manual", &w.alice.ID, "main", "")
	r, err := d.CancelRun(ctx, w.alice.ID, run.ID)
	must(t, err)
	if r.Status != RunCanceled {
		t.Errorf("queued run should cancel immediately: %s", r.Status)
	}
	if _, err := d.Claim(ctx, "w"); !errors.Is(err, ErrNotFound) {
		t.Error("canceled run was claimed")
	}

	must(t, d.SetCredential(ctx, w.alice.ID, w.qs.ID, "API_KEY", []byte("enc1")))
	must(t, d.SetCredential(ctx, w.alice.ID, w.qs.ID, "API_KEY", []byte("enc2")))
	if names, _ := d.CredentialNames(ctx, w.alice.ID, w.qs.ID); len(names) != 1 || names[0] != "API_KEY" {
		t.Errorf("names = %v", names)
	}
	if v, ok, _ := d.CredentialValue(ctx, w.qs.ID, "API_KEY"); !ok || string(v) != "enc2" {
		t.Errorf("value = %q %v", v, ok)
	}
	if err := d.SetCredential(ctx, w.bob.ID, w.qs.ID, "X", []byte("y")); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob set a credential on alice's quickstart: %v", err)
	}
	if ok, _ := d.RecordDelivery(ctx, "d1", "push"); !ok {
		t.Error("first delivery should record")
	}
	if ok, _ := d.RecordDelivery(ctx, "d1", "push"); ok {
		t.Error("duplicate delivery recorded")
	}
}
