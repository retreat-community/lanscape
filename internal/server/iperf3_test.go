package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/testengine"
)

type iperfConn struct {
	fakeConn
	got proto.Iperf3Msg
}

func (c *iperfConn) Request(_ context.Context, typ string, data any) (json.RawMessage, error) {
	if typ != proto.MsgIperf3 {
		return nil, nil
	}
	c.got = data.(proto.Iperf3Msg)
	return json.Marshal(testengine.Iperf3Result{BPS: 940e6, Streams: c.got.Streams, Reverse: c.got.Reverse, Retransmits: -1})
}

func TestIperf3API(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	ag := &iperfConn{fakeConn: fakeConn{id: "a1"}}
	s.hub.Connected(AgentState{ID: "a1", Name: "nas", Kind: "full", Caps: []string{proto.MsgIperf3}}, ag)
	s.hub.Connected(AgentState{ID: "old", Name: "old", Kind: "full"}, &fakeConn{id: "old"})

	var res testengine.Iperf3Result
	req := map[string]any{"agent": "a1", "host": "192.168.1.30", "seconds": 600, "streams": 4, "reverse": true}
	if code := do(t, c, "POST", ts.URL+"/api/v1/iperf3", req, &res); code != 200 || res.BPS != 940e6 || !res.Reverse {
		t.Fatalf("iperf3: %d %+v", code, res)
	}
	if ag.got.Host != "192.168.1.30" || ag.got.Seconds != 60 || ag.got.Streams != 4 {
		t.Errorf("request: %+v", ag.got)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/iperf3", map[string]any{"agent": "old", "host": "x"}, nil); code != 400 {
		t.Errorf("agent without iperf3: %d", code)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/iperf3", map[string]any{"agent": "a1", "host": ""}, nil); code != 400 {
		t.Errorf("no host: %d", code)
	}
}
