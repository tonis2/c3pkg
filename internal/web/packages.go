package web

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/tonis/c3pkg/internal/pkg"
	"github.com/tonis/c3pkg/internal/storage"
	"github.com/tonis/c3pkg/internal/store"
)

type packageView struct {
	Pkg              *store.Package
	Versions         []store.Version
	Latest           *store.Version
	Tab              string
	IsOwner          bool
	Starred          bool
	Readme           template.HTML
	VersionDownloads map[int64]int64
	Spark            []sparkBar
	SparkTotal       int64
}

// sparkBar is one bar of the 30-day download chart, in SVG user units.
type sparkBar struct{ X, Y, H int }

type formData struct {
	Pkg     *store.Package // set when editing or publishing an update
	MaxMB   int64
	Targets any
}

func (s *Server) limits() pkg.Limits {
	return pkg.Limits{MaxCompressed: s.cfg.MaxUploadMB << 20, MaxUnzipped: s.cfg.MaxUnzippedMB << 20, MaxFiles: s.cfg.MaxFiles}
}

func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	s.render(w, r, "message", pageData{Title: "Not found", Data: "We could not find that page."})
}

// livePackage loads the package named in the URL; removed packages count as missing.
func (s *Server) livePackage(w http.ResponseWriter, r *http.Request) (*store.Package, bool) {
	p, err := s.store.PackageByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Removed) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, "package lookup", err)
		return nil, false
	}
	return p, true
}

// ownedPackage is livePackage plus a check that the signed-in user owns it.
func (s *Server) ownedPackage(w http.ResponseWriter, r *http.Request) (*store.Package, bool) {
	p, ok := s.livePackage(w, r)
	if !ok {
		return nil, false
	}
	if u := userFrom(r); u == nil || u.ID != p.OwnerID {
		w.WriteHeader(http.StatusForbidden)
		s.render(w, r, "message", pageData{Title: "Not allowed", Data: "Only the package owner can do that."})
		return nil, false
	}
	return p, true
}

func (s *Server) packageView(w http.ResponseWriter, r *http.Request, tab string) {
	p, ok := s.livePackage(w, r)
	if !ok {
		return
	}
	vs, err := s.store.VersionsOf(r.Context(), p.ID)
	if err != nil {
		s.serverError(w, "versions", err)
		return
	}
	u := userFrom(r)
	view := packageView{Pkg: p, Versions: vs, Latest: store.LatestVersion(vs), Tab: tab, Readme: template.HTML(p.ReadmeHTML)}
	if u != nil {
		view.IsOwner = u.ID == p.OwnerID
		if view.Starred, err = s.store.IsStarred(r.Context(), u.ID, p.ID); err != nil {
			s.serverError(w, "starred", err)
			return
		}
	}
	if tab == "versions" {
		if view.VersionDownloads, err = s.store.VersionDownloads(r.Context(), p.ID); err != nil {
			s.serverError(w, "version downloads", err)
			return
		}
	}
	days, err := s.store.DownloadsLastDays(r.Context(), p.ID, 30)
	if err != nil {
		s.serverError(w, "daily downloads", err)
		return
	}
	view.Spark, view.SparkTotal = sparkline(days)
	s.render(w, r, "package", pageData{Title: p.Name, Data: view})
}

func (s *Server) packagePage(w http.ResponseWriter, r *http.Request) { s.packageView(w, r, "readme") }
func (s *Server) versionsPage(w http.ResponseWriter, r *http.Request) {
	s.packageView(w, r, "versions")
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	api := strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/index/")
	var p *store.Package
	var ok bool
	if api {
		p, ok = s.apiLivePackage(w, r)
	} else {
		p, ok = s.livePackage(w, r)
	}
	if !ok {
		return
	}
	v, err := s.store.VersionByNum(r.Context(), p.ID, r.PathValue("version"))
	if errors.Is(err, store.ErrNotFound) {
		s.downloadFail(w, r, api, http.StatusNotFound, "version not found", nil)
		return
	}
	if err != nil {
		s.downloadFail(w, r, api, http.StatusInternalServerError, "version lookup", err)
		return
	}
	s.sendVersion(w, r, p, v, api, true)
}

func (s *Server) downloadFail(w http.ResponseWriter, r *http.Request, api bool, status int, msg string, err error) {
	switch {
	case api && status == http.StatusNotFound:
		apiError(w, status, msg, nil)
	case api:
		s.apiInternal(w, msg, err)
	case status == http.StatusNotFound:
		s.notFound(w, r)
	default:
		s.serverError(w, msg, err)
	}
}

