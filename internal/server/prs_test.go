package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"rtfm.run/internal/db"
	"rtfm.run/internal/guidelines"
	"rtfm.run/internal/prs"
)

// fakeRepo is just enough of GitHub's repository API for prs.Open.
type fakeRepo struct {
	mu      sync.Mutex
	doc     string // README.md on main
	commit  map[string]any
	pr      map[string]any
	forked  bool
	branchs []string
}

func (f *fakeRepo) handle(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/repos/alice/app") {
		return false
	}
	if r.Header.Get("Authorization") != "Bearer ghs_100" {
		w.WriteHeader(401)
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	file := func() {
		json.NewEncoder(w).Encode(map[string]any{"type": "file", "encoding": "base64", "sha": "blob1",
			"content": base64.StdEncoding.EncodeToString([]byte(f.doc))})
	}
	switch p := strings.TrimPrefix(r.URL.Path, "/repos/alice/app"); {
	case p == "" && r.Method == "GET":
		// Installation tokens don't report push permission.
		json.NewEncoder(w).Encode(map[string]any{"default_branch": "main", "name": "app"})
	case p == "/forks":
		f.forked = true
		w.WriteHeader(403)
	case p == "/contents/README.md" && r.Method == "GET":
		file()
	case p == "/contents/README.md" && r.Method == "PUT":
		json.NewDecoder(r.Body).Decode(&f.commit)
		w.Write([]byte(`{}`))
	case p == "/git/ref/heads/main":
		json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": "abc123"}})
	case p == "/git/refs" && r.Method == "POST":
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		f.branchs = append(f.branchs, in["ref"])
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	case p == "/pulls" && r.Method == "POST":
		json.NewDecoder(r.Body).Decode(&f.pr)
		w.WriteHeader(201)
		w.Write([]byte(`{"number":7,"html_url":"https://github.com/alice/app/pull/7"}`))
	default:
		w.WriteHeader(404)
	}
	return true
}

