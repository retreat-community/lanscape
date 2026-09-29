// Package store persists server state in SQLite (default, WAL mode) or PostgreSQL through
// the same interface. Migrations live in code and run on open.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver (pure Go)
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Dialect identifies the SQL flavour.
type Dialect int

// Supported dialects.
const (
	SQLite Dialect = iota
	Postgres
)

// Store wraps the database.
type Store struct {
	DB      *sql.DB
	Dialect Dialect
}

// Open opens a database. dsn is a file path for SQLite or a postgres:// URL.
func Open(ctx context.Context, dsn string) (*Store, error) {
	s := &Store{}
	var err error
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		s.Dialect = Postgres
		s.DB, err = sql.Open(postgresDriver, dsn)
	} else {
		s.Dialect = SQLite
		s.DB, err = sql.Open("sqlite", "file:"+dsn+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"+
			"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)")
		if err == nil {
			s.DB.SetMaxOpenConns(1)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if err := s.DB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

// q rewrites '?' placeholders for PostgreSQL.
func (s *Store) q(query string) string {
	if s.Dialect != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Exec runs a statement.
func (s *Store) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.DB.ExecContext(ctx, s.q(query), args...)
}

// Query runs a query.
func (s *Store) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.DB.QueryContext(ctx, s.q(query), args...)
}

// QueryRow runs a single-row query.
func (s *Store) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.DB.QueryRowContext(ctx, s.q(query), args...)
}

// Insert runs an INSERT and returns the generated id.
func (s *Store) Insert(ctx context.Context, query string, args ...any) (int64, error) {
	if s.Dialect == Postgres {
		var id int64
		err := s.DB.QueryRowContext(ctx, s.q(query)+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	r, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func now() int64 { return time.Now().UnixMilli() }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Backup writes a consistent copy of the SQLite database to path.
func (s *Store) Backup(ctx context.Context, path string) error {
	if s.Dialect != SQLite {
		return errors.New("store: use pg_dump for PostgreSQL")
	}
	_, err := s.DB.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

// Restore replaces the SQLite database at dst with src (the server must be stopped).
func Restore(src, dst string) error {
	check, err := sql.Open("sqlite", "file:"+src+"?mode=ro")
	if err != nil {
		return err
	}
	var res string
	err = check.QueryRow(`PRAGMA integrity_check`).Scan(&res)
	check.Close()
	if err != nil || res != "ok" {
		return fmt.Errorf("store: %s is not a valid database: %v %s", src, err, res)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dst + suffix)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
