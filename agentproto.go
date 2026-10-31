package main

// Agent wire protocol (v1) — the contract between the headless endpoint
// agent ("gre-panel agent run") and the central controller (the Quantum
// panel running the agent hub). Both sides speak JSON envelopes over one
// outbound WebSocket the agent dials to the controller; the controller
// never dials the agent, so an endpoint behind NAT/firewall works and no
// inbound management port is opened on endpoints.
//
// Message flow:
//	agent   -> hello                      (right after the WS is up)
//	ctrl    -> welcome                    (ok:true or ok:false + close)
//	ctrl    -> desired                    (whenever a desired state is pending)
//	agent   -> ack                        (one per desired, applied or failed)
//	agent   -> heartbeat                  (every ~30s, carries latest state)
//	ctrl    -> command                    (allowlisted ops only)
//	agent   -> result                     (one per command)
//	agent   -> register                   (node announces itself to the hub)
//	ctrl    -> register_result            (after relaying to the hub agent)
//
// Every desired state carries a monotonically increasing revision; the
// agent rejects stale (older than applied) and duplicate (equal to
// applied) revisions, and applies the rest idempotently. The tunnel runs
// under its own systemd unit, so losing the controller connection never
// stops user traffic.
//
// This file is intentionally dependency-free (stdlib only): the agent
// side, the controller side and their tests all share these exact types.

import (
	"encoding/json"
	"strconv"
	"strings"
)

// agentProtoVersion is bumped on incompatible protocol changes; both
// sides include it in hello/welcome and refuse mismatches.
const agentProtoVersion = 1

// HTTP headers used for agent authentication and enrollment.
const (
	agentIDHeader     = "X-Agent-Id"
	agentKeyHeader    = "X-Agent-Key"
	agentEnrollHeader = "X-Enroll-Token"
)

// Agent capability strings. v1 ships the Matrix engine only; future
// engines add their own caps without breaking old agents.
const (
	agentCapMatrix     = "matrix"
	agentCapTraffic    = "traffic"
	agentCapHealth     = "health"
	agentCapNodeDetect = "node-detect" // read-only PasarGuard Node detection
)

// Envelope types.
const (
	agentMsgHello          = "hello"
	agentMsgWelcome        = "welcome"
	agentMsgHeartbeat      = "heartbeat"
	agentMsgDesired        = "desired"
	agentMsgAck            = "ack"
	agentMsgCommand        = "command"
	agentMsgResult         = "result"
	agentMsgRegister       = "register"
	agentMsgRegisterResult = "register_result"
	agentMsgError          = "error"
)

// Allowlisted remote commands. Anything else the controller sends is
// rejected with an error result — the agent never runs shell commands.
const (
	agentCmdMatrixRestart  = "matrix.restart"
	agentCmdMatrixStart    = "matrix.start"
	agentCmdMatrixStop     = "matrix.stop"
	agentCmdMatrixRegister = "matrix.register" // hub side: pair a node host file
	agentCmdDiagPing       = "diag.ping"
	agentCmdAgentUpdate    = "agent.update" // self-update from a signed artifact

	// Typed firewall/port operations (Session 1 handlers) and network
	// optimization operations (Session 2 handlers). Params carry one JSON
	// "payload" field per op; results return as JSON in Output. Mutating
	// ops additionally require payload.confirm == "yes".
	agentCmdFwDetect   = "firewall.detect"
	agentCmdFwSnapshot = "firewall.snapshot"
	agentCmdFwPlan     = "firewall.plan"
	agentCmdFwApply    = "firewall.apply"
	agentCmdFwTeardown = "firewall.teardown"
	agentCmdPortCheck  = "port.check"
	agentCmdPortList   = "port.list"
	agentCmdNetDetect  = "netopt.detect"
	agentCmdNetPlan    = "netopt.plan"
	agentCmdNetApply   = "netopt.apply"
	agentCmdNetStatus  = "netopt.status"
	agentCmdNetRB      = "netopt.rollback"
	agentCmdNetRevert  = "netopt.revert"
)

