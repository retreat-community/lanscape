package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/registry"
)

func TestImageUpdates(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tags := map[string][]string{
			"/v2/org/app/tags/list":  {"1.0.0", "1.2.0", "1.3.0-rc1", "latest"},
			"/v2/org/tool/tags/list": {"2.0"},
		}[r.URL.Path]
		if tags == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tags": tags})
	}))
	defer reg.Close()
	host := strings.TrimPrefix(reg.URL, "http://")
	s.images.client = &registry.Client{HTTP: reg.Client(), Scheme: "http"}
	s.hub.Connected(AgentState{ID: "nas", Name: "nas", Kind: "full"}, &fakeConn{id: "nas"})
	rep := discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceDocker, Items: []discovery.Item{
		{Key: "container/app", Kind: discovery.KindContainer, Name: "app", Image: host + "/org/app:1.0.0"},
		{Key: "container/app2", Kind: discovery.KindContainer, Name: "app2", Image: host + "/org/app:1.0.0"},
		{Key: "container/tool", Kind: discovery.KindContainer, Name: "tool", Image: host + "/org/tool:2.0"},
		{Key: "container/gone", Kind: discovery.KindContainer, Name: "gone", Image: host + "/org/missing:1.0"},
	}}}}
	ctx := context.Background()
	if err := s.ingestDiscovery(ctx, "nas", rep); err != nil {
		t.Fatal(err)
	}
	res := s.checkImages(ctx)
	if res.Images != 3 || len(res.Errors) != 1 || len(res.Updates) != 1 {
		t.Fatalf("check: %+v", res)
	}
	u := res.Updates[0]
	if u.Latest != "1.2.0" || strings.Join(u.Users, ",") != "nas/app,nas/app2" {
		t.Errorf("update: %+v", u)
	}
	s.checkImages(ctx) // unchanged: no second change entry
	changes, _ := s.store.Changes(ctx, 0, 100)
	n := 0
	for _, ch := range changes {
		if ch.Kind == ChangeImageUpdate {
			n++
		}
	}
	if n != 1 {
		t.Errorf("image_update changes: %d", n)
	}
	var d Dashboard
	do(t, c, "GET", ts.URL+"/api/v1/dashboard", nil, &d)
	if len(d.Images) != 1 {
		t.Errorf("dashboard images: %+v", d.Images)
	}
	var got ImageCheck
	if code := do(t, c, "GET", ts.URL+"/api/v1/updates/images", nil, &got); code != 200 || got.Images != 3 {
		t.Errorf("api: %d %+v", code, got)
	}
}
