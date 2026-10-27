package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// versionOrder returns the keys of the "versions" object in document order.
func versionOrder(t *testing.T, body string) []string {
	t.Helper()
	var raw struct {
		Versions json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw.Versions))
	if tok, _ := dec.Token(); tok != json.Delim('{') {
		t.Fatalf("versions must be an object: %s", raw.Versions)
	}
	var keys []string
	for dec.More() {
		k, _ := dec.Token()
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// checkInfo applies the rules of c3c's load_fetch_info.
func checkInfo(t *testing.T, r apiResp) map[string]any {
	t.Helper()
	if r.StatusCode != 200 {
		t.Fatalf("info: %d %s", r.StatusCode, r.Body)
	}
	m := r.JSON(t)
	for _, k := range []string{"library", "description", "latest", "updated"} {
		if _, ok := m[k].(string); !ok {
			t.Fatalf("info %q must be a string: %s", k, r.Body)
		}
	}
	if _, ok := m["tags"].([]any); !ok {
		t.Fatalf("info tags must be an array: %s", r.Body)
	}
	vs, ok := m["versions"].(map[string]any)
	if !ok {
		t.Fatalf("info versions must be an object: %s", r.Body)
	}
	if _, ok := vs[m["latest"].(string)].(map[string]any); !ok {
		t.Fatalf("latest %v is not a key of versions", m["latest"])
	}
	for k, v := range vs {
		if v.(map[string]any)["version"] != k {
			t.Errorf("version entry %q has the wrong version field", k)
		}
	}
	return m
}

func TestFetchProtocol(t *testing.T) {
	e := newEnv(t)

	// the full index is gone: c3c searches server-side instead
	if r := e.do("GET", "/index.json", "", nil, ""); r.StatusCode != 404 {
		t.Fatalf("index.json should be gone: %d", r.StatusCode)
	}

	tok := e.token("alice", "alice@example.com")
	fields := apiFields()
	fields["git_url"] = "https://github.com/alice/vk"
	c3l := map[string][]byte{}
	for _, v := range []string{"1.0.0", "1.2.0", "1.10.0", "2.0.0-rc.1"} {
		c3l[v] = makeC3L(t, "vk", zfile{"vk.c3", v})
		if resp := e.putPackage(tok, "vk", v, c3l[v], fields); resp.StatusCode != 201 {
			t.Fatalf("publish %s: %d %s", v, resp.StatusCode, resp.Body)
		}
	}
	if resp := e.putPackage(tok, "json", "0.1.0", makeC3L(t, "json"), apiFields()); resp.StatusCode != 201 {
		t.Fatalf("publish json: %d %s", resp.StatusCode, resp.Body)
	}

	t.Run("info", func(t *testing.T) {
		r := e.do("GET", "/info/alice/vk", "", nil, "")
		m := checkInfo(t, r)
		if m["library"] != "alice/vk" || m["latest"] != "1.10.0" {
			t.Errorf("info: %s", r.Body)
		}
		if got := strings.Join(versionOrder(t, r.Body), ","); got != "2.0.0-rc.1,1.10.0,1.2.0,1.0.0" {
			t.Errorf("key order must be newest first: %s", got)
		}
		v := m["versions"].(map[string]any)["1.2.0"].(map[string]any)
		sum := sha256.Sum256(c3l["1.2.0"])
		if v["checksum"] != "sha256:"+hex.EncodeToString(sum[:]) || v["size"] != float64(len(c3l["1.2.0"])) {
			t.Errorf("checksum/size wrong: %v", v)
		}
		if v["repository"] != "https://github.com/alice/vk" || v["license"] != "MIT" || v["maintainer"] != "Alice" {
			t.Errorf("version fields: %v", v)
		}
		if strings.Contains(r.Body, "alice@example.com") {
			t.Error("the e-mail address must not be published")
		}
		if _, ok := v["published"].(string); !ok {
			t.Errorf("published: %v", v)
		}
		// the namespace is case-insensitive like logins
		if r := e.do("GET", "/info/ALICE/vk", "", nil, ""); r.StatusCode != 200 {
			t.Errorf("ALICE/vk: %d", r.StatusCode)
		}
	})

	t.Run("download", func(t *testing.T) {
		r := e.do("GET", "/download/alice/vk@1.2.0", "", nil, "")
		if r.StatusCode != 200 || r.Body != string(c3l["1.2.0"]) {
			t.Fatalf("download: %d, %d bytes", r.StatusCode, len(r.Body))
		}
		if !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("explicit versions are immutable: %q", r.Header.Get("Cache-Control"))
		}
		info := e.do("GET", "/info/alice/vk", "", nil, "").JSON(t)
		sum := sha256.Sum256([]byte(r.Body))
		if info["versions"].(map[string]any)["1.2.0"].(map[string]any)["checksum"] != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Error("downloaded bytes do not match the advertised checksum")
		}
		// aliases serve the latest and must not be cached as immutable
		for _, path := range []string{"/download/alice/vk", "/download/alice/vk@latest"} {
			r := e.do("GET", path, "", nil, "")
			if r.StatusCode != 200 || r.Body != string(c3l["1.10.0"]) {
				t.Errorf("%s: %d, wrong body", path, r.StatusCode)
			}
			if r.Header.Get("Cache-Control") != "no-cache" {
				t.Errorf("%s Cache-Control = %q", path, r.Header.Get("Cache-Control"))
			}
		}
		pk := e.do("GET", "/api/v1/packages/vk", "", nil, "").JSON(t)
		if pk["downloads"] != float64(3) {
			t.Errorf("downloads should be counted, got %v", pk["downloads"])
		}
	})

	t.Run("not found", func(t *testing.T) {
		for _, path := range []string{
			"/info/bob/vk", "/info/alice/nope", "/info/alice/vk/extra",
			"/download/bob/vk@1.0.0", "/download/alice/nope@1.0.0", "/download/alice/vk@9.9.9", "/download/alice/vk@", "/download/alice/@1.0.0",
		} {
			if r := e.do("GET", path, "", nil, ""); r.StatusCode != 404 {
				t.Errorf("%s: want 404, got %d", path, r.StatusCode)
			}
		}
	})

	t.Run("yanked", func(t *testing.T) {
		if r := e.do("PUT", "/api/v1/packages/vk/versions/1.10.0/yank", tok, nil, ""); r.StatusCode >= 300 {
			t.Fatalf("yank: %d %s", r.StatusCode, r.Body)
		}
		r := e.do("GET", "/info/alice/vk", "", nil, "")
		m := checkInfo(t, r)
		if m["latest"] != "1.2.0" || m["versions"].(map[string]any)["1.10.0"].(map[string]any)["yanked"] != true {
			t.Errorf("info after yank: %s", r.Body)
		}
		if r := e.do("GET", "/download/alice/vk@1.10.0", "", nil, ""); r.StatusCode != 200 {
			t.Errorf("yanked versions stay fetchable by exact version: %d", r.StatusCode)
		}
		if r := e.do("GET", "/download/alice/vk", "", nil, ""); r.Body != string(c3l["1.2.0"]) {
			t.Error("the bare name must skip yanked versions")
		}
		// a package whose versions are all yanked disappears from /info
		if r := e.do("PUT", "/api/v1/packages/json/versions/0.1.0/yank", tok, nil, ""); r.StatusCode >= 300 {
			t.Fatalf("yank: %d", r.StatusCode)
		}
		if r := e.do("GET", "/info/alice/json", "", nil, ""); r.StatusCode != 404 {
			t.Errorf("info of fully yanked package: %d", r.StatusCode)
		}
	})

	t.Run("etag", func(t *testing.T) {
		for _, path := range []string{"/info/alice/vk"} {
			r := e.do("GET", path, "", nil, "")
			etag := r.Header.Get("ETag")
			if etag == "" || r.Header.Get("Cache-Control") == "" {
				t.Fatalf("%s needs ETag and Cache-Control", path)
			}
			if r2 := e.do("GET", path, "", nil, "", "If-None-Match", etag); r2.StatusCode != 304 || r2.Body != "" {
				t.Errorf("%s with matching ETag: %d", path, r2.StatusCode)
			}
			if r2 := e.do("GET", path, "", nil, "", "If-None-Match", `"other"`); r2.StatusCode != 200 {
				t.Errorf("%s with stale ETag: %d", path, r2.StatusCode)
			}
		}
	})

	t.Run("removed", func(t *testing.T) {
		if err := e.store.SetPackageRemoved(t.Context(), "vk", true); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/info/alice/vk", "/download/alice/vk@1.2.0", "/download/alice/vk"} {
			if r := e.do("GET", path, "", nil, ""); r.StatusCode != 404 {
				t.Errorf("%s of a removed package: %d", path, r.StatusCode)
			}
		}
	})
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
