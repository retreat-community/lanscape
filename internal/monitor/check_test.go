package monitor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok","items":[{"state":"ready"}]}`))
		case "/auth":
			if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
				w.WriteHeader(http.StatusUnauthorized)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	cases := []struct {
		s    Spec
		want string
	}{
		{Spec{Type: TypeHTTP, Target: srv.URL + "/health"}, Up},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/missing"}, Down},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/missing", ExpectStatus: []int{404}}, Up},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/missing", ExpectStatus: []int{4}}, Up},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/health", Keyword: `"ok"`}, Up},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/health", Keyword: `"fail"`}, Down},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/health", Keyword: `st.tus`, KeywordRegex: true}, Up},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/health", Keyword: `ok`, InvertKeyword: true}, Down},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/health", JSONPath: "items.0.state", JSONValue: "ready"}, Up},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/health", JSONPath: "$.status", JSONValue: "bad"}, Down},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/auth", BasicUser: "u", BasicPassword: "p"}, Up},
		{Spec{Type: TypeHTTP, Target: srv.URL + "/auth"}, Down},
		{Spec{Type: TypeHTTP, Target: "http://127.0.0.1:1/"}, Down},
	}
	for _, c := range cases {
		if err := c.s.Validate(); err != nil {
			t.Fatalf("%+v: %v", c.s, err)
		}
		r := Run(ctx, c.s)
		if r.Status != c.want {
			t.Errorf("%s %+v: got %s (%s)", c.s.Target, c.s, r.Status, r.Message)
		}
		if r.At == 0 {
			t.Error("result without timestamp")
		}
	}
}

func TestHTTPSAndTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	ctx := context.Background()
	// self-signed: fails verification unless ignored
	if r := Run(ctx, Spec{Type: TypeHTTP, Target: srv.URL}); r.Status != Down || !strings.Contains(r.Message, "TLS") {
		t.Errorf("self-signed accepted: %+v", r)
	}
	r := Run(ctx, Spec{Type: TypeHTTP, Target: srv.URL, IgnoreTLSErrors: true})
	if r.Status == Down || r.CertNotAfter == 0 {
		t.Errorf("ignore tls: %+v", r)
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	if r := Run(ctx, Spec{Type: TypeTLS, Target: host}); r.Status != Down {
		t.Errorf("tls verify: %+v", r)
	}
	// the httptest certificate is valid until 2084; a large warning window degrades it
	if r := Run(ctx, Spec{Type: TypeTLS, Target: host, IgnoreTLSErrors: true, WarnDays: 365 * 100}); r.Status != Degraded {
		t.Errorf("tls warn: %+v", r)
	}
}

func TestTCPAndUDP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("SSH-2.0-test\r\n"))
			c.Close()
		}
	}()
	ctx := context.Background()
	addr := ln.Addr().String()
	if r := Run(ctx, Spec{Type: TypeTCP, Target: addr}); r.Status != Up {
		t.Errorf("tcp: %+v", r)
	}
	if r := Run(ctx, Spec{Type: TypeTCP, Target: addr, Expect: "SSH-2.0"}); r.Status != Up {
		t.Errorf("tcp expect: %+v", r)
	}
	if r := Run(ctx, Spec{Type: TypeTCP, Target: addr, Expect: "HTTP", TimeoutMS: 500}); r.Status != Down {
		t.Errorf("tcp expect mismatch: %+v", r)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 100)
		for {
			n, a, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(append([]byte("pong:"), buf[:n]...), a)
		}
	}()
	if r := Run(ctx, Spec{Type: TypeUDP, Target: pc.LocalAddr().String(), Send: `ping\n`, Expect: "pong:ping"}); r.Status != Up {
		t.Errorf("udp: %+v", r)
	}
}

func TestValidateAndHelpers(t *testing.T) {
	bad := []Spec{{Type: "x"}, {Type: TypeHTTP, Target: "ftp://a"}, {Type: TypeTCP, Target: "host"},
		{Type: TypeICMP, Target: ""}, {Type: TypeHTTP, Target: "http://a", Keyword: "(", KeywordRegex: true}}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%+v accepted", s)
		}
	}
	if v, err := JSONPath([]byte(`{"a":{"b":[1,{"c":true}]}}`), "a.b.1.c"); err != nil || v != "true" {
		t.Errorf("jsonpath: %q %v", v, err)
	}
	if unescape(`a\r\nb\x41`) != "a\r\nbA" {
		t.Error("unescape")
	}
	if r := Run(context.Background(), Spec{Type: TypeDNS, Target: "localhost"}); r.Status != Up {
		t.Logf("dns localhost: %+v (resolver dependent)", r)
	}
}

func TestDomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com":
			_, _ = w.Write([]byte(`{"events":[{"eventAction":"registration","eventDate":"2000-01-01T00:00:00Z"},
				{"eventAction":"expiration","eventDate":"` + time.Now().Add(400*24*time.Hour).UTC().Format(time.RFC3339) + `"}]}`))
		case "/soon.com":
			_, _ = w.Write([]byte(`{"events":[{"eventAction":"expiration","eventDate":"` + time.Now().Add(5*24*time.Hour).UTC().Format(time.RFC3339) + `"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := RDAPBase
	RDAPBase = srv.URL + "/"
	defer func() { RDAPBase = old }()
	ctx := context.Background()
	if r := Run(ctx, Spec{Type: TypeDomain, Target: "example.com"}); r.Status != Up || r.CertNotAfter == 0 {
		t.Errorf("domain: %+v", r)
	}
	if r := Run(ctx, Spec{Type: TypeDomain, Target: "soon.com"}); r.Status != Degraded {
		t.Errorf("expiring domain: %+v", r)
	}
	if r := Run(ctx, Spec{Type: TypeDomain, Target: "missing.com"}); r.Status != Down {
		t.Errorf("unregistered domain: %+v", r)
	}
}

func TestExpr(t *testing.T) {
	state := map[int64]bool{1: true, 2: false, 3: true}
	up := func(id int64) (bool, bool) {
		v, ok := state[id]
		return v, ok
	}
	cases := map[string][2]bool{ // value, known
		"#1":                {true, true},
		"#1 && #2":          {false, true},
		"#1 && (#2 || #3)":  {true, true},
		"!#2":               {true, true},
		"#2 && #9":          {false, true}, // false && unknown
		"#1 && #9":          {false, false},
		"#1 || #9":          {true, true},
		"!(#1 && #3) || #2": {false, true},
	}
	for s, want := range cases {
		e, err := ParseExpr(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		v, k := e.Eval(up)
		if v != want[0] || k != want[1] {
			t.Errorf("%s = %v,%v want %v", s, v, k, want)
		}
	}
	for _, bad := range []string{"", "#", "#1 &&", "(#1", "#1 #2", "a", "#0"} {
		if _, err := ParseExpr(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	e, _ := ParseExpr("#4 || (#5 && !#6)")
	if ids := e.IDs(); len(ids) != 3 || ids[0] != 4 || ids[2] != 6 {
		t.Errorf("ids: %v", ids)
	}
}
