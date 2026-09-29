package notify

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capture struct {
	path, body string
	hdr        http.Header
}

func server(t *testing.T, code int) (*httptest.Server, chan capture) {
	ch := make(chan capture, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- capture{r.URL.Path, string(b), r.Header}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

var msg = Message{Event: "incident.opened", Severity: SevDown, Title: "Gitea is down", Text: "HTTP 502", URL: "http://panel/m/1",
	MonitorID: 1, At: 1}

func TestTelegram(t *testing.T) {
	srv, ch := server(t, 200)
	s, err := New(Telegram, json.RawMessage(`{"bot_token":"123:abc","chat_id":"42","api_url":"`+srv.URL+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	c := <-ch
	if c.path != "/bot123:abc/sendMessage" || !strings.Contains(c.body, `"chat_id":"42"`) || !strings.Contains(c.body, "Gitea is down") {
		t.Errorf("telegram request: %+v", c)
	}
	srv2, _ := server(t, 401)
	s, _ = New(Telegram, json.RawMessage(`{"bot_token":"123:abc","chat_id":"42","api_url":"`+srv2.URL+`"}`), nil)
	if err := s.Send(context.Background(), msg); err == nil || strings.Contains(err.Error(), "123:abc") {
		t.Errorf("error leaks token or is nil: %v", err)
	}
}

func TestWebhookSignature(t *testing.T) {
	srv, ch := server(t, 204)
	s, err := New(Webhook, json.RawMessage(`{"url":"`+srv.URL+`/hook","secret":"k","headers":{"X-Test":"1"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	c := <-ch
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write([]byte(c.body))
	if c.hdr.Get("X-Lanscape-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || c.hdr.Get("X-Test") != "1" {
		t.Errorf("headers: %v", c.hdr)
	}
	var m Message
	if json.Unmarshal([]byte(c.body), &m) != nil || m.Event != "incident.opened" || m.MonitorID != 1 {
		t.Errorf("body: %s", c.body)
	}
}

func TestNtfy(t *testing.T) {
	srv, ch := server(t, 200)
	s, _ := New(Ntfy, json.RawMessage(`{"url":"`+srv.URL+`","topic":"lab","token":"tk"}`), nil)
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	c := <-ch
	if c.path != "/lab" || c.hdr.Get("Title") != "Gitea is down" || c.hdr.Get("Priority") != "4" ||
		c.hdr.Get("Authorization") != "Bearer tk" || !strings.HasPrefix(c.body, "HTTP 502") {
		t.Errorf("ntfy: %+v", c)
	}
}

// fakeSMTP accepts one message without TLS and returns the DATA section.
func fakeSMTP(t *testing.T) (string, chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	out := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		r := bufio.NewReader(c)
		w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if l == ".\r\n" {
					inData = false
					out <- data.String()
					w("250 queued")
					continue
				}
				data.WriteString(l)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(l)); {
			case strings.HasPrefix(cmd, "EHLO"):
				w("250-fake")
				w("250 AUTH PLAIN")
			case strings.HasPrefix(cmd, "DATA"):
				inData = true
				w("354 go ahead")
			case strings.HasPrefix(cmd, "QUIT"):
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().String(), out
}

func TestEmail(t *testing.T) {
	addr, out := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	s, err := New(Email, json.RawMessage(`{"host":"`+host+`","port":`+port+`,"from":"lanscape@lab","to":["me@lab"],"tls":"none"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	data := <-out
	if !strings.Contains(data, "To: me@lab") || !strings.Contains(data, "Subject: =?UTF-8?B?") || !strings.Contains(data, "HTTP 502") {
		t.Errorf("mail: %q", data)
	}
	// STARTTLS is required by default
	addr2, _ := fakeSMTP(t)
	host, port, _ = net.SplitHostPort(addr2)
	s, _ = New(Email, json.RawMessage(`{"host":"`+host+`","port":`+port+`,"from":"a@b","to":["c@d"]}`), nil)
	if err := s.Send(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("starttls not enforced: %v", err)
	}
}

func TestValidationAndSecrets(t *testing.T) {
	for typ, cfg := range map[string]string{Telegram: `{}`, Webhook: `{"url":"ftp://x"}`, Email: `{"host":"h"}`, Ntfy: `{}`, "x": `{}`} {
		if _, err := New(typ, json.RawMessage(cfg), nil); err == nil {
			t.Errorf("%s %s accepted", typ, cfg)
		}
	}
	red := Redact(Telegram, json.RawMessage(`{"bot_token":"secret","chat_id":"1"}`))
	if strings.Contains(string(red), "secret") || !strings.Contains(string(red), Mask) {
		t.Errorf("redact: %s", red)
	}
	kept := KeepSecrets(Telegram, red, json.RawMessage(`{"bot_token":"secret","chat_id":"1"}`))
	if !strings.Contains(string(kept), `"bot_token":"secret"`) {
		t.Errorf("keep: %s", kept)
	}
}

func TestQuietAndFilter(t *testing.T) {
	c := Common{Quiet: "22:00-07:00", Monitors: []int64{3}}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	if !c.InQuiet(at(23, 0)) || !c.InQuiet(at(6, 59)) || c.InQuiet(at(7, 0)) || c.InQuiet(at(12, 0)) {
		t.Error("overnight quiet hours")
	}
	d := Common{Quiet: "12:00-13:00"}
	if !d.InQuiet(at(12, 30)) || d.InQuiet(at(13, 30)) {
		t.Error("daytime quiet hours")
	}
	if (&Common{Quiet: "bad"}).InQuiet(at(1, 0)) {
		t.Error("invalid range must not mute")
	}
	if !c.Wants(3) || c.Wants(4) || !(&Common{}).Wants(4) {
		t.Error("monitor filter")
	}
}

func TestChatChannels(t *testing.T) {
	ch := make(chan capture, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- capture{r.Method + " " + r.URL.Path, string(b), r.Header}
	}))
	defer srv.Close()
	cl := srv.Client()
	ctx := context.Background()

	s, err := New(Gotify, json.RawMessage(`{"url":"`+srv.URL+`","token":"gt"}`), cl)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	c := <-ch
	if c.path != "POST /message" || c.hdr.Get("X-Gotify-Key") != "gt" || !strings.Contains(c.body, `"priority":8`) {
		t.Errorf("gotify: %+v", c)
	}

	for _, typ := range []string{Discord, Slack} {
		s, err := New(typ, json.RawMessage(`{"url":"`+srv.URL+`/hook/secret"}`), cl)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Send(ctx, msg); err != nil {
			t.Fatal(err)
		}
		c := <-ch
		key := map[string]string{Discord: `"content":`, Slack: `"text":`}[typ]
		if c.path != "POST /hook/secret" || !strings.Contains(c.body, key) || !strings.Contains(c.body, "Gitea is down") {
			t.Errorf("%s: %+v", typ, c)
		}
	}
	if _, err := New(Discord, json.RawMessage(`{"url":"http://insecure"}`), nil); err == nil {
		t.Error("plain http discord webhook accepted")
	}

	s, err = New(Matrix, json.RawMessage(`{"homeserver":"`+srv.URL+`","access_token":"mx","room_id":"!room:example.org"}`), cl)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	c = <-ch
	if !strings.HasPrefix(c.path, "PUT /_matrix/client/v3/rooms/!room:example.org/send/m.room.message/") ||
		c.hdr.Get("Authorization") != "Bearer mx" || !strings.Contains(c.body, `"msgtype":"m.text"`) {
		t.Errorf("matrix: %+v", c)
	}
	if red := Redact(Discord, json.RawMessage(`{"url":"https://discord.com/api/webhooks/1/abc"}`)); strings.Contains(string(red), "abc") {
		t.Errorf("discord url not redacted: %s", red)
	}
}
