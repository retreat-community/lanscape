package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/testengine"
)

// wanConn answers Internet test requests for two gateways; the second can be failed.
type wanConn struct {
	fakeConn
	down atomic.Bool
}

func (c *wanConn) Request(_ context.Context, typ string, _ any) (json.RawMessage, error) {
	if typ != proto.MsgInternet {
		return nil, nil
	}
	b := testengine.InternetResult{Dev: "wan2", Gateway: "192.168.0.1", OK: true, PublicIP: "198.51.100.9", DownMbps: 95}
	if c.down.Load() {
		b = testengine.InternetResult{Dev: "wan2", Gateway: "192.168.0.1", Error: "i/o timeout"}
	}
	return json.Marshal([]testengine.InternetResult{
		{Dev: "wan", Gateway: "192.168.1.1", OK: true, PublicIP: "203.0.113.7", DownMbps: 480}, b})
}

func TestInternetChecks(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	ip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ip=192.0.2.44\n") }))
	defer ip.Close()
	rt := &wanConn{fakeConn: fakeConn{id: "rt"}}
	s.hub.Connected(AgentState{ID: "rt", Name: "router", Kind: "full", Caps: []string{proto.MsgInternet}}, rt)

	st := DefaultSettings
	var saved Settings
	st.InternetPoints, st.InternetIPURL, st.InternetEveryMin = []string{"server", "rt"}, ip.URL, 1
	if code := do(t, c, "PUT", ts.URL+"/api/v1/settings", st, &saved); code != 200 || saved.InternetEveryMin != 5 {
		t.Fatalf("settings: %d %+v", code, saved)
	}
	st.InternetPoints = []string{"nope"}
	if code := do(t, c, "PUT", ts.URL+"/api/v1/settings", st, nil); code != 400 {
		t.Errorf("unknown point accepted: %d", code)
	}

	ctx := context.Background()
	if res := s.runInternet(ctx, true); len(res) != 3 {
		t.Fatalf("results: %+v", res)
	}
	rt.down.Store(true)
	s.runInternet(ctx, false)
	s.runInternet(ctx, false)
	rt.down.Store(false)
	s.runInternet(ctx, false)

	var exits []InternetExit
	do(t, c, "GET", ts.URL+"/api/v1/internet", nil, &exits)
	if len(exits) != 3 {
		t.Fatalf("exits: %+v", exits)
	}
	byDev := map[string]InternetExit{}
	for _, e := range exits {
		byDev[e.Point+"/"+e.Dev] = e
	}
	srv := byDev["server/"]
	if srv.Last.PublicIP != "192.0.2.44" || !srv.Last.OK || srv.Checks != 4 {
		t.Errorf("server exit: %+v", srv)
	}
	wan2 := byDev["rt/wan2"]
	if wan2.Name != "router" || wan2.Failures != 2 || len(wan2.Outages) != 1 || wan2.Outages[0].To == 0 || len(wan2.Speeds) != 2 || wan2.Speeds[0].Mbps != 95 {
		t.Errorf("wan2: %+v", wan2)
	}
	changes, _ := s.store.Changes(ctx, 0, 50)
	kinds := map[string]int{}
	for _, ch := range changes {
		kinds[ch.Kind]++
	}
	if kinds[ChangeInternetDown] != 1 || kinds[ChangeInternetUp] != 1 {
		t.Errorf("change feed: %v", kinds)
	}
	var d Dashboard
	do(t, c, "GET", ts.URL+"/api/v1/dashboard", nil, &d)
	if len(d.Internet) != 3 {
		t.Errorf("dashboard: %+v", d.Internet)
	}
}
