package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/catalog"
	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/fingerprint"
	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/notify"
	"github.com/retreat-community/lanscape/internal/store"
	"github.com/retreat-community/lanscape/internal/topo"
)

type hookSink struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (h *hookSink) events() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, m := range h.msgs {
		out = append(out, m.Event)
	}
	return out
}

func (h *hookSink) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.events()) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("webhook got %v, want %d messages", h.events(), n)
}

func TestServicesFlow(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	if code := do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil); code != 200 {
		t.Fatalf("login: %d", code)
	}
	// a Gitea-like service that can be switched off
	var up atomic.Bool
	up.Store(true)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, "<title>Gitea: Git with a cup of tea</title>")
	}))
	defer app.Close()
	port, _ := strconv.Atoi(app.URL[strings.LastIndexByte(app.URL, ':')+1:])
	// webhook receiver
	sink := &hookSink{}
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m notify.Message
		_ = json.NewDecoder(r.Body).Decode(&m)
		sink.mu.Lock()
		sink.msgs = append(sink.msgs, m)
		sink.mu.Unlock()
	}))
	defer hook.Close()

	s.hub.Connected(AgentState{ID: "a1", Name: "nas", Kind: "full"}, &fakeConn{id: "a1"})
	gitea := &fingerprint.Match{ID: "gitea", Name: "Gitea", Category: "development", Icon: "gitea", Score: 6,
		Monitor: fingerprint.Monitor{Type: "http", Path: "/api/healthz"}}
	sock := discovery.Item{Key: "tcp/" + strconv.Itoa(port) + "@*", Kind: discovery.KindSocket, Proto: "tcp", Addr: "127.0.0.1",
		Port: port, Process: "gitea", URL: app.URL, App: gitea}
	ssh := discovery.Item{Key: "tcp/22@*", Kind: discovery.KindSocket, Proto: "tcp", Addr: "0.0.0.0", Port: 22, Process: "sshd"}
	ctx := context.Background()
	rep := discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceSockets, Items: []discovery.Item{sock, ssh}}}}
	if err := s.ingestDiscovery(ctx, "a1", rep); err != nil {
		t.Fatal(err)
	}
	var found []FoundCard
	do(t, c, "GET", ts.URL+"/api/v1/found?status=new", nil, &found)
	var card *FoundCard
	for i := range found {
		if found[i].Key == "proc:a1:gitea" {
			card = &found[i]
		}
	}
	if card == nil || card.Name != "Gitea" || card.Monitor == nil || card.Monitor.Target != app.URL+"/api/healthz" {
		t.Fatalf("found: %+v", found)
	}
	var feed []store.Change
	do(t, c, "GET", ts.URL+"/api/v1/changes", nil, &feed)
	if len(feed) < 2 || feed[len(feed)-1].Kind != catalog.ChangeServiceNew {
		t.Errorf("feed: %+v", feed)
	}

	// channel
	var ch store.Channel
	if code := do(t, c, "POST", ts.URL+"/api/v1/channels", map[string]any{"name": "hook", "type": "webhook", "enabled": true,
		"config": map[string]any{"url": hook.URL, "secret": "s3cret"}}, &ch); code != 200 || strings.Contains(string(ch.Config), "s3cret") {
		t.Fatalf("channel: %d %s", code, ch.Config)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/channels", map[string]any{"type": "telegram", "config": map[string]any{}}, nil); code != 400 {
		t.Errorf("invalid channel accepted: %d", code)
	}
	// update with the masked secret keeps it
	do(t, c, "PUT", ts.URL+"/api/v1/channels/"+strconv.FormatInt(ch.ID, 10), map[string]any{"name": "hook", "type": "webhook",
		"enabled": true, "config": map[string]any{"url": hook.URL, "secret": notify.Mask}}, nil)
	if stored, _ := s.store.ChannelByID(ctx, ch.ID); !strings.Contains(string(stored.Config), "s3cret") {
		t.Errorf("secret lost: %s", stored.Config)
	}

	// add the card: service + recommended monitor
	var svc store.Service
	if code := do(t, c, "POST", ts.URL+"/api/v1/found/add", map[string]any{"key": card.Key, "monitor": true, "tile": true}, &svc); code != 201 {
		t.Fatalf("add: %d", code)
	}
	if svc.AppID != "gitea" || svc.Group != "development" || svc.InternalURL != app.URL || !svc.Tile {
		t.Errorf("service: %+v", svc)
	}
	var mons []MonitorView
	do(t, c, "GET", ts.URL+"/api/v1/monitors", nil, &mons)
	if len(mons) != 1 || mons[0].ServiceID != svc.ID {
		t.Fatalf("monitors: %+v", mons)
	}
	mid := strconv.FormatInt(mons[0].ID, 10)
	var sp monitor.Spec
	_ = json.Unmarshal(mons[0].Spec, &sp)
	sp.Target = app.URL + "/" // the fake has no health endpoint
	spec, _ := json.Marshal(sp)
	do(t, c, "PUT", ts.URL+"/api/v1/monitors/"+mid, map[string]any{"name": "Gitea", "service_id": svc.ID, "spec": json.RawMessage(spec),
		"retries": 2, "interval_s": 60}, nil)
	check := func() monitor.Result {
		var out struct {
			Result monitor.Result `json:"result"`
		}
		if code := do(t, c, "POST", ts.URL+"/api/v1/monitors/"+mid+"/check", nil, &out); code != 200 {
			t.Fatalf("check: %d", code)
		}
		return out.Result
	}
	if r := check(); r.Status != monitor.Up {
		t.Fatalf("check up: %+v", r)
	}
	up.Store(false)
	if r := check(); r.Status != monitor.Down {
		t.Fatalf("check down: %+v", r)
	}
	if m, _ := s.uptime.get(mons[0].ID); m.Status != StatusPending && m.Status != monitor.Up {
		t.Errorf("status during retries: %s", m.Status)
	}
	check()
	sink.wait(t, 1)
	var incs []IncidentView
	do(t, c, "GET", ts.URL+"/api/v1/incidents?open=1", nil, &incs)
	if len(incs) != 1 || incs[0].Monitor != "Gitea" || !strings.Contains(incs[0].Cause, "502") {
		t.Fatalf("incidents: %+v", incs)
	}
	iid := strconv.FormatInt(incs[0].ID, 10)
	do(t, c, "POST", ts.URL+"/api/v1/incidents/"+iid+"/ack", nil, nil)
	do(t, c, "POST", ts.URL+"/api/v1/incidents/"+iid+"/notes", map[string]string{"text": "restarting the container"}, nil)
	var inc IncidentView
	do(t, c, "GET", ts.URL+"/api/v1/incidents/"+iid, nil, &inc)
	if inc.AckedBy != "admin" || len(inc.Notes) != 1 {
		t.Errorf("incident: %+v", inc)
	}
	var dash Dashboard
	do(t, c, "GET", ts.URL+"/api/v1/dashboard", nil, &dash)
	if len(dash.Groups) != 1 || dash.Groups[0].Tiles[0].Status != monitor.Down || dash.Summary["incidents"] != 1 ||
		dash.Groups[0].Tiles[0].URL != app.URL {
		t.Errorf("dashboard: %+v", dash)
	}
	up.Store(true)
	check()
	sink.wait(t, 2)
	if ev := sink.events(); ev[0] != "incident.opened" || ev[1] != "incident.resolved" {
		t.Errorf("notifications: %v", ev)
	}

	// maintenance: an incident opens, but nobody is notified
	var mw store.Maintenance
	if code := do(t, c, "POST", ts.URL+"/api/v1/maintenance", map[string]any{"name": "upgrade", "duration_min": 30}, &mw); code != 201 {
		t.Fatalf("maintenance: %d", code)
	}
	up.Store(false)
	check()
	check()
	do(t, c, "GET", ts.URL+"/api/v1/incidents?open=1", nil, &incs)
	if len(incs) != 1 || !incs[0].Maintenance {
		t.Errorf("maintenance incident: %+v", incs)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(sink.events()); n != 2 {
		t.Errorf("notified during maintenance: %v", sink.events())
	}
	var detail struct {
		Uptime Uptime              `json:"uptime"`
		Checks []store.CheckRecord `json:"checks"`
		Days   []store.DayStat     `json:"days"`
	}
	do(t, c, "GET", ts.URL+"/api/v1/monitors/"+mid, nil, &detail)
	if len(detail.Checks) != 6 || len(detail.Days) != 1 || detail.Days[0].Maint != 2 || detail.Uptime.Day == nil ||
		*detail.Uptime.Day != 50 {
		t.Errorf("detail: %+v day=%v", detail, detail.Uptime.Day)
	}

	// search
	var res []SearchResult
	do(t, c, "GET", ts.URL+"/api/v1/search?q=gite", nil, &res)
	if len(res) < 2 || res[0].Type != "service" {
		t.Errorf("search: %+v", res)
	}

	// the ssh socket disappears
	rep.Sources[0].Items = []discovery.Item{sock}
	if err := s.ingestDiscovery(ctx, "a1", rep); err != nil {
		t.Fatal(err)
	}
	do(t, c, "GET", ts.URL+"/api/v1/changes?limit=1", nil, &feed)
	if len(feed) != 1 || feed[0].Kind != catalog.ChangePortClosed || feed[0].Subject != "nas tcp/22" {
		t.Errorf("port closed: %+v", feed)
	}
	do(t, c, "GET", ts.URL+"/api/v1/found", nil, &found)
	for _, f := range found {
		if f.Key == "proc:a1:sshd" && !f.Gone {
			t.Errorf("ssh card not gone: %+v", f)
		}
		if f.Key == "proc:a1:gitea" && (f.Status != FoundAdded || f.ServiceID != svc.ID) {
			t.Errorf("gitea card: %+v", f)
		}
	}
	// ignore / restore
	if code := do(t, c, "POST", ts.URL+"/api/v1/found/state", map[string]string{"key": "proc:a1:sshd", "status": "ignored"}, nil); code != 200 {
		t.Errorf("ignore: %d", code)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/found/state", map[string]string{"key": "proc:a1:gitea", "status": "ignored"}, nil); code != 409 {
		t.Errorf("ignoring an added card: %d", code)
	}
	// deleting the service returns the card to the queue and keeps the monitor
	do(t, c, "DELETE", ts.URL+"/api/v1/services/"+strconv.FormatInt(svc.ID, 10), nil, nil)
	do(t, c, "GET", ts.URL+"/api/v1/found?status=new", nil, &found)
	if len(found) != 1 || found[0].Key != "proc:a1:gitea" {
		t.Errorf("after delete: %+v", found)
	}
	if m, ok := s.uptime.get(mons[0].ID); !ok || m.ServiceID != 0 {
		t.Errorf("monitor after service delete: %+v", m)
	}
}

