package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const homepageYAML = `
- Media:
    - Jellyfin:
        href: http://192.168.1.20:8096
        icon: jellyfin.png
        description: Movies
    - Navidrome:
        href: https://music.example.com
        icon: si-navidrome
- Infra:
    - Proxmox:
        href: https://pve.lan:8006
        icon: https://cdn.example/proxmox.svg
    - Broken:
        href: ftp://nope
`

const homerYAML = `
title: Home
services:
  - name: Dev
    items:
      - name: Gitea
        logo: assets/tools/gitea.png
        url: https://git.example.com
        subtitle: Code
`

const dashyYAML = `
pageInfo: {title: Home}
sections:
  - name: Monitoring
    items:
      - title: Grafana
        url: http://grafana.home.arpa
        icon: hl-grafana
`

func TestParseTiles(t *testing.T) {
	f, tiles, err := ParseTiles([]byte(homepageYAML))
	if err != nil || f != "homepage" || len(tiles) != 3 {
		t.Fatalf("%s %v %+v", f, err, tiles)
	}
	by := map[string]Tile{}
	for _, x := range tiles {
		by[x.Name] = x
	}
	if by["Jellyfin"].Group != "Media" || by["Jellyfin"].Icon != "jellyfin" || by["Navidrome"].Icon != "navidrome" || by["Proxmox"].Icon != "" {
		t.Errorf("homepage: %+v", tiles)
	}
	if f, tiles, _ = ParseTiles([]byte(homerYAML)); f != "homer" || len(tiles) != 1 || tiles[0].Icon != "gitea" || tiles[0].Group != "Dev" {
		t.Errorf("homer: %s %+v", f, tiles)
	}
	if f, tiles, _ = ParseTiles([]byte(dashyYAML)); f != "dashy" || len(tiles) != 1 || tiles[0].Icon != "grafana" {
		t.Errorf("dashy: %s %+v", f, tiles)
	}
	if _, _, err := ParseTiles([]byte("a: 1\n")); err == nil {
		t.Error("unknown format accepted")
	}
}

func TestImportTiles(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	post := func(body string) map[string]any {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/import/dashboard", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/yaml")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	if r := post(homepageYAML); r["created"] != float64(3) || r["format"] != "homepage" {
		t.Fatalf("import: %v", r)
	}
	if r := post(homepageYAML); r["created"] != float64(0) {
		t.Errorf("duplicates: %v", r)
	}
	svcs, _ := s.store.Services(context.Background())
	for _, v := range svcs {
		switch v.Name {
		case "Jellyfin":
			if v.InternalURL != "http://192.168.1.20:8096" || !v.Tile || v.Group != "Media" {
				t.Errorf("jellyfin: %+v", v)
			}
		case "Navidrome":
			if v.ExternalURL != "https://music.example.com" {
				t.Errorf("navidrome: %+v", v)
			}
		}
	}
}
