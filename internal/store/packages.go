package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/tonis/c3pkg/internal/pkg"
)

var (
	ErrNameTaken     = errors.New("package name is taken")
	ErrNotOwner      = errors.New("not the package owner")
	ErrVersionExists = errors.New("version already published")
)

type Meta struct {
	Description string
	DocsURL     string
	GitURL      string
	License     string
	Tags        []string
}

type Package struct {
	ID         int64
	Name       string
	OwnerID    int64
	OwnerLogin string
	OwnerName  string
	Meta       Meta
	ReadmeMD   string
	ReadmeHTML string
	Stars      int64
	Downloads  int64
	CreatedAt  string
	UpdatedAt  string
	Removed    bool
}

type Version struct {
	ID         int64
	PackageID  int64
	Version    string
	C3Version  string
	Targets    []string
	Size       int64
	SHA256File string
	SHA256Tree string
	StorageKey string
	Yanked     bool
	CreatedAt  string
}

// Summary is a package as shown in lists.
type Summary struct {
	Name        string
	Description string
	Tags        []string
	Latest      string // empty if every version is yanked
	C3Version   string
	Downloads   int64
	Stars       int64
	UpdatedAt   string
	OwnerLogin  string
}

type PublishParams struct {
	UserID     int64
	Name       string
	Version    string
	C3Version  string
	Targets    []string
	Size       int64
	SHA256File string
	SHA256Tree string
	StorageKey string
	Meta       Meta // applied only when the package is created
	// The README to apply. Empty ReadmeSource means none. "manual" (uploaded by the user) always
	// applies; "zip" (taken from the archive) applies to a new package, and to an existing one
	// unless its owner has uploaded a README by hand.
	ReadmeMD, ReadmeHTML string
	ReadmeSource         string
}

// PublishVersion adds a version, creating the package (owned by UserID) if the name is free.
func (s *Store) PublishVersion(ctx context.Context, p PublishParams) (created bool, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var pkgID, ownerID int64
	var removedAt sql.NullString
	var currentSource string
	err = tx.QueryRowContext(ctx, `SELECT id, owner_id, removed_at, readme_source FROM packages WHERE name = ?`, p.Name).Scan(&pkgID, &ownerID, &removedAt, &currentSource)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, ierr := tx.ExecContext(ctx,
			`INSERT INTO packages(name, name_key, owner_id, description, docs_url, git_url, license, readme_md, readme_html, readme_source)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Name, pkg.NameKey(p.Name), p.UserID, p.Meta.Description, p.Meta.DocsURL, p.Meta.GitURL, p.Meta.License, p.ReadmeMD, p.ReadmeHTML, readmeSource(p.ReadmeSource))
		if ierr != nil {
			if strings.Contains(ierr.Error(), "UNIQUE constraint failed") {
				return false, ErrNameTaken
			}
			return false, ierr
		}
		pkgID, _ = res.LastInsertId()
		created = true
		if err := replaceTags(ctx, tx, pkgID, p.Meta.Tags); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	case removedAt.Valid:
		return false, ErrNameTaken
	case ownerID != p.UserID:
		return false, ErrNotOwner
	default:
		if p.ReadmeSource == "manual" || p.ReadmeSource == "zip" && currentSource != "manual" {
			if _, err := tx.ExecContext(ctx, `UPDATE packages SET readme_md = ?, readme_html = ?, readme_source = ? WHERE id = ?`,
				p.ReadmeMD, p.ReadmeHTML, p.ReadmeSource, pkgID); err != nil {
				return false, err
			}
		}
	}

	targets, _ := json.Marshal(p.Targets)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO versions(package_id, version, c3_version, targets, size_bytes, sha256_file, sha256_tree, storage_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pkgID, p.Version, p.C3Version, string(targets), p.Size, p.SHA256File, p.SHA256Tree, p.StorageKey); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return false, ErrVersionExists
		}
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE packages SET updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id = ?`, pkgID); err != nil {
		return false, err
	}
	if err := refreshFTS(ctx, tx, pkgID); err != nil {
		return false, err
	}
	return created, tx.Commit()
}

func readmeSource(s string) string {
	if s == "" {
		return "zip"
	}
	return s
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func replaceTags(ctx context.Context, x execer, pkgID int64, tags []string) error {
	if _, err := x.ExecContext(ctx, `DELETE FROM package_tags WHERE package_id = ?`, pkgID); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := x.ExecContext(ctx, `INSERT INTO package_tags(package_id, tag) VALUES (?, ?)`, pkgID, t); err != nil {
			return err
		}
	}
	return nil
}

// refreshFTS rewrites the search row for a package (rowid = package id).
func refreshFTS(ctx context.Context, x execer, pkgID int64) error {
	if _, err := x.ExecContext(ctx, `DELETE FROM packages_fts WHERE rowid = ?`, pkgID); err != nil {
		return err
	}
	_, err := x.ExecContext(ctx,
		`INSERT INTO packages_fts(rowid, name, description, tags)
		 SELECT p.id, p.name, p.description, COALESCE((SELECT GROUP_CONCAT(tag, ' ') FROM package_tags WHERE package_id = p.id), '')
		 FROM packages p WHERE p.id = ? AND p.removed_at IS NULL`, pkgID)
	return err
}

const pkgCols = `p.id, p.name, p.owner_id, u.login, u.name, p.description, p.docs_url, p.git_url, p.license,
	p.readme_md, p.readme_html, p.stars_count, p.downloads_total, p.created_at, p.updated_at, p.removed_at IS NOT NULL,
	COALESCE((SELECT GROUP_CONCAT(tag, ',') FROM package_tags WHERE package_id = p.id), '')`