func TestRulesAutoAdd(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	if code := do(t, c, "PUT", ts.URL+"/api/v1/discovery/rules", []catalog.Rule{
		{Name: "tls ingress", Kind: "ingress", TLSOnly: true, Action: "add", Monitor: true, Tile: true},
		{Name: "no ssh", App: "", Kind: "socket", Action: "ignore"}}, nil); code != 200 {
		t.Fatalf("rules: %d", code)
	}
	rep := discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceK8s, Items: []discovery.Item{
		{Key: "ingress/a/web", Kind: discovery.KindIngress, Name: "web", Namespace: "a", Hosts: []string{"web.example"}, TLS: true},
	}}, {Source: discovery.SourceSockets, Items: []discovery.Item{
		{Key: "tcp/22@*", Kind: discovery.KindSocket, Proto: "tcp", Addr: "0.0.0.0", Port: 22, Process: "sshd"},
	}}}}
	s.hub.Connected(AgentState{ID: "k", Name: "k8s", Kind: "full"}, &fakeConn{id: "k"})
	if err := s.ingestDiscovery(context.Background(), "k", rep); err != nil {
		t.Fatal(err)
	}
	var found []FoundCard
	do(t, c, "GET", ts.URL+"/api/v1/found", nil, &found)
	st := map[string]string{}
	for _, f := range found {
		st[f.Key] = f.Status + "/" + f.Rule
	}
	if st["k8s:ingress/a/web"] != "added/tls ingress" || st["proc:k:sshd"] != "ignored/no ssh" {
		t.Errorf("rules: %v", st)
	}
	ms := s.uptime.snapshot()
	if len(ms) != 1 {
		t.Fatalf("monitors: %+v", ms)
	}
	var sp monitor.Spec
	_ = json.Unmarshal(ms[0].Spec, &sp)
	if sp.Type != monitor.TypeHTTP || sp.Target != "https://web.example" {
		t.Errorf("monitor spec: %+v", sp)
	}
}

