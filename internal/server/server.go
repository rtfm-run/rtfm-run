// Package server is rtfm's HTTP API for the dashboard, plus the GitHub
// App sign-in flow and webhook endpoint.
package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rtfm.run/internal/db"
	"rtfm.run/internal/ghapp"
	"rtfm.run/internal/secretbox"
)

// Server holds dependencies.
type Server struct {
	DB           *db.DB
	App          *ghapp.App
	Secrets      *secretbox.Box
	BaseURL      string // public URL of this API, e.g. https://api.rtfm.run
	DashboardURL string // where to send users after sign-in; also the allowed CORS origin
	Log          *slog.Logger
	// SessionTTL defaults to 30 days.
	SessionTTL time.Duration
}

const sessionCookie = "rtfm_session"

// Handler returns the HTTP handler with all routes.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.SessionTTL == 0 {
		s.SessionTTL = 30 * 24 * time.Hour
	}
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) }
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /health", health) // Cloud Run reserves paths ending in "z"

	// The API has no pages of its own: send people who land on it (e.g. from
	// the App's homepage link) to the dashboard.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if s.DashboardURL == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, s.DashboardURL, http.StatusFound)
	})
	mux.HandleFunc("GET /auth/github/login", s.login)
	mux.HandleFunc("GET /auth/github/callback", s.callback)
	mux.HandleFunc("POST /auth/logout", s.logout)
	mux.HandleFunc("POST /webhooks/github", s.webhook)

	api := func(pattern string, h func(http.ResponseWriter, *http.Request, db.User)) {
		mux.Handle(pattern, s.authed(h))
	}
	api("GET /api/me", s.me)
	api("POST /api/sync", s.sync)
	api("GET /api/installations", s.listInstallations)
	api("GET /api/repos", s.listRepos)
	api("GET /api/repos/{id}", s.getRepo)
	api("GET /api/repos/{id}/quickstarts", s.listQuickstarts)
	api("POST /api/repos/{id}/quickstarts", s.createQuickstart)
	api("GET /api/quickstarts/{id}", s.getQuickstart)
	api("PATCH /api/quickstarts/{id}", s.updateQuickstart)
	api("DELETE /api/quickstarts/{id}", s.deleteQuickstart)
	api("GET /api/quickstarts/{id}/steps", s.stepHealth)
	api("GET /api/quickstarts/{id}/pull-requests", s.listPRs)
	api("GET /api/quickstarts/{id}/triggers", s.listTriggers)
	api("POST /api/quickstarts/{id}/triggers", s.createTrigger)
	api("PATCH /api/triggers/{id}", s.updateTrigger)
	api("DELETE /api/triggers/{id}", s.deleteTrigger)
	api("GET /api/quickstarts/{id}/credentials", s.listCredentials)
	api("PUT /api/quickstarts/{id}/credentials/{name}", s.setCredential)
	api("DELETE /api/quickstarts/{id}/credentials/{name}", s.deleteCredential)
	api("POST /api/quickstarts/{id}/runs", s.createRun)
	api("GET /api/runs", s.listRuns)
	api("GET /api/runs/{id}", s.getRun)
	api("POST /api/runs/{id}/cancel", s.cancelRun)
	api("GET /api/runs/{id}/pr", s.getPR)
	api("POST /api/runs/{id}/pr", s.openPR)
	api("GET /api/runs/{id}/events", s.runEvents)

	return s.cors(s.logRequests(mux))
}

// authed resolves the session (cookie, or "Authorization: Bearer <token>"
// for non-browser clients) and guards state-changing browser requests
// against CSRF by requiring a JSON content type.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, db.User)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, viaCookie := "", false
		if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			token = v
		} else if c, err := r.Cookie(sessionCookie); err == nil {
			token, viaCookie = c.Value, true
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		u, err := s.DB.UserBySession(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "session expired; sign in again")
			return
		}
		if viaCookie && r.Method != http.MethodGet && r.Method != http.MethodHead &&
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "send Content-Type: application/json")
			return
		}
		h(w, r, u)
	})
}

// cors allows the dashboard's origin to call the API with credentials.
func (s *Server) cors(next http.Handler) http.Handler {
	origin := ""
	if u, err := url.Parse(s.DashboardURL); err == nil && u.Scheme != "" && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" && r.Header.Get("Origin") == origin && strings.HasPrefix(r.URL.Path, "/api/") {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Last-Event-ID")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail maps storage errors to responses without leaking internals.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.Log.Error("request failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// decode reads a JSON body (max 1 MB), rejecting unknown fields.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
