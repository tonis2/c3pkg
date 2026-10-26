package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tonis/c3pkg/internal/config"
	"github.com/tonis/c3pkg/internal/db"
	"github.com/tonis/c3pkg/internal/storage"
	"github.com/tonis/c3pkg/internal/store"
	"github.com/tonis/c3pkg/migrations"
)

type env struct {
	t      *testing.T
	srv    *httptest.Server
	store  *store.Store
	server *Server
	files  string // directory holding stored .c3l files
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	cfg := config.Config{
		BaseURL:            "http://example.test",
		SessionSecret:      "0123456789abcdef0123456789abcdef",
		GitHubClientID:     "cid",
		GitHubClientSecret: "csecret",
		MaxUploadMB:        2,
		MaxUnzippedMB:      10,
		MaxFiles:           100,
	}
	st := &store.Store{DB: d}
	files := t.TempDir()
	s, err := New(cfg, st, storage.Local{Dir: files})
	if err != nil {
		t.Fatal(err)
	}
	s.limiter.off = true
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, srv: ts, store: st, server: s, files: files}
}

// browser is a cookie-keeping client that does not follow redirects.
type browser struct {
	e *env
	c *http.Client
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) get(path string) (*http.Response, string) {
	b.e.t.Helper()
	resp, err := b.c.Get(b.e.srv.URL + path)
	if err != nil {
		b.e.t.Fatal(err)
	}
	return resp, readBody(resp)
}

