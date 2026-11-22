package panel

import "testing"

// phase46_real_manifests_test.go — BACKEND-46 fixtures: the REAL landed
// quantum-cores MANIFEST.json bodies (verbatim bytes, read from the repo at
// origin/main 6ebd450, 2026-11-23; all 13 byte-verified against the origin
// blobs). ALL THIRTEEN cores now publish methods[] — ctunnel 49 (AGENT-29),
// sing-box 16 + hysteria 2 (OVERNIGHT-C/AGENT-31), waterwall 18
// (OVERNIGHT-C2/A4), easytier 1 (OVERNIGHT-D2), and CORES-5's concurrent
// mirror blocks (frp 16 / xray 17 / gost 37 / backhaul 8 / rathole 6 /
// udp2raw 4 / chisel 1 / wstunnel 2) = 177 published rows, the enumeration
// ground truth for the §2.23 method-board join and the §2.22 passthrough.
// Hermetic: served through the coresManifestFetcher seam, no network, no
// sibling-checkout reads at test time.

const phase46Manifestctunnel = `{
  "core": "ctunnel",
  "upstream": "https://github.com/CIAUB/C-Tunnel",
  "license": "AGPL-3.0",
  "latest": "2.0.0",
  "versions": [
    {
      "tag": "ctunnel-v2.0.0",
      "assets": {
        "linux-amd64": {
          "name": "ctunnel-linux-amd64.tar.gz",
          "sha256": "19d6b3f0d64c57b7b9fb1231ceed8cb23219aee079e262c5c2abaf2b8da2cf32"
        },
        "linux-arm64": {
          "name": "ctunnel-linux-arm64.tar.gz",
          "sha256": "69f57078a800a67e6afded0c77b7819240c102bc0593e9bfe0bc4a1b9ad15a2b"
        }
      }
    }
  ],
  "methods": [
    {
      "id": 1,
      "name": "6to4",
      "title": "6to4 (SIT carrier + IPIP6 inside)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 60,
      "needs_port": false,
      "userspace": false,
      "mikrotik": true,
      "family": "kernel-l3"
    },
    {
      "id": 2,
      "name": "6to4-gre",
      "title": "6to4-GRE (SIT carrier + IP6GRE inside)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 64,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 3,
      "name": "sit",
      "title": "SIT (IPv6-in-IPv4, proto 41, IPv6 inside only)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 20,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 4,
      "name": "ipip",
      "title": "IPIP (IPv4-in-IPv4, proto 4)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 20,
      "needs_port": false,
      "userspace": false,
      "mikrotik": true,
      "family": "kernel-l3"
    },
    {
      "id": 5,
      "name": "gre",
      "title": "GRE (proto 47)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 24,
      "needs_port": false,
      "userspace": false,
      "mikrotik": true,
      "family": "kernel-l3"
    },
    {
      "id": 6,
      "name": "ip6gre",
      "title": "IP6GRE (GRE over IPv6)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v6",
      "overhead": 44,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 7,
      "name": "ipip6",
      "title": "IPIP6 (IPv4 over IPv6)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v6",
      "overhead": 40,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 8,
      "name": "ip6ip6",
      "title": "IP6IP6 (IPv6 over IPv6, IPv6 inside only)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v6",
      "overhead": 40,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 9,
      "name": "wireguard",
      "title": "WireGuard (kernel, ChaCha20-Poly1305)",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 60,
      "needs_port": true,
      "userspace": false,
      "mikrotik": true,
      "family": "kernel-l3"
    },
    {
      "id": 10,
      "name": "xfrm-ipsec",
      "title": "GRE + kernel IPsec ESP (AES-GCM-256)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 62,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 11,
      "name": "fou-gre",
      "title": "FOU-GRE (GRE inside UDP)",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 32,
      "needs_port": true,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 12,
      "name": "gue-ipip",
      "title": "GUE-IPIP (IPIP inside GUE/UDP)",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 32,
      "needs_port": true,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 13,
      "name": "ecmp-lanes",
      "title": "ECMP-Lanes (2-8 parallel keyed GRE)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 28,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 41,
      "name": "bareudp",
      "title": "BareUDP (IPv4 straight inside UDP, kernel 5.7+)",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 28,
      "needs_port": true,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 44,
      "name": "xgre",
      "title": "xGRE (eXtended multi-lane keyed GRE, ignore-df, FOU-ready)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 28,
      "needs_port": false,
      "userspace": false,
      "mikrotik": true,
      "family": "kernel-l3"
    },
    {
      "id": 45,
      "name": "xgre-ip",
      "title": "xGRE-IP (IPIP inside multi-lane keyed xGRE carrier)",
      "layer": "L3",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 48,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l3"
    },
    {
      "id": 14,
      "name": "eoip",
      "title": "EoIP (MikroTik-compatible, raw GRE)",
      "layer": "L2",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 42,
      "needs_port": false,
      "userspace": true,
      "mikrotik": true,
      "family": "kernel-l2"
    },
    {
      "id": 15,
      "name": "gretap",
      "title": "GRETAP (Ethernet over GRE)",
      "layer": "L2",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 38,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l2"
    },
    {
      "id": 16,
      "name": "ip6gretap",
      "title": "IP6GRETAP (Ethernet over IPv6 GRE)",
      "layer": "L2",
      "transport": "raw-ip",
      "ipver": "v6",
      "overhead": 58,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l2"
    },
    {
      "id": 17,
      "name": "vxlan",
      "title": "VXLAN (Ethernet over UDP)",
      "layer": "L2",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 50,
      "needs_port": true,
      "userspace": false,
      "mikrotik": true,
      "family": "kernel-l2"
    },
    {
      "id": 18,
      "name": "geneve",
      "title": "GENEVE (Ethernet over UDP)",
      "layer": "L2",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 50,
      "needs_port": true,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l2"
    },
    {
      "id": 19,
      "name": "l2tpv3",
      "title": "L2TPv3 static pseudowire (IP 115 or UDP)",
      "layer": "L2",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 50,
      "needs_port": false,
      "userspace": false,
      "mikrotik": false,
      "family": "kernel-l2"
    },
    {
      "id": 20,
      "name": "etherip",
      "title": "EtherIP (RFC 3378, proto 97)",
      "layer": "L2",
      "transport": "raw-ip",
      "ipver": "v4",
      "overhead": 36,
      "needs_port": false,
      "userspace": true,
      "mikrotik": false,
      "family": "kernel-l2"
    },
    {
      "id": 21,
      "name": "dtls-1.3",
      "title": "DTLS record framing + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 66,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 22,
      "name": "quic-v1",
      "title": "QUIC short-header framing + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 64,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 23,
      "name": "srtp-direct",
      "title": "RTP media framing + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 65,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 24,
      "name": "wg-stealth",
      "title": "Random-magic header + junk burst + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 61,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 25,
      "name": "rudp-fec",
      "title": "XOR-FEC 2+1 parity + AES-256-GCM (lossy links)",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 65,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 26,
      "name": "dnscrypt-udp",
      "title": "DNS response framing + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 65,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 27,
      "name": "ntp-direct",
      "title": "NTPv4 framing + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 61,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 28,
      "name": "openvpn-dco",
      "title": "OpenVPN P_DATA_V2 framing + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 61,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 36,
      "name": "stun-turn",
      "title": "TURN ChannelData framing (WebRTC calls) + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 57,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 39,
      "name": "gtp-u",
      "title": "GTP-U G-PDU framing (mobile core) + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 61,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 40,
      "name": "esp-udp",
      "title": "IPsec ESP-in-UDP framing (NAT-T) + AES-256-GCM",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 61,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "udp-crypt"
    },
    {
      "id": 37,
      "name": "faketcp",
      "title": "Hand-built TCP segments with TLS records + AES-256-GCM",
      "layer": "L3",
      "transport": "tcp",
      "ipver": "v4",
      "overhead": 82,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "stealth"
    },
    {
      "id": 38,
      "name": "icmp",
      "title": "ICMP echo (ping) + AES-256-GCM",
      "layer": "L3",
      "transport": "icmp",
      "ipver": "v4",
      "overhead": 53,
      "needs_port": false,
      "userspace": true,
      "mikrotik": false,
      "family": "stealth"
    },
    {
      "id": 29,
      "name": "tcp",
      "title": "TCP (a pooled connection per user connection)",
      "layer": "L4",
      "transport": "tcp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 30,
      "name": "tcpmux",
      "title": "TCP + MUX (streams over a few connections)",
      "layer": "L4",
      "transport": "tcp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 31,
      "name": "pck",
      "title": "TCP + PCK (KCP in hand-built TCP segments, raw socket)",
      "layer": "L4",
      "transport": "tcp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 32,
      "name": "ws",
      "title": "WebSocket",
      "layer": "L4",
      "transport": "tcp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 33,
      "name": "wsmux",
      "title": "WebSocket + MUX",
      "layer": "L4",
      "transport": "tcp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 34,
      "name": "wss",
      "title": "WebSocket in TLS",
      "layer": "L4",
      "transport": "tcp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 35,
      "name": "wssmux",
      "title": "WebSocket in TLS + MUX",
      "layer": "L4",
      "transport": "tcp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 42,
      "name": "udp",
      "title": "UDP (a KCP session per user connection, AES)",
      "layer": "L4",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 43,
      "name": "udpmux",
      "title": "UDP + MUX (streams over a few KCP sessions, AES)",
      "layer": "L4",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "reverse-l4"
    },
    {
      "id": 46,
      "name": "game-udp",
      "title": "Gaming UDP (Ultra-Low Ping \u00b7 4B header, DSCP EF, zero-batch)",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 48,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "gaming"
    },
    {
      "id": 47,
      "name": "game-fec",
      "title": "Gaming FEC (Packet-Loss Shield \u00b7 2+1 XOR-FEC + DSCP EF)",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 56,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "gaming"
    },
    {
      "id": 48,
      "name": "game-dup",
      "title": "Gaming Dual-Send (Zero-Spike Redundant UDP + Dedup)",
      "layer": "L3",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 52,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "gaming"
    },
    {
      "id": 49,
      "name": "game-kcp",
      "title": "Gaming Fast-KCP L4 (5ms NoDelay Port-Forward \u00b7 No TUN)",
      "layer": "L4",
      "transport": "udp",
      "ipver": "v4",
      "overhead": 0,
      "needs_port": true,
      "userspace": true,
      "mikrotik": false,
      "family": "gaming"
    }
  ]
}
`

