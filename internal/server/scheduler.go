package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/store"
)

// ParseSpec parses "every 5m", "hourly" or "daily 02:00" and returns the next run after last.
func ParseSpec(spec string, last, now time.Time) (time.Time, error) {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(spec)))
	switch {
	case len(f) == 2 && f[0] == "every":
		d, err := time.ParseDuration(f[1])
		if err != nil || d < time.Minute {
			return time.Time{}, fmt.Errorf("invalid interval %q (minimum 1m)", f[1])
		}
		if last.IsZero() {
			return now, nil
		}
		return last.Add(d), nil
	case len(f) == 1 && f[0] == "hourly":
		return ParseSpec("every 1h", last, now)
	case len(f) == 2 && f[0] == "daily":
		t, err := time.ParseInLocation("15:04", f[1], now.Location())
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid time %q", f[1])
		}
		base := last
		if base.IsZero() {
			base = now.Add(-24 * time.Hour)
		}
		next := time.Date(base.Year(), base.Month(), base.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		for !next.After(base) {
			next = next.Add(24 * time.Hour)
		}
		return next, nil
	}
	return time.Time{}, fmt.Errorf("invalid schedule %q: use \"every 5m\", \"hourly\" or \"daily 02:00\"", spec)
}

func (s *Server) scheduler(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runDue(ctx, time.Now())
		}
	}
}

func (s *Server) runDue(ctx context.Context, now time.Time) {
	scheds, err := s.store.Schedules(ctx)
	if err != nil {
		return
	}
	for _, sc := range scheds {
		if !sc.Enabled {
			continue
		}
		var last time.Time
		if sc.LastRun > 0 {
			last = time.UnixMilli(sc.LastRun)
		}
		next, err := ParseSpec(sc.Spec, last, now)
		if err != nil || next.After(now) {
			continue
		}
		if _, busy := s.runner.Active(); busy {
			return
		}
		var opts RunOptions
		_ = json.Unmarshal(sc.Params, &opts)
		opts.Kind = sc.Kind
		opts.Actor = "schedule:" + sc.Name
		sc.LastRun = now.UnixMilli()
		if _, err := s.store.SaveSchedule(ctx, sc); err != nil {
			continue
		}
		if _, err := s.runner.Start(ctx, opts); err != nil {
			s.log.Info("scheduled run skipped", "schedule", sc.Name, "reason", err)
			_ = s.store.Audit(ctx, store.AuditEntry{Actor: opts.Actor, Action: "run.start", Result: "skipped", Detail: err.Error()})
		}
		return
	}
}
