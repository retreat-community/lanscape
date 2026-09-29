package server

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/retreat-community/lanscape/internal/notify"
)

func TestSecretsAtRest(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	ctx := context.Background()

	spec := map[string]any{"type": "http", "target": "https://nas.example/api", "basic_user": "admin", "basic_password": "hunter2",
		"headers": map[string]string{"X-Api-Key": "k-123"}}
	var created struct {
		ID   int64           `json:"id"`
		Spec json.RawMessage `json:"spec"`
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": "NAS API", "spec": spec}, &created); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if strings.Contains(string(created.Spec), "hunter2") || !strings.Contains(string(created.Spec), notify.Mask) {
		t.Errorf("secret returned: %s", created.Spec)
	}
	var raw string
	if err := s.store.QueryRow(ctx, `SELECT spec FROM monitors WHERE id=?`, created.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "hunter2") || strings.Contains(raw, "k-123") || !strings.Contains(raw, "enc:v1:") {
		t.Errorf("stored in plain text: %s", raw)
	}
	// the engine sees the real values
	m, _ := s.uptime.get(created.ID)
	if !strings.Contains(string(m.Spec), "hunter2") || !strings.Contains(string(m.Spec), "k-123") {
		t.Errorf("engine spec: %s", m.Spec)
	}
	// saving the masked form back keeps the secrets
	var masked map[string]any
	_ = json.Unmarshal(created.Spec, &masked)
	masked["target"] = "https://nas.example/api/v2"
	if code := do(t, c, "PUT", ts.URL+"/api/v1/monitors/"+strconv.FormatInt(created.ID, 10), map[string]any{"name": "NAS API", "spec": masked}, nil); code != 200 {
		t.Fatalf("update: %d", code)
	}
	m, _ = s.uptime.get(created.ID)
	if !strings.Contains(string(m.Spec), "hunter2") || !strings.Contains(string(m.Spec), "/api/v2") {
		t.Errorf("secret lost on update: %s", m.Spec)
	}
	// the configuration export carries masks and applies as a no-op
	resp, _ := c.Get(ts.URL + "/api/v1/config")
	exported, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(exported), "hunter2") || strings.Contains(string(exported), "k-123") {
		t.Fatalf("export leaks secrets:\n%s", exported)
	}
	if code, p := applyConfig(t, c, ts.URL, string(exported), ""); code != 200 || actions(p)["update"] != 0 {
		t.Errorf("export re-apply: %d %+v", code, p)
	}
	m, _ = s.uptime.get(created.ID)
	if !strings.Contains(string(m.Spec), "hunter2") {
		t.Errorf("secret lost on config apply: %s", m.Spec)
	}
	// channels are sealed too
	ch := map[string]any{"name": "tg", "type": "telegram", "enabled": true, "config": map[string]any{"bot_token": "123:abc", "chat_id": "1"}}
	if code := do(t, c, "POST", ts.URL+"/api/v1/channels", ch, nil); code != 200 && code != 201 {
		t.Fatalf("channel: %d", code)
	}
	if err := s.store.QueryRow(ctx, `SELECT config FROM channels WHERE name=?`, "tg").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "123:abc") {
		t.Errorf("channel secret in plain text: %s", raw)
	}
	chans, _ := s.store.Channels(ctx)
	if !strings.Contains(string(chans[0].Config), "123:abc") {
		t.Errorf("channel secret not readable: %s", chans[0].Config)
	}
	// the key file is private
	fi, err := os.Stat(filepath.Join(s.cfg.DataDir, "secret.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key file: %v %v", fi, err)
	}
}
