package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tonis/c3pkg/internal/store"
)

func params(userID int64, name, version string) store.PublishParams {
	return store.PublishParams{
		UserID: userID, Name: name, Version: version, C3Version: "0.7", Targets: []string{"linux-x64"},
		Size: 100, SHA256File: "f" + version, SHA256Tree: "t" + version, StorageKey: "k" + version,
		Meta: store.Meta{Description: "GPU bindings", Tags: []string{"gpu", "vulkan"}, License: "MIT"},
	}
}

func twoUsers(t *testing.T, s *store.Store) (alice, bob int64) {
	ctx := context.Background()
	a, _ := s.CreateUser(ctx, "a@example.com", "alice", "Alice", "h", 0, "", "")
	b, _ := s.CreateUser(ctx, "b@example.com", "bob", "Bob", "h", 0, "", "")
	return a.ID, b.ID
}

func TestPublishFlow(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	alice, bob := twoUsers(t, s)

	created, err := s.PublishVersion(ctx, params(alice, "vk", "1.0.0"))
	if err != nil || !created {
		t.Fatalf("first publish: %v %v", created, err)
	}
	if created, err := s.PublishVersion(ctx, params(alice, "vk", "1.1.0")); err != nil || created {
		t.Fatalf("second version: %v %v", created, err)
	}
	if _, err := s.PublishVersion(ctx, params(alice, "vk", "1.1.0")); !errors.Is(err, store.ErrVersionExists) {
		t.Errorf("duplicate version: %v", err)
	}
	if _, err := s.PublishVersion(ctx, params(bob, "vk", "2.0.0")); !errors.Is(err, store.ErrNotOwner) {
		t.Errorf("other user's package: %v", err)
	}
	// look-alike names are blocked even though the exact name differs
	if _, err := s.PublishVersion(ctx, params(alice, "my-lib", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishVersion(ctx, params(bob, "my_lib", "1.0.0")); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("look-alike name: %v", err)
	}
	// a failed publish must not leave a half-created package behind
	if _, err := s.PublishVersion(ctx, params(bob, "other", "1.0.0")); err != nil {
		t.Fatal(err)
	}

	p, err := s.PackageByName(ctx, "VK")
	if err != nil || p.OwnerLogin != "alice" || p.Meta.Description != "GPU bindings" || len(p.Meta.Tags) != 2 {
		t.Fatalf("package: %+v %v", p, err)
	}
	vs, _ := s.VersionsOf(ctx, p.ID)
	if len(vs) != 2 || vs[0].Version != "1.1.0" || vs[0].Targets[0] != "linux-x64" {
		t.Errorf("versions: %+v", vs)
	}
}

func TestLatestVersionAndYank(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	alice, bob := twoUsers(t, s)
	for _, v := range []string{"1.0.0", "1.10.0", "1.9.0", "2.0.0-rc.1"} {
		if _, err := s.PublishVersion(ctx, params(alice, "vk", v)); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := s.PackageByName(ctx, "vk")
	latest := func() string {
		vs, _ := s.VersionsOf(ctx, p.ID)
		if l := store.LatestVersion(vs); l != nil {
			return l.Version
		}
		return ""
	}
	if got := latest(); got != "1.10.0" {
		t.Errorf("latest = %s, want 1.10.0 (numeric order, prerelease skipped)", got)
	}
	if err := s.SetYanked(ctx, p.ID, bob, "1.10.0", true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("non-owner yank: %v", err)
	}
	s.SetYanked(ctx, p.ID, alice, "1.10.0", true)
	if got := latest(); got != "1.9.0" {
		t.Errorf("after yank latest = %s, want 1.9.0", got)
	}
	s.SetYanked(ctx, p.ID, alice, "1.10.0", false)
	if got := latest(); got != "1.10.0" {
		t.Errorf("after unyank latest = %s", got)
	}
	for _, v := range []string{"1.0.0", "1.9.0", "1.10.0"} {
		s.SetYanked(ctx, p.ID, alice, v, true)
	}
	if got := latest(); got != "2.0.0-rc.1" {
		t.Errorf("only prerelease left: %q", got)
	}
}

func TestMetadataReadmeAndSearchIndex(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	alice, bob := twoUsers(t, s)
	s.PublishVersion(ctx, params(alice, "vk", "1.0.0"))
	p, _ := s.PackageByName(ctx, "vk")

	fts := func(q string) int {
		var n int
		s.DB.QueryRow(`SELECT COUNT(*) FROM packages_fts WHERE packages_fts MATCH ?`, q).Scan(&n)
		return n
	}
	if fts("vulkan") != 1 || fts("bindings") != 1 {
		t.Error("package should be searchable by tag and description after publish")
	}
	if err := s.UpdateMetadata(ctx, p.ID, bob, store.Meta{Description: "hijack"}); !errors.Is(err, store.ErrNotOwner) {
		t.Errorf("non-owner metadata edit: %v", err)
	}
	if err := s.UpdateMetadata(ctx, p.ID, alice, store.Meta{Description: "Math library", Tags: []string{"math"}, GitURL: "https://example.com/vk"}); err != nil {
		t.Fatal(err)
	}
	if fts("vulkan") != 0 || fts("math") != 1 {
		t.Error("search index not refreshed after metadata edit")
	}
	if err := s.SetReadme(ctx, p.ID, bob, "x", "x"); !errors.Is(err, store.ErrNotOwner) {
		t.Errorf("non-owner readme: %v", err)
	}
	if err := s.SetReadme(ctx, p.ID, alice, "# md", "<h1>md</h1>"); err != nil {
		t.Fatal(err)
	}
	p, _ = s.PackageByName(ctx, "vk")
	if p.ReadmeHTML != "<h1>md</h1>" || p.Meta.GitURL != "https://example.com/vk" || len(p.Meta.Tags) != 1 {
		t.Errorf("package after edits: %+v", p)
	}
}

func TestPackagesByOwnerAndRemoved(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	alice, _ := twoUsers(t, s)
	s.PublishVersion(ctx, params(alice, "aaa", "1.0.0"))
	s.PublishVersion(ctx, params(alice, "bbb", "0.1.0"))
	s.PublishVersion(ctx, params(alice, "bbb", "0.2.0"))
	list, err := s.PackagesByOwner(ctx, alice)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	for _, sm := range list {
		if sm.Name == "bbb" && sm.Latest != "0.2.0" || sm.OwnerLogin != "alice" || sm.C3Version != "0.7" {
			t.Errorf("summary: %+v", sm)
		}
	}
	s.DB.Exec(`UPDATE packages SET removed_at = '2026-01-01T00:00:00Z' WHERE name = 'aaa'`)
	if list, _ := s.PackagesByOwner(ctx, alice); len(list) != 1 {
		t.Errorf("removed package still listed: %+v", list)
	}
	if _, err := s.PublishVersion(ctx, params(alice, "aaa", "2.0.0")); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("publishing to a removed package: %v", err)
	}
}

func TestReadmeSource(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	alice, _ := twoUsers(t, s)
	readme := func() string { p, _ := s.PackageByName(ctx, "vk"); return p.ReadmeMD }
	publish := func(version, md, source string) {
		p := params(alice, "vk", version)
		p.ReadmeMD, p.ReadmeHTML, p.ReadmeSource = md, "<p>"+md+"</p>", source
		if _, err := s.PublishVersion(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	publish("1.0.0", "from zip 1", "zip")
	publish("1.0.1", "from zip 2", "zip")
	if readme() != "from zip 2" {
		t.Errorf("a zip README should refresh on publish, got %q", readme())
	}
	publish("1.0.2", "", "")
	if readme() != "from zip 2" {
		t.Errorf("an archive without a README keeps the current one, got %q", readme())
	}
	p, _ := s.PackageByName(ctx, "vk")
	if err := s.SetReadme(ctx, p.ID, alice, "by hand", "<p>by hand</p>"); err != nil {
		t.Fatal(err)
	}
	publish("1.0.3", "from zip 3", "zip")
	if readme() != "by hand" {
		t.Errorf("a manual README must survive a publish, got %q", readme())
	}
	publish("1.0.4", "uploaded with version", "manual")
	if readme() != "uploaded with version" {
		t.Errorf("a manual upload always applies, got %q", readme())
	}
}
