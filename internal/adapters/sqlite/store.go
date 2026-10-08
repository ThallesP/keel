// Package sqlite implements app.Store on SQLite (modernc.org/sqlite, pure Go). Schema in
// migrations/, queries in queries/ (sqlc → db/). See docs/go/ARCHITECTURE.md, "Data".
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ThallesP/keel/internal/adapters/sqlite/db"
	"github.com/ThallesP/keel/internal/app"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store: one write connection (SQLite has one writer; BEGIN IMMEDIATE takes the lock up front)
// and a pool of readers (WAL lets them run beside the writer).
type Store struct {
	w *sql.DB
	r *sql.DB
}

var _ app.Store = (*Store)(nil)

// Open opens (creating if needed) the database at path and applies pending migrations. ":memory:"
// is not supported (readers and the writer must share the file); tests use a temp file.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := func(lock string) string {
		return "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)" +
			"&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=" + lock
	}
	w, err := sql.Open("sqlite", dsn("immediate"))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", dsn("deferred"))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)
	s := &Store{w: w, r: r}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return errors.Join(s.w.Close(), s.r.Close())
}

// DB is the writer, for tooling (import, tests).
func (s *Store) DB() *sql.DB { return s.w }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var n int
		if err := s.w.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.w.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			name, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Read(ctx context.Context, fn func(app.Tx) error) error {
	return s.run(ctx, s.r, &sql.TxOptions{ReadOnly: true}, fn)
}

func (s *Store) Write(ctx context.Context, fn func(app.Tx) error) error {
	return s.run(ctx, s.w, nil, fn)
}

func (s *Store) run(ctx context.Context, pool *sql.DB, opts *sql.TxOptions, fn func(app.Tx) error) error {
	sqlTx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	t := &tx{ctx: ctx, q: db.New(sqlTx), sql: sqlTx}
	if err := fn(t); err != nil {
		sqlTx.Rollback()
		return err
	}
	return sqlTx.Commit()
}

// tx implements app.Tx. Area methods live in <area>.go next to this file.
type tx struct {
	ctx context.Context
	q   *db.Queries
	sql *sql.Tx
}

var _ app.Tx = (*tx)(nil)

// noRow maps sql.ErrNoRows to app.ErrNoRow and passes anything else through.
func noRow(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return app.ErrNoRow
	}
	return err
}

// IsUniqueViolation: a UNIQUE or PRIMARY KEY constraint failed (map it to NAME_TAKEN & co).
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Small conversions shared by the area files.

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func ptrInt(p *int64) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func ptrInt64(p *int) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
