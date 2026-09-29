// Package fingerprint recognises self-hosted applications by HTTP responses (title,
// headers, characteristic paths and body text) and by container images. The built-in
// library is embedded; users can add signatures in the same YAML format.
package fingerprint

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:generate go run gen.go

// builtin is generated from signatures.yaml by "go generate".
//
//go:embed signatures.json
var builtin []byte

// Monitor is the recommended monitor for an application.
type Monitor struct {
	Type string `yaml:"type" json:"type"`
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
}

// PathProbe is a characteristic URL and a regex its body must match.
type PathProbe struct {
	Path string `yaml:"path" json:"path"`
	Body string `yaml:"body" json:"body"`
	re   *regexp.Regexp
}

// Signature describes one application.
type Signature struct {
	ID       string            `yaml:"id" json:"id"`
	Name     string            `yaml:"name" json:"name"`
	Category string            `yaml:"category" json:"category"`
	Icon     string            `yaml:"icon,omitempty" json:"icon,omitempty"`
	Title    []string          `yaml:"title,omitempty" json:"title,omitempty"`
	Headers  map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body     []string          `yaml:"body,omitempty" json:"body,omitempty"`
	Paths    []PathProbe       `yaml:"paths,omitempty" json:"paths,omitempty"`
	Images   []string          `yaml:"images,omitempty" json:"images,omitempty"`
	Ports    []int             `yaml:"ports,omitempty" json:"ports,omitempty"`
	Favicons []string          `yaml:"favicons,omitempty" json:"favicons,omitempty"` // sha256 prefixes of /favicon.ico
	Monitor  Monitor           `yaml:"monitor" json:"monitor"`

	title   []*regexp.Regexp
	headers map[string]*regexp.Regexp
	body    []*regexp.Regexp
}

// Library is a compiled set of signatures.
type Library struct {
	Sigs []*Signature
	byID map[string]*Signature
}

// Parse compiles signatures from JSON or (in builds with YAML support) YAML.
func Parse(data []byte) ([]*Signature, error) {
	var sigs []*Signature
	trimmed := strings.TrimSpace(string(data))
	var err error
	if strings.HasPrefix(trimmed, "[") {
		err = json.Unmarshal(data, &sigs)
	} else {
		err = unmarshalYAML(data, &sigs)
	}
	if err != nil {
		return nil, fmt.Errorf("fingerprint: %w", err)
	}
	for _, s := range sigs {
		if s.ID == "" || s.Name == "" {
			return nil, fmt.Errorf("fingerprint: signature without id or name")
		}
		for _, t := range s.Title {
			re, err := regexp.Compile("(?i)" + t)
			if err != nil {
				return nil, fmt.Errorf("fingerprint: %s title: %w", s.ID, err)
			}
			s.title = append(s.title, re)
		}
		s.headers = map[string]*regexp.Regexp{}
		for k, v := range s.Headers {
			re, err := regexp.Compile("(?i)" + v)
			if err != nil {
				return nil, fmt.Errorf("fingerprint: %s header %s: %w", s.ID, k, err)
			}
			s.headers[http.CanonicalHeaderKey(k)] = re
		}
		for _, b := range s.Body {
			re, err := regexp.Compile("(?i)" + b)
			if err != nil {
				return nil, fmt.Errorf("fingerprint: %s body: %w", s.ID, err)
			}
			s.body = append(s.body, re)
		}
		for i := range s.Paths {
			re, err := regexp.Compile("(?i)" + s.Paths[i].Body)
			if err != nil {
				return nil, fmt.Errorf("fingerprint: %s path: %w", s.ID, err)
			}
			s.Paths[i].re = re
		}
		if s.Monitor.Type == "" {
			s.Monitor.Type = "http"
		}
	}
	return sigs, nil
}

// Load returns the built-in library plus user signatures from extraFiles (later ids win).
func Load(extraFiles ...string) (*Library, error) {
	sigs, err := Parse(builtin)
	if err != nil {
		return nil, err
	}
	lib := &Library{byID: map[string]*Signature{}}
	add := func(ss []*Signature) {
		for _, s := range ss {
			if old, ok := lib.byID[s.ID]; ok {
				*old = *s
				continue
			}
			lib.byID[s.ID] = s
			lib.Sigs = append(lib.Sigs, s)
		}
	}
	add(sigs)
	for _, f := range extraFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		us, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		add(us)
	}
	return lib, nil
}

// With returns a copy of the library with extra signatures added; an extra signature with the id of
// an existing one replaces it. The receiver is not changed.
func (l *Library) With(extra []*Signature) *Library {
	out := &Library{Sigs: make([]*Signature, 0, len(l.Sigs)+len(extra)), byID: map[string]*Signature{}}
	idx := map[string]int{}
	for _, list := range [][]*Signature{l.Sigs, extra} {
		for _, s := range list {
			if i, ok := idx[s.ID]; ok {
				out.Sigs[i] = s
			} else {
				idx[s.ID] = len(out.Sigs)
				out.Sigs = append(out.Sigs, s)
			}
			out.byID[s.ID] = s
		}
	}
	return out
}

// Get returns a signature by id.
func (l *Library) Get(id string) (*Signature, bool) {
	s, ok := l.byID[id]
	return s, ok
}

// Observation is what was seen at an HTTP endpoint.
type Observation struct {
	URL      string
	Status   int
	Title    string
	Headers  http.Header
	Body     string
	Favicon  string            // sha256 hex of /favicon.ico
	PathHits map[string]string // path -> body (filled by Probe for candidate paths)
	Image    string
	Port     int
	TLS      *TLSInfo // leaf certificate of an https endpoint
}

