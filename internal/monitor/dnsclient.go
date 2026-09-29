package monitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// resolver returns a resolver for a DNS server given as host[:53] (UDP, then TCP),
// tcp://host[:53], tls://host[:853] (DNS over TLS) or https://host/dns-query (DNS over HTTPS).
// Go's resolver frames messages for stream connections itself, so DoT is a TLS connection and
// DoH a connection that posts each framed query.
func resolver(server string) *net.Resolver {
	if server == "" {
		return net.DefaultResolver
	}
	withPort := func(h, port string) string {
		if _, _, err := net.SplitHostPort(h); err != nil {
			return net.JoinHostPort(h, port)
		}
		return h
	}
	var dial func(ctx context.Context, network string) (net.Conn, error)
	switch {
	case strings.HasPrefix(server, "https://"):
		dial = func(ctx context.Context, _ string) (net.Conn, error) { return &dohConn{ctx: ctx, url: server}, nil }
	case strings.HasPrefix(server, "tls://"):
		addr := withPort(strings.TrimPrefix(server, "tls://"), "853")
		host, _, _ := net.SplitHostPort(addr)
		dial = func(ctx context.Context, _ string) (net.Conn, error) {
			d := tls.Dialer{Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: dnsRootCAs}}
			return d.DialContext(ctx, "tcp", addr)
		}
	case strings.HasPrefix(server, "tcp://"):
		addr := withPort(strings.TrimPrefix(server, "tcp://"), "53")
		dial = func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}
	default:
		addr := withPort(server, "53")
		dial = func(ctx context.Context, network string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dial(ctx, network)
	}}
}

// dohConn turns length-prefixed DNS messages into RFC 8484 POST requests.
type dohConn struct {
	ctx      context.Context
	url      string
	out, in  bytes.Buffer
	deadline time.Time
}

var (
	dohClient  = &http.Client{Timeout: 10 * time.Second}
	dnsRootCAs *x509.CertPool // nil = system roots (tests replace it)
)

func (c *dohConn) Write(b []byte) (int, error) {
	c.out.Write(b)
	for c.out.Len() >= 2 {
		n := int(binary.BigEndian.Uint16(c.out.Bytes()[:2]))
		if c.out.Len() < 2+n {
			break
		}
		msg := make([]byte, n)
		c.out.Next(2)
		_, _ = c.out.Read(msg)
		if err := c.roundTrip(msg); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func (c *dohConn) roundTrip(msg []byte) error {
	ctx := c.ctx
	if !c.deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, c.deadline)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := dohClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("doh: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65535))
	if err != nil {
		return err
	}
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(body)))
	c.in.Write(l[:])
	c.in.Write(body)
	return nil
}

func (c *dohConn) Read(b []byte) (int, error) {
	if c.in.Len() == 0 {
		return 0, errors.New("doh: no response")
	}
	return c.in.Read(b)
}

func (c *dohConn) Close() error                       { return nil }
func (c *dohConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *dohConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (c *dohConn) SetDeadline(t time.Time) error      { c.deadline = t; return nil }
func (c *dohConn) SetReadDeadline(t time.Time) error  { c.deadline = t; return nil }
func (c *dohConn) SetWriteDeadline(t time.Time) error { c.deadline = t; return nil }
