package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/retreat-community/lanscape/internal/monitor"
)

const gitopsYAML = `apiVersion: lanscape/v1
services:
  - name: Gitea
    app: gitea
    group: Dev
    internal_url: http://10.0.0.5:3000
    tile: true
monitors:
  # operands of the composite come later in the file on purpose
  - name: Uplink
    check: {type: composite, expr: "{Router} && {Gitea HTTP}"}
  - name: Gitea HTTP
    service: Gitea
    check: {type: http, target: http://10.0.0.5:3000/api/healthz}
    interval: 30
    depends_on: [Router]
  - name: Router
    check: {type: icmp, target: 10.0.0.1}
segments:
  - {id: 10.0.0.0/24, name: LAN, expected_mbps: 1000}
rules:
  - {name: ingresses, kind: ingress, action: add, monitor: true}
channels:
  - name: ops
    type: webhook
    config: {url: "https://hooks.example/lanscape", secret: s3cret, monitors: [Uplink]}
status_pages:
  - slug: home
    title: Home
    public: true
    groups:
      - {name: Core, monitors: [Router, Gitea HTTP]}
`

type planResp struct {
	Error string         `json:"error"`
	Plan  []ConfigChange `json:"plan"`
}

func applyConfig(t *testing.T, c *http.Client, url, body, query string) (int, planResp) {
	t.Helper()
	resp, err := c.Post(url+"/api/v1/config"+query, "application/yaml", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var p planResp
	_ = json.NewDecoder(resp.Body).Decode(&p)
	return resp.StatusCode, p
}

func actions(p planResp) map[string]int {
	out := map[string]int{}
	for _, c := range p.Plan {
		out[c.Action]++
	}
	return out
}

func TestGitOpsConfig(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)

	if code, p := applyConfig(t, c, ts.URL, gitopsYAML, "?dry_run=true"); code != 200 || actions(p)["create"] != 7 || actions(p)["update"] != 1 || len(s.uptime.snapshot()) != 0 {
		t.Fatalf("dry run: %d %+v", code, p)
	}
	if code, p := applyConfig(t, c, ts.URL, gitopsYAML, ""); code != 200 || actions(p)["create"] != 7 {
		t.Fatalf("apply: %d %+v", code, p)
	}
	ids := map[string]int64{}
	for _, m := range s.uptime.snapshot() {
		ids[m.Name] = m.ID
	}
	for _, m := range s.uptime.snapshot() {
		sp, _ := validateSpec(m.Spec)
		switch m.Name {
		case "Uplink":
			e, err := monitor.ParseExpr(sp.Expr)
			if err != nil || len(e.IDs()) != 2 || e.IDs()[0] != ids["Router"] || e.IDs()[1] != ids["Gitea HTTP"] {
				t.Errorf("composite: %q %v", sp.Expr, err)
			}
		case "Gitea HTTP":
			if len(m.Parents) != 1 || m.Parents[0] != ids["Router"] || m.ServiceID == 0 || m.IntervalS != 30 {
				t.Errorf("gitea monitor: %+v", m)
			}
		}
	}
	chans, _ := s.store.Channels(context.Background())
	if len(chans) != 1 || !strings.Contains(string(chans[0].Config), `"monitors":[`) || !strings.Contains(string(chans[0].Config), "s3cret") {
		t.Fatalf("channel: %s", chans[0].Config)
	}

	// applying the same file again changes nothing
	if _, p := applyConfig(t, c, ts.URL, gitopsYAML, ""); actions(p)["unchanged"] != 8 {
		t.Errorf("re-apply: %+v", p)
	}
	// the export carries no secrets and applies as a no-op (secrets are kept)
	resp, err := c.Get(ts.URL + "/api/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	exported, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(exported, []byte("s3cret")) || !bytes.Contains(exported, []byte("{Router} && {Gitea HTTP}")) {
		t.Fatalf("export:\n%s", exported)
	}
	if code, p := applyConfig(t, c, ts.URL, string(exported), ""); code != 200 || actions(p)["unchanged"] != len(p.Plan) {
		t.Errorf("export re-apply: %d %+v\n%s", code, p, exported)
	}
	chans, _ = s.store.Channels(context.Background())
	if !strings.Contains(string(chans[0].Config), "s3cret") {
		t.Error("secret lost on re-apply")
	}

	// unknown references are refused before anything changes
	bad := strings.Replace(gitopsYAML, "depends_on: [Router]", "depends_on: [Nope]", 1)
	if code, p := applyConfig(t, c, ts.URL, bad, ""); code != 400 || !strings.Contains(p.Error, "Nope") {
		t.Errorf("bad ref: %d %+v", code, p)
	}
	if code, _ := applyConfig(t, c, ts.URL, "apiVersion: v0\n", ""); code != 400 {
		t.Errorf("wrong version accepted: %d", code)
	}
	if code, _ := applyConfig(t, c, ts.URL, "apiVersion: lanscape/v1\nmonitorz: []\n", ""); code != 400 {
		t.Errorf("unknown field accepted: %d", code)
	}

	// prune removes what the file no longer lists, only in the sections it contains, and never
	// monitors that stored status pages or channels still use
	small := "apiVersion: lanscape/v1\nmonitors:\n  - name: Router\n    check: {type: icmp, target: 10.0.0.1}\n"
	if code, p := applyConfig(t, c, ts.URL, small, "?prune=true"); code != 400 || !strings.Contains(p.Error, "status page") {
		t.Errorf("prune of used monitors: %d %+v", code, p)
	}
	withRefs := small + "status_pages:\n  - {slug: home, title: Home, public: true, groups: [{name: Core, monitors: [Router]}]}\n" +
		"channels:\n  - {name: ops, type: webhook, config: {url: \"https://hooks.example/lanscape\"}}\n"
	if code, p := applyConfig(t, c, ts.URL, withRefs, "?prune=true&dry_run=true"); code != 200 || actions(p)["delete"] != 2 || len(s.uptime.snapshot()) != 3 {
		t.Errorf("prune dry run: %d %+v", code, p)
	}
	if code, p := applyConfig(t, c, ts.URL, withRefs, "?prune=true"); code != 200 || len(s.uptime.snapshot()) != 1 {
		t.Errorf("prune: %d %+v", code, p)
	}
	chans, _ = s.store.Channels(context.Background())
	if !strings.Contains(string(chans[0].Config), "s3cret") {
		t.Error("secret lost when the channel was updated without it")
	}
	svcs, _ := s.store.Services(context.Background())
	if len(svcs) != 1 {
		t.Errorf("services touched by a monitors-only file: %d", len(svcs))
	}
}