const phase46Manifestsingbox = `{
 "core": "sing-box",
 "upstream": "https://github.com/SagerNet/sing-box",
 "license": "GPL-3.0",
 "latest": "1.14.4",
 "versions": [
  {
   "tag": "sing-box-v1.14.4",
   "assets": {
    "linux-amd64": {
     "name": "sing-box-linux-amd64.tar.gz",
     "sha256": "71b2d4a0ee1cea9a116ee944ebb9987295b823af78b562c9988f4e0b7b289c29"
    },
    "linux-arm64": {
     "name": "sing-box-linux-arm64.tar.gz",
     "sha256": "33152387648b22e8e13ae637f9c8e336c292c8260c13f7627a6c9e76c978a3de"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "vless",
   "title": "VLESS inbound/outbound (Reality/TLS)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 58
  },
  {
   "id": 2,
   "name": "vmess",
   "title": "VMess inbound/outbound",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 64
  },
  {
   "id": 3,
   "name": "trojan",
   "title": "Trojan inbound/outbound",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 58
  },
  {
   "id": 4,
   "name": "shadowsocks",
   "title": "Shadowsocks inbound/outbound (tcp+udp relay)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 60
  },
  {
   "id": 5,
   "name": "hysteria2",
   "title": "Hysteria2 inbound/outbound (QUIC)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 72
  },
  {
   "id": 6,
   "name": "hysteria",
   "title": "Hysteria v1 inbound/outbound (QUIC, legacy)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 72
  },
  {
   "id": 7,
   "name": "tuic",
   "title": "TUIC v5 inbound/outbound (QUIC)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 70
  },
  {
   "id": 8,
   "name": "anytls",
   "title": "AnyTLS inbound/outbound",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 58
  },
  {
   "id": 9,
   "name": "shadowtls",
   "title": "ShadowTLS v1-v3 inbound/outbound",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "obfuscation",
   "overhead": 62
  },
  {
   "id": 10,
   "name": "ssh",
   "title": "SSH outbound (tunnel over SSH)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 58
  },
  {
   "id": 11,
   "name": "snell",
   "title": "Snell outbound",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 58
  },
  {
   "id": 12,
   "name": "socks-http",
   "title": "SOCKS4/4a/5 + HTTP ingress (local proxy entry)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "ingress",
   "overhead": 0
  },
  {
   "id": 13,
   "name": "wireguard",
   "title": "WireGuard endpoint outbound/inbound (L3)",
   "layer": "L3",
   "transport": "udp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "kernel-l3",
   "overhead": 60
  },
  {
   "id": 14,
   "name": "openvpn",
   "title": "OpenVPN outbound (OCServ-style)",
   "layer": "L3",
   "transport": "udp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 74
  },
  {
   "id": 15,
   "name": "tun",
   "title": "TUN device inbound (gvisor/system stack)",
   "layer": "L3",
   "transport": "any",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tun",
   "overhead": 0
  },
  {
   "id": 16,
   "name": "tailscale",
   "title": "Tailscale outbound (DERP/WireGuard mesh)",
   "layer": "L3",
   "transport": "udp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "mesh",
   "overhead": 60
  }
 ]
}
`

