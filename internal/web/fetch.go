package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tonis/c3pkg/internal/store"
)

// The `c3c fetch` registry protocol (see fetch_protocol.md): three plain GETs at the site root.
// A library id is <owner login>/<package name>. Logins never change, so the id is stable, and the
// package name alone is already unique, so a request with the wrong namespace is just a 404.

type fetchEntry struct {
	Library     string   `json:"library"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Latest      string   `json:"latest"`
	Updated     string   `json:"updated"`
	Versions    []string `json:"versions"`
}

type fetchVersion struct {
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Maintainer  string   `json:"maintainer,omitempty"`
	Repository  string   `json:"repository,omitempty"`
	License     string   `json:"license,omitempty"`
	Targets     []string `json:"targets,omitempty"`
	Checksum    string   `json:"checksum"`
	Size        int64    `json:"size"`
	Published   string   `json:"published"`
	Yanked      bool     `json:"yanked,omitempty"` // not in the protocol; c3c ignores unknown fields
}

// fetchVersions marshals as an object keyed by version in slice order: c3c shows the versions in
// key order, so a Go map (sorted or random) would scramble "newest first".
type fetchVersions []fetchVersion

func (vs fetchVersions) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, v := range vs {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(v.Version)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type fetchInfo struct {
	Library     string        `json:"library"`
	Description string        `json:"description"`
	Tags        []string      `json:"tags"`
	Latest      string        `json:"latest"`
	Updated     string        `json:"updated"`
	Versions    fetchVersions `json:"versions"`
}

// writeCacheable sends v as JSON with a body-hash ETag, answering If-None-Match with 304.
func writeCacheable(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "internal error", nil)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "public, max-age=60")
	for _, tag := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if tag = strings.TrimSpace(tag); tag == etag || tag == "*" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	h.Set("Content-Type", "application/json; charset=utf-8")
	w.Write(append(body, '\n'))
}

// GET /index.json
func (s *Server) fetchIndex(w http.ResponseWriter, r *http.Request) {
	libs, err := s.store.ListLibraries(r.Context())
	if err != nil {
		s.apiInternal(w, "fetch index", err)
		return
	}
	entries := []fetchEntry{} // an empty registry is {"libraries": []}, never null
	for _, l := range libs {
		latest := store.LatestVersion(l.Versions)
		if latest == nil {
			continue // everything yanked: nothing to offer, and c3c needs a `latest`
		}
		e := fetchEntry{Library: l.OwnerLogin + "/" + l.Name, Description: l.Description, Tags: nonNil(l.Tags),
			Latest: latest.Version, Updated: latest.CreatedAt, Versions: []string{}}
		for _, v := range l.Versions {
			if !v.Yanked {
				e.Versions = append(e.Versions, v.Version)
			}
		}
		entries = append(entries, e)
	}
	writeCacheable(w, r, map[string]any{"libraries": entries})
}

// fetchPackage loads the live package named by the "name" path value and checks the namespace.
func (s *Server) fetchPackage(w http.ResponseWriter, r *http.Request, ns string) (*store.Package, bool) {
	p, ok := s.apiLivePackage(w, r)
	if !ok {
		return nil, false
	}
	if !strings.EqualFold(p.OwnerLogin, ns) {
		apiError(w, http.StatusNotFound, "package not found", nil)
		return nil, false
	}
	return p, true
}

// GET /info/{ns}/{name}
func (s *Server) fetchInfo(w http.ResponseWriter, r *http.Request) {
	p, ok := s.fetchPackage(w, r, r.PathValue("ns"))
	if !ok {
		return
	}
	vs, err := s.store.VersionsOf(r.Context(), p.ID)
	if err != nil {
		s.apiInternal(w, "fetch info", err)
		return
	}
	latest := store.LatestVersion(vs)
	if latest == nil {
		apiError(w, http.StatusNotFound, "package has no available version", nil)
		return
	}
	maintainer := p.OwnerName // never the e-mail address
	if maintainer == "" {
		maintainer = p.OwnerLogin
	}
	info := fetchInfo{Library: p.OwnerLogin + "/" + p.Name, Description: p.Meta.Description, Tags: nonNil(p.Meta.Tags),
		Latest: latest.Version, Updated: latest.CreatedAt, Versions: fetchVersions{}}
	// Yanked versions stay in the record: c3c needs the version to be a key, and yanked
	// versions remain fetchable by exact version so that existing builds keep working.
	for _, v := range vs {
		info.Versions = append(info.Versions, fetchVersion{Version: v.Version, Description: p.Meta.Description, Maintainer: maintainer,
			Repository: p.Meta.GitURL, License: p.Meta.License, Targets: v.Targets, Checksum: "sha256:" + v.SHA256File,
			Size: v.Size, Published: v.CreatedAt, Yanked: v.Yanked})
	}
	writeCacheable(w, r, info)
}

// GET /download/{ns}/{name}@{version}. A bare name or @latest (optional in the protocol) serves
// the current latest version, so that a browser or `curl -OJ` just works.
func (s *Server) fetchDownload(w http.ResponseWriter, r *http.Request) {
	name, version, hasVersion := strings.Cut(r.PathValue("file"), "@")
	r.SetPathValue("name", name)
	p, ok := s.fetchPackage(w, r, r.PathValue("ns"))
	if !ok {
		return
	}
	var v *store.Version
	var err error
	alias := !hasVersion || version == "latest"
	if alias {
		var vs []store.Version
		if vs, err = s.store.VersionsOf(r.Context(), p.ID); err == nil {
			if v = store.LatestVersion(vs); v == nil {
				err = store.ErrNotFound
			}
		}
	} else {
		v, err = s.store.VersionByNum(r.Context(), p.ID, version)
	}
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "version not found", nil)
		return
	}
	if err != nil {
		s.apiInternal(w, "fetch download", err)
		return
	}
	s.sendVersion(w, r, p, v, true, !alias)
}
