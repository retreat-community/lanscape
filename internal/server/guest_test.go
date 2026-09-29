package server

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/retreat-community/lanscape/internal/store"
)

func TestGuestDashboard(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()
	if _, err := s.store.SaveService(ctx, store.Service{Name: "Gitea", Tile: true, InternalURL: "http://10.0.0.5:3000",
		ExternalURL: "https://git.example", Addresses: []store.Address{{Type: "internal", Value: "http://10.0.0.5:3000"}}}); err != nil {
		t.Fatal(err)
	}
	anon := client(t)
	if code := do(t, anon, "GET", ts.URL+"/api/v1/dashboard", nil, nil); code != 401 {
		t.Fatalf("guest dashboard served while off: %d", code)
	}
	var st struct {
		Guest bool `json:"guest"`
	}
	do(t, anon, "GET", ts.URL+"/api/v1/setup", nil, &st)
	if st.Guest {
		t.Error("guest announced while off")
	}
	set := DefaultSettings
	set.GuestDashboard = true
	if err := s.store.SetSetting(ctx, "settings", set); err != nil {
		t.Fatal(err)
	}
	do(t, anon, "GET", ts.URL+"/api/v1/setup", nil, &st)
	resp, err := anon.Get(ts.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !st.Guest || resp.StatusCode != 200 || !strings.Contains(string(body), "https://git.example") || strings.Contains(string(body), "10.0.0.5") {
		t.Fatalf("guest view: %v %d %s", st.Guest, resp.StatusCode, body)
	}
	// other data stays private
	if code := do(t, anon, "GET", ts.URL+"/api/v1/services", nil, nil); code != 401 {
		t.Errorf("services open to guests: %d", code)
	}
	// signed-in users still get the full dashboard
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	resp, _ = c.Get(ts.URL + "/api/v1/dashboard")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "10.0.0.5") {
		t.Errorf("full dashboard reduced: %s", body)
	}
}
