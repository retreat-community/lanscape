package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/notify"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/store"
)

// Monitor statuses beyond monitor.Up/Down/Degraded.
const (
	StatusPending     = "pending"
	StatusPaused      = "paused"
	StatusMaintenance = "maintenance"
	statusUnknown     = "unknown" // no observation point could run the check
	pointServer       = "server"
	minIntervalS      = 5
)

type monState struct {
	m        store.Monitor
	spec     monitor.Spec
	fails    int
	running  bool
	next     time.Time
	incident int64 // open incident id
	notified bool  // the open incident was announced
}

// uptime schedules monitors, opens and closes incidents and sends notifications.
type uptime struct {
	s     *Server
	mu    sync.Mutex
	mons  map[int64]*monState
	maint []store.Maintenance
	sem   chan struct{}
	http  *http.Client
	// last reminder per incident and channel (escalation)
	reminded map[[2]int64]time.Time
	// sent messages (for tests and the channel "test" button); nil in production
	sendHook func(ch store.Channel, m notify.Message)
}

func newUptime(s *Server) *uptime {
	return &uptime{s: s, mons: map[int64]*monState{}, sem: make(chan struct{}, 32),
		http: &http.Client{Timeout: 15 * time.Second}, reminded: map[[2]int64]time.Time{}}
}

func (u *uptime) load(ctx context.Context) error {
	ms, err := u.s.store.Monitors(ctx)
	if err != nil {
		return err
	}
	open, err := u.s.store.Incidents(ctx, true, 0, 0, 10000)
	if err != nil {
		return err
	}
	byMon := map[int64]store.Incident{}
	for _, i := range open {
		byMon[i.MonitorID] = i
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, m := range ms {
		st := u.newState(m)
		if i, ok := byMon[m.ID]; ok {
			st.incident, st.notified, st.fails = i.ID, i.Notified > 0, m.Retries
		}
		u.mons[m.ID] = st
	}
	return u.refreshMaint(ctx)
}

func (u *uptime) newState(m store.Monitor) *monState {
	st := &monState{m: m}
	_ = json.Unmarshal(m.Spec, &st.spec)
	// spread the first checks after a restart
	st.next = time.Now().Add(time.Duration(m.ID%10) * 300 * time.Millisecond)
	return st
}

// refreshMaint reloads maintenance windows; u.mu must be held or the state unshared.
func (u *uptime) refreshMaint(ctx context.Context) error {
	ms, err := u.s.store.Maintenances(ctx, time.Now().Add(-time.Minute).UnixMilli())
	if err != nil {
		return err
	}
	u.maint = ms
	return nil
}

func (u *uptime) reloadMaint(ctx context.Context) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.refreshMaint(ctx); err != nil {
		u.s.log.Warn("cannot load maintenance windows", "err", err)
	}
}

// inMaint reports whether a monitor is inside a maintenance window; u.mu must be held.
func (u *uptime) inMaint(id int64, t time.Time) bool {
	ms := t.UnixMilli()
	for _, w := range u.maint {
		if ms < w.Starts || ms >= w.Ends {
			continue
		}
		if len(w.Monitors) == 0 {
			return true
		}
		for _, m := range w.Monitors {
			if m == id {
				return true
			}
		}
	}
	return false
}

// set replaces (or adds) a monitor after an API change and schedules it immediately.
func (u *uptime) set(m store.Monitor) {
	u.mu.Lock()
	defer u.mu.Unlock()
	old := u.mons[m.ID]
	st := u.newState(m)
	st.next = time.Now()
	if old != nil {
		st.incident, st.notified, st.fails = old.incident, old.notified, old.fails
		st.m.Status, st.m.LastCheck, st.m.LastLatency, st.m.LastMessage = old.m.Status, old.m.LastCheck, old.m.LastLatency, old.m.LastMessage
	}
	u.mons[m.ID] = st
}

func (u *uptime) remove(id int64) {
	u.mu.Lock()
	delete(u.mons, id)
	u.mu.Unlock()
}