func readBody(resp *http.Response) string {
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

func (b *browser) csrf() string {
	b.e.t.Helper()
	u, _ := url.Parse(b.e.srv.URL)
	for _, c := range b.c.Jar.Cookies(u) {
		if c.Name == csrfCookie {
			return c.Value
		}
	}
	b.get("/login") // first request issues the cookie
	return b.csrf()
}

func (b *browser) post(path string, form url.Values, withCSRF bool) (*http.Response, string) {
	b.e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if withCSRF {
		form.Set("csrf", b.csrf())
	}
	resp, err := b.c.PostForm(b.e.srv.URL+path, form)
	if err != nil {
		b.e.t.Fatal(err)
	}
	return resp, readBody(resp)
}

func (b *browser) register(email, login, pw string) *http.Response {
	resp, _ := b.post("/register", url.Values{"email": {email}, "login": {login}, "name": {"Test User"}, "password": {pw}, "confirm": {pw}}, true)
	return resp
}

func TestPublicPages(t *testing.T) {
	b := newEnv(t).browser()
	for path, want := range map[string]string{"/healthz": "ok", "/": "<h1>Packages</h1>", "/static/css/tokens.css": "--accent", "/login": "Log in", "/robots.txt": "Disallow: /api/", "/static/favicon.svg": "<svg", "/register": "Create your account"} {
		resp, body := b.get(path)
		if resp.StatusCode != 200 || !strings.Contains(body, want) {
			t.Errorf("%s: %d %q", path, resp.StatusCode, body)
		}
	}
	if resp, _ := b.get("/nope"); resp.StatusCode != 404 {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
	resp, _ := b.get("/")
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("missing CSP: %q", csp)
	}
	// Protected pages bounce to login and keep the target.
	resp, _ = b.get("/settings")
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Errorf("settings unauthenticated: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestRegisterLoginLogout(t *testing.T) {
	e := newEnv(t)
	b := e.browser()

	if resp := b.register("alice@example.com", "alice", "s3cret-pass"); resp.StatusCode != 303 || resp.Header.Get("Location") != "/dashboard" {
		t.Fatalf("register: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := b.get("/dashboard"); resp.StatusCode != 200 || !strings.Contains(body, "@alice") {
		t.Fatalf("dashboard after register: %d", resp.StatusCode)
	}
	u, err := e.store.UserByLogin(t.Context(), "alice")
	if err != nil || u.PasswordHash == "" || strings.Contains(u.PasswordHash, "s3cret") {
		t.Fatalf("stored user: %+v %v", u, err)
	}

	b.post("/logout", nil, true)
	if resp, _ := b.get("/dashboard"); resp.StatusCode != 303 {
		t.Errorf("dashboard after logout: %d", resp.StatusCode)
	}

	// wrong password and unknown email look the same
	for _, email := range []string{"alice@example.com", "nobody@example.com"} {
		resp, body := b.post("/login", url.Values{"email": {email}, "password": {"wrong-pass-1"}}, true)
		if resp.StatusCode != 401 || !strings.Contains(body, "Wrong email or password.") {
			t.Errorf("bad login %s: %d", email, resp.StatusCode)
		}
	}
	resp, _ := b.post("/login", url.Values{"email": {"ALICE@example.com"}, "password": {"s3cret-pass"}, "next": {"/settings"}}, true)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/settings" {
		t.Errorf("login: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// open redirect attempts fall back to the dashboard
	b.post("/logout", nil, true)
	resp, _ = b.post("/login", url.Values{"email": {"alice@example.com"}, "password": {"s3cret-pass"}, "next": {"//evil.example"}}, true)
	if resp.Header.Get("Location") != "/dashboard" {
		t.Errorf("open redirect: %q", resp.Header.Get("Location"))
	}
}

func TestRegisterValidation(t *testing.T) {
	b := newEnv(t).browser()
	b.register("alice@example.com", "alice", "s3cret-pass")
	b.post("/logout", nil, true)

	cases := []struct {
		name, email, login, pw string
		status                 int
		msg                    string
	}{
		{"bad email", "nope", "bob", "s3cret-pass", 422, "valid email"},
		{"reserved login", "b@example.com", "admin", "s3cret-pass", 422, "lowercase letters"},
		{"short password", "b@example.com", "bob", "short", 422, "at least 8"},
		{"duplicate email", "ALICE@example.com", "bob", "s3cret-pass", 409, "already registered"},
		{"duplicate login, different case", "b@example.com", "Alice", "s3cret-pass", 409, "taken"},
		{"taken login", "b@example.com", "alice", "s3cret-pass", 409, "taken"},
	}
	for _, c := range cases {
		resp, body := b.post("/register", url.Values{"email": {c.email}, "login": {c.login}, "name": {"B"}, "password": {c.pw}, "confirm": {c.pw}}, true)
		if resp.StatusCode != c.status || !strings.Contains(body, c.msg) {
			t.Errorf("%s: %d, body has msg=%v", c.name, resp.StatusCode, strings.Contains(body, c.msg))
		}
	}
	resp, _ := b.post("/register", url.Values{"email": {"b@example.com"}, "login": {"bob"}, "name": {"B"}, "password": {"s3cret-pass"}, "confirm": {"different-pass"}}, true)
	if resp.StatusCode != 422 {
		t.Errorf("mismatched confirm: %d", resp.StatusCode)
	}
}

func TestCSRFRequired(t *testing.T) {
	b := newEnv(t).browser()
	resp, _ := b.post("/register", url.Values{"email": {"a@example.com"}, "login": {"alice"}, "name": {"A"}, "password": {"s3cret-pass"}, "confirm": {"s3cret-pass"}}, false)
	if resp.StatusCode != 403 {
		t.Errorf("post without csrf: %d", resp.StatusCode)
	}
	// a token that is validly signed but not the one in the cookie is also rejected
	other := newEnv(t).browser()
	other.get("/login")
	resp, err := b.c.PostForm(b.e.srv.URL+"/login", url.Values{"csrf": {other.csrf()}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("foreign csrf token accepted: %d", resp.StatusCode)
	}
}

func TestSettingsAndTokens(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.register("alice@example.com", "alice", "s3cret-pass")

	if resp, _ := b.post("/settings/profile", url.Values{"name": {"Alice A"}, "bio": {"hi"}, "github_login": {"alice-gh"}}, true); resp.StatusCode != 303 {
		t.Fatalf("save profile: %d", resp.StatusCode)
	}
	if u, _ := e.store.UserByLogin(t.Context(), "alice"); u.Name != "Alice A" || u.GitHubLogin != "alice-gh" {
		t.Errorf("profile not saved: %+v", u)
	}
	if resp, _ := b.post("/settings/profile", url.Values{"name": {"A"}, "github_login": {"not valid!"}}, true); resp.StatusCode != 422 {
		t.Errorf("invalid github name accepted: %d", resp.StatusCode)
	}

	// change password: wrong current rejected, right current works and old sessions end
	if resp, _ := b.post("/settings/password", url.Values{"current": {"nope"}, "password": {"new-s3cret-pw"}, "confirm": {"new-s3cret-pw"}}, true); resp.StatusCode != 422 {
		t.Errorf("wrong current password: %d", resp.StatusCode)
	}
	other := e.browser()
	other.post("/login", url.Values{"email": {"alice@example.com"}, "password": {"s3cret-pass"}}, true)
	if resp, _ := b.post("/settings/password", url.Values{"current": {"s3cret-pass"}, "password": {"new-s3cret-pw"}, "confirm": {"new-s3cret-pw"}}, true); resp.StatusCode != 303 {
		t.Fatalf("change password: %d", resp.StatusCode)
	}
	if resp, _ := other.get("/dashboard"); resp.StatusCode != 303 {
		t.Errorf("other session survived password change: %d", resp.StatusCode)
	}
	if resp, _ := b.get("/dashboard"); resp.StatusCode != 200 {
		t.Errorf("current session lost after password change: %d", resp.StatusCode)
	}

	// API token: shown once, then usable as a bearer token, then revocable
	resp, body := b.post("/settings/tokens", url.Values{"name": {"laptop"}}, true)
	raw := between(body, `<p class="token mono">`, `</p>`)
	if resp.StatusCode != 200 || !strings.HasPrefix(raw, store.APITokenPrefix) {
		t.Fatalf("create token: %d %q", resp.StatusCode, raw)
	}
	_, page := b.get("/settings")
	if strings.Contains(page, raw) {
		t.Error("token shown again after creation")
	}
	bearer := func() int {
		req, _ := http.NewRequest("GET", e.srv.URL+"/dashboard", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	if got := bearer(); got != 200 {
		t.Errorf("bearer token rejected: %d", got)
	}
	list, _ := e.store.ListAPITokens(t.Context(), 1)
	b.post("/settings/tokens/"+itoa(list[0].ID)+"/revoke", nil, true)
	if got := bearer(); got != 303 {
		t.Errorf("revoked token still works: %d", got)
	}
}

func TestGitHubOAuth(t *testing.T) {
	e := newEnv(t)
	var gh struct {
		id           int64
		login, email string
	}
	gh.id, gh.login, gh.email = 4242, "Octo-Cat", "octo@example.com"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.ParseForm()
			if r.PostForm.Get("code") != "good-code" || r.PostForm.Get("client_secret") != "csecret" {
				json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_x"})
		case "/user":
			json.NewEncoder(w).Encode(map[string]any{"id": gh.id, "login": gh.login, "name": "Octo Cat", "avatar_url": "https://avatars.example/o.png"})
		case "/user/emails":
			json.NewEncoder(w).Encode([]map[string]any{
				{"email": "unverified@example.com", "primary": false, "verified": false},
				{"email": gh.email, "primary": true, "verified": true},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	e.server.github.tokenURL, e.server.github.apiURL, e.server.github.authURL = fake.URL+"/token", fake.URL, fake.URL+"/authorize"

	login := func(b *browser, code string) (*http.Response, string) {
		resp, _ := b.get("/auth/github/login")
		if resp.StatusCode != 303 {
			t.Fatalf("github login: %d", resp.StatusCode)
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if loc.Query().Get("client_id") != "cid" || loc.Query().Get("redirect_uri") != "http://example.test/auth/github/callback" {
			t.Fatalf("authorize url: %s", loc)
		}
		return b.get("/auth/github/callback?code=" + code + "&state=" + loc.Query().Get("state"))
	}

	b := e.browser()
	if resp, _ := login(b, "good-code"); resp.StatusCode != 303 || resp.Header.Get("Location") != "/dashboard" {
		t.Fatalf("callback: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	u, err := e.store.UserByGitHubID(t.Context(), 4242)
	if err != nil || u.Login != "octo-cat" || u.Email != "octo@example.com" || u.PasswordHash != "" {
		t.Fatalf("github user: %+v %v", u, err)
	}
	if resp, _ := b.get("/dashboard"); resp.StatusCode != 200 {
		t.Errorf("not signed in after github login: %d", resp.StatusCode)
	}
	// the same GitHub account logs in again without creating a second user
	b2 := e.browser()
	login(b2, "good-code")
	var n int
	e.store.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 1 {
		t.Errorf("users = %d, want 1", n)
	}

	// failures: bad state, bad code
	b3 := e.browser()
	if resp, _ := b3.get("/auth/github/callback?code=good-code&state=forged"); resp.StatusCode != 400 {
		t.Errorf("forged state accepted: %d", resp.StatusCode)
	}
	if resp, _ := login(e.browser(), "bad-code"); resp.StatusCode != 502 {
		t.Errorf("bad code: %d", resp.StatusCode)
	}

	// an existing password account with the same email is never auto-linked
	gh.id, gh.login = 777, "someone-else"
	e.store.DB.Exec(`UPDATE users SET github_id = NULL WHERE id = 1`)
	resp, body := login(e.browser(), "good-code")
	if resp.StatusCode != 409 || !strings.Contains(body, "already exists") {
		t.Errorf("email collision: %d", resp.StatusCode)
	}

	// signed-in users can link GitHub from settings
	e.store.DB.Exec(`UPDATE users SET email = 'other@example.com' WHERE id = 1`)
	b4 := e.browser()
	b4.register("alice@example.com", "alice", "s3cret-pass")
	gh.id, gh.login = 999, "alice-gh"
	if resp, _ := login(b4, "good-code"); resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/settings") {
		t.Errorf("link: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if u, _ := e.store.UserByLogin(t.Context(), "alice"); u.GitHubID != 999 {
		t.Errorf("not linked: %+v", u)
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return ""
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
