package catalog

import (
	"testing"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/fingerprint"
	"github.com/retreat-community/lanscape/internal/monitor"
)

func gitea() *fingerprint.Match {
	return &fingerprint.Match{ID: "gitea", Name: "Gitea", Category: "development", Score: 9,
		Monitor: fingerprint.Monitor{Type: "http", Path: "/api/healthz"}}
}

func TestKubernetesMerge(t *testing.T) {
	// Ingress git.example + Service + Deployment + HTTP fingerprint = one "Gitea" card (§8.3),
	// reported by two agents of the same cluster
	k8s := []discovery.Item{
		{Key: "ingress/forge/gitea", Kind: discovery.KindIngress, Name: "gitea", Namespace: "forge", Hosts: []string{"git.example"},
			TLS: true, Backends: []string{"forge/gitea-http:3000"}},
		{Key: "svc/forge/gitea-http", Kind: discovery.KindService, Name: "gitea-http", Namespace: "forge",
			IPs: []string{"10.96.5.5"}, Ports: []discovery.Port{{Port: 3000, Proto: "tcp"}}, Selector: map[string]string{"app": "gitea"},
			URL: "http://10.96.5.5:3000", App: gitea()},
		{Key: "deploy/forge/gitea", Kind: discovery.KindDeployment, Name: "gitea", Namespace: "forge", State: "ready",
			PodLabels: map[string]string{"app": "gitea", "pod-template-hash": "x"}, Image: "gitea/gitea:1.22"},
		{Key: "deploy/forge/other", Kind: discovery.KindDeployment, Name: "other", Namespace: "forge", State: "ready",
			PodLabels: map[string]string{"app": "other"}},
		{Key: "pvc/forge/data", Kind: discovery.KindPVC, Name: "data", Namespace: "forge"},
	}
	var fs []Finding
	for _, agent := range []string{"a1", "a2"} {
		for _, it := range k8s {
			fs = append(fs, Finding{Agent: agent, Source: discovery.SourceK8s, Item: it, First: 100})
		}
	}
	cards := Build(fs, nil)
	if len(cards) != 2 {
		t.Fatalf("cards: %+v", cards)
	}
	c := cards[0]
	if c.Key != "k8s:deploy/forge/gitea" || c.Name != "gitea" || c.App == nil || c.App.ID != "gitea" || len(c.Refs) != 3 {
		t.Fatalf("gitea card: %+v", c)
	}
	if c.ExternalURL != "https://git.example" || c.InternalURL != "http://10.96.5.5:3000" || !c.TLS || c.State != "ready" {
		t.Errorf("addresses: %+v", c)
	}
	if c.Monitor == nil || c.Monitor.Type != monitor.TypeHTTP || c.Monitor.Target != "http://10.96.5.5:3000/api/healthz" {
		t.Errorf("monitor: %+v", c.Monitor)
	}
	if cards[1].Key != "k8s:deploy/forge/other" || cards[1].Monitor != nil {
		t.Errorf("other: %+v", cards[1])
	}
}

func TestDockerAndSocketMerge(t *testing.T) {
	agents := map[string]AgentInfo{"nas": {Name: "nas", IP: "192.168.1.10"}}
	fs := []Finding{
		{Agent: "nas", Source: discovery.SourceDocker, Item: discovery.Item{Key: "container/jellyfin", Kind: discovery.KindContainer,
			Name: "jellyfin", Image: "jellyfin/jellyfin", State: "running", Owner: "0123456789ab", Project: "media",
			Labels: map[string]string{"com.docker.compose.service": "jellyfin"},
			Ports:  []discovery.Port{{Port: 8096, Target: 8096, Proto: "tcp"}}, App: &fingerprint.Match{ID: "jellyfin", Name: "Jellyfin",
				Monitor: fingerprint.Monitor{Type: "http", Path: "/health"}}}},
		// docker-proxy for the published port
		{Agent: "nas", Source: discovery.SourceSockets, Item: discovery.Item{Key: "tcp/8096@*", Kind: discovery.KindSocket, Proto: "tcp",
			Addr: "0.0.0.0", Port: 8096, Process: "docker-proxy", URL: "http://127.0.0.1:8096", Title: "Jellyfin"}},
		// a host process with two ports
		{Agent: "nas", Source: discovery.SourceSockets, Item: discovery.Item{Key: "tcp/3000@*", Kind: discovery.KindSocket, Proto: "tcp",
			Addr: "0.0.0.0", Port: 3000, Process: "gitea", URL: "http://127.0.0.1:3000", App: gitea()}},
		{Agent: "nas", Source: discovery.SourceSockets, Item: discovery.Item{Key: "tcp/2222@*", Kind: discovery.KindSocket, Proto: "tcp",
			Addr: "0.0.0.0", Port: 2222, Process: "gitea"}},
		// noise
		{Agent: "nas", Source: discovery.SourceSockets, Item: discovery.Item{Key: "tcp/47700@*", Kind: discovery.KindSocket, Proto: "tcp",
			Addr: "0.0.0.0", Port: 47700, Process: "lanscape-agent"}},
		{Agent: "nas", Source: discovery.SourceSockets, Item: discovery.Item{Key: "tcp/631@127.0.0.1", Kind: discovery.KindSocket, Proto: "tcp",
			Addr: "127.0.0.1", Port: 631, Process: "cupsd"}},
		{Agent: "nas", Source: discovery.SourceSockets, Item: discovery.Item{Key: "udp/51234@*", Kind: discovery.KindSocket, Proto: "udp",
			Addr: "0.0.0.0", Port: 51234, Process: "x"}},
		// unnamed socket (no permission to see the process)
		{Agent: "nas", Source: discovery.SourceSockets, Item: discovery.Item{Key: "tcp/22@*", Kind: discovery.KindSocket, Proto: "tcp",
			Addr: "0.0.0.0", Port: 22}, Gone: true},
	}
	cards := Build(fs, agents)
	if len(cards) != 3 {
		t.Fatalf("cards: %+v", cards)
	}
	byKey := map[string]Card{}
	for _, c := range cards {
		byKey[c.Key] = c
	}
	j := byKey["docker:nas:jellyfin"]
	if j.Name != "media/jellyfin" || len(j.Refs) != 2 || j.HostPort != "192.168.1.10:8096" || j.InternalURL != "http://192.168.1.10:8096" {
		t.Errorf("jellyfin: %+v", j)
	}
	if j.Monitor == nil || j.Monitor.Target != "http://192.168.1.10:8096/health" {
		t.Errorf("jellyfin monitor: %+v", j.Monitor)
	}
	g := byKey["proc:nas:gitea"]
	if g.Name != "Gitea" || len(g.Refs) != 2 || g.InternalURL != "http://192.168.1.10:3000" || g.HostPort != "192.168.1.10:2222" && g.HostPort != "192.168.1.10:3000" {
		t.Errorf("gitea: %+v", g)
	}
	s := byKey["sockets:nas:tcp/22@*"]
	if !s.Gone || s.Name != "tcp/22" || s.Monitor == nil || s.Monitor.Type != monitor.TypeTCP || s.Monitor.Target != "192.168.1.10:22" {
		t.Errorf("ssh: %+v %+v", s, s.Monitor)
	}
}

