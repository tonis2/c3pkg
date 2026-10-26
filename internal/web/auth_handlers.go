package web

import (
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/tonis/c3pkg/internal/auth"
	"github.com/tonis/c3pkg/internal/store"
)

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	if userFrom(r) != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	s.render(w, r, "register", pageData{NoLoginDialog: true, Title: "Create account", Data: githubEnabled(s)})
}

func githubEnabled(s *Server) bool { return s.cfg.GitHubClientID != "" }

func validatePassword(pw string) string {
	switch {
	case utf8.RuneCountInString(pw) < auth.MinPasswordLen:
		return "Use at least 8 characters."
	case len(pw) > auth.MaxPasswordBytes:
		return "Password is too long."
	}
	return ""
}

func (s *Server) registerSubmit(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PostFormValue("email"))
	login := strings.ToLower(strings.TrimSpace(r.PostFormValue("login")))
	name := strings.TrimSpace(r.PostFormValue("name"))
	pw, confirm := r.PostFormValue("password"), r.PostFormValue("confirm")

	errs := map[string]string{}
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		errs["email"] = "Enter a valid email address."
	}
	if !store.ValidLogin(login) {
		errs["login"] = "2–39 characters: lowercase letters, digits and hyphens; must start and end with a letter or digit."
	}
	if name == "" || utf8.RuneCountInString(name) > 80 {
		errs["name"] = "Enter your name (up to 80 characters)."
	}
	if msg := validatePassword(pw); msg != "" {
		errs["password"] = msg
	} else if pw != confirm {
		errs["confirm"] = "Passwords do not match."
	}

	form := map[string]string{"email": email, "login": login, "name": name}
	fail := func(status int) {
		w.WriteHeader(status)
		s.render(w, r, "register", pageData{NoLoginDialog: true, Title: "Create account", Errors: errs, Form: form, Data: githubEnabled(s)})
	}
	if len(errs) > 0 {
		fail(http.StatusUnprocessableEntity)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		log.Printf("hash: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	u, err := s.store.CreateUser(r.Context(), email, login, name, hash, 0, "", "")
	switch {
	case errors.Is(err, store.ErrEmailTaken):
		errs["email"] = "This email is already registered."
		fail(http.StatusConflict)
		return
	case errors.Is(err, store.ErrLoginTaken):
		errs["login"] = "This username is taken."
		fail(http.StatusConflict)
		return
	case err != nil:
		log.Printf("create user: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.setSession(w, r, u.ID); err != nil {
		log.Printf("session: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if userFrom(r) != nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", pageData{NoLoginDialog: true, Title: "Log in", Form: map[string]string{"next": r.URL.Query().Get("next")}, Data: githubEnabled(s)})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PostFormValue("email"))
	pw := r.PostFormValue("password")
	next := r.PostFormValue("next")

	u, err := s.store.UserByEmail(r.Context(), email)
	ok := false
	switch {
	case err == nil && u.PasswordHash != "" && len(pw) <= auth.MaxPasswordBytes:
		ok = auth.VerifyPassword(pw, u.PasswordHash)
	default:
		auth.DummyVerify(pw)
	}
	if !ok || u.Banned {
		if wantsJSON(r) {
			apiError(w, http.StatusUnauthorized, "Wrong email or password.", nil)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, r, "login", pageData{NoLoginDialog: true, Title: "Log in", Errors: map[string]string{"form": "Wrong email or password."},
			Form: map[string]string{"email": email, "next": next}, Data: githubEnabled(s)})
		return
	}
	if err := s.setSession(w, r, u.ID); err != nil {
		log.Printf("session: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next": safeNext(next)})
		return
	}
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.store.DeleteSession(r.Context(), c.Value)
	}
	s.clearSession(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
