package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// User is a panel account.
type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`
	TOTPSecret   string `json:"-"`
	TOTPEnabled  bool   `json:"totp_enabled"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    int64  `json:"created_at"`
}

const userCols = `id, username, password_hash, role, totp_secret, disabled, created_at`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	var dis int
	err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &dis, &u.CreatedAt)
	u.Disabled = dis != 0
	u.TOTPEnabled = u.TOTPSecret != ""
	return u, err
}

// CreateUser inserts a user.
func (s *Store) CreateUser(ctx context.Context, u User) (int64, error) {
	return s.Insert(ctx, `INSERT INTO users(username, password_hash, role, totp_secret, disabled, created_at) VALUES (?,?,?,?,?,?)`,
		u.Username, u.PasswordHash, u.Role, u.TOTPSecret, b2i(u.Disabled), now())
}

// UpdateUser saves role, password hash, TOTP secret and disabled flag.
func (s *Store) UpdateUser(ctx context.Context, u User) error {
	_, err := s.Exec(ctx, `UPDATE users SET password_hash=?, role=?, totp_secret=?, disabled=? WHERE id=?`,
		u.PasswordHash, u.Role, u.TOTPSecret, b2i(u.Disabled), u.ID)
	return err
}

// DeleteUser removes a user and its sessions and tokens.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// UserByName finds a user by username.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	u, err := scanUser(s.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE username=?`, name))
	return u, notFound(err)
}

// UserByID finds a user by id.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
	return u, notFound(err)
}

// Users lists all users.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
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
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of users.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// Session is a login session (id is a hash of the cookie value).
type Session struct {
	ID        string
	UserID    int64
	ExpiresAt int64
}

// CreateSession stores a session.
func (s *Store) CreateSession(ctx context.Context, id string, userID, expires int64, ip, ua string) error {
	_, err := s.Exec(ctx, `INSERT INTO sessions(id, user_id, expires_at, created_at, ip, user_agent) VALUES (?,?,?,?,?,?)`,
		id, userID, expires, now(), ip, ua)
	return err
}

// SessionUser returns the user of a live session.
func (s *Store) SessionUser(ctx context.Context, id string) (User, error) {
	u, err := scanUser(s.QueryRow(ctx, `SELECT u.id, u.username, u.password_hash, u.role, u.totp_secret, u.disabled, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id=? AND s.expires_at > ?`, id, now()))
	return u, notFound(err)
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.Exec(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now())
	return err
}

// APIToken is a bearer token.
type APIToken struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	CreatedAt int64  `json:"created_at"`
	LastUsed  int64  `json:"last_used"`
	ExpiresAt int64  `json:"expires_at"`
}

// CreateAPIToken stores a token hash.
func (s *Store) CreateAPIToken(ctx context.Context, t APIToken, hash string) (int64, error) {
	return s.Insert(ctx, `INSERT INTO api_tokens(user_id, name, token_hash, role, created_at, expires_at) VALUES (?,?,?,?,?,?)`,
		t.UserID, t.Name, hash, t.Role, now(), t.ExpiresAt)
}

// APITokenByHash resolves a bearer token.
func (s *Store) APITokenByHash(ctx context.Context, hash string) (APIToken, User, error) {
	var t APIToken
	row := s.QueryRow(ctx, `SELECT t.id, t.user_id, t.name, t.role, t.created_at, t.last_used, t.expires_at,
		u.id, u.username, u.password_hash, u.role, u.totp_secret, u.disabled, u.created_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash=?`, hash)
	var u User
	var dis int
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Role, &t.CreatedAt, &t.LastUsed, &t.ExpiresAt,
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &dis, &u.CreatedAt)
	u.Disabled = dis != 0
	if err != nil {
		return t, u, notFound(err)
	}
	_, _ = s.Exec(ctx, `UPDATE api_tokens SET last_used=? WHERE id=?`, now(), t.ID)
	return t, u, nil
}

