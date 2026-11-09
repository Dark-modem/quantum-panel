// enginetemplates.go — TASK 2a: the engine PARAMETER TEMPLATE system.
//
// Every engine's engine_params has a different shape (Reality keys and
// shortId, wireguard peer exchange, rathole token, gost carrier,
// udp2raw password, ports, server_side). The panel needs to render a
// TYPED FORM per engine for a non-expert — labels fa/en, defaults,
// validation, secret fields masked and never echoed back after saving.
//
// Sources, in priority order:
//  1. the engine catalog the agents report in hello (params_schema) —
//     used when the agent publishes our standard field shape;
//  2. the panel's FALLBACK TABLE below (always available, kept in sync
//     with the drivers).
//
// Secret fields are NEVER echoed back: tunnel GET responses strip them
// (they render as "set/unset" in the UI); tunnel create/update MERGE —
// an omitted or empty secret keeps the stored value, so the operator
// never has to re-enter (or see) a key to change another field.
// Generators run client-side (crypto.getRandomValues) so random secrets
// are born in the browser; the panel only ever sees the saved form.

package panel

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// engineParamField is one typed form field.
type engineParamField struct {
	JSON      string `json:"json"`                // engine_params key
	LabelEn   string `json:"label_en"`            // English label
	LabelFa   string `json:"label_fa"`            // Persian label
	Kind      string `json:"kind"`                // number | string | bool | secret
	Generator string `json:"generator,omitempty"` // "secret32" | "" (client-side)
	Required  bool   `json:"required,omitempty"`
	Default   string `json:"default,omitempty"` // rendered into the empty form
	Hint      string `json:"hint,omitempty"`    // one-line operator hint (en)
}

// engineParamTemplate is the fallback form for one engine.
type engineParamTemplate struct {
	Engine string             `json:"engine"`
	Fields []engineParamField `json:"fields"`
}

