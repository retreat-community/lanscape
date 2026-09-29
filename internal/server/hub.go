package server

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/testengine"
	"github.com/retreat-community/lanscape/internal/topo"
)

// ErrOffline is returned when an agent is not connected.
var ErrOffline = errors.New("server: agent offline")

// TestSpec is a test request independent of the agent kind.
type TestSpec = proto.TestMsg

// Conn is a connected agent, either Full (WebSocket) or lite (Mini control protocol).
type Conn interface {
	ID() string
	Lite() bool
	Grant(ctx context.Context, runID uint32, token testengine.Token) error
	Test(ctx context.Context, spec TestSpec) (agent.TestResult, error)
	Request(ctx context.Context, typ string, data any) (json.RawMessage, error)
	Close()
}

// AgentState is the live view of an agent.
type AgentState struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Online   bool            `json:"online"`
	Version  string          `json:"version"`
	OS       string          `json:"os"`
	Arch     string          `json:"arch"`
	Hostname string          `json:"hostname"`
	HostID   string          `json:"host_id"`
	DataPort int             `json:"data_port"`
	LastSeen int64           `json:"last_seen"`
	Caps     []string        `json:"caps,omitempty"`
	Addr     string          `json:"addr,omitempty"` // address the agent connects from
	Inv      agent.Inventory `json:"inventory"`
	conn     Conn
}

// Hub tracks connected agents and their inventories.
type Hub struct {
	mu     sync.RWMutex
	agents map[string]*AgentState
	onInv  func(*AgentState)
	onConn func(*AgentState, bool)
}

// NewHub creates an empty hub.
func NewHub() *Hub { return &Hub{agents: map[string]*AgentState{}} }

// Load seeds the hub with persisted agents (offline until they connect).
func (h *Hub) Load(states []AgentState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range states {
		s := states[i]
		h.agents[s.ID] = &s
	}
}

// Connected registers a live connection, replacing an older one of the same agent.
func (h *Hub) Connected(st AgentState, c Conn) {
	h.mu.Lock()
	old := h.agents[st.ID]
	if old != nil && old.conn != nil && old.conn != c {
		old.conn.Close()
	}
	if old != nil && len(st.Inv.Ifaces) == 0 {
		st.Inv = old.Inv
	}
	st.conn, st.Online, st.LastSeen = c, true, time.Now().UnixMilli()
	h.agents[st.ID] = &st
	cb := h.onConn
	h.mu.Unlock()
	if cb != nil {
		cb(&st, true)
	}
}

// Disconnected marks an agent offline if c is still its connection.
func (h *Hub) Disconnected(id string, c Conn) {
	h.mu.Lock()
	st := h.agents[id]
	if st == nil || st.conn != c {
		h.mu.Unlock()
		return
	}
	st.conn, st.Online, st.LastSeen = nil, false, time.Now().UnixMilli()
	cp := *st
	cb := h.onConn
	h.mu.Unlock()
	if cb != nil {
		cb(&cp, false)
	}
}

// SetInventory stores a new inventory.
func (h *Hub) SetInventory(id string, inv agent.Inventory) {
	h.mu.Lock()
	st := h.agents[id]
	if st == nil {
		h.mu.Unlock()
		return
	}
	st.Inv, st.LastSeen = inv, time.Now().UnixMilli()
	cp := *st
	cb := h.onInv
	h.mu.Unlock()
	if cb != nil {
		cb(&cp)
	}
}

// Touch updates last-seen.
func (h *Hub) Touch(id string) {
	h.mu.Lock()
	if st := h.agents[id]; st != nil {
		st.LastSeen = time.Now().UnixMilli()
	}
	h.mu.Unlock()
}

// Remove deletes an agent.
func (h *Hub) Remove(id string) {
	h.mu.Lock()
	st := h.agents[id]
	delete(h.agents, id)
	h.mu.Unlock()
	if st != nil && st.conn != nil {
		st.conn.Close()
	}
}

// Rename updates the display name.
func (h *Hub) Rename(id, name string) {
	h.mu.Lock()
	if st := h.agents[id]; st != nil {
		st.Name = name
	}
	h.mu.Unlock()
}

// Conn returns the live connection of an agent.
func (h *Hub) Conn(id string) (Conn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	st := h.agents[id]
	if st == nil || st.conn == nil {
		return nil, false
	}
	return st.conn, true
}

// Get returns a copy of one agent.
func (h *Hub) Get(id string) (AgentState, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	st := h.agents[id]
	if st == nil {
		return AgentState{}, false
	}
	return *st, true
}

// List returns copies of all agents sorted by name.
func (h *Hub) List() []AgentState {
	h.mu.RLock()
	out := make([]AgentState, 0, len(h.agents))
	for _, st := range h.agents {
		out = append(out, *st)
	}
	h.mu.RUnlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Nodes converts agents into topology nodes (online only unless all is set).
func (h *Hub) Nodes(all bool) []topo.Node {
	var out []topo.Node
	for _, st := range h.List() {
		if !st.Online && !all {
			continue
		}
		out = append(out, topo.Node{ID: st.ID, Name: st.Name, Hostname: st.Hostname, HostID: st.HostID,
			Lite: st.Kind == "lite", Online: st.Online, Env: st.Inv.Env.Kind, Ifaces: st.Inv.Ifaces})
	}
	return out
}