// snapshot returns the live monitors.
func (u *uptime) snapshot() []store.Monitor {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]store.Monitor, 0, len(u.mons))
	now := time.Now()
	for _, st := range u.mons {
		m := st.m
		if !m.Enabled {
			m.Status = StatusPaused
		} else if u.inMaint(m.ID, now) {
			m.Status = StatusMaintenance
		}
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func (u *uptime) get(id int64) (store.Monitor, bool) {
	for _, m := range u.snapshot() {
		if m.ID == id {
			return m, true
		}
	}
	return store.Monitor{}, false
}

func (u *uptime) run(ctx context.Context) {
	if err := u.load(ctx); err != nil {
		u.s.log.Error("cannot load monitors", "err", err)
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var lastMaint, lastRemind time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(lastMaint) > 30*time.Second {
				u.reloadMaint(ctx)
				lastMaint = now
			}
			if now.Sub(lastRemind) > time.Minute {
				go u.remind(ctx)
				lastRemind = now
			}
			u.mu.Lock()
			var due []*monState
			for _, st := range u.mons {
				if st.m.Enabled && !st.running && !now.Before(st.next) {
					st.running = true
					due = append(due, st)
				}
			}
			u.mu.Unlock()
			for _, st := range due {
				select {
				case u.sem <- struct{}{}:
					go func(st *monState) {
						defer func() { <-u.sem }()
						u.execute(ctx, st)
					}(st)
				case <-ctx.Done():
					return
				default:
					u.mu.Lock()
					st.running, st.next = false, now.Add(time.Second)
					u.mu.Unlock()
				}
			}
		}
	}
}

// observe runs a spec from all points of a monitor.
func (u *uptime) observe(ctx context.Context, spec monitor.Spec, points []string) []monitor.Result {
	if len(points) == 0 {
		points = []string{pointServer}
	}
	res := make([]monitor.Result, len(points))
	var wg sync.WaitGroup
	for i, p := range points {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			res[i] = u.observeAt(ctx, spec, p)
			res[i].Point = p
		}(i, p)
	}
	wg.Wait()
	return res
}

func (u *uptime) observeAt(ctx context.Context, spec monitor.Spec, point string) monitor.Result {
	if point == pointServer || point == "" {
		return monitor.Run(ctx, spec)
	}
	c, ok := u.s.hub.Conn(point)
	if !ok {
		return monitor.Result{Status: statusUnknown, Message: "agent offline"}
	}
	a, _ := u.s.hub.Get(point)
	if c.Lite() || !hasCap(a.Caps, proto.MsgCheck) {
		return monitor.Result{Status: statusUnknown, Message: "agent cannot run checks"}
	}
	timeout := 10 * time.Second
	if spec.TimeoutMS > 0 {
		timeout = time.Duration(spec.TimeoutMS) * time.Millisecond
	}
	rctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	raw, err := c.Request(rctx, proto.MsgCheck, spec)
	if err != nil {
		return monitor.Result{Status: statusUnknown, Message: err.Error()}
	}
	var r monitor.Result
	if err := json.Unmarshal(raw, &r); err != nil {
		return monitor.Result{Status: statusUnknown, Message: err.Error()}
	}
	return r
}

