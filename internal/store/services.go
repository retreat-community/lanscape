package store

import (
	"context"
	"encoding/json"
	"strings"
)

// --- discovery findings ---

// Finding is one discovered item reported by an agent source.
type Finding struct {
	AgentID   string          `json:"agent_id"`
	Source    string          `json:"source"`
	Key       string          `json:"key"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
	FirstSeen int64           `json:"first_seen"`
	LastSeen  int64           `json:"last_seen"`
	Gone      int64           `json:"gone,omitempty"`
}

const findingCols = `agent_id, source, item_key, kind, data, first_seen, last_seen, gone`

func scanFinding(sc interface{ Scan(...any) error }) (Finding, error) {
	var f Finding
	var data string
	err := sc.Scan(&f.AgentID, &f.Source, &f.Key, &f.Kind, &data, &f.FirstSeen, &f.LastSeen, &f.Gone)
	f.Data = json.RawMessage(data)
	return f, err
}

// Findings returns findings, optionally of one agent and source.
func (s *Store) Findings(ctx context.Context, agentID, source string) ([]Finding, error) {
	q := `SELECT ` + findingCols + ` FROM findings`
	var args []any
	if agentID != "" {
		q += ` WHERE agent_id=? AND source=?`
		args = append(args, agentID, source)
	}
	rows, err := s.Query(ctx, q+` ORDER BY agent_id, source, item_key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ReplaceFindings stores the current items of one agent source: new and changed items are
// upserted, missing ones are marked gone.
func (s *Store) ReplaceFindings(ctx context.Context, agentID, source string, items []Finding, ts int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	keep := map[string]bool{}
	for _, f := range items {
		keep[f.Key] = true
		_, err := tx.ExecContext(ctx, s.q(`INSERT INTO findings(`+findingCols+`) VALUES (?,?,?,?,?,?,?,0)
			ON CONFLICT(agent_id, source, item_key) DO UPDATE SET kind=excluded.kind, data=excluded.data,
			last_seen=excluded.last_seen, gone=0`), agentID, source, f.Key, f.Kind, string(f.Data), ts, ts)
		if err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, s.q(`SELECT item_key FROM findings WHERE agent_id=? AND source=? AND gone=0`), agentID, source)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		if !keep[k] {
			gone = append(gone, k)
		}
	}
	rows.Close()
	for _, k := range gone {
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE findings SET gone=? WHERE agent_id=? AND source=? AND item_key=?`),
			ts, agentID, source, k); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneFindings deletes items gone before the cut-off and all items of deleted agents.
func (s *Store) PruneFindings(ctx context.Context, before int64) error {
	// "server" owns findings imported by the server itself (Prometheus, Home Assistant)
	_, err := s.Exec(ctx, `DELETE FROM findings WHERE (gone > 0 AND gone < ?) OR (agent_id <> 'server' AND agent_id NOT IN (SELECT id FROM agents))`,
		before)
	return err
}

// FoundState is the triage status of a discovered card.
type FoundState struct {
	Key       string `json:"key"`
	Status    string `json:"status"` // new, added, ignored, hidden
	ServiceID int64  `json:"service_id,omitempty"`
	Rule      string `json:"rule,omitempty"`
	FirstSeen int64  `json:"first_seen"`
	Updated   int64  `json:"updated"`
}

// FoundStates returns all card states by key.
func (s *Store) FoundStates(ctx context.Context) (map[string]FoundState, error) {
	rows, err := s.Query(ctx, `SELECT card_key, status, service_id, rule, first_seen, updated FROM found_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]FoundState{}
	for rows.Next() {
		var f FoundState
		if err := rows.Scan(&f.Key, &f.Status, &f.ServiceID, &f.Rule, &f.FirstSeen, &f.Updated); err != nil {
			return nil, err
		}
		m[f.Key] = f
	}
	return m, rows.Err()
}