const phase46Manifesthysteria = `{
 "core": "hysteria",
 "upstream": "https://github.com/apernet/hysteria",
 "license": "MIT",
 "latest": "2.14.0",
 "versions": [
  {
   "tag": "hysteria-v2.14.0",
   "assets": {
    "linux-amd64": {
     "name": "hysteria-linux-amd64.tar.gz",
     "sha256": "ef9270a5f10ce4a3868b52f10847ff8e249bed488d5e08eb5d1a78632aabbece"
    },
    "linux-arm64": {
     "name": "hysteria-linux-arm64.tar.gz",
     "sha256": "19c78f02c8210699435008bfa3a4b7ef9c686f151e4bb27125a1e56ee1b82732"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "hysteria2",
   "title": "Hysteria2 — QUIC-based bruteforce-resistant proxy (the v2 core protocol)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "proxy",
   "overhead": 72
  },
  {
   "id": 2,
   "name": "salamander",
   "title": "Salamander — QUIC obfuscation layer (password-encrypted, indistinguishable UDP)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "obfuscation",
   "overhead": 80
  }
 ]
}
`

const phase46Manifestwaterwall = `{
 "core": "waterwall",
 "upstream": "https://github.com/radkesvat/WaterWall",
 "license": "MPL-2.0",
 "latest": "1.6.0",
 "versions": [
  {
   "tag": "waterwall-v1.6.0",
   "assets": {
    "linux-amd64": {
     "name": "waterwall-linux-amd64.tar.gz",
     "sha256": "63facedcd912de7c37145cfb24505331ef1fd074fb1481c4b8736d3a0f56caa2"
    },
    "linux-arm64": {
     "name": "waterwall-linux-arm64.tar.gz",
     "sha256": "0efa4f7de2008cc5f518b20db5c76388e53428a05e860b6958b076ee3b08fbe4"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "quic",
   "title": "QUIC tunnel (Chromium quiche; client/server pairs)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 2,
   "name": "kcp",
   "title": "KCP tunnel (udp, fast retransmit)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 3,
   "name": "enet",
   "title": "ENet tunnel (reliable-udp, game-grade)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 4,
   "name": "reality",
   "title": "REALITY-steeled tunnel (anti-probing)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 5,
   "name": "trojan",
   "title": "Trojan tunnel",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 6,
   "name": "vless",
   "title": "VLESS tunnel",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 7,
   "name": "vmess",
   "title": "VMess tunnel",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 8,
   "name": "wireguard",
   "title": "WireGuard tunnel transport",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 9,
   "name": "rtmp",
   "title": "RTMP-shaped tunnel (media camouflage)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 10,
   "name": "gns",
   "title": "GNS-shaped tunnel",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 11,
   "name": "bgp4",
   "title": "BGP4-shaped tunnel",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 12,
   "name": "http",
   "title": "HTTP-shaped tunnel (plain camouflage)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 13,
   "name": "socks5",
   "title": "SOCKS5 ingress/egress",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 14,
   "name": "fake-tcp",
   "title": "FakeTcpHeader camouflage layer",
   "layer": "L4",
   "transport": "raw-ip",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 15,
   "name": "obfuscator",
   "title": "packet obfuscation layer (JunkDatagram + Disturber companions)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 16,
   "name": "connection-fisher",
   "title": "ConnectionFisher anti-analysis decoy pair",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 17,
   "name": "connection-stripe",
   "title": "ConnectionStripe multi-path split layer",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 18,
   "name": "mux",
   "title": "stream multiplexing layer",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  }
 ]
}
`

