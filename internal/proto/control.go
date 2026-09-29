package proto

import "encoding/json"

// ControlVersion is the Full control-plane protocol version negotiated in hello.
const ControlVersion = 1

// Full control-plane message types (JSON over WebSocket, see docs/PROTOCOL.md §4).
const (
	MsgHello      = "hello"
	MsgWelcome    = "welcome"
	MsgInventory  = "inventory"
	MsgGrant      = "grant"
	MsgGrantAck   = "grant_ack"
	MsgTest       = "test"
	MsgTestResult = "test_result"
	MsgDiscovery  = "discovery"
	MsgScan       = "scan"
	// agent → server requests (LuCI "check from this router")
	MsgRunRequest   = "run_request"
	MsgRunSummary   = "run_summary"
	MsgCheck        = "check"
	MsgCheckResult  = "check_result"
	MsgAction       = "action"
	MsgActionResult = "action_result"
	MsgPing         = "ping"
	MsgPong         = "pong"
	MsgConfig       = "config"
	MsgError        = "error"
)

// Envelope wraps every control-plane message.
type Envelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NewEnvelope marshals data into an envelope.
func NewEnvelope(typ, id string, data any) (Envelope, error) {
	e := Envelope{Type: typ, ID: id}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return e, err
		}
		e.Data = b
	}
	return e, nil
}

// HelloMsg is sent by the agent after connecting.
type HelloMsg struct {
	Proto    int      `json:"proto"`
	AgentID  string   `json:"agent_id"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Hostname string   `json:"hostname"`
	HostID   string   `json:"host_id"`
	Caps     []string `json:"caps"`
	DataPort int      `json:"data_port"`
}

// WelcomeMsg answers hello.
type WelcomeMsg struct {
	Proto  int             `json:"proto"`
	Config json.RawMessage `json:"config,omitempty"`
}

// GrantMsg allows a data-plane run on the responder.
type GrantMsg struct {
	RunID uint32 `json:"run_id"`
	Token []byte `json:"token"`
	TTLS  int    `json:"ttl_s"`
}

// TestMsg asks the agent to run a test as the initiator.
type TestMsg struct {
	Kind        string `json:"kind"` // ping, pmtu, echo, tcp, udp
	Dev         string `json:"dev"`
	Src         string `json:"src"`
	Dst         string `json:"dst"`
	Port        int    `json:"port,omitempty"`
	Count       int    `json:"count,omitempty"`
	IntervalMS  int    `json:"interval_ms,omitempty"`
	Size        int    `json:"size,omitempty"`
	RunID       uint32 `json:"run_id,omitempty"`
	Token       []byte `json:"token,omitempty"`
	Dir         uint8  `json:"dir,omitempty"`
	Streams     int    `json:"streams,omitempty"`
	DurationMS  int    `json:"duration_ms,omitempty"`
	UDPRateKbps int    `json:"udp_rate_kbps,omitempty"`
	PktSize     int    `json:"pkt_size,omitempty"`
}

// RunRequestMsg asks the server to test the paths of the requesting agent.
type RunRequestMsg struct {
	Kind string `json:"kind"` // full or reachability
}

// RunSummaryMsg is the part of a run report that concerns one agent.
type RunSummaryMsg struct {
	RunID    int64         `json:"run_id"`
	Status   string        `json:"status"`
	Finished int64         `json:"finished"`
	Paths    []PathSummary `json:"paths"`
	Problems []string      `json:"problems"`
}

// PathSummary is one path as seen from the agent.
type PathSummary struct {
	Peer    string  `json:"peer"`
	Segment string  `json:"segment"`
	Mbps    float64 `json:"mbps"`
	Verdict string  `json:"verdict"`
	RTTMS   float64 `json:"rtt_ms"`
}

// Actions an agent may perform when the administrator allows them on the agent (--actions).
const (
	ActionWake    = "wol"
	ActionRestart = "restart"
)

// ActionMsg asks the agent to perform an action.
type ActionMsg struct {
	Action string `json:"action"`
	MAC    string `json:"mac,omitempty"`    // wol
	IP     string `json:"ip,omitempty"`     // wol: pick the interfaces on this subnet
	Source string `json:"source,omitempty"` // restart: docker, k8s, proxmox
	Key    string `json:"key,omitempty"`    // restart: finding key
}

// ActionResultMsg describes what the agent did.
type ActionResultMsg struct {
	Detail string `json:"detail"`
}
