package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/gen/sqlc"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	w *sql.DB
	r *sql.DB
}

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
	defer sqlTx.Rollback()
	if err := fn(&tx{ctx: ctx, q: sqlc.New(sqlTx)}); err != nil {
		return err
	}
	return sqlTx.Commit()
}

type tx struct {
	ctx context.Context
	q   *sqlc.Queries
}

func noRow(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return app.ErrNoRow
	}
	return err
}

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

func intOr0(p *int64) int {
	if p == nil {
		return 0
	}
	return int(*p)
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