// engineParamTemplates is the FALLBACK table (panel-side; the agent
// catalog's params_schema takes precedence when it carries fields).
var engineParamTemplates = map[string][]engineParamField{
	"frp": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "0.61.1", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "frp-0.61.1-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "server_port", LabelEn: "frps control port", LabelFa: "پورت کنترل", Kind: "number", Default: "7000", Required: true},
		{JSON: "token", LabelEn: "Shared token", LabelFa: "توکن مشترک", Kind: "secret", Generator: "secret32", Required: true, Hint: "generated in the browser; same value both ends"},
		{JSON: "remote_port", LabelEn: "Forwarded port (foreign)", LabelFa: "پورت فوروارد", Kind: "number", Required: true},
		{JSON: "local_addr", LabelEn: "Iran target host", LabelFa: "هاست مقصد ایران", Kind: "string", Default: "127.0.0.1", Required: true},
		{JSON: "local_port", LabelEn: "Iran target port", LabelFa: "پورت مقصد ایران", Kind: "number", Required: true},
		{JSON: "dash_port", LabelEn: "Dashboard port", LabelFa: "پورت داشبورد", Kind: "number", Default: "7500"},
	},
	"wstunnel": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "10.1.9", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "wstunnel-10.1.9-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "ws_port", LabelEn: "WebSocket port", LabelFa: "پورت وب‌سوکت", Kind: "number", Default: "18901", Required: true},
		{JSON: "remote_port", LabelEn: "Forwarded TCP port", LabelFa: "پورت فوروارد TCP", Kind: "number", Required: true},
		{JSON: "remote_udp_port", LabelEn: "Forwarded UDP port (0 = off)", LabelFa: "پورت UDP (0 = خاموش)", Kind: "number"},
		{JSON: "local_addr", LabelEn: "Iran target host", LabelFa: "هاست مقصد ایران", Kind: "string", Default: "127.0.0.1", Required: true},
		{JSON: "local_port", LabelEn: "Iran target TCP port", LabelFa: "پورت مقصد TCP", Kind: "number", Required: true},
		{JSON: "local_udp_port", LabelEn: "Iran target UDP port", LabelFa: "پورت مقصد UDP", Kind: "number"},
	},
	"chisel": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "1.10.1", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "chisel-1.10.1-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "server_port", LabelEn: "Chisel server port", LabelFa: "پورت سرور", Kind: "number", Default: "18911", Required: true},
		{JSON: "remote_port", LabelEn: "Reverse port (foreign)", LabelFa: "پورت معکوس", Kind: "number", Required: true},
		{JSON: "local_addr", LabelEn: "Iran target host", LabelFa: "هاست مقصد ایران", Kind: "string", Default: "127.0.0.1", Required: true},
		{JSON: "local_port", LabelEn: "Iran target port", LabelFa: "پورت مقصد ایران", Kind: "number", Required: true},
		{JSON: "auth", LabelEn: "user:pass", LabelFa: "کاربر:رمز", Kind: "secret", Generator: "secret32", Hint: "generated in the browser; format user:pass"},
	},
	"backhaul": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "0.7.2", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "backhaul-0.7.2-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "server_port", LabelEn: "Backhaul server port", LabelFa: "پورت سرور", Kind: "number", Required: true},
		{JSON: "token", LabelEn: "Backhaul token", LabelFa: "توکن", Kind: "secret", Generator: "secret32", Required: true},
		{JSON: "local_addr", LabelEn: "Iran target host", LabelFa: "هاست مقصد ایران", Kind: "string", Default: "127.0.0.1", Required: true},
		{JSON: "local_port", LabelEn: "Iran target port", LabelFa: "پورت مقصد ایران", Kind: "number", Required: true},
	},
	"rathole": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "0.5.0", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "rathole-0.5.0-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "server_port", LabelEn: "Rathole server port", LabelFa: "پورت سرور", Kind: "number", Required: true},
		{JSON: "token", LabelEn: "Noise token", LabelFa: "توکن", Kind: "secret", Generator: "secret32", Required: true},
		{JSON: "local_addr", LabelEn: "Iran target host", LabelFa: "هاست مقصد ایران", Kind: "string", Default: "127.0.0.1", Required: true},
		{JSON: "local_port", LabelEn: "Iran target port", LabelFa: "پورت مقصد ایران", Kind: "number", Required: true},
	},
	"gost": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "3.3.3", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "gost-3.3.3-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "server_port", LabelEn: "Relay port", LabelFa: "پورت رله", Kind: "number", Required: true},
		{JSON: "carrier", LabelEn: "Carrier", LabelFa: "حامل", Kind: "string", Default: "relay"},
		{JSON: "userpass", LabelEn: "user:pass", LabelFa: "کاربر:رمز", Kind: "secret", Generator: "secret32"},
		{JSON: "local_addr", LabelEn: "Iran target host", LabelFa: "هاست مقصد ایران", Kind: "string", Default: "127.0.0.1", Required: true},
		{JSON: "local_port", LabelEn: "Iran target port", LabelFa: "پورت مقصد ایران", Kind: "number", Required: true},
	},
	"xray": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "24.12.31", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "xray-24.12.31-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "carrier_port", LabelEn: "VLESS+Reality port", LabelFa: "پورت VLESS", Kind: "number", Required: true},
		{JSON: "public_port", LabelEn: "Public forwarded port", LabelFa: "پورت عمومی", Kind: "number", Required: true},
		{JSON: "local_addr", LabelEn: "Iran target host", LabelFa: "هاست مقصد ایران", Kind: "string", Default: "127.0.0.1", Required: true},
		{JSON: "local_port", LabelEn: "Iran target port", LabelFa: "پورت مقصد ایران", Kind: "number", Required: true},
		{JSON: "uuid", LabelEn: "VLESS uuid", LabelFa: "uuid", Kind: "secret", Generator: "uuid", Required: true},
		{JSON: "reality_public_key", LabelEn: "Reality public key", LabelFa: "کلید عمومی Reality", Kind: "secret", Required: true, Hint: "generated on the agent (xray x25519); paste here"},
		{JSON: "reality_private_key", LabelEn: "Reality private key", LabelFa: "کلید خصوصی Reality", Kind: "secret", Required: true, Hint: "agent-side generation (xray x25519)"},
		{JSON: "short_id", LabelEn: "Reality shortId", LabelFa: "shortId", Kind: "secret", Required: true, Hint: "hex <=16 chars; agent-side generation"},
		{JSON: "server_name", LabelEn: "SNI", LabelFa: "SNI", Kind: "string", Default: "www.microsoft.com", Required: true},
		{JSON: "dest", LabelEn: "Reality dest", LabelFa: "مقصد Reality", Kind: "string", Default: "www.microsoft.com:443", Required: true},
	},
	"wireguard": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "listen_port", LabelEn: "WG listen port", LabelFa: "پورت شنونده", Kind: "number", Required: true},
		{JSON: "peer_public_key", LabelEn: "Peer public key", LabelFa: "کلید عمومی همتا", Kind: "secret", Required: true, Hint: "generated on the agent (wg genkey); paste here"},
	},
	"udp2raw": {
		{JSON: "password", LabelEn: "udp2raw password", LabelFa: "رمز udp2raw", Kind: "secret", Generator: "secret32", Required: true},
		{JSON: "listen_port", LabelEn: "fake-TCP listen port", LabelFa: "پورت شنونده", Kind: "number", Required: true},
	},
	"ctunnel": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "1.1.1", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "ctunnel-1.1.1-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "method", LabelEn: "Transport method", LabelFa: "روش انتقال", Kind: "string", Default: "dtls-1.3", Required: true, Hint: "dtls-1.3, quic-v1, srtp-direct, wg-stealth, rudp-fec, dnscrypt-udp, ntp-direct, openvpn-dco"},
		{JSON: "direction", LabelEn: "Direction", LabelFa: "جهت", Kind: "string", Default: "reverse", Hint: "reverse = foreign public port -> Iran target; direct = public Iran port -> foreign target"},
		{JSON: "foreign_addr", LabelEn: "Foreign host:port", LabelFa: "هاست:پورت خارج", Kind: "string", Required: true, Hint: "the host:port of the dialing end's peer (carrier)"},
		{JSON: "carrier_port", LabelEn: "UDP carrier port", LabelFa: "پورت حامل UDP", Kind: "number", Required: true},
		{JSON: "public_port", LabelEn: "Public forwarded port", LabelFa: "پورت عمومی", Kind: "number", Required: true},
		{JSON: "local_addr", LabelEn: "Target host:port", LabelFa: "مقصد هاست:پورت", Kind: "string", Default: "127.0.0.1", Required: true, Hint: "target service on the non-server end; 127.0.0.1 works (in-agent relay)"},
		{JSON: "proto", LabelEn: "Forwarded protocols", LabelFa: "پروتکل‌های فوروارد", Kind: "string", Default: "tcp", Hint: "tcp, udp or both"},
		{JSON: "secret", LabelEn: "Tunnel secret (64 hex)", LabelFa: "رمز تونل (۶۴ هگز)", Kind: "secret", Generator: "secret32", Required: true, Hint: "generated in the browser; same value both ends"},
		{JSON: "padding", LabelEn: "Packet padding (0 = default 16)", LabelFa: "پرکننده بسته (۰ = پیش‌فرض ۱۶)", Kind: "number"},
		{JSON: "mtu", LabelEn: "Tunnel MTU (0 = auto)", LabelFa: "MTU تونل (۰ = خودکار)", Kind: "number"},
	},
	"matrix": {
		{JSON: "instance", LabelEn: "Matrix instance (from Agent Tunnels)", LabelFa: "نمونه ماتریکس (از Agent Tunnels)", Kind: "string", Required: true, Hint: "provision via Agent Tunnels FIRST, then reference here"},
	},
}

