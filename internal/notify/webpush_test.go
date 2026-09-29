package notify

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/hkdf"
)

// decrypt is the user agent side of RFC 8291.
func decrypt(t *testing.T, uaKey *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	salt, idlen := body[:16], int(body[20])
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs != 4096 {
		t.Fatalf("record size %d", rs)
	}
	asPub := body[21 : 21+idlen]
	ct := body[21+idlen:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := uaKey.ECDH(as)
	info := append(append([]byte("WebPush: info\x00"), uaKey.PublicKey().Bytes()...), asPub...)
	ikm := make([]byte, 32)
	_, _ = io.ReadFull(hkdf.New(sha256.New, shared, auth, info), ikm)
	cek, nonce := make([]byte, 16), make([]byte, 12)
	_, _ = io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")), cek)
	_, _ = io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")), nonce)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if pt[len(pt)-1] != 2 {
		t.Fatal("missing record delimiter")
	}
	return pt[:len(pt)-1]
}

func TestWebPush(t *testing.T) {
	uaKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	var sub Subscription
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(auth)

	v, err := NewVAPID("mailto:admin@example.org")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := ParseVAPID(v.MarshalPrivate(), v.Subject)
	if err != nil || v2.PublicKey() != v.PublicKey() {
		t.Fatalf("round trip: %v", err)
	}
	var got []byte
	var hdr http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		hdr = r.Header
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	sub.Endpoint = srv.URL + "/push/abc"
	if err := v2.Push(context.Background(), srv.Client(), sub, []byte(`{"title":"Gitea is down"}`), time.Hour); err != nil {
		t.Fatal(err)
	}
	if string(decrypt(t, uaKey, auth, got)) != `{"title":"Gitea is down"}` {
		t.Error("payload mismatch")
	}
	if hdr.Get("Content-Encoding") != "aes128gcm" || hdr.Get("TTL") != "3600" {
		t.Errorf("headers: %v", hdr)
	}
	// VAPID: "vapid t=<jwt>, k=<public key>"; verify the ES256 signature
	a := hdr.Get("Authorization")
	jwt := strings.TrimPrefix(strings.Split(a, ",")[0], "vapid t=")
	parts := strings.Split(jwt, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&v.Private.PublicKey, h[:], r, s) || !strings.Contains(a, "k="+v.PublicKey()) {
		t.Errorf("vapid header: %s", a)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !strings.Contains(string(claims), `"aud":"`+srv.URL+`"`) {
		t.Errorf("claims: %s", claims)
	}
	sub.Endpoint = srv.URL + "/gone"
	if err := v.Push(context.Background(), srv.Client(), sub, []byte("x"), time.Minute); !errors.Is(err, ErrGone) {
		t.Errorf("gone: %v", err)
	}
	sub.Endpoint = "http://insecure/push"
	if err := v.Push(context.Background(), nil, sub, []byte("x"), time.Minute); err == nil {
		t.Error("plain http endpoint accepted")
	}
}
