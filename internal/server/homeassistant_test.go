package server

import (
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHomeAssistant(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	for _, n := range []string{"Grafana (web)", "NAS"} {
		if code := do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": n,
			"spec": map[string]any{"type": "tcp", "target": "127.0.0.1:1"}}, nil); code != 201 {
			t.Fatalf("monitor %s: %d", n, code)
		}
	}
	s.hub.Connected(AgentState{ID: "rt", Name: "router-1", Kind: "full"}, &fakeConn{id: "rt"})

	var st HAState
	do(t, c, "GET", ts.URL+"/api/v1/integrations/homeassistant", nil, &st)
	if _, ok := st.Monitors["monitor_grafana_web"]; !ok || len(st.Monitors) != 2 || st.Agents["agent_router_1"].State != "online" {
		t.Fatalf("state: %+v", st)
	}

	res, err := c.Get(ts.URL + "/api/v1/integrations/homeassistant/config")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	var pkg struct {
		Rest []struct {
			Resource string           `yaml:"resource"`
			Sensor   []map[string]any `yaml:"sensor"`
			Binary   []map[string]any `yaml:"binary_sensor"`
		} `yaml:"rest"`
	}
	// !secret is a Home Assistant tag; the plain YAML parser keeps it as a string
	if err := yaml.Unmarshal(b, &pkg); err != nil {
		t.Fatalf("config is not YAML: %v\n%s", err, b)
	}
	if len(pkg.Rest) != 1 || !strings.HasSuffix(pkg.Rest[0].Resource, "/api/v1/integrations/homeassistant") ||
		len(pkg.Rest[0].Sensor) != 6 || len(pkg.Rest[0].Binary) != 1 {
		t.Errorf("package:\n%s", b)
	}
	if !strings.Contains(string(b), `value_json.monitors.monitor_nas.state`) {
		t.Errorf("no NAS sensor:\n%s", b)
	}
}
