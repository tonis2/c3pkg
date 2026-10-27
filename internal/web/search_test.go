package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// checkSearch applies the same rules as c3c's search answer: every entry must have the required
// string fields, or the whole response is rejected.
func checkSearch(t *testing.T, r apiResp) (libs []map[string]any, total int) {
	t.Helper()
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("search: %d %s %s", r.StatusCode, r.Header.Get("Content-Type"), r.Body)
	}
	var top struct {
		Libraries *[]map[string]any `json:"libraries"`
		Total     *int              `json:"total"`
	}
	if err := json.Unmarshal([]byte(r.Body), &top); err != nil || top.Libraries == nil || top.Total == nil {
		t.Fatalf("search must be an object with libraries and total: %v %s", err, r.Body)
	}
	for _, e := range *top.Libraries {
		for _, k := range []string{"library", "description", "latest", "updated"} {
			if _, ok := e[k].(string); !ok {
				t.Errorf("search entry %v: %q must be a string", e, k)
			}
		}
		if id, _ := e["library"].(string); !strings.Contains(id, "/") {
			t.Errorf("library id %q must be <ns>/<name>", id)
		}
		for _, k := range []string{"tags", "versions"} {
			arr, ok := e[k].([]any)
			if !ok {
				t.Errorf("search entry %v: %q must be an array", e, k)
			}
			for _, x := range arr {
				if _, ok := x.(string); !ok {
					t.Errorf("%q holds a non-string", k)
				}
			}
		}
		vs := toStrings(e["versions"])
		if len(vs) == 0 || !contains(vs, e["latest"].(string)) {
			t.Errorf("latest %v must be one of versions %v", e["latest"], vs)
		}
	}
	return *top.Libraries, *top.Total
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func ids(libs []map[string]any) string {
	var out []string
	for _, l := range libs {
		out = append(out, l["library"].(string))
	}
	return strings.Join(out, ",")
}