const phase46Manifesteasytier = `{
 "core": "easytier",
 "upstream": "https://github.com/EasyTier/EasyTier",
 "license": "LGPL-3.0",
 "latest": "2.7.1",
 "versions": [
  {
   "tag": "easytier-v2.7.1",
   "assets": {
    "linux-amd64": {
     "name": "easytier-linux-amd64.tar.gz",
     "sha256": "b13772b9cf61f107c2e67fd7f859fe6705fac14164a7a14024f5a3b94c3d8662"
    },
    "linux-arm64": {
     "name": "easytier-linux-arm64.tar.gz",
     "sha256": "fa58c183f98a7ebec8551765d834d1021d81eb3a087c47cad0e019eb7817d56b"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "easytier-mesh",
   "title": "EasyTier mesh VPN — L3 TUN overlay joining peers by network-name + network-secret (carriers tcp|udp|wg|ws|wss|quic|faketcp|ring; DHCP or static node IPs; public shared node / external-node discovery; subnet export via proxy-networks; config-server udp|tcp|ws|wss with easytier-web)",
   "layer": "L3",
   "transport": "tcp|udp|wg|ws|wss|quic|faketcp|ring",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "mesh"
  }
 ]
}
`

const phase46Manifestfrp = `{
 "core": "frp",
 "upstream": "https://github.com/fatedier/frp",
 "license": "Apache-2.0",
 "latest": "0.61.1",
 "versions": [
  {
   "tag": "frp-v0.61.1",
   "assets": {
    "linux-amd64": {
     "name": "frp-linux-amd64.tar.gz",
     "sha256": "9fb8bdbb7b96c80d81add5f11565483558c88ec7aa59237559c3d3c28eec21b5"
    },
    "linux-arm64": {
     "name": "frp-linux-arm64.tar.gz",
     "sha256": "ec6330ee780f3d67697ad7f0387fc816b15330795c558674ecb272b40149058f"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "proxy-tcp",
   "title": "plain TCP proxy (our driver's only shape today)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 2,
   "name": "proxy-udp",
   "title": "UDP proxy",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 3,
   "name": "proxy-http",
   "title": "HTTP reverse proxy with vhost routing",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 4,
   "name": "proxy-https",
   "title": "HTTPS reverse proxy with vhost",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 5,
   "name": "proxy-tcpmux",
   "title": "TCP mux with httpConnect (multiplexed by hostname)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 6,
   "name": "proxy-stcp",
   "title": "secret TCP - visitors need the secretKey",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 7,
   "name": "proxy-xtcp",
   "title": "P2P hole-punching proxy",
   "layer": "L4",
   "transport": "udp+p2p",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 8,
   "name": "proxy-sudp",
   "title": "secret UDP",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 9,
   "name": "visitor-stcp",
   "title": "stcp visitor (binds local port on the visitor end)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 10,
   "name": "visitor-xtcp",
   "title": "xtcp visitor (p2p hole punch client side)",
   "layer": "L4",
   "transport": "udp+p2p",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 11,
   "name": "visitor-sudp",
   "title": "sudp visitor",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 12,
   "name": "transport-tcp",
   "title": "client-server control+data over plain TCP",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 13,
   "name": "transport-kcp",
   "title": "KCP transport (lossy-link friendly)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 14,
   "name": "transport-quic",
   "title": "QUIC transport",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 15,
   "name": "transport-websocket",
   "title": "websocket transport",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 16,
   "name": "transport-wss",
   "title": "websocket over TLS transport",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  }
 ]
}
`

