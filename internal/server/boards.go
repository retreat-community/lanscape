package server

import (
	"context"
	"net/http"
	"strings"
)

// Board is one of several dashboards ("Main", "Media", "Infrastructure", §10): it shows the
// chosen tile groups (all when empty) to users with at least MinRole, with free notes and links.
type Board struct {
	ID      string   `json:"id" yaml:"id"`
	Name    string   `json:"name" yaml:"name"`
	Groups  []string `json:"groups,omitempty" yaml:"groups,omitempty"`
	MinRole string   `json:"min_role,omitempty" yaml:"min_role,omitempty"` // viewer (default), operator, admin
	Notes   string   `json:"notes,omitempty" yaml:"notes,omitempty"`       // text; lines with URLs become links
}

// boards returns the configured dashboards, or the single default one.
func (s *Server) boards(ctx context.Context) []Board {
	var bs []Board
	_ = s.store.GetSetting(ctx, "dashboards", &bs)
	if len(bs) == 0 {
		bs = []Board{{ID: "main", Name: "Main"}}
	}
	return bs
}

// visibleBoards filters the boards by role; guests only see the first board.
func (s *Server) visibleBoards(ctx context.Context, role string) []Board {
	all := s.boards(ctx)
	if role == "" {
		return all[:1]
	}
	out := []Board{}
	for _, b := range all {
		if roleRank[role] >= roleRank[orDefault(b.MinRole, RoleViewer)] {
			out = append(out, b)
		}
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (s *Server) apiBoards(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	writeJSON(w, http.StatusOK, s.visibleBoards(r.Context(), p.Role))
}

// validateBoards checks ids, names and roles of a board list.
func validateBoards(bs []Board) error {
	seen := map[string]bool{}
	for _, b := range bs {
		if !slugRe.MatchString(b.ID) || strings.TrimSpace(b.Name) == "" || seen[b.ID] {
			return badInput("dashboard %q: id (a-z, 0-9, -) must be unique and the name set", b.ID)
		}
		if b.MinRole != "" && !ValidRole(b.MinRole) {
			return badInput("dashboard %q: unknown role %q", b.ID, b.MinRole)
		}
		seen[b.ID] = true
	}
	return nil
}

func (s *Server) apiSaveBoards(w http.ResponseWriter, r *http.Request) {
	var bs []Board
	if !readJSON(w, r, &bs) {
		return
	}
	if len(bs) == 0 {
		bs = []Board{{ID: "main", Name: "Main"}}
	}
	if err := validateBoards(bs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetSetting(r.Context(), "dashboards", bs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "dashboards.update", "", "ok", "")
	writeJSON(w, http.StatusOK, bs)
}

// applyBoard reduces the dashboard to the groups of the chosen board and adds its notes.
func applyBoard(d *Dashboard, b Board) {
	d.Board = b
	if len(b.Groups) == 0 {
		return
	}
	want := map[string]bool{}
	for _, g := range b.Groups {
		want[strings.ToLower(g)] = true
	}
	groups := []TileGroup{}
	for _, g := range d.Groups {
		if want[strings.ToLower(g.Name)] {
			groups = append(groups, g)
		}
	}
	d.Groups = groups
}
