package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zfile struct{ name, body string }

func makeC3L(t *testing.T, provides string, extra ...zfile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := append([]zfile{
		{"manifest.json", `{"provides":"` + provides + `","sources":["src/**"],"targets":{"linux-x64":{}}}`},
		{"src/" + provides + ".c3", "module " + provides + ";"},
	}, extra...)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	zw.Close()
	return buf.Bytes()
}

type upload struct {
	field, filename string
	data            []byte
}

func (b *browser) postMultipart(path string, fields map[string]string, files ...upload) (*http.Response, string) {
	b.e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("csrf", b.csrf())
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	for _, f := range files {
		w, _ := mw.CreateFormFile(f.field, f.filename)
		w.Write(f.data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", b.e.srv.URL+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	return resp, readBody(resp)
}

func newPkgFields(version string) map[string]string {
	return map[string]string{"version": version, "c3_version": "0.7.5", "description": "Handy library", "tags": "gpu, math",
		"license": "MIT", "docs_url": "https://docs.example.com/mylib", "git_url": "https://github.com/alice/mylib"}
}

func (e *env) countStored() int {
	n := 0
	filepath.WalkDir(e.files, func(p string, d os.DirEntry, _ error) error {
		if strings.HasSuffix(p, ".c3l") {
			n++
		}
		return nil
	})
	return n
}

func TestPublishAndBrowse(t *testing.T) {
	e := newEnv(t)
	alice := e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")

	c3l := makeC3L(t, "mylib")
	resp, body := alice.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"),
		upload{"file", "mylib.c3l", c3l},
		upload{"readme", "README.md", []byte("# Mylib\n\nUse `mylib`.\n\n<script>alert(1)</script>")})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/packages/mylib" {
		t.Fatalf("publish: %d %q %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}

	// the package page is public
	anon := e.browser()
	resp, page := anon.get("/packages/mylib")
	if resp.StatusCode != 200 {
		t.Fatalf("package page: %d", resp.StatusCode)
	}
	for _, want := range []string{"Handy library", "1.0.0", "0.7.5", "linux-x64", "MIT", "<h1", "Use <code>mylib</code>",
		"https://docs.example.com/mylib", "https://github.com/alice/mylib", "@alice", ">gpu<", "/packages/mylib/1.0.0/download"} {
		if !strings.Contains(page, want) {
			t.Errorf("package page missing %q", want)
		}
	}
	if strings.Contains(page, "<script>alert") {
		t.Error("raw script from README survived")
	}
	if strings.Contains(page, "Publish update") {
		t.Error("anonymous visitors must not see owner actions")
	}
	if _, owner := alice.get("/packages/mylib"); !strings.Contains(owner, "Publish update") {
		t.Error("owner should see Publish update")
	}

	// download returns the exact bytes
	resp, dl := anon.get("/packages/mylib/1.0.0/download")
	sum := sha256.Sum256(c3l)
	if resp.StatusCode != 200 || dl != string(c3l) || resp.Header.Get("ETag") != `"`+hex.EncodeToString(sum[:])+`"` ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "mylib-1.0.0.c3l") {
		t.Errorf("download: %d disposition=%q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	if resp, _ := anon.get("/packages/mylib/9.9.9/download"); resp.StatusCode != 404 {
		t.Errorf("missing version: %d", resp.StatusCode)
	}

	// dashboard and public profile list it
	if _, dash := alice.get("/dashboard"); !strings.Contains(dash, "mylib") || !strings.Contains(dash, "Publish update") {
		t.Error("dashboard does not list the package")
	}
	if resp, user := anon.get("/users/alice"); resp.StatusCode != 200 || !strings.Contains(user, "mylib") || strings.Contains(user, "alice@example.com") {
		t.Errorf("user page: %d (must not leak email)", resp.StatusCode)
	}
	if resp, _ := anon.get("/users/nobody"); resp.StatusCode != 404 {
		t.Errorf("unknown user: %d", resp.StatusCode)
	}
	if resp, _ := anon.get("/packages/mylib/dependencies"); resp.StatusCode != 404 {
		t.Errorf("the dependencies tab was removed: %d", resp.StatusCode)
	}
	if _, page := anon.get("/packages/mylib"); strings.Contains(page, "Dependencies") {
		t.Error("package page still mentions Dependencies")
	}
	if resp, _ := anon.get("/packages/nothing"); resp.StatusCode != 404 {
		t.Errorf("unknown package: %d", resp.StatusCode)
	}
}

func TestPublishUpdateAndImmutability(t *testing.T) {
	e := newEnv(t)
	alice := e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")
	alice.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"), upload{"file", "mylib.c3l", makeC3L(t, "mylib")})

	v2 := makeC3L(t, "mylib", zfile{"src/extra.c3", "module mylib;"})
	resp, _ := alice.postMultipart("/dashboard/packages/mylib/publish", map[string]string{"version": "1.1.0", "c3_version": "0.7.5"}, upload{"file", "mylib.c3l", v2})
	if resp.StatusCode != 303 {
		t.Fatalf("publish update: %d", resp.StatusCode)
	}
	_, page := alice.get("/packages/mylib")
	if !strings.Contains(page, "Download 1.1.0") {
		t.Error("latest version not updated")
	}
	_, versions := alice.get("/packages/mylib/versions")
	if !strings.Contains(versions, "1.0.0") || !strings.Contains(versions, "1.1.0") {
		t.Error("versions tab incomplete")
	}

	// the same version can never be published twice, even with different content
	stored := e.countStored()
	resp, body := alice.postMultipart("/dashboard/packages/mylib/publish", map[string]string{"version": "1.1.0", "c3_version": "0.7.5"},
		upload{"file", "mylib.c3l", makeC3L(t, "mylib", zfile{"src/other.c3", "module mylib;"})})
	if resp.StatusCode != 409 || !strings.Contains(body, "already published") {
		t.Errorf("duplicate version: %d", resp.StatusCode)
	}
	if e.countStored() != stored {
		t.Error("rejected upload left a file behind in storage")
	}

	// validation problems come back as 422 with a message
	cases := []struct {
		name   string
		fields map[string]string
		file   []byte
		want   string
	}{
		{"bad version", map[string]string{"version": "v2", "c3_version": "0.7.5"}, makeC3L(t, "mylib"), "not a valid version"},
		{"bad c3 version", map[string]string{"version": "2.0.0", "c3_version": "x"}, makeC3L(t, "mylib"), "C3 version"},
		{"wrong package", map[string]string{"version": "2.0.0", "c3_version": "0.7.5"}, makeC3L(t, "otherlib"), `is &#34;otherlib&#34;`},
		{"not a zip", map[string]string{"version": "2.0.0", "c3_version": "0.7.5"}, []byte("plain text"), "not a valid"},
	}
	for _, c := range cases {
		resp, body := alice.postMultipart("/dashboard/packages/mylib/publish", c.fields, upload{"file", "mylib.c3l", c.file})
		if resp.StatusCode != 422 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d, has message=%v", c.name, resp.StatusCode, strings.Contains(body, c.want))
		}
	}
	if resp, body := alice.postMultipart("/dashboard/packages/mylib/publish", map[string]string{"version": "2.0.0", "c3_version": "0.7.5"}, upload{"file", "mylib.zip", makeC3L(t, "mylib")}); resp.StatusCode != 422 || !strings.Contains(body, ".c3l extension") {
		t.Errorf("wrong extension: %d", resp.StatusCode)
	}
	if resp, _ := alice.postMultipart("/dashboard/packages/mylib/publish", map[string]string{"version": "2.0.0", "c3_version": "0.7.5"}); resp.StatusCode != 422 {
		t.Errorf("missing file: %d", resp.StatusCode)
	}
}

func TestOwnershipAndNames(t *testing.T) {
	e := newEnv(t)
	alice, bob, anon := e.browser(), e.browser(), e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")
	bob.register("bob@example.com", "bob", "s3cret-pass")
	alice.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"), upload{"file", "mylib.c3l", makeC3L(t, "mylib")})
	stored := e.countStored()

	// bob cannot publish to, edit, or yank alice's package
	if resp, _ := bob.postMultipart("/dashboard/packages/mylib/publish", map[string]string{"version": "1.0.1", "c3_version": "0.7.5"}, upload{"file", "mylib.c3l", makeC3L(t, "mylib")}); resp.StatusCode != 403 {
		t.Errorf("non-owner publish: %d", resp.StatusCode)
	}
	if resp, _ := bob.get("/dashboard/packages/mylib/edit"); resp.StatusCode != 403 {
		t.Errorf("non-owner edit form: %d", resp.StatusCode)
	}
	if resp, _ := bob.postMultipart("/dashboard/packages/mylib/edit", newPkgFields("")); resp.StatusCode != 403 {
		t.Errorf("non-owner edit: %d", resp.StatusCode)
	}
	if resp, _ := bob.post("/dashboard/packages/mylib/versions/1.0.0/yank", nil, true); resp.StatusCode != 403 {
		t.Errorf("non-owner yank: %d", resp.StatusCode)
	}
	// creating a package under a name alice owns is refused, as is a look-alike
	for _, name := range []string{"mylib"} {
		resp, body := bob.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"), upload{"file", "x.c3l", makeC3L(t, name)})
		if resp.StatusCode != 409 || !strings.Contains(body, "already taken") {
			t.Errorf("taken name %s: %d", name, resp.StatusCode)
		}
	}
	alice.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"), upload{"file", "x.c3l", makeC3L(t, "my-lib")})
	if resp, _ := bob.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"), upload{"file", "x.c3l", makeC3L(t, "my_lib")}); resp.StatusCode != 409 {
		t.Errorf("look-alike name: %d", resp.StatusCode)
	}
	if e.countStored() != stored+1 { // only alice's my-lib upload added a file
		t.Errorf("stored files = %d, want %d: rejected uploads must be cleaned up", e.countStored(), stored+1)
	}
	// anonymous users are sent to log in
	if resp, _ := anon.get("/dashboard/packages/new"); resp.StatusCode != 303 {
		t.Errorf("anonymous new package form: %d", resp.StatusCode)
	}
}