func TestCombine(t *testing.T) {
	r := func(p, st string) monitor.Result {
		return monitor.Result{Point: p, Status: st, LatencyMS: 10, Message: st}
	}
	cases := []struct {
		rs   []monitor.Result
		min  int
		want string
	}{
		{[]monitor.Result{r("server", "up")}, 1, monitor.Up},
		{[]monitor.Result{r("server", "down")}, 1, monitor.Down},
		{[]monitor.Result{r("a", "down"), r("b", "up")}, 2, monitor.Degraded},
		{[]monitor.Result{r("a", "down"), r("b", "down")}, 2, monitor.Down},
		{[]monitor.Result{r("a", "down"), r("b", statusUnknown)}, 2, monitor.Down}, // only one point could observe
		{[]monitor.Result{r("a", statusUnknown)}, 1, statusUnknown},
		{[]monitor.Result{r("a", "degraded"), r("b", "up")}, 1, monitor.Degraded},
	}
	for i, c := range cases {
		if got := combine(c.rs, c.min); got.Status != c.want {
			t.Errorf("%d: %s, want %s (%s)", i, got.Status, c.want, got.Message)
		}
	}
	if got := combine([]monitor.Result{r("a", "down"), r("b", "up")}, 2); !strings.Contains(got.Message, "1 of 2") || got.Point != "a" {
		t.Errorf("message: %+v", got)
	}
}

