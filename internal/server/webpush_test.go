package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebPush(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)

	var key struct {
		Key string `json:"key"`
	}
	if code := do(t, c, "GET", ts.URL+"/api/v1/push/key", nil, &key); code != 200 || len(key.Key) != 87 {
		t.Fatalf("key: %d %q", code, key.Key)
	}
	var again struct {
		Key string `json:"key"`
	}
	do(t, c, "GET", ts.URL+"/api/v1/push/key", nil, &again)
	if again.Key != key.Key {
		t.Error("vapid key is not persistent")
	}

	var got, gone atomic.Int32
	push := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") || r.Header.Get("Content-Encoding") != "aes128gcm" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/gone" {
			gone.Add(1)
			w.WriteHeader(http.StatusGone)
			return
		}
		got.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer push.Close()
	s.pushClient = push.Client()

	sub := func(path string) map[string]any {
		k, _ := ecdh.P256().GenerateKey(rand.Reader)
		auth := make([]byte, 16)
		_, _ = rand.Read(auth)
		return map[string]any{"endpoint": push.URL + path, "keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(auth)}}
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/push/subscribe", map[string]any{"endpoint": "http://x", "keys": map[string]string{}}, nil); code != 400 {
		t.Errorf("bad subscription accepted: %d", code)
	}
	for _, p := range []string{"/ok", "/gone"} {
		if code := do(t, c, "POST", ts.URL+"/api/v1/push/subscribe", sub(p), nil); code != 200 {
			t.Fatalf("subscribe %s: %d", p, code)
		}
	}
	var res struct {
		Sent int `json:"sent"`
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/push/test", nil, &res); code != 200 || res.Sent != 1 || got.Load() != 1 || gone.Load() != 1 {
		t.Fatalf("test: %d %+v got=%d gone=%d", code, res, got.Load(), gone.Load())
	}
	// the expired subscription is dropped
	do(t, c, "POST", ts.URL+"/api/v1/push/test", nil, &res)
	if gone.Load() != 1 || got.Load() != 2 {
		t.Errorf("gone subscription retried: got=%d gone=%d", got.Load(), gone.Load())
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/push/unsubscribe", map[string]string{"endpoint": push.URL + "/ok"}, nil); code != 204 {
		t.Errorf("unsubscribe: %d", code)
	}
	do(t, c, "POST", ts.URL+"/api/v1/push/test", nil, &res)
	if res.Sent != 0 {
		t.Errorf("sent after unsubscribe: %+v", res)
	}
}
