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
	// 2: services, discovery, monitors, incidents, notifications
	`CREATE TABLE findings (
		agent_id TEXT NOT NULL,
		source TEXT NOT NULL,
		item_key TEXT NOT NULL,
		kind TEXT NOT NULL,
		data TEXT NOT NULL,
		first_seen BIGINT NOT NULL,
		last_seen BIGINT NOT NULL,
		gone BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (agent_id, source, item_key)
	);
	CREATE TABLE found_state (
		card_key TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		service_id BIGINT NOT NULL DEFAULT 0,
		rule TEXT NOT NULL DEFAULT '',
		first_seen BIGINT NOT NULL,
		updated BIGINT NOT NULL
	);
	CREATE TABLE services (
		id {{PK}},
		name TEXT NOT NULL,
		app_id TEXT NOT NULL DEFAULT '',
		icon TEXT NOT NULL DEFAULT '',
		category TEXT NOT NULL DEFAULT '',
		grp TEXT NOT NULL DEFAULT '',
		internal_url TEXT NOT NULL DEFAULT '',
		external_url TEXT NOT NULL DEFAULT '',
		addresses TEXT NOT NULL DEFAULT '[]',
		card_key TEXT NOT NULL DEFAULT '',
		tile INTEGER NOT NULL DEFAULT 1,
		sort INTEGER NOT NULL DEFAULT 0,
		notes TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	);
	CREATE TABLE changes (
		id {{PK}},
		ts BIGINT NOT NULL,
		kind TEXT NOT NULL,
		subject TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT '',
		agent_id TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX changes_ts ON changes(ts);
	CREATE TABLE monitors (
		id {{PK}},
		service_id BIGINT NOT NULL DEFAULT 0,
		name TEXT NOT NULL,
		spec TEXT NOT NULL,
		interval_s INTEGER NOT NULL DEFAULT 60,
		retries INTEGER NOT NULL DEFAULT 3,
		points TEXT NOT NULL DEFAULT '[]',
		min_failing INTEGER NOT NULL DEFAULT 1,
		sla REAL NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL DEFAULT 'pending',
		last_check BIGINT NOT NULL DEFAULT 0,
		last_latency REAL NOT NULL DEFAULT 0,
		last_message TEXT NOT NULL DEFAULT '',
		cert_not_after BIGINT NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL
	);
	CREATE TABLE checks (
		monitor_id BIGINT NOT NULL,
		ts BIGINT NOT NULL,
		status TEXT NOT NULL,
		latency_ms REAL NOT NULL DEFAULT 0,
		point TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX checks_monitor_ts ON checks(monitor_id, ts);
	CREATE TABLE uptime_daily (
		monitor_id BIGINT NOT NULL,
		day INTEGER NOT NULL,
		up INTEGER NOT NULL DEFAULT 0,
		down INTEGER NOT NULL DEFAULT 0,
		degraded INTEGER NOT NULL DEFAULT 0,
		maint INTEGER NOT NULL DEFAULT 0,
		latency_sum REAL NOT NULL DEFAULT 0,
		PRIMARY KEY (monitor_id, day)
	);
	CREATE TABLE incidents (
		id {{PK}},
		monitor_id BIGINT NOT NULL,
		opened BIGINT NOT NULL,
		closed BIGINT NOT NULL DEFAULT 0,
		cause TEXT NOT NULL DEFAULT '',
		acked_by TEXT NOT NULL DEFAULT '',
		acked_at BIGINT NOT NULL DEFAULT 0,
		parent_id BIGINT NOT NULL DEFAULT 0,
		maintenance INTEGER NOT NULL DEFAULT 0,
		notified BIGINT NOT NULL DEFAULT 0
	);
	CREATE INDEX incidents_monitor ON incidents(monitor_id, closed);
	CREATE TABLE incident_notes (
		id {{PK}},
		incident_id BIGINT NOT NULL,
		ts BIGINT NOT NULL,
		author TEXT NOT NULL,
		text TEXT NOT NULL
	);
	CREATE TABLE maintenance (
		id {{PK}},
		name TEXT NOT NULL,
		starts BIGINT NOT NULL,
		ends BIGINT NOT NULL,
		monitors TEXT NOT NULL DEFAULT '[]',
		created_by TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	);
	CREATE TABLE channels (
		id {{PK}},
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		config TEXT NOT NULL DEFAULT '{}',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at BIGINT NOT NULL
	);`,
	// 3: heartbeat monitors, dependencies and suppressed incidents
	`ALTER TABLE monitors ADD COLUMN push_token TEXT NOT NULL DEFAULT '';
	ALTER TABLE monitors ADD COLUMN last_push BIGINT NOT NULL DEFAULT 0;
	ALTER TABLE monitors ADD COLUMN parents TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE incidents ADD COLUMN suppressed INTEGER NOT NULL DEFAULT 0`,
	// 4: status pages
	`CREATE TABLE status_pages (
		id {{PK}},
		slug TEXT NOT NULL UNIQUE,
		title TEXT NOT NULL,
		public INTEGER NOT NULL DEFAULT 0,
		token TEXT NOT NULL DEFAULT '',
		domain TEXT NOT NULL DEFAULT '',
		config TEXT NOT NULL DEFAULT '{}',
		created_at BIGINT NOT NULL
	)`,
	// 5: web push subscriptions (PWA)
	`CREATE TABLE push_subscriptions (
		id {{PK}},
		user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		endpoint TEXT NOT NULL UNIQUE,
		p256dh TEXT NOT NULL,
		auth TEXT NOT NULL,
		user_agent TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	)`,
	// 6: users signed in through OpenID Connect
	`ALTER TABLE users ADD COLUMN oidc_subject TEXT NOT NULL DEFAULT '';
	CREATE INDEX users_oidc ON users(oidc_subject)`,
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
