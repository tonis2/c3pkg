package web

import (
	"strings"
	"testing"
)

func TestInstallLines(t *testing.T) {
	if got := installLine("https://www.c3pkg.eu", "tonis2", "image", "0.0.6"); got != `"https://www.c3pkg.eu:tonis2/image@0.0.6"` {
		t.Errorf("install line: %s", got)
	}
	if got := lockedLine("https://www.c3pkg.eu", "tonis2", "image", "0.0.6", "abc"); got != `"https://www.c3pkg.eu:tonis2/image@0.0.6:abc"` {
		t.Errorf("locked line: %s", got)
	}
	// it may wrap after the URL and before the checksum, but never inside "https://"
	got := string(lineBlock(`"https://www.c3pkg.eu:tonis2/image@0.0.6:abc"`))
	if want := `<pre><code>&#34;https://www.c3pkg.eu:<wbr>tonis2/image@0.0.6:<wbr>abc&#34;</code></pre>`; got != want {
		t.Errorf("block: %s", got)
	}
}

// publishDeps publishes a library whose manifest names the given dependencies.
func publishDeps(e *env, tok, name, version, deps string) apiResp {
	manifest := `{"provides":"` + name + `","version":"` + versionPlaceholder + `","sources":["src/**"],"dependencies":[` + deps + `],"targets":{"linux-x64":{}}}`
	return e.putPackage(tok, name, version, makeC3L(e.t, name, zfile{"manifest.json", manifest}), apiFields())
}

func TestPackagePageInstall(t *testing.T) {
	e := newEnv(t)
	tok := e.token("alice", "alice@example.com")
	if r := publishDeps(e, tok, "vk", "1.0.0", ""); r.StatusCode != 201 {
		t.Fatalf("publish vk: %d %s", r.StatusCode, r.Body)
	}
	if r := publishDeps(e, tok, "cui", "1.0.0", `"vk","image"`); r.StatusCode != 201 {
		t.Fatalf("publish cui: %d %s", r.StatusCode, r.Body)
	}
	index := e.do("GET", "/index/cui/1.0.0.json", "", nil, "").JSON(t)
	if got := toStrings(index["requires"]); strings.Join(got, ",") != "vk,image" {
		t.Errorf("index requires: %v", got)
	}

	_, page := e.browser().get("/packages/cui")
	for _, want := range []string{
		`<span class="pkg-owner">alice/</span>cui`,
		`:<wbr>alice/cui@1.0.0&#34;`,
		"cui", `Lock to a checksum`,
		`:<wbr>` + index["sha256_file"].(string) + `&#34;`,
		`<a href="/?q=vk">vk</a>`, `<a href="/?q=image">image</a>`,
		`title="` + index["sha256_file"].(string) + `"`,
		`Download 1.0.0 (.c3l)`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("package page lacks %q", want)
		}
	}
	for _, bad := range []string{"--fetch-url", index["sha256_tree"].(string), "btn-primary wide"} {
		if strings.Contains(page, bad) {
			t.Errorf("package page has %q", bad)
		}
	}
	if !strings.Contains(page, "project") || !strings.Contains(strings.ReplaceAll(page, "</span>", ""), "fetch") {
		t.Error("package page lacks the fetch command")
	}

	if _, vk := e.browser().get("/packages/vk"); strings.Contains(vk, "deps-card") {
		t.Error("a package without dependencies shows the Dependencies box")
	}
}

func TestVersionsTabCopyLines(t *testing.T) {
	e := newEnv(t)
	tok := e.token("alice", "alice@example.com")
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if r := publishDeps(e, tok, "cui", v, ""); r.StatusCode != 201 {
			t.Fatalf("publish %s: %d %s", v, r.StatusCode, r.Body)
		}
	}
	if r := e.do("PUT", "/api/v1/packages/cui/versions/1.0.0/yank", tok, nil, ""); r.StatusCode != 204 {
		t.Fatalf("yank: %d %s", r.StatusCode, r.Body)
	}
	_, page := e.browser().get("/packages/cui/versions")
	if n := strings.Count(page, `label="Copy line"`); n != 1 {
		t.Errorf("copy buttons = %d, want 1: a yanked version gets none", n)
	}
	if strings.Contains(page, "alice/cui@1.0.0") {
		t.Error("the yanked version is offered for copying")
	}
	if !strings.Contains(page, "alice/cui@1.1.0") || !strings.Contains(page, "Updating to a new version") {
		t.Error("versions tab lacks the line or the update note")
	}
}

func TestPublishRejectsOtherDependencyForms(t *testing.T) {
	e := newEnv(t)
	tok := e.token("alice", "alice@example.com")
	for _, dep := range []string{`"tonis2/vk"`, `"https://www.c3pkg.eu:tonis2/vk@1.0.0"`} {
		r := publishDeps(e, tok, "cui", "1.0.0", dep)
		if r.StatusCode != 422 || !strings.Contains(r.Body, "Use the plain library name") {
			t.Errorf("%s: %d %s", dep, r.StatusCode, r.Body)
		}
	}
}

func TestInstallGuideAndDocs(t *testing.T) {
	e := newEnv(t)
	resp, guide := e.browser().get("/docs/install")
	if resp.StatusCode != 200 {
		t.Fatalf("guide: %d", resp.StatusCode)
	}
	for _, want := range []string{"Install guide", "You need c3c 0.8.6 or newer", "Troubleshooting", "Lock to a checksum"} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide lacks %q", want)
		}
	}
	if strings.Contains(guide, "fetch-list") {
		t.Error("the guide mentions fetch-list, which c3pkg doesn't support")
	}

	_, home := e.browser().get("/")
	if !strings.Contains(home, `<a href="/docs/install">Install</a>`) {
		t.Error("the nav lacks the Install link")
	}

	_, api := e.browser().get("/docs/api")
	if !strings.Contains(api, `<code class="endpoint"><span class="method">GET</span> /api/v1/packages</code>`) {
		t.Error("docs lack endpoints in code format")
	}
	if strings.Contains(api, "--fetch-url") {
		t.Error("docs still teach c3c --fetch-url fetch")
	}
	for _, want := range []string{"/info/{owner}/{name}", "/download/{owner}/{name}@{version}", `<span class="method">GET</span> /search</code>`} {
		if !strings.Contains(api, want) {
			t.Errorf("docs lack %s", want)
		}
	}
}
