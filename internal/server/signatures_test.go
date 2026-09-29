package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
)

// configConn records config messages.
type configConn struct {
	fakeConn
	mu  sync.Mutex
	got []proto.ConfigMsg
}

func (c *configConn) Request(_ context.Context, typ string, data any) (json.RawMessage, error) {
	if typ == proto.MsgConfig {
		c.mu.Lock()
		c.got = append(c.got, data.(proto.ConfigMsg))
		c.mu.Unlock()
	}
	return json.RawMessage(`{}`), nil
}

func (c *configConn) last(t *testing.T) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.got)
		var m proto.ConfigMsg
		if n > 0 {
			m = c.got[n-1]
		}
		c.mu.Unlock()
		if n > 0 {
			var sigs []map[string]any
			if err := json.Unmarshal(m.Signatures, &sigs); err != nil {
				t.Fatal(err)
			}
			return sigs
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no config message")
	return nil
}

func (c *configConn) reset() {
	c.mu.Lock()
	c.got = nil
	c.mu.Unlock()
}

func TestSignatureDistribution(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("- id: newapp\n  name: New App\n  category: development\n  images: [\"acme/newapp\"]\n"))
	}))
	defer feed.Close()
	ag := &configConn{fakeConn: fakeConn{id: "a1"}}
	s.hub.Connected(AgentState{ID: "a1", Name: "nas", Kind: "full"}, ag)
	if sigs := ag.last(t); len(sigs) != 0 {
		t.Errorf("pushed on connect: %v", sigs)
	}

	bad := map[string]string{"custom": "- id: x\n  name: X\n  title: [\"(\"]\n"}
	if code := do(t, c, "PUT", ts.URL+"/api/v1/signatures", bad, nil); code != 400 {
		t.Errorf("invalid regex accepted: %d", code)
	}
	ag.reset()
	var info SignatureInfo
	req := map[string]string{"url": feed.URL, "custom": "- id: ourapp\n  name: Our App\n  category: development\n  images: [\"acme/ourapp\"]\n"}
	if code := do(t, c, "PUT", ts.URL+"/api/v1/signatures", req, &info); code != 200 || info.CustomCount != 1 || info.Builtin < 300 {
		t.Fatalf("save: %d %+v", code, info)
	}
	if sigs := ag.last(t); len(sigs) != 1 || sigs[0]["id"] != "ourapp" {
		t.Errorf("pushed after save: %v", sigs)
	}
	ag.reset()
	if code := do(t, c, "POST", ts.URL+"/api/v1/signatures/update", nil, &info); code != 200 || info.FetchedCount != 1 || info.FetchedAt == 0 {
		t.Fatalf("update: %d %+v", code, info)
	}
	if sigs := ag.last(t); len(sigs) != 2 || sigs[0]["id"] != "newapp" || sigs[1]["id"] != "ourapp" {
		t.Errorf("pushed after update: %v", sigs)
	}
}
