package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/retreat-community/lanscape/internal/store"
)

// Roles, ordered by privilege.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

var roleRank = map[string]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ValidRole reports whether r is a known role.
func ValidRole(r string) bool { return roleRank[r] > 0 }

// argon2id parameters (RFC 9106 second recommended option, memory in KiB).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword returns an encoded argon2id hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword verifies pw against an encoded argon2id hash.
func CheckPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[3])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[4])
	if err1 != nil || err2 != nil || m > 1<<20 || t > 16 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

type ctxKey int

const userKey ctxKey = 1

// Principal is the authenticated caller.
type Principal struct {
	User  store.User
	Role  string // effective role (API tokens may be restricted)
	Token bool
}

func principal(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(userKey).(Principal)
	return p, ok
}

const sessionCookie = "lanscape_session"

func (s *Server) authenticate(r *http.Request) (Principal, bool) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		t, u, err := s.store.APITokenByHash(ctx, hashToken(strings.TrimPrefix(h, "Bearer ")))
		if err != nil || u.Disabled || (t.ExpiresAt != 0 && t.ExpiresAt < time.Now().UnixMilli()) {
			return Principal{}, false
		}
		role := t.Role
		if roleRank[role] > roleRank[u.Role] {
			role = u.Role
		}
		return Principal{User: u, Role: role, Token: true}, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Principal{}, false
	}
	u, err := s.store.SessionUser(ctx, hashToken(c.Value))
	if err != nil || u.Disabled {
		return Principal{}, false
	}
	return Principal{User: u, Role: u.Role}, true
}

// require wraps a handler with an authentication and role check.
func (s *Server) require(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if roleRank[p.Role] < roleRank[role] {
			writeError(w, http.StatusForbidden, "insufficient role")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, p)))
	}
}

// loginLimiter slows down password guessing per client address.
type loginLimiter struct {
	mu   sync.Mutex
	fail map[string][]time.Time
}

func (l *loginLimiter) allowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-5 * time.Minute)
	var keep []time.Time
	for _, t := range l.fail[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.fail[ip] = keep
	return len(keep) < 10
}

func (l *loginLimiter) failed(ip string) {
	l.mu.Lock()
	l.fail[ip] = append(l.fail[ip], time.Now())
	l.mu.Unlock()
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

var errBadLogin = errors.New("invalid username or password")

// login verifies credentials (and TOTP when enabled) and creates a session.
func (s *Server) login(w http.ResponseWriter, r *http.Request, username, password, code string) (store.User, error) {
	ip := clientIP(r)
	if !s.limiter.allowed(ip) {
		return store.User{}, errors.New("too many failed attempts, try again later")
	}
	u, err := s.store.UserByName(r.Context(), username)
	if err != nil || u.Disabled || !CheckPassword(u.PasswordHash, password) {
		if err != nil {
			// spend comparable time for unknown users
			_ = CheckPassword("argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		}
		s.limiter.failed(ip)
		return store.User{}, errBadLogin
	}
	if u.TOTPSecret != "" && !ValidateTOTP(u.TOTPSecret, code) {
		s.limiter.failed(ip)
		return store.User{}, errors.New("invalid or missing two-factor code")
	}
	if err := s.startSession(w, r, u); err != nil {
		return store.User{}, err
	}
	return u, nil
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) error {
	raw := NewSecret("")
	exp := time.Now().Add(s.cfg.SessionTTL)
	if err := s.store.CreateSession(r.Context(), hashToken(raw), u.ID, exp.UnixMilli(), clientIP(r), r.UserAgent()); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: raw, Path: "/", Expires: exp, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || s.cfg.SecureCookies})
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteLaxMode})
}
