package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tonis/c3pkg/internal/store"
)

func seed(t *testing.T, s *store.Store) (alice, bob int64) {
	t.Helper()
	ctx := context.Background()
	alice, bob = twoUsers(t, s)
	mk := func(owner int64, name, desc string, tags ...string) {
		p := params(owner, name, "1.0.0")
		p.Meta = store.Meta{Description: desc, Tags: tags}
		if _, err := s.PublishVersion(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	mk(alice, "vk", "Vulkan bindings for C3", "gpu", "vulkan")
	mk(alice, "wgpu", "WebGPU bindings", "gpu")
	mk(bob, "json", "JSON parser and writer", "parsing")
	mk(bob, "http", "HTTP client", "net", "parsing")
	return
}

func names(l []store.Summary) string {
	var n []string
	for _, s := range l {
		n = append(n, s.Name)
	}
	return strings.Join(n, ",")
}

func TestFTSQuery(t *testing.T) {
	for in, want := range map[string]string{
		"vulkan":            `"vulkan"*`,
		"Web GPU":           `"web"* "gpu"*`,
		`foo" OR "bar`:      `"foo"* "or"* "bar"*`,
		"a-b_c":             `"a"* "b"* "c"*`,
		"  ":                "",
		"*** ()":            "",
		"NEAR(a b) col:val": `"near"* "a"* "b"* "col"* "val"*`,
	} {
		if got := store.FTSQuery(in); got != want {
			t.Errorf("FTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListSearchAndSort(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	alice, bob := seed(t, s)

	l, total, err := s.ListPackages(ctx, store.ListParams{})
	if err != nil || total != 4 || len(l) != 4 {
		t.Fatalf("list: %v total=%d %v", err, total, names(l))
	}
	// search matches name, description and tags, with prefix matching
	for q, want := range map[string]string{"vulkan": "vk", "vul": "vk", "bindings": "wgpu,vk", "parsing": "json,http", "gpu": "wgpu,vk", "http client": "http", "nonexistent": ""} {
		l, _, err := s.ListPackages(ctx, store.ListParams{Q: q})
		got := names(l)
		if err != nil || (got != want && !(len(strings.Split(got, ",")) == len(strings.Split(want, ",")) && sameSet(got, want))) {
			t.Errorf("search %q = %q (err %v), want %q", q, got, err, want)
		}
	}
	// hostile input must never error
	for _, q := range []string{`"`, `"*"`, `a OR`, `NEAR(`, `) AND (`, `\`, `name:vk`, strings.Repeat("a ", 50)} {
		if _, _, err := s.ListPackages(ctx, store.ListParams{Q: q}); err != nil {
			t.Errorf("query %q errored: %v", q, err)
		}
	}
	// tag filter, combined with search
	if l, total, _ := s.ListPackages(ctx, store.ListParams{Tag: "parsing"}); total != 2 || names(l) != "http,json" && names(l) != "json,http" {
		t.Errorf("tag filter: %d %s", total, names(l))
	}
	if l, _, _ := s.ListPackages(ctx, store.ListParams{Tag: "gpu", Q: "web"}); names(l) != "wgpu" {
		t.Errorf("tag+search: %s", names(l))
	}
	// exact name match ranks first
	if l, _, _ := s.ListPackages(ctx, store.ListParams{Q: "json"}); names(l) != "json" {
		t.Errorf("exact: %s", names(l))
	}

	// sorting by downloads and stars
	p := func(name string) *store.Package { x, _ := s.PackageByName(ctx, name); return x }
	vk, js := p("vk"), p("json")
	v, _ := s.VersionByNum(ctx, js.ID, "1.0.0")
	for i := 0; i < 3; i++ {
		s.RecordDownload(ctx, js.ID, v.ID)
	}
	v2, _ := s.VersionByNum(ctx, vk.ID, "1.0.0")
	s.RecordDownload(ctx, vk.ID, v2.ID)
	l, _, _ = s.ListPackages(ctx, store.ListParams{Sort: store.SortDownloads})
	if names(l)[:9] != "json,vk,h" && !strings.HasPrefix(names(l), "json,vk") {
		t.Errorf("by downloads: %s", names(l))
	}
	s.SetStar(ctx, alice, vk.ID, true)
	s.SetStar(ctx, bob, vk.ID, true)
	s.SetStar(ctx, bob, js.ID, true)
	l, _, _ = s.ListPackages(ctx, store.ListParams{Sort: store.SortStars})
	if !strings.HasPrefix(names(l), "vk,json") {
		t.Errorf("by stars: %s", names(l))
	}
	if l[0].Stars != 2 || l[0].Downloads != 1 {
		t.Errorf("counts: %+v", l[0])
	}

	// paging
	l, total, _ = s.ListPackages(ctx, store.ListParams{PerPage: 3, Page: 2})
	if total != 4 || len(l) != 1 {
		t.Errorf("page 2: total=%d len=%d", total, len(l))
	}
	if l, _, _ := s.ListPackages(ctx, store.ListParams{PerPage: 3, Page: 9}); len(l) != 0 {
		t.Errorf("page past end: %v", names(l))
	}
	// removed packages disappear from list, search, tags and stats
	s.DB.Exec(`UPDATE packages SET removed_at = '2026-01-01T00:00:00Z' WHERE name = 'vk'`)
	s.PublishVersion(ctx, params(alice, "wgpu", "1.0.1")) // refreshes FTS rows for wgpu only
	if _, total, _ := s.ListPackages(ctx, store.ListParams{}); total != 3 {
		t.Errorf("removed still listed: %d", total)
	}
	if l, _, _ := s.ListPackages(ctx, store.ListParams{Q: "vulkan"}); len(l) != 0 {
		t.Errorf("removed package found by search: %s", names(l))
	}
}

func sameSet(a, b string) bool {
	x, y := strings.Split(a, ","), strings.Split(b, ",")
	if len(x) != len(y) {
		return false
	}
	m := map[string]bool{}
	for _, s := range x {
		m[s] = true
	}
	for _, s := range y {
		if !m[s] {
			return false
		}
	}
	return true
}

func TestStarsAreIdempotent(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	alice, bob := seed(t, s)
	vk, _ := s.PackageByName(ctx, "vk")
	for i := 0; i < 3; i++ {
		if err := s.SetStar(ctx, alice, vk.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	s.SetStar(ctx, bob, vk.ID, true)
	if p, _ := s.PackageByName(ctx, "vk"); p.Stars != 2 {
		t.Errorf("stars = %d, want 2", p.Stars)
	}
	if ok, _ := s.IsStarred(ctx, alice, vk.ID); !ok {
		t.Error("alice should have starred")
	}
	s.SetStar(ctx, alice, vk.ID, false)
	s.SetStar(ctx, alice, vk.ID, false)
	if p, _ := s.PackageByName(ctx, "vk"); p.Stars != 1 {
		t.Errorf("stars after unstar = %d, want 1", p.Stars)
	}
	if ok, _ := s.IsStarred(ctx, alice, vk.ID); ok {
		t.Error("alice should not have starred")
	}
}

func TestDownloadStatsAndHomeData(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	seed(t, s)
	vk, _ := s.PackageByName(ctx, "vk")
	v, _ := s.VersionByNum(ctx, vk.ID, "1.0.0")
	for i := 0; i < 5; i++ {
		s.RecordDownload(ctx, vk.ID, v.ID)
	}
	if p, _ := s.PackageByName(ctx, "vk"); p.Downloads != 5 {
		t.Errorf("downloads = %d", p.Downloads)
	}
	if m, _ := s.VersionDownloads(ctx, vk.ID); m[v.ID] != 5 {
		t.Errorf("per-version = %v", m)
	}
	days, err := s.DownloadsLastDays(ctx, vk.ID, 7)
	if err != nil || len(days) != 7 || days[6] != 5 {
		t.Errorf("daily = %v %v", days, err)
	}
	// yesterday's rows land in the previous slot
	s.DB.Exec(`UPDATE downloads_daily SET day = strftime('%Y-%m-%d','now','-1 day')`)
	if days, _ := s.DownloadsLastDays(ctx, vk.ID, 7); days[5] != 5 || days[6] != 0 {
		t.Errorf("yesterday = %v", days)
	}
	st, _ := s.Stats(ctx)
	if st.Packages != 4 || st.Downloads != 5 || st.Users != 2 {
		t.Errorf("stats = %+v", st)
	}
	tags, _ := s.PopularTags(ctx, 3)
	if len(tags) != 3 || tags[0].Count != 2 {
		t.Errorf("tags = %+v", tags)
	}
}
