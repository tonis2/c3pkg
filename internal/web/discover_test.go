package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func publish(t *testing.T, b *browser, name, version, desc, tags string) {
	t.Helper()
	f := newPkgFields(version)
	f["description"], f["tags"] = desc, tags
	resp, body := b.postMultipart("/dashboard/packages/new", f, upload{"file", name + ".c3l", makeC3L(t, name)})
	if resp.StatusCode != 303 {
		t.Fatalf("publish %s: %d %s", name, resp.StatusCode, body)
	}
}

func TestHomeSearchAndSort(t *testing.T) {
	e := newEnv(t)
	alice, anon := e.browser(), e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")
	publish(t, alice, "vk", "1.0.0", "Vulkan bindings", "gpu, vulkan")
	publish(t, alice, "json", "1.0.0", "JSON parser", "parsing")
	publish(t, alice, "http", "1.0.0", "HTTP client", "net, parsing")

	_, home := anon.get("/")
	for _, want := range []string{"<h1>Packages</h1>", "Vulkan bindings", "JSON parser", "HTTP client", `href="/?tag=parsing"`, "Most downloaded", "Top rated"} {
		if !strings.Contains(home, want) {
			t.Errorf("home missing %q", want)
		}
	}
	if strings.Contains(home, "Popular tags") || strings.Count(home, `type="search"`) != 1 {
		t.Error("home should only have the navbar search")
	}
	// newest first by default
	if strings.Index(home, "HTTP client") > strings.Index(home, "Vulkan bindings") {
		t.Error("default order should be latest first")
	}

	_, res := anon.get("/?q=vulkan")
	if !strings.Contains(res, "Vulkan bindings") || strings.Contains(res, "JSON parser") || !strings.Contains(res, "1 result") {
		t.Error("search results wrong")
	}
	if _, res := anon.get("/?tag=parsing"); !strings.Contains(res, "JSON parser") || !strings.Contains(res, "HTTP client") || strings.Contains(res, "Vulkan bindings") {
		t.Error("tag filter wrong")
	}
	if _, res := anon.get("/?q=zzz"); !strings.Contains(res, "Nothing matches") {
		t.Error("empty result state missing")
	}
	if resp, _ := anon.get("/search?q=vulkan&sort=stars"); resp.StatusCode != 301 || resp.Header.Get("Location") != "/?q=vulkan&sort=stars" {
		t.Errorf("/search should redirect to /, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// hostile queries and bad params never break the page
	for _, q := range []string{`/?q=%22`, `/?q=a+OR`, `/?page=abc`, `/?page=-3`, `/?sort=drop+table`, `/?page=99999`} {
		if resp, _ := anon.get(q); resp.StatusCode != 200 {
			t.Errorf("%s: %d", q, resp.StatusCode)
		}
	}
	// XSS in the query is escaped
	if _, res := anon.get(`/?q=%3Cscript%3Ealert(1)%3C/script%3E`); strings.Contains(res, "<script>alert") {
		t.Error("query reflected unescaped")
	}

	// downloads and stars drive the other two sorts
	for i := 0; i < 3; i++ {
		anon.get("/packages/json/1.0.0/download")
	}
	anon.get("/packages/vk/1.0.0/download")
	_, res = anon.get("/?sort=downloads")
	if strings.Index(res, "JSON parser") > strings.Index(res, "Vulkan bindings") || strings.Index(res, "Vulkan bindings") > strings.Index(res, "HTTP client") {
		t.Error("most downloaded order wrong")
	}
	alice.post("/packages/vk/star", nil, true)
	_, res = alice.get("/?sort=stars")
	if strings.Index(res, "Vulkan bindings") > strings.Index(res, "JSON parser") {
		t.Error("top rated order wrong")
	}
	if !strings.Contains(res, `class="active" aria-current="true">Top rated`) {
		t.Error("active sort not highlighted")
	}
}

func TestPagination(t *testing.T) {
	e := newEnv(t)
	alice, anon := e.browser(), e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")
	for i := 0; i < 23; i++ {
		publish(t, alice, fmt.Sprintf("lib%02d", i), "1.0.0", "A library", "x")
	}
	_, p1 := anon.get("/")
	if !strings.Contains(p1, "Next →") || strings.Contains(p1, "← Newer") || strings.Count(p1, `class="card pkg-card"`) != 20 {
		t.Errorf("page 1: cards=%d", strings.Count(p1, `class="card pkg-card"`))
	}
	_, p2 := anon.get("/?page=2")
	if strings.Contains(p2, "Next →") || !strings.Contains(p2, "← Newer") || strings.Count(p2, `class="card pkg-card"`) != 3 {
		t.Errorf("page 2: cards=%d", strings.Count(p2, `class="card pkg-card"`))
	}
}

func TestStarFlow(t *testing.T) {
	e := newEnv(t)
	alice, bob, anon := e.browser(), e.browser(), e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")
	bob.register("bob@example.com", "bob", "s3cret-pass")
	publish(t, alice, "vk", "1.0.0", "Vulkan bindings", "gpu")

	if _, page := anon.get("/packages/vk"); !strings.Contains(page, "/login?next=/packages/vk") {
		t.Error("anonymous star button should lead to login")
	}
	if resp, _ := anon.post("/packages/vk/star", nil, true); resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("anonymous star: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for i := 0; i < 2; i++ { // starring twice must not double count
		if resp, _ := bob.post("/packages/vk/star", nil, true); resp.StatusCode != 303 {
			t.Fatalf("star: %d", resp.StatusCode)
		}
	}
	alice.post("/packages/vk/star", nil, true)
	_, page := bob.get("/packages/vk")
	if !strings.Contains(page, "★ Starred · 2") {
		t.Error("star state/count not shown")
	}
	bob.post("/packages/vk/unstar", nil, true)
	if _, page := bob.get("/packages/vk"); !strings.Contains(page, "☆ Star · 1") {
		t.Error("unstar not applied")
	}
	if resp, _ := bob.post("/packages/nothing/star", nil, true); resp.StatusCode != 404 {
		t.Errorf("star unknown package: %d", resp.StatusCode)
	}
}

func TestDownloadCounting(t *testing.T) {
	e := newEnv(t)
	alice, anon := e.browser(), e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")
	publish(t, alice, "vk", "1.0.0", "Vulkan bindings", "gpu")
	count := func() int64 { p, _ := e.store.PackageByName(t.Context(), "vk"); return p.Downloads }

	anon.get("/packages/vk/1.0.0/download")
	anon.get("/packages/vk/1.0.0/download")
	if count() != 2 {
		t.Fatalf("downloads = %d, want 2", count())
	}
	req, _ := http.NewRequest("HEAD", e.srv.URL+"/packages/vk/1.0.0/download", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("HEAD: %v", err)
	}
	req, _ = http.NewRequest("GET", e.srv.URL+"/packages/vk/1.0.0/download", nil)
	req.Header.Set("Range", "bytes=100-")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 206 {
		t.Fatalf("range: %v", err)
	}
	if count() != 2 {
		t.Errorf("HEAD and resumed ranges must not count: %d", count())
	}
	anon.get("/packages/vk/9.9.9/download")
	if count() != 2 {
		t.Errorf("failed download counted: %d", count())
	}
	_, versions := anon.get("/packages/vk/versions")
	if !strings.Contains(versions, "<th>Downloads</th>") || !strings.Contains(versions, "<td>2</td>") {
		t.Error("per-version download count missing")
	}
	if _, page := anon.get("/packages/vk"); !strings.Contains(page, `class="spark"`) || !strings.Contains(page, "2 downloads in the last 30 days") {
		t.Error("download chart missing")
	}
}