// sendVersion streams a version's .c3l and counts the download. immutable is false for aliases such
// as "latest", whose target changes: clients must revalidate instead of caching them for a year.
func (s *Server) sendVersion(w http.ResponseWriter, r *http.Request, p *store.Package, v *store.Version, api, immutable bool) {
	f, err := s.storage.Open(v.StorageKey)
	if err != nil {
		s.downloadFail(w, r, api, http.StatusInternalServerError, "open stored file", err)
		return
	}
	defer f.Close()
	// Count real downloads only: not HEAD requests or resumed ranges.
	if r.Method == http.MethodGet && (r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-")) {
		if err := s.store.RecordDownload(r.Context(), p.ID, v.ID); err != nil {
			log.Printf("record download: %v", err)
		}
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.c3l"`, p.Name, v.Version))
	h.Set("ETag", `"`+v.SHA256File+`"`)
	if immutable {
		h.Set("Cache-Control", "public, max-age=31536000, immutable") // published versions never change
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "", v.CreatedAtTime(), f)
}

func (s *Server) userPage(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByLogin(r.Context(), r.PathValue("login"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Banned) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "user lookup", err)
		return
	}
	pkgs, err := s.store.PackagesByOwner(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, "user packages", err)
		return
	}
	s.render(w, r, "user", pageData{Title: u.Name, Data: struct {
		Profile  *store.User
		Packages []store.Summary
	}{u, pkgs}})
}

// ---- publishing ----

// readUpload reads and validates the "file" part. expectName, if set, must match the manifest.
func (s *Server) readUpload(r *http.Request, errs map[string]string, expectName string) (*pkg.Info, []byte) {
	f, hdr, err := r.FormFile("file")
	if err != nil {
		errs["file"] = "Choose a .c3l file to upload."
		return nil, nil
	}
	defer f.Close()
	if !strings.HasSuffix(strings.ToLower(hdr.Filename), ".c3l") {
		errs["file"] = "The file must have the .c3l extension."
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, s.cfg.MaxUploadMB<<20+1))
	if err != nil {
		errs["file"] = "Could not read the uploaded file."
		return nil, nil
	}
	info, err := pkg.Inspect(data, s.limits())
	if err != nil {
		var ve *pkg.ValidationError
		if errors.As(err, &ve) {
			errs["file"] = ve.Msg
		} else {
			errs["file"] = "Could not read the archive."
		}
		return nil, nil
	}
	if expectName != "" && info.Name != expectName {
		errs["file"] = fmt.Sprintf(`manifest.json "provides" is %q, but this package is named %q.`, info.Name, expectName)
		return nil, nil
	}
	return info, data
}

func checkVersionFields(r *http.Request, errs map[string]string, versionOverride string) (version, c3 string) {
	version = strings.TrimSpace(r.PostFormValue("version"))
	if versionOverride != "" {
		version = versionOverride
	}
	c3 = strings.TrimSpace(r.PostFormValue("c3_version"))
	if _, err := pkg.ParseSemver(version); err != nil {
		errs["version"] = err.Error()
	}
	if msg := pkg.ValidateC3Version(c3); msg != "" {
		errs["c3_version"] = msg
	}
	return version, c3
}

// readMeta reads the description, tags, license and links shared by the new/edit forms.
func readMeta(r *http.Request, errs map[string]string) store.Meta {
	return checkMeta(store.Meta{
		Description: strings.TrimSpace(r.PostFormValue("description")),
		License:     strings.TrimSpace(r.PostFormValue("license")),
		DocsURL:     strings.TrimSpace(r.PostFormValue("docs_url")),
		GitURL:      strings.TrimSpace(r.PostFormValue("git_url")),
	}, r.PostFormValue("tags"), errs)
}

// checkMeta validates package metadata, recording problems in errs, and sets m.Tags from tagsRaw.
func checkMeta(m store.Meta, tagsRaw string, errs map[string]string) store.Meta {
	if n := utf8.RuneCountInString(m.Description); n == 0 || n > pkg.MaxDescription {
		errs["description"] = "Describe the package in up to 280 characters."
	}
	if utf8.RuneCountInString(m.License) > pkg.MaxLicense {
		errs["license"] = "Keep the license under 40 characters, for example MIT."
	}
	if msg := pkg.ValidateURL(m.DocsURL); msg != "" {
		errs["docs_url"] = msg
	}
	if msg := pkg.ValidateURL(m.GitURL); msg != "" {
		errs["git_url"] = msg
	}
	tags, problem := pkg.NormalizeTags(tagsRaw)
	if problem != "" {
		errs["tags"] = problem
	}
	m.Tags = tags
	return m
}

