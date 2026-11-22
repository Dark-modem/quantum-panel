package panel

import "testing"

// phase46_real_manifests_test.go — BACKEND-46 fixtures: the REAL landed
// quantum-cores MANIFEST.json bodies (verbatim bytes, read from the repo at
// origin/main 741ba974, 2026-11-23). The five cores that publish methods[]
// are the enumeration ground truth for the §2.23 method-board join and the
// §2.22 passthrough: ctunnel 49 (AGENT-29), sing-box 16 + hysteria 2
// (OVERNIGHT-C/AGENT-31), waterwall 18 (OVERNIGHT-C2/A4), easytier 1
// (OVERNIGHT-D2). Hermetic: served through the coresManifestFetcher seam,
// no network, no sibling-checkout reads at test time.

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
		}
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
}
