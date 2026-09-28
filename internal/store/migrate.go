package store

import (
	"context"
	"fmt"
	"strings"
)

// migrations are applied in order; never edit an applied migration, append a new one.
var migrations = []string{
	// 1: core schema
	`CREATE TABLE users (
		id {{PK}},
		username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL,
		totp_secret TEXT NOT NULL DEFAULT '',
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL
	);
	CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at BIGINT NOT NULL,
		created_at BIGINT NOT NULL,
		ip TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE api_tokens (
		id {{PK}},
		user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		role TEXT NOT NULL,
		created_at BIGINT NOT NULL,
		last_used BIGINT NOT NULL DEFAULT 0,
		expires_at BIGINT NOT NULL DEFAULT 0
	);
	CREATE TABLE agent_tokens (
		id {{PK}},
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		labels TEXT NOT NULL DEFAULT '{}',
		reusable INTEGER NOT NULL DEFAULT 0,
		uses INTEGER NOT NULL DEFAULT 0,
		expires_at BIGINT NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL
	);
	CREATE TABLE agents (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		hostname TEXT NOT NULL DEFAULT '',
		host_id TEXT NOT NULL DEFAULT '',
		labels TEXT NOT NULL DEFAULT '{}',
		version TEXT NOT NULL DEFAULT '',
		arch TEXT NOT NULL DEFAULT '',
		os TEXT NOT NULL DEFAULT '',
		cert_fingerprint TEXT NOT NULL DEFAULT '',
		cert_not_after BIGINT NOT NULL DEFAULT 0,
		first_seen BIGINT NOT NULL,
		last_seen BIGINT NOT NULL,
		inventory TEXT NOT NULL DEFAULT '{}',
		config TEXT NOT NULL DEFAULT '{}'
	);
	CREATE TABLE segment_config (
		segment_id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		expected_mbps INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE runs (
		id {{PK}},
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		started BIGINT NOT NULL,
		finished BIGINT NOT NULL DEFAULT 0,
		params TEXT NOT NULL DEFAULT '{}',
		report TEXT NOT NULL DEFAULT '{}'
	);
	CREATE INDEX runs_started ON runs(started);
	CREATE TABLE schedules (
		id {{PK}},
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		spec TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		params TEXT NOT NULL DEFAULT '{}',
		last_run BIGINT NOT NULL DEFAULT 0
	);
	CREATE TABLE settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE audit (
		id {{PK}},
		ts BIGINT NOT NULL,
		actor TEXT NOT NULL,
		action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX audit_ts ON audit(ts);
	CREATE TABLE known_macs (
		mac TEXT PRIMARY KEY,
		ip TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		first_seen BIGINT NOT NULL,
		last_seen BIGINT NOT NULL
	);`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	var v int
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
		return fmt.Errorf("store: schema version: %w", err)
	}
	pk := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.Dialect == Postgres {
		pk = "BIGSERIAL PRIMARY KEY"
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range strings.Split(strings.ReplaceAll(migrations[i], "{{PK}}", pk), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("store: migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO schema_version(version) VALUES (?)`), i+1); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Migrations returns the number of known migrations (for tests and diagnostics).
func Migrations() int { return len(migrations) }