const phase46Manifestxray = `{
 "core": "xray",
 "upstream": "https://github.com/XTLS/Xray-core",
 "license": "MPL-2.0",
 "latest": "26.3.27",
 "versions": [
  {
   "tag": "xray-v26.3.27",
   "assets": {
    "linux-amd64": {
     "name": "xray-linux-amd64.tar.gz",
     "sha256": "68934f23069ded408b926fb253e079d874d97a88c9849946fa305f825555b533"
    },
    "linux-arm64": {
     "name": "xray-linux-arm64.tar.gz",
     "sha256": "7366d3e30e96a76a3448f69205624ae72b61b2acec649b44a34b558173ec8d53"
    }
   }
  },
  {
   "tag": "xray-v24.12.31",
   "assets": {
    "linux-amd64": {
     "name": "xray-linux-amd64.tar.gz",
     "sha256": "b18b92c6bf39af7c639ec0667c8bceec76d0bbe478bd1b7d332b239a61e19a0f"
    },
    "linux-arm64": {
     "name": "xray-linux-arm64.tar.gz",
     "sha256": "6fd6a1979504fe1eba7bc664799caa41cd40308836e62954d09d83b68c1e6dc2"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "vless+tcp+reality",
   "title": "our driver's current shape (vision flow disabled for mux)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 2,
   "name": "vless+xhttp+reality",
   "title": "XHTTP stream transport under REALITY (reality supports raw/xhttp/grpc only)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 3,
   "name": "vless+grpc+reality",
   "title": "gRPC carrier under REALITY (upstream-deprecated but functional)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 4,
   "name": "vless+tcp+tls",
   "title": "VLESS over TLS (operator cert needed; reality preferred)",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 5,
   "name": "vless+ws+tls",
   "title": "websocket transport (upstream-deprecated, still common in Iran)",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 6,
   "name": "vless+httpupgrade",
   "title": "httpupgrade transport (deprecated)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 7,
   "name": "vless+mkcp",
   "title": "mKCP carrier",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 8,
   "name": "vless+xhttp+tls",
   "title": "XHTTP under TLS",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 9,
   "name": "vmess+ws+tls",
   "title": "VMess over websocket+TLS",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 10,
   "name": "vmess+tcp+reality",
   "title": "VMess over Reality (mux.cool inner)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 11,
   "name": "trojan+tcp+tls",
   "title": "Trojan server",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 12,
   "name": "shadowsocks",
   "title": "shadowsocks inbound (tcp; udp relay optional)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 13,
   "name": "socks/http-in",
   "title": "socks/mixed/http local proxies",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 14,
   "name": "wireguard-inout",
   "title": "wireguard endpoint inside xray",
   "layer": "L3",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 15,
   "name": "hysteria-inout",
   "title": "hysteria protocol built in (both directions)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 16,
   "name": "masque-inout",
   "title": "MASQUE built in",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 17,
   "name": "tun-in",
   "title": "tun inbound (xray-side TUN)",
   "layer": "L3",
   "transport": "any",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  }
 ]
}
`