func TestRules(t *testing.T) {
	c := Card{Key: "k8s:deploy/a/b", Kind: discovery.KindDeployment, ExternalURL: "https://b.example", TLS: true, App: gitea()}
	rules := []Rule{
		{Name: "bad", Action: "delete"},
		{Name: "media", Category: "media", Action: "add"},
		{Name: "tls ingress", Kind: "ingress", TLSOnly: true, Action: "add", Monitor: true, Tile: true},
	}
	r, ok := FirstMatch(rules, &c)
	if !ok || r.Name != "tls ingress" {
		t.Errorf("match: %+v %v", r, ok)
	}
	c.TLS = false
	if _, ok := FirstMatch(rules, &c); ok {
		t.Error("non-TLS ingress matched")
	}
	if !(&Rule{App: "git*"}).Match(&c) || (&Rule{App: "*"}).Match(&Card{}) || (&Rule{Kind: "container"}).Match(&c) {
		t.Error("app glob / kind")
	}
}

func TestDiff(t *testing.T) {
	prev := []discovery.Item{
		{Key: "tcp/22@*", Kind: discovery.KindSocket, Proto: "tcp", Port: 22, Process: "sshd"},
		{Key: "tcp/8080@*", Kind: discovery.KindSocket, Proto: "tcp", Port: 8080, Process: "java"},
		{Key: "container/db", Kind: discovery.KindContainer, Name: "db", State: "running", IPs: []string{"172.18.0.2"}},
		{Key: "container/old", Kind: discovery.KindContainer, Name: "old", State: "running"},
		{Key: "svc/a/lb", Kind: discovery.KindService, Name: "lb", Namespace: "a", IPs: []string{"10.0.0.1", "192.168.1.240"}},
	}
	cur := []discovery.Item{
		{Key: "tcp/22@*", Kind: discovery.KindSocket, Proto: "tcp", Port: 22, Process: "sshd"},
		{Key: "tcp/9090@*", Kind: discovery.KindSocket, Proto: "tcp", Port: 9090, Process: "prometheus"},
		{Key: "tcp/47700@*", Kind: discovery.KindSocket, Proto: "tcp", Port: 47700, Process: "lanscape-agent"},
		{Key: "container/db", Kind: discovery.KindContainer, Name: "db", State: "running", IPs: []string{"172.18.0.3"}},
		{Key: "svc/a/lb", Kind: discovery.KindService, Name: "lb", Namespace: "a", IPs: []string{"10.0.0.1", "192.168.1.241"}},
	}
	got := map[string]string{}
	for _, c := range Diff(prev, cur, "nas") {
		got[c.Kind+" "+c.Subject] = c.Detail
	}
	want := []string{"port_opened nas tcp/9090", "port_closed nas tcp/8080", "ip_changed nas db", "container_gone nas old",
		"ip_changed service a/lb"}
	if len(got) != len(want) {
		t.Errorf("changes: %v", got)
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	stopped := Diff([]discovery.Item{{Key: "c", Kind: discovery.KindContainer, Name: "c", State: "running"}},
		[]discovery.Item{{Key: "c", Kind: discovery.KindContainer, Name: "c", State: "exited"}}, "h")
	if len(stopped) != 1 || stopped[0].Kind != ChangeContainerStopped {
		t.Errorf("stopped: %+v", stopped)
	}
}
