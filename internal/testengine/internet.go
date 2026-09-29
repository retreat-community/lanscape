package testengine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/netio"
)

// Gateway is a default route of the host.
type Gateway struct {
	Dev     string `json:"dev"`
	Gateway string `json:"gateway"`
	Src     string `json:"src,omitempty"`
}

// InternetParams configure the Internet test (§6.1): the public address and, optionally, the
// download speed through each gateway.
type InternetParams struct {
	IPURL       string        `json:"ip_url"`       // plain text address or Cloudflare trace ("ip=…")
	DownloadURL string        `json:"download_url"` // large file; empty = no speed test
	MaxBytes    int64         `json:"max_bytes,omitempty"`
	Duration    time.Duration `json:"duration,omitempty"` // download time limit
}

// Default Internet test endpoints (used only when an administrator enables the test).
const (
	DefaultIPURL       = "https://1.1.1.1/cdn-cgi/trace"
	DefaultDownloadURL = "https://speed.cloudflare.com/__down?bytes=50000000"
)

// InternetResult is the outcome for one gateway.
type InternetResult struct {
	Dev       string  `json:"dev,omitempty"`
	Gateway   string  `json:"gateway,omitempty"`
	OK        bool    `json:"ok"`
	Error     string  `json:"error,omitempty"`
	PublicIP  string  `json:"public_ip,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"` // time to the first response byte of the address request
	DownMbps  float64 `json:"down_mbps,omitempty"`
}

// Internet runs the test through every gateway (bound to its interface) or, without gateways,
// through the default route.
func Internet(ctx context.Context, gws []Gateway, p InternetParams) []InternetResult {
	if p.IPURL == "" {
		p.IPURL = DefaultIPURL
	}
	if p.Duration <= 0 || p.Duration > 30*time.Second {
		p.Duration = 10 * time.Second
	}
	if p.MaxBytes <= 0 {
		p.MaxBytes = 200 << 20
	}
	if len(gws) == 0 {
		gws = []Gateway{{}}
	}
	out := make([]InternetResult, 0, len(gws))
	for _, g := range gws {
		out = append(out, internetVia(ctx, g, p))
	}
	return out
}

func internetVia(ctx context.Context, g Gateway, p InternetParams) InternetResult {
	res := InternetResult{Dev: g.Dev, Gateway: g.Gateway}
	d := &net.Dialer{Timeout: 5 * time.Second, Control: netio.BindControl(g.Dev, false)}
	if ip := net.ParseIP(g.Src); ip != nil {
		d.LocalAddr = &net.TCPAddr{IP: ip}
	}
	cl := &http.Client{Timeout: p.Duration + 10*time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		}, DisableKeepAlives: true, ForceAttemptHTTP2: true}}
	start := time.Now()
	body, err := get(ctx, cl, p.IPURL, 4096)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	res.PublicIP = parsePublicIP(string(body))
	if res.PublicIP == "" {
		res.Error = "no address in the response"
		return res
	}
	res.OK = true
	if p.DownloadURL != "" {
		res.DownMbps, err = download(ctx, cl, p)
		if err != nil {
			res.Error = "speed: " + err.Error()
		}
	}
	return res
}

func get(ctx context.Context, cl *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// parsePublicIP accepts a bare address or "ip=<addr>" lines (Cloudflare trace).
func parsePublicIP(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		l = strings.TrimPrefix(l, "ip=")
		if ip := net.ParseIP(l); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// download reads the file for at most p.Duration and returns the rate after the first second
// (TCP slow start is not counted).
func download(ctx context.Context, cl *http.Client, p InternetParams) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, p.Duration+5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.DownloadURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s", resp.Status)
	}
	buf := make([]byte, 64<<10)
	start := time.Now()
	var warm time.Time
	var total, warmBytes int64
	for time.Since(start) < p.Duration && total < p.MaxBytes {
		n, err := resp.Body.Read(buf)
		total += int64(n)
		if warm.IsZero() && time.Since(start) >= time.Second {
			warm, warmBytes = time.Now(), total
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if warm.IsZero() || time.Since(warm) < 200*time.Millisecond {
		// short transfer: use the whole time
		warm, warmBytes = start, 0
	}
	secs := time.Since(warm).Seconds()
	if secs <= 0 {
		return 0, errors.New("transfer too short")
	}
	return float64(total-warmBytes) * 8 / secs / 1e6, nil
}
