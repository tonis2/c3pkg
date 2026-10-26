package web

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tonis/c3pkg/internal/pkg"
	"github.com/tonis/c3pkg/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// apiError writes {"error": msg, "errors": {field: message}}.
func apiError(w http.ResponseWriter, status int, msg string, fields map[string]string) {
	body := map[string]any{"error": msg}
	if len(fields) > 0 {
		body["errors"] = fields
		if msg == "" {
			keys := make([]string, 0, len(fields))
			for k := range fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			body["error"] = fields[keys[0]]
		}
	}
	writeJSON(w, status, body)
}

func (s *Server) apiInternal(w http.ResponseWriter, what string, err error) {
	log.Printf("api %s: %v", what, err)
	apiError(w, http.StatusInternalServerError, "internal error", nil)
}

// apiAuth requires a signed-in user (session or API token).
func (s *Server) apiAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r) == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="c3pkg"`)
			apiError(w, http.StatusUnauthorized, "authentication required: send 'Authorization: Bearer <token>'", nil)
			return
		}
		h(w, r)
	}
}

// apiSessionOnly requires a browser session; API tokens cannot be used for token management.
func (s *Server) apiSessionOnly(h http.HandlerFunc) http.HandlerFunc {
	return s.apiAuth(func(w http.ResponseWriter, r *http.Request) {
		if via, _ := r.Context().Value(tokenAuthKey).(bool); via {
			apiError(w, http.StatusForbidden, "this endpoint needs a logged-in browser session, not an API token", nil)
			return
		}
		h(w, r)
	})
}

func (s *Server) apiLivePackage(w http.ResponseWriter, r *http.Request) (*store.Package, bool) {
	p, err := s.store.PackageByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Removed) {
		apiError(w, http.StatusNotFound, "package not found", nil)
		return nil, false
	}
	if err != nil {
		s.apiInternal(w, "package", err)
		return nil, false
	}
	return p, true
}

func (s *Server) apiOwnedPackage(w http.ResponseWriter, r *http.Request) (*store.Package, bool) {
	p, ok := s.apiLivePackage(w, r)
	if !ok {
		return nil, false
	}
	if p.OwnerID != userFrom(r).ID {
		apiError(w, http.StatusForbidden, "you are not the owner of this package", nil)
		return nil, false
	}
	return p, true
}

// ---- JSON shapes ----

type apiVersion struct {
	Version     string   `json:"version"`
	C3Version   string   `json:"c3_version"`
	Targets     []string `json:"targets"`
	Size        int64    `json:"size"`
	SHA256File  string   `json:"sha256_file"`
	SHA256Tree  string   `json:"sha256_tree"`
	Yanked      bool     `json:"yanked"`
	Published   string   `json:"published"`
	DownloadURL string   `json:"download_url"`
}

type apiPackage struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Tags        []string     `json:"tags"`
	Owner       string       `json:"owner"`
	Latest      string       `json:"latest_version,omitempty"`
	C3Version   string       `json:"c3_version,omitempty"`
	Downloads   int64        `json:"downloads"`
	Stars       int64        `json:"stars"`
	Updated     string       `json:"updated"`
	License     string       `json:"license,omitempty"`
	DocsURL     string       `json:"docs_url,omitempty"`
	GitURL      string       `json:"git_url,omitempty"`
	Created     string       `json:"created,omitempty"`
	Readme      string       `json:"readme,omitempty"`
	Versions    []apiVersion `json:"versions,omitempty"`
}

func nonNil(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

func toAPIVersion(name string, v store.Version) apiVersion {
	return apiVersion{Version: v.Version, C3Version: v.C3Version, Targets: nonNil(v.Targets), Size: v.Size, SHA256File: v.SHA256File,
		SHA256Tree: v.SHA256Tree, Yanked: v.Yanked, Published: v.CreatedAt, DownloadURL: "/api/v1/packages/" + name + "/" + v.Version + "/download"}
}

func toAPISummary(sm store.Summary) apiPackage {
	return apiPackage{Name: sm.Name, Description: sm.Description, Tags: nonNil(sm.Tags), Owner: sm.OwnerLogin, Latest: sm.Latest,
		C3Version: sm.C3Version, Downloads: sm.Downloads, Stars: sm.Stars, Updated: sm.UpdatedAt}
}

// ---- read endpoints ----

func (s *Server) apiListPackages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = store.PageSize
	}
	query := strings.TrimSpace(q.Get("q"))
	if len(query) > 100 {
		query = query[:100]
	}
	page := pageParam(r)
	list, total, err := s.store.ListPackages(r.Context(), store.ListParams{
		Sort: normalizeSort(q.Get("sort")), Q: query, Tag: strings.ToLower(q.Get("tag")), Page: page, PerPage: perPage})
	if err != nil {
		s.apiInternal(w, "list", err)
		return
	}
	out := make([]apiPackage, 0, len(list))
	for _, sm := range list {
		out = append(out, toAPISummary(sm))
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": out, "total": total, "page": page, "per_page": perPage})
}

func (s *Server) apiGetPackage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiLivePackage(w, r)
	if !ok {
		return
	}
	vs, err := s.store.VersionsOf(r.Context(), p.ID)
	if err != nil {
		s.apiInternal(w, "versions", err)
		return
	}
	out := apiPackage{Name: p.Name, Description: p.Meta.Description, Tags: nonNil(p.Meta.Tags), Owner: p.OwnerLogin, Downloads: p.Downloads,
		Stars: p.Stars, Updated: p.UpdatedAt, Created: p.CreatedAt, License: p.Meta.License, DocsURL: p.Meta.DocsURL, GitURL: p.Meta.GitURL,
		Readme: p.ReadmeMD, Versions: []apiVersion{}}
	if l := store.LatestVersion(vs); l != nil {
		out.Latest, out.C3Version = l.Version, l.C3Version
	}
	for _, v := range vs {
		out.Versions = append(out.Versions, toAPIVersion(p.Name, v))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiLivePackage(w, r)
	if !ok {
		return
	}
	vs, err := s.store.VersionsOf(r.Context(), p.ID)
	if err != nil {
		s.apiInternal(w, "versions", err)
		return
	}
	out := make([]apiVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, toAPIVersion(p.Name, v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (s *Server) apiUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByLogin(r.Context(), r.PathValue("login"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Banned) {
		apiError(w, http.StatusNotFound, "user not found", nil)
		return
	}
	if err != nil {
		s.apiInternal(w, "user", err)
		return
	}
	pkgs, err := s.store.PackagesByOwner(r.Context(), u.ID)
	if err != nil {
		s.apiInternal(w, "user packages", err)
		return
	}
	out := make([]apiPackage, 0, len(pkgs))
	for _, sm := range pkgs {
		out = append(out, toAPISummary(sm))
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": u.Login, "name": u.Name, "bio": u.Bio, "github": u.GitHubLogin, "joined": u.CreatedAt, "packages": out})
}

// ---- write endpoints ----

func (s *Server) apiPublish(w http.ResponseWriter, r *http.Request) {
	name, version := r.PathValue("name"), r.PathValue("version")
	if msg := pkg.ValidateName(name); msg != "" {
		apiError(w, http.StatusUnprocessableEntity, msg, nil)
		return
	}
	existing, err := s.store.PackageByName(r.Context(), name)
	newPackage := errors.Is(err, store.ErrNotFound)
	switch {
	case err != nil && !newPackage:
		s.apiInternal(w, "package lookup", err)
		return
	case !newPackage && (existing.Removed || existing.OwnerID != userFrom(r).ID):
		apiError(w, http.StatusForbidden, "you are not the owner of this package", nil)
		return
	}
	out, errs, status := s.doPublish(r, name, version, newPackage)
	if out == nil {
		if status == http.StatusInternalServerError {
			apiError(w, status, "internal error", nil)
			return
		}
		apiError(w, status, "", errs)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": out.Name, "version": out.Version, "created": out.Created,
		"url": s.cfg.BaseURL + "/packages/" + out.Name})
}

func (s *Server) apiYank(yanked bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.apiOwnedPackage(w, r)
		if !ok {
			return
		}
		err := s.store.SetYanked(r.Context(), p.ID, userFrom(r).ID, r.PathValue("version"), yanked)
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "version not found", nil)
			return
		}
		if err != nil {
			s.apiInternal(w, "yank", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) apiSetReadme(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiOwnedPackage(w, r)
	if !ok {
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, pkg.MaxReadmeBytes+1))
	if err != nil {
		apiError(w, http.StatusBadRequest, "could not read the request body", nil)
		return
	}
	if msg := pkg.ValidateReadme(b); msg != "" {
		apiError(w, http.StatusUnprocessableEntity, msg, nil)
		return
	}
	if err := s.store.SetReadme(r.Context(), p.ID, userFrom(r).ID, string(b), string(pkg.RenderReadme(string(b)))); err != nil {
		s.apiInternal(w, "readme", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiPatchPackage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiOwnedPackage(w, r)
	if !ok {
		return
	}
	var in struct {
		Description *string   `json:"description"`
		Tags        *[]string `json:"tags"`
		License     *string   `json:"license"`
		DocsURL     *string   `json:"docs_url"`
		GitURL      *string   `json:"git_url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, "request body must be a JSON object", nil)
		return
	}
	m := p.Meta
	if in.Description != nil {
		m.Description = strings.TrimSpace(*in.Description)
	}
	if in.License != nil {
		m.License = strings.TrimSpace(*in.License)
	}
	if in.DocsURL != nil {
		m.DocsURL = strings.TrimSpace(*in.DocsURL)
	}
	if in.GitURL != nil {
		m.GitURL = strings.TrimSpace(*in.GitURL)
	}
	tags := strings.Join(p.Meta.Tags, ",")
	if in.Tags != nil {
		tags = strings.Join(*in.Tags, ",")
	}
	errs := map[string]string{}
	m = checkMeta(m, tags, errs)
	if len(errs) > 0 {
		apiError(w, http.StatusUnprocessableEntity, "", errs)
		return
	}
	if err := s.store.UpdateMetadata(r.Context(), p.ID, userFrom(r).ID, m); err != nil {
		s.apiInternal(w, "update metadata", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiStar(starred bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.apiLivePackage(w, r)
		if !ok {
			return
		}
		if err := s.store.SetStar(r.Context(), userFrom(r).ID, p.ID, starred); err != nil {
			s.apiInternal(w, "star", err)
			return
		}
		updated, err := s.store.PackageByName(r.Context(), p.Name)
		if err != nil {
			s.apiInternal(w, "star count", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"starred": starred, "stars": updated.Stars})
	}
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"login": u.Login, "name": u.Name, "email": u.Email, "bio": u.Bio, "github": u.GitHubLogin, "admin": u.IsAdmin})
}