// combine turns per-point results into one: down when at least minFailing points
// (of those that could observe) fail — "down from ≥ N points" (§9.2).
func combine(rs []monitor.Result, minFailing int) monitor.Result {
	var failing, observed, degraded int
	var lat float64
	var msgs []string
	var cert int64
	for _, r := range rs {
		if r.CertNotAfter > 0 && (cert == 0 || r.CertNotAfter < cert) {
			cert = r.CertNotAfter
		}
		switch r.Status {
		case statusUnknown:
			continue
		case monitor.Down:
			failing++
			msgs = append(msgs, pointMsg(r))
		case monitor.Degraded:
			degraded++
			lat += r.LatencyMS
			msgs = append(msgs, pointMsg(r))
		default:
			lat += r.LatencyMS
		}
		observed++
	}
	out := monitor.Result{At: time.Now().UnixMilli(), CertNotAfter: cert}
	if observed == 0 {
		out.Status = statusUnknown
		for _, r := range rs {
			msgs = append(msgs, pointMsg(r))
		}
		out.Message = strings.Join(msgs, "; ")
		return out
	}
	need := max(1, min(minFailing, observed))
	switch {
	case failing >= need:
		out.Status = monitor.Down
	case degraded > 0 || failing > 0:
		out.Status = monitor.Degraded
		if failing > 0 && degraded == 0 {
			msgs = append([]string{fmt.Sprintf("failing from %d of %d points", failing, observed)}, msgs...)
		}
	default:
		out.Status = monitor.Up
	}
	if ok := observed - failing; ok > 0 {
		out.LatencyMS = lat / float64(ok)
	}
	var points []string
	for _, r := range rs {
		if r.Status == monitor.Down {
			points = append(points, r.Point)
		}
	}
	out.Point = strings.Join(points, ",")
	out.Message = strings.Join(msgs, "; ")
	return out
}

func pointMsg(r monitor.Result) string {
	if len(r.Point) > 0 && r.Point != pointServer {
		return r.Point + ": " + r.Message
	}
	return r.Message
}

func dayKey(t time.Time) int { return t.Year()*10000 + int(t.Month())*100 + t.Day() }

func (u *uptime) execute(ctx context.Context, st *monState) {
	u.mu.Lock()
	m, spec := st.m, st.spec
	u.mu.Unlock()
	r := combine(u.observe(ctx, spec, m.Points), m.MinFailing)
	u.record(ctx, st, r)
}

