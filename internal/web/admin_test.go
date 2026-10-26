package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminAccessControl(t *testing.T) {
	e := newEnv(t)
	alice, anon := e.browser(), e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")
	if resp, _ := anon.get("/admin"); resp.StatusCode != 303 {
		t.Errorf("anonymous: %d", resp.StatusCode)
	}
	if resp, _ := alice.get("/admin"); resp.StatusCode != 404 {
		t.Errorf("non-admin must see 404, got %d", resp.StatusCode)
	}
	for _, path := range []string{"/admin/packages/x/remove", "/admin/users/alice/ban", "/admin/reports/1/resolve"} {
		if resp, _ := alice.post(path, nil, true); resp.StatusCode != 404 {
			t.Errorf("non-admin POST %s: %d", path, resp.StatusCode)
		}
	}
	if err := e.store.SetAdminByEmail(t.Context(), "ALICE@example.com", true); err != nil {
		t.Fatal(err)
	}
	if resp, body := alice.get("/admin"); resp.StatusCode != 200 || !strings.Contains(body, "Open reports") {
		t.Errorf("admin: %d", resp.StatusCode)
	}
	if err := e.store.SetAdminByEmail(t.Context(), "nobody@example.com", true); err == nil {
		t.Error("granting admin to an unknown email should fail")
	}
}

