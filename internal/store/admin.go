package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Report struct {
	ID            int64
	PackageName   string
	ReporterLogin string
	Reason        string
	CreatedAt     string
}

// CreateReport files a report. A user can have only one open report per package.
func (s *Store) CreateReport(ctx context.Context, pkgID, reporterID int64, reason string) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO reports(package_id, reporter_id, reason)
		 SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM reports WHERE package_id = ? AND reporter_id = ? AND status = 'open')`,
		pkgID, reporterID, reason, pkgID, reporterID)
	return err
}

func (s *Store) OpenReports(ctx context.Context) ([]Report, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT r.id, p.name, COALESCE(u.login,'(deleted)'), r.reason, r.created_at
		 FROM reports r JOIN packages p ON p.id = r.package_id LEFT JOIN users u ON u.id = r.reporter_id
		 WHERE r.status = 'open' ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.ID, &r.PackageName, &r.ReporterLogin, &r.Reason, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ResolveReport(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE reports SET status = 'resolved' WHERE id = ? AND status = 'open'`, id)
	return err
}

// SetPackageRemoved hides or restores a package. A removed package keeps its name reserved.
func (s *Store) SetPackageRemoved(ctx context.Context, name string, removed bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM packages WHERE name = ?`, strings.ToLower(name)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var removedAt any
	if removed {
		removedAt = ts(time.Now())
	}
	if _, err := tx.ExecContext(ctx, `UPDATE packages SET removed_at = ? WHERE id = ?`, removedAt, id); err != nil {
		return err
	}
	if err := refreshFTS(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetUserBanned bans or unbans a user. Banning ends their sessions and revokes their API tokens.
func (s *Store) SetUserBanned(ctx context.Context, login string, banned bool) error {
	u, err := s.UserByLogin(ctx, login)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if banned {
		now := ts(time.Now())
		if _, err := tx.ExecContext(ctx, `UPDATE users SET banned_at = ? WHERE id = ?`, now, u.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, u.ID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE users SET banned_at = NULL WHERE id = ?`, u.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetAdminByEmail grants or revokes the admin flag.
func (s *Store) SetAdminByEmail(ctx context.Context, email string, admin bool) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE email = ?`, admin, strings.ToLower(email))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type AdminPackage struct {
	Name    string
	Owner   string
	Removed bool
	Updated string
}

// AdminPackages lists packages (including removed ones) whose name contains q, newest first.
func (s *Store) AdminPackages(ctx context.Context, q string, limit int) ([]AdminPackage, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT p.name, u.login, p.removed_at IS NOT NULL, p.updated_at FROM packages p JOIN users u ON u.id = p.owner_id
		 WHERE p.name LIKE ? ESCAPE '\' ORDER BY p.updated_at DESC LIMIT ?`, likePattern(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminPackage
	for rows.Next() {
		var p AdminPackage
		if err := rows.Scan(&p.Name, &p.Owner, &p.Removed, &p.Updated); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AdminUsers lists users whose login, name or email contains q, newest first.
func (s *Store) AdminUsers(ctx context.Context, q string, limit int) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+userCols+` FROM users WHERE login LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\'
		 ORDER BY id DESC LIMIT ?`, likePattern(q), likePattern(q), likePattern(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.TrimSpace(q)) + "%"
}

func (s *Store) PurgeExpired(ctx context.Context) error {
	return s.PurgeExpiredSessions(ctx)
}
