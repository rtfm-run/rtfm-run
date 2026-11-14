package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"rtfm.run/internal/creds"
	"rtfm.run/internal/db"
	"rtfm.run/internal/match"
	"rtfm.run/internal/targets"
)

func (s *Server) listInstallations(w http.ResponseWriter, r *http.Request, u db.User) {
	ins, err := s.DB.UserInstallations(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"installations": orEmpty(ins), "install_url": s.App.InstallURL()})
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request, u db.User) {
	repos, err := s.DB.ReposForUser(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": orEmpty(repos)})
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad repo id")
		return
	}
	repo, err := s.DB.RepoForUser(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	qs, err := s.DB.QuickstartsForRepo(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repo": repo, "quickstarts": orEmpty(qs)})
}

func (s *Server) listQuickstarts(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad repo id")
		return
	}
	if _, err := s.DB.RepoForUser(r.Context(), u.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	qs, err := s.DB.QuickstartsForRepo(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quickstarts": orEmpty(qs)})
}

// quickstartInput is the editable part of a quickstart; nil fields are
// left unchanged on PATCH.
type quickstartInput struct {
	Name         *string `json:"name"`
	DocPath      *string `json:"doc_path"`
	Goal         *string `json:"goal"`
	SuccessCheck *string `json:"success_check"`
	Branch       *string `json:"branch"`
	Backend      *string `json:"backend"`
	Model        *string `json:"model"`
	Fix          *bool   `json:"fix"`
	Verify       *bool   `json:"verify"`
}

func (in quickstartInput) apply(q *db.Quickstart) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&q.Name, in.Name)
	set(&q.DocPath, in.DocPath)
	set(&q.Goal, in.Goal)
	set(&q.SuccessCheck, in.SuccessCheck)
	set(&q.Branch, in.Branch)
	set(&q.Backend, in.Backend)
	set(&q.Model, in.Model)
	if in.Fix != nil {
		q.Fix = *in.Fix
	}
	if in.Verify != nil {
		q.Verify = *in.Verify
	}
}

var branchName = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,200}$`)

func validQuickstart(q db.Quickstart) error {
	if err := (targets.Target{Repo: "repo", DocPath: q.DocPath}).Validate(); err != nil {
		return errors.New("doc_path must be a relative path inside the repo")
	}
	switch {
	case q.Goal == "":
		return errors.New("goal is required: say what a newcomer should achieve, e.g. \"start the server and see it respond\"")
	case !branchName.MatchString(q.Branch) || strings.Contains(q.Branch, ".."):
		return errors.New("invalid branch")
	case q.Backend != "auto" && q.Backend != "docker" && q.Backend != "firecracker":
		return errors.New("backend must be auto, docker or firecracker")
	case len(q.SuccessCheck) > 2000 || len(q.Goal) > 2000:
		return errors.New("goal and success_check are limited to 2000 characters")
	}
	return nil
}

func (s *Server) createQuickstart(w http.ResponseWriter, r *http.Request, u db.User) {
	repoID, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad repo id")
		return
	}
	var in quickstartInput
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	q := db.Quickstart{RepoID: repoID, Backend: "auto", Fix: true, Verify: true}
	in.apply(&q)
	if err := validQuickstart(q); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	created, err := s.DB.CreateQuickstart(r.Context(), u.ID, q)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this repo already has a quickstart for that doc and branch")
			return
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getQuickstart(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	q, err := s.DB.QuickstartForUser(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	trs, err := s.DB.TriggersForQuickstart(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	runs, err := s.DB.ListRuns(r.Context(), u.ID, db.RunFilter{QuickstartID: id, Limit: 1})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var last *db.Run
	if len(runs) > 0 {
		last = &runs[0]
	}
	writeJSON(w, http.StatusOK, map[string]any{"quickstart": q, "triggers": orEmpty(trs), "last_run": last})
}

func (s *Server) updateQuickstart(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	q, err := s.DB.QuickstartForUser(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var in quickstartInput
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	in.apply(&q)
	if err := validQuickstart(q); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	updated, err := s.DB.UpdateQuickstart(r.Context(), u.ID, q)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this repo already has a quickstart for that doc and branch")
			return
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteQuickstart(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	if err := s.DB.DeleteQuickstart(r.Context(), u.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stepHealth(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	h, err := s.DB.StepHealthForQuickstart(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": h})
}

type triggerInput struct {
	Kind     string    `json:"kind"`
	Branches *[]string `json:"branches"`
	Paths    *[]string `json:"paths"`
	Enabled  *bool     `json:"enabled"`
}

func (in triggerInput) apply(t *db.Trigger) error {
	if in.Branches != nil {
		t.Branches = clean(*in.Branches)
	}
	if in.Paths != nil {
		t.Paths = clean(*in.Paths)
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	if len(t.Branches) > 50 || len(t.Paths) > 100 {
		return errors.New("too many branch or path patterns")
	}
	if err := match.Valid(t.Branches); err != nil {
		return fmt.Errorf("invalid branch pattern: %w", err)
	}
	if err := match.Valid(t.Paths); err != nil {
		return fmt.Errorf("invalid path pattern: %w", err)
	}
	return nil
}

func clean(ss []string) []string {
	out := []string{}
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) listTriggers(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	trs, err := s.DB.TriggersForQuickstart(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"triggers": orEmpty(trs)})
}

func (s *Server) createTrigger(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	var in triggerInput
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if in.Kind == "" {
		in.Kind = "push"
	}
	if in.Kind != "push" {
		writeError(w, http.StatusUnprocessableEntity, "kind must be push")
		return
	}
	t := db.Trigger{QuickstartID: id, Kind: in.Kind, Enabled: true}
	if err := in.apply(&t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	created, err := s.DB.CreateTrigger(r.Context(), u.ID, t)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateTrigger(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad trigger id")
		return
	}
	t, err := s.DB.TriggerForUser(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var in triggerInput
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if in.Kind != "" && in.Kind != t.Kind {
		writeError(w, http.StatusUnprocessableEntity, "a trigger's kind can't be changed")
		return
	}
	if err := in.apply(&t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	updated, err := s.DB.UpdateTrigger(r.Context(), u.ID, t)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteTrigger(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad trigger id")
		return
	}
	if err := s.DB.DeleteTrigger(r.Context(), u.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	names, err := s.DB.CredentialNames(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": names})
}

func (s *Server) setCredential(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	name := r.PathValue("name")
	if !ok || !creds.ValidName(name) {
		writeError(w, http.StatusBadRequest, "credential names are environment variable names, e.g. GEMINI_API_KEY")
		return
	}
	var in struct {
		Value string `json:"value"`
	}
	if err := decode(r, &in); err != nil || in.Value == "" || len(in.Value) > 16<<10 {
		writeError(w, http.StatusBadRequest, "send {\"value\": \"...\"} (up to 16 KB)")
		return
	}
	enc, err := s.Secrets.Seal([]byte(in.Value))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.DB.SetCredential(r.Context(), u.ID, id, name, enc); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	if err := s.DB.DeleteCredential(r.Context(), u.ID, id, r.PathValue("name")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var commitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func (s *Server) createRun(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad quickstart id")
		return
	}
	var in struct {
		Commit string `json:"commit"`
		Branch string `json:"branch"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}
	if in.Commit != "" && !commitRe.MatchString(in.Commit) {
		writeError(w, http.StatusUnprocessableEntity, "commit must be a hex SHA")
		return
	}
	if !branchName.MatchString(in.Branch) || strings.Contains(in.Branch, "..") {
		writeError(w, http.StatusUnprocessableEntity, "invalid branch")
		return
	}
	q, err := s.DB.QuickstartForUser(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	branch := in.Branch
	if branch == "" {
		branch = q.Branch
	}
	uid := u.ID
	run, err := s.DB.Enqueue(r.Context(), q, "manual", &uid, branch, in.Commit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, u db.User) {
	q := r.URL.Query()
	num := func(k string) int64 { n, _ := strconv.ParseInt(q.Get(k), 10, 64); return n }
	f := db.RunFilter{RepoID: num("repo"), QuickstartID: num("quickstart"), Status: q.Get("status"), Before: num("before"), Limit: int(num("limit"))}
	runs, err := s.DB.ListRuns(r.Context(), u.ID, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := map[string]any{"runs": orEmpty(runs)}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if len(runs) == f.Limit {
		resp["next_before"] = runs[len(runs)-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad run id")
		return
	}
	det, err := s.DB.RunDetailForUser(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, det)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad run id")
		return
	}
	run, err := s.DB.CancelRun(r.Context(), u.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// runEvents streams a run's progress as server-sent events until it ends.
// Clients resume with Last-Event-ID (EventSource does this automatically).
func (s *Server) runEvents(w http.ResponseWriter, r *http.Request, u db.User) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad run id")
		return
	}
	ctx := r.Context()
	if _, err := s.DB.RunForUser(ctx, u.ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	last, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	heartbeat := time.Now()
	for {
		evs, err := s.DB.EventsSince(ctx, u.ID, id, last)
		if err != nil {
			return
		}
		for _, e := range evs {
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Kind, data)
			last = e.ID
		}
		if len(evs) > 0 {
			flusher.Flush()
			heartbeat = time.Now()
		}
		run, err := s.DB.RunForUser(ctx, u.ID, id)
		if err != nil {
			return
		}
		if run.Status != db.RunQueued && run.Status != db.RunRunning && len(evs) == 0 {
			data, _ := json.Marshal(run)
			fmt.Fprintf(w, "event: end\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}
		if time.Since(heartbeat) > 15*time.Second {
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
			heartbeat = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
