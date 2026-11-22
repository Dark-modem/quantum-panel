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

	"rakhsh": {
		{JSON: "version", LabelEn: "Release version", LabelFa: "نسخه", Kind: "string", Default: "v0.1.0", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "rakhsh-{version}-x86_64-unknown-linux-musl", Required: true},
		{JSON: "token", LabelEn: "Pre-shared key", LabelFa: "کلید مشترک (psk)", Kind: "secret", Generator: "secret32", Required: true, Hint: "generated in the browser; same value both ends"},
		{JSON: "carrier", LabelEn: "Carrier", LabelFa: "حامل", Kind: "string", Default: "tcp", Hint: "tcp / tls / ws / quic / arq-udp"},
		{JSON: "remote_identity", LabelEn: "Remote identity", LabelFa: "هویت مقصد", Kind: "string", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "remote_port", LabelEn: "Remote port", LabelFa: "پورت مقصد", Kind: "number", Default: "443", Required: true},
		{JSON: "lanes", LabelEn: "Tunnel lanes", LabelFa: "لاین‌های تونل", Kind: "number", Default: "4"},
	},
	"aestun": {
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "1.0.0", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "aestun-{version}-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "key", LabelEn: "Shared key", LabelFa: "کلید مشترک", Kind: "secret", Generator: "secret32", Required: true, Hint: "generated in the browser; same value both ends"},
		{JSON: "cipher", LabelEn: "Cipher", LabelFa: "رمزنگاری", Kind: "string", Default: "aes-256-gcm", Required: true},
		{JSON: "obfs", LabelEn: "Wire disguise", LabelFa: "استتار ترافیک", Kind: "string", Default: "quic", Required: true},
		{JSON: "remote_addr", LabelEn: "Foreign host", LabelFa: "هاست خارج", Kind: "string", Required: true},
		{JSON: "udp_port", LabelEn: "Carrier port (UDP)", LabelFa: "پورت حامل (UDP)", Kind: "number", Default: "443", Required: true},
		{JSON: "tun_addr", LabelEn: "TUN address", LabelFa: "آدرس TUN", Kind: "string", Default: "10.8.0.2/24"},
	},
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
		// BACKEND-45: the OVERNIGHT-A/A2/A3 additions — proxy_type family +
		// secret-proxy key + the vhost family (the frps vhost ports route by
		// domain; vhost proxy blocks carry NO remote_port).
		{JSON: "proxy_type", LabelEn: "Proxy type", LabelFa: "نوع پروکسی", Kind: "string", Default: "tcp", Hint: "tcp|udp|stcp|sudp|xtcp|http|https|tcpmux"},
		{JSON: "transport", LabelEn: "frpc↔frps carrier", LabelFa: "حامل فرپ‌کلیت", Kind: "string", Default: "tcp", Hint: "tcp|kcp|quic|websocket"},
		{JSON: "use_encryption", LabelEn: "Encrypt traffic", LabelFa: "رمزنگاری ترافیک", Kind: "bool"},
		{JSON: "use_compression", LabelEn: "Compress traffic", LabelFa: "فشرده‌سازی ترافیک", Kind: "bool"},
		{JSON: "secret_key", LabelEn: "Secret-proxy key", LabelFa: "کلید پروکسی مخفی", Kind: "secret", Generator: "secret32", Hint: "proxy_type stcp/sudp/xtcp — same value both ends"},
		{JSON: "vhost_http_port", LabelEn: "frps vhostHTTPPort (0 = off)", LabelFa: "پورت vhost HTTP", Kind: "number", Hint: "proxy_type http — server end"},
		{JSON: "vhost_https_port", LabelEn: "frps vhostHTTPSPort (0 = off)", LabelFa: "پورت vhost HTTPS", Kind: "number", Hint: "proxy_type https — server end"},
		{JSON: "tcpmux_http_connect_port", LabelEn: "frps tcpmux HTTP CONNECT port", LabelFa: "پورت tcpmux", Kind: "number", Hint: "proxy_type tcpmux — server end"},
		{JSON: "tcpmux_passthrough", LabelEn: "tcpmux passthrough", LabelFa: "عبور مستقیم tcpmux", Kind: "bool"},
		{JSON: "sub_domain_host", LabelEn: "frps subDomainHost", LabelFa: "دامنه والد", Kind: "string", Hint: "server end; <subdomain>.<sub_domain_host> routes"},
		{JSON: "subdomain", LabelEn: "Subdomain", LabelFa: "زیردامنه", Kind: "string", Hint: "client proxy block"},
		{JSON: "host_header_rewrite", LabelEn: "Host header rewrite", LabelFa: "بازنویسی Host", Kind: "string", Hint: "proxy_type http"},
		{JSON: "http_user", LabelEn: "HTTP auth user", LabelFa: "کاربر HTTP", Kind: "string", Hint: "proxy_type http/tcpmux"},
		{JSON: "http_password", LabelEn: "HTTP auth password", LabelFa: "رمز HTTP", Kind: "secret", Generator: "secret32", Hint: "proxy_type http/tcpmux"},
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
	// BACKEND-45: resynced to the AGENT-30-generation gost driver's params
	// (gost_driver.go gostParams — the old rows remote_addr/server_port/
	// carrier/userpass were the PRE-driver names no driver consumes) plus
	// the OVERNIGHT-A2/A3 additions (TLS-family cert paths, ssh/ss/utls
	// carrier auth, ingress flip, service families tap/redirect/sni/masque).
	"gost": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "3.3.3", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "gost-3.3.3-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "carrier_addr", LabelEn: "Carrier address (server listen / client dial host:port)", LabelFa: "آدرس حامل (شنونده/اتصال host:port)", Kind: "string", Default: "0.0.0.0:23344", Required: true},
		{JSON: "public_port", LabelEn: "Public data port on the foreign host (rtcp/rudp)", LabelFa: "پورت عمومی روی خارج", Kind: "number", Required: true},
		{JSON: "local_addr", LabelEn: "Iran target host:port", LabelFa: "مقصد ایران host:port", Kind: "string", Default: "127.0.0.1:5201", Required: true},
		{JSON: "transport", LabelEn: "Carrier transport", LabelFa: "حامل", Kind: "string", Default: "ws", Hint: "ws|tcp|dtls|quic|grpc|http2|kcp|obfs-http|obfs-tls|ftcp|mws|pht|tls|mtls|h2|http3|h3|wt|utls|ssh|sshd|ss"},
		{JSON: "mode", LabelEn: "Forward family", LabelFa: "خانواده فوروارد", Kind: "string", Default: "tcp", Hint: "tcp (rtcp) | udp (rudp)"},
		{JSON: "tls_cert", LabelEn: "TLS cert path (PEM, server end)", LabelFa: "مسیر گواهی TLS", Kind: "string", Hint: "required for the TLS-family carriers"},
		{JSON: "tls_key", LabelEn: "TLS key path (PEM, server end)", LabelFa: "مسیر کلید TLS", Kind: "string", Hint: "required for the TLS-family carriers"},
		{JSON: "ssh_user", LabelEn: "SSH carrier user", LabelFa: "کاربر SSH", Kind: "string", Hint: "transport ssh/sshd"},
		{JSON: "ssh_password", LabelEn: "SSH carrier password", LabelFa: "رمز SSH", Kind: "secret", Generator: "secret32", Hint: "transport ssh/sshd"},
		{JSON: "ss_method", LabelEn: "shadowsocks cipher", LabelFa: "روش رمزنگاری ss", Kind: "string", Default: "aes-256-gcm", Hint: "transport ss"},
		{JSON: "ss_password", LabelEn: "shadowsocks password", LabelFa: "رمز shadowsocks", Kind: "secret", Generator: "secret32", Hint: "transport ss"},
		{JSON: "utls_fingerprint", LabelEn: "uTLS fingerprint", LabelFa: "اثر انگشت uTLS", Kind: "string", Default: "chrome", Hint: "transport utls"},
		{JSON: "ingress", LabelEn: "Ingress flip (data-port end)", LabelFa: "معکوس ورودی", Kind: "string", Hint: "empty = reverse forwarding | socks5 | http | socks4"},
		{JSON: "service", LabelEn: "Service family", LabelFa: "خانواده سرویس", Kind: "string", Hint: "empty = relay tunnel | tap | redirect-tcp | redirect-udp | sni | masque"},
		{JSON: "tap_net", LabelEn: "TAP client CIDR", LabelFa: "CIDR کلاینت TAP", Kind: "string", Default: "10.66.0.1/24", Hint: "service=tap"},
		{JSON: "tap_net_server", LabelEn: "TAP server CIDR", LabelFa: "CIDR سرور TAP", Kind: "string", Default: "10.66.0.2/24", Hint: "service=tap"},
		{JSON: "tap_name", LabelEn: "TAP device name", LabelFa: "نام دستگاه TAP", Kind: "string", Hint: "service=tap (default = gost's preset)"},
		{JSON: "tap_mtu", LabelEn: "TAP MTU", LabelFa: "MTU تپ", Kind: "number", Default: "1420", Hint: "service=tap"},
		{JSON: "tap_gw", LabelEn: "TAP gateway IP", LabelFa: "گیت‌وی TAP", Kind: "string", Hint: "service=tap"},
		{JSON: "tap_route", LabelEn: "TAP client routes (comma CIDRs)", LabelFa: "روت‌های کلاینت TAP", Kind: "string", Hint: "service=tap"},
		{JSON: "tap_route_server", LabelEn: "TAP server routes (comma CIDRs)", LabelFa: "روت‌های سرور TAP", Kind: "string", Hint: "service=tap"},
		{JSON: "redirect_iface", LabelEn: "Redirect interface", LabelFa: "اینترفیس ریدایرکت", Kind: "string", Hint: "service=redirect-*: nat REDIRECT/TPROXY scope; empty = rules skipped"},
		{JSON: "masque_user", LabelEn: "MASQUE auth user", LabelFa: "کاربر MASQUE", Kind: "string", Hint: "service=masque (HTTP Basic)"},
		{JSON: "masque_password", LabelEn: "MASQUE auth password", LabelFa: "رمز MASQUE", Kind: "secret", Generator: "secret32", Hint: "service=masque (HTTP Basic)"},
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
		// BACKEND-45: the OVERNIGHT-A3 client inbounds (loopback, routed to
		// the carrier outbound; the server end never renders them).
		{JSON: "socks_port", LabelEn: "Client socks5 inbound (0 = off)", LabelFa: "پورت socks کلاینت", Kind: "number", Hint: "client end: 127.0.0.1 socks inbound"},
		{JSON: "http_port", LabelEn: "Client http inbound (0 = off)", LabelFa: "پورت http کلاینت", Kind: "number", Hint: "client end: 127.0.0.1 http inbound"},
	},
	// BACKEND-45: the AGENT-31/OVERNIGHT-A3 drivers' typed forms (the
	// shapes mirror the agents' Describe() params_schema verbatim — the
	// authoritative copy rides the §2.4 engines rows' params_schema once an
	// agent has reported). hysteria2 is always TLS; tcp_forwards/
	// udp_forwards are JSON-array fields and stay outside the typed form
	// (raw JSON / the params_schema UI path).
	"hysteria": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true, Hint: "the end hosting hysteria server"},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "2.14.0", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "hysteria-2.14.0-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "server_addr", LabelEn: "QUIC host:port (server listen / client dial)", LabelFa: "آدرس QUIC host:port", Kind: "string", Default: "0.0.0.0:36712", Required: true},
		{JSON: "auth_password", LabelEn: "Auth password", LabelFa: "رمز احراز", Kind: "secret", Generator: "secret32", Required: true, Hint: "same value both ends"},
		{JSON: "obfs_password", LabelEn: "Salamander obfs password", LabelFa: "رمز استتار سالاماندر", Kind: "secret", Generator: "secret32", Hint: "optional; same value both ends"},
		{JSON: "tls_cert", LabelEn: "TLS cert path (PEM, server end)", LabelFa: "مسیر گواهی TLS", Kind: "string", Hint: "server end; hysteria2 is always TLS — self-signed pairs are legitimate"},
		{JSON: "tls_key", LabelEn: "TLS key path (PEM, server end)", LabelFa: "مسیر کلید TLS", Kind: "string", Hint: "server end"},
		{JSON: "sni", LabelEn: "Client SNI", LabelFa: "SNI کلاینت", Kind: "string"},
		{JSON: "insecure", LabelEn: "Skip TLS verify (self-signed)", LabelFa: "بدون تأیید TLS", Kind: "bool", Hint: "client end; for operator self-signed pairs"},
		{JSON: "pin_sha256", LabelEn: "Cert pin (hex sha256)", LabelFa: "پین گواهی", Kind: "string", Hint: "client end; alternative to insecure"},
		{JSON: "up_mbps", LabelEn: "Up bandwidth (N mbps)", LabelFa: "پهنای‌بند بالا", Kind: "string", Default: "100 mbps", Hint: "upstream ConvBandwidth: numeric \"N mbps\""},
		{JSON: "down_mbps", LabelEn: "Down bandwidth (N mbps)", LabelFa: "پهنای‌بند پایین", Kind: "string", Hint: "upstream ConvBandwidth: numeric \"N mbps\""},
		{JSON: "socks_port", LabelEn: "Client socks5 ingress (0 = off)", LabelFa: "پورت socks کلاینت", Kind: "number", Hint: "client end: 127.0.0.1 socks ingress"},
		{JSON: "http_port", LabelEn: "Client http ingress (0 = off)", LabelFa: "پورت http کلاینت", Kind: "number", Hint: "client end: http ingress"},
	},
	"singbox": {
		{JSON: "server_side", LabelEn: "Server end (foreign name)", LabelFa: "سمت سرور (نام خارج)", Kind: "string", Required: true, Hint: "the end hosting the sing-box inbound"},
		{JSON: "version", LabelEn: "Artifact version", LabelFa: "نسخه", Kind: "string", Default: "1.14.4", Required: true},
		{JSON: "artifact", LabelEn: "Artifact name template", LabelFa: "قالب نام فایل", Kind: "string", Default: "sing-box-1.14.4-linux-{arch}.tar.gz", Required: true},
		{JSON: "sha256_amd64", LabelEn: "sha256 (amd64)", LabelFa: "sha256 (amd64)", Kind: "secret", Required: true},
		{JSON: "sha256_arm64", LabelEn: "sha256 (arm64)", LabelFa: "sha256 (arm64)", Kind: "secret", Required: true},
		{JSON: "signature_amd64", LabelEn: "ed25519 signature (amd64)", LabelFa: "امضا (amd64)", Kind: "secret", Required: true},
		{JSON: "signature_arm64", LabelEn: "ed25519 signature (arm64)", LabelFa: "امضا (arm64)", Kind: "secret", Required: true},
		{JSON: "server_addr", LabelEn: "Carrier host:port (server listen / client dial)", LabelFa: "آدرس حامل host:port", Kind: "string", Default: "0.0.0.0:8388", Required: true},
		{JSON: "protocol", LabelEn: "Protocol", LabelFa: "پروتکل", Kind: "string", Default: "shadowsocks", Required: true, Hint: "shadowsocks|vless|vmess|trojan|hysteria2|hysteria|tuic|anytls|shadowtls|snell|naive(server)|tor|tailscale"},
		{JSON: "uuid", LabelEn: "User uuid", LabelFa: "uuid کاربر", Kind: "secret", Generator: "uuid", Hint: "vless/vmess/tuic"},
		{JSON: "password", LabelEn: "Auth password", LabelFa: "رمز احراز", Kind: "secret", Generator: "secret32", Hint: "trojan/anytls/hysteria2/tuic/snell/shadowtls/naive/shadowsocks — per-protocol"},
		{JSON: "ss_method", LabelEn: "shadowsocks cipher", LabelFa: "روش رمزنگاری ss", Kind: "string", Default: "aes-256-gcm"},
		{JSON: "ss_password", LabelEn: "shadowtls inner ss password", LabelFa: "رمز داخلی ss", Kind: "secret", Generator: "secret32", Hint: "shadowtls: the inner shadowsocks layer"},
		{JSON: "up_mbps", LabelEn: "Up bandwidth", LabelFa: "پهنای‌بند بالا", Kind: "number", Hint: "hysteria2/tuic"},
		{JSON: "down_mbps", LabelEn: "Down bandwidth", LabelFa: "پهنای‌بند پایین", Kind: "number", Hint: "hysteria2/tuic"},
		{JSON: "congestion", LabelEn: "Congestion control", LabelFa: "کنترل ازدحام", Kind: "string", Hint: "cubic|new_reno|bbr (tuic)"},
		{JSON: "tls_cert", LabelEn: "TLS cert path (PEM, server end)", LabelFa: "مسیر گواهی TLS", Kind: "string", Hint: "required for trojan/hysteria2/tuic/anytls (upstream TLS-only)"},
		{JSON: "tls_key", LabelEn: "TLS key path (PEM, server end)", LabelFa: "مسیر کلید TLS", Kind: "string", Hint: "required for trojan/hysteria2/tuic/anytls"},
		{JSON: "sni", LabelEn: "Client SNI", LabelFa: "SNI کلاینت", Kind: "string"},
		{JSON: "insecure", LabelEn: "Skip TLS verify (self-signed)", LabelFa: "بدون تأیید TLS", Kind: "bool"},
		{JSON: "local_mode", LabelEn: "Client ingress mode", LabelFa: "حالت ورودی کلاینت", Kind: "string", Default: "proxy", Hint: "proxy (mixed socks+http) | forward (direct inbound) — exactly one"},
		{JSON: "local_port", LabelEn: "Client ingress listen port", LabelFa: "پورت ورودی کلاینت", Kind: "number", Required: true},
		{JSON: "forward_addr", LabelEn: "Forward destination host:port", LabelFa: "مقصد فوروارد host:port", Kind: "string", Hint: "mode=forward: dialed by the SERVER end through the tunnel"},
		{JSON: "obfs", LabelEn: "hysteria v1 obfs password", LabelFa: "رمز استتار hysteria v1", Kind: "string"},
		{JSON: "shadowtls_handshake", LabelEn: "shadowtls camouflage site", LabelFa: "سایت استتار shadowtls", Kind: "string", Default: "www.apple.com:443", Hint: "shadowtls handshake server"},
		{JSON: "shadowtls_version", LabelEn: "shadowtls version", LabelFa: "نسخه shadowtls", Kind: "number", Default: "3"},
		{JSON: "snell_version", LabelEn: "snell version", LabelFa: "نسخه snell", Kind: "number", Default: "6"},
		{JSON: "tor_exec_path", LabelEn: "tor daemon path", LabelFa: "مسیر دیمن tor", Kind: "string", Hint: "operator-supplied tor binary (nothing vendored)"},
		{JSON: "naive_username", LabelEn: "naive HTTP Basic user", LabelFa: "کاربر naive", Kind: "string", Hint: "naive server half (the client half is cronet-gated upstream)"},
		{JSON: "tailscale_auth_key", LabelEn: "tailscale auth key", LabelFa: "کلید tailscale", Kind: "secret", Hint: "required for protocol=tailscale"},
		{JSON: "tailscale_state_dir", LabelEn: "tailscale state dir", LabelFa: "پوشه حالت tailscale", Kind: "string"},
		{JSON: "tailscale_hostname", LabelEn: "tailscale hostname", LabelFa: "نام میزبان tailscale", Kind: "string"},
		{JSON: "tailscale_ephemeral", LabelEn: "tailscale ephemeral node", LabelFa: "گره موقت tailscale", Kind: "bool"},
		{JSON: "tailscale_exit_node", LabelEn: "tailscale exit node", LabelFa: "گره خروج tailscale", Kind: "string"},
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
