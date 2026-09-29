package server

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIdP is a minimal OpenID Connect provider with PKCE.
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]url.Values // code -> authorize params
	claims map[string]any        // extra claims of the next token
	badSig bool
}

func newFakeIdP(t *testing.T) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: k, codes: map[string]url.Values{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig",
			"n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		code := NewSecret("c")
		f.mu.Lock()
		f.codes[code] = r.URL.Query()
		f.mu.Unlock()
		http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?code="+code+"&state="+r.URL.Query().Get("state"), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		q, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") || r.Form.Get("client_secret") != "s3cret" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		c := map[string]any{"iss": f.srv.URL, "aud": "lanscape", "sub": "u-42", "exp": time.Now().Add(time.Hour).Unix(),
			"nonce": q.Get("nonce"), "preferred_username": "alice"}
		for k, v := range f.claims {
			c[k] = v
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(c)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) sign(claims map[string]any) string {
	b64 := base64.RawURLEncoding.EncodeToString
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if f.badSig {
		sig[0] ^= 1
	}
	return in + "." + b64(sig)
}

func TestOIDCLogin(t *testing.T) {
	s, ts := newTestServer(t)
	idp := newFakeIdP(t)
	s.cfg.OIDC = OIDCConfig{Issuer: idp.srv.URL, ClientID: "lanscape", ClientSecret: "s3cret", Name: "Authentik",
		AdminGroups: []string{"lanscape-admins"}, DefaultRole: RoleViewer}

	var info struct {
		Enabled bool   `json:"enabled"`
		Name    string `json:"name"`
	}
	if do(t, client(t), "GET", ts.URL+"/api/v1/auth/oidc", nil, &info); !info.Enabled || info.Name != "Authentik" {
		t.Fatalf("info: %+v", info)
	}
	signIn := func() (*http.Client, string) {
		c := client(t)
		var last string
		c.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
			last = req.URL.String()
			return nil
		}
		resp, err := c.Get(ts.URL + "/api/v1/auth/oidc/login")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return c, last
	}
	me := func(c *http.Client) (string, string) {
		var m struct {
			User struct {
				Username string `json:"username"`
				Role     string `json:"role"`
			} `json:"user"`
		}
		if code := do(t, c, "GET", ts.URL+"/api/v1/auth/me", nil, &m); code != 200 {
			return "", ""
		}
		return m.User.Username, m.User.Role
	}

	c, _ := signIn()
	if name, role := me(c); name != "alice" || role != RoleViewer {
		t.Fatalf("first sign-in: %q %q", name, role)
	}
	// group membership changes the role on the next sign-in; the account is reused
	idp.claims = map[string]any{"groups": []string{"staff", "lanscape-admins"}, "preferred_username": "alice2"}
	c, _ = signIn()
	if name, role := me(c); name != "alice" || role != RoleAdmin {
		t.Fatalf("admin sign-in: %q %q", name, role)
	}
	// an identity never takes over a local account with the same name
	idp.claims = map[string]any{"sub": "u-other", "preferred_username": "admin"}
	c, last := signIn()
	if name, _ := me(c); name != "" || !strings.Contains(last, "error=") {
		t.Fatalf("local account taken over: %q %s", name, last)
	}
	// forged signature and refused default role
	idp.claims, idp.badSig = nil, true
	if c, _ = signIn(); func() string { n, _ := me(c); return n }() != "" {
		t.Fatal("forged token accepted")
	}
	idp.badSig = false
	s.cfg.OIDC.DefaultRole = ""
	idp.claims = map[string]any{"sub": "u-new", "preferred_username": "bob"}
	if c, _ = signIn(); func() string { n, _ := me(c); return n }() != "" {
		t.Fatal("user outside the allowed groups signed in")
	}
	// replayed callback state
	resp, err := client(t).Get(ts.URL + "/api/v1/auth/oidc/callback?code=x&state=unknown")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// OIDC accounts cannot sign in with a password
	if code := do(t, client(t), "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "alice", Password: ""}, nil); code == 200 {
		t.Error("password sign-in for an oidc account")
	}
}

func TestVerifyJWTES256(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	b64 := base64.RawURLEncoding.EncodeToString
	pub, err := jwk{Kty: "EC", Crv: "P-256", X: b64(k.X.FillBytes(make([]byte, 32))), Y: b64(k.Y.FillBytes(make([]byte, 32)))}.public()
	if err != nil {
		t.Fatal(err)
	}
	h, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": "e"})
	c, _ := json.Marshal(map[string]any{"sub": "x"})
	in := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(in))
	r, s, _ := ecdsa.Sign(rand.Reader, k, sum[:])
	tok := in + "." + b64(append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...))
	if claims, _, err := verifyJWT(tok, map[string]crypto.PublicKey{"e": pub}); err != nil || claims["sub"] != "x" {
		t.Fatalf("%v %v", claims, err)
	}
	if _, _, err := verifyJWT(strings.Replace(tok, ".", ".x", 1), map[string]crypto.PublicKey{"e": pub}); err == nil {
		t.Error("tampered token accepted")
	}
	// "none" and HMAC are refused
	none := b64([]byte(`{"alg":"none","kid":"e"}`)) + "." + b64(c) + "."
	if _, _, err := verifyJWT(none, map[string]crypto.PublicKey{"e": pub}); err == nil {
		t.Error("alg none accepted")
	}
}

func TestCheckClaims(t *testing.T) {
	now := time.Now()
	ok := map[string]any{"iss": "https://id/", "aud": []any{"x", "lanscape"}, "exp": float64(now.Add(time.Minute).Unix()), "nonce": "n", "sub": "s"}
	if err := checkClaims(ok, "https://id", "lanscape", "n", now); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(map[string]any){
		"iss":   func(c map[string]any) { c["iss"] = "https://evil" },
		"aud":   func(c map[string]any) { c["aud"] = "other" },
		"exp":   func(c map[string]any) { c["exp"] = float64(now.Add(-time.Hour).Unix()) },
		"nonce": func(c map[string]any) { c["nonce"] = "z" },
		"sub":   func(c map[string]any) { delete(c, "sub") },
	} {
		c := map[string]any{}
		for k, v := range ok {
			c[k] = v
		}
		mut(c)
		if checkClaims(c, "https://id", "lanscape", "n", now) == nil {
			t.Errorf("%s not checked", name)
		}
	}
}