// agentValidCommands is the complete remote-operation allowlist.
var agentValidCommands = map[string]bool{
	agentCmdMatrixRestart:  true,
	agentCmdMatrixStart:    true,
	agentCmdMatrixStop:     true,
	agentCmdMatrixRegister: true,
	agentCmdDiagPing:       true,
	agentCmdAgentUpdate:    true,
	agentCmdFwDetect:       true,
	agentCmdFwSnapshot:     true,
	agentCmdFwPlan:         true,
	agentCmdFwApply:        true,
	agentCmdFwTeardown:     true,
	agentCmdPortCheck:      true,
	agentCmdPortList:       true,
	agentCmdNetDetect:      true,
	agentCmdNetPlan:        true,
	agentCmdNetApply:       true,
	agentCmdNetStatus:      true,
	agentCmdNetRB:          true,
	agentCmdNetRevert:      true,
}

// agentMutatingOps require payload.confirm == "yes" on the agent and are
// audit-logged on both sides.
var agentMutatingOps = map[string]bool{
	agentCmdFwApply:    true,
	agentCmdFwTeardown: true,
	agentCmdNetApply:   true,
	agentCmdNetRB:      true,
	agentCmdNetRevert:  true,
}

// agentEnvelope wraps every message in both directions.
type agentEnvelope struct {
	V    int             `json:"v"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// agentHello is the first message the agent sends after connecting.
type agentHello struct {
	ID       string   `json:"id"` // agent id from enrollment ("agt_…")
	Version  string   `json:"version"`
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Caps     []string `json:"caps,omitempty"`
}

// agentWelcome is the controller's answer to hello.
type agentWelcome struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Server string `json:"server,omitempty"` // controller version
}

// agentHeartbeat is sent periodically and on demand; it carries the
// latest full state report so a controller that just (re)connected gets
// everything in one message.
type agentHeartbeat struct {
	TS      int64             `json:"ts"`
	UptimeS int64             `json:"uptime_s"`
	State   *agentStateReport `json:"state,omitempty"`
}

// agentStateReport is the agent's complete picture of its host: what is
// applied, what health it measured, and what traffic it counted. It is
// also what the controller persists per agent for the dashboards.
type agentStateReport struct {
	AppliedRevision string               `json:"applied_revision,omitempty"`
	Matrix          []agentMatrixStatus  `json:"matrix,omitempty"`
	Health          []agentTunnelHealth  `json:"health,omitempty"`
	Traffic         *agentTrafficReport  `json:"traffic,omitempty"`
	Resources       *agentResourceReport `json:"resources,omitempty"`
	UpdateProgress  *agentUpdateProgress `json:"update_progress,omitempty"`
	NodeDetect      *agentNodeDetect     `json:"node_detect,omitempty"`
	Errors          []string             `json:"errors,omitempty"`
}

// agentResourceReport is the lightweight host snapshot for the controller
// dashboards: CPU/RAM/disk/virt plus the listening-port inventory with
// ownership hints. Measured values only — nothing is estimated here.
type agentResourceReport struct {
	CPUModel    string            `json:"cpu_model,omitempty"`
	CPUCores    int               `json:"cpu_cores"`
	CPUPercent  float64           `json:"cpu_percent"` // sampled over ~200ms
	LoadAvg1    float64           `json:"load_avg_1"`
	MemTotalMB  uint64            `json:"mem_total_mb"`
	MemUsedMB   uint64            `json:"mem_used_mb"` // total - available
	DiskTotalGB uint64            `json:"disk_total_gb"`
	DiskUsedGB  uint64            `json:"disk_used_gb"` // root filesystem
	UptimeS     uint64            `json:"uptime_s"`
	Virt        string            `json:"virt,omitempty"` // systemd-detect-virt
	Listening   []agentListenPort `json:"listening,omitempty"`
}

// agentListenPort is one listening socket with an ownership hint.
type agentListenPort struct {
	Proto string `json:"proto"` // "tcp" | "udp"
	Port  int    `json:"port"`
	Addr  string `json:"addr"`           // bind address as reported
	Proc  string `json:"proc,omitempty"` // process name(s), comma-joined
}

// agentMatrixStatus is one local Matrix instance as seen by the agent.
// Secrets (tinc token, keys) never cross this interface.
type agentMatrixStatus struct {
	Instance   string          `json:"instance"` // "matrix", "matrix2", …
	Role       string          `json:"role"`     // "hub" (Iran) | "node" (foreign)
	Network    string          `json:"network,omitempty"`
	Enabled    bool            `json:"enabled"`
	Service    bool            `json:"service"`            // systemd unit active
	Iface      string          `json:"iface"`              // qmx0, qmx2, …
	IfaceUp    bool            `json:"iface_up"`           // interface exists & up
	Port       int             `json:"port"`               // mesh port
	MeshIP     string          `json:"mesh_ip"`            // own /30 address
	Peers      []agentPeerPing `json:"peers"`              // mesh reachability right now
	Registered bool            `json:"registered"`         // hub: a node is paired; node: hub accepted us
	Revision   string          `json:"revision,omitempty"` // last applied desired revision
}

// agentPeerPing is one mesh peer's instantaneous reachability.
type agentPeerPing struct {
	Name      string  `json:"name"`
	IP        string  `json:"ip"`
	OK        bool    `json:"ok"`
	LatencyMs float64 `json:"latency_ms"`
}

// agentTunnelHealth is the rolling 60-second quality window for one
// Matrix instance: low-bandwidth ICMP probes only, never a bandwidth
// test. Quality buckets: "good" | "fair" | "poor" | "down".
type agentTunnelHealth struct {
	Instance  string  `json:"instance"`
	WindowS   int     `json:"window_s"`
	Samples   int     `json:"samples"`
	LatencyMs float64 `json:"latency_ms"` // window average
	JitterMs  float64 `json:"jitter_ms"`  // mean absolute delta between probes
	LossPct   float64 `json:"loss_pct"`   // 0-100
	Reachable bool    `json:"reachable"`
	Service   bool    `json:"service"`
	IfaceUp   bool    `json:"iface_up"`
	Quality   string  `json:"quality"`
}

// agentTrafficReport carries per-interface cumulative counters. The
// agent reports raw counters; the controller derives deltas so a missed
// heartbeat never loses bytes (counters are monotonic between reports).
type agentTrafficReport struct {
	TS     int64                `json:"ts"`
	Ifaces []agentIfaceCounters `json:"ifaces"`
}

// agentIfaceCounters is one interface's cumulative RX/TX bytes. Kind is
// "physical" (billable NIC accounting) or "tunnel" (Matrix overlay,
// e.g. qmx0). Known=false means the counter could not be read — the
// controller must then show UNKNOWN instead of inventing a number.
type agentIfaceCounters struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // "physical" | "tunnel"
	Rx    uint64 `json:"rx"`
	Tx    uint64 `json:"tx"`
	Known bool   `json:"known"`
}

// agentNodeDetect is the read-only PasarGuard Node signal. The agent
// never touches the containers, Docker config, API keys or units.
type agentNodeDetect struct {
	Detected bool   `json:"detected"`
	Kind     string `json:"kind,omitempty"` // e.g. "docker-container"
	Detail   string `json:"detail,omitempty"`
}

// agentUpdateRequest is the payload of the agent.update command. Every
// field is mandatory: the agent refuses partial requests. The artifact must
// be HTTPS, carry the exact sha256 and an ed25519 signature made with the
// update public key pinned in the binary; without a pinned key the agent
// refuses all updates (fail closed).
type agentUpdateRequest struct {
	Version   string `json:"version"`   // target version marker, e.g. v1.28.1
	URL       string `json:"url"`       // https:// artifact URL (agent-authenticated)
	SHA256Hex string `json:"sha256"`    // sha256 of the artifact bytes
	Signature string `json:"signature"` // base64 std ed25519 signature over the artifact bytes
}

// agentUpdateProgress rides the heartbeat state so the controller UI can
// show live update phases and the final verdict.
type agentUpdateProgress struct {
	Phase   string `json:"phase"` // requested|downloading|verifying|swapped|restarting|completed|failed
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
	At      int64  `json:"at"`
}

// agentDesired is the full desired state the controller hands one agent.
// Revision must be strictly newer than what the agent has applied.
type agentDesired struct {
	Revision string              `json:"revision"`
	Matrix   *agentDesiredMatrix `json:"matrix,omitempty"`
}

// agentDesiredMatrix describes the Matrix work an agent should do.
// Action "apply" provisions/updates the instance; "remove" tears it down.
// Hub payloads are structured; node payloads reuse the existing
// hmx1_ pairing bundle verbatim so the proven node path is reused.
type agentDesiredMatrix struct {
	Instance string           `json:"instance"` // "matrix", "matrix2", …
	Action   string           `json:"action"`   // "apply" | "remove"
	Hub      *agentDesiredHub `json:"hub,omitempty"`
	Bundle   string           `json:"bundle,omitempty"` // "hmx1_…" (node side)
}

// agentDesiredHub is the hub-side Matrix configuration.
type agentDesiredHub struct {
	Family string `json:"family"` // "ipv4" | "ipv6"
	Port   int    `json:"port"`   // mesh port; 0 = agent auto-picks
	Addr   string `json:"addr"`   // hub public address
	Ports  []int  `json:"ports"`  // forwarded user ports
	Token  string `json:"token"`  // pairing token (controller-generated)
}

// agentAck answers a desired revision. Detail may carry extra artifacts
// the controller needs to drive the other side (e.g. the hub's tinc
// host file under key "main_host").
type agentAck struct {
	Revision string            `json:"revision"`
	OK       bool              `json:"ok"`
	Error    string            `json:"error,omitempty"`
	Detail   map[string]string `json:"detail,omitempty"`
}

// agentCommand is one allowlisted remote operation.
type agentCommand struct {
	ID     string            `json:"id"`
	Op     string            `json:"op"`
	Params map[string]string `json:"params,omitempty"`
}

// agentCommandResult answers a command.
type agentCommandResult struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"` // short diagnostic text only
}