func TestHeartbeatCompositeSuppression(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	var sent []string
	var mu sync.Mutex
	s.uptime.sendHook = func(_ store.Channel, m notify.Message) {
		mu.Lock()
		sent = append(sent, m.Event+" "+m.Monitor)
		mu.Unlock()
	}
	do(t, c, "POST", ts.URL+"/api/v1/channels", map[string]any{"type": "webhook", "enabled": true,
		"config": map[string]any{"url": "http://127.0.0.1:1/"}}, nil)

	// heartbeat
	var hb store.Monitor
	if code := do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": "backup", "retries": 1, "interval_s": 3600,
		"spec": map[string]any{"type": "heartbeat"}}, &hb); code != 201 || !strings.HasPrefix(hb.PushToken, "lsh_") {
		t.Fatalf("heartbeat monitor: %d %+v", code, hb)
	}
	run := func(id int64) store.Monitor {
		s.uptime.mu.Lock()
		st := s.uptime.mons[id]
		s.uptime.mu.Unlock()
		s.uptime.execute(context.Background(), st)
		m, _ := s.uptime.get(id)
		return m
	}
	if m := run(hb.ID); m.Status != StatusPending {
		t.Errorf("before the first push: %s %s", m.Status, m.LastMessage)
	}
	if code := do(t, c, "GET", ts.URL+"/api/push/"+hb.PushToken+"?msg=ok&ping=12", nil, nil); code != 200 {
		t.Fatalf("push: %d", code)
	}
	if code := do(t, c, "GET", ts.URL+"/api/push/lsh_wrong", nil, nil); code != 404 {
		t.Errorf("wrong token: %d", code)
	}
	if m := run(hb.ID); m.Status != monitor.Up || m.LastLatency != 12 {
		t.Errorf("after push: %+v", m)
	}
	do(t, c, "POST", ts.URL+"/api/push/"+hb.PushToken+"?status=down&msg=disk+full", nil, nil)
	if m := run(hb.ID); m.Status != monitor.Down || m.LastMessage != "disk full" {
		t.Errorf("failed job: %+v", m)
	}
	// silence: pretend the last push was long ago
	s.uptime.mu.Lock()
	s.uptime.mons[hb.ID].m.LastPush = time.Now().Add(-3 * time.Hour).UnixMilli()
	s.uptime.mons[hb.ID].pushDown = false
	s.uptime.mu.Unlock()
	if m := run(hb.ID); m.Status != monitor.Down || !strings.Contains(m.LastMessage, "no heartbeat for") {
		t.Errorf("silence: %+v", m)
	}

	// a router and a service behind it: the service incident is suppressed while the router is down
	var up, routerBack atomic.Bool
	up.Store(true)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() && (!routerBack.Load() || r.URL.Path != "/router") {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer app.Close()
	var router, svc store.Monitor
	do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": "router", "retries": 1,
		"spec": map[string]any{"type": "http", "target": app.URL + "/router"}}, &router)
	if code := do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": "nas", "retries": 1, "parents": []int64{router.ID},
		"spec": map[string]any{"type": "http", "target": app.URL + "/nas"}}, &svc); code != 201 {
		t.Fatalf("child: %d", code)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": "loop", "parents": []int64{999},
		"spec": map[string]any{"type": "http", "target": app.URL}}, nil); code != 400 {
		t.Errorf("unknown parent accepted: %d", code)
	}
	var comp store.Monitor
	if code := do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": "storage", "retries": 1,
		"spec": map[string]any{"type": "composite", "expr": "#" + strconv.FormatInt(router.ID, 10) + " && #" + strconv.FormatInt(svc.ID, 10)}},
		&comp); code != 201 {
		t.Fatalf("composite: %d", code)
	}
	run(router.ID)
	run(svc.ID)
	if m := run(comp.ID); m.Status != monitor.Up {
		t.Errorf("composite up: %+v", m)
	}
	up.Store(false)
	run(router.ID)
	run(svc.ID)
	if m := run(comp.ID); m.Status != monitor.Down || !strings.Contains(m.LastMessage, "router") {
		t.Errorf("composite down: %+v", m)
	}
	var incs []IncidentView
	do(t, c, "GET", ts.URL+"/api/v1/incidents?open=1", nil, &incs)
	var child *IncidentView
	for i := range incs {
		if incs[i].MonitorID == svc.ID {
			child = &incs[i]
		}
	}
	if child == nil || !child.Suppressed || child.ParentID == 0 || !strings.Contains(child.Cause, "router is down") {
		t.Fatalf("child incident: %+v", incs)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	joined := strings.Join(sent, ";")
	mu.Unlock()
	if !strings.Contains(joined, "incident.opened router") || strings.Contains(joined, "incident.opened nas") {
		t.Errorf("notifications: %v", sent)
	}
	// the router recovers, the nas stays down: its incident is announced now
	routerBack.Store(true)
	run(router.ID)
	run(svc.ID)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	joined = strings.Join(sent, ";")
	mu.Unlock()
	if !strings.Contains(joined, "incident.resolved router") || !strings.Contains(joined, "incident.opened nas") {
		t.Errorf("after the parent recovered: %v", sent)
	}
}

