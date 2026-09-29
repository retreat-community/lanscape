package store

import "context"

// InternetCheck is one Internet test result of an exit (observation point and interface).
type InternetCheck struct {
	ID        int64   `json:"id"`
	TS        int64   `json:"ts"`
	Point     string  `json:"point"` // agent id or "server"
	Dev       string  `json:"dev,omitempty"`
	Gateway   string  `json:"gateway,omitempty"`
	OK        bool    `json:"ok"`
	Error     string  `json:"error,omitempty"`
	PublicIP  string  `json:"public_ip,omitempty"`
	LatencyMS float64 `json:"latency_ms"`
	DownMbps  float64 `json:"down_mbps"`
}

// AddInternetCheck stores a result.
func (s *Store) AddInternetCheck(ctx context.Context, c InternetCheck) (int64, error) {
	return s.Insert(ctx, `INSERT INTO internet_checks(ts, point, dev, gateway, ok, error, public_ip, latency_ms, down_mbps)
		VALUES (?,?,?,?,?,?,?,?,?)`, c.TS, c.Point, c.Dev, c.Gateway, b2i(c.OK), c.Error, c.PublicIP, c.LatencyMS, c.DownMbps)
}

// InternetChecks returns results since a time, oldest first.
func (s *Store) InternetChecks(ctx context.Context, since int64) ([]InternetCheck, error) {
	rows, err := s.Query(ctx, `SELECT id, ts, point, dev, gateway, ok, error, public_ip, latency_ms, down_mbps
		FROM internet_checks WHERE ts >= ? ORDER BY ts, id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InternetCheck{}
	for rows.Next() {
		var c InternetCheck
		var ok int
		if err := rows.Scan(&c.ID, &c.TS, &c.Point, &c.Dev, &c.Gateway, &ok, &c.Error, &c.PublicIP, &c.LatencyMS, &c.DownMbps); err != nil {
			return nil, err
		}
		c.OK = ok != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneInternetChecks deletes results older than before.
func (s *Store) PruneInternetChecks(ctx context.Context, before int64) error {
	_, err := s.Exec(ctx, `DELETE FROM internet_checks WHERE ts < ?`, before)
	return err
}

// PruneDaily deletes daily uptime aggregates before the day key (YYYYMMDD).
func (s *Store) PruneDaily(ctx context.Context, beforeDay int) error {
	_, err := s.Exec(ctx, `DELETE FROM uptime_daily WHERE day < ?`, beforeDay)
	return err
}
