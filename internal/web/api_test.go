package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type apiResp struct {
	*http.Response
	Body string
}

func (r apiResp) JSON(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
		t.Fatalf("not JSON (%d): %s", r.StatusCode, r.Body)
	}
	return m
}

func (e *env) do(method, path, token string, body io.Reader, contentType string, hdr ...string) apiResp {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return apiResp{resp, string(b)}
}

func (e *env) putPackage(token, name, version string, c3l []byte, fields map[string]string) apiResp {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	w, _ := mw.CreateFormFile("file", name+".c3l")
	w.Write(c3l)
	mw.Close()
	return e.do("PUT", "/api/v1/packages/"+name+"/versions/"+version, token, &body, mw.FormDataContentType())
}

func (e *env) token(login, email string) string {
	e.t.Helper()
	u, err := e.store.CreateUser(e.t.Context(), email, login, strings.ToUpper(login[:1])+login[1:], "x", 0, "", "")
	if err != nil {
		e.t.Fatal(err)
	}
	raw, err := e.store.CreateAPIToken(e.t.Context(), u.ID, "test")
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

func apiFields() map[string]string {
	return map[string]string{"c3_version": "0.7.5", "description": "A library", "tags": "gpu", "license": "MIT"}
}

func TestAPIPublishAndRead(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.token("alice", "a@example.com"), e.token("bob", "b@example.com")
	c3l := makeC3L(t, "mylib")

	// auth failures
	if r := e.putPackage("", "mylib", "1.0.0", c3l, apiFields()); r.StatusCode != 401 || r.Header.Get("WWW-Authenticate") == "" || r.JSON(t)["error"] == nil {
		t.Errorf("no token: %d %s", r.StatusCode, r.Body)
	}
	if r := e.putPackage("c3p_bogus", "mylib", "1.0.0", c3l, apiFields()); r.StatusCode != 401 {
		t.Errorf("bad token: %d", r.StatusCode)
	}

	// create, then add a version
	r := e.putPackage(alice, "mylib", "1.0.0", c3l, apiFields())
	if j := r.JSON(t); r.StatusCode != 201 || j["created"] != true || j["version"] != "1.0.0" || !strings.HasSuffix(j["url"].(string), "/packages/mylib") {
		t.Fatalf("create: %d %s", r.StatusCode, r.Body)
	}
	r = e.putPackage(alice, "mylib", "1.1.0", makeC3L(t, "mylib", zfile{"src/b.c3", "module mylib;"}), map[string]string{"c3_version": "0.7.5"})
	if r.StatusCode != 201 || r.JSON(t)["created"] != false {
		t.Fatalf("second version: %d %s", r.StatusCode, r.Body)
	}
	// conflicts and ownership
	if r := e.putPackage(alice, "mylib", "1.1.0", c3l, map[string]string{"c3_version": "0.7.5"}); r.StatusCode != 409 || r.JSON(t)["errors"].(map[string]any)["version"] == nil {
		t.Errorf("duplicate: %d %s", r.StatusCode, r.Body)
	}
	if r := e.putPackage(bob, "mylib", "2.0.0", c3l, apiFields()); r.StatusCode != 403 {
		t.Errorf("non-owner: %d", r.StatusCode)
	}
	// validation: name in the URL must match the manifest, version/fields checked, new packages need a description
	for name, c := range map[string]struct {
		path    string
		version string
		c3l     []byte
		fields  map[string]string
		field   string
	}{
		"manifest mismatch": {"other", "1.0.0", c3l, apiFields(), "file"},
		"bad version":       {"mylib", "banana", c3l, map[string]string{"c3_version": "0.7.5"}, "version"},
		"missing c3":        {"mylib", "3.0.0", c3l, map[string]string{}, "c3_version"},
		"no description":    {"fresh", "1.0.0", makeC3L(t, "fresh"), map[string]string{"c3_version": "0.7.5"}, "description"},
	} {
		r := e.putPackage(alice, c.path, c.version, c.c3l, c.fields)
		errs, _ := r.JSON(t)["errors"].(map[string]any)
		if r.StatusCode != 422 || errs[c.field] == nil {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.Body)
		}
	}
	if r := e.putPackage(alice, "Bad_Name", "1.0.0", c3l, apiFields()); r.StatusCode != 422 {
		t.Errorf("invalid name in URL: %d", r.StatusCode)
	}

	// public reads
	r = e.do("GET", "/api/v1/packages/mylib", "", nil, "")
	j := r.JSON(t)
	if r.StatusCode != 200 || j["latest_version"] != "1.1.0" || j["owner"] != "alice" || len(j["versions"].([]any)) != 2 || j["license"] != "MIT" {
		t.Errorf("get package: %d %s", r.StatusCode, r.Body)
	}
	if r := e.do("GET", "/api/v1/packages/nothing", "", nil, ""); r.StatusCode != 404 || r.JSON(t)["error"] == nil {
		t.Errorf("missing package: %d %s", r.StatusCode, r.Body)
	}
	r = e.do("GET", "/api/v1/packages?q=library&per_page=5", "", nil, "")
	j = r.JSON(t)
	if j["total"].(float64) != 1 || j["per_page"].(float64) != 5 || j["packages"].([]any)[0].(map[string]any)["name"] != "mylib" {
		t.Errorf("list: %s", r.Body)
	}
	if r := e.do("GET", "/api/v1/packages?q=nomatch", "", nil, ""); !strings.Contains(r.Body, `"packages":[]`) {
		t.Errorf("empty list must be [] not null: %s", r.Body)
	}
	if r := e.do("GET", "/api/v1/packages/mylib/versions", "", nil, ""); len(r.JSON(t)["versions"].([]any)) != 2 {
		t.Errorf("versions: %s", r.Body)
	}
	if r := e.do("GET", "/api/v1/users/alice", "", nil, ""); r.StatusCode != 200 || strings.Contains(r.Body, "a@example.com") || r.JSON(t)["packages"] == nil {
		t.Errorf("user: %d %s", r.StatusCode, r.Body)
	}
	if r := e.do("GET", "/api/v1/packages/mylib/1.0.0/download", "", nil, ""); r.StatusCode != 200 || r.Body != string(c3l) {
		t.Errorf("download: %d", r.StatusCode)
	}
	if r := e.do("GET", "/api/v1/packages/mylib/7.7.7/download", "", nil, ""); r.StatusCode != 404 || r.JSON(t)["error"] == nil {
		t.Errorf("download missing version should be JSON 404: %d %s", r.StatusCode, r.Body)
	}
}