// record stores a result and drives the incident state machine.
func (u *uptime) record(ctx context.Context, st *monState, r monitor.Result) {
	now := time.Now()
	u.mu.Lock()
	st.running = false
	interval := max(st.m.IntervalS, minIntervalS)
	st.next = now.Add(time.Duration(interval) * time.Second)
	if _, live := u.mons[st.m.ID]; !live || u.mons[st.m.ID] != st {
		u.mu.Unlock()
		return // deleted or replaced while running
	}
	if r.Status == statusUnknown {
		st.m.LastMessage = r.Message
		u.mu.Unlock()
		return
	}
	inMaint := u.inMaint(st.m.ID, now)
	var open, closeInc bool
	switch r.Status {
	case monitor.Down:
		st.fails++
		if st.incident == 0 && st.fails >= max(1, st.m.Retries) {
			open = true
		}
	default:
		st.fails = 0
		closeInc = st.incident != 0
	}
	switch {
	case open || (st.incident != 0 && r.Status == monitor.Down):
		st.m.Status = monitor.Down
	case r.Status == monitor.Down:
		// still retrying: keep the previous state
		if st.m.Status == StatusPending || st.m.Status == "" {
			st.m.Status = StatusPending
		}
	default:
		st.m.Status = r.Status
	}
	st.m.LastCheck, st.m.LastLatency, st.m.LastMessage = r.At, r.LatencyMS, r.Message
	if r.CertNotAfter > 0 {
		st.m.CertNotAfter = r.CertNotAfter
	}
	m := st.m
	incident := st.incident
	wasNotified := st.notified
	u.mu.Unlock()

	rec := store.CheckRecord{MonitorID: m.ID, TS: r.At, Status: r.Status, LatencyMS: r.LatencyMS, Point: r.Point}
	if r.Status != monitor.Up {
		rec.Message = r.Message
	}
	if err := u.s.store.AddCheck(ctx, rec, dayKey(now), inMaint); err != nil {
		u.s.log.Warn("cannot store check", "monitor", m.ID, "err", err)
	}
	if err := u.s.store.SetMonitorState(ctx, m); err != nil {
		u.s.log.Warn("cannot store monitor state", "monitor", m.ID, "err", err)
	}
	u.s.metrics.SetMonitor(m.ID, m.Name, m.Status, r.LatencyMS)
	u.s.events.Publish("monitor", map[string]any{"id": m.ID, "status": m.Status, "latency_ms": r.LatencyMS, "at": r.At})

	switch {
	case open:
		inc := store.Incident{MonitorID: m.ID, Opened: now.UnixMilli(), Cause: r.Message, Maintenance: inMaint}
		id, err := u.s.store.OpenIncident(ctx, inc)
		if err != nil {
			u.s.log.Warn("cannot open incident", "monitor", m.ID, "err", err)
			return
		}
		u.mu.Lock()
		st.incident = id
		u.mu.Unlock()
		u.s.log.Info("incident opened", "monitor", m.Name, "incident", id, "cause", r.Message)
		u.s.events.Publish("incident", map[string]any{"id": id, "monitor_id": m.ID, "state": "opened"})
		if !inMaint {
			u.dispatch(ctx, notify.Message{Event: "incident.opened", Severity: notify.SevDown, Title: m.Name + " is down",
				Text: r.Message, IncidentID: id, MonitorID: m.ID, Monitor: m.Name, URL: u.link(ctx, m.ID)})
			_ = u.s.store.SetIncidentNotified(ctx, id, now.UnixMilli())
			u.mu.Lock()
			st.notified = true
			u.mu.Unlock()
		}
	case closeInc:
		if err := u.s.store.CloseIncident(ctx, incident, now.UnixMilli()); err != nil {
			u.s.log.Warn("cannot close incident", "incident", incident, "err", err)
		}
		u.mu.Lock()
		st.incident, st.notified = 0, false
		u.mu.Unlock()
		u.s.log.Info("incident resolved", "monitor", m.Name, "incident", incident)
		u.s.events.Publish("incident", map[string]any{"id": incident, "monitor_id": m.ID, "state": "resolved"})
		if wasNotified {
			var dur string
			if inc, err := u.s.store.IncidentByID(ctx, incident); err == nil {
				dur = " after " + time.Duration(now.UnixMilli()-inc.Opened).Round(time.Second).String()
			}
			u.dispatch(ctx, notify.Message{Event: "incident.resolved", Severity: notify.SevUp, Title: m.Name + " is up",
				Text: "Resolved" + dur, IncidentID: incident, MonitorID: m.ID, Monitor: m.Name, URL: u.link(ctx, m.ID)})
		}
	}
}

func (u *uptime) link(ctx context.Context, monitorID int64) string {
	base := u.s.settings(ctx).PublicURL
	if base == "" {
		base = u.s.cfg.PublicURL
	}
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/#/monitors/" + fmt.Sprint(monitorID)
}

// dispatch sends a message to all subscribed channels.
func (u *uptime) dispatch(ctx context.Context, m notify.Message) {
	m.At = time.Now().UnixMilli()
	chans, err := u.s.store.Channels(ctx)
	if err != nil {
		u.s.log.Warn("cannot load channels", "err", err)
		return
	}
	for _, ch := range chans {
		if !ch.Enabled {
			continue
		}
		var c notify.Common
		_ = json.Unmarshal(ch.Config, &c)
		if !c.Wants(m.MonitorID) || (m.Event == "incident.resolved" && c.NoResolved) || c.InQuiet(time.Now()) {
			continue
		}
		go func(ch store.Channel) {
			// the caller's context may be a finished API request
			if err := u.send(u.s.ctx, ch, m); err != nil {
				u.s.log.Warn("notification failed", "channel", ch.Name, "type", ch.Type, "err", err)
				u.s.metrics.NotifyFailed(ch.Type)
			}
		}(ch)
	}
}

func (u *uptime) send(ctx context.Context, ch store.Channel, m notify.Message) error {
	if u.sendHook != nil {
		u.sendHook(ch, m)
	}
	snd, err := notify.New(ch.Type, ch.Config, u.http)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return snd.Send(sctx, m)
}

