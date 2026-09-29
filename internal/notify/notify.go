// Package notify delivers incident notifications to Telegram, webhooks, email and ntfy.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Channel types.
const (
	Telegram = "telegram"
	Webhook  = "webhook"
	Email    = "email"
	Ntfy     = "ntfy"
	Gotify   = "gotify"
	Discord  = "discord"
	Slack    = "slack"
	Matrix   = "matrix"
)

// Types lists supported channel types.
var Types = []string{Telegram, Webhook, Email, Ntfy, Gotify, Discord, Slack, Matrix}

// Severity of a message.
const (
	SevDown = "down"
	SevUp   = "up"
	SevInfo = "info"
)

// Message is a notification.
type Message struct {
	Event      string `json:"event"` // incident.opened, incident.resolved, incident.reminder, test
	Severity   string `json:"severity"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	URL        string `json:"url,omitempty"`
	IncidentID int64  `json:"incident_id,omitempty"`
	MonitorID  int64  `json:"monitor_id,omitempty"`
	Monitor    string `json:"monitor,omitempty"`
	At         int64  `json:"at"`
}

// Common holds settings shared by all channel types.
type Common struct {
	Quiet         string  `json:"quiet,omitempty"`          // "22:00-07:00" local time: nothing is sent
	RepeatMinutes int     `json:"repeat_minutes,omitempty"` // re-notify unacknowledged incidents (escalation)
	Monitors      []int64 `json:"monitors,omitempty"`       // empty = all
	NoResolved    bool    `json:"no_resolved,omitempty"`    // do not send "resolved"
}

// InQuiet reports whether t falls into the quiet hours.
func (c *Common) InQuiet(t time.Time) bool {
	from, to, ok := parseRange(c.Quiet)
	if !ok {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if from <= to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

// Wants reports whether the channel is subscribed to a monitor.
func (c *Common) Wants(monitorID int64) bool {
	if len(c.Monitors) == 0 || monitorID == 0 {
		return true
	}
	for _, id := range c.Monitors {
		if id == monitorID {
			return true
		}
	}
	return false
}

func parseRange(s string) (int, int, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, false
	}
	from, ok1 := parseHM(a)
	to, ok2 := parseHM(b)
	return from, to, ok1 && ok2
}

func parseHM(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

type telegramCfg struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	APIURL   string `json:"api_url,omitempty"`
}

type webhookCfg struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Secret  string            `json:"secret,omitempty"` // HMAC-SHA256 of the body in X-Lanscape-Signature
}

type emailCfg struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	TLS      string   `json:"tls,omitempty"` // starttls (default), tls, none
}

type ntfyCfg struct {
	URL      string `json:"url,omitempty"` // default https://ntfy.sh
	Topic    string `json:"topic"`
	Token    string `json:"token,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

// secretFields are never returned by the API.
var secretFields = map[string][]string{
	Telegram: {"bot_token"},
	Webhook:  {"secret"},
	Email:    {"password"},
	Ntfy:     {"token"},
	Gotify:   {"token"},
	Discord:  {"url"},
	Slack:    {"url"},
	Matrix:   {"access_token"},
}

// SecretFields returns the secret configuration fields per channel type.
func SecretFields() map[string][]string {
	out := make(map[string][]string, len(secretFields))
	for k, v := range secretFields {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Mask is shown instead of stored secrets.
const Mask = "********"

// Redact replaces secrets in a channel configuration.
func Redact(typ string, cfg json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(cfg, &m) != nil {
		return json.RawMessage("{}")
	}
	for _, f := range secretFields[typ] {
		if v, ok := m[f].(string); ok && v != "" {
			m[f] = Mask
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// KeepSecrets copies secrets from old into cfg where cfg carries the mask.
func KeepSecrets(typ string, cfg, old json.RawMessage) json.RawMessage {
	var m, o map[string]any
	if json.Unmarshal(cfg, &m) != nil || json.Unmarshal(old, &o) != nil {
		return cfg
	}
	for _, f := range secretFields[typ] {
		if m[f] == Mask {
			m[f] = o[f]
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// StripSecrets removes secrets from a channel configuration (for exports).
func StripSecrets(typ string, cfg json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(cfg, &m)
	for _, f := range secretFields[typ] {
		delete(m, f)
	}
	return m
}

// FillSecrets copies secrets from old into cfg where cfg leaves them out, empty or masked.
func FillSecrets(typ string, cfg map[string]any, old json.RawMessage) {
	var o map[string]any
	_ = json.Unmarshal(old, &o)
	for _, f := range secretFields[typ] {
		if v, _ := cfg[f].(string); v == "" || v == Mask {
			if ov, ok := o[f]; ok {
				cfg[f] = ov
			}
		}
	}
}

// New validates a configuration and returns a sender.
func New(typ string, cfg json.RawMessage, client *http.Client) (Sender, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	switch typ {
	case Telegram:
		var c telegramCfg
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		if c.BotToken == "" || c.ChatID == "" {
			return nil, errors.New("telegram: bot_token and chat_id are required")
		}
		if c.APIURL == "" {
			c.APIURL = "https://api.telegram.org"
		}
		return &telegram{c, client}, nil
	case Webhook:
		var c webhookCfg
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			return nil, errors.New("webhook: url must be http(s)")
		}
		return &webhook{c, client}, nil
	case Email:
		var c emailCfg
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		if c.Host == "" || c.From == "" || len(c.To) == 0 {
			return nil, errors.New("email: host, from and to are required")
		}
		if c.Port == 0 {
			c.Port = 587
			if c.TLS == "tls" {
				c.Port = 465
			}
		}
		return &email{c}, nil
	case Ntfy:
		var c ntfyCfg
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		if c.Topic == "" {
			return nil, errors.New("ntfy: topic is required")
		}
		if c.URL == "" {
			c.URL = "https://ntfy.sh"
		}
		return &ntfy{c, client}, nil
	case Gotify:
		var c gotifyCfg
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		if !httpURL(c.URL) || c.Token == "" {
			return nil, errors.New("gotify: url and application token are required")
		}
		return &gotify{c, client}, nil
	case Discord, Slack:
		var c struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(c.URL, "https://") {
			return nil, fmt.Errorf("%s: webhook url must be https", typ)
		}
		return &chatHook{typ: typ, url: c.URL, cl: client}, nil
	case Matrix:
		var c matrixCfg
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, err
		}
		if !httpURL(c.Homeserver) || c.AccessToken == "" || c.RoomID == "" {
			return nil, errors.New("matrix: homeserver, access_token and room_id are required")
		}
		return &matrix{c, client}, nil
	}
	return nil, fmt.Errorf("unknown channel type %q", typ)
}

func httpURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func plain(m Message) string {
	text := icon(m.Severity) + " " + m.Title
	if m.Text != "" {
		text += "\n" + m.Text
	}
	if m.URL != "" {
		text += "\n" + m.URL
	}
	return text
}

type gotifyCfg struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Priority int    `json:"priority,omitempty"`
}

type gotify struct {
	c  gotifyCfg
	cl *http.Client
}

func (g *gotify) Send(ctx context.Context, m Message) error {
	prio := g.c.Priority
	if prio == 0 {
		prio = 5
		if m.Severity == SevDown {
			prio = 8
		}
	}
	msg := m.Text
	if m.URL != "" {
		msg += "\n" + m.URL
	}
	body, _ := json.Marshal(map[string]any{"title": m.Title, "message": msg, "priority": prio})
	err := post(ctx, g.cl, strings.TrimRight(g.c.URL, "/")+"/message", body,
		map[string]string{"Content-Type": "application/json", "X-Gotify-Key": g.c.Token})
	if err != nil {
		return fmt.Errorf("gotify: %w", err)
	}
	return nil
}

// chatHook posts to Discord or Slack incoming webhooks.
type chatHook struct {
	typ, url string
	cl       *http.Client
}

func (h *chatHook) Send(ctx context.Context, m Message) error {
	var body []byte
	if h.typ == Discord {
		body, _ = json.Marshal(map[string]any{"content": plain(m), "allowed_mentions": map[string]any{"parse": []string{}}})
	} else {
		body, _ = json.Marshal(map[string]any{"text": plain(m)})
	}
	if err := post(ctx, h.cl, h.url, body, map[string]string{"Content-Type": "application/json"}); err != nil {
		// the webhook URL is the secret
		return errors.New(h.typ + ": " + strings.ReplaceAll(err.Error(), h.url, Mask))
	}
	return nil
}

type matrixCfg struct {
	Homeserver  string `json:"homeserver"`
	AccessToken string `json:"access_token"`
	RoomID      string `json:"room_id"`
}

type matrix struct {
	c  matrixCfg
	cl *http.Client
}

func (x *matrix) Send(ctx context.Context, m Message) error {
	txn := strconv.FormatInt(time.Now().UnixNano(), 36)
	u := strings.TrimRight(x.c.Homeserver, "/") + "/_matrix/client/v3/rooms/" + url.PathEscape(x.c.RoomID) + "/send/m.room.message/" + txn
	body, _ := json.Marshal(map[string]string{"msgtype": "m.text", "body": plain(m)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+x.c.AccessToken)
	resp, err := x.cl.Do(req)
	if err != nil {
		return fmt.Errorf("matrix: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("matrix: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func icon(sev string) string {
	switch sev {
	case SevDown:
		return "🔴"
	case SevUp:
		return "🟢"
	}
	return "ℹ️"
}

func post(ctx context.Context, cl *http.Client, url string, body []byte, hdr map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "lanscape")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

type telegram struct {
	c  telegramCfg
	cl *http.Client
}

func (t *telegram) Send(ctx context.Context, m Message) error {
	text := plain(m)
	body, _ := json.Marshal(map[string]any{"chat_id": t.c.ChatID, "text": text, "disable_web_page_preview": true})
	err := post(ctx, t.cl, strings.TrimRight(t.c.APIURL, "/")+"/bot"+t.c.BotToken+"/sendMessage", body,
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		// never leak the bot token through error messages
		return errors.New("telegram: " + strings.ReplaceAll(err.Error(), t.c.BotToken, Mask))
	}
	return nil
}

type webhook struct {
	c  webhookCfg
	cl *http.Client
}

func (w *webhook) Send(ctx context.Context, m Message) error {
	body, _ := json.Marshal(m)
	hdr := map[string]string{"Content-Type": "application/json"}
	for k, v := range w.c.Headers {
		hdr[k] = v
	}
	if w.c.Secret != "" {
		mac := hmac.New(sha256.New, []byte(w.c.Secret))
		mac.Write(body)
		hdr["X-Lanscape-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	if err := post(ctx, w.cl, w.c.URL, body, hdr); err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	return nil
}

type ntfy struct {
	c  ntfyCfg
	cl *http.Client
}

func (n *ntfy) Send(ctx context.Context, m Message) error {
	text := m.Text
	if m.URL != "" {
		text += "\n" + m.URL
	}
	hdr := map[string]string{"Title": m.Title, "Tags": map[string]string{SevDown: "red_circle", SevUp: "green_circle"}[m.Severity]}
	if hdr["Tags"] == "" {
		hdr["Tags"] = "information_source"
	}
	if n.c.Priority > 0 {
		hdr["Priority"] = strconv.Itoa(n.c.Priority)
	} else if m.Severity == SevDown {
		hdr["Priority"] = "4"
	}
	if n.c.Token != "" {
		hdr["Authorization"] = "Bearer " + n.c.Token
	}
	if m.URL != "" {
		hdr["Click"] = m.URL
	}
	if err := post(ctx, n.cl, strings.TrimRight(n.c.URL, "/")+"/"+n.c.Topic, []byte(strings.TrimSpace(text)), hdr); err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	return nil
}

type email struct{ c emailCfg }

func (e *email) Send(ctx context.Context, m Message) error {
	addr := net.JoinHostPort(e.c.Host, strconv.Itoa(e.c.Port))
	d := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	tc := &tls.Config{ServerName: e.c.Host, MinVersion: tls.VersionTLS12}
	if e.c.TLS == "tls" {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: tc}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, e.c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("email: %w", err)
	}
	defer func() { _ = c.Close() }()
	if e.c.TLS == "" || e.c.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("email: server does not offer STARTTLS (set tls to \"none\" to send in clear text)")
		}
		if err := c.StartTLS(tc); err != nil {
			return fmt.Errorf("email: starttls: %w", err)
		}
	}
	if e.c.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.c.Username, e.c.Password, e.c.Host)); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}
	if err := c.Mail(e.c.From); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	for _, to := range e.c.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("email: %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if _, err := w.Write(e.message(m)); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	return c.Quit()
}

func (e *email) message(m Message) []byte {
	var b bytes.Buffer
	clean := func(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\n", clean(e.c.From), clean(strings.Join(e.c.To, ", ")))
	fmt.Fprintf(&b, "Subject: =?UTF-8?B?%s?=\r\n", b64(icon(m.Severity)+" "+clean(m.Title)))
	fmt.Fprintf(&b, "Date: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("Content-Transfer-Encoding: 8bit\r\nAuto-Submitted: auto-generated\r\n\r\n")
	body := m.Text
	if m.URL != "" {
		body += "\n\n" + m.URL
	}
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.Bytes()
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
