package server

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// HAState is the document Home Assistant polls with its RESTful integration (§15): monitors,
// agents and summary counters as sensors.
type HAState struct {
	Summary  map[string]int      `json:"summary"`
	Monitors map[string]HASensor `json:"monitors"` // by object id
	Agents   map[string]HASensor `json:"agents"`
}

// HASensor is one entity: its state and attributes.
type HASensor struct {
	Name      string  `json:"name"`
	State     string  `json:"state"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
	Message   string  `json:"message,omitempty"`
	LastCheck int64   `json:"last_check,omitempty"`
}

var nonObjectID = regexp.MustCompile(`[^a-z0-9]+`)

// objectID turns a name into a Home Assistant object id ("Grafana (web)" -> "grafana_web").
func objectID(prefix, name string) string {
	id := strings.Trim(nonObjectID.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if id == "" {
		id = "x"
	}
	return prefix + "_" + id
}

func (s *Server) haState(r *http.Request) HAState {
	st := HAState{Summary: map[string]int{}, Monitors: map[string]HASensor{}, Agents: map[string]HASensor{}}
	for _, m := range s.uptime.snapshot() {
		st.Summary[m.Status]++
		id := objectID("monitor", m.Name)
		if _, dup := st.Monitors[id]; dup {
			id += "_" + strconv.FormatInt(m.ID, 10)
		}
		st.Monitors[id] = HASensor{Name: m.Name, State: m.Status, LatencyMS: m.LastLatency, Message: m.LastMessage,
			LastCheck: m.LastCheck}
	}
	open, _ := s.store.Incidents(r.Context(), true, 0, 0, 1000)
	st.Summary["incidents"] = len(open)
	for _, a := range s.hub.List() {
		state := "offline"
		if a.Online {
			state = "online"
		}
		st.Agents[objectID("agent", a.Name)] = HASensor{Name: a.Name, State: state}
	}
	return st
}

func (s *Server) apiHAState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.haState(r))
}

// apiHAConfig renders a Home Assistant package for the current monitors and agents: one REST
// resource and a sensor (or binary sensor) per entity; the API token goes to secrets.yaml.
func (s *Server) apiHAConfig(w http.ResponseWriter, r *http.Request) {
	st := s.haState(r)
	base := s.settings(r.Context()).PublicURL
	if base == "" {
		base = "http://" + r.Host
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Lanscape sensors for Home Assistant: save as packages/lanscape.yaml and add\n")
	fmt.Fprintf(&b, "# lanscape_token: \"Bearer <API token with the viewer role>\" to secrets.yaml.\n")
	fmt.Fprintf(&b, "rest:\n  - resource: %s/api/v1/integrations/homeassistant\n", strings.TrimRight(base, "/"))
	fmt.Fprintf(&b, "    headers:\n      Authorization: !secret lanscape_token\n    scan_interval: 60\n")
	fmt.Fprintf(&b, "    sensor:\n")
	for _, k := range []string{"up", "degraded", "down", "incidents"} {
		fmt.Fprintf(&b, "      - name: \"Lanscape %s\"\n        unique_id: lanscape_summary_%s\n", k, k)
		fmt.Fprintf(&b, "        value_template: \"{{ value_json.summary.%s | default(0) }}\"\n", k)
	}
	for _, id := range sortedKeys(st.Monitors) {
		m := st.Monitors[id]
		fmt.Fprintf(&b, "      - name: %s\n        unique_id: lanscape_%s\n", strconv.Quote("Lanscape "+m.Name), id)
		fmt.Fprintf(&b, "        value_template: \"{{ value_json.monitors.%s.state }}\"\n", id)
		fmt.Fprintf(&b, "        json_attributes_path: \"$.monitors.%s\"\n        json_attributes: [latency_ms, message, last_check]\n", id)
	}
	if len(st.Agents) > 0 {
		fmt.Fprintf(&b, "    binary_sensor:\n")
		for _, id := range sortedKeys(st.Agents) {
			fmt.Fprintf(&b, "      - name: %s\n        unique_id: lanscape_%s\n        device_class: connectivity\n",
				strconv.Quote("Lanscape agent "+st.Agents[id].Name), id)
			fmt.Fprintf(&b, "        value_template: \"{{ value_json.agents.%s.state == 'online' }}\"\n", id)
		}
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="lanscape.yaml"`)
	_, _ = w.Write([]byte(b.String()))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
