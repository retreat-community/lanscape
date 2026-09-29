// Package monitor executes availability checks (HTTP, TCP, UDP, ICMP, DNS, TLS). The same
// code runs on the server and on agents, so a check can be observed from several points.
package monitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/testengine"
)

// Check types.
const (
	TypeHTTP = "http"
	TypeTCP  = "tcp"
	TypeUDP  = "udp"
	TypeICMP = "icmp"
	TypeDNS  = "dns"
	TypeTLS  = "tls"
)

// Spec describes one check. Target is a URL for HTTP, host:port for TCP/UDP/TLS, a host for
// ICMP and a record name for DNS.
type Spec struct {
	Type      string `json:"type"`
	Target    string `json:"target"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`

	// HTTP
	Method          string            `json:"method,omitempty"`
	ExpectStatus    []int             `json:"expect_status,omitempty"` // default 200–399
	Keyword         string            `json:"keyword,omitempty"`       // substring, or a regex when KeywordRegex
	KeywordRegex    bool              `json:"keyword_regex,omitempty"`
	InvertKeyword   bool              `json:"invert_keyword,omitempty"`
	JSONPath        string            `json:"json_path,omitempty"` // dotted path, e.g. status or data.0.state
	JSONValue       string            `json:"json_value,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	BasicUser       string            `json:"basic_user,omitempty"`
	BasicPassword   string            `json:"basic_password,omitempty"`
	Bearer          string            `json:"bearer,omitempty"`
	NoRedirects     bool              `json:"no_redirects,omitempty"`
	IgnoreTLSErrors bool              `json:"ignore_tls_errors,omitempty"`

	// TCP / UDP
	Send   string `json:"send,omitempty"`
	Expect string `json:"expect,omitempty"`

	// DNS
	Server string `json:"server,omitempty"` // host[:53]; empty = system resolver
	Record string `json:"record,omitempty"` // A, AAAA, CNAME, MX, TXT, NS

	// TLS (also applies to https:// HTTP checks)
	WarnDays int `json:"warn_days,omitempty"`
}

// Status of a check result.
const (
	Up       = "up"
	Down     = "down"
	Degraded = "degraded" // works, but e.g. the certificate expires soon
)

// Result of one check execution.
type Result struct {
	Status       string   `json:"status"`
	LatencyMS    float64  `json:"latency_ms"`
	Message      string   `json:"message,omitempty"`
	Code         int      `json:"code,omitempty"`
	CertNotAfter int64    `json:"cert_not_after,omitempty"` // unix ms
	CertNames    []string `json:"cert_names,omitempty"`
	At           int64    `json:"at"`
	Point        string   `json:"point,omitempty"` // observation point (server or agent id)
}

// Validate checks that a spec is complete.
func (s *Spec) Validate() error {
	switch s.Type {
	case TypeHTTP:
		u, err := url.Parse(s.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("target must be an http(s) URL")
		}
		if s.KeywordRegex {
			if _, err := regexp.Compile(s.Keyword); err != nil {
				return fmt.Errorf("keyword: %w", err)
			}
		}
	case TypeTCP, TypeUDP, TypeTLS:
		if _, _, err := net.SplitHostPort(s.Target); err != nil {
			return errors.New("target must be host:port")
		}
	case TypeICMP, TypeDNS:
		if s.Target == "" || strings.ContainsAny(s.Target, "/ ") {
			return errors.New("target must be a host name or address")
		}
	default:
		return fmt.Errorf("unknown check type %q", s.Type)
	}
	return nil
}

func (s *Spec) timeout() time.Duration {
	if s.TimeoutMS <= 0 {
		return 10 * time.Second
	}
	return time.Duration(s.TimeoutMS) * time.Millisecond
}

// Run executes a check.
func Run(ctx context.Context, s Spec) Result {
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	start := time.Now()
	var r Result
	switch s.Type {
	case TypeHTTP:
		r = runHTTP(ctx, &s)
	case TypeTCP:
		r = runTCP(ctx, &s)
	case TypeUDP:
		r = runUDP(ctx, &s)
	case TypeICMP:
		r = runICMP(ctx, &s)
	case TypeDNS:
		r = runDNS(ctx, &s)
	case TypeTLS:
		r = runTLS(ctx, &s)
	default:
		r = Result{Status: Down, Message: "unknown check type " + s.Type}
	}
	if r.LatencyMS == 0 && r.Status != Down {
		r.LatencyMS = ms(time.Since(start))
	}
	r.At = time.Now().UnixMilli()
	return r
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func down(format string, a ...any) Result {
	return Result{Status: Down, Message: fmt.Sprintf(format, a...)}
}

// certResult fills certificate fields and degrades the status when it expires soon.
func certResult(r *Result, st *tls.ConnectionState, warnDays int) {
	if st == nil || len(st.PeerCertificates) == 0 {
		return
	}
	c := st.PeerCertificates[0]
	r.CertNotAfter = c.NotAfter.UnixMilli()
	r.CertNames = c.DNSNames
	if warnDays <= 0 {
		warnDays = 14
	}
	left := time.Until(c.NotAfter)
	switch {
	case left <= 0:
		r.Status, r.Message = Down, "certificate expired "+c.NotAfter.UTC().Format(time.DateOnly)
	case left < time.Duration(warnDays)*24*time.Hour && r.Status == Up:
		r.Status = Degraded
		r.Message = fmt.Sprintf("certificate expires in %d days", int(left.Hours()/24))
	}
}

func runHTTP(ctx context.Context, s *Spec) Result {
	method := s.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, s.Target, nil)
	if err != nil {
		return down("%v", err)
	}
	req.Header.Set("User-Agent", "lanscape-monitor")
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	if s.BasicUser != "" {
		req.SetBasicAuth(s.BasicUser, s.BasicPassword)
	}
	if s.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+s.Bearer)
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: s.IgnoreTLSErrors}} //nolint:gosec // opt-in per monitor
	cl := &http.Client{Transport: tr}
	if s.NoRedirects {
		cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	start := time.Now()
	resp, err := cl.Do(req)
	if err != nil {
		var ce *tls.CertificateVerificationError
		if errors.As(err, &ce) {
			return down("TLS: %v", ce.Err)
		}
		return down("%v", trimURLErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	r := Result{Status: Up, LatencyMS: ms(time.Since(start)), Code: resp.StatusCode}
	if !statusOK(resp.StatusCode, s.ExpectStatus) {
		r.Status, r.Message = Down, fmt.Sprintf("HTTP %d", resp.StatusCode)
		return r
	}
	if s.Keyword != "" {
		var found bool
		if s.KeywordRegex {
			re, err := regexp.Compile(s.Keyword)
			if err != nil {
				return down("keyword: %v", err)
			}
			found = re.Match(body)
		} else {
			found = bytes.Contains(body, []byte(s.Keyword))
		}
		if found == s.InvertKeyword {
			r.Status = Down
			if s.InvertKeyword {
				r.Message = fmt.Sprintf("keyword %q present", s.Keyword)
			} else {
				r.Message = fmt.Sprintf("keyword %q not found", s.Keyword)
			}
			return r
		}
	}
	if s.JSONPath != "" {
		v, err := JSONPath(body, s.JSONPath)
		if err != nil {
			r.Status, r.Message = Down, err.Error()
			return r
		}
		if s.JSONValue != "" && v != s.JSONValue {
			r.Status, r.Message = Down, fmt.Sprintf("%s = %q, want %q", s.JSONPath, v, s.JSONValue)
			return r
		}
	}
	certResult(&r, resp.TLS, s.WarnDays)
	return r
}

func trimURLErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func statusOK(code int, want []int) bool {
	if len(want) == 0 {
		return code >= 200 && code < 400
	}
	for _, w := range want {
		// 2 means 2xx, 200 means exactly 200
		if w == code || (w < 10 && code/100 == w) {
			return true
		}
	}
	return false
}

// JSONPath returns the value at a dotted path ("a.b.0.c") as a string.
func JSONPath(body []byte, path string) (string, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", errors.New("response is not JSON")
	}
	for _, p := range strings.Split(strings.TrimPrefix(path, "$."), ".") {
		switch t := v.(type) {
		case map[string]any:
			x, ok := t[p]
			if !ok {
				return "", fmt.Errorf("%s: no field %q", path, p)
			}
			v = x
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(t) {
				return "", fmt.Errorf("%s: bad index %q", path, p)
			}
			v = t[i]
		default:
			return "", fmt.Errorf("%s: %q is not an object", path, p)
		}
	}
	switch t := v.(type) {
	case string:
		return t, nil
	case nil:
		return "null", nil
	default:
		b, _ := json.Marshal(t)
		return string(b), nil
	}
}

func runTCP(ctx context.Context, s *Spec) Result {
	var d net.Dialer
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", s.Target)
	if err != nil {
		return down("%v", err)
	}
	defer c.Close()
	r := Result{Status: Up, LatencyMS: ms(time.Since(start))}
	if s.Send == "" && s.Expect == "" {
		return r
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if s.Send != "" {
		if _, err := c.Write([]byte(unescape(s.Send))); err != nil {
			return down("send: %v", err)
		}
	}
	if s.Expect != "" {
		if err := expect(c, unescape(s.Expect)); err != nil {
			return down("%v", err)
		}
	}
	return r
}

func expect(c net.Conn, want string) error {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for len(buf) < cap(buf) {
		n, err := c.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if strings.Contains(string(buf), want) {
			return nil
		}
		if err != nil {
			break
		}
	}
	return fmt.Errorf("response does not contain %q", want)
}

// unescape handles \n, \r, \t and \xNN in send/expect strings.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return u
	}
	return s
}

func runUDP(ctx context.Context, s *Spec) Result {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", s.Target)
	if err != nil {
		return down("%v", err)
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	payload := unescape(s.Send)
	if payload == "" {
		payload = "\n"
	}
	start := time.Now()
	if _, err := c.Write([]byte(payload)); err != nil {
		return down("send: %v", err)
	}
	if s.Expect == "" {
		// without an expected answer only an ICMP port-unreachable can prove the port closed
		buf := make([]byte, 1)
		c.SetReadDeadline(time.Now().Add(min(time.Second, time.Until(deadline(ctx))))) //nolint:errcheck // best effort
		if _, err := c.Read(buf); err != nil && !isTimeout(err) {
			return down("%v", err)
		}
		return Result{Status: Up, LatencyMS: ms(time.Since(start))}
	}
	if err := expect(c, unescape(s.Expect)); err != nil {
		return down("%v", err)
	}
	return Result{Status: Up, LatencyMS: ms(time.Since(start))}
}

func deadline(ctx context.Context) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		return dl
	}
	return time.Now().Add(time.Second)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func runICMP(ctx context.Context, s *Spec) Result {
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", s.Target)
	if err != nil || len(ips) == 0 {
		return down("resolve %s: %v", s.Target, err)
	}
	p := testengine.Ping(ctx, testengine.PingParams{Dst: ips[0], Count: 3, Interval: 200 * time.Millisecond})
	if p.Status != testengine.StatusOK {
		return down("ping: %s %s", p.Status, p.Message)
	}
	if p.Recv == 0 {
		return down("no echo replies from %s", ips[0])
	}
	r := Result{Status: Up, LatencyMS: float64(p.RTTAvgUS) / 1000}
	if p.Recv < p.Sent {
		r.Message = fmt.Sprintf("%d/%d replies", p.Recv, p.Sent)
	}
	return r
}

func resolver(server string) *net.Resolver {
	if server == "" {
		return net.DefaultResolver
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "53")
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, server)
	}}
}

func runDNS(ctx context.Context, s *Spec) Result {
	res := resolver(s.Server)
	start := time.Now()
	var vals []string
	var err error
	switch strings.ToUpper(s.Record) {
	case "", "A", "AAAA":
		fam := "ip4"
		if strings.EqualFold(s.Record, "AAAA") {
			fam = "ip6"
		}
		var ips []net.IP
		ips, err = res.LookupIP(ctx, fam, s.Target)
		for _, ip := range ips {
			vals = append(vals, ip.String())
		}
	case "CNAME":
		var c string
		c, err = res.LookupCNAME(ctx, s.Target)
		vals = []string{strings.TrimSuffix(c, ".")}
	case "MX":
		var mx []*net.MX
		mx, err = res.LookupMX(ctx, s.Target)
		for _, m := range mx {
			vals = append(vals, strings.TrimSuffix(m.Host, "."))
		}
	case "TXT":
		vals, err = res.LookupTXT(ctx, s.Target)
	case "NS":
		var ns []*net.NS
		ns, err = res.LookupNS(ctx, s.Target)
		for _, n := range ns {
			vals = append(vals, strings.TrimSuffix(n.Host, "."))
		}
	default:
		return down("unsupported record type %s", s.Record)
	}
	if err != nil {
		return down("%v", err)
	}
	if len(vals) == 0 {
		return down("no records")
	}
	r := Result{Status: Up, LatencyMS: ms(time.Since(start)), Message: strings.Join(vals, ", ")}
	if s.Expect != "" {
		for _, v := range vals {
			if v == s.Expect {
				return r
			}
		}
		r.Status = Down
		r.Message = fmt.Sprintf("got %s, want %s", strings.Join(vals, ", "), s.Expect)
	}
	return r
}

func runTLS(ctx context.Context, s *Spec) Result {
	host, _, _ := net.SplitHostPort(s.Target)
	d := tls.Dialer{Config: &tls.Config{ServerName: host, InsecureSkipVerify: true}} //nolint:gosec // verified below to report the reason
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", s.Target)
	if err != nil {
		return down("%v", err)
	}
	defer c.Close()
	st := c.(*tls.Conn).ConnectionState()
	r := Result{Status: Up, LatencyMS: ms(time.Since(start))}
	if !s.IgnoreTLSErrors && len(st.PeerCertificates) > 0 {
		pool := x509.NewCertPool()
		for _, ic := range st.PeerCertificates[1:] {
			pool.AddCert(ic)
		}
		name := host
		if net.ParseIP(host) != nil {
			name = ""
		}
		if _, err := st.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: name, Intermediates: pool}); err != nil {
			r.Status, r.Message = Down, err.Error()
		}
	}
	certResult(&r, &st, s.WarnDays)
	return r
}
