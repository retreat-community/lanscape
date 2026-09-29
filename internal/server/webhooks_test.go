package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/retreat-community/lanscape/internal/catalog"
	"github.com/retreat-community/lanscape/internal/notify"
	"github.com/retreat-community/lanscape/internal/store"
)

func TestEventWebhooks(t *testing.T) {
	s, _ := newTestServer(t)
	s.ctx = context.Background()
	var mu sync.Mutex
	got := map[string]json.RawMessage{}
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ev map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&ev)
		var name string
		_ = json.Unmarshal(ev["event"], &name)
		mu.Lock()
		got[name] = ev["incident"]
		if ev["change"] != nil {
			got[name] = ev["change"]
		}
		mu.Unlock()
	}))
	defer hook.Close()
	st := DefaultSettings
	st.Webhooks = []string{hook.URL}
	if err := s.store.SetSetting(context.Background(), "settings", st); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s.addChange(ctx, store.Change{Kind: catalog.ChangeDeviceNew, Subject: "printer", Detail: "192.168.1.50"})
	s.addChange(ctx, store.Change{Kind: catalog.ChangePortOpened, Subject: "nas", Detail: "22"})
	s.uptime.dispatch(ctx, notify.Message{Event: "incident.opened", Title: "Gitea is down", MonitorID: 3})
	s.uptime.dispatch(ctx, notify.Message{Event: "test", Title: "test"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // an unexpected third event would arrive by now
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got["device.new"] == nil || got["incident.opened"] == nil {
		t.Fatalf("events: %v", got)
	}
	var m notify.Message
	if json.Unmarshal(got["incident.opened"], &m) != nil || m.MonitorID != 3 {
		t.Errorf("incident payload: %s", got["incident.opened"])
	}
}
