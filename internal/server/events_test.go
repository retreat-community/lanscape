package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestEventsWebSocket(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws"
	dial := func(opts *websocket.DialOptions) (*websocket.Conn, error) {
		ws, res, err := websocket.Dial(ctx, url, opts)
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		return ws, err
	}
	if _, err := dial(nil); err == nil {
		t.Fatal("unauthenticated websocket accepted")
	}
	ws, err := dial(&websocket.DialOptions{HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	// the subscription is registered right after the handshake; publish until it arrives
	go func() {
		for ctx.Err() == nil {
			s.events.Publish("monitor", map[string]any{"id": 7})
			time.Sleep(50 * time.Millisecond)
		}
	}()
	var ev struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := wsjson.Read(ctx, ws, &ev); err != nil || ev.Type != "monitor" || ev.Data["id"] != float64(7) {
		t.Fatalf("event: %+v %v", ev, err)
	}
	// a page of another origin cannot open the stream with the session cookie
	h := http.Header{"Origin": {"https://evil.example"}}
	if _, err := dial(&websocket.DialOptions{HTTPClient: c, HTTPHeader: h}); err == nil {
		t.Error("cross-origin websocket accepted")
	}
}
