package web

import (
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tonis/c3pkg/internal/auth"
)

var githubLoginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	pkgs, err := s.store.PackagesByOwner(r.Context(), userFrom(r).ID)
	if err != nil {
		s.serverError(w, "dashboard packages", err)
		return
	}
	s.render(w, r, "dashboard", pageData{Title: "Dashboard", Data: pkgs})
}

type settingsData struct {
	Tokens      any
	NewToken    string
	GitHubReady bool
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, d pageData, newToken string) {
	tokens, err := s.store.ListAPITokens(r.Context(), userFrom(r).ID)
	if err != nil {
		log.Printf("list tokens: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d.Title = "Settings"
	d.Data = settingsData{Tokens: tokens, NewToken: newToken, GitHubReady: githubEnabled(s)}
	s.render(w, r, "settings", d)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	s.renderSettings(w, r, pageData{
		Form:  map[string]string{"name": u.Name, "bio": u.Bio, "github_login": u.GitHubLogin},
		Flash: r.URL.Query().Get("flash"),
	}, "")
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	name := strings.TrimSpace(r.PostFormValue("name"))
	bio := strings.TrimSpace(r.PostFormValue("bio"))
	gh := strings.TrimSpace(r.PostFormValue("github_login"))
	errs := map[string]string{}
	if name == "" || utf8.RuneCountInString(name) > 80 {
		errs["name"] = "Enter your name (up to 80 characters)."
	}
	if utf8.RuneCountInString(bio) > 280 {
		errs["bio"] = "Keep the bio under 280 characters."
	}
	if gh != "" && !githubLoginRe.MatchString(gh) {
		errs["github_login"] = "That is not a valid GitHub username."
	}
	if len(errs) > 0 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.renderSettings(w, r, pageData{Errors: errs, Form: map[string]string{"name": name, "bio": bio, "github_login": gh}}, "")
		return
	}
	if err := s.store.UpdateProfile(r.Context(), u.ID, name, bio, gh); err != nil {
		log.Printf("update profile: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?flash=Profile+saved", http.StatusSeeOther)
}

func (s *Server) savePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	current, pw, confirm := r.PostFormValue("current"), r.PostFormValue("password"), r.PostFormValue("confirm")
	errs := map[string]string{}
	// GitHub-only accounts have no password yet, so they can set one without a current password.
	if u.PasswordHash != "" && (len(current) > auth.MaxPasswordBytes || !auth.VerifyPassword(current, u.PasswordHash)) {
		errs["current"] = "Current password is wrong."
	}
	if msg := validatePassword(pw); msg != "" {
		errs["password"] = msg
	} else if pw != confirm {
		errs["confirm"] = "Passwords do not match."
	}
	form := map[string]string{"name": u.Name, "bio": u.Bio, "github_login": u.GitHubLogin}
	if len(errs) > 0 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.renderSettings(w, r, pageData{Errors: errs, Form: form}, "")
		return
	}
	hash, err := auth.HashPassword(pw)
	if err == nil {
		err = s.store.SetPassword(r.Context(), u.ID, hash)
	}
	if err == nil {
		// Sign out every session, then keep this browser signed in with a fresh one.
		err = s.store.DeleteUserSessions(r.Context(), u.ID)
	}
	if err == nil {
		err = s.setSession(w, r, u.ID)
	}
	if err != nil {
		log.Printf("change password: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?flash=Password+changed", http.StatusSeeOther)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	name := strings.TrimSpace(r.PostFormValue("name"))
	form := map[string]string{"name": u.Name, "bio": u.Bio, "github_login": u.GitHubLogin}
	if name == "" || utf8.RuneCountInString(name) > 60 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.renderSettings(w, r, pageData{Errors: map[string]string{"token_name": "Give the token a name (up to 60 characters)."}, Form: form}, "")
		return
	}
	raw, err := s.store.CreateAPIToken(r.Context(), u.ID, name)
	if err != nil {
		log.Printf("create token: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderSettings(w, r, pageData{Form: form, Flash: "Token created. Copy it now; it will not be shown again."}, raw)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.RevokeAPIToken(r.Context(), userFrom(r).ID, id); err != nil {
		log.Printf("revoke token: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?flash=Token+revoked", http.StatusSeeOther)
}
