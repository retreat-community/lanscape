package catalog

import (
	"path"
	"strings"
)

// Rule triages new cards automatically, e.g. "every Ingress with TLS → HTTPS monitor + tile".
type Rule struct {
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"`     // "", ingress, k8s, container, socket
	App      string `json:"app,omitempty"`      // application id glob, "*" = any recognised
	Category string `json:"category,omitempty"` // application category
	TLSOnly  bool   `json:"tls_only,omitempty"`
	Action   string `json:"action"` // add, ignore
	Monitor  bool   `json:"monitor,omitempty"`
	Tile     bool   `json:"tile,omitempty"`
	Group    string `json:"group,omitempty"`
}

// Match reports whether the rule applies to a card.
func (r *Rule) Match(c *Card) bool {
	switch r.Kind {
	case "":
	case "ingress":
		if c.ExternalURL == "" {
			return false
		}
	case "k8s":
		if !strings.HasPrefix(c.Key, "k8s:") {
			return false
		}
	case "container":
		if c.Kind != "container" {
			return false
		}
	case "socket":
		if c.Kind != "socket" {
			return false
		}
	default:
		return false
	}
	if r.TLSOnly && !c.TLS {
		return false
	}
	if r.App != "" {
		if c.App == nil {
			return false
		}
		if ok, _ := path.Match(r.App, c.App.ID); !ok {
			return false
		}
	}
	if r.Category != "" && (c.App == nil || c.App.Category != r.Category) {
		return false
	}
	return true
}

// FirstMatch returns the first matching rule.
func FirstMatch(rules []Rule, c *Card) (Rule, bool) {
	for _, r := range rules {
		if r.Action != "add" && r.Action != "ignore" {
			continue
		}
		if r.Match(c) {
			return r, true
		}
	}
	return Rule{}, false
}
