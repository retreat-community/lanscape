package monitor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// answer builds a reply with A 10.1.2.3 for any A question.
func answer(q []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		return nil
	}
	qs, _ := p.AllQuestions()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
	_ = b.StartQuestions()
	for _, x := range qs {
		_ = b.Question(x)
	}
	_ = b.StartAnswers()
	for _, x := range qs {
		if x.Type == dnsmessage.TypeA {
			_ = b.AResource(dnsmessage.ResourceHeader{Name: x.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{10, 1, 2, 3}})
		}
	}
	out, _ := b.Finish()
	return out
}

// serveStream answers length-prefixed queries on a stream listener (TCP or TLS).
func serveStream(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			for {
				var n [2]byte
				if _, err := io.ReadFull(c, n[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(n[:]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				a := answer(q)
				binary.BigEndian.PutUint16(n[:], uint16(len(a)))
				_, _ = c.Write(append(n[:], a...))
			}
		}(c)
	}
}

func TestDNSTransports(t *testing.T) {
	check := func(server string) {
		t.Helper()
		r := Run(context.Background(), Spec{Type: TypeDNS, Target: "nas.home.arpa", Server: server, Expect: "10.1.2.3"})
		if r.Status != Up {
			t.Errorf("%s: %+v", server, r)
		}
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer tcp.Close()
	go serveStream(tcp)
	check("tcp://" + tcp.Addr().String())

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/dns-message" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		q, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(answer(q))
	}))
	defer ts.Close()
	oldClient, oldRoots := dohClient, dnsRootCAs
	defer func() { dohClient, dnsRootCAs = oldClient, oldRoots }()
	dohClient = ts.Client()
	check(ts.URL + "/dns-query")

	// DNS over TLS with the test server's certificate (issued for 127.0.0.1)
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	dnsRootCAs = pool
	cert := ts.TLS.Certificates[0]
	dot, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer dot.Close()
	go serveStream(dot)
	check("tls://" + dot.Addr().String())

	// a wrong expectation is reported with the answer
	r := Run(context.Background(), Spec{Type: TypeDNS, Target: "nas.home.arpa", Server: "tcp://" + tcp.Addr().String(), Expect: "10.9.9.9"})
	if r.Status != Down || !strings.Contains(r.Message, "10.1.2.3") {
		t.Errorf("expect: %+v", r)
	}
}
