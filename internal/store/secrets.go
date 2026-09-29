package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// sealPrefix marks an encrypted value inside stored JSON.
const sealPrefix = "enc:v1:"

// MonitorSecretFields are the monitor spec fields stored encrypted; header values are
// encrypted as well.
var MonitorSecretFields = []string{"basic_password", "bearer"}

// Secrets encrypts secret fields of monitor specs and channel configurations at rest
// (AES-256-GCM). Values written before a key was configured are read as they are and sealed
// on the next save.
type Secrets struct {
	aead          cipher.AEAD
	ChannelFields map[string][]string // channel type -> secret config fields
}

// NewSecrets creates the sealer from a 32-byte key.
func NewSecrets(key []byte, channelFields map[string][]string) (*Secrets, error) {
	if len(key) != 32 {
		return nil, errors.New("store: the secret key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Secrets{aead: aead, ChannelFields: channelFields}, nil
}

func (x *Secrets) seal(v string) string {
	if x == nil || v == "" || strings.HasPrefix(v, sealPrefix) {
		return v
	}
	nonce := make([]byte, x.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return sealPrefix + base64.RawStdEncoding.EncodeToString(x.aead.Seal(nonce, nonce, []byte(v), nil))
}

func (x *Secrets) open(v string) string {
	if x == nil || !strings.HasPrefix(v, sealPrefix) {
		return v
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(v, sealPrefix))
	n := x.aead.NonceSize()
	if err != nil || len(b) < n {
		return ""
	}
	pt, err := x.aead.Open(nil, b[:n], b[n:], nil)
	if err != nil {
		return "" // another key: the secret has to be entered again
	}
	return string(pt)
}

// transform applies f to the given string fields and, with headers, to the values of the
// "headers" object.
func transform(raw json.RawMessage, fields []string, headers bool, f func(string) string) json.RawMessage {
	if len(fields) == 0 && !headers {
		return raw
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	changed := false
	for _, k := range fields {
		if v, ok := m[k].(string); ok && v != "" {
			m[k], changed = f(v), true
		}
	}
	if h, ok := m["headers"].(map[string]any); ok && headers {
		for k, v := range h {
			if s, ok := v.(string); ok && s != "" {
				h[k], changed = f(s), true
			}
		}
	}
	if !changed {
		return raw
	}
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}

func (s *Store) sealMonitor(spec json.RawMessage) json.RawMessage {
	if s.Secrets == nil {
		return spec
	}
	return transform(spec, MonitorSecretFields, true, s.Secrets.seal)
}

func (s *Store) openMonitor(spec json.RawMessage) json.RawMessage {
	if s.Secrets == nil {
		return spec
	}
	return transform(spec, MonitorSecretFields, true, s.Secrets.open)
}

func (s *Store) sealChannel(typ string, cfg json.RawMessage) json.RawMessage {
	if s.Secrets == nil {
		return cfg
	}
	return transform(cfg, s.Secrets.ChannelFields[typ], false, s.Secrets.seal)
}

func (s *Store) openChannel(typ string, cfg json.RawMessage) json.RawMessage {
	if s.Secrets == nil {
		return cfg
	}
	return transform(cfg, s.Secrets.ChannelFields[typ], false, s.Secrets.open)
}
