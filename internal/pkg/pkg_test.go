package pkg_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/tonis/c3pkg/internal/pkg"
)

var lim = pkg.Limits{MaxCompressed: 1 << 20, MaxUnzipped: 1 << 20, MaxFiles: 100}

const goodManifest = `{"provides":"mylib","sources":["src/**"],"targets":{"linux-x64":{},"windows-x64":{"linked-libraries":[]}}}`

type file struct{ name, body string }

func makeZip(t *testing.T, files ...file) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodZip(t *testing.T) []byte {
	return makeZip(t, file{"manifest.json", goodManifest}, file{"src/", ""}, file{"src/mylib.c3", "module mylib;"}, file{"README.md", "# hi"})
}

func TestInspectGood(t *testing.T) {
	info, err := pkg.Inspect(goodZip(t), lim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "mylib" || strings.Join(info.Targets, ",") != "linux-x64,windows-x64" || info.FileCount != 3 {
		t.Errorf("info = %+v", info)
	}
	if len(info.SHA256File) != 64 || len(info.SHA256Tree) != 64 {
		t.Errorf("hashes: %+v", info)
	}
	// the tree hash depends on the files, not on zip layout, so re-zipping in another order matches
	again, _ := pkg.Inspect(makeZip(t, file{"README.md", "# hi"}, file{"src/mylib.c3", "module mylib;"}, file{"manifest.json", goodManifest}), lim)
	if again.SHA256Tree != info.SHA256Tree {
		t.Error("tree hash should not depend on entry order or directory entries")
	}
	changed, _ := pkg.Inspect(makeZip(t, file{"manifest.json", goodManifest}, file{"src/mylib.c3", "module mylib; // x"}, file{"README.md", "# hi"}), lim)
	if changed.SHA256Tree == info.SHA256Tree {
		t.Error("tree hash must change with content")
	}
}

func TestInspectRejects(t *testing.T) {
	m := file{"manifest.json", goodManifest}
	src := file{"src/mylib.c3", "module mylib;"}
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"not a zip", []byte("hello"), "not a valid"},
		{"no manifest", makeZip(t, src), "manifest.json is missing"},
		{"manifest in subdir", makeZip(t, file{"x/manifest.json", goodManifest}, src), "manifest.json is missing"},
		{"bad json", makeZip(t, file{"manifest.json", "{"}, src), "not valid JSON"},
		{"missing provides", makeZip(t, file{"manifest.json", `{"sources":["src/**"]}`}, src), "provides"},
		{"uppercase provides", makeZip(t, file{"manifest.json", `{"provides":"MyLib","sources":["src/**"]}`}, src), "provides"},
		{"reserved provides", makeZip(t, file{"manifest.json", `{"provides":"std","sources":["src/**"]}`}, src), "reserved"},
		{"sources match nothing", makeZip(t, file{"manifest.json", `{"provides":"mylib","sources":["lib/**"]}`}, src), "match a .c3 file"},
		{"unsafe sources", makeZip(t, file{"manifest.json", `{"provides":"mylib","sources":["../**"]}`}, src), "unsafe pattern"},
		{"bad target", makeZip(t, file{"manifest.json", `{"provides":"mylib","sources":["src/**"],"targets":{"Bad Name":{}}}`}, src), "target name"},
		{"path traversal", makeZip(t, m, src, file{"../evil.c3", "x"}), "traversal"},
		{"absolute path", makeZip(t, m, src, file{"/etc/passwd", "x"}), "absolute"},
		{"backslash path", makeZip(t, m, src, file{`a\b.c3`, "x"}), "backslash"},
		{"duplicate entry", makeZip(t, m, src, src), "Duplicate"},
	}
	for _, c := range cases {
		_, err := pkg.Inspect(c.data, lim)
		var ve *pkg.ValidationError
		if !errors.As(err, &ve) || !strings.Contains(ve.Msg, c.want) {
			t.Errorf("%s: err = %v, want message containing %q", c.name, err, c.want)
		}
	}
}

func TestInspectLimits(t *testing.T) {
	big := strings.Repeat("a", 3000)
	data := makeZip(t, file{"manifest.json", goodManifest}, file{"src/a.c3", big}, file{"src/b.c3", big})
	if _, err := pkg.Inspect(data, pkg.Limits{MaxCompressed: 1 << 20, MaxUnzipped: 5000, MaxFiles: 100}); err == nil || !strings.Contains(err.Error(), "expands to more than") {
		t.Errorf("unzipped limit: %v", err)
	}
	if _, err := pkg.Inspect(data, pkg.Limits{MaxCompressed: 10, MaxUnzipped: 1 << 20, MaxFiles: 100}); err == nil || !strings.Contains(err.Error(), "upload limit") {
		t.Errorf("compressed limit: %v", err)
	}
	if _, err := pkg.Inspect(data, pkg.Limits{MaxCompressed: 1 << 20, MaxUnzipped: 1 << 20, MaxFiles: 2}); err == nil || !strings.Contains(err.Error(), "more than 2 entries") {
		t.Errorf("file count limit: %v", err)
	}
}

