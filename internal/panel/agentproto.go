package panel

// Agent wire protocol (v1) — the shared contract with the agent now
// lives in the quantum-protocol module
// (github.com/Dark-modem/quantum-protocol), which pins the exact wire
// values with golden fixtures and compatibility tests shared by the
// panel and the agent.
//
// This file keeps the historical unexported names as aliases so the rest
// of this package is unchanged. The aliasing is mechanical: type aliases
// are identical types, const aliases carry identical values.

import (
	"encoding/json"

	protocol "github.com/Dark-modem/quantum-protocol"
)

const agentProtoVersion = protocol.ProtoVersion

// HTTP headers used for agent authentication and enrollment.
const (
	agentIDHeader     = protocol.HeaderAgentID
	agentKeyHeader    = protocol.HeaderAgentKey
	agentEnrollHeader = protocol.HeaderEnrollToken
)

// Agent capability strings.
const (
	agentCapMatrix     = protocol.CapMatrix
	agentCapTraffic    = protocol.CapTraffic
	agentCapHealth     = protocol.CapHealth
	agentCapNodeDetect = protocol.CapNodeDetect
)

// Envelope types.
const (
	agentMsgHello          = protocol.MsgHello
	agentMsgWelcome        = protocol.MsgWelcome
	agentMsgHeartbeat      = protocol.MsgHeartbeat
	agentMsgDesired        = protocol.MsgDesired
	agentMsgAck            = protocol.MsgAck
	agentMsgCommand        = protocol.MsgCommand
	agentMsgResult         = protocol.MsgResult
	agentMsgRegister       = protocol.MsgRegister
	agentMsgRegisterResult = protocol.MsgRegisterResult
	agentMsgError          = protocol.MsgError
)

// Allowlisted remote commands.
const (
	agentCmdMatrixRestart  = protocol.CmdMatrixRestart
	agentCmdMatrixStart    = protocol.CmdMatrixStart
	agentCmdMatrixStop     = protocol.CmdMatrixStop
	agentCmdMatrixRegister = protocol.CmdMatrixRegister
	agentCmdDiagPing       = protocol.CmdDiagPing
	agentCmdAgentUpdate    = protocol.CmdAgentUpdate
	agentCmdFwDetect       = protocol.CmdFwDetect
	agentCmdFwSnapshot     = protocol.CmdFwSnapshot
	agentCmdFwPlan         = protocol.CmdFwPlan
	agentCmdFwApply        = protocol.CmdFwApply
	agentCmdFwTeardown     = protocol.CmdFwTeardown
	agentCmdPortCheck      = protocol.CmdPortCheck
	agentCmdPortList       = protocol.CmdPortList
	agentCmdNetDetect      = protocol.CmdNetDetect
	agentCmdNetPlan        = protocol.CmdNetPlan
	agentCmdNetApply       = protocol.CmdNetApply
	agentCmdNetStatus      = protocol.CmdNetStatus
	agentCmdNetRB          = protocol.CmdNetRB
	agentCmdNetRevert      = protocol.CmdNetRevert
)

// agentValidCommands is the complete remote-operation allowlist;
// agentMutatingOps require payload.confirm == "yes" on the agent.
var (
	agentValidCommands = protocol.ValidCommands
	agentMutatingOps   = protocol.MutatingOps
)

// Envelope and payload types (identical types, not copies).
type (
	agentEnvelope       = protocol.Envelope
	agentHello          = protocol.Hello
	agentWelcome        = protocol.Welcome
	agentHeartbeat      = protocol.Heartbeat
	agentStateReport    = protocol.StateReport
	agentResourceReport = protocol.ResourceReport
	agentListenPort     = protocol.ListenPort
	agentMatrixStatus   = protocol.MatrixStatus
	agentPeerPing       = protocol.PeerPing
	agentTunnelHealth   = protocol.TunnelHealth
	agentTrafficReport  = protocol.TrafficReport
	agentIfaceCounters  = protocol.IfaceCounters
	agentNodeDetect     = protocol.NodeDetect
	agentUpdateRequest  = protocol.UpdateRequest
	agentUpdateProgress = protocol.UpdateProgress
	agentDesired        = protocol.Desired
	agentDesiredMatrix  = protocol.DesiredMatrix
	agentDesiredHub     = protocol.DesiredHub
	agentAck            = protocol.Ack
	agentCommand        = protocol.Command
	agentCommandResult  = protocol.CommandResult
	agentRegisterMsg    = protocol.RegisterMsg
	agentRegisterResult = protocol.RegisterResult
	agentErrorMsg       = protocol.ErrorMsg
)

// agentProtoMismatchError keeps the original name of the protocol's
// version-mismatch error (see the grouped alias above).
type agentProtoMismatchError = protocol.ProtoMismatchError

func agentRevisionNewer(cand, cur string) bool { return protocol.RevisionNewer(cand, cur) }

func agentEncode(typ string, payload any) ([]byte, error) { return protocol.Encode(typ, payload) }

func agentDecode(raw []byte) (string, json.RawMessage, error) { return protocol.Decode(raw) }