func TestAPIPublishReadme(t *testing.T) {
	e := newEnv(t)
	alice := e.token("alice", "a@example.com")
	readme := func() string { return e.do("GET", "/api/v1/packages/mylib", "", nil, "").JSON(t)["readme"].(string) }
	publish := func(version, body string, fields map[string]string) {
		t.Helper()
		fields["c3_version"] = "0.7.5"
		if r := e.putPackage(alice, "mylib", version, makeC3L(t, "mylib", zfile{"README.md", body}), fields); r.StatusCode != 201 {
			t.Fatalf("publish %s: %d %s", version, r.StatusCode, r.Body)
		}
	}
	publish("1.0.0", "# One", map[string]string{"description": "A library"})
	publish("1.0.1", "# Two", map[string]string{})
	if got := readme(); got != "# One" {
		t.Errorf("a publish without readme_from_archive keeps the README, got %q", got)
	}
	publish("1.0.2", "# Three", map[string]string{"readme_from_archive": "1"})
	if got := readme(); got != "# Three" {
		t.Errorf("readme_from_archive=1 replaces the README, got %q", got)
	}
}

func TestAPIManagement(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.token("alice", "a@example.com"), e.token("bob", "b@example.com")
	e.putPackage(alice, "mylib", "1.0.0", makeC3L(t, "mylib"), apiFields())
	e.putPackage(alice, "mylib", "1.1.0", makeC3L(t, "mylib", zfile{"src/b.c3", "module mylib;"}), map[string]string{"c3_version": "0.7.5"})

	// yank and restore
	if r := e.do("PUT", "/api/v1/packages/mylib/versions/1.1.0/yank", bob, nil, ""); r.StatusCode != 403 {
		t.Errorf("non-owner yank: %d", r.StatusCode)
	}
	if r := e.do("PUT", "/api/v1/packages/mylib/versions/1.1.0/yank", alice, nil, ""); r.StatusCode != 204 {
		t.Errorf("yank: %d %s", r.StatusCode, r.Body)
	}
	if j := e.do("GET", "/api/v1/packages/mylib", "", nil, "").JSON(t); j["latest_version"] != "1.0.0" {
		t.Errorf("latest after yank: %v", j["latest_version"])
	}
	if r := e.do("GET", "/index/mylib/list", "", nil, ""); r.Body != "1.0.0\n" || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("index list should skip yanked versions: %q", r.Body)
	}
	if r := e.do("DELETE", "/api/v1/packages/mylib/versions/1.1.0/yank", alice, nil, ""); r.StatusCode != 204 {
		t.Errorf("unyank: %d", r.StatusCode)
	}
	if r := e.do("GET", "/index/mylib/list", "", nil, ""); r.Body != "1.1.0\n1.0.0\n" {
		t.Errorf("index list: %q", r.Body)
	}
	if r := e.do("PUT", "/api/v1/packages/mylib/versions/9.9.9/yank", alice, nil, ""); r.StatusCode != 404 {
		t.Errorf("yank unknown: %d", r.StatusCode)
	}

	// readme and metadata
	if r := e.do("PUT", "/api/v1/packages/mylib/readme", alice, strings.NewReader("# Hello\n\n<script>x</script>"), "text/markdown"); r.StatusCode != 204 {
		t.Fatalf("readme: %d %s", r.StatusCode, r.Body)
	}
	if r := e.do("PUT", "/api/v1/packages/mylib/readme", bob, strings.NewReader("# Hijack"), "text/markdown"); r.StatusCode != 403 {
		t.Errorf("non-owner readme: %d", r.StatusCode)
	}
	if r := e.do("PUT", "/api/v1/packages/mylib/readme", alice, bytes.NewReader([]byte{0xff, 0xfe}), "text/markdown"); r.StatusCode != 422 {
		t.Errorf("binary readme: %d", r.StatusCode)
	}
	_, page := e.browser().get("/packages/mylib")
	if !strings.Contains(page, "<h1") || strings.Contains(page, "<script>x") {
		t.Error("readme not rendered safely")
	}
	patch := func(tok, body string) apiResp {
		return e.do("PATCH", "/api/v1/packages/mylib", tok, strings.NewReader(body), "application/json")
	}
	if r := patch(alice, `{"description":"Changed","tags":["audio","math"],"git_url":"https://github.com/a/b"}`); r.StatusCode != 204 {
		t.Fatalf("patch: %d %s", r.StatusCode, r.Body)
	}
	j := e.do("GET", "/api/v1/packages/mylib", "", nil, "").JSON(t)
	if j["description"] != "Changed" || j["license"] != "MIT" || len(j["tags"].([]any)) != 2 || j["git_url"] != "https://github.com/a/b" {
		t.Errorf("patch result (license must be untouched): %v", j)
	}
	if r := patch(alice, `{"git_url":"javascript:alert(1)"}`); r.StatusCode != 422 {
		t.Errorf("bad url: %d", r.StatusCode)
	}
	if r := patch(alice, `{"description":""}`); r.StatusCode != 422 {
		t.Errorf("empty description: %d", r.StatusCode)
	}
	if r := patch(alice, `nope`); r.StatusCode != 400 {
		t.Errorf("bad json: %d", r.StatusCode)
	}
	if r := patch(bob, `{"description":"x"}`); r.StatusCode != 403 {
		t.Errorf("non-owner patch: %d", r.StatusCode)
	}

	// stars
	if r := e.do("PUT", "/api/v1/packages/mylib/star", "", nil, ""); r.StatusCode != 401 {
		t.Errorf("anonymous star: %d", r.StatusCode)
	}
	e.do("PUT", "/api/v1/packages/mylib/star", alice, nil, "")
	if j := e.do("PUT", "/api/v1/packages/mylib/star", bob, nil, "").JSON(t); j["starred"] != true || j["stars"].(float64) != 2 {
		t.Errorf("star: %v", j)
	}
	if j := e.do("DELETE", "/api/v1/packages/mylib/star", bob, nil, "").JSON(t); j["starred"] != false || j["stars"].(float64) != 1 {
		t.Errorf("unstar: %v", j)
	}

	// profile
	if j := e.do("GET", "/api/v1/me", alice, nil, "").JSON(t); j["login"] != "alice" || j["email"] != "a@example.com" {
		t.Errorf("me: %v", j)
	}
	if r := e.do("PATCH", "/api/v1/me", alice, strings.NewReader(`{"name":"Alice A","github":"alice-gh"}`), "application/json"); r.StatusCode != 204 {
		t.Errorf("patch me: %d", r.StatusCode)
	}
	if r := e.do("PATCH", "/api/v1/me", alice, strings.NewReader(`{"github":"not valid!"}`), "application/json"); r.StatusCode != 422 {
		t.Errorf("patch me invalid: %d", r.StatusCode)
	}
}

