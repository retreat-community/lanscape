package server

import (
	"context"
	"testing"

	"github.com/retreat-community/lanscape/internal/store"
)

func TestBoards(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()
	for _, v := range []store.Service{{Name: "Jellyfin", Group: "Media", Tile: true}, {Name: "Proxmox", Group: "Infra", Tile: true}} {
		v.Addresses = []store.Address{}
		if _, err := s.store.SaveService(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	admin := client(t)
	do(t, admin, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	boards := []Board{{ID: "main", Name: "Main"}, {ID: "media", Name: "Media", Groups: []string{"media"}, Notes: "Runbook https://wiki.lan/media"},
		{ID: "infra", Name: "Infra", Groups: []string{"Infra"}, MinRole: RoleOperator}}
	if code := do(t, admin, "PUT", ts.URL+"/api/v1/dashboards", []Board{{ID: "Bad id", Name: "x"}}, nil); code != 400 {
		t.Errorf("bad id accepted: %d", code)
	}
	if code := do(t, admin, "PUT", ts.URL+"/api/v1/dashboards", boards, nil); code != 200 {
		t.Fatalf("save: %d", code)
	}
	var d Dashboard
	do(t, admin, "GET", ts.URL+"/api/v1/dashboard?board=media", nil, &d)
	if d.Board.ID != "media" || len(d.Groups) != 1 || d.Groups[0].Name != "Media" || d.Board.Notes == "" {
		t.Errorf("media board: %+v %+v", d.Board, d.Groups)
	}
	do(t, admin, "GET", ts.URL+"/api/v1/dashboard", nil, &d)
	if d.Board.ID != "main" || len(d.Groups) != 2 {
		t.Errorf("main board: %+v", d.Groups)
	}
	// viewers do not see the operator board
	do(t, admin, "POST", ts.URL+"/api/v1/users", map[string]string{"username": "vw", "password": "viewer-password-1", "role": "viewer"}, nil)
	vc := client(t)
	do(t, vc, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "vw", Password: "viewer-password-1"}, nil)
	var list []Board
	do(t, vc, "GET", ts.URL+"/api/v1/dashboards", nil, &list)
	if len(list) != 2 {
		t.Errorf("viewer boards: %+v", list)
	}
	do(t, vc, "GET", ts.URL+"/api/v1/dashboard?board=infra", nil, &d)
	if d.Board.ID != "main" {
		t.Errorf("viewer got the operator board: %+v", d.Board)
	}
}