func TestReportAndModerate(t *testing.T) {
	e := newEnv(t)
	admin, owner, reporter, anon := e.browser(), e.browser(), e.browser(), e.browser()
	admin.register("admin@example.com", "boss", "s3cret-pass")
	e.store.SetAdminByEmail(t.Context(), "admin@example.com", true)
	owner.register("owner@example.com", "owner", "s3cret-pass")
	reporter.register("rep@example.com", "rep", "s3cret-pass")
	publish(t, owner, "badlib", "1.0.0", "Suspicious library", "x")
	publish(t, owner, "goodlib", "1.0.0", "Fine library", "x")

	// reporting needs a login and a real reason
	if resp, _ := anon.get("/packages/badlib/report"); resp.StatusCode != 303 {
		t.Errorf("anonymous report form: %d", resp.StatusCode)
	}
	if resp, body := reporter.post("/packages/badlib/report", map[string][]string{"reason": {"bad"}}, true); resp.StatusCode != 422 || !strings.Contains(body, "10 to 1000") {
		t.Errorf("short reason: %d", resp.StatusCode)
	}
	for i := 0; i < 2; i++ { // a second report from the same user is ignored while one is open
		if resp, _ := reporter.post("/packages/badlib/report", map[string][]string{"reason": {"Contains a credential stealer in src/"}}, true); resp.StatusCode != 202 {
			t.Fatalf("report: %d", resp.StatusCode)
		}
	}
	_, page := admin.get("/admin")
	if strings.Count(page, "credential stealer") != 1 || !strings.Contains(page, "@rep") {
		t.Errorf("report queue wrong: %d entries", strings.Count(page, "credential stealer"))
	}

	// removing hides the package everywhere but keeps the name reserved
	if resp, _ := admin.post("/admin/packages/badlib/remove", nil, true); resp.StatusCode != 303 {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	if resp, _ := anon.get("/packages/badlib"); resp.StatusCode != 404 {
		t.Errorf("removed package page: %d", resp.StatusCode)
	}
	if resp, _ := anon.get("/packages/badlib/1.0.0/download"); resp.StatusCode != 404 {
		t.Errorf("removed package download: %d", resp.StatusCode)
	}
	if r := e.do("GET", "/index/badlib/list", "", nil, ""); r.StatusCode != 404 {
		t.Errorf("removed package index: %d", r.StatusCode)
	}
	if _, home := anon.get("/?q=suspicious"); !strings.Contains(home, "Nothing matches") {
		t.Error("removed package still searchable")
	}
	if _, dash := owner.get("/dashboard"); strings.Contains(dash, "badlib") {
		t.Error("removed package still on the owner's dashboard")
	}
	resp, body := owner.postMultipart("/dashboard/packages/new", newPkgFields("2.0.0"), upload{"file", "x.c3l", makeC3L(t, "badlib")})
	if resp.StatusCode != 409 || !strings.Contains(body, "already taken") {
		t.Errorf("re-registering a removed name: %d", resp.StatusCode)
	}
	if _, goodHome := anon.get("/"); !strings.Contains(goodHome, "Fine library") {
		t.Error("other packages must stay visible")
	}
	admin.post("/admin/reports/1/resolve", nil, true)
	if _, page := admin.get("/admin"); strings.Contains(page, "credential stealer") {
		t.Error("resolved report still listed")
	}
	// restore brings it back, searchable again
	admin.post("/admin/packages/badlib/restore", nil, true)
	if resp, _ := anon.get("/packages/badlib"); resp.StatusCode != 200 {
		t.Errorf("restored package: %d", resp.StatusCode)
	}
	if _, res := anon.get("/?q=suspicious"); !strings.Contains(res, "Suspicious library") {
		t.Error("restored package not searchable")
	}
	if resp, _ := admin.post("/admin/packages/ghost/remove", nil, true); resp.StatusCode != 404 {
		t.Errorf("remove unknown: %d", resp.StatusCode)
	}
}

func TestBanUser(t *testing.T) {
	e := newEnv(t)
	admin, victim, anon := e.browser(), e.browser(), e.browser()
	admin.register("admin@example.com", "boss", "s3cret-pass")
	e.store.SetAdminByEmail(t.Context(), "admin@example.com", true)
	victim.register("v@example.com", "victim", "s3cret-pass")
	u, _ := e.store.UserByLogin(t.Context(), "victim")
	tok, _ := e.store.CreateAPIToken(t.Context(), u.ID, "ci")

	if resp, _ := victim.get("/dashboard"); resp.StatusCode != 200 {
		t.Fatal("victim should be signed in")
	}
	if resp, _ := admin.post("/admin/users/boss/ban", nil, true); resp.StatusCode != 403 {
		t.Errorf("admins must not be bannable: %d", resp.StatusCode)
	}
	if resp, _ := admin.post("/admin/users/victim/ban", nil, true); resp.StatusCode != 303 {
		t.Fatalf("ban: %d", resp.StatusCode)
	}
	if resp, _ := victim.get("/dashboard"); resp.StatusCode != 303 {
		t.Errorf("banned user's session survived: %d", resp.StatusCode)
	}
	if r := e.do("GET", "/api/v1/me", tok, nil, ""); r.StatusCode != 401 {
		t.Errorf("banned user's token survived: %d", r.StatusCode)
	}
	if resp, body := anon.post("/login", map[string][]string{"email": {"v@example.com"}, "password": {"s3cret-pass"}}, true); resp.StatusCode != 401 || !strings.Contains(body, "Wrong email") {
		t.Errorf("banned login: %d", resp.StatusCode)
	}
	if resp, _ := anon.get("/users/victim"); resp.StatusCode != 404 {
		t.Errorf("banned profile page: %d", resp.StatusCode)
	}
	admin.post("/admin/users/victim/unban", nil, true)
	if resp, _ := anon.post("/login", map[string][]string{"email": {"v@example.com"}, "password": {"s3cret-pass"}}, true); resp.StatusCode != 303 {
		t.Errorf("login after unban: %d", resp.StatusCode)
	}
	if _, page := admin.get("/admin?q=%25_"); strings.Contains(page, "victim") && !strings.Contains(page, "@victim") {
		t.Error("search page broke")
	}
}

func TestMiddleware(t *testing.T) {
	e := newEnv(t)
	// a panicking handler becomes a 500, not a dropped connection
	h := recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 {
		t.Errorf("recoverer: %d", rec.Code)
	}
	// HSTS only when served over https
	if got := get(e, "/").Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS on plain http: %q", got)
	}
	e.server.cfg.BaseURL = "https://example.test"
	srv := httptest.NewServer(e.server.Handler())
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/")
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing for an https base URL")
	}
	if c := resp.Header.Get("Set-Cookie"); !strings.Contains(c, "Secure") || !strings.Contains(c, "HttpOnly") || !strings.Contains(c, "SameSite=Lax") {
		t.Errorf("csrf cookie flags: %q", c)
	}
}

func get(e *env, path string) *http.Response {
	resp, err := http.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}