const phase46Manifestgost = `{
 "core": "gost",
 "upstream": "https://github.com/go-gost/gost",
 "license": "MIT",
 "latest": "3.3.3",
 "versions": [
  {
   "tag": "gost-v3.3.3",
   "assets": {
    "linux-amd64": {
     "name": "gost-linux-amd64.tar.gz",
     "sha256": "ca03d2a2fda2bd5003c91ac3e41e365715b8d077b283ff81469c5ab413ce769a"
    },
    "linux-arm64": {
     "name": "gost-linux-arm64.tar.gz",
     "sha256": "37bafec8eb72fe69ba1cde56083389c2994a36c6e17874e8d5c1ce1a0b6a7ef2"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "relay+tcp",
   "title": "reverse tunnel via relay handler over plain TCP carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 2,
   "name": "relay+tls",
   "title": "reverse tunnel via relay handler over TLS carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 3,
   "name": "relay+mtls",
   "title": "reverse tunnel via relay handler over mutual-TLS carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 4,
   "name": "relay+ws",
   "title": "reverse tunnel via relay handler over websocket carrier (our current driver shape relay+ws)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 5,
   "name": "relay+mws",
   "title": "reverse tunnel via relay handler over muxed websocket carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 6,
   "name": "relay+dtls",
   "title": "reverse tunnel via relay handler over DTLS (UDP) carrier",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 7,
   "name": "relay+quic",
   "title": "reverse tunnel via relay handler over QUIC carrier",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 8,
   "name": "relay+http3",
   "title": "reverse tunnel via relay handler over HTTP/3 carrier",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 9,
   "name": "relay+h3",
   "title": "reverse tunnel via relay handler over HTTP/3 alias carrier",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 10,
   "name": "relay+wt",
   "title": "reverse tunnel via relay handler over WebTransport carrier (http3)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 11,
   "name": "relay+grpc",
   "title": "reverse tunnel via relay handler over gRPC carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 12,
   "name": "relay+http2",
   "title": "reverse tunnel via relay handler over HTTP/2 carrier (h2c)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 13,
   "name": "relay+h2",
   "title": "reverse tunnel via relay handler over HTTP/2 over TLS carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 14,
   "name": "relay+kcp",
   "title": "reverse tunnel via relay handler over KCP (smux over UDP) carrier",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 15,
   "name": "relay+icmp",
   "title": "reverse tunnel via relay handler over raw ICMP echo carrier",
   "layer": "L4",
   "transport": "icmp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 16,
   "name": "relay+ftcp",
   "title": "reverse tunnel via relay handler over fragmented-TCP carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 17,
   "name": "relay+ssh",
   "title": "reverse tunnel via relay handler over SSH channel carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 18,
   "name": "relay+sshd",
   "title": "reverse tunnel via relay handler over SSH remote-forward carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 19,
   "name": "relay+obfs-http",
   "title": "reverse tunnel via relay handler over HTTP-obfuscated carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 20,
   "name": "relay+obfs-tls",
   "title": "reverse tunnel via relay handler over TLS-obfuscated carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 21,
   "name": "relay+pht",
   "title": "reverse tunnel via relay handler over Plain HTTP Tunnel carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 22,
   "name": "relay+utls",
   "title": "reverse tunnel via relay handler over uTLS-fingerprinted TLS carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 23,
   "name": "rtcp",
   "title": "reverse TCP port forwarding (public port on the server end)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 24,
   "name": "rudp",
   "title": "reverse UDP port forwarding",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 25,
   "name": "ss",
   "title": "shadowsocks listener+handler (ss-udp variant adds UDP relay)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 26,
   "name": "socks5",
   "title": "SOCKS5 proxy service (mixed adds http)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 27,
   "name": "socks4",
   "title": "SOCKS4 proxy service",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 28,
   "name": "http-proxy",
   "title": "HTTP proxy service",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 29,
   "name": "forward-local",
   "title": "local TCP port forwarding",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 30,
   "name": "forward-remote",
   "title": "remote port forwarding",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 31,
   "name": "tun",
   "title": "TUN device service (tungo = gvisor userspace variant)",
   "layer": "L3",
   "transport": "any",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 32,
   "name": "tap",
   "title": "TAP device service (L2 ethernet)",
   "layer": "L2",
   "transport": "any",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 33,
   "name": "redirect-tcp",
   "title": "transparent TCP proxy (iptables REDIRECT)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 34,
   "name": "redirect-udp",
   "title": "transparent UDP proxy",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 35,
   "name": "sni",
   "title": "SNI-dispatch proxy",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 36,
   "name": "dns",
   "title": "DNS forwarding service",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 37,
   "name": "masque",
   "title": "MASQUE (CONNECT-UDP) service",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  }
 ]
}
`

