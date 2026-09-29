package server

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/store"
)

// OIDCConfig enables sign-in through an OpenID Connect provider (Authentik, Keycloak,
// Authelia, Google, Entra ID …). Roles come from a claim (groups by default).
type OIDCConfig struct {
	Issuer         string
	ClientID       string
	ClientSecret   string
	Name           string   // button label, "SSO" by default
	RoleClaim      string   // claim with group names, "groups" by default
	AdminGroups    []string // members become administrators
	OperatorGroups []string // members become operators
	DefaultRole    string   // everybody else: viewer (default) or "" to refuse sign-in
}

// Enabled reports whether OIDC is configured.
func (c OIDCConfig) Enabled() bool { return c.Issuer != "" && c.ClientID != "" }

type oidcProvider struct {
	AuthURL  string `json:"authorization_endpoint"`
	TokenURL string `json:"token_endpoint"`
	JWKSURL  string `json:"jwks_uri"`
	Issuer   string `json:"issuer"`
}

type oidcPending struct {
	nonce, verifier, redirect string
	exp                       time.Time
}

// oidcState caches the provider metadata and keys and tracks sign-ins in progress.
type oidcState struct {
	mu      sync.Mutex
	prov    *oidcProvider
	keys    map[string]crypto.PublicKey
	fetched time.Time
	pending map[string]oidcPending
	hc      *http.Client
}