func TestFetchSearch(t *testing.T) {
	e := newEnv(t)
	search := func(query string) ([]map[string]any, int) {
		t.Helper()
		return checkSearch(t, e.do("GET", "/search"+query, "", nil, ""))
	}

	// an empty registry answers an empty list, not null
	if r := e.do("GET", "/search", "", nil, ""); strings.TrimSpace(r.Body) != `{"libraries":[],"total":0}` {
		t.Fatalf("empty registry: %d %s", r.StatusCode, r.Body)
	}

	tok := e.token("alice", "alice@example.com")
	publish := func(name, desc, tags string, versions ...string) {
		t.Helper()
		f := apiFields()
		f["description"], f["tags"] = desc, tags
		for _, v := range versions {
			if r := e.putPackage(tok, name, v, makeC3L(t, name), f); r.StatusCode != 201 {
				t.Fatalf("publish %s@%s: %d %s", name, v, r.StatusCode, r.Body)
			}
		}
	}
	yank := func(name, version string) {
		t.Helper()
		if r := e.do("PUT", "/api/v1/packages/"+name+"/versions/"+version+"/yank", tok, nil, ""); r.StatusCode >= 300 {
			t.Fatalf("yank: %d %s", r.StatusCode, r.Body)
		}
	}
	publish("json", "Fast JSON parser", "parsing", "0.1.0")
	publish("http", "HTTP client with a parser", "parsing,net", "0.2.0")
	publish("vk", "Vulkan bindings", "gpu,graphics", "1.0.0", "1.2.0", "1.10.0", "2.0.0-rc.1")
	publish("ghost", "Everything yanked parser", "parsing", "1.0.0")
	publish("gone", "Removed parser", "parsing", "1.0.0")
	yank("ghost", "1.0.0")
	if err := e.store.SetPackageRemoved(t.Context(), "gone", true); err != nil {
		t.Fatal(err)
	}

	t.Run("by name", func(t *testing.T) {
		libs, total := search("?q=vk")
		if ids(libs) != "alice/vk" || total != 1 {
			t.Fatalf("got %s (%d)", ids(libs), total)
		}
		vk := libs[0]
		if vk["latest"] != "1.10.0" || vk["description"] != "Vulkan bindings" {
			t.Errorf("vk entry: %v", vk)
		}
		if got := strings.Join(toStrings(vk["versions"]), ","); got != "2.0.0-rc.1,1.10.0,1.2.0,1.0.0" {
			t.Errorf("versions should be newest first: %s", got)
		}
		if got := toStrings(vk["tags"]); strings.Join(got, ",") != "gpu,graphics" && strings.Join(got, ",") != "graphics,gpu" {
			t.Errorf("tags: %v", got)
		}
	})

	t.Run("by description and tags text", func(t *testing.T) {
		if libs, total := search("?q=vulkan"); ids(libs) != "alice/vk" || total != 1 {
			t.Errorf("description word: %s (%d)", ids(libs), total)
		}
		if libs, _ := search("?q=graphics"); ids(libs) != "alice/vk" {
			t.Errorf("tag word: %s", ids(libs))
		}
	})

	t.Run("exact name first, then latest updated", func(t *testing.T) {
		// "parser" is in the description of json and http; ghost and gone are filtered out
		libs, total := search("?q=parser")
		if ids(libs) != "alice/http,alice/json" || total != 2 {
			t.Errorf("got %s (%d)", ids(libs), total)
		}
		if libs, _ := search("?q=json+parser"); ids(libs) != "alice/json" {
			t.Errorf("all words must match: %s", ids(libs))
		}
	})

	t.Run("tag is exact", func(t *testing.T) {
		libs, total := search("?tag=parsing")
		if ids(libs) != "alice/http,alice/json" || total != 2 {
			t.Errorf("got %s (%d)", ids(libs), total)
		}
		if libs, total := search("?tag=pars"); len(libs) != 0 || total != 0 {
			t.Errorf("a tag prefix must not match: %s", ids(libs))
		}
		if libs, _ := search("?tag=PARSING&q=http"); ids(libs) != "alice/http" {
			t.Errorf("q and tag combine: %s", ids(libs))
		}
		if libs, _ := search("?tag=gpu&q=json"); len(libs) != 0 {
			t.Errorf("q and tag must both match: %s", ids(libs))
		}
	})

	t.Run("empty query lists the latest updated", func(t *testing.T) {
		libs, total := search("")
		if ids(libs) != "alice/vk,alice/http,alice/json" || total != 3 {
			t.Errorf("got %s (%d)", ids(libs), total)
		}
		if r := e.do("GET", "/search?q=&tag=", "", nil, ""); r.Body != e.do("GET", "/search", "", nil, "").Body {
			t.Error("empty parameters should equal no parameters")
		}
	})

	t.Run("no match", func(t *testing.T) {
		for _, q := range []string{"?q=zzzz", "?q=%22", "?q=a+OR", "?q=%21%21"} {
			r := e.do("GET", "/search"+q, "", nil, "")
			if r.StatusCode != 200 || strings.TrimSpace(r.Body) != `{"libraries":[],"total":0}` {
				t.Errorf("%s: %d %s", q, r.StatusCode, r.Body)
			}
		}
	})

	t.Run("yanked and removed are excluded", func(t *testing.T) {
		for _, q := range []string{"?q=ghost", "?q=gone", "?tag=parsing&q=yanked"} {
			if libs, total := search(q); len(libs) != 0 || total != 0 {
				t.Errorf("%s: %s (%d)", q, ids(libs), total)
			}
		}
		// yanking the newest version moves latest down but keeps the package
		yank("vk", "1.10.0")
		libs, _ := search("?q=vk")
		if libs[0]["latest"] != "1.2.0" || contains(toStrings(libs[0]["versions"]), "1.10.0") {
			t.Errorf("yanked version must not be latest or listed: %v", libs[0])
		}
	})

	t.Run("page size", func(t *testing.T) {
		for i := 0; i < searchLimit+5; i++ {
			publish(fmt.Sprintf("bulk-%02d", i), "Bulky thing", "bulk", "1.0.0")
		}
		libs, total := search("?q=bulky")
		if len(libs) != searchLimit || total != searchLimit+5 {
			t.Errorf("want %d of %d, got %d of %d", searchLimit, searchLimit+5, len(libs), total)
		}
		// the newest come first
		if libs[0]["library"] != fmt.Sprintf("alice/bulk-%02d", searchLimit+4) {
			t.Errorf("first: %v", libs[0]["library"])
		}
		libs, total = search("")
		if len(libs) != searchLimit || total != searchLimit+5+3 {
			t.Errorf("empty query: %d of %d", len(libs), total)
		}
	})

	t.Run("route", func(t *testing.T) {
		if r := e.do("GET", "/index.json", "", nil, ""); r.StatusCode != 404 {
			t.Errorf("index.json: %d", r.StatusCode)
		}
		if r := e.do("POST", "/search", "", nil, ""); r.StatusCode == 200 {
			t.Errorf("POST /search: %d", r.StatusCode)
		}
		if r := e.do("GET", "/search", "", nil, ""); r.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q", r.Header.Get("Cache-Control"))
		}
	})
}