const phase46Manifestbackhaul = `{
 "core": "backhaul",
 "upstream": "https://github.com/Musixal/Backhaul",
 "license": "AGPL-3.0",
 "latest": "0.7.2",
 "versions": [
  {
   "tag": "backhaul-v0.7.2",
   "assets": {
    "linux-amd64": {
     "name": "backhaul-linux-amd64.tar.gz",
     "sha256": "7e9ecf9d960ec9a6ab2556c06cf3f04469ecc93ec9200d44532aa0a766e64b98"
    },
    "linux-arm64": {
     "name": "backhaul-linux-arm64.tar.gz",
     "sha256": "1de1d229c2ae00fd037ae81b19a41aa17b52210811cd8641f1a0463b21d9439e"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "tcp",
   "title": "plain TCP reverse tunnel; accept_udp adds UDP-over-TCP",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "v4",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 2,
   "name": "tcpmux",
   "title": "SMUX-multiplexed TCP (mux_con/mux_version/framesize/buffers)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "v4",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 3,
   "name": "ws",
   "title": "HTTP-upgrade websocket carrier; edge_ip CDN dial supported",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "v4",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 4,
   "name": "wss",
   "title": "websocket over TLS; tls_cert/tls_key mandatory server-side",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "v4",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 5,
   "name": "wsmux",
   "title": "SMUX over websocket",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "v4",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 6,
   "name": "wssmux",
   "title": "SMUX over websocket+TLS",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "v4",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 7,
   "name": "udp",
   "title": "native UDP carrier (server binds UDP; client dials)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "v4",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 8,
   "name": "udp-over-tcp",
   "title": "accept_udp=true: UDP sessions encapsulated over the TCP-family carrier",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "v4",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  }
 ]
}
`

const phase46Manifestrathole = `{
 "core": "rathole",
 "upstream": "https://github.com/rapiz1/rathole",
 "license": "Apache-2.0",
 "latest": "0.5.0",
 "versions": [
  {
   "tag": "rathole-v0.5.0",
   "assets": {
    "linux-amd64": {
     "name": "rathole-linux-amd64.tar.gz",
     "sha256": "b8081560756a1b65c40f6b87f718186f34f19e8ecba15522281a1de385401b57"
    },
    "linux-arm64": {
     "name": "rathole-linux-arm64.tar.gz",
     "sha256": "fb40ce69fb91c243a4f1113d5fdfea729ee5e6c36e7028961ad788fd7b28c874"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "tcp-service",
   "title": "reverse TCP forwarding (type=tcp default)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 2,
   "name": "udp-service",
   "title": "reverse UDP forwarding (type=udp per service)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 3,
   "name": "noise",
   "title": "Noise_NK_25519_ChaChaPoly_BLAKE2s encrypted carrier over TCP",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 4,
   "name": "tcp-carrier",
   "title": "plain TCP carrier (transport type tcp)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 5,
   "name": "tls-carrier",
   "title": "TLS carrier - requires operator cert/key on both ends",
   "layer": "L4",
   "transport": "tcp+tls",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 6,
   "name": "websocket-carrier",
   "title": "websocket carrier; tls=false = plain ws, tls=true rides the TLS block",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  }
 ]
}
`

const phase46Manifestudp2raw = `{
 "core": "udp2raw",
 "upstream": "https://github.com/wangyu-/udp2raw",
 "license": "MIT",
 "latest": "20230206.0",
 "versions": [
  {
   "tag": "udp2raw-v20230206.0",
   "assets": {
    "linux-amd64": {
     "name": "udp2raw-linux-amd64.tar.gz",
     "sha256": "a5a371957f9dccbb9de7d4c280378eba29f257b07412e790aeb3c9a202e9d7f6"
    }
   }
  }
 ],
 "methods": [
  {
   "id": 1,
   "name": "faketcp",
   "title": "UDP disguised as raw TCP SYN/ACK/FIN packets (our companion's mode)",
   "layer": "L4",
   "transport": "raw-ip",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 2,
   "name": "easyfaketcp",
   "title": "faketcp with tcp dummy socket (use_tcp_dummy_socket=1)",
   "layer": "L4",
   "transport": "raw-ip",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 3,
   "name": "udp-raw",
   "title": "raw UDP mode (no disguise, lowest overhead)",
   "layer": "L4",
   "transport": "raw-ip",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  },
  {
   "id": 4,
   "name": "icmp",
   "title": "UDP disguised as ICMP echo (the sealed-carrier shape)",
   "layer": "L4",
   "transport": "icmp",
   "ipver": "both",
   "needs_port": false,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel"
  }
 ]
}
`

