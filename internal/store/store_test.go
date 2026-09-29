package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/retreat-community/lanscape/internal/store/storetest"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), storetest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUsersSessionsTokens(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	id, err := s.CreateUser(ctx, User{Username: "admin", PasswordHash: "h", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, User{Username: "admin", PasswordHash: "h", Role: "admin"}); err == nil {
		t.Error("duplicate username accepted")
	}
	if err := s.CreateSession(ctx, "sess", id, now()+60000, "1.2.3.4", "ua"); err != nil {
		t.Fatal(err)
	}
	if u, err := s.SessionUser(ctx, "sess"); err != nil || u.Username != "admin" {
		t.Fatalf("session: %v %v", u, err)
	}
	if err := s.CreateSession(ctx, "old", id, now()-1, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Error("expired session accepted")
	}
	if _, err := s.CreateAPIToken(ctx, APIToken{UserID: id, Name: "ci", Role: "operator"}, "th"); err != nil {
		t.Fatal(err)
	}
	if tok, u, err := s.APITokenByHash(ctx, "th"); err != nil || tok.Role != "operator" || u.ID != id {
		t.Fatalf("api token: %v", err)
	}
	if _, err := s.CreateAgentToken(ctx, AgentToken{Name: "once"}, "at"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseAgentToken(ctx, "at"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseAgentToken(ctx, "at"); !errors.Is(err, ErrNotFound) {
		t.Error("single-use token reused")
	}
	if err := s.DeleteUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "sess"); !errors.Is(err, ErrNotFound) {
		t.Error("session survived user deletion")
	}
}

func TestAgentsRunsSettings(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := Agent{ID: "a1", Name: "n1", Kind: "full", Labels: map[string]string{"site": "home"},
		Inventory: json.RawMessage(`{"ifaces":[]}`)}
	if err := s.UpsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Name = "n1-renamed"
	if err := s.UpsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentByID(ctx, "a1")
	if err != nil || got.Name != "n1-renamed" || got.Labels["site"] != "home" {
		t.Fatalf("agent: %+v %v", got, err)
	}
	id, err := s.CreateRun(ctx, Run{Kind: "full", Status: "running", Started: now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LastRun(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Error("running run returned as last")
	}
	if err := s.FinishRun(ctx, id, "done", []byte(`{"paths":[]}`)); err != nil {
		t.Fatal(err)
	}
	r, err := s.LastRun(ctx, "full")
	if err != nil || r.ID != id || string(r.Report) != `{"paths":[]}` {
		t.Fatalf("last run: %+v %v", r, err)
	}
	var v struct{ A int }
	if err := s.SetSetting(ctx, "x", map[string]int{"A": 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.GetSetting(ctx, "x", &v); err != nil || v.A != 3 {
		t.Fatalf("setting: %v %v", v, err)
	}
	if isNew, _ := s.TouchMAC(ctx, "aa", "1.1.1.1", "", "arp"); !isNew {
		t.Error("first MAC not new")
	}
	if isNew, _ := s.TouchMAC(ctx, "aa", "1.1.1.2", "", "arp"); isNew {
		t.Error("second MAC new")
	}
	lite, pg := &Store{Dialect: SQLite}, &Store{Dialect: Postgres}
	if lite.q("a=? and b=?") != "a=? and b=?" {
		t.Error("sqlite placeholders rewritten")
	}
	if pg.q("a=? and b=?") != "a=$1 and b=$2" {
		t.Error("postgres placeholders")
	}
}
