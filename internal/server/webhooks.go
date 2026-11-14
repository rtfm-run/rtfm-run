package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"vanguard.run/internal/db"
	"vanguard.run/internal/ghapp"
	"vanguard.run/internal/match"
)

// maxWebhookBody is GitHub's maximum payload size.
const maxWebhookBody = 25 << 20

type pushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		HTMLURL       string `json:"html_url"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Commits []struct {
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	} `json:"commits"`
}

type installationEvent struct {
	Action       string             `json:"action"`
	Installation ghapp.Installation `json:"installation"`
	Sender       struct {
		ID int64 `json:"id"`
	} `json:"sender"`
}

// webhook receives GitHub App events: installations change which repos are
// available; pushes enqueue runs for matching triggers.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable body")
		return
	}
	if !s.App.VerifySignature(body, r.Header.Get("X-Hub-Signature-256")) {
		writeError(w, http.StatusUnauthorized, "bad signature")
		return
	}
	event, delivery := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	ctx := r.Context()
	if delivery != "" {
		fresh, err := s.DB.RecordDelivery(ctx, delivery, event)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !fresh {
			writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate delivery"})
			return
		}
	}

	var result map[string]any
	switch event {
	case "ping":
		result = map[string]any{"status": "pong"}
	case "installation", "installation_repositories":
		err = s.handleInstallation(ctx, event, body)
		result = map[string]any{"status": "synced"}
	case "push":
		var n int
		n, err = s.handlePush(ctx, body)
		result = map[string]any{"status": "ok", "enqueued": n}
	default:
		result = map[string]any{"status": "ignored"}
	}
	if err != nil {
		s.Log.Error("webhook failed", "event", event, "delivery", delivery, "err", err)
		if delivery != "" {
			_ = s.DB.DeliveryError(context.Background(), delivery, err.Error())
		}
		writeError(w, http.StatusInternalServerError, "processing failed")
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleInstallation(ctx context.Context, event string, body []byte) error {
	var ev installationEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return err
	}
	in := ev.Installation
	switch {
	case event == "installation" && ev.Action == "deleted":
		return s.DB.DeleteInstallation(ctx, in.ID)
	case event == "installation" && ev.Action == "suspend":
		return s.DB.SetInstallationSuspended(ctx, in.ID, true)
	}
	// created, unsuspend, new_permissions_accepted, repositories added/removed:
	// re-read the installation's repos from GitHub (payloads omit default
	// branches, and a full sync also heals any missed events).
	if ev.Action == "unsuspend" {
		in.SuspendedAt = nil
	}
	if err := s.syncInstallation(ctx, in); err != nil {
		return err
	}
	// Whoever installed it can see it right away (if they've signed in).
	return s.DB.AddUserInstallationByGitHubID(ctx, ev.Sender.ID, in.ID)
}

func (s *Server) handlePush(ctx context.Context, body []byte) (int, error) {
	var ev pushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return 0, err
	}
	branch, isBranch := strings.CutPrefix(ev.Ref, "refs/heads/")
	if ev.Deleted || !isBranch || len(ev.Commits) == 0 || strings.Trim(ev.After, "0") == "" {
		return 0, nil // branch deletions, tags, and pushes with no new commits
	}
	repo, err := s.DB.RepoByID(ctx, ev.Repository.ID)
	if err == db.ErrNotFound {
		known, err := s.DB.InstallationExists(ctx, ev.Installation.ID)
		if err != nil || !known {
			return 0, err
		}
		repo = db.Repo{ID: ev.Repository.ID, FullName: ev.Repository.FullName, DefaultBranch: ev.Repository.DefaultBranch,
			Private: ev.Repository.Private, HTMLURL: ev.Repository.HTMLURL}
		if err := s.DB.AddRepos(ctx, ev.Installation.ID, []db.Repo{repo}); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}

	var changed []string
	for _, c := range ev.Commits {
		changed = append(changed, c.Added...)
		changed = append(changed, c.Removed...)
		changed = append(changed, c.Modified...)
	}
	// GitHub includes at most 20 commits per push payload; beyond that the
	// file list is incomplete, so path filters match to be safe.
	complete := len(ev.Commits) < 20

	triggers, err := s.DB.PushTriggersForRepo(ctx, repo.ID)
	if err != nil {
		return 0, err
	}
	enqueued := 0
	seen := map[int64]bool{}
	for _, pt := range triggers {
		q := pt.Quickstart
		if seen[q.ID] {
			continue // several triggers on one quickstart: one run per push
		}
		fallback := q.Branch
		if fallback == "" {
			fallback = ev.Repository.DefaultBranch
		}
		if !match.Branch(pt.Trigger.Branches, branch, fallback) || !match.Paths(pt.Trigger.Paths, changed, complete) {
			continue
		}
		seen[q.ID] = true
		if active, err := s.DB.HasActiveRun(ctx, q.ID, ev.After); err != nil {
			return enqueued, err
		} else if active {
			continue
		}
		if _, err := s.DB.Enqueue(ctx, q, "push", nil, branch, ev.After); err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}
