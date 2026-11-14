package prs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"rtfm.run/internal/github"
	"rtfm.run/internal/report"
)

const orig = "## Deploy\n\n```\n$ docker compose up -d\n```\n"
const fixed = "## Deploy\n\n```\n$ cd flask-redis && docker compose up -d\n```\n"

func demoRun(status string) *report.Run {
	return &report.Run{
		ID: "20261114-075620-fe131b", Repo: "https://github.com/docker/awesome-compose",
		BaseCommit: "abc123", DocPath: "flask-redis/README.md", Goal: "get the page-view counter",
		Sandbox: "devbox:firecracker:rtfm-vmbox:latest", Model: "gemini-3.8-flash-medium",
		Steps: []report.Step{{Index: 1, Status: report.StatusUnsupported, Failure: report.KindStaleCommand,
			DocCommand: "docker compose up -d", Command: "cd flask-redis && docker compose up -d",
			Diagnosis: "Docs say `docker compose up -d`; what actually worked was `cd flask-redis && docker compose up -d`."}},
		Commands: []report.Command{{Line: "docker compose up -d", ExitCode: 1, Output: "no configuration file provided: not found"}},
		Fix: &report.FixResult{Status: status, Patch: "--- a\n+++ b\n", IssuesBefore: 2,
			Changes: []report.Change{{StepIndex: 1, Summary: "Change directory to flask-redis first.", Evidence: "compose.yaml is in /work/project/flask-redis/, not /work/project.", Source: "fixer"}}},
	}
}

func TestEligible(t *testing.T) {
	if err := Eligible(demoRun(report.FixVerified), false); err != nil {
		t.Errorf("verified: %v", err)
	}
	blocked := demoRun(report.FixBlocked)
	if err := Eligible(blocked, false); err != nil {
		t.Errorf("blocked with 0 doc issues left should be eligible: %v", err)
	}
	blocked.Fix.IssuesAfter = 1
	if err := Eligible(blocked, false); !errors.Is(err, ErrNotEligible) {
		t.Errorf("partial blocked should need --allow-partial: %v", err)
	}
	if err := Eligible(blocked, true); err != nil {
		t.Errorf("--allow-partial: %v", err)
	}
	for _, st := range []string{report.FixNotFixed, report.FixError, report.FixNoChange} {
		if err := Eligible(demoRun(st), true); !errors.Is(err, ErrNotEligible) {
			t.Errorf("%s should not be eligible: %v", st, err)
		}
	}
	noFix := demoRun(report.FixVerified)
	noFix.Fix = nil
	if Eligible(noFix, true) == nil {
		t.Error("run without a fix should not be eligible")
	}
}

func TestBuildBody(t *testing.T) {
	verify := &report.Run{Steps: []report.Step{{Status: report.StatusPass}, {Status: report.StatusPass}},
		SuccessCheck: &report.Command{Line: "curl -fsS localhost:8000", ExitCode: 0}}
	p := Build(demoRun(report.FixVerified), verify, []byte(orig), []byte(fixed))
	if p.Branch != "rtfm/fix-quickstart-fe131b" {
		t.Errorf("branch = %q", p.Branch)
	}
	for _, want := range []string{
		"exactly as written", "microVM", "Fails with `no configuration file provided: not found`.",
		"It worked as `cd flask-redis && docker compose up -d`.",
		"**Step 1:** Change directory to flask-redis first.", "Evidence: compose.yaml is in flask-redis/, not the repository root.",
		"reached the goal with no problems", "Tested commit: `abc123`", "2 steps passed",
		"prepared by rtfm.run",
	} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("body missing %q:\n%s", want, p.Body)
		}
	}
	if !strings.HasPrefix(p.CommitMessage, "docs: fix quickstart steps in flask-redis/README.md\n\n- Change directory") {
		t.Errorf("commit message = %q", p.CommitMessage)
	}
}

func TestParseRepo(t *testing.T) {
	cases := map[string]string{
		"https://github.com/docker/awesome-compose": "docker/awesome-compose",
		"https://github.com/encode/httpx.git":       "encode/httpx",
		"git@github.com:google-gemini/cookbook.git": "google-gemini/cookbook",
		"ssh://git@github.com/a/b":                  "a/b",
		"https://github.com/owner/repo.name/":       "owner/repo.name",
	}
	for in, want := range cases {
		r, ok := github.ParseRepo(in)
		if !ok || r.String() != want {
			t.Errorf("ParseRepo(%q) = %v %v, want %s", in, r, ok, want)
		}
	}
	for _, bad := range []string{"examples/local", "https://gitlab.com/a/b", "https://github.com/a"} {
		if _, ok := github.ParseRepo(bad); ok {
			t.Errorf("ParseRepo(%q) should fail", bad)
		}
	}
}

// fakeGitHub is an in-memory GitHub with an upstream repo and the user's fork.
type fakeGitHub struct {
	mu        sync.Mutex
	push      bool
	upstream  string // doc content on upstream main
	forked    bool
	branches  map[string]string
	committed string
	pr        map[string]any
	authSeen  string
}