// readReadme reads the optional "readme" upload; ok is false if one was sent but invalid.
func readReadme(r *http.Request, errs map[string]string) (md string, html string, present bool) {
	f, _, err := r.FormFile("readme")
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, pkg.MaxReadmeBytes+1))
	if err != nil {
		errs["readme"] = "Could not read the README."
		return "", "", true
	}
	if msg := pkg.ValidateReadme(b); msg != "" {
		errs["readme"] = msg
		return "", "", true
	}
	return string(b), string(pkg.RenderReadme(string(b))), true
}

func formValues(r *http.Request, keys ...string) map[string]string {
	m := map[string]string{}
	for _, k := range keys {
		m[k] = strings.TrimSpace(r.PostFormValue(k))
	}
	return m
}

func (s *Server) newPackageForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "new_package", pageData{Title: "New package", Data: formData{MaxMB: s.cfg.MaxUploadMB}})
}

// publishOutcome is the result of a successful publish.
type publishOutcome struct {
	Name, Version string
	Created       bool // the package itself was created by this publish
}

// doPublish validates an upload request and publishes it. expectName, if set, must match the
// manifest; newPackage also reads the description, tags, links and README. versionOverride
// replaces the "version" field (used when the version is part of the URL). On failure it returns
// the HTTP status and per-field messages to show.
func (s *Server) doPublish(r *http.Request, expectName, versionOverride string, newPackage bool) (*publishOutcome, map[string]string, int) {
	errs := map[string]string{}
	info, data := s.readUpload(r, errs, expectName)
	version, c3 := checkVersionFields(r, errs, versionOverride)
	var meta store.Meta
	var readmeMD, readmeHTML, readmeSource string
	if newPackage {
		meta = readMeta(r, errs)
		var uploaded bool
		if readmeMD, readmeHTML, uploaded = readReadme(r, errs); uploaded {
			readmeSource = "manual"
		}
	}
	// No README uploaded by hand: use the README.md inside the archive, if it has one.
	if readmeSource == "" && info != nil && info.Readme != nil {
		readmeMD = string(info.Readme)
		readmeHTML = string(pkg.RenderReadme(readmeMD))
		readmeSource = "zip"
	}
	if len(errs) > 0 {
		return nil, errs, http.StatusUnprocessableEntity
	}
	created, err := s.storeVersion(r, info, data, version, c3, meta, readmeMD, readmeHTML, readmeSource)
	switch {
	case errors.Is(err, store.ErrNameTaken), errors.Is(err, store.ErrNotOwner):
		errs["file"] = fmt.Sprintf("The package name %q is already taken. To publish a new version of your own package, use \"Publish update\".", info.Name)
		return nil, errs, http.StatusConflict
	case errors.Is(err, store.ErrVersionExists):
		errs["version"] = fmt.Sprintf("Version %s is already published. Versions are immutable; choose a new version number.", version)
		return nil, errs, http.StatusConflict
	case err != nil:
		log.Printf("publish: %v", err)
		return nil, map[string]string{"form": "internal error"}, http.StatusInternalServerError
	}
	return &publishOutcome{Name: info.Name, Version: version, Created: created}, nil, http.StatusCreated
}

func (s *Server) newPackageSubmit(w http.ResponseWriter, r *http.Request) {
	out, errs, status := s.doPublish(r, "", "", true)
	if out == nil {
		if status == http.StatusInternalServerError {
			http.Error(w, "internal error", status)
			return
		}
		w.WriteHeader(status)
		s.render(w, r, "new_package", pageData{Title: "New package", Errors: errs,
			Form: formValues(r, "version", "c3_version", "description", "tags", "license", "docs_url", "git_url"), Data: formData{MaxMB: s.cfg.MaxUploadMB}})
		return
	}
	http.Redirect(w, r, "/packages/"+out.Name, http.StatusSeeOther)
}

func (s *Server) publishForm(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ownedPackage(w, r)
	if !ok {
		return
	}
	s.render(w, r, "publish", pageData{Title: "Publish " + p.Name, Data: formData{Pkg: p, MaxMB: s.cfg.MaxUploadMB}})
}