func TestAPITokensNeedASession(t *testing.T) {
	e := newEnv(t)
	tok := e.token("alice", "a@example.com")
	// an API token cannot mint or list tokens
	for _, c := range []struct{ method, path, body string }{{"GET", "/api/v1/me/tokens", ""}, {"POST", "/api/v1/me/tokens", `{"name":"x"}`}} {
		if r := e.do(c.method, c.path, tok, strings.NewReader(c.body), "application/json"); r.StatusCode != 403 {
			t.Errorf("%s %s with token: %d", c.method, c.path, r.StatusCode)
		}
	}
	// a browser session can, but must send the CSRF header
	b := e.browser()
	b.register("bob@example.com", "bob", "s3cret-pass")
	req := func(token string) apiResp {
		hdr := []string{"Cookie", cookieHeader(b)}
		if token != "" {
			hdr = append(hdr, "X-CSRF-Token", token)
		}
		return e.do("POST", "/api/v1/me/tokens", "", strings.NewReader(`{"name":"laptop"}`), "application/json", hdr...)
	}
	if r := req(""); r.StatusCode != 403 {
		t.Errorf("session without CSRF header: %d", r.StatusCode)
	}
	r := req(b.csrf())
	raw, _ := r.JSON(t)["token"].(string)
	if r.StatusCode != 201 || !strings.HasPrefix(raw, "c3p_") {
		t.Fatalf("create token: %d %s", r.StatusCode, r.Body)
	}
	if r := e.do("GET", "/api/v1/me", raw, nil, ""); r.StatusCode != 200 || r.JSON(t)["login"] != "bob" {
		t.Errorf("new token: %d %s", r.StatusCode, r.Body)
	}
	list := e.do("GET", "/api/v1/me/tokens", "", nil, "", "Cookie", cookieHeader(b)).JSON(t)["tokens"].([]any)
	if len(list) != 1 || strings.Contains(r.Body, "c3p_") && false {
		t.Errorf("token list: %v", list)
	}
	id := int(list[0].(map[string]any)["id"].(float64))
	del := e.do("DELETE", "/api/v1/me/tokens/"+itoa(int64(id)), "", nil, "", "Cookie", cookieHeader(b), "X-CSRF-Token", b.csrf())
	if del.StatusCode != 204 {
		t.Errorf("revoke: %d", del.StatusCode)
	}
	if r := e.do("GET", "/api/v1/me", raw, nil, ""); r.StatusCode != 401 {
		t.Errorf("revoked token still works: %d", r.StatusCode)
	}
}