// engineParamTemplatesFor returns the form template for one engine: the
// panel fallback table (agent params_schema parsing lands with the
// catalog work; the fallback is always available and authoritative for
// secret masking).
func engineParamTemplatesFor(engine string) ([]engineParamField, bool) {
	f, ok := engineParamTemplates[engine]
	return f, ok
}

// engineSecretKeys returns the JSON keys of the SECRET fields for one
// engine (used to strip values from API responses and to merge updates).
func engineSecretKeys(engine string) map[string]bool {
	out := map[string]bool{}
	for _, f := range engineParamTemplates[engine] {
		if f.Kind == "secret" {
			out[f.JSON] = true
		}
	}
	return out
}

// stripEngineSecrets removes the values of SECRET fields from an
// engine_params JSON for API responses: each secret becomes {"__set":true}
// (or {"__set":false} when empty) so the UI can render set/unset without
// ever seeing the value. Invalid JSON passes through untouched.
func stripEngineSecrets(engine string, params string) string {
	fields, ok := engineParamTemplatesFor(engine)
	if !ok || len(params) == 0 {
		return params
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(params), &m); err != nil {
		return params
	}
	changed := false
	for _, f := range fields {
		if f.Kind != "secret" {
			continue
		}
		v, present := m[f.JSON]
		if !present {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if strings.TrimSpace(s) != "" {
				m[f.JSON] = json.RawMessage(`{"__set":true}`)
			} else {
				m[f.JSON] = json.RawMessage(`{"__set":false}`)
			}
			changed = true
		}
	}
	if !changed {
		return params
	}
	out, err := json.Marshal(m)
	if err != nil {
		return params
	}
	return string(out)
}