func TestPullRequest(t *testing.T) {
	repo := &fakeRepo{doc: "# App\n\nRun `app --old`.\n"}
	e := newEnvWith(t, repo.handle)
	e.signIn()
	_, qs := e.do("POST", "/api/repos/500/quickstarts", "application/json", map[string]any{"doc_path": "README.md", "goal": "run it"})
	qsID := int64(qs["id"].(float64))
	_, run := e.do("POST", fmt.Sprintf("/api/quickstarts/%d/runs", qsID), "application/json", map[string]any{})
	runID := int64(run["id"].(float64))
	path := fmt.Sprintf("/api/runs/%d/pr", runID)

	if code, out := e.do("GET", path, "", nil); code != 200 || out["draft"] != nil {
		t.Fatalf("no draft yet = %d %v", code, out)
	}

	p := prs.Proposal{Repo: "https://github.com/alice/app", DocPath: "README.md", Original: []byte(repo.doc),
		Fixed: []byte("# App\n\nRun `app --new`.\n"), Patch: "-old\n+new\n", Branch: "rtfm/fix-quickstart-abc",
		Title: "docs: fix", CommitMessage: "docs: fix\n", Body: "Fixes the run command.\n\n- [ ] I have read the CLA\n"}
	found := guidelines.Found{Contributing: []guidelines.File{{Path: "CONTRIBUTING.md", Content: "Sign off your commits."}}}
	var dec guidelines.Decision
	dec.DCORequired = true
	dec.Checkboxes = append(dec.Checkboxes, struct {
		Text       string `json:"text"`
		Checked    bool   `json:"checked"`
		NeedsHuman bool   `json:"needs_human"`
		Reason     string `json:"reason"`
	}{Text: "I have read the CLA", NeedsHuman: true})
	pj, _ := json.Marshal(p)
	fj, _ := json.Marshal(found)
	dj, _ := json.Marshal(dec)
	if err := e.d.SavePRDraft(t.Context(), db.PRDraft{RunID: runID, Proposal: pj, Guidelines: fj, Decision: dj}); err != nil {
		t.Fatal(err)
	}

	// The preview lists what only the person can answer.
	code, out := e.do("GET", path, "", nil)
	draft, _ := out["draft"].(map[string]any)
	if code != 200 || draft == nil || draft["status"] != "ready" || draft["repo"] != "alice/app" {
		t.Fatalf("preview = %d %v", code, out)
	}
	codes := map[string]bool{}
	for _, b := range draft["blockers"].([]any) {
		codes[b.(map[string]any)["code"].(string)] = true
	}
	if !codes["dco"] || !codes["attest"] || len(codes) != 2 {
		t.Errorf("blockers = %v", draft["blockers"])
	}
	if id := draft["identity"].(map[string]any); id["name"] != "Alice" || id["email"] != "1+alice@users.noreply.github.com" {
		t.Errorf("identity = %v", id)
	}

	// It is never opened without the answers.
	if code, _ := e.do("POST", path, "application/json", map[string]any{}); code != 422 {
		t.Errorf("open without answers = %d", code)
	}
	if repo.pr != nil {
		t.Fatal("a pull request was opened without the answers")
	}
	if code, _ := e.do("POST", path, "application/json", map[string]any{"signoff": true, "attest": true, "issue_url": "javascript:alert(1)"}); code != 422 {
		t.Errorf("bad issue url = %d", code)
	}

	code, out = e.do("POST", path, "application/json", map[string]any{"signoff": true, "attest": true})
	if code != 201 || out["draft"].(map[string]any)["pr_url"] != "https://github.com/alice/app/pull/7" {
		t.Fatalf("open = %d %v", code, out)
	}
	if repo.forked || len(repo.branchs) != 1 || repo.branchs[0] != "refs/heads/rtfm/fix-quickstart-abc" {
		t.Errorf("forked=%v branches=%v (the App should push to the repo itself)", repo.forked, repo.branchs)
	}
	msg, _ := repo.commit["message"].(string)
	author, _ := repo.commit["author"].(map[string]any)
	if !strings.Contains(msg, "Signed-off-by: Alice <1+alice@users.noreply.github.com>") || author["name"] != "Alice" {
		t.Errorf("commit = %v", repo.commit)
	}
	if body, _ := repo.pr["body"].(string); !strings.Contains(body, "- [x] I have read the CLA") || !strings.Contains(body, "rtfm.run") {
		t.Errorf("pr body = %q", body)
	}

	// Recorded once; a second click doesn't open a second PR.
	_, got := e.do("GET", fmt.Sprintf("/api/runs/%d", runID), "", nil)
	if got["pr_url"] != "https://github.com/alice/app/pull/7" {
		t.Errorf("run pr_url = %v", got["pr_url"])
	}
	if code, _ := e.do("POST", path, "application/json", map[string]any{"signoff": true, "attest": true}); code != 409 {
		t.Errorf("second open = %d", code)
	}

	// The quickstart lists it.
	_, list := e.do("GET", fmt.Sprintf("/api/quickstarts/%d/pull-requests", qsID), "", nil)
	prsList, _ := list["pull_requests"].([]any)
	if len(prsList) != 1 {
		t.Fatalf("pull requests = %v", list)
	}
	if pr := prsList[0].(map[string]any); pr["status"] != "opened" || pr["pr_url"] != "https://github.com/alice/app/pull/7" || pr["title"] != "docs: fix" {
		t.Errorf("listed = %v", pr)
	}
}

func TestPullRequestDocChanged(t *testing.T) {
	repo := &fakeRepo{doc: "# App\n\nSomeone already edited this.\n"}
	e := newEnvWith(t, repo.handle)
	e.signIn()
	_, qs := e.do("POST", "/api/repos/500/quickstarts", "application/json", map[string]any{"doc_path": "README.md", "goal": "run it"})
	_, run := e.do("POST", fmt.Sprintf("/api/quickstarts/%d/runs", int64(qs["id"].(float64))), "application/json", map[string]any{})
	runID := int64(run["id"].(float64))
	pj, _ := json.Marshal(prs.Proposal{Repo: "https://github.com/alice/app", DocPath: "README.md", Original: []byte("# App\n"),
		Fixed: []byte("# App!\n"), Branch: "rtfm/fix-quickstart-def", Title: "docs: fix", CommitMessage: "docs: fix\n", Body: "x"})
	if err := e.d.SavePRDraft(t.Context(), db.PRDraft{RunID: runID, Proposal: pj, Partial: true}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/runs/%d/pr", runID)
	if code, _ := e.do("POST", path, "application/json", map[string]any{}); code != 422 {
		t.Errorf("partial without confirmation = %d", code)
	}
	code, out := e.do("POST", path, "application/json", map[string]any{"allow_partial": true})
	if code != 502 || !strings.Contains(out["error"].(string), "changed on GitHub") {
		t.Errorf("doc changed = %d %v", code, out)
	}
	// The failure is kept and the draft can be tried again.
	_, out = e.do("GET", path, "", nil)
	if d := out["draft"].(map[string]any); d["status"] != "ready" || !strings.Contains(d["error"].(string), "changed on GitHub") {
		t.Errorf("after failure = %v", d)
	}
}
