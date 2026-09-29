package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/store"
	"github.com/retreat-community/lanscape/internal/testengine"
)

// Change kinds for the Internet exits (provider outages).
const (
	ChangeInternetDown = "internet_down"
	ChangeInternetUp   = "internet_up"
)

// internetState remembers the last outcome per exit and when checks last ran.
type internetState struct {
	mu        sync.Mutex
	running   bool
	lastCheck time.Time
	lastSpeed time.Time
	up        map[string]bool // exit key -> last result
}

func exitKey(point, dev string) string { return point + "/" + dev }

// runInternet runs the Internet test from the configured points and stores the results.
func (s *Server) runInternet(ctx context.Context, speed bool) []store.InternetCheck {
	s.inet.mu.Lock()
	if s.inet.running {
		s.inet.mu.Unlock()
		return nil
	}
	s.inet.running = true
	s.inet.lastCheck = time.Now()
	if speed {
		s.inet.lastSpeed = time.Now()
	}
	s.inet.mu.Unlock()
	defer func() {
		s.inet.mu.Lock()
		s.inet.running = false
		s.inet.mu.Unlock()
	}()

	st := s.settings(ctx)
	msg := proto.InternetMsg{IPURL: st.InternetIPURL, DownloadURL: ""}
	if msg.IPURL == "" {
		msg.IPURL = testengine.DefaultIPURL
	}
	if speed {
		msg.DownloadURL = st.InternetDownloadURL
		if msg.DownloadURL == "" {
			msg.DownloadURL = testengine.DefaultDownloadURL
		}
	}
	points := st.InternetPoints
	if len(points) == 0 {
		points = []string{pointServer}
	}
	var out []store.InternetCheck
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range points {
		wg.Add(1)
		go func(point string) {
			defer wg.Done()
			var res []testengine.InternetResult
			if point == pointServer {
				res = testengine.Internet(ctx, nil, testengine.InternetParams{IPURL: msg.IPURL, DownloadURL: msg.DownloadURL})
			} else {
				a, ok := s.hub.Get(point)
				c, online := s.hub.Conn(point)
				if !ok || !online || !hasCap(a.Caps, proto.MsgInternet) {
					res = []testengine.InternetResult{{Error: "agent offline or without the Internet test"}}
				} else {
					rctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
					raw, err := c.Request(rctx, proto.MsgInternet, msg)
					cancel()
					if err != nil {
						res = []testengine.InternetResult{{Error: err.Error()}}
					} else if json.Unmarshal(raw, &res) != nil || len(res) == 0 {
						res = []testengine.InternetResult{{Error: "no default gateway"}}
					}
				}
			}
			now := time.Now().UnixMilli()
			for _, r := range res {
				c := store.InternetCheck{TS: now, Point: point, Dev: r.Dev, Gateway: r.Gateway, OK: r.OK, Error: r.Error,
					PublicIP: r.PublicIP, LatencyMS: r.LatencyMS, DownMbps: r.DownMbps}
				if id, err := s.store.AddInternetCheck(ctx, c); err == nil {
					c.ID = id
				}
				mu.Lock()
				out = append(out, c)
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return exitKey(out[i].Point, out[i].Dev) < exitKey(out[j].Point, out[j].Dev) })
	s.internetTransitions(ctx, out)
	s.events.Publish("internet", out)
	return out
}

// internetTransitions writes provider outages and recoveries to the change feed.
func (s *Server) internetTransitions(ctx context.Context, res []store.InternetCheck) {
	s.inet.mu.Lock()
	if s.inet.up == nil {
		s.inet.up = map[string]bool{}
	}
	var changes []store.Change
	for _, c := range res {
		k := exitKey(c.Point, c.Dev)
		prev, known := s.inet.up[k]
		s.inet.up[k] = c.OK
		if !known || prev == c.OK {
			continue
		}
		subject := s.pointName(c.Point)
		if c.Dev != "" {
			subject += " " + c.Dev
		}
		if c.OK {
			changes = append(changes, store.Change{Kind: ChangeInternetUp, Subject: subject, Detail: c.PublicIP, AgentID: c.Point})
		} else {
			changes = append(changes, store.Change{Kind: ChangeInternetDown, Subject: subject, Detail: c.Error, AgentID: c.Point})
		}
	}
	s.inet.mu.Unlock()
	for _, c := range changes {
		c.TS = time.Now().UnixMilli()
		s.addChange(ctx, c)
	}
}

func (s *Server) pointName(point string) string {
	if a, ok := s.hub.Get(point); ok && a.Name != "" {
		return a.Name
	}
	return point
}

// internetScheduler runs the configured periodic checks (both are off by default).
func (s *Server) internetScheduler(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st := s.settings(ctx)
		s.inet.mu.Lock()
		checkDue := st.InternetEveryMin > 0 && time.Since(s.inet.lastCheck) >= time.Duration(st.InternetEveryMin)*time.Minute
		speedDue := st.InternetSpeedEveryH > 0 && time.Since(s.inet.lastSpeed) >= time.Duration(st.InternetSpeedEveryH)*time.Hour
		s.inet.mu.Unlock()
		if checkDue || speedDue {
			s.runInternet(ctx, speedDue)
		}
	}
}

