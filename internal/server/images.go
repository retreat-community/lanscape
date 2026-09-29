package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/registry"
	"github.com/retreat-community/lanscape/internal/store"
)

// ChangeImageUpdate is written to the change feed when a newer image tag appears.
const ChangeImageUpdate = "image_update"

// ImageUpdate is a running image with a newer tag in its registry (§10 "updates").
type ImageUpdate struct {
	Image   string   `json:"image"`  // as it runs, e.g. grafana/grafana:10.2.0
	Latest  string   `json:"latest"` // newest tag of the same shape, e.g. 10.4.1
	Users   []string `json:"users"`  // "agent/container" that run it
	Checked int64    `json:"checked"`
}

// ImageCheck is the state of the last check.
type ImageCheck struct {
	Checked int64         `json:"checked"`
	Images  int           `json:"images"`
	Errors  []string      `json:"errors"`
	Updates []ImageUpdate `json:"updates"`
}

type imageState struct {
	mu      sync.Mutex
	running bool
	last    time.Time
	client  *registry.Client // tests swap it
}

// runningImages maps the images of current containers and workloads to their users.
func (s *Server) runningImages(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	fs, err := s.store.Findings(ctx, "", "")
	if err != nil {
		return out
	}
	for _, f := range fs {
		if f.Gone != 0 || (f.Kind != discovery.KindContainer && !strings.HasPrefix(f.Kind, "k8s_")) {
			continue
		}
		var it discovery.Item
		if json.Unmarshal(f.Data, &it) != nil || it.Image == "" {
			continue
		}
		user := s.agentName(f.AgentID) + "/" + it.Name
		out[it.Image] = append(out[it.Image], user)
	}
	return out
}

// checkImages looks up newer tags of every running image and stores the result.
func (s *Server) checkImages(ctx context.Context) ImageCheck {
	s.images.mu.Lock()
	if s.images.running {
		s.images.mu.Unlock()
		return ImageCheck{}
	}
	s.images.running, s.images.last = true, time.Now()
	client := s.images.client
	s.images.mu.Unlock()
	defer func() {
		s.images.mu.Lock()
		s.images.running = false
		s.images.mu.Unlock()
	}()
	if client == nil {
		client = &registry.Client{HTTP: &http.Client{Timeout: 20 * time.Second}}
	}
	prev := map[string]string{}
	for _, u := range s.imageCheck(ctx).Updates {
		prev[u.Image] = u.Latest
	}
	now := time.Now().UnixMilli()
	res := ImageCheck{Checked: now, Errors: []string{}, Updates: []ImageUpdate{}}
	used := s.runningImages(ctx)
	tags := map[string][]string{} // per repository, each listed once
	for img, users := range used {
		ref, err := registry.Parse(img)
		if err != nil {
			continue
		}
		res.Images++
		repo := ref.Registry + "/" + ref.Repo
		list, seen := tags[repo]
		if !seen {
			if list, err = client.Tags(ctx, ref); err != nil {
				res.Errors = append(res.Errors, img+": "+err.Error())
			}
			tags[repo] = list
		}
		latest := registry.Newer(ref.Tag, list)
		if latest == "" {
			continue
		}
		sort.Strings(users)
		res.Updates = append(res.Updates, ImageUpdate{Image: img, Latest: latest, Users: users, Checked: now})
		if prev[img] != latest {
			s.addChange(ctx, store.Change{Kind: ChangeImageUpdate, Subject: img, Detail: latest})
		}
	}
	sort.Slice(res.Updates, func(i, j int) bool { return res.Updates[i].Image < res.Updates[j].Image })
	sort.Strings(res.Errors)
	if err := s.store.SetSetting(ctx, "image_updates", res); err != nil {
		s.log.Warn("cannot store image updates", "err", err)
	}
	return res
}

func (s *Server) imageCheck(ctx context.Context) ImageCheck {
	res := ImageCheck{Errors: []string{}, Updates: []ImageUpdate{}}
	_ = s.store.GetSetting(ctx, "image_updates", &res)
	return res
}

// imageScheduler checks the registries every ImageUpdatesEveryH hours (off by default: the
// check contacts registries on the Internet).
func (s *Server) imageScheduler(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h := s.settings(ctx).ImageUpdatesEveryH
		s.images.mu.Lock()
		due := h > 0 && time.Since(s.images.last) >= time.Duration(h)*time.Hour
		s.images.mu.Unlock()
		if due {
			s.checkImages(ctx)
		}
	}
}

func (s *Server) apiImageUpdates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.imageCheck(r.Context()))
}

func (s *Server) apiCheckImages(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "images.check", "", "ok", "")
	go s.checkImages(s.ctx)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
