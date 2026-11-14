package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vanguard.run/internal/db"
	"vanguard.run/internal/ghapp"
)

func (s *Server) callbackURL() string {
	return strings.TrimRight(s.BaseURL, "/") + "/auth/github/callback"
}

func (s *Server) secureCookies() bool { return strings.HasPrefix(s.BaseURL, "https://") }

// login starts "Sign in with GitHub".
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	state, err := s.DB.CreateOAuthState(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, s.App.AuthorizeURL(state, s.callbackURL()), http.StatusFound)
}

// callback finishes sign-in: exchange the code, record the user and the
// installations they can see, and start a session.
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if ok, err := s.DB.ConsumeOAuthState(ctx, r.URL.Query().Get("state")); err != nil || !ok {
		writeError(w, http.StatusBadRequest, "sign-in link expired; try again")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing code")
		return
	}
	tok, err := s.App.ExchangeCode(ctx, code, s.callbackURL())
	if err != nil {
		s.Log.Warn("sign-in failed", "err", err)
		writeError(w, http.StatusBadGateway, "GitHub sign-in failed")
		return
	}
	prof, err := s.App.UserProfile(ctx, tok.AccessToken)
	if err != nil {
		s.Log.Warn("profile fetch failed", "err", err)
		writeError(w, http.StatusBadGateway, "could not read your GitHub profile")
		return
	}
	enc, err := s.sealTokens(tok)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.DB.UpsertUser(ctx, db.User{GitHubID: prof.ID, Login: prof.Login, Name: prof.Name, Email: prof.Email, AvatarURL: prof.AvatarURL}, enc)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.syncUser(ctx, u.ID, tok.AccessToken); err != nil {
		s.Log.Warn("initial sync failed", "user", u.Login, "err", err) // sign-in still succeeds; /api/sync retries
	}
	session, err := s.DB.CreateSession(ctx, u.ID, s.SessionTTL)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: session, Path: "/", HttpOnly: true,
		Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode, MaxAge: int(s.SessionTTL.Seconds())})
	dest := s.DashboardURL
	if dest == "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.DB.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookies(), MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sealTokens(t ghapp.UserToken) (db.UserTokens, error) {
	var out db.UserTokens
	var err error
	if out.AccessEnc, err = s.Secrets.Seal([]byte(t.AccessToken)); err != nil {
		return out, err
	}
	if t.RefreshToken != "" {
		if out.RefreshEnc, err = s.Secrets.Seal([]byte(t.RefreshToken)); err != nil {
			return out, err
		}
	}
	out.ExpiresAt = t.ExpiresAt
	return out, nil
}

// userToken returns a usable user-to-server token, refreshing it if needed.
func (s *Server) userToken(ctx context.Context, userID int64) (string, error) {
	t, err := s.DB.UserTokensFor(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(t.AccessEnc) == 0 {
		return "", fmt.Errorf("no GitHub token; sign in again")
	}
	if t.ExpiresAt == nil || time.Until(*t.ExpiresAt) > time.Minute {
		b, err := s.Secrets.Open(t.AccessEnc)
		return string(b), err
	}
	if len(t.RefreshEnc) == 0 {
		return "", fmt.Errorf("GitHub token expired; sign in again")
	}
	refresh, err := s.Secrets.Open(t.RefreshEnc)
	if err != nil {
		return "", err
	}
	nt, err := s.App.Refresh(ctx, string(refresh))
	if err != nil {
		return "", fmt.Errorf("GitHub token expired; sign in again: %w", err)
	}
	enc, err := s.sealTokens(nt)
	if err != nil {
		return "", err
	}
	return nt.AccessToken, s.DB.SetUserTokens(ctx, userID, enc)
}

// syncUser refreshes the installations a user can see and their repos.
func (s *Server) syncUser(ctx context.Context, userID int64, userToken string) error {
	insts, err := s.App.UserInstallations(ctx, userToken)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(insts))
	for _, in := range insts {
		if err := s.syncInstallation(ctx, in); err != nil {
			return err
		}
		ids = append(ids, in.ID)
	}
	return s.DB.SetUserInstallations(ctx, userID, ids)
}

// syncInstallation records an installation and its current repos.
func (s *Server) syncInstallation(ctx context.Context, in ghapp.Installation) error {
	if err := s.DB.UpsertInstallation(ctx, db.Installation{ID: in.ID, AccountID: in.Account.ID, AccountLogin: in.Account.Login,
		AccountType: in.Account.Type, Suspended: in.SuspendedAt != nil}); err != nil {
		return err
	}
	if in.SuspendedAt != nil {
		return nil
	}
	repos, err := s.App.InstallationRepos(ctx, in.ID)
	if err != nil {
		return err
	}
	rs := make([]db.Repo, 0, len(repos))
	for _, r := range repos {
		rs = append(rs, db.Repo{ID: r.ID, FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private, HTMLURL: r.HTMLURL})
	}
	return s.DB.SyncRepos(ctx, in.ID, rs)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u db.User) {
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "install_url": s.App.InstallURL()})
}

// sync re-reads the user's installations and repos from GitHub (e.g. right
// after they install the App on more repos).
func (s *Server) sync(w http.ResponseWriter, r *http.Request, u db.User) {
	tok, err := s.userToken(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := s.syncUser(r.Context(), u.ID, tok); err != nil {
		s.Log.Warn("sync failed", "user", u.Login, "err", err)
		writeError(w, http.StatusBadGateway, "could not sync with GitHub")
		return
	}
	s.listRepos(w, r, u)
}
