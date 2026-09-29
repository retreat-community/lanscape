package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/retreat-community/lanscape/internal/store"
)

// Tile is a dashboard link read from another dashboard's configuration.
type Tile struct {
	Group       string `json:"group"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Icon        string `json:"icon,omitempty"`
	Description string `json:"description,omitempty"`
}

// ParseTiles reads a Homepage services.yaml, a Homer config.yml or a Dashy conf.yml.
func ParseTiles(b []byte) (format string, tiles []Tile, err error) {
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", nil, fmt.Errorf("not YAML: %w", err)
	}
	str := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	switch d := doc.(type) {
	case map[string]any:
		// Homer: services: [{name, items: [{name, url, logo|icon, subtitle}]}]
		if groups, ok := d["services"].([]any); ok {
			for _, g := range groups {
				gm, _ := g.(map[string]any)
				items, _ := gm["items"].([]any)
				for _, it := range items {
					im, _ := it.(map[string]any)
					tiles = append(tiles, Tile{Group: str(gm, "name"), Name: str(im, "name"), URL: str(im, "url"),
						Icon: str(im, "logo", "icon"), Description: str(im, "subtitle")})
				}
			}
			return "homer", clean(tiles), nil
		}
		// Dashy: sections: [{name, items: [{title, url, icon, description}]}]
		if sections, ok := d["sections"].([]any); ok {
			for _, sct := range sections {
				sm, _ := sct.(map[string]any)
				items, _ := sm["items"].([]any)
				for _, it := range items {
					im, _ := it.(map[string]any)
					tiles = append(tiles, Tile{Group: str(sm, "name"), Name: str(im, "title", "name"), URL: str(im, "url"),
						Icon: str(im, "icon"), Description: str(im, "description")})
				}
			}
			return "dashy", clean(tiles), nil
		}
	case []any:
		// Homepage: [{Group: [{Service: {href, icon, description}}]}], groups may nest
		var walk func(group string, list []any)
		walk = func(group string, list []any) {
			for _, e := range list {
				em, _ := e.(map[string]any)
				for name, v := range em {
					switch body := v.(type) {
					case map[string]any:
						tiles = append(tiles, Tile{Group: group, Name: name, URL: str(body, "href"), Icon: str(body, "icon"),
							Description: str(body, "description")})
					case []any:
						walk(name, body)
					}
				}
			}
		}
		walk("", d)
		return "homepage", clean(tiles), nil
	}
	return "", nil, errors.New("not a Homepage, Homer or Dashy configuration")
}

// clean drops entries without a name or an http(s) URL and turns icon file names into
// application ids ("sonarr.png" -> "sonarr", "mdi-home" and "si-gitea" keep their names).
func clean(in []Tile) []Tile {
	out := make([]Tile, 0, len(in))
	for _, t := range in {
		if t.Name == "" || !validURL(t.URL) || t.URL == "" {
			continue
		}
		icon := strings.ToLower(path.Base(t.Icon))
		icon = strings.TrimSuffix(icon, path.Ext(icon))
		for _, p := range []string{"si-", "sh-", "hl-", "mdi-", "fas fa-", "fa-"} {
			icon = strings.TrimPrefix(icon, p)
		}
		if strings.Contains(t.Icon, "://") || strings.ContainsAny(icon, " /") {
			icon = ""
		}
		t.Icon = icon
		out = append(out, t)
	}
	return out
}

// privateURL reports whether a URL points into a local network (internal address of a tile).
func privateURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	return !strings.Contains(h, ".") || strings.HasSuffix(h, ".lan") || strings.HasSuffix(h, ".local") ||
		strings.HasSuffix(h, ".home.arpa") || strings.HasSuffix(h, ".internal")
}

// apiImportTiles creates dashboard tiles from another dashboard's configuration.
func (s *Server) apiImportTiles(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	format, tiles, err := ParseTiles(b)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	existing := map[string]bool{}
	svcs, err := s.store.Services(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, v := range svcs {
		existing[strings.ToLower(v.Name)] = true
	}
	res := struct {
		Format  string   `json:"format"`
		Created int      `json:"created"`
		Skipped []string `json:"skipped"`
	}{Format: format, Skipped: []string{}}
	for i, t := range tiles {
		if existing[strings.ToLower(t.Name)] {
			res.Skipped = append(res.Skipped, t.Name+" (already exists)")
			continue
		}
		v := store.Service{Name: t.Name, AppID: t.Icon, Icon: t.Icon, Group: t.Group, Notes: t.Description, Tile: true, Sort: i,
			Addresses: []store.Address{}}
		if privateURL(t.URL) {
			v.InternalURL = t.URL
		} else {
			v.ExternalURL = t.URL
		}
		if _, err := s.store.SaveService(r.Context(), v); err != nil {
			res.Skipped = append(res.Skipped, t.Name+" ("+err.Error()+")")
			continue
		}
		existing[strings.ToLower(t.Name)] = true
		res.Created++
	}
	s.audit(r, "import."+format, "", "ok", strconv.Itoa(res.Created))
	s.events.Publish("services", map[string]any{})
	writeJSON(w, http.StatusOK, res)
}
