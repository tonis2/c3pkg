package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tonis/c3pkg/internal/store"
)

type sortLink struct {
	Label  string
	URL    string
	Active bool
}

type listingView struct {
	Home      bool // the landing page with the hero, as opposed to a search result page
	Q, Tag    string
	Sort      string
	Packages  []store.Summary
	Total     int
	Page      int
	PrevURL   string
	NextURL   string
	SortLinks []sortLink
	Stats     store.Stats
	Tags      []store.TagCount
}

var sortOptions = []struct{ key, label string }{
	{store.SortLatest, "Latest"},
	{store.SortDownloads, "Most downloaded"},
	{store.SortStars, "Top rated"},
}

func normalizeSort(s string) string {
	for _, o := range sortOptions {
		if s == o.key {
			return s
		}
	}
	return store.SortLatest
}

func pageParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 || n > 10000 {
		return 1
	}
	return n
}

// listURL builds /search?... keeping only non-default parameters.
func listURL(q, tag, sort string, page int) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if tag != "" {
		v.Set("tag", tag)
	}
	if sort != store.SortLatest {
		v.Set("sort", sort)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/"
	}
	return "/search?" + v.Encode()
}

// listing serves both the home page and /search.
func (s *Server) listing(home bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(q) > 100 {
			q = q[:100]
		}
		tag := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag")))
		sort := normalizeSort(r.URL.Query().Get("sort"))
		page := pageParam(r)

		list, total, err := s.store.ListPackages(r.Context(), store.ListParams{Sort: sort, Q: q, Tag: tag, Page: page})
		if err != nil {
			s.serverError(w, "list packages", err)
			return
		}
		v := listingView{Home: home && q == "" && tag == "", Q: q, Tag: tag, Sort: sort, Packages: list, Total: total, Page: page}
		for _, o := range sortOptions {
			v.SortLinks = append(v.SortLinks, sortLink{o.label, listURL(q, tag, o.key, 1), o.key == sort})
		}
		if page > 1 {
			v.PrevURL = listURL(q, tag, sort, page-1)
		}
		if page*store.PageSize < total {
			v.NextURL = listURL(q, tag, sort, page+1)
		}
		title := ""
		if v.Home {
			if v.Stats, err = s.store.Stats(r.Context()); err == nil {
				v.Tags, err = s.store.PopularTags(r.Context(), 12)
			}
			if err != nil {
				s.serverError(w, "home data", err)
				return
			}
		} else {
			title = "Search"
			if q != "" {
				title = "Search: " + q
			}
		}
		s.render(w, r, "home", pageData{Title: title, NoHeaderSearch: v.Home, Data: v})
	}
}

// ---- stars ----

func (s *Server) setStar(starred bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.livePackage(w, r)
		if !ok {
			return
		}
		if err := s.store.SetStar(r.Context(), userFrom(r).ID, p.ID, starred); err != nil {
			s.serverError(w, "star", err)
			return
		}
		http.Redirect(w, r, "/packages/"+p.Name, http.StatusSeeOther)
	}
}
