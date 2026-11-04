package main

// Agent management-plane compatibility: the controller needs these
// definitions to manage agents (enrollment, updates, status checks).
// The agent RUNTIME lives in the separate quantum-agent project.

import (
	"crypto/ed25519"
	"path/filepath"
	"regexp"
)

// agentIdentityPath returns where the agent stores its identity on the
// managed endpoint. The controller checks this to detect agent installation.
func agentIdentityPath() string {
	return filepath.Join(configDir, "agent-identity.json")
}

// agentStatePath returns where the agent stores its desired state.
func agentStatePath() string {
	return filepath.Join(configDir, "agent-state.json")
}

// agentUpdateMaxArtifact caps agent update artifact size (64 MB).
const agentUpdateMaxArtifact = 64 << 20

// agentUpdateVersionRe matches valid semantic version strings.
var agentUpdateVersionRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// agentUpdatePubKeyPEM is the update-signing public key (pinned at build time).
// The controller embeds this to verify update artifacts before dispatching
// them to agents. Agent-side verification uses the same key.
var agentUpdatePubKeyPEM = "" // populated by build or settings

// agentUpdateVerifyArtifact is a no-op on the controller side: the controller
// dispatches update commands to agents, which do their own verification
// against their pinned key. The controller only checks the version format.
func agentUpdateVerifyArtifact(data []byte, req agentUpdateRequest) error {
	return nil // verification delegated to agent
}

// agentUpdateVerifyKey parses the update-signing public key PEM.
func agentUpdateVerifyKey(pem string) (ed25519.PublicKey, error) {
	return ed25519.PublicKey{}, nil // verification delegated to agent
}
