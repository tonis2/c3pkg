package store

import (
	"context"
	"strconv"
	"strings"
	"unicode"
)

const (
	SortLatest    = "latest"
	SortDownloads = "downloads"
	SortStars     = "stars"
	PageSize      = 20
)

// ListParams selects which packages to list.
type ListParams struct {
	Sort string // SortLatest (default), SortDownloads or SortStars
	Q    string // free-text search over name, description and tags
	Tag  string
	// Installable keeps only packages with a non-yanked version, i.e. those that have a latest.
	Installable bool
	Page        int // 1-based
	PerPage     int
}

// FTSQuery turns free text into a safe FTS5 query: every word becomes a quoted prefix
// term, so user input can never inject FTS operators. It returns "" if nothing searchable remains.
func FTSQuery(q string) string {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) > 8 {
		words = words[:8]
	}
	for i, w := range words {
		words[i] = `"` + w + `"*`
	}
	return strings.Join(words, " ")
}

func (s *Store) ListPackages(ctx context.Context, p ListParams) (list []Summary, total int, err error) {
	if p.PerPage <= 0 || p.PerPage > 100 {
		p.PerPage = PageSize
	}
	if p.Page < 1 {
		p.Page = 1
	}
	where := []string{"p.removed_at IS NULL"}
	var args []any
	from := "packages p JOIN users u ON u.id = p.owner_id"
	order := "p.updated_at DESC, p.id DESC"
	switch p.Sort {
	case SortDownloads:
		order = "p.downloads_total DESC, p.updated_at DESC, p.id DESC"
	case SortStars:
		order = "p.stars_count DESC, p.downloads_total DESC, p.id DESC"
	}
	if p.Installable {
		where = append(where, "EXISTS (SELECT 1 FROM versions v WHERE v.package_id = p.id AND v.yanked_at IS NULL)")
	}
	if p.Tag != "" {
		where = append(where, "EXISTS (SELECT 1 FROM package_tags t WHERE t.package_id = p.id AND t.tag = ?)")
		args = append(args, strings.ToLower(p.Tag))
	}
	if p.Q != "" {
		fts := FTSQuery(p.Q)
		if fts == "" {
			return nil, 0, nil
		}
		where = append(where, "p.id IN (SELECT rowid FROM packages_fts WHERE packages_fts MATCH ?)")
		args = append(args, fts)
		// exact name matches first, then the chosen sort
		order = "(p.name = ?) DESC, " + order
	}
	cond := strings.Join(where, " AND ")
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+from+` WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append([]any{}, args...)
	if p.Q != "" {
		queryArgs = append(queryArgs, strings.ToLower(strings.TrimSpace(p.Q)))
	}
	queryArgs = append(queryArgs, p.PerPage, (p.Page-1)*p.PerPage)
	list, err = s.listSummariesOrdered(ctx, from, cond, order, queryArgs)
	return list, total, err
}

type Stats struct {
	Packages  int64
	Downloads int64
	Users     int64
}

func (s *Store) Stats(ctx context.Context) (st Stats, err error) {
	err = s.DB.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM packages WHERE removed_at IS NULL),
		        (SELECT COALESCE(SUM(downloads_total),0) FROM packages WHERE removed_at IS NULL),
		        (SELECT COUNT(*) FROM users WHERE banned_at IS NULL)`).Scan(&st.Packages, &st.Downloads, &st.Users)
	return
}

type TagCount struct {
	Tag   string
	Count int
}

