package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tonis/c3pkg/internal/config"
	"github.com/tonis/c3pkg/internal/store"
)

const stateCookie = "c3_oauth_state"

type githubClient struct {
	clientID, secret          string
	redirectURI               string
	authURL, tokenURL, apiURL string
	http                      *http.Client
}

func newGitHubClient(cfg config.Config) *githubClient {
	return &githubClient{
		clientID:    cfg.GitHubClientID,
		secret:      cfg.GitHubClientSecret,
		redirectURI: cfg.BaseURL + "/auth/github/callback",
		authURL:     "https://github.com/login/oauth/authorize",
		tokenURL:    "https://github.com/login/oauth/access_token",
		apiURL:      "https://api.github.com",
		http:        &http.Client{Timeout: 10 * time.Second},
	}
}

type githubUser struct {
	ID        int64
	Login     string
	Name      string
	AvatarURL string
	Email     string // primary, verified
}

func (g *githubClient) authorizeURL(state string) string {
	q := url.Values{
		"client_id":    {g.clientID},
		"redirect_uri": {g.redirectURI},
		"scope":        {"read:user user:email"},
		"state":        {state},
	}
	return g.authURL + "?" + q.Encode()
}

func (g *githubClient) getJSON(ctx context.Context, method, u, token string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github %s: status %d", req.URL.Path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (g *githubClient) fetchUser(ctx context.Context, code string) (*githubUser, error) {
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	err := g.getJSON(ctx, http.MethodPost, g.tokenURL, "", url.Values{
		"client_id": {g.clientID}, "client_secret": {g.secret}, "code": {code}, "redirect_uri": {g.redirectURI},
	}, &tok)
	if err != nil {
		return nil, err
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("github token exchange failed: %s", tok.Error)
	}
	var me struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := g.getJSON(ctx, http.MethodGet, g.apiURL+"/user", tok.AccessToken, nil, &me); err != nil {
		return nil, err
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := g.getJSON(ctx, http.MethodGet, g.apiURL+"/user/emails", tok.AccessToken, nil, &emails); err != nil {
		return nil, err
	}
	u := &githubUser{ID: me.ID, Login: me.Login, Name: me.Name, AvatarURL: me.AvatarURL}
	for _, e := range emails {
		if e.Primary && e.Verified {
			u.Email = e.Email
		}
	}
	return u, nil
}

func (s *Server) githubLogin(w http.ResponseWriter, r *http.Request) {
	if !githubEnabled(s) {
		http.NotFound(w, r)
		return
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: state, Path: "/auth/github", MaxAge: 600,
		HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, s.github.authorizeURL(state), http.StatusSeeOther)
}

func (s *Server) oauthError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.WriteHeader(status)
	s.render(w, r, "message", pageData{Title: "Sign-in problem", Data: msg})
}

func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	if !githubEnabled(s) {
		http.NotFound(w, r)
		return
	}
	c, err := r.Cookie(stateCookie)
	state := r.URL.Query().Get("state")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		s.oauthError(w, r, http.StatusBadRequest, "The sign-in request expired or was invalid. Please try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/auth/github", MaxAge: -1})
	code := r.URL.Query().Get("code")
	if code == "" {
		s.oauthError(w, r, http.StatusBadRequest, "GitHub sign-in was cancelled.")
		return
	}
	gh, err := s.github.fetchUser(r.Context(), code)
	if err != nil {
		log.Printf("github: %v", err)
		s.oauthError(w, r, http.StatusBadGateway, "Could not complete sign-in with GitHub. Please try again.")
		return
	}

	// Signed in already: link this GitHub identity to the current account.
	if cur := userFrom(r); cur != nil {
		if existing, err := s.store.UserByGitHubID(r.Context(), gh.ID); err == nil && existing.ID != cur.ID {
			s.oauthError(w, r, http.StatusConflict, "That GitHub account is already linked to another user.")
			return
		}
		if err := s.store.LinkGitHub(r.Context(), cur.ID, gh.ID, gh.Login); err != nil {
			log.Printf("link github: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings?flash=GitHub+account+linked", http.StatusSeeOther)
		return
	}

	u, err := s.store.UserByGitHubID(r.Context(), gh.ID)
	if errors.Is(err, store.ErrNotFound) {
		u, err = s.createGitHubUser(r, gh)
		if errors.Is(err, store.ErrEmailTaken) {
			// Never auto-link by email: the existing account's email is unverified.
			s.oauthError(w, r, http.StatusConflict, "An account with your GitHub email already exists. Log in with your password, then link GitHub from Settings.")
			return
		}
		if errors.Is(err, errNoEmail) {
			s.oauthError(w, r, http.StatusBadRequest, "Your GitHub account has no verified primary email. Add one on GitHub, or register with email and password.")
			return
		}
	}
	if err != nil {
		log.Printf("github user: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if u.Banned {
		s.oauthError(w, r, http.StatusForbidden, "This account is suspended.")
		return
	}
	if err := s.setSession(w, r, u.ID); err != nil {
		log.Printf("session: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

var errNoEmail = errors.New("github account has no verified email")

func (s *Server) createGitHubUser(r *http.Request, gh *githubUser) (*store.User, error) {
	if gh.Email == "" {
		return nil, errNoEmail
	}
	base := strings.ToLower(gh.Login)
	name := gh.Name
	if name == "" {
		name = gh.Login
	}
	for i := 0; i < 20; i++ {
		login := base
		if i > 0 {
			login = base + "-" + strconv.Itoa(i+1)
		}
		if !store.ValidLogin(login) {
			continue
		}
		u, err := s.store.CreateUser(r.Context(), gh.Email, login, name, "", gh.ID, gh.Login, gh.AvatarURL)
		if errors.Is(err, store.ErrLoginTaken) {
			continue
		}
		return u, err
	}
	return nil, store.ErrLoginTaken
}
