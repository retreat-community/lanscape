// Package pki manages the server CA, the gateway certificate and agent certificates.
// Agents register over TLS with a token, receive a certificate signed by the CA and
// then authenticate with mTLS; certificates are renewed 30 days before expiry.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Validity periods.
const (
	CAValidity   = 10 * 365 * 24 * time.Hour
	CertValidity = 365 * 24 * time.Hour
	RenewBefore  = 30 * 24 * time.Hour
	AgentOU      = "lanscape-agent"
	ServerOU     = "lanscape-server"
)

// CA is the server certificate authority.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     crypto.Signer
	dir     string

	mu     sync.Mutex
	server *tls.Certificate
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

// LoadOrCreate loads the CA from dir (data/pki) or creates a new one.
func LoadOrCreate(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	cpem, cerr := os.ReadFile(certPath)
	kpem, kerr := os.ReadFile(keyPath)
	if cerr == nil && kerr == nil {
		cert, err := parseCert(cpem)
		if err != nil {
			return nil, err
		}
		key, err := parseKey(kpem)
		if err != nil {
			return nil, err
		}
		return &CA{Cert: cert, CertPEM: cpem, key: key, dir: dir}, nil
	}
	if !errors.Is(cerr, os.ErrNotExist) && cerr != nil {
		return nil, cerr
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Lanscape CA", Organization: []string{"Lanscape"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	cpem = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, cpem, 0o644); err != nil {
		return nil, err
	}
	return &CA{Cert: cert, CertPEM: cpem, key: key, dir: dir}, nil
}

func parseCert(p []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(p)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("pki: no certificate in PEM")
	}
	return x509.ParseCertificate(b.Bytes)
}

func parseKey(p []byte) (crypto.Signer, error) {
	b, _ := pem.Decode(p)
	if b == nil {
		return nil, errors.New("pki: no key in PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	s, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("pki: unsupported key type")
	}
	return s, nil
}

// Fingerprint returns the SHA-256 fingerprint of a certificate in hex.
func Fingerprint(c *x509.Certificate) string {
	s := sha256.Sum256(c.Raw)
	return hex.EncodeToString(s[:])
}

// Pool returns a cert pool with the CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

// ServerCert returns the gateway certificate for hosts, issuing or renewing it as needed.
func (ca *CA) ServerCert(hosts []string) (*tls.Certificate, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if ca.server != nil && time.Until(ca.server.Leaf.NotAfter) > RenewBefore {
		return ca.server, nil
	}
	certPath, keyPath := filepath.Join(ca.dir, "server.crt"), filepath.Join(ca.dir, "server.key")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err == nil && time.Until(leaf.NotAfter) > RenewBefore && coversHosts(leaf, hosts) {
			c.Leaf = leaf
			ca.server = &c
			return ca.server, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "lanscape", OrganizationalUnit: []string{ServerOU}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(CertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	cpem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kpem := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
	if err := os.WriteFile(keyPath, kpem, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, cpem, 0o644); err != nil {
		return nil, err
	}
	c, err := tls.X509KeyPair(append(cpem, ca.CertPEM...), kpem)
	if err != nil {
		return nil, err
	}
	c.Leaf, _ = x509.ParseCertificate(der)
	ca.server = &c
	return ca.server, nil
}

func coversHosts(c *x509.Certificate, hosts []string) bool {
	for _, h := range hosts {
		if h != "" && c.VerifyHostname(h) != nil {
			return false
		}
	}
	return true
}

// SignAgent issues a client certificate for agentID from a PEM CSR.
func (ca *CA) SignAgent(csrPEM []byte, agentID string) ([]byte, *x509.Certificate, error) {
	b, _ := pem.Decode(csrPEM)
	if b == nil || b.Type != "CERTIFICATE REQUEST" {
		return nil, nil, errors.New("pki: no CSR in PEM")
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, fmt.Errorf("pki: CSR signature: %w", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: agentID, OrganizationalUnit: []string{AgentOU}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(CertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, nil
}

// AgentID extracts the agent id from a verified client certificate.
func AgentID(c *x509.Certificate) (string, bool) {
	for _, ou := range c.Subject.OrganizationalUnit {
		if ou == AgentOU {
			return c.Subject.CommonName, c.Subject.CommonName != ""
		}
	}
	return "", false
}

// NewKeyAndCSR creates an agent key and CSR (PEM).
func NewKeyAndCSR(name string) (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: name},
	}, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// NeedsRenewal reports whether a certificate expires within RenewBefore.
func NeedsRenewal(certPEM []byte) bool {
	c, err := parseCert(certPEM)
	return err != nil || time.Until(c.NotAfter) < RenewBefore
}

// ParseCert parses a PEM certificate.
func ParseCert(p []byte) (*x509.Certificate, error) { return parseCert(p) }