func TestMissingFieldsOnNewPackage(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.register("alice@example.com", "alice", "s3cret-pass")
	bad := newPkgFields("1.0.0")
	bad["description"], bad["docs_url"], bad["tags"] = "", "javascript:alert(1)", "bad_tag"
	resp, body := b.postMultipart("/dashboard/packages/new", bad, upload{"file", "mylib.c3l", makeC3L(t, "mylib")})
	if resp.StatusCode != 422 || !strings.Contains(body, "Describe the package") || !strings.Contains(body, "http(s) URL") || !strings.Contains(body, "Tags use") {
		t.Errorf("validation: %d", resp.StatusCode)
	}
	if e.countStored() != 0 {
		t.Error("nothing should be stored when validation fails")
	}
	if _, err := e.store.PackageByName(t.Context(), "mylib"); err == nil {
		t.Error("package created despite validation errors")
	}
}

func TestUploadTooLarge(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.register("alice@example.com", "alice", "s3cret-pass")
	resp, _ := b.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"), upload{"file", "big.c3l", bytes.Repeat([]byte("x"), 5<<20)})
	if resp.StatusCode != 413 {
		t.Errorf("oversized body: %d", resp.StatusCode)
	}
	// a file just over the limit but within the body allowance is rejected by validation
	resp, body := b.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"), upload{"file", "big.c3l", bytes.Repeat([]byte("x"), 2<<20+10)})
	if resp.StatusCode != 422 || !strings.Contains(body, "upload limit") {
		t.Errorf("over-limit file: %d", resp.StatusCode)
	}
}

