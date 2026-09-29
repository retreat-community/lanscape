package notify

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

// VAPID is the server identity for Web Push (RFC 8292).
type VAPID struct {
	Private *ecdsa.PrivateKey
	Subject string // mailto: or https: contact
}

// NewVAPID generates a key pair.
func NewVAPID(subject string) (*VAPID, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &VAPID{Private: k, Subject: subject}, nil
}

// PublicKey is the application server key for PushManager.subscribe (uncompressed point, base64url).
func (v *VAPID) PublicKey() string {
	pub, err := v.Private.PublicKey.ECDH()
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(pub.Bytes())
}

// MarshalPrivate encodes the private scalar for storage.
func (v *VAPID) MarshalPrivate() string {
	return base64.RawURLEncoding.EncodeToString(v.Private.D.FillBytes(make([]byte, 32)))
}

// ParseVAPID restores a key pair.
func ParseVAPID(private, subject string) (*VAPID, error) {
	d, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil || len(d) != 32 {
		return nil, errors.New("webpush: bad private key")
	}
	k := new(ecdsa.PrivateKey)
	k.Curve = elliptic.P256()
	k.D = new(big.Int).SetBytes(d)
	k.X, k.Y = k.Curve.ScalarBaseMult(d) //nolint:staticcheck // no other way to derive the public point of a raw scalar
	return &VAPID{Private: k, Subject: subject}, nil
}

// token returns the VAPID authorization header value for a push service origin.
func (v *VAPID) token(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString
	header := b64([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": time.Now().Add(12 * time.Hour).Unix(),
		"sub": v.Subject})
	signing := header + "." + b64(claims)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, v.Private, h[:])
	if err != nil {
		return "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return "vapid t=" + signing + "." + b64(sig) + ", k=" + v.PublicKey(), nil
}

// Subscription is a browser push subscription (PushSubscription.toJSON()).
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// Encrypt encrypts a payload for a subscription (RFC 8291, aes128gcm content coding).
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	uaPub, err := decodeB64(sub.Keys.P256dh)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	authSecret, err := decodeB64(sub.Keys.Auth)
	if err != nil || len(authSecret) < 16 {
		return nil, errors.New("webpush: bad auth secret")
	}
	curve := ecdh.P256()
	uaKey, err := curve.NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	asKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := asKey.ECDH(uaKey)
	if err != nil {
		return nil, err
	}
	asPub := asKey.PublicKey().Bytes()
	// IKM = HKDF(auth_secret, ecdh_secret, "WebPush: info" || 0x00 || ua_public || as_public, 32)
	info := append(append([]byte("WebPush: info\x00"), uaPub...), asPub...)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, authSecret, info), ikm); err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	cek := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")), cek); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")), nonce); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// single record: plaintext || 0x02 (last-record delimiter)
	ct := gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 2), nil)
	var buf bytes.Buffer
	buf.Write(salt)
	_ = binary.Write(&buf, binary.BigEndian, uint32(4096)) // record size
	buf.WriteByte(byte(len(asPub)))
	buf.Write(asPub)
	buf.Write(ct)
	return buf.Bytes(), nil
}

// ErrGone reports an expired subscription (the browser unsubscribed).
var ErrGone = errors.New("webpush: subscription expired")

// Push sends an encrypted message to one subscription.
func (v *VAPID) Push(ctx context.Context, cl *http.Client, sub Subscription, payload []byte, ttl time.Duration) error {
	if !strings.HasPrefix(sub.Endpoint, "https://") {
		return errors.New("webpush: endpoint must be https")
	}
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	auth, err := v.token(sub.Endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", fmt.Sprint(int(ttl.Seconds())))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", auth)
	if cl == nil {
		cl = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
		return ErrGone
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("webpush: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}
