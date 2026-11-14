package guidelines

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"vanguard.run/internal/github"
	"vanguard.run/internal/prs"
)

func TestDiscover(t *testing.T) {
	files := map[string]string{
		"CONTRIBUTING.md":                       "Use Conventional Commits.",
		".github/Pull_Request_Template.md":      "## Summary\n- [ ] I have read CONTRIBUTING",
		".github/PULL_REQUEST_TEMPLATE/docs.md": "## Docs change",
		".github/PULL_REQUEST_TEMPLATE/code.md": "## Code change",
		".github/dco.yml":                       "require: members: false",
		".commitlintrc.json":                    `{"extends":["@commitlint/config-conventional"]}`,
		"README.md":                             "not a guideline",
	}
	dirs := map[string][]github.Entry{
		"": {{Name: "CONTRIBUTING.md", Path: "CONTRIBUTING.md", Type: "file"}, {Name: "README.md", Path: "README.md", Type: "file"},
			{Name: ".commitlintrc.json", Path: ".commitlintrc.json", Type: "file"}, {Name: ".github", Path: ".github", Type: "dir"}},
		".github": {{Name: "Pull_Request_Template.md", Path: ".github/Pull_Request_Template.md", Type: "file"},
			{Name: "PULL_REQUEST_TEMPLATE", Path: ".github/PULL_REQUEST_TEMPLATE", Type: "dir"},
			{Name: "dco.yml", Path: ".github/dco.yml", Type: "file"}},
		".github/PULL_REQUEST_TEMPLATE": {{Name: "docs.md", Path: ".github/PULL_REQUEST_TEMPLATE/docs.md", Type: "file"},
			{Name: "code.md", Path: ".github/PULL_REQUEST_TEMPLATE/code.md", Type: "file"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/repos/o/r/contents")
		p = strings.TrimPrefix(p, "/")
		if entries, ok := dirs[p]; ok {
			json.NewEncoder(w).Encode(entries)
			return
		}
		if c, ok := files[p]; ok {
			json.NewEncoder(w).Encode(map[string]string{"type": "file", "encoding": "base64", "sha": "x",
				"content": base64.StdEncoding.EncodeToString([]byte(c))})
			return
		}
		w.WriteHeader(404) // e.g. docs/ doesn't exist
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	f, err := Discover(context.Background(), &github.Client{BaseURL: srv.URL}, github.Repo{Owner: "o", Name: "r"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, file := range f.All() {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	want := []string{".commitlintrc.json", ".github/PULL_REQUEST_TEMPLATE/code.md", ".github/PULL_REQUEST_TEMPLATE/docs.md",
		".github/Pull_Request_Template.md", ".github/dco.yml", "CONTRIBUTING.md"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("found %v\nwant  %v", paths, want)
	}
	if !f.DCOConfig || len(f.Templates) != 3 {
		t.Errorf("dco=%v templates=%d", f.DCOConfig, len(f.Templates))
	}
}

func proposal() *prs.Proposal {
	return &prs.Proposal{Title: "docs: fix quickstart", Branch: "vanguard/fix-quickstart-abc", CommitMessage: "docs: fix\n",
		Body: "draft body by vanguard.run"}
}

func decision(js string) *Decision {
	var d Decision
	if err := json.Unmarshal([]byte(js), &d); err != nil {
		panic(err)
	}
	return &d
}

const filled = `{
 "ai_policy":{"status":"not_mentioned","quote":"","requirements":""},
 "issue_first":{"required":false,"quote":""},
 "dco_required":true,"cla_required":false,"target_branch":"",
 "title":"docs(readme): fix the hello flag","commit_subject":"docs(readme): fix the hello flag",
 "commit_body":"Use --name, which the CLI accepts.\nSigned-off-by: Someone Else <x@y>",
 "branch_name":"docs/fix-hello-flag","template_used":".github/pull_request_template.md",
 "body":"## Summary\nFixes the flag.\n\n## Checklist\n- [x] Documentation only\n- [ ] I have read CONTRIBUTING.md\n- [ ] I agree to the Code of Conduct",
 "checkboxes":[
  {"text":"Documentation only","checked":true,"needs_human":false,"reason":"diff touches README only"},
  {"text":"I have read CONTRIBUTING.md","checked":false,"needs_human":true,"reason":"personal"},
  {"text":"I agree to the Code of Conduct","checked":false,"needs_human":true,"reason":"personal"}],
 "other_requirements":[{"requirement":"Add a CHANGELOG entry","satisfied":false,"how":"docs changes are exempt per CONTRIBUTING? unclear"}],
 "notes":["Used Conventional Commits with scope readme"]}`

func TestApplyBlocksUntilTheHumanConfirms(t *testing.T) {
	p := proposal()
	o := Apply(p, decision(filled), &Found{Templates: []File{{Path: ".github/pull_request_template.md"}}}, Options{})
	if o.Ready() || len(o.Blockers) != 2 {
		t.Fatalf("want DCO + attestation blockers, got %+v", o.Blockers)
	}
	if strings.Contains(p.Body, "- [x] I have read") || strings.Contains(p.CommitMessage, "Signed-off-by") {
		t.Errorf("must not attest or sign off on the human's behalf:\n%s\n%s", p.Body, p.CommitMessage)
	}
	if p.Title != "docs(readme): fix the hello flag" || p.Branch != "docs/fix-hello-flag" {
		t.Errorf("conventions not applied: %q %q", p.Title, p.Branch)
	}
	if !strings.Contains(p.Body, "vanguard.run") {
		t.Errorf("AI disclosure must always be kept:\n%s", p.Body)
	}
	if len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "CHANGELOG") {
		t.Errorf("warnings = %v", o.Warnings)
	}
}

func TestApplyWithSignoffAndAttest(t *testing.T) {
	p := proposal()
	id := &github.Identity{Name: "Kevin", Email: "k@example.com"}
	o := Apply(p, decision(filled), &Found{}, Options{Signoff: true, Identity: id, Attest: true})
	if !o.Ready() {
		t.Fatalf("blockers: %+v", o.Blockers)
	}
	if !strings.HasSuffix(p.CommitMessage, "Signed-off-by: Kevin <k@example.com>\n") || strings.Contains(p.CommitMessage, "Someone Else") {
		t.Errorf("commit message = %q", p.CommitMessage)
	}
	if p.Author != id {
		t.Error("commit must be authored by the signing identity")
	}
	if !strings.Contains(p.Body, "- [x] I have read CONTRIBUTING.md") || !strings.Contains(p.Body, "- [x] I agree to the Code of Conduct") {
		t.Errorf("attested boxes not ticked:\n%s", p.Body)
	}
}

func TestApplyHardStopOnAIBan(t *testing.T) {
	d := decision(filled)
	d.AIPolicy.Status, d.AIPolicy.Quote = "forbidden", "We do not accept AI-generated pull requests."
	o := Apply(proposal(), d, &Found{}, Options{Signoff: true, Identity: &github.Identity{Name: "a", Email: "b"}, Attest: true})
	if o.Ready() || !o.Blockers[0].Hard || !strings.Contains(o.Blockers[0].Message, "We do not accept AI-generated pull requests.") {
		t.Errorf("blockers = %+v", o.Blockers)
	}
}

func TestApplyIssueFirstAndDCOConfig(t *testing.T) {
	d := decision(filled)
	d.DCORequired = false
	d.IssueFirst.Required = true
	d.Checkboxes = nil
	p := proposal()
	o := Apply(p, d, &Found{DCOConfig: true}, Options{})
	var msgs []string
	for _, b := range o.Blockers {
		msgs = append(msgs, b.Message)
	}
	all := strings.Join(msgs, "\n")
	if !strings.Contains(all, "--issue") || !strings.Contains(all, "--signoff") {
		t.Errorf("want issue + DCO (from dco.yml) blockers, got:\n%s", all)
	}
	p2 := proposal()
	o2 := Apply(p2, d, &Found{}, Options{Issue: "https://github.com/o/r/issues/12"})
	if !o2.Ready() || !strings.Contains(p2.Body, "Related issue: https://github.com/o/r/issues/12") {
		t.Errorf("issue not referenced: %+v\n%s", o2.Blockers, p2.Body)
	}
}

func TestApplyRejectsBadBranchNames(t *testing.T) {
	d := decision(filled)
	d.BranchName = "../../evil branch"
	p := proposal()
	o := Apply(p, d, &Found{}, Options{Signoff: true, Identity: &github.Identity{Name: "a", Email: "b"}, Attest: true})
	if p.Branch != "vanguard/fix-quickstart-abc" || len(o.Warnings) == 0 {
		t.Errorf("branch = %q warnings = %v", p.Branch, o.Warnings)
	}
}

func TestSavedDecisionIsReusedOnlyForSameInputs(t *testing.T) {
	dir := t.TempDir()
	f := &Found{Contributing: []File{{Path: "CONTRIBUTING.md", Content: "Use Conventional Commits."}}}
	p := proposal()
	key := Key(f, p, "m1")
	path := dir + "/g.json"
	if err := Save(path, key, decision(filled)); err != nil {
		t.Fatal(err)
	}
	if d := Load(path, key); d == nil || d.Title != "docs(readme): fix the hello flag" {
		t.Errorf("same inputs should reuse the decision: %+v", d)
	}
	f.Contributing[0].Content = "Changed upstream."
	if Load(path, Key(f, p, "m1")) != nil {
		t.Error("changed guidelines must not reuse the saved decision")
	}
	if Load(path, Key(&Found{Contributing: []File{{Path: "CONTRIBUTING.md", Content: "Use Conventional Commits."}}}, p, "m2")) != nil {
		t.Error("a different model must not reuse the saved decision")
	}
}