func (o *oidcState) client() *http.Client {
	if o.hc != nil {
		return o.hc
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (o *oidcState) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// provider returns the discovery document and signing keys, refreshed hourly (or when a token
// names an unknown key).
func (o *oidcState) provider(ctx context.Context, issuer string, force bool) (*oidcProvider, map[string]crypto.PublicKey, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.prov != nil && !force && time.Since(o.fetched) < time.Hour {
		return o.prov, o.keys, nil
	}
	var p oidcProvider
	if err := o.getJSON(ctx, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", &p); err != nil {
		return nil, nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if strings.TrimRight(p.Issuer, "/") != strings.TrimRight(issuer, "/") || p.AuthURL == "" || p.TokenURL == "" || p.JWKSURL == "" {
		return nil, nil, errors.New("oidc discovery: issuer mismatch or missing endpoints")
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := o.getJSON(ctx, p.JWKSURL, &set); err != nil {
		return nil, nil, fmt.Errorf("oidc keys: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if pub, err := k.public(); err == nil && (k.Use == "" || k.Use == "sig") {
			keys[k.Kid] = pub
		}
	}
	o.prov, o.keys, o.fetched = &p, keys, time.Now()
	return o.prov, o.keys, nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jwk) public() (crypto.PublicKey, error) {
	b64 := base64.RawURLEncoding.DecodeString
	switch k.Kty {
	case "RSA":
		n, err1 := b64(k.N)
		e, err2 := b64(k.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			return nil, errors.New("bad rsa key")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, errors.New("unsupported curve")
		}
		x, err1 := b64(k.X)
		y, err2 := b64(k.Y)
		if err1 != nil || err2 != nil {
			return nil, errors.New("bad ec key")
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if _, err := pub.ECDH(); err != nil { // rejects points off the curve
			return nil, err
		}
		return pub, nil
	}
	return nil, errors.New("unsupported key type")
}

// verifyJWT checks the signature (RS256 or ES256) and returns the claims.
func verifyJWT(token string, keys map[string]crypto.PublicKey) (map[string]any, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, "", errors.New("malformed token")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &hdr) != nil {
		return nil, "", errors.New("malformed token header")
	}
	key, ok := keys[hdr.Kid]
	if !ok && hdr.Kid == "" && len(keys) == 1 {
		for _, k := range keys {
			key, ok = k, true
		}
	}
	if !ok {
		return nil, hdr.Kid, errUnknownKey
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, "", errors.New("malformed signature")
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch hdr.Alg {
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok || rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig) != nil {
			return nil, "", errors.New("bad signature")
		}
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok || len(sig) != 64 || !ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return nil, "", errors.New("bad signature")
		}
	default:
		return nil, "", fmt.Errorf("unsupported algorithm %q", hdr.Alg)
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, "", errors.New("malformed claims")
	}
	var claims map[string]any
	if err := json.Unmarshal(pb, &claims); err != nil {
		return nil, "", errors.New("malformed claims")
	}
	return claims, hdr.Kid, nil
}

var errUnknownKey = errors.New("unknown signing key")

// checkClaims validates issuer, audience, expiry and nonce.
func checkClaims(c map[string]any, issuer, clientID, nonce string, now time.Time) error {
	if iss, _ := c["iss"].(string); strings.TrimRight(iss, "/") != strings.TrimRight(issuer, "/") {
		return errors.New("wrong issuer")
	}
	audOK := false
	switch aud := c["aud"].(type) {
	case string:
		audOK = aud == clientID
	case []any:
		for _, a := range aud {
			audOK = audOK || a == clientID
		}
	}
	if !audOK {
		return errors.New("wrong audience")
	}
	exp, _ := c["exp"].(float64)
	if now.After(time.Unix(int64(exp), 0).Add(time.Minute)) {
		return errors.New("token expired")
	}
	if n, _ := c["nonce"].(string); n != nonce {
		return errors.New("nonce mismatch")
	}
	if sub, _ := c["sub"].(string); sub == "" {
		return errors.New("no subject")
	}
	return nil
}

// oidcRole maps the role claim to a Lanscape role ("" = not allowed).
func oidcRole(cfg OIDCConfig, claims map[string]any) string {
	claim := cfg.RoleClaim
	if claim == "" {
		claim = "groups"
	}
	var groups []string
	switch g := claims[claim].(type) {
	case string:
		groups = []string{g}
	case []any:
		for _, x := range g {
			if s, ok := x.(string); ok {
				groups = append(groups, s)
			}
		}
	}
	has := func(list []string) bool {
		for _, a := range list {
			for _, b := range groups {
				if strings.EqualFold(a, b) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has(cfg.AdminGroups):
		return RoleAdmin
	case has(cfg.OperatorGroups):
		return RoleOperator
	}
	return cfg.DefaultRole
}

// redirectURL is the callback registered at the provider.
func (s *Server) oidcRedirectURL(r *http.Request) string {
	base := strings.TrimRight(s.settings(r.Context()).PublicURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/api/v1/auth/oidc/callback"
}

func (s *Server) apiOIDCInfo(w http.ResponseWriter, _ *http.Request) {
	name := s.cfg.OIDC.Name
	if name == "" {
		name = "SSO"
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": s.cfg.OIDC.Enabled(), "name": name})
}

// apiOIDCLogin starts the authorization code flow with PKCE.
func (s *Server) apiOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled() {
		http.NotFound(w, r)
		return
	}
	prov, _, err := s.oidc.provider(r.Context(), s.cfg.OIDC.Issuer, false)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	state, nonce, verifier := NewSecret(""), NewSecret(""), NewSecret("")+NewSecret("")
	redirect := s.oidcRedirectURL(r)
	s.oidc.mu.Lock()
	if s.oidc.pending == nil {
		s.oidc.pending = map[string]oidcPending{}
	}
	for k, p := range s.oidc.pending {
		if time.Now().After(p.exp) {
			delete(s.oidc.pending, k)
		}
	}
	s.oidc.pending[state] = oidcPending{nonce: nonce, verifier: verifier, redirect: redirect, exp: time.Now().Add(10 * time.Minute)}
	s.oidc.mu.Unlock()
	ch := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {s.cfg.OIDC.ClientID}, "redirect_uri": {redirect},
		"scope": {"openid profile email groups"}, "state": {state}, "nonce": {nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(ch[:])}, "code_challenge_method": {"S256"}}
	sep := "?"
	if strings.Contains(prov.AuthURL, "?") {
		sep = "&"
	}
	http.Redirect(w, r, prov.AuthURL+sep+q.Encode(), http.StatusFound)
}

// apiOIDCCallback finishes the flow: code exchange, ID token check, user mapping, session.
func (s *Server) apiOIDCCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		s.log.Warn("oidc sign-in failed", "err", msg)
		_ = s.store.Audit(r.Context(), store.AuditEntry{Actor: "oidc", Action: "auth.oidc", Result: "error", Detail: msg})
		http.Redirect(w, r, "/#/login?error="+url.QueryEscape(msg), http.StatusFound)
	}
	if !s.cfg.OIDC.Enabled() {
		http.NotFound(w, r)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail("provider: " + e)
		return
	}
	state := r.URL.Query().Get("state")
	s.oidc.mu.Lock()
	p, ok := s.oidc.pending[state]
	delete(s.oidc.pending, state)
	s.oidc.mu.Unlock()
	if !ok || time.Now().After(p.exp) {
		fail("sign-in expired, try again")
		return
	}
	claims, err := s.oidcExchange(r.Context(), r.URL.Query().Get("code"), p)
	if err != nil {
		fail(err.Error())
		return
	}
	u, err := s.oidcUser(r.Context(), claims)
	if err != nil {
		fail(err.Error())
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		fail(err.Error())
		return
	}
	_ = s.store.Audit(r.Context(), store.AuditEntry{Actor: u.Username, Action: "auth.oidc", Result: "ok", Detail: u.Role})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) oidcExchange(ctx context.Context, code string, p oidcPending) (map[string]any, error) {
	cfg := s.cfg.OIDC
	prov, keys, err := s.oidc.provider(ctx, cfg.Issuer, false)
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.redirect},
		"client_id": {cfg.ClientID}, "code_verifier": {p.verifier}}
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prov.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.oidc.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || resp.StatusCode != http.StatusOK || tok.IDToken == "" {
		return nil, fmt.Errorf("token request: %s %s", resp.Status, tok.Error)
	}
	claims, _, err := verifyJWT(tok.IDToken, keys)
	if errors.Is(err, errUnknownKey) {
		// the provider rotated its keys
		if _, keys, err = s.oidc.provider(ctx, cfg.Issuer, true); err == nil {
			claims, _, err = verifyJWT(tok.IDToken, keys)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	if err := checkClaims(claims, cfg.Issuer, cfg.ClientID, p.nonce, time.Now()); err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	return claims, nil
}

// oidcUser finds or creates the account of an OIDC subject and syncs its role.
func (s *Server) oidcUser(ctx context.Context, claims map[string]any) (store.User, error) {
	sub, _ := claims["sub"].(string)
	subject := strings.TrimRight(s.cfg.OIDC.Issuer, "/") + "#" + sub
	role := oidcRole(s.cfg.OIDC, claims)
	u, err := s.store.UserByOIDC(ctx, subject)
	switch {
	case err == nil:
		if u.Disabled {
			return u, errors.New("account disabled")
		}
		if role == "" {
			return u, errors.New("not a member of an allowed group")
		}
		if u.Role != role {
			u.Role = role
			if err := s.store.UpdateUser(ctx, u); err != nil {
				return u, err
			}
		}
		return u, nil
	case !errors.Is(err, store.ErrNotFound):
		return u, err
	}
	if role == "" {
		return u, errors.New("not a member of an allowed group")
	}
	name := ""
	for _, c := range []string{"preferred_username", "email", "name"} {
		if v, _ := claims[c].(string); v != "" {
			name = v
			break
		}
	}
	if name == "" {
		name = "oidc-" + sub
	}
	if _, err := s.store.UserByName(ctx, name); err == nil {
		// never attach an identity from the provider to an existing local account
		return u, fmt.Errorf("the name %q belongs to a local account", name)
	}
	u = store.User{Username: name, Role: role, OIDCSubject: subject}
	if u.ID, err = s.store.CreateUser(ctx, u); err != nil {
		return u, err
	}
	return s.store.UserByID(ctx, u.ID)
}