func (s *Store) PackageByName(ctx context.Context, name string) (*Package, error) {
	var p Package
	var tags string
	err := s.DB.QueryRowContext(ctx, `SELECT `+pkgCols+` FROM packages p JOIN users u ON u.id = p.owner_id WHERE p.name = ?`,
		strings.ToLower(name)).Scan(&p.ID, &p.Name, &p.OwnerID, &p.OwnerLogin, &p.OwnerName, &p.Meta.Description, &p.Meta.DocsURL,
		&p.Meta.GitURL, &p.Meta.License, &p.ReadmeMD, &p.ReadmeHTML, &p.Stars, &p.Downloads, &p.CreatedAt, &p.UpdatedAt, &p.Removed, &tags)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Meta.Tags = splitTags(tags)
	return &p, nil
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	t := strings.Split(s, ",")
	sort.Strings(t)
	return t
}

const versionCols = `id, package_id, version, c3_version, targets, size_bytes, sha256_file, sha256_tree, storage_key, yanked_at IS NOT NULL, created_at`

func scanVersion(r scanner) (*Version, error) {
	var v Version
	var targets string
	if err := r.Scan(&v.ID, &v.PackageID, &v.Version, &v.C3Version, &targets, &v.Size, &v.SHA256File, &v.SHA256Tree, &v.StorageKey, &v.Yanked, &v.CreatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(targets), &v.Targets)
	return &v, nil
}

// VersionsOf returns every version of a package, newest first by semver.
func (s *Store) VersionsOf(ctx context.Context, pkgID int64) ([]Version, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+versionCols+` FROM versions WHERE package_id = ?`, pkgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortVersions(out)
	return out, nil
}

func sortVersions(vs []Version) {
	sort.SliceStable(vs, func(i, j int) bool {
		a, errA := pkg.ParseSemver(vs[i].Version)
		b, errB := pkg.ParseSemver(vs[j].Version)
		if errA != nil || errB != nil {
			return vs[i].Version > vs[j].Version
		}
		return a.Compare(b) > 0
	})
}

// LatestVersion picks the highest non-yanked stable version, falling back to the highest
// non-yanked prerelease. vs must already be sorted newest first. It returns nil if none qualify.
func LatestVersion(vs []Version) *Version {
	var pre *Version
	for i := range vs {
		if vs[i].Yanked {
			continue
		}
		if sv, err := pkg.ParseSemver(vs[i].Version); err == nil && sv.IsPrerelease() {
			if pre == nil {
				pre = &vs[i]
			}
			continue
		}
		return &vs[i]
	}
	return pre
}

func (s *Store) VersionByNum(ctx context.Context, pkgID int64, version string) (*Version, error) {
	v, err := scanVersion(s.DB.QueryRowContext(ctx, `SELECT `+versionCols+` FROM versions WHERE package_id = ? AND version = ?`, pkgID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

// PackagesByOwner lists a user's live packages, most recently updated first.
func (s *Store) PackagesByOwner(ctx context.Context, ownerID int64) ([]Summary, error) {
	return s.listSummaries(ctx, `p.owner_id = ? AND p.removed_at IS NULL`, ownerID)
}

func (s *Store) listSummaries(ctx context.Context, where string, args ...any) ([]Summary, error) {
	return s.listSummariesOrdered(ctx, "packages p JOIN users u ON u.id = p.owner_id", where, "p.updated_at DESC, p.id DESC",
		append(args, 1000, 0))
}

// UpdateMetadata changes a package's description, links, license and tags. Only the owner may.
func (s *Store) UpdateMetadata(ctx context.Context, pkgID, userID int64, m Meta) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE packages SET description = ?, docs_url = ?, git_url = ?, license = ? WHERE id = ? AND owner_id = ? AND removed_at IS NULL`,
		m.Description, m.DocsURL, m.GitURL, m.License, pkgID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotOwner
	}
	if err := replaceTags(ctx, tx, pkgID, m.Tags); err != nil {
		return err
	}
	if err := refreshFTS(ctx, tx, pkgID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetReadme stores a README uploaded by the owner; later publishes will not replace it.
func (s *Store) SetReadme(ctx context.Context, pkgID, userID int64, md, html string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE packages SET readme_md = ?, readme_html = ?, readme_source = 'manual' WHERE id = ? AND owner_id = ? AND removed_at IS NULL`, md, html, pkgID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotOwner
	}
	return nil
}

// SetYanked hides (or restores) a version for new installs; the file stays downloadable by exact version.
func (s *Store) SetYanked(ctx context.Context, pkgID, userID int64, version string, yanked bool) error {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE versions SET yanked_at = CASE WHEN ? THEN strftime('%Y-%m-%dT%H:%M:%SZ','now') ELSE NULL END
		 WHERE package_id = ? AND version = ? AND EXISTS (SELECT 1 FROM packages WHERE id = ? AND owner_id = ? AND removed_at IS NULL)`,
		yanked, pkgID, version, pkgID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// StorageKeyInUse reports whether any version still references a stored file.
func (s *Store) StorageKeyInUse(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM versions WHERE storage_key = ?`, key).Scan(&n)
	return n > 0, err
}

// CreatedAtTime parses CreatedAt; the zero time is returned if it is malformed.
func (v Version) CreatedAtTime() time.Time {
	t, _ := time.Parse(time.RFC3339, v.CreatedAt)
	return t
}