// SetFoundState inserts or updates a card state (first_seen is kept).
func (s *Store) SetFoundState(ctx context.Context, f FoundState) error {
	t := now()
	if f.FirstSeen == 0 {
		f.FirstSeen = t
	}
	_, err := s.Exec(ctx, `INSERT INTO found_state(card_key, status, service_id, rule, first_seen, updated) VALUES (?,?,?,?,?,?)
		ON CONFLICT(card_key) DO UPDATE SET status=excluded.status, service_id=excluded.service_id, rule=excluded.rule,
		updated=excluded.updated`, f.Key, f.Status, f.ServiceID, f.Rule, f.FirstSeen, t)
	return err
}

// --- services ---

// Address is one way to reach a service.
type Address struct {
	Type  string `json:"type"` // internal, external, hostport, workload, container, process
	Value string `json:"value"`
	Agent string `json:"agent,omitempty"`
}

// Service is a catalog entry (a dashboard tile).
type Service struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	AppID       string    `json:"app_id"`
	Icon        string    `json:"icon"`
	Category    string    `json:"category"`
	Group       string    `json:"group"`
	InternalURL string    `json:"internal_url"`
	ExternalURL string    `json:"external_url"`
	Addresses   []Address `json:"addresses"`
	CardKey     string    `json:"card_key,omitempty"`
	Tile        bool      `json:"tile"`
	Sort        int       `json:"sort"`
	Notes       string    `json:"notes"`
	CreatedAt   int64     `json:"created_at"`
}

const serviceCols = `id, name, app_id, icon, category, grp, internal_url, external_url, addresses, card_key, tile, sort, notes, created_at`

func scanService(sc interface{ Scan(...any) error }) (Service, error) {
	var v Service
	var addrs string
	var tile int
	err := sc.Scan(&v.ID, &v.Name, &v.AppID, &v.Icon, &v.Category, &v.Group, &v.InternalURL, &v.ExternalURL, &addrs,
		&v.CardKey, &tile, &v.Sort, &v.Notes, &v.CreatedAt)
	v.Tile = tile != 0
	_ = json.Unmarshal([]byte(addrs), &v.Addresses)
	if v.Addresses == nil {
		v.Addresses = []Address{}
	}
	return v, err
}