// remind re-notifies open, unacknowledged incidents on channels with repeat_minutes (escalation).
func (u *uptime) remind(ctx context.Context) {
	open, err := u.s.store.Incidents(ctx, true, 0, 0, 1000)
	if err != nil || len(open) == 0 {
		return
	}
	chans, err := u.s.store.Channels(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, inc := range open {
		if inc.AckedAt > 0 || inc.Maintenance || inc.Notified == 0 || inc.ParentID > 0 {
			continue
		}
		m, ok := u.get(inc.MonitorID)
		if !ok {
			continue
		}
		for _, ch := range chans {
			var c notify.Common
			_ = json.Unmarshal(ch.Config, &c)
			if !ch.Enabled || c.RepeatMinutes <= 0 || !c.Wants(m.ID) || c.InQuiet(now) {
				continue
			}
			key := [2]int64{inc.ID, ch.ID}
			u.mu.Lock()
			last, ok := u.reminded[key]
			if !ok {
				last = time.UnixMilli(inc.Notified)
			}
			due := now.Sub(last) >= time.Duration(c.RepeatMinutes)*time.Minute
			if due {
				u.reminded[key] = now
			}
			u.mu.Unlock()
			if !due {
				continue
			}
			msg := notify.Message{Event: "incident.reminder", Severity: notify.SevDown, Title: m.Name + " is still down",
				Text:       fmt.Sprintf("Down for %s: %s", now.Sub(time.UnixMilli(inc.Opened)).Round(time.Minute), m.LastMessage),
				IncidentID: inc.ID, MonitorID: m.ID, Monitor: m.Name, URL: u.link(ctx, m.ID), At: now.UnixMilli()}
			go func(ch store.Channel) {
				if err := u.send(ctx, ch, msg); err != nil {
					u.s.log.Warn("reminder failed", "channel", ch.Name, "err", err)
				}
			}(ch)
		}
	}
	u.mu.Lock()
	for k := range u.reminded {
		found := false
		for _, inc := range open {
			if inc.ID == k[0] {
				found = true
			}
		}
		if !found {
			delete(u.reminded, k)
		}
	}
	u.mu.Unlock()
}

// createMonitor stores a new monitor and schedules it.
func (s *Server) createMonitor(ctx context.Context, m store.Monitor) (int64, error) {
	if m.IntervalS < minIntervalS {
		m.IntervalS = 60
	}
	if m.Retries <= 0 {
		m.Retries = 3
	}
	if m.MinFailing <= 0 {
		m.MinFailing = 1
	}
	id, err := s.store.SaveMonitor(ctx, m)
	if err != nil {
		return 0, err
	}
	m.ID, m.Status, m.CreatedAt = id, StatusPending, time.Now().UnixMilli()
	s.uptime.set(m)
	return id, nil
}

// Uptime is the availability over a period.
type Uptime struct {
	Day   *float64 `json:"day"`
	Week  *float64 `json:"week"`
	Month *float64 `json:"month"`
	Year  *float64 `json:"year"`
}

func uptimeOf(days []store.DayStat, from int) *float64 {
	var ok, total int
	for _, d := range days {
		if d.Day < from {
			continue
		}
		ok += d.Up + d.Degraded
		total += d.Up + d.Degraded + d.Down
	}
	if total == 0 {
		return nil
	}
	v := float64(ok) * 100 / float64(total)
	return &v
}

func (s *Server) uptimeStats(ctx context.Context, id int64) (Uptime, []store.DayStat) {
	now := time.Now()
	days, _ := s.store.DayStats(ctx, id, dayKey(now.AddDate(-1, 0, 0)))
	return Uptime{
		Day:   uptimeOf(days, dayKey(now)),
		Week:  uptimeOf(days, dayKey(now.AddDate(0, 0, -6))),
		Month: uptimeOf(days, dayKey(now.AddDate(0, -1, 0))),
		Year:  uptimeOf(days, dayKey(now.AddDate(-1, 0, 0))),
	}, days
}
