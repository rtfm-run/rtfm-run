package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rtfm.run/internal/db"
	"rtfm.run/internal/github"
	"rtfm.run/internal/guidelines"
	"rtfm.run/internal/prs"
	"rtfm.run/internal/report"
)

// draftPR prepares a run's fix as a pull request: the proposal, the
// repository's contribution guidelines and the agent's reading of them, so
// the dashboard can show exactly what will be posted and open it later
// without this worker's disk. Fixes that aren't worth proposing are skipped.
// It returns a short reason when no draft was saved (for the run's events).
func (cfg Config) draftPR(ctx context.Context, runID, installationID int64, rep, verify *report.Run, dir, model string) (bool, string) {
	if prs.Eligible(rep, true) != nil {
		return false, "" // no usable fix: nothing to propose
	}
	partial := prs.Eligible(rep, false) != nil
	ext := filepath.Ext(rep.DocPath)
	fixed, err := os.ReadFile(filepath.Join(dir, rep.ID+".fixed"+ext))
	if err != nil {
		return false, "the fixed doc was not saved"
	}
	original, err := os.ReadFile(filepath.Join(dir, rep.ID+".orig"+ext))
	if err != nil {
		return false, "the original doc was not saved"
	}
	p := prs.Build(rep, verify, original, fixed)

	repo, ok := github.ParseRepo(rep.Repo)
	if !ok {
		return false, "not a GitHub repository"
	}
	gh, err := cfg.App.InstallationClient(ctx, installationID)
	if err != nil {
		return false, "GitHub access failed"
	}
	found, err := guidelines.Discover(ctx, gh, repo, "")
	if err != nil {
		return false, fmt.Sprintf("could not read the contribution guidelines: %v", err)
	}
	var decision *guidelines.Decision
	if !found.Empty() {
		// Without a reading of the guidelines we can't know whether they
		// forbid AI contributions or require a sign-off, so no draft.
		if decision, err = guidelines.Interpret(ctx, cfg.NewRunner(model), repo.String(), found, p); err != nil {
			return false, "could not interpret the contribution guidelines"
		}
	}
	draft := db.PRDraft{RunID: runID, Partial: partial}
	if draft.Proposal, err = json.Marshal(p); err != nil {
		return false, err.Error()
	}
	if draft.Guidelines, err = json.Marshal(found); err != nil {
		return false, err.Error()
	}
	if decision != nil {
		if draft.Decision, err = json.Marshal(decision); err != nil {
			return false, err.Error()
		}
	}
	if err := cfg.DB.SavePRDraft(context.Background(), draft); err != nil {
		return false, "could not save the draft"
	}
	return true, ""
}

// DraftRun (re)prepares the pull request draft for a finished run, e.g. one
// that finished before drafts existed or whose draft failed. It needs the
// run's artifacts on this worker's disk.
func DraftRun(ctx context.Context, cfg Config, runID int64) error {
	run, repJSON, verJSON, err := cfg.DB.RunReports(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != db.RunSucceeded || len(repJSON) == 0 {
		return fmt.Errorf("run %d has not finished successfully", runID)
	}
	var rep report.Run
	if err := json.Unmarshal(repJSON, &rep); err != nil {
		return err
	}
	if err := prs.Eligible(&rep, true); err != nil {
		return err
	}
	var verify *report.Run
	if len(verJSON) > 0 {
		verify = &report.Run{}
		if err := json.Unmarshal(verJSON, verify); err != nil {
			return err
		}
	}
	repo, err := cfg.DB.RepoByID(ctx, run.RepoID)
	if err != nil {
		return err
	}
	q, err := cfg.DB.QuickstartByID(ctx, run.QuickstartID)
	if err != nil {
		return err
	}
	model := q.Model
	if model == "" {
		model = cfg.DefaultModel
	}
	dir := filepath.Join(cfg.OutDir, fmt.Sprintf("run-%d", run.ID), fmt.Sprintf("qs-%d", q.ID))
	if ok, why := cfg.draftPR(ctx, run.ID, repo.InstallationID, &rep, verify, dir, model); !ok {
		return fmt.Errorf("no draft: %s", why)
	}
	return nil
}

// autoOpenPR opens a run's freshly saved draft as the GitHub App, without
// any human answers. It returns the PR's URL, or why it was left as a draft.
func (cfg Config) autoOpenPR(ctx context.Context, runID, installationID int64) (string, string) {
	d, err := cfg.DB.PRDraftByRun(ctx, runID)
	if err != nil {
		return "", "the draft could not be loaded"
	}
	if d.Partial {
		return "", "the fix is only partial, so it needs a person to decide"
	}
	var p prs.Proposal
	found := &guidelines.Found{}
	var decision *guidelines.Decision
	if err := json.Unmarshal(d.Proposal, &p); err != nil {
		return "", err.Error()
	}
	if len(d.Guidelines) > 0 {
		_ = json.Unmarshal(d.Guidelines, found)
	}
	if len(d.Decision) > 0 {
		decision = &guidelines.Decision{}
		if err := json.Unmarshal(d.Decision, decision); err != nil {
			return "", err.Error()
		}
	}
	out := guidelines.Apply(&p, decision, found, guidelines.Options{})
	for _, b := range out.Blockers {
		if b.Hard {
			return "", "the project does not accept AI-generated contributions"
		}
	}
	if err := cfg.DB.ClaimPROpenAny(ctx, runID); err != nil {
		return "", err.Error()
	}
	gh, err := cfg.App.InstallationClient(ctx, installationID)
	if err == nil {
		p.Direct = true
		var opened *prs.Opened
		if opened, err = prs.Open(ctx, gh, &p); err == nil {
			if err := cfg.DB.FinishPROpen(context.Background(), runID, nil, opened.URL); err != nil {
				return "", err.Error()
			}
			return opened.URL, ""
		}
	}
	_ = cfg.DB.FailPROpen(context.Background(), runID, "Opening automatically failed: "+err.Error())
	return "", "opening it failed (" + err.Error() + ")"
}
