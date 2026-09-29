// Package registry finds newer tags of container images in OCI registries (Docker Hub, GHCR,
// Quay, private registries) through the Distribution API with anonymous tokens.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Ref is a parsed image reference.
type Ref struct {
	Registry string // registry-1.docker.io, ghcr.io …
	Repo     string // library/nginx, org/app
	Tag      string // "" when the image is pinned by digest only
}

// String is the reference as users write it.
func (r Ref) String() string {
	repo := r.Repo
	if r.Registry == "registry-1.docker.io" {
		repo = strings.TrimPrefix(repo, "library/")
		return repo + ":" + r.Tag
	}
	return r.Registry + "/" + repo + ":" + r.Tag
}

// Parse splits an image reference: "nginx:1.25", "ghcr.io/org/app:v2.1.0@sha256:…".
func Parse(image string) (Ref, error) {
	image = strings.TrimSpace(image)
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	if image == "" || strings.HasPrefix(image, "sha256:") {
		return Ref{}, errors.New("registry: no image name")
	}
	var r Ref
	name := image
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, r.Tag = name[:i], name[i+1:]
	}
	if r.Tag == "" {
		r.Tag = "latest"
	}
	first, rest, ok := strings.Cut(name, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Registry, r.Repo = first, rest
	} else {
		r.Registry, r.Repo = "registry-1.docker.io", name
	}
	if r.Registry == "docker.io" || r.Registry == "index.docker.io" {
		r.Registry = "registry-1.docker.io"
	}
	if r.Registry == "registry-1.docker.io" && !strings.Contains(r.Repo, "/") {
		r.Repo = "library/" + r.Repo
	}
	return r, nil
}

// Client lists tags. Scheme is "https" unless a test serves plain HTTP.
type Client struct {
	HTTP   *http.Client
	Scheme string
}

// maxPages bounds the tag listing of repositories with thousands of tags.
const maxPages = 20

// Tags lists the tags of a repository.
func (c *Client) Tags(ctx context.Context, r Ref) ([]string, error) {
	scheme := c.Scheme
	if scheme == "" {
		scheme = "https"
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	next := fmt.Sprintf("%s://%s/v2/%s/tags/list?n=1000", scheme, r.Registry, r.Repo)
	token := ""
	var tags []string
	for page := 0; next != "" && page < maxPages; page++ {
		var body struct {
			Tags []string `json:"tags"`
		}
		hdr, err := getJSON(ctx, hc, next, token, &body)
		var ae authError
		if errors.As(err, &ae) && token == "" {
			if token, err = c.token(ctx, hc, ae.challenge, r.Repo); err != nil {
				return nil, err
			}
			hdr, err = getJSON(ctx, hc, next, token, &body)
		}
		if err != nil {
			return nil, err
		}
		tags = append(tags, body.Tags...)
		next = nextLink(next, hdr.Get("Link"))
	}
	return tags, nil
}

// authError is a 401 answer with its challenge.
type authError struct{ challenge string }

func (e authError) Error() string { return "registry: authentication required" }

// getJSON fetches and decodes a JSON document and returns the response headers.
func getJSON(ctx context.Context, hc *http.Client, u, token string, v any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return res.Header, authError{res.Header.Get("WWW-Authenticate")}
	case res.StatusCode != http.StatusOK:
		return res.Header, fmt.Errorf("registry: %s: %s", req.URL.Host, res.Status)
	}
	return res.Header, json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(v)
}

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// token fetches an anonymous pull token as the Bearer challenge asks.
func (*Client) token(ctx context.Context, hc *http.Client, challenge, repo string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", errors.New("registry: authentication required")
	}
	p := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(challenge, -1) {
		p[m[1]] = m[2]
	}
	if p["realm"] == "" {
		return "", errors.New("registry: bearer challenge without realm")
	}
	q := url.Values{}
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	scope := p["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if _, err := getJSON(ctx, hc, p["realm"]+"?"+q.Encode(), "", &body); err != nil {
		return "", err
	}
	if body.Token == "" {
		body.Token = body.AccessToken
	}
	return body.Token, nil
}

var linkRe = regexp.MustCompile(`<([^>]+)>;\s*rel="?next"?`)

func nextLink(cur, link string) string {
	m := linkRe.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	base, err := url.Parse(cur)
	if err != nil {
		return ""
	}
	u, err := base.Parse(m[1])
	if err != nil {
		return ""
	}
	return u.String()
}

// version is a tag split into its numbers and the prefix and suffix around them.
type version struct {
	prefix, suffix string
	nums           []int
}

var versionRe = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,3})(-[A-Za-z][\w.]*)?$`)

// prerelease suffixes are never offered as updates.
var prerelease = regexp.MustCompile(`(?i)^-(alpha|beta|rc|dev|pre|preview|nightly|snapshot|test)`)

func parseVersion(tag string) (version, bool) {
	m := versionRe.FindStringSubmatch(tag)
	if m == nil || prerelease.MatchString(m[3]) {
		return version{}, false
	}
	v := version{prefix: m[1], suffix: m[3]}
	for _, p := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		v.nums = append(v.nums, n)
	}
	return v, true
}

func (v version) less(o version) bool {
	for i := range v.nums {
		if v.nums[i] != o.nums[i] {
			return v.nums[i] < o.nums[i]
		}
	}
	return false
}

// Newer returns the highest tag of the same shape as current ("1.25.3" → "1.27.0", "v2.1-alpine" →
// "v2.3-alpine"), or "" when current is not a version ("latest") or is the newest.
func Newer(current string, tags []string) string {
	cur, ok := parseVersion(current)
	if !ok {
		return ""
	}
	best, bestTag := cur, ""
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok || v.prefix != cur.prefix || v.suffix != cur.suffix || len(v.nums) != len(cur.nums) {
			continue
		}
		if best.less(v) {
			best, bestTag = v, t
		}
	}
	return bestTag
}