func TestExpandVars(t *testing.T) {
	env := map[string]string{"TOKEN": "abc", "EMPTY": ""}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	out, err := ExpandVars([]byte("a: ${TOKEN}\nb: $TOKEN\nc: '${EMPTY}'\n"), lookup)
	if err != nil || string(out) != "a: abc\nb: $TOKEN\nc: ''\n" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := ExpandVars([]byte("${NOPE} ${TOKEN} ${ALSO}"), lookup); err == nil || !strings.Contains(err.Error(), "NOPE, ALSO") {
		t.Errorf("missing variables: %v", err)
	}
}

// the example in docs/INSTALL.md must stay a valid file
func TestConfigDocExample(t *testing.T) {
	doc, err := os.ReadFile("../../docs/INSTALL.md")
	if err != nil {
		t.Skip(err)
	}
	_, rest, ok := strings.Cut(string(doc), "```yaml\napiVersion: lanscape/v1")
	if !ok {
		t.Fatal("example not found")
	}
	body, _, _ := strings.Cut(rest, "```")
	b, err := ExpandVars([]byte("apiVersion: lanscape/v1"+body), func(string) (string, bool) { return "x", true })
	if err != nil {
		t.Fatal(err)
	}
	cf, err := ParseConfig(b)
	if err != nil || cf.Monitors == nil || len(*cf.Monitors) != 3 || cf.StatusPages == nil {
		t.Fatalf("%v %+v", err, cf)
	}
}