// A zip bomb: a tiny archive that inflates far past the budget must be stopped while reading.
func TestInspectZipBomb(t *testing.T) {
	bomb := makeZip(t, file{"manifest.json", goodManifest}, file{"src/a.c3", strings.Repeat("\x00", 50<<20)})
	if len(bomb) > 1<<20 {
		t.Skipf("bomb did not compress (%d bytes)", len(bomb))
	}
	_, err := pkg.Inspect(bomb, pkg.Limits{MaxCompressed: 1 << 20, MaxUnzipped: 10 << 20, MaxFiles: 100})
	if err == nil || !strings.Contains(err.Error(), "expands to more than") {
		t.Errorf("zip bomb accepted: %v", err)
	}
}

func TestSemver(t *testing.T) {
	valid := []string{"0.0.1", "1.2.3", "10.20.30", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-0.3.7", "1.0.0-x.7.z.92"}
	for _, v := range valid {
		if _, err := pkg.ParseSemver(v); err != nil {
			t.Errorf("%s rejected: %v", v, err)
		}
	}
	invalid := []string{"", "1", "1.2", "01.2.3", "1.2.3.4", "v1.2.3", "1.2.3+build", "1.0.0-", "1.0.0-01", "1.2.3 ", "99999999999.0.0"}
	for _, v := range invalid {
		if _, err := pkg.ParseSemver(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
	// order from the semver spec, lowest to highest
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"}
	for i := range order {
		for j := range order {
			a, _ := pkg.ParseSemver(order[i])
			b, _ := pkg.ParseSemver(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestNamesAndFields(t *testing.T) {
	for name, ok := range map[string]bool{"vk": true, "my-lib_2": true, "a": false, "1abc": false, "Abc": false, "a b": false, "std": false, strings.Repeat("a", 65): false} {
		if got := pkg.ValidateName(name) == ""; got != ok {
			t.Errorf("ValidateName(%q) ok=%v, want %v", name, got, ok)
		}
	}
	if pkg.NameKey("My-Lib") != pkg.NameKey("my_lib") {
		t.Error("look-alike names must share a key")
	}
	tags, problem := pkg.NormalizeTags("GPU, vulkan  gpu\nmath")
	if problem != "" || strings.Join(tags, ",") != "gpu,vulkan,math" {
		t.Errorf("tags = %v %q", tags, problem)
	}
	if _, p := pkg.NormalizeTags("a,b,c,d,e,f"); p == "" {
		t.Error("too many tags accepted")
	}
	if _, p := pkg.NormalizeTags("bad_tag"); p == "" {
		t.Error("invalid tag accepted")
	}
	for u, ok := range map[string]bool{"": true, "https://github.com/a/b": true, "http://x.org": true, "javascript:alert(1)": false, "ftp://x.org": false, "https://": false, "https://u:p@x.org": false} {
		if got := pkg.ValidateURL(u) == ""; got != ok {
			t.Errorf("ValidateURL(%q) ok=%v, want %v", u, got, ok)
		}
	}
	for v, ok := range map[string]bool{"0.7": true, "0.7.5": true, "": false, "v0.7": false, "0.7.5-rc": false, "abc": false} {
		if got := pkg.ValidateC3Version(v) == ""; got != ok {
			t.Errorf("ValidateC3Version(%q) ok=%v, want %v", v, got, ok)
		}
	}
}

func TestReadme(t *testing.T) {
	html := string(pkg.RenderReadme("# Title\n\n```c3\nfn void main() {}\n```\n\n<script>alert(1)</script>\n\n[x](javascript:alert(1)) [ok](https://example.com)\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"))
	for _, want := range []string{"<h1", "language-c3", "<table", `href="https://example.com"`, `rel="nofollow`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in %s", want, html)
		}
	}
	for _, bad := range []string{"<script", "javascript:", "alert(1)</"} {
		if strings.Contains(html, bad) {
			t.Errorf("unsafe content %q survived: %s", bad, html)
		}
	}
	if pkg.ValidateReadme([]byte("ok")) != "" || pkg.ValidateReadme([]byte{0xff, 0xfe}) == "" || pkg.ValidateReadme(make([]byte, pkg.MaxReadmeBytes+1)) == "" {
		t.Error("readme validation wrong")
	}
}