func TestResourceMonitorsAndProxmoxSpeed(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	s.hub.Connected(AgentState{ID: "pve", Name: "pve1", Hostname: "pve1", Kind: "full", Inv: agent.Inventory{Ifaces: []netio.Iface{
		{Name: "eno1", Kind: "physical", Speed: 1000}, {Name: "eno2", Kind: "physical", Speed: 1000},
		{Name: "bond0", Kind: "bond", Members: []string{"eno1", "eno2"}},
		{Name: "vmbr0", Kind: "bridge", Members: []string{"bond0", "tap100i0"}},
	}}}, &fakeConn{id: "pve"})
	s.hub.Connected(AgentState{ID: "guest", Name: "nas", Kind: "full", Inv: agent.Inventory{Ifaces: []netio.Iface{
		{Name: "eth0", Kind: "physical", MAC: "BC:24:11:AA:BB:CC"},
	}}}, &fakeConn{id: "guest"})
	vm := discovery.Item{Key: "qemu/100", Kind: discovery.KindVM, Name: "nas", State: "running", Labels: map[string]string{"node": "pve1"},
		NICs: []discovery.NIC{{Name: "net0", MAC: "bc:24:11:aa:bb:cc", Bridge: "vmbr0", Model: "virtio"}}}
	report := func(items ...discovery.Item) {
		if err := s.ingestDiscovery(ctx, "pve", discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceProxmox, Items: items}}}); err != nil {
			t.Fatal(err)
		}
	}
	report(vm)
	if sp := s.inheritSpeed(topo.Member{Node: "guest", Iface: "eth0"}); sp != 2000 {
		t.Errorf("inherited speed %d, want 2000 (bond of two 1G ports)", sp)
	}
	spec := monitor.Spec{Type: monitor.TypeVM, Target: "proxmox:*:qemu/100"}
	if r := s.resourceResult(ctx, spec); r.Status != monitor.Up {
		t.Errorf("running vm: %+v", r)
	}
	vm.State = "stopped"
	report(vm)
	if r := s.resourceResult(ctx, spec); r.Status != monitor.Down || !strings.Contains(r.Message, "stopped") {
		t.Errorf("stopped vm: %+v", r)
	}
	report()
	if r := s.resourceResult(ctx, spec); r.Status != monitor.Down || !strings.Contains(r.Message, "disappeared") {
		t.Errorf("removed vm: %+v", r)
	}
	if r := s.resourceResult(ctx, monitor.Spec{Type: monitor.TypeContainer, Target: "docker:x:container/none"}); r.Status != monitor.Down {
		t.Errorf("unknown container: %+v", r)
	}
}