func TestYankEditAndReadme(t *testing.T) {
	e := newEnv(t)
	alice := e.browser()
	alice.register("alice@example.com", "alice", "s3cret-pass")
	alice.postMultipart("/dashboard/packages/new", newPkgFields("1.0.0"), upload{"file", "a.c3l", makeC3L(t, "mylib")})
	alice.postMultipart("/dashboard/packages/mylib/publish", map[string]string{"version": "1.1.0", "c3_version": "0.7.5"},
		upload{"file", "a.c3l", makeC3L(t, "mylib", zfile{"src/x.c3", "module mylib;"})})

	if resp, _ := alice.post("/dashboard/packages/mylib/versions/1.1.0/yank", nil, true); resp.StatusCode != 303 {
		t.Fatalf("yank: %d", resp.StatusCode)
	}
	_, page := alice.get("/packages/mylib")
	if !strings.Contains(page, "Download 1.0.0") {
		t.Error("yanked version should not be offered as latest")
	}
	_, versions := alice.get("/packages/mylib/versions")
	if !strings.Contains(versions, "yanked") || !strings.Contains(versions, "Restore") {
		t.Error("versions tab should mark the yanked version")
	}
	if resp, _ := e.browser().get("/packages/mylib/1.1.0/download"); resp.StatusCode != 200 {
		t.Errorf("yanked version must stay downloadable by exact version: %d", resp.StatusCode)
	}
	alice.post("/dashboard/packages/mylib/versions/1.1.0/unyank", nil, true)
	if _, page := alice.get("/packages/mylib"); !strings.Contains(page, "Download 1.1.0") {
		t.Error("unyank did not restore latest")
	}
	if resp, _ := alice.post("/dashboard/packages/mylib/versions/9.9.9/yank", nil, true); resp.StatusCode != 404 {
		t.Errorf("yank unknown version: %d", resp.StatusCode)
	}

	// edit metadata and replace the README without a new version
	fields := newPkgFields("")
	fields["description"], fields["tags"] = "New description", "audio"
	resp, _ := alice.postMultipart("/dashboard/packages/mylib/edit", fields, upload{"readme", "README.md", []byte("# Fresh readme")})
	if resp.StatusCode != 303 {
		t.Fatalf("edit: %d", resp.StatusCode)
	}
	_, page = alice.get("/packages/mylib")
	if !strings.Contains(page, "New description") || !strings.Contains(page, "Fresh readme") || !strings.Contains(page, ">audio<") || strings.Contains(page, ">gpu<") {
		t.Error("edit not applied")
	}
	// leaving the README field empty keeps the existing one
	alice.postMultipart("/dashboard/packages/mylib/edit", fields)
	if _, page := alice.get("/packages/mylib"); !strings.Contains(page, "Fresh readme") {
		t.Error("README lost when edit form was saved without a file")
	}
	// an invalid README is rejected
	resp, body := alice.postMultipart("/dashboard/packages/mylib/edit", fields, upload{"readme", "README.md", []byte{0xff, 0xfe, 0x00}})
	if resp.StatusCode != 422 || !strings.Contains(body, "UTF-8") {
		t.Errorf("binary README: %d", resp.StatusCode)
	}
}
