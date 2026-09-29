package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/retreat-community/lanscape/internal/notify"
	"github.com/retreat-community/lanscape/internal/store"
)

// secretKey returns the key that encrypts monitor and channel secrets: LANSCAPE_SECRET_KEY
// (base64, 32 bytes) or <data-dir>/secret.key, created on first start.
func secretKey(cfg Config) ([]byte, error) {
	if cfg.SecretKey != "" {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.SecretKey))
		if err != nil || len(k) != 32 {
			return nil, errors.New("server: the secret key must be 32 bytes in base64 (openssl rand -base64 32)")
		}
		return k, nil
	}
	path := filepath.Join(cfg.DataDir, "secret.key")
	if b, err := os.ReadFile(path); err == nil {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("server: %s is not a valid key", path)
		}
		return k, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// sealStoredSecrets rewrites monitors and channels so that secrets saved in plain text (before
// encryption existed) are encrypted.
func (s *Server) sealStoredSecrets(ctx context.Context) error {
	ms, err := s.store.Monitors(ctx)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if _, err := s.store.SaveMonitor(ctx, m); err != nil {
			return err
		}
	}
	cs, err := s.store.Channels(ctx)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if _, err := s.store.SaveChannel(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// specSecrets walks the secret fields of a monitor spec (and header values).
func specSecrets(m map[string]any, f func(get func() string, set func(string))) {
	for _, k := range store.MonitorSecretFields {
		k := k
		f(func() string { v, _ := m[k].(string); return v }, func(v string) { m[k] = v })
	}
	if h, ok := m["headers"].(map[string]any); ok {
		for k := range h {
			k := k
			f(func() string { v, _ := h[k].(string); return v }, func(v string) { h[k] = v })
		}
	}
}

// maskSpec replaces secrets in a monitor spec by the mask shown in the UI.
func maskSpec(raw json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	specSecrets(m, func(get func() string, set func(string)) {
		if get() != "" {
			set(notify.Mask)
		}
	})
	b, _ := json.Marshal(m)
	return b
}

// keepMaskedSecrets copies stored secrets into a spec where it carries the mask (or, with
// missing, where it leaves the value out, as configuration files do).
func keepMaskedSecrets(raw, old json.RawMessage, missing bool) json.RawMessage {
	var m, o map[string]any
	if json.Unmarshal(raw, &m) != nil || json.Unmarshal(old, &o) != nil {
		return raw
	}
	oldVals := map[string]string{}
	for _, k := range store.MonitorSecretFields {
		if v, _ := o[k].(string); v != "" {
			oldVals[k] = v
			if missing {
				if _, ok := m[k]; !ok {
					m[k] = v
				}
			}
		}
	}
	for _, k := range store.MonitorSecretFields {
		if v, _ := m[k].(string); v == notify.Mask {
			m[k] = oldVals[k]
		}
	}
	if h, ok := m["headers"].(map[string]any); ok {
		oh, _ := o["headers"].(map[string]any)
		for k, v := range h {
			if v == notify.Mask {
				h[k] = oh[k]
			}
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// maskMonitor hides the secrets of a monitor for API responses.
func maskMonitor(m store.Monitor) store.Monitor {
	m.Spec = maskSpec(m.Spec)
	return m
}