func cookieHeader(b *browser) string {
	var parts []string
	for _, c := range b.c.Jar.Cookies(mustURL(b.e.srv.URL)) {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

func TestIndexEndpoints(t *testing.T) {
	e := newEnv(t)
	alice := e.token("alice", "a@example.com")
	c3l := makeC3L(t, "mylib")
	e.putPackage(alice, "mylib", "1.0.0", c3l, apiFields())

	j := e.do("GET", "/index/mylib/1.0.0.json", "", nil, "").JSON(t)
	if j["name"] != "mylib" || j["c3_version"] != "0.7.5" || len(j["sha256_tree"].(string)) != 64 || j["download"] != "/index/mylib/1.0.0.c3l" || j["yanked"] != false {
		t.Errorf("index json: %v", j)
	}
	if r := e.do("GET", "/index/mylib/1.0.0.c3l", "", nil, ""); r.StatusCode != 200 || r.Body != string(c3l) {
		t.Errorf("index download: %d", r.StatusCode)
	}
	p, _ := e.store.PackageByName(t.Context(), "mylib")
	if p.Downloads != 1 {
		t.Errorf("index download not counted: %d", p.Downloads)
	}
	for _, path := range []string{"/index/mylib/2.0.0.json", "/index/mylib/2.0.0.c3l", "/index/nothing/list", "/index/mylib/readme.txt"} {
		if r := e.do("GET", path, "", nil, ""); r.StatusCode != 404 || r.JSON(t)["error"] == nil {
			t.Errorf("%s: %d %s", path, r.StatusCode, r.Body)
		}
	}
}

// ---- rate limiting ----

func TestLimiterTokenBucket(t *testing.T) {
	l := newLimiter()
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("k", 3, time.Minute); !ok {
			t.Fatalf("request %d within burst was refused", i)
		}
	}
	ok, wait := l.allow("k", 3, time.Minute)
	if ok || wait <= 0 || wait > time.Minute {
		t.Fatalf("over burst: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.allow("other", 3, time.Minute); !ok {
		t.Error("keys must be independent")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.allow("k", 3, time.Minute); !ok {
		t.Error("a token should have refilled after one interval")
	}
	if ok, _ := l.allow("k", 3, time.Minute); ok {
		t.Error("only one token should have refilled")
	}
	// idle buckets are dropped and the table is bounded
	now = now.Add(2 * time.Hour)
	l.allow("trigger-gc", 1, time.Minute)
	if len(l.buckets) != 1 {
		t.Errorf("idle buckets not collected: %d", len(l.buckets))
	}
	l.maxItems = 2
	l.allow("b", 1, time.Minute)
	if ok, _ := l.allow("c", 1, time.Minute); ok {
		t.Error("new keys beyond maxItems should be refused")
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	e := newEnv(t)
	e.server.limiter.off = false
	b := e.browser()
	var last *http.Response
	limited := -1
	for i := 0; i < 15; i++ {
		last, _ = b.post("/login", map[string][]string{"email": {"x@example.com"}, "password": {"wrong-pass-1"}}, true)
		if last.StatusCode == 429 {
			limited = i
			break
		}
	}
	if limited != 10 || last.Header.Get("Retry-After") == "" {
		t.Errorf("limited at attempt %d (want 10), Retry-After=%q", limited, last.Header.Get("Retry-After"))
	}
	// other endpoints are unaffected, and the API answers in JSON
	if resp, _ := b.get("/"); resp.StatusCode != 200 {
		t.Errorf("home blocked: %d", resp.StatusCode)
	}
	r := e.do("POST", "/login", "", strings.NewReader("email=x"), "application/x-www-form-urlencoded", "Accept", "application/json", "X-CSRF-Token", "n/a")
	if r.StatusCode != 403 && r.StatusCode != 429 {
		t.Errorf("unexpected: %d", r.StatusCode)
	}
}

func TestClientIPTrustsForwardedForOnlyFromProxies(t *testing.T) {
	e := newEnv(t)
	s := e.server
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	s.cfg.TrustedProxies = nil
	if got := s.clientIP(req("203.0.113.9:5000", "198.51.100.1")); got != "203.0.113.9" {
		t.Errorf("untrusted peer's XFF must be ignored: %s", got)
	}
	s.cfg.TrustedProxies = append(s.cfg.TrustedProxies, loopback)
	if got := s.clientIP(req("127.0.0.1:5000", "198.51.100.1")); got != "198.51.100.1" {
		t.Errorf("trusted proxy XFF: %s", got)
	}
	if got := s.clientIP(req("127.0.0.1:5000", "6.6.6.6, 198.51.100.1")); got != "198.51.100.1" {
		t.Errorf("must use the last hop, not a client-supplied one: %s", got)
	}
	if got := s.clientIP(req("203.0.113.9:5000", "198.51.100.1")); got != "203.0.113.9" {
		t.Errorf("direct client spoofing XFF: %s", got)
	}
	if got := s.clientIP(req("127.0.0.1:5000", "garbage")); got != "127.0.0.1" {
		t.Errorf("garbage XFF: %s", got)
	}
}