func (g *fakeGitHub) handler(t *testing.T) http.Handler {
	file := func(w http.ResponseWriter, content string) {
		json.NewEncoder(w).Encode(map[string]string{"type": "file", "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString([]byte(content)), "sha": "blob1"})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.authSeen = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == "/repos/docker/awesome-compose":
			json.NewEncoder(w).Encode(map[string]any{"default_branch": "main", "name": "awesome-compose",
				"owner": map[string]string{"login": "docker"}, "permissions": map[string]bool{"push": g.push}})
		case r.Method == "GET" && p == "/repos/docker/awesome-compose/contents/flask-redis/README.md":
			file(w, g.upstream)
		case r.Method == "GET" && p == "/repos/docker/awesome-compose/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "headsha"}})
		case r.Method == "POST" && p == "/repos/docker/awesome-compose/forks":
			g.forked = true
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]any{"name": "awesome-compose", "owner": map[string]string{"login": "me"}})
		case r.Method == "GET" && p == "/repos/me/awesome-compose" && g.forked:
			json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case r.Method == "POST" && p == "/repos/me/awesome-compose/merge-upstream":
			w.WriteHeader(200)
		case r.Method == "POST" && strings.HasSuffix(p, "/git/refs"):
			var in map[string]string
			json.Unmarshal(body, &in)
			if in["sha"] != "headsha" {
				t.Errorf("branch created at %q, want upstream head", in["sha"])
			}
			g.branches[p+in["ref"]] = in["sha"]
			w.WriteHeader(201)
		case r.Method == "GET" && strings.HasSuffix(p, "/contents/flask-redis/README.md"):
			file(w, g.upstream)
		case r.Method == "PUT" && strings.HasSuffix(p, "/contents/flask-redis/README.md"):
			var in map[string]string
			json.Unmarshal(body, &in)
			b, _ := base64.StdEncoding.DecodeString(in["content"])
			g.committed = p + "@" + in["branch"] + ":" + string(b)
			w.WriteHeader(200)
		case r.Method == "POST" && p == "/repos/docker/awesome-compose/pulls":
			json.Unmarshal(body, &g.pr)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"number": 7, "html_url": "https://github.com/docker/awesome-compose/pull/7"})
		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"message": "Not Found " + r.Method + " " + p})
		}
	})
}

func TestOpenViaFork(t *testing.T) {
	g := &fakeGitHub{upstream: orig, branches: map[string]string{}}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	gh := &github.Client{Token: "ghp_secret", BaseURL: srv.URL}

	p := Build(demoRun(report.FixVerified), nil, []byte(orig), []byte(fixed))
	opened, err := Open(context.Background(), gh, p)
	if err != nil {
		t.Fatal(err)
	}
	if opened.URL != "https://github.com/docker/awesome-compose/pull/7" || opened.Fork != "me/awesome-compose" {
		t.Errorf("opened = %+v", opened)
	}
	if !strings.HasPrefix(g.committed, "/repos/me/awesome-compose/contents/flask-redis/README.md@rtfm/fix-quickstart-fe131b:") ||
		!strings.HasSuffix(g.committed, fixed) {
		t.Errorf("committed = %q", g.committed)
	}
	if g.pr["head"] != "me:rtfm/fix-quickstart-fe131b" || g.pr["base"] != "main" || g.pr["maintainer_can_modify"] != true {
		t.Errorf("pr = %v", g.pr)
	}
	if g.authSeen != "Bearer ghp_secret" {
		t.Errorf("auth header = %q", g.authSeen)
	}
}

func TestOpenWithPushAccess(t *testing.T) {
	g := &fakeGitHub{push: true, upstream: orig, branches: map[string]string{}}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	opened, err := Open(context.Background(), &github.Client{BaseURL: srv.URL}, Build(demoRun(report.FixVerified), nil, []byte(orig), []byte(fixed)))
	if err != nil {
		t.Fatal(err)
	}
	if g.forked || opened.Fork != "" || g.pr["head"] != "rtfm/fix-quickstart-fe131b" {
		t.Errorf("should branch in the repo itself: forked=%v pr=%v", g.forked, g.pr)
	}
}

func TestOpenRefusesWhenDocChanged(t *testing.T) {
	g := &fakeGitHub{upstream: orig + "\nNew upstream text.\n", branches: map[string]string{}}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	_, err := Open(context.Background(), &github.Client{BaseURL: srv.URL}, Build(demoRun(report.FixVerified), nil, []byte(orig), []byte(fixed)))
	if !errors.Is(err, ErrDocChanged) {
		t.Errorf("err = %v, want ErrDocChanged", err)
	}
	if g.forked || len(g.branches) > 0 || g.pr != nil {
		t.Error("nothing should be created when the doc changed upstream")
	}
}

func TestOpenRejectsNonGitHub(t *testing.T) {
	p := Build(demoRun(report.FixVerified), nil, []byte(orig), []byte(fixed))
	p.Repo = "examples/stale-quickstart"
	if _, err := Open(context.Background(), &github.Client{}, p); err == nil || !strings.Contains(err.Error(), "not a GitHub repository") {
		t.Errorf("err = %v", err)
	}
}