// SaveService inserts (ID 0) or updates a service.
func (s *Store) SaveService(ctx context.Context, v Service) (int64, error) {
	if v.Addresses == nil {
		v.Addresses = []Address{}
	}
	addrs, _ := json.Marshal(v.Addresses)
	if v.ID == 0 {
		return s.Insert(ctx, `INSERT INTO services(name, app_id, icon, category, grp, internal_url, external_url, addresses,
			card_key, tile, sort, notes, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.Name, v.AppID, v.Icon, v.Category,
			v.Group, v.InternalURL, v.ExternalURL, string(addrs), v.CardKey, b2i(v.Tile), v.Sort, v.Notes, now())
	}
	_, err := s.Exec(ctx, `UPDATE services SET name=?, app_id=?, icon=?, category=?, grp=?, internal_url=?, external_url=?,
		addresses=?, card_key=?, tile=?, sort=?, notes=? WHERE id=?`, v.Name, v.AppID, v.Icon, v.Category, v.Group,
		v.InternalURL, v.ExternalURL, string(addrs), v.CardKey, b2i(v.Tile), v.Sort, v.Notes, v.ID)
	return v.ID, err
}

// ServiceByID returns one service.
func (s *Store) ServiceByID(ctx context.Context, id int64) (Service, error) {
	v, err := scanService(s.QueryRow(ctx, `SELECT `+serviceCols+` FROM services WHERE id=?`, id))
	return v, notFound(err)
}

// Services lists the catalog ordered for the dashboard.
func (s *Store) Services(ctx context.Context) ([]Service, error) {
	rows, err := s.Query(ctx, `SELECT `+serviceCols+` FROM services ORDER BY grp, sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Service{}
	for rows.Next() {
		v, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetServiceOrder moves a service to a group and position.
func (s *Store) SetServiceOrder(ctx context.Context, id int64, group string, sort int) error {
	_, err := s.Exec(ctx, `UPDATE services SET grp=?, sort=? WHERE id=?`, group, sort, id)
	return err
}

// DeleteService removes a service, its monitors and returns its card to the queue.
func (s *Store) DeleteService(ctx context.Context, id int64) error {
	if _, err := s.Exec(ctx, `UPDATE found_state SET status='new', service_id=0 WHERE service_id=?`, id); err != nil {
		return err
	}
	if _, err := s.Exec(ctx, `UPDATE monitors SET service_id=0 WHERE service_id=?`, id); err != nil {
		return err
	}
	_, err := s.Exec(ctx, `DELETE FROM services WHERE id=?`, id)
	return err
}

// --- change feed ---

// Change is an entry in the change feed.
type Change struct {
	ID      int64  `json:"id"`
	TS      int64  `json:"ts"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Detail  string `json:"detail,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

// AddChange appends to the change feed.
func (s *Store) AddChange(ctx context.Context, c Change) (int64, error) {
	if c.TS == 0 {
		c.TS = now()
	}
	return s.Insert(ctx, `INSERT INTO changes(ts, kind, subject, detail, agent_id) VALUES (?,?,?,?,?)`,
		c.TS, c.Kind, c.Subject, c.Detail, c.AgentID)
}

// Changes returns the newest changes older than before (0 = now).
func (s *Store) Changes(ctx context.Context, before int64, limit int) ([]Change, error) {
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := s.Query(ctx, `SELECT id, ts, kind, subject, detail, agent_id FROM changes WHERE id < ? ORDER BY id DESC LIMIT ?`,
		before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.ID, &c.TS, &c.Kind, &c.Subject, &c.Detail, &c.AgentID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- monitors ---

// Monitor is a stored availability check.
type Monitor struct {
	ID           int64           `json:"id"`
	ServiceID    int64           `json:"service_id"`
	Name         string          `json:"name"`
	Spec         json.RawMessage `json:"spec"`
	IntervalS    int             `json:"interval_s"`
	Retries      int             `json:"retries"`
	Points       []string        `json:"points"`
	MinFailing   int             `json:"min_failing"`
	SLA          float64         `json:"sla"`
	Enabled      bool            `json:"enabled"`
	Status       string          `json:"status"`
	LastCheck    int64           `json:"last_check"`
	LastLatency  float64         `json:"last_latency"`
	LastMessage  string          `json:"last_message"`
	CertNotAfter int64           `json:"cert_not_after,omitempty"`
	CreatedAt    int64           `json:"created_at"`
	PushToken    string          `json:"push_token,omitempty"` // heartbeat monitors
	LastPush     int64           `json:"last_push,omitempty"`
	Parents      []int64         `json:"parents"` // manual dependencies: incidents are suppressed while a parent is down
}

const monitorCols = `id, service_id, name, spec, interval_s, retries, points, min_failing, sla, enabled, status, last_check,
	last_latency, last_message, cert_not_after, created_at, push_token, last_push, parents`

func scanMonitor(sc interface{ Scan(...any) error }) (Monitor, error) {
	var m Monitor
	var spec, points, parents string
	var en int
	err := sc.Scan(&m.ID, &m.ServiceID, &m.Name, &spec, &m.IntervalS, &m.Retries, &points, &m.MinFailing, &m.SLA, &en,
		&m.Status, &m.LastCheck, &m.LastLatency, &m.LastMessage, &m.CertNotAfter, &m.CreatedAt, &m.PushToken, &m.LastPush, &parents)
	m.Spec = json.RawMessage(spec)
	m.Enabled = en != 0
	_ = json.Unmarshal([]byte(points), &m.Points)
	if m.Points == nil {
		m.Points = []string{}
	}
	_ = json.Unmarshal([]byte(parents), &m.Parents)
	if m.Parents == nil {
		m.Parents = []int64{}
	}
	return m, err
}

// SaveMonitor inserts (ID 0) or updates the configuration of a monitor.
func (s *Store) SaveMonitor(ctx context.Context, m Monitor) (int64, error) {
	if m.Points == nil {
		m.Points = []string{}
	}
	if m.Parents == nil {
		m.Parents = []int64{}
	}
	points, _ := json.Marshal(m.Points)
	parents, _ := json.Marshal(m.Parents)
	if m.ID == 0 {
		return s.Insert(ctx, `INSERT INTO monitors(service_id, name, spec, interval_s, retries, points, min_failing, sla, enabled,
			status, created_at, push_token, parents) VALUES (?,?,?,?,?,?,?,?,?,'pending',?,?,?)`, m.ServiceID, m.Name, string(m.Spec),
			m.IntervalS, m.Retries, string(points), m.MinFailing, m.SLA, b2i(m.Enabled), now(), m.PushToken, string(parents))
	}
	_, err := s.Exec(ctx, `UPDATE monitors SET service_id=?, name=?, spec=?, interval_s=?, retries=?, points=?, min_failing=?,
		sla=?, enabled=?, push_token=?, parents=? WHERE id=?`, m.ServiceID, m.Name, string(m.Spec), m.IntervalS, m.Retries,
		string(points), m.MinFailing, m.SLA, b2i(m.Enabled), m.PushToken, string(parents), m.ID)
	return m.ID, err
}

// SetMonitorState stores the latest check outcome.
func (s *Store) SetMonitorState(ctx context.Context, m Monitor) error {
	_, err := s.Exec(ctx, `UPDATE monitors SET status=?, last_check=?, last_latency=?, last_message=?, cert_not_after=? WHERE id=?`,
		m.Status, m.LastCheck, m.LastLatency, m.LastMessage, m.CertNotAfter, m.ID)
	return err
}

// SetMonitorPush records a heartbeat.
func (s *Store) SetMonitorPush(ctx context.Context, id, ts int64) error {
	_, err := s.Exec(ctx, `UPDATE monitors SET last_push=? WHERE id=?`, ts, id)
	return err
}

// MonitorByID returns one monitor.
func (s *Store) MonitorByID(ctx context.Context, id int64) (Monitor, error) {
	m, err := scanMonitor(s.QueryRow(ctx, `SELECT `+monitorCols+` FROM monitors WHERE id=?`, id))
	return m, notFound(err)
}

// Monitors lists monitors.
func (s *Store) Monitors(ctx context.Context) ([]Monitor, error) {
	rows, err := s.Query(ctx, `SELECT `+monitorCols+` FROM monitors ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Monitor{}
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMonitor removes a monitor with its history.
func (s *Store) DeleteMonitor(ctx context.Context, id int64) error {
	for _, q := range []string{`DELETE FROM checks WHERE monitor_id=?`, `DELETE FROM uptime_daily WHERE monitor_id=?`,
		`DELETE FROM incident_notes WHERE incident_id IN (SELECT id FROM incidents WHERE monitor_id=?)`,
		`DELETE FROM incidents WHERE monitor_id=?`, `DELETE FROM monitors WHERE id=?`} {
		if _, err := s.Exec(ctx, q, id); err != nil {
			return err
		}
	}
	return nil
}

// CheckRecord is one stored check result.
type CheckRecord struct {
	MonitorID int64   `json:"-"`
	TS        int64   `json:"ts"`
	Status    string  `json:"status"`
	LatencyMS float64 `json:"latency_ms"`
	Point     string  `json:"point,omitempty"`
	Message   string  `json:"message,omitempty"`
}

// AddCheck stores a check result and updates the daily uptime aggregate.
func (s *Store) AddCheck(ctx context.Context, c CheckRecord, day int, maint bool) error {
	if _, err := s.Exec(ctx, `INSERT INTO checks(monitor_id, ts, status, latency_ms, point, message) VALUES (?,?,?,?,?,?)`,
		c.MonitorID, c.TS, c.Status, c.LatencyMS, c.Point, c.Message); err != nil {
		return err
	}
	col := "down"
	switch {
	case maint:
		col = "maint"
	case c.Status == "up":
		col = "up"
	case c.Status == "degraded":
		col = "degraded"
	}
	lat := 0.0
	if col == "up" || col == "degraded" {
		lat = c.LatencyMS
	}
	_, err := s.Exec(ctx, `INSERT INTO uptime_daily(monitor_id, day, `+col+`, latency_sum) VALUES (?,?,1,?)
		ON CONFLICT(monitor_id, day) DO UPDATE SET `+col+`=uptime_daily.`+col+`+1, latency_sum=uptime_daily.latency_sum+excluded.latency_sum`,
		c.MonitorID, day, lat)
	return err
}

// Checks returns check results of a monitor since ts, oldest first.
func (s *Store) Checks(ctx context.Context, monitorID, since int64, limit int) ([]CheckRecord, error) {
	rows, err := s.Query(ctx, `SELECT ts, status, latency_ms, point, message FROM checks WHERE monitor_id=? AND ts>=?
		ORDER BY ts DESC LIMIT ?`, monitorID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CheckRecord{}
	for rows.Next() {
		var c CheckRecord
		if err := rows.Scan(&c.TS, &c.Status, &c.LatencyMS, &c.Point, &c.Message); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// PruneChecks removes raw check results older than the cut-off (daily aggregates stay).
func (s *Store) PruneChecks(ctx context.Context, before int64) error {
	_, err := s.Exec(ctx, `DELETE FROM checks WHERE ts < ?`, before)
	if err == nil {
		_, err = s.Exec(ctx, `DELETE FROM changes WHERE ts < ?`, before)
	}
	return err
}

// DayStat is the daily uptime aggregate.
type DayStat struct {
	Day        int     `json:"day"` // yyyymmdd
	Up         int     `json:"up"`
	Down       int     `json:"down"`
	Degraded   int     `json:"degraded"`
	Maint      int     `json:"maint"`
	LatencySum float64 `json:"-"`
}

// DayStats returns aggregates of a monitor from day (yyyymmdd) on.
func (s *Store) DayStats(ctx context.Context, monitorID int64, fromDay int) ([]DayStat, error) {
	rows, err := s.Query(ctx, `SELECT day, up, down, degraded, maint, latency_sum FROM uptime_daily
		WHERE monitor_id=? AND day>=? ORDER BY day`, monitorID, fromDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayStat{}
	for rows.Next() {
		var d DayStat
		if err := rows.Scan(&d.Day, &d.Up, &d.Down, &d.Degraded, &d.Maint, &d.LatencySum); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// --- incidents ---

// Incident is a period of unavailability of a monitor.
type Incident struct {
	ID          int64          `json:"id"`
	MonitorID   int64          `json:"monitor_id"`
	Opened      int64          `json:"opened"`
	Closed      int64          `json:"closed,omitempty"`
	Cause       string         `json:"cause"`
	AckedBy     string         `json:"acked_by,omitempty"`
	AckedAt     int64          `json:"acked_at,omitempty"`
	ParentID    int64          `json:"parent_id,omitempty"`
	Maintenance bool           `json:"maintenance"`
	Suppressed  bool           `json:"suppressed"` // a parent (monitor or agent) was down: not notified
	Notified    int64          `json:"notified,omitempty"`
	Notes       []IncidentNote `json:"notes,omitempty"`
}

// IncidentNote is a comment on an incident.
type IncidentNote struct {
	ID     int64  `json:"id"`
	TS     int64  `json:"ts"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

const incidentCols = `id, monitor_id, opened, closed, cause, acked_by, acked_at, parent_id, maintenance, notified, suppressed`

func scanIncident(sc interface{ Scan(...any) error }) (Incident, error) {
	var i Incident
	var m, sup int
	err := sc.Scan(&i.ID, &i.MonitorID, &i.Opened, &i.Closed, &i.Cause, &i.AckedBy, &i.AckedAt, &i.ParentID, &m, &i.Notified, &sup)
	i.Maintenance, i.Suppressed = m != 0, sup != 0
	return i, err
}

// OpenIncident creates an incident.
func (s *Store) OpenIncident(ctx context.Context, i Incident) (int64, error) {
	return s.Insert(ctx, `INSERT INTO incidents(monitor_id, opened, cause, parent_id, maintenance, suppressed) VALUES (?,?,?,?,?,?)`,
		i.MonitorID, i.Opened, i.Cause, i.ParentID, b2i(i.Maintenance), b2i(i.Suppressed))
}

// Unsuppress marks an incident as no longer caused by a parent.
func (s *Store) Unsuppress(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `UPDATE incidents SET suppressed=0, parent_id=0 WHERE id=?`, id)
	return err
}

// CloseIncident closes an incident.
func (s *Store) CloseIncident(ctx context.Context, id, ts int64) error {
	_, err := s.Exec(ctx, `UPDATE incidents SET closed=? WHERE id=?`, ts, id)
	return err
}

// AckIncident acknowledges an incident.
func (s *Store) AckIncident(ctx context.Context, id int64, user string) error {
	_, err := s.Exec(ctx, `UPDATE incidents SET acked_by=?, acked_at=? WHERE id=? AND acked_at=0`, user, now(), id)
	return err
}

// SetIncidentNotified records when notifications were last sent.
func (s *Store) SetIncidentNotified(ctx context.Context, id, ts int64) error {
	_, err := s.Exec(ctx, `UPDATE incidents SET notified=? WHERE id=?`, ts, id)
	return err
}

// IncidentByID returns an incident with its notes.
func (s *Store) IncidentByID(ctx context.Context, id int64) (Incident, error) {
	i, err := scanIncident(s.QueryRow(ctx, `SELECT `+incidentCols+` FROM incidents WHERE id=?`, id))
	if err != nil {
		return i, notFound(err)
	}
	rows, err := s.Query(ctx, `SELECT id, ts, author, text FROM incident_notes WHERE incident_id=? ORDER BY ts`, id)
	if err != nil {
		return i, err
	}
	defer rows.Close()
	for rows.Next() {
		var n IncidentNote
		if err := rows.Scan(&n.ID, &n.TS, &n.Author, &n.Text); err != nil {
			return i, err
		}
		i.Notes = append(i.Notes, n)
	}
	return i, rows.Err()
}

// Incidents lists incidents: open only, or all newer than since (and of one monitor when monitorID > 0).
func (s *Store) Incidents(ctx context.Context, openOnly bool, monitorID int64, since int64, limit int) ([]Incident, error) {
	var where []string
	var args []any
	if openOnly {
		where = append(where, "closed=0")
	} else if since > 0 {
		where = append(where, "(closed=0 OR opened>=?)")
		args = append(args, since)
	}
	if monitorID > 0 {
		where = append(where, "monitor_id=?")
		args = append(args, monitorID)
	}
	q := `SELECT ` + incidentCols + ` FROM incidents`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	rows, err := s.Query(ctx, q+` ORDER BY opened DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// AddIncidentNote appends a comment.
func (s *Store) AddIncidentNote(ctx context.Context, incidentID int64, author, text string) (int64, error) {
	return s.Insert(ctx, `INSERT INTO incident_notes(incident_id, ts, author, text) VALUES (?,?,?,?)`, incidentID, now(), author, text)
}

// --- maintenance windows ---

// Maintenance is a planned window during which notifications are muted.
type Maintenance struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Starts    int64   `json:"starts"`
	Ends      int64   `json:"ends"`
	Monitors  []int64 `json:"monitors"` // empty = all
	CreatedBy string  `json:"created_by"`
	CreatedAt int64   `json:"created_at"`
}

// SaveMaintenance inserts a window.
func (s *Store) SaveMaintenance(ctx context.Context, m Maintenance) (int64, error) {
	if m.Monitors == nil {
		m.Monitors = []int64{}
	}
	mon, _ := json.Marshal(m.Monitors)
	if m.ID > 0 {
		_, err := s.Exec(ctx, `UPDATE maintenance SET name=?, starts=?, ends=?, monitors=? WHERE id=?`, m.Name, m.Starts, m.Ends,
			string(mon), m.ID)
		return m.ID, err
	}
	return s.Insert(ctx, `INSERT INTO maintenance(name, starts, ends, monitors, created_by, created_at) VALUES (?,?,?,?,?,?)`,
		m.Name, m.Starts, m.Ends, string(mon), m.CreatedBy, now())
}

// Maintenances lists windows ending after ts.
func (s *Store) Maintenances(ctx context.Context, endsAfter int64) ([]Maintenance, error) {
	rows, err := s.Query(ctx, `SELECT id, name, starts, ends, monitors, created_by, created_at FROM maintenance WHERE ends > ?
		ORDER BY starts`, endsAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Maintenance{}
	for rows.Next() {
		var m Maintenance
		var mon string
		if err := rows.Scan(&m.ID, &m.Name, &m.Starts, &m.Ends, &mon, &m.CreatedBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(mon), &m.Monitors)
		if m.Monitors == nil {
			m.Monitors = []int64{}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMaintenance removes a window.
func (s *Store) DeleteMaintenance(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `DELETE FROM maintenance WHERE id=?`, id)
	return err
}

// --- notification channels ---

// Channel is a notification target; Config holds type-specific settings including secrets.
type Channel struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Config    json.RawMessage `json:"config"`
	Enabled   bool            `json:"enabled"`
	CreatedAt int64           `json:"created_at"`
}

// SaveChannel inserts (ID 0) or updates a channel.
func (s *Store) SaveChannel(ctx context.Context, c Channel) (int64, error) {
	if len(c.Config) == 0 {
		c.Config = json.RawMessage("{}")
	}
	if c.ID == 0 {
		return s.Insert(ctx, `INSERT INTO channels(name, type, config, enabled, created_at) VALUES (?,?,?,?,?)`,
			c.Name, c.Type, string(c.Config), b2i(c.Enabled), now())
	}
	_, err := s.Exec(ctx, `UPDATE channels SET name=?, type=?, config=?, enabled=? WHERE id=?`, c.Name, c.Type, string(c.Config),
		b2i(c.Enabled), c.ID)
	return c.ID, err
}

// Channels lists channels.
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.Query(ctx, `SELECT id, name, type, config, enabled, created_at FROM channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		var cfg string
		var en int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &cfg, &en, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Config, c.Enabled = json.RawMessage(cfg), en != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChannelByID returns one channel.
func (s *Store) ChannelByID(ctx context.Context, id int64) (Channel, error) {
	var c Channel
	var cfg string
	var en int
	err := s.QueryRow(ctx, `SELECT id, name, type, config, enabled, created_at FROM channels WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.Type, &cfg, &en, &c.CreatedAt)
	c.Config, c.Enabled = json.RawMessage(cfg), en != 0
	return c, notFound(err)
}

// DeleteChannel removes a channel.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `DELETE FROM channels WHERE id=?`, id)
	return err
}

// --- status pages ---

// StatusGroup is a titled list of monitors on a status page.
type StatusGroup struct {
	Name     string  `json:"name"`
	Monitors []int64 `json:"monitors"`
}

// StatusConfig is the content and theme of a status page.
type StatusConfig struct {
	Description string        `json:"description,omitempty"`
	Groups      []StatusGroup `json:"groups"`
	Accent      string        `json:"accent,omitempty"` // CSS colour
	LogoURL     string        `json:"logo_url,omitempty"`
	Theme       string        `json:"theme,omitempty"` // auto, light, dark
	Footer      string        `json:"footer,omitempty"`
}

// StatusPage is a public (or link-only) status page.
type StatusPage struct {
	ID        int64        `json:"id"`
	Slug      string       `json:"slug"`
	Title     string       `json:"title"`
	Public    bool         `json:"public"`
	Token     string       `json:"token,omitempty"` // required in ?t= when not public
	Domain    string       `json:"domain,omitempty"`
	Config    StatusConfig `json:"config"`
	CreatedAt int64        `json:"created_at"`
}

const statusCols = `id, slug, title, public, token, domain, config, created_at`

func scanStatusPage(sc interface{ Scan(...any) error }) (StatusPage, error) {
	var p StatusPage
	var pub int
	var cfg string
	err := sc.Scan(&p.ID, &p.Slug, &p.Title, &pub, &p.Token, &p.Domain, &cfg, &p.CreatedAt)
	p.Public = pub != 0
	_ = json.Unmarshal([]byte(cfg), &p.Config)
	if p.Config.Groups == nil {
		p.Config.Groups = []StatusGroup{}
	}
	return p, err
}

// SaveStatusPage inserts (ID 0) or updates a status page.
func (s *Store) SaveStatusPage(ctx context.Context, p StatusPage) (int64, error) {
	cfg, _ := json.Marshal(p.Config)
	if p.ID == 0 {
		return s.Insert(ctx, `INSERT INTO status_pages(slug, title, public, token, domain, config, created_at) VALUES (?,?,?,?,?,?,?)`,
			p.Slug, p.Title, b2i(p.Public), p.Token, p.Domain, string(cfg), now())
	}
	_, err := s.Exec(ctx, `UPDATE status_pages SET slug=?, title=?, public=?, token=?, domain=?, config=? WHERE id=?`,
		p.Slug, p.Title, b2i(p.Public), p.Token, p.Domain, string(cfg), p.ID)
	return p.ID, err
}

// StatusPages lists status pages.
func (s *Store) StatusPages(ctx context.Context) ([]StatusPage, error) {
	rows, err := s.Query(ctx, `SELECT `+statusCols+` FROM status_pages ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatusPage{}
	for rows.Next() {
		p, err := scanStatusPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// StatusPageBy finds a page by id, slug or domain (the first non-empty one).
func (s *Store) StatusPageBy(ctx context.Context, id int64, slug, domain string) (StatusPage, error) {
	var row interface{ Scan(...any) error }
	switch {
	case id > 0:
		row = s.QueryRow(ctx, `SELECT `+statusCols+` FROM status_pages WHERE id=?`, id)
	case slug != "":
		row = s.QueryRow(ctx, `SELECT `+statusCols+` FROM status_pages WHERE slug=?`, slug)
	default:
		row = s.QueryRow(ctx, `SELECT `+statusCols+` FROM status_pages WHERE domain=? AND domain<>''`, domain)
	}
	p, err := scanStatusPage(row)
	return p, notFound(err)
}

// DeleteStatusPage removes a status page.
func (s *Store) DeleteStatusPage(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `DELETE FROM status_pages WHERE id=?`, id)
	return err
}

// --- web push ---

// PushSubscription is a browser subscribed to notifications.
type PushSubscription struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Endpoint  string `json:"endpoint"`
	P256dh    string `json:"-"`
	Auth      string `json:"-"`
	UserAgent string `json:"user_agent"`
	CreatedAt int64  `json:"created_at"`
}

// SavePushSubscription inserts or refreshes a subscription (by endpoint).
func (s *Store) SavePushSubscription(ctx context.Context, p PushSubscription) error {
	_, err := s.Exec(ctx, `INSERT INTO push_subscriptions(user_id, endpoint, p256dh, auth, user_agent, created_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET user_id=excluded.user_id, p256dh=excluded.p256dh, auth=excluded.auth,
		user_agent=excluded.user_agent`, p.UserID, p.Endpoint, p.P256dh, p.Auth, p.UserAgent, now())
	return err
}

// PushSubscriptions lists subscriptions (of one user when userID > 0).
func (s *Store) PushSubscriptions(ctx context.Context, userID int64) ([]PushSubscription, error) {
	q := `SELECT id, user_id, endpoint, p256dh, auth, user_agent, created_at FROM push_subscriptions`
	var args []any
	if userID > 0 {
		q += ` WHERE user_id=?`
		args = append(args, userID)
	}
	rows, err := s.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushSubscription{}
	for rows.Next() {
		var p PushSubscription
		if err := rows.Scan(&p.ID, &p.UserID, &p.Endpoint, &p.P256dh, &p.Auth, &p.UserAgent, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePushSubscription removes a subscription by endpoint.
func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) error {
	_, err := s.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint=?`, endpoint)
	return err
}