// mergeEngineSecrets re-applies the STORED secret values on top of an
// update: any secret field the update does not carry (or carries as an
// empty string — the masked form's "unchanged" value) keeps its stored
// value; a non-empty new value replaces it. Unknown engines pass through.
func mergeEngineSecrets(engine string, oldParams, newParams string) string {
	fields, ok := engineParamTemplatesFor(engine)
	if !ok || len(oldParams) == 0 || len(newParams) == 0 {
		return newParams
	}
	var oldM, newM map[string]json.RawMessage
	if err := json.Unmarshal([]byte(oldParams), &oldM); err != nil {
		return newParams
	}
	if err := json.Unmarshal([]byte(newParams), &newM); err != nil {
		return newParams
	}
	changed := false
	for _, f := range fields {
		if f.Kind != "secret" {
			continue
		}
		// Absent or masked-empty → the stored value rides along (the
		// operator never re-enters a secret to change another field).
		nv, present := newM[f.JSON]
		if !present {
			if ov, ok := oldM[f.JSON]; ok {
				newM[f.JSON] = ov
				changed = true
			}
			continue
		}
		var ns string
		if err := json.Unmarshal(nv, &ns); err == nil && strings.TrimSpace(ns) == "" {
			if ov, ok := oldM[f.JSON]; ok {
				newM[f.JSON] = ov
				changed = true
			}
		}
	}
	if !changed {
		return newParams
	}
	out, err := json.Marshal(newM)
	if err != nil {
		return newParams
	}
	return string(out)
}

// handleEngineTemplates serves the fallback parameter templates (plus a
// note about the agent-catalog source) for the wizard/topology forms.
func handleEngineTemplates(w http.ResponseWriter, r *http.Request) {
	out := make([]engineParamTemplate, 0, len(engineParamTemplates))
	for engine, fields := range engineParamTemplates {
		out = append(out, engineParamTemplate{Engine: engine, Fields: fields})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Engine < out[j].Engine })
	writeJSON(w, map[string]any{"templates": out})
}

func registerEngineTemplateRoutes(mux *http.ServeMux, base string) {
	mux.HandleFunc("GET "+base+"/api/mesh/engines/templates", requireAuth(handleEngineTemplates))
}
