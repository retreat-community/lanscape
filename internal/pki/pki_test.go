package pki

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
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

// TestServerCertChainAfterRestart: the gateway certificate loaded from disk must still carry the
// CA, otherwise agents that pin its fingerprint fail with "server CA fingerprint mismatch".
func TestServerCertChainAfterRestart(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"lanscape.lan", "192.168.1.10"}
	ca, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ca.ServerCert(hosts)
	if err != nil {
		t.Fatal(err)
	}
	endsWithCA := func(c *tls.Certificate, ca *CA) bool {
		return len(c.Certificate) == 2 && bytes.Equal(c.Certificate[1], ca.Cert.Raw)
	}
	if !endsWithCA(first, ca) {
		t.Fatalf("issued chain has %d certificates", len(first.Certificate))
	}
	// restart: a new CA object loads server.crt from disk
	restarted, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.ServerCert(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Certificate[0], first.Certificate[0]) || !endsWithCA(loaded, restarted) {
		t.Fatalf("loaded chain: reissued %v, %d certificates", !bytes.Equal(loaded.Certificate[0], first.Certificate[0]),
			len(loaded.Certificate))
	}
	// server.crt of older versions holds the leaf only: the CA is added on load
	leafOnly := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: first.Certificate[0]})
	if err := os.WriteFile(filepath.Join(dir, "server.crt"), leafOnly, 0o644); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	old, _ := LoadOrCreate(dir)
	c, err := old.ServerCert(hosts)
	if err != nil || !bytes.Equal(c.Certificate[0], first.Certificate[0]) || !endsWithCA(c, old) {
		t.Fatalf("leaf-only server.crt: %v, %d certificates", err, len(c.Certificate))
	}
	// a TLS client pinning the CA fingerprint (as the agent does on registration) finds the CA
	// in the chain presented after the restart
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{*c}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		if conn, err := l.Accept(); err == nil {
			_ = conn.(*tls.Conn).Handshake()
			_ = conn.Close()
		}
	}()
	pinned := false
	conn, err := tls.Dial("tcp", l.Addr().String(), &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, //nolint:gosec // pinned below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, r := range raw {
				if pc, err := x509.ParseCertificate(r); err == nil && pc.IsCA && Fingerprint(pc) == Fingerprint(ca.Cert) {
					pinned = true
				}
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if !pinned {
		t.Error("the CA is not in the chain presented after a restart")
	}
}