// agentRegisterMsg is sent by a node agent after it applied its bundle:
// the hub needs the node's tinc host file to finish pairing. The
// controller relays it to the hub agent as a matrix register command.
type agentRegisterMsg struct {
	Instance string `json:"instance"` // instance on the node
	TunnelID string `json:"tunnel_id,omitempty"`
	Token    string `json:"token"`
	Name     string `json:"name"`
	HostFile string `json:"host_file"` // node hosts/<name> content (PEM included)
	Revision string `json:"revision,omitempty"`
}

// agentRegisterResult is the controller's answer to register.
type agentRegisterResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// agentErrorMsg is a soft failure notice (agent -> ctrl) that does not
// close the session.
type agentErrorMsg struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// agentRevisionNewer reports whether cand is strictly newer than cur.
// Revisions are decimal strings (controller-assigned unix nanos); an
// empty cur accepts anything non-empty, an empty cand is never newer.
func agentRevisionNewer(cand, cur string) bool {
	cand = strings.TrimSpace(cand)
	if cand == "" {
		return false
	}
	cur = strings.TrimSpace(cur)
	if cur == "" {
		return true
	}
	c, err1 := strconv.ParseUint(cand, 10, 64)
	u, err2 := strconv.ParseUint(cur, 10, 64)
	if err1 != nil || err2 != nil {
		// Unparseable — fall back to inequality so a corrupted stored
		// revision can never wedge the agent into rejecting updates.
		return cand != cur
	}
	return c > u
}

// agentEncode wraps a payload into an envelope.
func agentEncode(typ string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(agentEnvelope{V: agentProtoVersion, Type: typ, Data: data})
}

// agentDecode unwraps an envelope, checking the protocol version.
func agentDecode(raw []byte) (string, json.RawMessage, error) {
	var env agentEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", nil, err
	}
	if env.V != agentProtoVersion {
		return env.Type, nil, &agentProtoMismatchError{Got: env.V}
	}
	return env.Type, env.Data, nil
}

// agentProtoMismatchError signals a version mismatch on the wire.
type agentProtoMismatchError struct{ Got int }

func (e *agentProtoMismatchError) Error() string {
	return "agent protocol version mismatch: got " + strconv.Itoa(e.Got) +
		", want " + strconv.Itoa(agentProtoVersion)
}