func (s *Server) publishSubmit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ownedPackage(w, r)
	if !ok {
		return
	}
	out, errs, status := s.doPublish(r, p.Name, "", false)
	if out == nil {
		if status == http.StatusInternalServerError {
			http.Error(w, "internal error", status)
			return
		}
		w.WriteHeader(status)
		s.render(w, r, "publish", pageData{Title: "Publish " + p.Name, Errors: errs,
			Form: formValues(r, "version", "c3_version"), Data: formData{Pkg: p, MaxMB: s.cfg.MaxUploadMB}})
		return
	}
	http.Redirect(w, r, "/packages/"+p.Name, http.StatusSeeOther)
}

// storeVersion saves the file, then records the version; the file is removed again if the
// database insert fails and nothing else references it.
func (s *Server) storeVersion(r *http.Request, info *pkg.Info, data []byte, version, c3 string, meta store.Meta, readmeMD, readmeHTML, readmeSource string) (bool, error) {
	created, err := s.storage.Put(info.SHA256File, data)
	if err != nil {
		return false, err
	}
	pubCreated, err := s.store.PublishVersion(r.Context(), store.PublishParams{
		UserID: userFrom(r).ID, Name: info.Name, Version: version, C3Version: c3, Targets: info.Targets,
		Size: int64(len(data)), SHA256File: info.SHA256File, SHA256Tree: info.SHA256Tree, StorageKey: info.SHA256File,
		Meta: meta, ReadmeMD: readmeMD, ReadmeHTML: readmeHTML, ReadmeSource: readmeSource,
	})
	if err != nil && created {
		if inUse, uerr := s.store.StorageKeyInUse(r.Context(), info.SHA256File); uerr == nil && !inUse {
			if derr := s.storage.Delete(info.SHA256File); derr != nil && !errors.Is(derr, storage.ErrNotFound) {
				log.Printf("cleanup stored file: %v", derr)
			}
		}
	}
	return pubCreated, err
}

func (s *Server) editForm(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ownedPackage(w, r)
	if !ok {
		return
	}
	form := map[string]string{"description": p.Meta.Description, "license": p.Meta.License, "docs_url": p.Meta.DocsURL,
		"git_url": p.Meta.GitURL, "tags": strings.Join(p.Meta.Tags, ", ")}
	s.render(w, r, "edit_package", pageData{Title: "Edit " + p.Name, Form: form, Flash: r.URL.Query().Get("flash"), Data: formData{Pkg: p}})
}

func (s *Server) editSubmit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ownedPackage(w, r)
	if !ok {
		return
	}
	errs := map[string]string{}
	meta := readMeta(r, errs)
	md, html, hasReadme := readReadme(r, errs)
	if len(errs) > 0 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "edit_package", pageData{Title: "Edit " + p.Name, Errors: errs,
			Form: formValues(r, "description", "license", "docs_url", "git_url", "tags"), Data: formData{Pkg: p}})
		return
	}
	u := userFrom(r)
	err := s.store.UpdateMetadata(r.Context(), p.ID, u.ID, meta)
	if err == nil && hasReadme {
		err = s.store.SetReadme(r.Context(), p.ID, u.ID, md, html)
	}
	if err != nil {
		s.serverError(w, "edit package", err)
		return
	}
	http.Redirect(w, r, "/dashboard/packages/"+p.Name+"/edit?flash=Saved", http.StatusSeeOther)
}

func (s *Server) setYanked(yanked bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.ownedPackage(w, r)
		if !ok {
			return
		}
		err := s.store.SetYanked(r.Context(), p.ID, userFrom(r).ID, r.PathValue("version"), yanked)
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, "yank", err)
			return
		}
		http.Redirect(w, r, "/packages/"+p.Name+"/versions", http.StatusSeeOther)
	}
}

// sparkline scales daily counts to bars in a 300x40 box (10 units per day).
func sparkline(days []int64) ([]sparkBar, int64) {
	var max, total int64
	for _, d := range days {
		total += d
		if d > max {
			max = d
		}
	}
	bars := make([]sparkBar, len(days))
	for i, d := range days {
		h := 2 // a flat baseline keeps empty days visible
		if max > 0 && d > 0 {
			h = int(d*38/max) + 2
		}
		bars[i] = sparkBar{X: i * 10, Y: 40 - h, H: h}
	}
	return bars, total
}