// TLSInfo summarises the certificate presented by an endpoint.
type TLSInfo struct {
	Names    []string `json:"names,omitempty"`
	NotAfter int64    `json:"not_after"` // unix ms
	Issuer   string   `json:"issuer,omitempty"`
}

// Match is a recognised application with its score.
type Match struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Icon     string  `json:"icon,omitempty"`
	Score    int     `json:"score"`
	Monitor  Monitor `json:"monitor"`
}

const threshold = 3

func (s *Signature) score(o *Observation) int {
	sc := 0
	if o.Image != "" {
		img := strings.ToLower(o.Image)
		img = strings.TrimPrefix(img, "docker.io/")
		img = strings.TrimPrefix(img, "library/")
		if i := strings.LastIndexAny(img, ":@"); i > strings.LastIndex(img, "/") {
			img = img[:i]
		}
		for _, p := range s.Images {
			if img == p || strings.HasSuffix(img, "/"+p) {
				sc += 5
				break
			}
		}
	}
	for _, re := range s.title {
		if o.Title != "" && re.MatchString(o.Title) {
			sc += 3
			break
		}
	}
	for k, re := range s.headers {
		for _, v := range o.Headers.Values(k) {
			if re.MatchString(v) {
				sc += 3
				break
			}
		}
	}
	for _, re := range s.body {
		if re.MatchString(o.Body) {
			sc++
			break
		}
	}
	for _, p := range s.Paths {
		if b, ok := o.PathHits[p.Path]; ok && p.re.MatchString(b) {
			sc += 4
			break
		}
	}
	for _, f := range s.Favicons {
		if f != "" && strings.HasPrefix(o.Favicon, strings.ToLower(f)) {
			sc += 4
			break
		}
	}
	return sc
}

// Identify returns the best match or false.
func (l *Library) Identify(o *Observation) (Match, bool) {
	ms := l.Candidates(o)
	if len(ms) == 0 || ms[0].Score < threshold {
		return Match{}, false
	}
	return ms[0], true
}

// Candidates returns all signatures with a positive score, best first.
func (l *Library) Candidates(o *Observation) []Match {
	var out []Match
	for _, s := range l.Sigs {
		if sc := s.score(o); sc > 0 {
			out = append(out, Match{ID: s.ID, Name: s.Name, Category: s.Category, Icon: s.Icon, Score: sc, Monitor: s.Monitor})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	return out
}

// IdentifyImage recognises a container image.
func (l *Library) IdentifyImage(image string) (Match, bool) {
	return l.Identify(&Observation{Image: image})
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// ExtractTitle returns the HTML title.
func ExtractTitle(body string) string {
	m := titleRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(strings.Join(strings.Fields(m[1]), " ")))
}

// Prober fetches observations over HTTP(S).
type Prober struct {
	Client *http.Client
	Lib    *Library
}

// NewProber returns a prober that accepts self-signed certificates (internal services).
func NewProber(lib *Library) *Prober {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // fingerprinting internal services
		ResponseHeaderTimeout: 5 * time.Second, MaxIdleConnsPerHost: 2}
	return &Prober{Lib: lib, Client: &http.Client{Transport: tr, Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		}}}
}

type page struct {
	status  int
	headers http.Header
	body    string
	tls     *tls.ConnectionState
}

func (p *Prober) get(ctx context.Context, url string, limit int64) (page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return page{}, err
	}
	req.Header.Set("User-Agent", "lanscape-discovery")
	resp, err := p.Client.Do(req)
	if err != nil {
		return page{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return page{status: resp.StatusCode, headers: resp.Header, body: string(b), tls: resp.TLS}, err
}

// Probe fetches base (scheme://host:port), the favicon and characteristic paths of
// candidate applications, then identifies the application.
func (p *Prober) Probe(ctx context.Context, base string) (*Observation, Match, bool) {
	base = strings.TrimRight(base, "/")
	root, err := p.get(ctx, base+"/", 256<<10)
	if err != nil {
		return nil, Match{}, false
	}
	o := &Observation{URL: base, Status: root.status, Headers: root.headers, Body: root.body, Title: ExtractTitle(root.body),
		PathHits: map[string]string{}}
	if root.status < 400 {
		o.PathHits["/"] = root.body // signatures probing "/" reuse the page already fetched
	}
	if root.tls != nil && len(root.tls.PeerCertificates) > 0 {
		c := root.tls.PeerCertificates[0]
		o.TLS = &TLSInfo{Names: c.DNSNames, NotAfter: c.NotAfter.UnixMilli(), Issuer: c.Issuer.CommonName}
	}
	if fav, err := p.get(ctx, base+"/favicon.ico", 64<<10); err == nil && fav.status == http.StatusOK && fav.body != "" {
		s := sha256.Sum256([]byte(fav.body))
		o.Favicon = hex.EncodeToString(s[:])
	}
	// probe characteristic paths for the leading candidates and for signatures that only
	// have paths (JSON APIs without a title)
	tried := map[string]bool{"/": true}
	cands := p.Lib.Candidates(o)
	for _, s := range p.Lib.Sigs {
		inCands := false
		for i, c := range cands {
			if c.ID == s.ID && i < 5 {
				inCands = true
			}
		}
		if !inCands && (len(s.Title) > 0 || len(s.Headers) > 0) {
			continue
		}
		for _, pp := range s.Paths {
			if tried[pp.Path] || len(tried) >= 12 {
				continue
			}
			tried[pp.Path] = true
			if r, err := p.get(ctx, base+pp.Path, 64<<10); err == nil && r.status < 400 {
				o.PathHits[pp.Path] = r.body
			}
		}
	}
	m, ok := p.Lib.Identify(o)
	return o, m, ok
}