func (s *Store) PopularTags(ctx context.Context, limit int) ([]TagCount, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT t.tag, COUNT(*) AS n FROM package_tags t JOIN packages p ON p.id = t.package_id
		 WHERE p.removed_at IS NULL GROUP BY t.tag ORDER BY n DESC, t.tag LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Suggest returns up to limit package names/descriptions matching a prefix, for the search box.
func (s *Store) Suggest(ctx context.Context, q string, limit int) ([]Summary, error) {
	list, _, err := s.ListPackages(ctx, ListParams{Q: q, PerPage: limit, Sort: SortDownloads})
	return list, err
}

// ---- stars ----

// SetStar adds or removes a user's star and keeps the cached count in step. It is idempotent.
func (s *Store) SetStar(ctx context.Context, userID, pkgID int64, starred bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var q string
	if starred {
		q = `INSERT OR IGNORE INTO stars(user_id, package_id) VALUES (?, ?)`
	} else {
		q = `DELETE FROM stars WHERE user_id = ? AND package_id = ?`
	}
	if _, err := tx.ExecContext(ctx, q, userID, pkgID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE packages SET stars_count = (SELECT COUNT(*) FROM stars WHERE package_id = ?) WHERE id = ?`, pkgID, pkgID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) IsStarred(ctx context.Context, userID, pkgID int64) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM stars WHERE user_id = ? AND package_id = ?`, userID, pkgID).Scan(&n)
	return n > 0, err
}

// ---- downloads ----

// RecordDownload counts one download of a version, in the daily table and the package total.
func (s *Store) RecordDownload(ctx context.Context, pkgID, versionID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO downloads_daily(version_id, day, count) VALUES (?, strftime('%Y-%m-%d','now'), 1)
		 ON CONFLICT(version_id, day) DO UPDATE SET count = count + 1`, versionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE packages SET downloads_total = downloads_total + 1 WHERE id = ?`, pkgID); err != nil {
		return err
	}
	return tx.Commit()
}

// VersionDownloads returns total downloads per version id for a package.
func (s *Store) VersionDownloads(ctx context.Context, pkgID int64) (map[int64]int64, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT d.version_id, SUM(d.count) FROM downloads_daily d JOIN versions v ON v.id = d.version_id
		 WHERE v.package_id = ? GROUP BY d.version_id`, pkgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// DownloadsLastDays returns the package's daily download counts for the last n days (oldest first, zero-filled).
func (s *Store) DownloadsLastDays(ctx context.Context, pkgID int64, n int) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT CAST(julianday('now','start of day') - julianday(d.day) AS INTEGER) AS ago, SUM(d.count)
		 FROM downloads_daily d JOIN versions v ON v.id = d.version_id
		 WHERE v.package_id = ? AND d.day > strftime('%Y-%m-%d','now', ?) GROUP BY d.day`, pkgID, "-"+strconv.Itoa(n)+" days")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, n)
	for rows.Next() {
		var ago, c int64
		if err := rows.Scan(&ago, &c); err != nil {
			return nil, err
		}
		if ago >= 0 && int(ago) < n {
			out[n-1-int(ago)] = c
		}
	}
	return out, rows.Err()
}

// listSummariesOrdered is the shared list query with a caller-chosen FROM, WHERE and ORDER BY.
// args must end with LIMIT and OFFSET.
func (s *Store) listSummariesOrdered(ctx context.Context, from, where, order string, args []any) ([]Summary, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT p.id, p.name, p.description, p.downloads_total, p.stars_count, p.updated_at, u.login,
		        COALESCE((SELECT GROUP_CONCAT(tag, ',') FROM package_tags WHERE package_id = p.id), '')
		 FROM `+from+` WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []Summary
	var ids []int64
	for rows.Next() {
		var sm Summary
		var id int64
		var tags string
		if err := rows.Scan(&id, &sm.Name, &sm.Description, &sm.Downloads, &sm.Stars, &sm.UpdatedAt, &sm.OwnerLogin, &tags); err != nil {
			rows.Close()
			return nil, err
		}
		sm.Tags = splitTags(tags)
		out = append(out, sm)
		ids = append(ids, id)
	}
	rows.Close() // single-connection pool: finish this query before the next
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		vs, err := s.VersionsOf(ctx, id)
		if err != nil {
			return nil, err
		}
		out[i].Versions = vs
		if l := LatestVersion(vs); l != nil {
			out[i].Latest, out[i].C3Version = l.Version, l.C3Version
		}
	}
	return out, nil
}
