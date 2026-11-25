package panel

// frp_compat.go — BACKEND-48: the FRP driver has always consumed
// server_addr/dashboard_port. Rows written before BACKEND-47 still carry
// the old remote_addr/dash_port spellings, and compileDesiredV2 used to
// forward them verbatim, so the driver saw empty values. This is a
// boundary-only translation: the stored tunnel row is never rewritten.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// normalizeLegacyFRPParams translates the two pre-driver FRP spellings at
// compile time. A non-empty new key wins only when the old alias is
// empty; two non-empty, different values are a hard error (never guess).
func normalizeLegacyFRPParams(tunnelID string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		// Non-object / invalid JSON keeps the historical verbatim path;
		// the agent's typed Validate remains the fail-safe.
		return raw, nil
	}

	hostRaw, hasHost := m["remote_addr"]
	newHostRaw, hasNewHost := m["server_addr"]
	if hasHost {
		oldHost, err := frpCompatString(hostRaw)
		if err != nil {
			return nil, fmt.Errorf("frp tunnel %q: remote_addr: %w", tunnelID, err)
		}
		newHost := ""
		if hasNewHost {
			newHost, err = frpCompatString(newHostRaw)
			if err != nil {
				return nil, fmt.Errorf("frp tunnel %q: server_addr: %w", tunnelID, err)
			}
		}
		if newHost != "" && oldHost != "" && newHost != oldHost {
			return nil, fmt.Errorf("frp tunnel %q: remote_addr %q conflicts with server_addr %q; refusing to guess",
				tunnelID, oldHost, newHost)
		}
		if newHost == "" {
			newHost = oldHost
		}
		encoded, _ := json.Marshal(newHost)
		m["server_addr"] = encoded
		delete(m, "remote_addr")
	}

	portRaw, hasPort := m["dash_port"]
	newPortRaw, hasNewPort := m["dashboard_port"]
	if hasPort {
		oldPort, err := frpCompatInt(portRaw)
		if err != nil {
			return nil, fmt.Errorf("frp tunnel %q: dash_port: %w", tunnelID, err)
		}
		newPort := 0
		if hasNewPort {
			newPort, err = frpCompatInt(newPortRaw)
			if err != nil {
				return nil, fmt.Errorf("frp tunnel %q: dashboard_port: %w", tunnelID, err)
			}
		}
		if newPort != 0 && oldPort != 0 && newPort != oldPort {
			return nil, fmt.Errorf("frp tunnel %q: dash_port %d conflicts with dashboard_port %d; refusing to guess",
				tunnelID, oldPort, newPort)
		}
		if newPort == 0 {
			newPort = oldPort
		}
		encoded, _ := json.Marshal(newPort)
		m["dashboard_port"] = encoded
		delete(m, "dash_port")
	}

	if !hasHost && !hasPort {
		return raw, nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("frp tunnel %q: re-encode params: %w", tunnelID, err)
	}
	return out, nil
}

func frpCompatString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("must be a string")
	}
	return strings.TrimSpace(s), nil
}

func frpCompatInt(raw json.RawMessage) (int, error) {
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("must be an integer")
	}
	return n, nil
}