// APITokens lists tokens of a user (all users when userID is 0).
func (s *Store) APITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.Query(ctx, `SELECT id, user_id, name, role, created_at, last_used, expires_at FROM api_tokens
		WHERE ? = 0 OR user_id = ? ORDER BY id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Role, &t.CreatedAt, &t.LastUsed, &t.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken removes a token owned by userID (any owner when userID is 0).
func (s *Store) DeleteAPIToken(ctx context.Context, id, userID int64) error {
	_, err := s.Exec(ctx, `DELETE FROM api_tokens WHERE id=? AND (? = 0 OR user_id = ?)`, id, userID, userID)
	return err
}

// AgentToken is a registration token.
type AgentToken struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	Reusable  bool              `json:"reusable"`
	Uses      int               `json:"uses"`
	ExpiresAt int64             `json:"expires_at"`
	CreatedAt int64             `json:"created_at"`
}

// CreateAgentToken stores a registration token hash.
func (s *Store) CreateAgentToken(ctx context.Context, t AgentToken, hash string) (int64, error) {
	lb, _ := json.Marshal(t.Labels)
	return s.Insert(ctx, `INSERT INTO agent_tokens(name, token_hash, labels, reusable, expires_at, created_at) VALUES (?,?,?,?,?,?)`,
		t.Name, hash, string(lb), b2i(t.Reusable), t.ExpiresAt, now())
}

// UseAgentToken validates a registration token and consumes single-use tokens.
func (s *Store) UseAgentToken(ctx context.Context, hash string) (AgentToken, error) {
	var t AgentToken
	var lb string
	var re int
	err := s.QueryRow(ctx, `SELECT id, name, labels, reusable, uses, expires_at, created_at FROM agent_tokens WHERE token_hash=?`, hash).
		Scan(&t.ID, &t.Name, &lb, &re, &t.Uses, &t.ExpiresAt, &t.CreatedAt)
	if err != nil {
		return t, notFound(err)
	}
	t.Reusable = re != 0
	_ = json.Unmarshal([]byte(lb), &t.Labels)
	if (t.ExpiresAt != 0 && t.ExpiresAt < now()) || (!t.Reusable && t.Uses > 0) {
		return t, ErrNotFound
	}
	_, err = s.Exec(ctx, `UPDATE agent_tokens SET uses = uses + 1 WHERE id=?`, t.ID)
	return t, err
}

// AgentTokens lists registration tokens.
func (s *Store) AgentTokens(ctx context.Context) ([]AgentToken, error) {
	rows, err := s.Query(ctx, `SELECT id, name, labels, reusable, uses, expires_at, created_at FROM agent_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentToken{}
	for rows.Next() {
		var t AgentToken
		var lb string
		var re int
		if err := rows.Scan(&t.ID, &t.Name, &lb, &re, &t.Uses, &t.ExpiresAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Reusable = re != 0
		_ = json.Unmarshal([]byte(lb), &t.Labels)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAgentToken removes a registration token.
func (s *Store) DeleteAgentToken(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `DELETE FROM agent_tokens WHERE id=?`, id)
	return err
}

// Agent is a registered (full) or connected (lite) agent.
type Agent struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"` // full or lite
	Hostname        string            `json:"hostname"`
	HostID          string            `json:"host_id"`
	Labels          map[string]string `json:"labels"`
	Version         string            `json:"version"`
	Arch            string            `json:"arch"`
	OS              string            `json:"os"`
	CertFingerprint string            `json:"cert_fingerprint,omitempty"`
	CertNotAfter    int64             `json:"cert_not_after,omitempty"`
	FirstSeen       int64             `json:"first_seen"`
	LastSeen        int64             `json:"last_seen"`
	Inventory       json.RawMessage   `json:"inventory"`
	Config          json.RawMessage   `json:"config"`
}

const agentCols = `id, name, kind, hostname, host_id, labels, version, arch, os, cert_fingerprint, cert_not_after, first_seen, last_seen, inventory, config`

func scanAgent(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var lb, inv, cfg string
	err := sc.Scan(&a.ID, &a.Name, &a.Kind, &a.Hostname, &a.HostID, &lb, &a.Version, &a.Arch, &a.OS,
		&a.CertFingerprint, &a.CertNotAfter, &a.FirstSeen, &a.LastSeen, &inv, &cfg)
	_ = json.Unmarshal([]byte(lb), &a.Labels)
	a.Inventory, a.Config = json.RawMessage(inv), json.RawMessage(cfg)
	return a, err
}

// UpsertAgent creates or updates an agent row.
func (s *Store) UpsertAgent(ctx context.Context, a Agent) error {
	lb, _ := json.Marshal(a.Labels)
	if len(a.Inventory) == 0 {
		a.Inventory = json.RawMessage(`{}`)
	}
	if len(a.Config) == 0 {
		a.Config = json.RawMessage(`{}`)
	}
	t := now()
	if a.FirstSeen == 0 {
		a.FirstSeen = t
	}
	if a.LastSeen == 0 {
		a.LastSeen = t
	}
	_, err := s.Exec(ctx, `INSERT INTO agents(`+agentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, hostname=excluded.hostname,
		host_id=excluded.host_id, labels=excluded.labels, version=excluded.version, arch=excluded.arch, os=excluded.os,
		cert_fingerprint=excluded.cert_fingerprint, cert_not_after=excluded.cert_not_after, last_seen=excluded.last_seen,
		inventory=excluded.inventory, config=excluded.config`,
		a.ID, a.Name, a.Kind, a.Hostname, a.HostID, string(lb), a.Version, a.Arch, a.OS, a.CertFingerprint,
		a.CertNotAfter, a.FirstSeen, a.LastSeen, string(a.Inventory), string(a.Config))
	return err
}

// AgentByID returns one agent.
func (s *Store) AgentByID(ctx context.Context, id string) (Agent, error) {
	a, err := scanAgent(s.QueryRow(ctx, `SELECT `+agentCols+` FROM agents WHERE id=?`, id))
	return a, notFound(err)
}

// Agents lists all agents.
func (s *Store) Agents(ctx context.Context) ([]Agent, error) {
	rows, err := s.Query(ctx, `SELECT `+agentCols+` FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAgent removes an agent.
func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	_, err := s.Exec(ctx, `DELETE FROM agents WHERE id=?`, id)
	return err
}

// SegmentConfig holds manual settings of a segment.
type SegmentConfig struct {
	SegmentID    string `json:"segment_id"`
	Name         string `json:"name"`
	ExpectedMbps int    `json:"expected_mbps"`
}

// SetSegmentConfig stores manual segment settings.
func (s *Store) SetSegmentConfig(ctx context.Context, c SegmentConfig) error {
	_, err := s.Exec(ctx, `INSERT INTO segment_config(segment_id, name, expected_mbps) VALUES (?,?,?)
		ON CONFLICT(segment_id) DO UPDATE SET name=excluded.name, expected_mbps=excluded.expected_mbps`,
		c.SegmentID, c.Name, c.ExpectedMbps)
	return err
}

// SegmentConfigs returns all manual segment settings.
func (s *Store) SegmentConfigs(ctx context.Context) (map[string]SegmentConfig, error) {
	rows, err := s.Query(ctx, `SELECT segment_id, name, expected_mbps FROM segment_config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SegmentConfig{}
	for rows.Next() {
		var c SegmentConfig
		if err := rows.Scan(&c.SegmentID, &c.Name, &c.ExpectedMbps); err != nil {
			return nil, err
		}
		out[c.SegmentID] = c
	}
	return out, rows.Err()
}

// Run is a stored test run.
type Run struct {
	ID       int64           `json:"id"`
	Kind     string          `json:"kind"`
	Status   string          `json:"status"`
	Started  int64           `json:"started"`
	Finished int64           `json:"finished"`
	Params   json.RawMessage `json:"params"`
	Report   json.RawMessage `json:"report,omitempty"`
}

// CreateRun inserts a run.
func (s *Store) CreateRun(ctx context.Context, r Run) (int64, error) {
	if len(r.Params) == 0 {
		r.Params = json.RawMessage(`{}`)
	}
	return s.Insert(ctx, `INSERT INTO runs(kind, status, started, params, report) VALUES (?,?,?,?,'{}')`,
		r.Kind, r.Status, r.Started, string(r.Params))
}

// FinishRun stores the report.
func (s *Store) FinishRun(ctx context.Context, id int64, status string, report []byte) error {
	_, err := s.Exec(ctx, `UPDATE runs SET status=?, finished=?, report=? WHERE id=?`, status, now(), string(report), id)
	return err
}

// RunByID loads a run including its report.
func (s *Store) RunByID(ctx context.Context, id int64) (Run, error) {
	var r Run
	var p, rep string
	err := s.QueryRow(ctx, `SELECT id, kind, status, started, finished, params, report FROM runs WHERE id=?`, id).
		Scan(&r.ID, &r.Kind, &r.Status, &r.Started, &r.Finished, &p, &rep)
	r.Params, r.Report = json.RawMessage(p), json.RawMessage(rep)
	return r, notFound(err)
}

// LastRun returns the latest finished run of a kind ("" for any).
func (s *Store) LastRun(ctx context.Context, kind string) (Run, error) {
	var id int64
	err := s.QueryRow(ctx, `SELECT id FROM runs WHERE status <> 'running' AND (? = '' OR kind = ?) ORDER BY id DESC LIMIT 1`,
		kind, kind).Scan(&id)
	if err != nil {
		return Run{}, notFound(err)
	}
	return s.RunByID(ctx, id)
}

// Runs lists runs without reports, newest first.
func (s *Store) Runs(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.Query(ctx, `SELECT id, kind, status, started, finished, params FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var p string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Status, &r.Started, &r.Finished, &p); err != nil {
			return nil, err
		}
		r.Params = json.RawMessage(p)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneRuns deletes runs older than the cutoff (unix ms).
func (s *Store) PruneRuns(ctx context.Context, before int64) (int64, error) {
	r, err := s.Exec(ctx, `DELETE FROM runs WHERE started < ?`, before)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// Schedule is a recurring run.
type Schedule struct {
	ID      int64           `json:"id"`
	Name    string          `json:"name"`
	Kind    string          `json:"kind"` // full, reachability, aggregate
	Spec    string          `json:"spec"` // "every 5m" or "daily 02:00"
	Enabled bool            `json:"enabled"`
	Params  json.RawMessage `json:"params"`
	LastRun int64           `json:"last_run"`
}

// SaveSchedule creates (ID 0) or updates a schedule.
func (s *Store) SaveSchedule(ctx context.Context, sc Schedule) (int64, error) {
	if len(sc.Params) == 0 {
		sc.Params = json.RawMessage(`{}`)
	}
	if sc.ID == 0 {
		return s.Insert(ctx, `INSERT INTO schedules(name, kind, spec, enabled, params, last_run) VALUES (?,?,?,?,?,?)`,
			sc.Name, sc.Kind, sc.Spec, b2i(sc.Enabled), string(sc.Params), sc.LastRun)
	}
	_, err := s.Exec(ctx, `UPDATE schedules SET name=?, kind=?, spec=?, enabled=?, params=?, last_run=? WHERE id=?`,
		sc.Name, sc.Kind, sc.Spec, b2i(sc.Enabled), string(sc.Params), sc.LastRun, sc.ID)
	return sc.ID, err
}

// Schedules lists schedules.
func (s *Store) Schedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.Query(ctx, `SELECT id, name, kind, spec, enabled, params, last_run FROM schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		var sc Schedule
		var en int
		var p string
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Kind, &sc.Spec, &en, &p, &sc.LastRun); err != nil {
			return nil, err
		}
		sc.Enabled, sc.Params = en != 0, json.RawMessage(p)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// DeleteSchedule removes a schedule.
func (s *Store) DeleteSchedule(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `DELETE FROM schedules WHERE id=?`, id)
	return err
}

// GetSetting loads a JSON setting into v; missing keys leave v unchanged.
func (s *Store) GetSetting(ctx context.Context, key string, v any) error {
	var raw string
	err := s.QueryRow(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

// SetSetting stores v as JSON.
func (s *Store) SetSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `INSERT INTO settings(key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, string(b))
	return err
}

// AuditEntry is one audit log record.
type AuditEntry struct {
	ID     int64  `json:"id"`
	TS     int64  `json:"ts"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

// Audit appends an audit entry.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.Exec(ctx, `INSERT INTO audit(ts, actor, action, target, result, detail) VALUES (?,?,?,?,?,?)`,
		now(), e.Actor, e.Action, e.Target, e.Result, e.Detail)
	return err
}

// AuditLog returns the latest entries.
func (s *Store) AuditLog(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.Query(ctx, `SELECT id, ts, actor, action, target, result, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Action, &e.Target, &e.Result, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// TouchMAC records a MAC address and reports whether it was new.
func (s *Store) TouchMAC(ctx context.Context, mac, ip, name, source string) (bool, error) {
	r, err := s.Exec(ctx, `UPDATE known_macs SET ip=?, name=?, source=?, last_seen=? WHERE mac=?`, ip, name, source, now(), mac)
	if err != nil {
		return false, err
	}
	if n, _ := r.RowsAffected(); n > 0 {
		return false, nil
	}
	t := now()
	_, err = s.Exec(ctx, `INSERT INTO known_macs(mac, ip, name, source, first_seen, last_seen) VALUES (?,?,?,?,?,?)`,
		mac, ip, name, source, t, t)
	return err == nil, err
}

// KnownMACs returns all recorded MAC addresses.
func (s *Store) KnownMACs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.Query(ctx, `SELECT mac FROM known_macs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out[m] = true
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