const phase46Manifestchisel = `{
 "core": "chisel",
 "upstream": "https://github.com/jpillora/chisel",
 "license": "MIT",
 "latest": "1.10.1",
 "versions": [
  {
   "tag": "chisel-v1.10.1",
   "assets": {
    "linux-amd64": {
     "name": "chisel-linux-amd64.tar.gz",
     "sha256": "10a463a8caf47ea88707f72dd06eb0863df27770dcbd3bb1f7b120d25d531e5f"
    },
    "linux-arm64": {
     "name": "chisel-linux-arm64.tar.gz",
     "sha256": "9b965a1ebc0829129452ed7e385f46cb83baf33ac8723cf7f1f631e122098931"
    }
   }
  }
 ],
 "methods": [
  {
   "name": "reverse-tcp",
   "title": "reverse TCP port-forward over the chisel HTTP/WS carrier (remote_port binds on the foreign end → the iran-local target)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel",
   "id": 1
  }
 ]
}
`

const phase46Manifestwstunnel = `{
 "core": "wstunnel",
 "upstream": "https://github.com/erebe/wstunnel",
 "license": "BSD-3-Clause",
 "latest": "10.1.9",
 "versions": [
  {
   "tag": "wstunnel-v10.1.9",
   "assets": {
    "linux-amd64": {
     "name": "wstunnel-linux-amd64.tar.gz",
     "sha256": "1834b8338ccdc667db6e0bbc4bd420ea14e1f81f6081f7e17d1218bf003aced2"
    },
    "linux-arm64": {
     "name": "wstunnel-linux-arm64.tar.gz",
     "sha256": "73988185b72f03d6fac75c1e048ee65d8394e5847f972016897f97cccc353837"
    }
   }
  }
 ],
 "methods": [
  {
   "name": "reverse-tcp",
   "title": "reverse TCP port-forward over the WebSocket carrier (remote_port binds on the foreign end → the iran-local target)",
   "layer": "L4",
   "transport": "tcp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel",
   "id": 1
  },
  {
   "name": "reverse-udp",
   "title": "reverse UDP port-forward over the WebSocket carrier (remote_udp_port binds on the foreign end → the iran-local target)",
   "layer": "L4",
   "transport": "udp",
   "ipver": "both",
   "needs_port": true,
   "userspace": true,
   "mikrotik": false,
   "family": "tunnel",
   "id": 2
  }
 ]
}
`

// phase46RealManifestFixture swaps the manifest fetcher for the five REAL
// manifest bodies (every other core 404s) and isolates the package-global
// manifest cache for the test's duration.
func phase46RealManifestFixture(t *testing.T) {
	t.Helper()
	old := coresManifestFetcher
	t.Cleanup(func() { coresManifestFetcher = old })
	coresManifestFetcher = func(core string) ([]byte, error) {
		switch core {
		case "ctunnel":
			return []byte(phase46Manifestctunnel), nil
		case "sing-box":
			return []byte(phase46Manifestsingbox), nil
		case "hysteria":
			return []byte(phase46Manifesthysteria), nil
		case "waterwall":
			return []byte(phase46Manifestwaterwall), nil
		case "easytier":
			return []byte(phase46Manifesteasytier), nil
		case "frp":
			return []byte(phase46Manifestfrp), nil
		case "xray":
			return []byte(phase46Manifestxray), nil
		case "gost":
			return []byte(phase46Manifestgost), nil
		case "backhaul":
			return []byte(phase46Manifestbackhaul), nil
		case "rathole":
			return []byte(phase46Manifestrathole), nil
		case "udp2raw":
			return []byte(phase46Manifestudp2raw), nil
		case "chisel":
			return []byte(phase46Manifestchisel), nil
		case "wstunnel":
			return []byte(phase46Manifestwstunnel), nil
		}
		// matrix and wireguard publish NO core folder — the fetch 404s and
		// their engines ride their engine rows only (honest)
		return nil, &coresManifest404{}
	}
	coresManifestReset(t)
}

// phase46MethodCount is the landed manifest methods[] row count per core —
// the assertion ground truth (matches the repo at 741ba974).
var phase46MethodCount = map[string]int{
	"ctunnel":   49,
	"sing-box":  16,
	"hysteria":  2,
	"waterwall": 18,
	"easytier":  1,
	// CORES-5 (concurrent landing, origin/main 6ebd450): the 8 blocks below
	// mirror the OVERNIGHT-B inventories + driver catalogs — the FULL
	// 13-core methods[] surface the §2.22 feed and the §2.23 board join
	// now serve
	"frp":      16,
	"xray":     17,
	"gost":     37,
	"backhaul": 8,
	"rathole":  6,
	"udp2raw":  4,
	"chisel":   1,
	"wstunnel": 2,
}