// InternetExit summarises one exit (point and interface) over a period.
type InternetExit struct {
	Point    string              `json:"point"`
	Name     string              `json:"name"`
	Dev      string              `json:"dev,omitempty"`
	Gateway  string              `json:"gateway,omitempty"`
	Last     store.InternetCheck `json:"last"`
	Speeds   []InternetSpeed     `json:"speeds"`
	Outages  []InternetOutage    `json:"outages"`
	Checks   int                 `json:"checks"`
	Failures int                 `json:"failures"`
}

// InternetSpeed is one download measurement.
type InternetSpeed struct {
	TS   int64   `json:"ts"`
	Mbps float64 `json:"mbps"`
}

// InternetOutage is a period of failed checks; To is 0 while it lasts.
type InternetOutage struct {
	From  int64  `json:"from"`
	To    int64  `json:"to"`
	Error string `json:"error"`
}

// internetExits groups results by exit and derives speeds and outages.
func (s *Server) internetExits(checks []store.InternetCheck) []InternetExit {
	idx := map[string]int{}
	exits := []InternetExit{}
	for _, c := range checks {
		k := exitKey(c.Point, c.Dev)
		i, ok := idx[k]
		if !ok {
			i = len(exits)
			idx[k] = i
			exits = append(exits, InternetExit{Point: c.Point, Name: s.pointName(c.Point), Dev: c.Dev, Speeds: []InternetSpeed{},
				Outages: []InternetOutage{}})
		}
		e := &exits[i]
		e.Last, e.Checks = c, e.Checks+1
		if c.Gateway != "" {
			e.Gateway = c.Gateway
		}
		if c.DownMbps > 0 {
			e.Speeds = append(e.Speeds, InternetSpeed{TS: c.TS, Mbps: c.DownMbps})
		}
		if !c.OK {
			e.Failures++
			if n := len(e.Outages); n == 0 || e.Outages[n-1].To != 0 {
				e.Outages = append(e.Outages, InternetOutage{From: c.TS, Error: c.Error})
			}
		} else if n := len(e.Outages); n > 0 && e.Outages[n-1].To == 0 {
			e.Outages[n-1].To = c.TS
		}
	}
	sort.Slice(exits, func(i, j int) bool {
		return exitKey(exits[i].Point, exits[i].Dev) < exitKey(exits[j].Point, exits[j].Dev)
	})
	return exits
}

func (s *Server) apiInternet(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 366 {
		days = 30
	}
	checks, err := s.store.InternetChecks(r.Context(), time.Now().AddDate(0, 0, -days).UnixMilli())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.internetExits(checks))
}

func (s *Server) apiRunInternet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Speed bool `json:"speed"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &req) {
		return
	}
	s.audit(r, "internet.run", "", "started", strconv.FormatBool(req.Speed))
	go s.runInternet(s.ctx, req.Speed)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
