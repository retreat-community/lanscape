package pki

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestCAFlow(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreate(dir)
	if err != nil || Fingerprint(again.Cert) != Fingerprint(ca.Cert) {
		t.Fatalf("reload: %v", err)
	}
	srv, err := ca.ServerCert([]string{"127.0.0.1", "lanscape.local"})
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, csrPEM, err := NewKeyAndCSR("n1")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, cert, err := ca.SignAgent(csrPEM, "agent-42")
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := AgentID(cert); !ok || id != "agent-42" || NeedsRenewal(certPEM) {
		t.Fatalf("agent cert: %v %v", id, ok)
	}
	// mTLS handshake with the issued certificates
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{*srv}, ClientCAs: ca.Pool(), ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		tc := c.(*tls.Conn)
		if err := tc.Handshake(); err != nil {
			got <- err.Error()
			return
		}
		id, _ := AgentID(tc.ConnectionState().PeerCertificates[0])
		got <- id
		c.Close()
	}()
	cc, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)
	c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{Certificates: []tls.Certificate{cc}, RootCAs: pool,
		ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Handshake()
	c.Close()
	if id := <-got; id != "agent-42" {
		t.Fatalf("server saw %q", id)
	}
}