func (s *Server) apiPatchMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var in struct {
		Name   *string `json:"name"`
		Bio    *string `json:"bio"`
		GitHub *string `json:"github"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, "request body must be a JSON object", nil)
		return
	}
	name, bio, gh := u.Name, u.Bio, u.GitHubLogin
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Bio != nil {
		bio = strings.TrimSpace(*in.Bio)
	}
	if in.GitHub != nil {
		gh = strings.TrimSpace(*in.GitHub)
	}
	errs := map[string]string{}
	if name == "" || utf8.RuneCountInString(name) > 80 {
		errs["name"] = "Enter a name (up to 80 characters)."
	}
	if utf8.RuneCountInString(bio) > 280 {
		errs["bio"] = "Keep the bio under 280 characters."
	}
	if gh != "" && !githubLoginRe.MatchString(gh) {
		errs["github"] = "That is not a valid GitHub username."
	}
	if len(errs) > 0 {
		apiError(w, http.StatusUnprocessableEntity, "", errs)
		return
	}
	if err := s.store.UpdateProfile(r.Context(), u.ID, name, bio, gh); err != nil {
		s.apiInternal(w, "update profile", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListAPITokens(r.Context(), userFrom(r).ID)
	if err != nil {
		s.apiInternal(w, "tokens", err)
		return
	}
	type item struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		LastUsed string `json:"last_used,omitempty"`
		Created  string `json:"created"`
	}
	out := make([]item, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, item{t.ID, t.Name, t.LastUsedAt, t.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) apiCreateToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, "request body must be JSON like {\"name\": \"laptop\"}", nil)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 60 {
		apiError(w, http.StatusUnprocessableEntity, "give the token a name (up to 60 characters)", nil)
		return
	}
	raw, err := s.store.CreateAPIToken(r.Context(), userFrom(r).ID, name)
	if err != nil {
		s.apiInternal(w, "create token", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "name": name})
}

func (s *Server) apiRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusNotFound, "token not found", nil)
		return
	}
	if err := s.store.RevokeAPIToken(r.Context(), userFrom(r).ID, id); err != nil {
		s.apiInternal(w, "revoke token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- proxy-style index for a future command-line client ----

// index serves /index/{name}/list, /index/{name}/{version}.json and /index/{name}/{version}.c3l.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	switch {
	case file == "list":
		p, ok := s.apiLivePackage(w, r)
		if !ok {
			return
		}
		vs, err := s.store.VersionsOf(r.Context(), p.ID)
		if err != nil {
			s.apiInternal(w, "index list", err)
			return
		}
		var b strings.Builder
		for _, v := range vs {
			if !v.Yanked { // yanked versions stay fetchable by exact version but are not offered
				b.WriteString(v.Version + "\n")
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, b.String())
	case strings.HasSuffix(file, ".json"):
		p, ok := s.apiLivePackage(w, r)
		if !ok {
			return
		}
		v, err := s.store.VersionByNum(r.Context(), p.ID, strings.TrimSuffix(file, ".json"))
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "version not found", nil)
			return
		}
		if err != nil {
			s.apiInternal(w, "index version", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name": p.Name, "version": v.Version, "c3_version": v.C3Version, "targets": nonNil(v.Targets), "requires": []string{},
			"size": v.Size, "sha256_file": v.SHA256File, "sha256_tree": v.SHA256Tree, "yanked": v.Yanked,
			"download": "/index/" + p.Name + "/" + v.Version + ".c3l"})
	case strings.HasSuffix(file, ".c3l"):
		r.SetPathValue("version", strings.TrimSuffix(file, ".c3l"))
		s.download(w, r)
	default:
		apiError(w, http.StatusNotFound, "not found", nil)
	}
}
