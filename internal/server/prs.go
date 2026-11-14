package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rtfm.run/internal/db"
	"rtfm.run/internal/github"
	"rtfm.run/internal/guidelines"
	"rtfm.run/internal/prs"
)

// prView is a run's pull request as the dashboard shows it: the exact text
// that will be posted, and what the repository's guidelines still need from
// the person opening it.
type prView struct {
	Status        string               `json:"status"` // ready | opening | opened
	Error         string               `json:"error,omitempty"`
	PRURL         string               `json:"pr_url,omitempty"`
	OpenedAt      *time.Time           `json:"opened_at,omitempty"`
	Partial       bool                 `json:"partial"`
	Repo          string               `json:"repo"`
	Title         string               `json:"title"`
	Body          string               `json:"body"`
	CommitMessage string               `json:"commit_message"`
	Branch        string               `json:"branch"`
	BaseBranch    string               `json:"base_branch"`
	Patch         string               `json:"patch"`
	Found         []string             `json:"found"`
	Applied       []string             `json:"applied"`
	Warnings      []string             `json:"warnings"`
	Blockers      []guidelines.Blocker `json:"blockers"`
	// Identity is who a DCO sign-off would name (and who authors the commit).
	Identity github.Identity `json:"identity"`
}

// prOptions are the signed-in person's explicit answers.
type prOptions struct {
	Signoff      bool   `json:"signoff"`
	CLASigned    bool   `json:"cla_signed"`
	Attest       bool   `json:"attest"`
	IssueURL     string `json:"issue_url"`
	AllowPartial bool   `json:"allow_partial"`
}

// identity is the git identity used for a sign-off: the user's GitHub name
// and public email, or GitHub's noreply address for them.
func identity(u db.User) github.Identity {
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name = u.Login
	}
	email := strings.TrimSpace(u.Email)
	if email == "" {
		email = fmt.Sprintf("%d+%s@users.noreply.github.com", u.GitHubID, u.Login)
	}
	return github.Identity{Name: name, Email: email}
}

// buildPR applies the guidelines to a draft with the given answers.
func buildPR(d db.PRDraft, u db.User, opt prOptions) (*prs.Proposal, prView, error) {
	var p prs.Proposal
	if err := json.Unmarshal(d.Proposal, &p); err != nil {
		return nil, prView{}, err
	}
	found := &guidelines.Found{}
	if len(d.Guidelines) > 0 {
		if err := json.Unmarshal(d.Guidelines, found); err != nil {
			return nil, prView{}, err
		}
	}
	var decision *guidelines.Decision
	if len(d.Decision) > 0 {
		decision = &guidelines.Decision{}
		if err := json.Unmarshal(d.Decision, decision); err != nil {
			return nil, prView{}, err
		}
	}
	id := identity(u)
	out := guidelines.Apply(&p, decision, found, guidelines.Options{
		Signoff: opt.Signoff, Identity: &id, CLASigned: opt.CLASigned, Attest: opt.Attest, Issue: opt.IssueURL,
	})
	repo, _ := github.ParseRepo(p.Repo)
	v := prView{
		Status: d.Status, Error: d.Error, PRURL: d.PRURL, OpenedAt: d.OpenedAt, Partial: d.Partial,
		Repo: repo.String(), Title: p.Title, Body: p.Body, CommitMessage: p.CommitMessage,
		Branch: p.Branch, BaseBranch: p.BaseBranch, Patch: p.Patch,
		Found: orEmpty(out.Found), Applied: orEmpty(out.Applied), Warnings: orEmpty(out.Warnings), Blockers: orEmpty(out.Blockers),
		Identity: id,
	}
	return &p, v, nil
}

// getPR previews a run's pull request. A run without a draft returns
// {"draft": null}.
func (s *Server) getPR(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad run id")
		return
	}
	d, err := s.DB.PRDraftForUser(r.Context(), u.ID, id)
	if errors.Is(err, db.ErrNotFound) {
		if _, err := s.DB.RunForUser(r.Context(), u.ID, id); err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"draft": nil})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, v, err := buildPR(d, u, prOptions{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": v})
}

// openPR opens a run's pull request as the GitHub App, with the person's
// answers to the repository's requirements. It never answers for them.
func (s *Server) openPR(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad run id")
		return
	}
	var opt prOptions
	if err := decode(r, &opt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	opt.IssueURL = strings.TrimSpace(opt.IssueURL)
	if opt.IssueURL != "" {
		if iu, err := url.Parse(opt.IssueURL); err != nil || iu.Scheme != "https" || iu.Host == "" {
			writeError(w, http.StatusUnprocessableEntity, "issue_url must be an https link")
			return
		}
	}
	ctx := r.Context()
	d, err := s.DB.PRDraftForUser(ctx, u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, v, err := buildPR(d, u, opt)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch {
	case d.Status == db.PROpened:
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already opened", "draft": v})
		return
	case d.Partial && !opt.AllowPartial:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "this fix only partly fixes the doc; confirm you want to propose it anyway", "draft": v})
		return
	case len(v.Blockers) > 0:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "the repository's guidelines need more from you first", "draft": v})
		return
	}
	run, err := s.DB.RunForUser(ctx, u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	repo, err := s.DB.RepoByID(ctx, run.RepoID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.ClaimPROpen(ctx, u.ID, id); err != nil {
		if errors.Is(err, db.ErrPRBusy) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.fail(w, r, err)
		return
	}
	// Finish the attempt even if the browser goes away mid-request.
	octx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	opened, err := s.openAsApp(octx, repo.InstallationID, p)
	if err != nil {
		msg := prErrorMessage(err)
		s.Log.Warn("opening pull request failed", "run", id, "err", err)
		_ = s.DB.FailPROpen(context.Background(), id, msg)
		writeError(w, http.StatusBadGateway, msg)
		return
	}
	if err := s.DB.FinishPROpen(context.Background(), id, &u.ID, opened.URL); err != nil {
		s.fail(w, r, err)
		return
	}
	v.Status, v.PRURL, v.Error = db.PROpened, opened.URL, ""
	writeJSON(w, http.StatusCreated, map[string]any{"draft": v})
}

func (s *Server) openAsApp(ctx context.Context, installationID int64, p *prs.Proposal) (*prs.Opened, error) {
	gh, err := s.App.InstallationClient(ctx, installationID)
	if err != nil {
		return nil, err
	}
	p.Direct = true // the App can push branches to repositories it's installed on
	return prs.Open(ctx, gh, p)
}

// prErrorMessage turns GitHub failures into something a person can act on.
func prErrorMessage(err error) string {
	switch {
	case errors.Is(err, prs.ErrDocChanged):
		return "The doc changed on GitHub since this run tested it. Run the quickstart again to fix the current version."
	case github.IsStatus(err, http.StatusForbidden), github.IsStatus(err, http.StatusNotFound):
		return "The GitHub App can't push to this repository. Check that it is installed with contents and pull request write access."
	case github.IsStatus(err, http.StatusUnprocessableEntity):
		return "GitHub refused the pull request (" + err.Error() + "). The branch may already exist from an earlier attempt."
	}
	return "Opening the pull request failed: " + err.Error()
}

// listPRs lists a quickstart's pull requests: drafts ready to open and
// ones already opened.
func (s *Server) listPRs(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	if _, err := s.DB.QuickstartForUser(r.Context(), u.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.DB.PRsForQuickstart(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pull_requests": orEmpty(list)})
}