func TestMapDecorationAndPathHistory(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	s.hub.Connected(AgentState{ID: "pve", Name: "pve1", Hostname: "pve1", Kind: "full"}, &fakeConn{id: "pve"})
	s.hub.Connected(AgentState{ID: "nas", Name: "nas", Kind: "full", Inv: agent.Inventory{Env: agent.Env{Kind: "vm"},
		Ifaces: []netio.Iface{{Name: "eth0", MAC: "bc:24:11:aa:bb:cc"}}}}, &fakeConn{id: "nas"})
	s.hub.Connected(AgentState{ID: "pod1", Name: "pve1-pod", Kind: "full", Inv: agent.Inventory{Env: agent.Env{Kind: "k8s-pod", K8sNode: "pve1"}}},
		&fakeConn{id: "pod1"})
	rep := discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceProxmox, Items: []discovery.Item{
		{Key: "qemu/100", Kind: discovery.KindVM, Name: "nas", State: "running", Labels: map[string]string{"node": "pve1"},
			NICs: []discovery.NIC{{MAC: "bc:24:11:aa:bb:cc", Bridge: "vmbr0"}}},
		{Key: "lxc/200", Kind: discovery.KindCT, Name: "dns", State: "running", Labels: map[string]string{"node": "pve1"}},
	}}}}
	if err := s.ingestDiscovery(ctx, "pve", rep); err != nil {
		t.Fatal(err)
	}
	var svc store.Service
	do(t, c, "POST", ts.URL+"/api/v1/found/add", map[string]any{"key": "proxmox:lxc/200", "monitor": true, "tile": true}, &svc)
	var g MapGraph
	do(t, c, "GET", ts.URL+"/api/v1/map", nil, &g)
	parent := map[string]string{}
	for _, n := range g.Nodes {
		parent[n.ID] = n.Parent
	}
	if parent["dev:nas"] != "dev:pve" || parent["dev:pod1"] != "dev:pve" || parent["guest:lxc/200"] != "dev:pve" {
		t.Errorf("nesting: %v", parent)
	}
	if _, ok := parent["guest:qemu/100"]; ok {
		t.Error("a VM with an agent must not be duplicated")
	}
	if parent["svc:"+strconv.FormatInt(svc.ID, 10)] != "guest:lxc/200" {
		t.Errorf("service badge: %v", parent)
	}

	for i, bps := range []uint64{900e6, 400e6} {
		id, _ := s.store.CreateRun(ctx, store.Run{Kind: KindFull, Status: "running", Started: int64(i)})
		b, _ := json.Marshal(Report{ID: id, Kind: KindFull, Status: "done", Paths: []topo.PathResult{
			{SegID: "lan", Src: "a", Dst: "b", SrcIf: "eth0", DstIf: "eth0", BestBPS: bps, Verdict: "green"},
			{SegID: "lan", Src: "a", Dst: "c", BestBPS: 1}}})
		_ = s.store.FinishRun(ctx, id, "done", b)
	}
	var hist []PathPoint
	do(t, c, "GET", ts.URL+"/api/v1/paths/history?src=b&dst=a&seg=lan", nil, &hist)
	if len(hist) != 2 || hist[0].BestBPS != 900e6 || hist[1].BestBPS != 400e6 {
		t.Errorf("history: %+v", hist)
	}
	if code := do(t, c, "GET", ts.URL+"/api/v1/paths/history", nil, nil); code != 400 {
		t.Errorf("missing params: %d", code)
	}
}
