// phase45_engines_test.go — BACKEND-45: the panel-side acceptance of the
// new engines/methods the agent team landed (AGENT-31 + OVERNIGHT-A2/A3).
//   - the manifest-method join decodes the REAL landed manifest shapes
//     (int ids, name-keyed rows, singbox↔sing-box alias, engine-id rows);
//   - the §2.4/§2.5 engines rows carry the hello catalog's params_schema
//     verbatim (the engines-catalog passthrough);
//   - the typed-form templates exist for hysteria/singbox, are resynced
//     for gost (driver generation 2) and extended for frp/xray — and the
//     secret strip/merge covers their secret fields;
//   - compileDesiredV2 accepts the new families' params (hostile matrix
//     fails safe panel-side; happy paths compile VERBATIM).
//
// The sing-box/hysteria manifest fixtures mirror the landed cores repo
// MANIFEST.json rows (id = JSON number, name = the driver-consumable
// spelling) — quantum-cores read 2026-11-22.

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// phase45ManifestFixture swaps the manifest fetcher for the REAL landed
// shapes (int ids + names) and returns nothing — restored via t.Cleanup.
// The bodies are trimmed copies of the actual quantum-cores manifests:
// sing-box (16 rows, first/last shown by count), hysteria (2), ctunnel
// (3-row slice of the 49) — counts and id/name types are the assertion
// surface, not invented shapes.
func phase45ManifestFixture(t *testing.T) {
	t.Helper()
	old := coresManifestFetcher
	t.Cleanup(func() { coresManifestFetcher = old })
	coresManifestFetcher = func(core string) ([]byte, error) {
		switch core {
		case "sing-box":
			return []byte(`{"methods":[
				{"id":1,"name":"vless","title":"VLESS","layer":"L4","transport":"tcp","ipver":"both","family":"proxy","overhead":26,"needs_port":true,"userspace":true,"mikrotik":false},
				{"id":2,"name":"vmess","layer":"L4","transport":"tcp","ipver":"both"},
				{"id":3,"name":"trojan","layer":"L4","transport":"tcp+tls","ipver":"both"},
				{"id":4,"name":"shadowsocks","layer":"L4","transport":"tcp","ipver":"both"},
				{"id":5,"name":"hysteria2","layer":"L4","transport":"udp","ipver":"both"},
				{"id":6,"name":"hysteria","layer":"L4","transport":"udp","ipver":"both"},
				{"id":7,"name":"tuic","layer":"L4","transport":"udp","ipver":"both"},
				{"id":8,"name":"anytls","layer":"L4","transport":"tcp+tls","ipver":"both"},
				{"id":9,"name":"shadowtls","layer":"L4","transport":"tcp+tls","ipver":"both"},
				{"id":10,"name":"ssh","layer":"L4","transport":"tcp","ipver":"both"},
				{"id":11,"name":"snell","layer":"L4","transport":"tcp+tls","ipver":"both"},
				{"id":12,"name":"socks-http","layer":"L4","transport":"tcp","ipver":"both"},
				{"id":13,"name":"wireguard","layer":"L3","transport":"udp","ipver":"both"},
				{"id":14,"name":"openvpn","layer":"L3","transport":"udp","ipver":"both"},
				{"id":15,"name":"tun","layer":"L3","transport":"any","ipver":"both"},
				{"id":16,"name":"tailscale","layer":"L3","transport":"udp","ipver":"both"}]}`), nil
		case "hysteria":
			return []byte(`{"methods":[
				{"id":1,"name":"hysteria2","layer":"L4","transport":"udp","ipver":"both"},
				{"id":2,"name":"salamander","layer":"L4","transport":"udp","ipver":"both"}]}`), nil
		case "ctunnel":
			return []byte(`{"methods":[
				{"id":1,"name":"6to4","layer":"L3","transport":"raw-ip","ipver":"v4"},
				{"id":2,"name":"6to4-gre","layer":"L3","transport":"raw-ip","ipver":"v4"},
				{"id":3,"name":"sit","layer":"L3","transport":"raw-ip","ipver":"v4"}]}`), nil
		case "gost":
			// the gost/frp/xray cores publish NO methods[] (their method
			// families are driver params, not manifest rows) — honest null
			return []byte(`{"upstream":"https://github.com/go-gost/gost"}`), nil
		}
		return nil, http.ErrBodyReadAfterClose
	}
}

func TestPhase45ManifestJoinRealShapes(t *testing.T) {
	phase45ManifestFixture(t)
	// snapshot + restore the package-global manifest cache — this test
	// fills it with fixture entries that must never leak into later tests
	coresManifestMu.Lock()
	oldCache := coresManifestCache
	oldBusy := coresManifestBusy
	coresManifestCache = map[string]*coresManifestCacheEntry{}
	coresManifestBusy = map[string]bool{}
	coresManifestMu.Unlock()
	t.Cleanup(func() {
		coresManifestMu.Lock()
		coresManifestCache = oldCache
		coresManifestBusy = oldBusy
		coresManifestMu.Unlock()
	})
	// the manifest read is background-filled (a GET never serializes behind
	// a possibly dead raw request) — the seam is in-memory, so the fill
	// lands within the poll window
	waitManifest := func(core string) []methodManifestEntry {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if rows := coresManifestMethods(core); len(rows) > 0 {
				return rows
			}
			time.Sleep(2 * time.Millisecond)
		}
		return nil
	}

	// int-id manifests decode; the method id is the NAME (the
	// driver-consumable spelling — ctunnel's ctMethods keys are names)
	ct := waitManifest("ctunnel")
	if len(ct) != 3 || ct[0].ID != "6to4" || ct[1].ID != "6to4-gre" || ct[2].ID != "sit" {
		t.Fatalf("ctunnel name-keyed rows: %+v", ct)
	}
	if ct[0].Engine != "ctunnel" || ct[0].Layer != "L3" || ct[0].Transport != "raw-ip" || ct[0].IPVer != "v4" {
		t.Fatalf("ctunnel identity columns: %+v", ct[0])
	}
	sb := waitManifest("sing-box")
	if len(sb) != 16 {
		t.Fatalf("sing-box rows: got %d, want 16", len(sb))
	}
	if sb[0].ID != "vless" || sb[4].ID != "hysteria2" || sb[15].ID != "tailscale" {
		t.Fatalf("sing-box name-keyed rows: %+v", sb)
	}
	// the row's Engine is the ENGINE id the agents report (the alias), not
	// the repo folder name — a probe tunnel for "sing-box" could never run
	if sb[0].Engine != "singbox" {
		t.Fatalf("sing-box rows must ride engine \"singbox\": %q", sb[0].Engine)
	}
	hys := waitManifest("hysteria")
	if len(hys) != 2 || hys[0].ID != "hysteria2" || hys[0].Engine != "hysteria" {
		t.Fatalf("hysteria rows: %+v", hys)
	}
	// a core without methods[] yields no rows (honest null passthrough)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e := fetchCoresManifestCached("gost"); e != nil && e.Err == nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if rows := coresManifestMethods("gost"); len(rows) != 0 {
		t.Fatalf("gost has no methods[]: %+v", rows)
	}

	// the board join's wired seam resolves the alias: engine id "singbox"
	// reads the sing-box manifest
	wireCoresManifestJoin()
	rows := mboardManifestMethodsFn("singbox")
	if len(rows) != 16 || rows[0].Engine != "singbox" {
		t.Fatalf("wired join for engine singbox: %d rows, first %+v", len(rows), rows[0])
	}
	// and the reverse lookup finds a manifest method by NAME
	if e, ok := mboardManifestEntryFn("vless"); !ok || e.Engine != "singbox" {
		t.Fatalf("reverse lookup vless: %+v %v", e, ok)
	}
}

func TestPhase45SingBoxInstalledVersionAlias(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	if err := s.UpsertAgent(store.Agent{ID: "agt_0a0a0a0a0a0a", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: "agt_0a0a0a0a0a0a",
		Engine: "singbox", Installed: true, Version: "1.14.4", UpdatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	// the §2.22 row is the CORE name ("sing-box") — the agents' "singbox"
	// reports must resolve through the alias
	installed, outdated := coreRowInstalledOutdated(s, "sing-box", "1.14.5")
	if installed != "1.14.4" || outdated == nil || !*outdated {
		t.Fatalf("sing-box installed/outdated: %q %v", installed, outdated)
	}
	installed, _ = coreRowInstalledOutdated(s, "sing-box", "1.14.4")
	if installed != "1.14.4" {
		t.Fatalf("installed lost: %q", installed)
	}
}

func TestPhase45TemplatesNewEngines(t *testing.T) {
	// every new/resynced template entry carries both labels and the exact
	// driver-facing keys
	hys, ok := engineParamTemplatesFor("hysteria")
	if !ok || len(hys) == 0 {
		t.Fatalf("no hysteria template")
	}
	wantHysteria := map[string]string{
		"server_side": "string", "version": "string", "artifact": "string",
		"server_addr": "string", "auth_password": "secret", "obfs_password": "secret",
		"tls_cert": "string", "tls_key": "string", "sni": "string",
		"insecure": "bool", "pin_sha256": "string", "up_mbps": "string",
		"down_mbps": "string", "socks_port": "number", "http_port": "number",
	}
	phase45AssertTemplate(t, "hysteria", hys, wantHysteria)

	sb, ok := engineParamTemplatesFor("singbox")
	if !ok || len(sb) == 0 {
		t.Fatalf("no singbox template")
	}
	wantSingbox := map[string]string{
		"server_side": "string", "version": "string", "artifact": "string",
		"server_addr": "string", "protocol": "string", "uuid": "secret",
		"password": "secret", "ss_method": "string", "ss_password": "secret",
		"up_mbps": "number", "down_mbps": "number", "congestion": "string",
		"tls_cert": "string", "tls_key": "string", "sni": "string",
		"insecure": "bool", "local_mode": "string", "local_port": "number",
		"forward_addr": "string", "obfs": "string", "shadowtls_handshake": "string",
		"shadowtls_version": "number", "snell_version": "number",
		"tor_exec_path": "string", "naive_username": "string",
		"tailscale_auth_key": "secret", "tailscale_state_dir": "string",
		"tailscale_hostname": "string", "tailscale_ephemeral": "bool",
		"tailscale_exit_node": "string",
	}
	phase45AssertTemplate(t, "singbox", sb, wantSingbox)

	gost, ok := engineParamTemplatesFor("gost")
	if !ok {
		t.Fatalf("no gost template")
	}
	// resynced to the driver generation: the OLD pre-driver keys are gone,
	// the OVERNIGHT-A2/A3 fields present
	wantGost := map[string]string{
		"carrier_addr": "string", "public_port": "number", "local_addr": "string",
		"transport": "string", "mode": "string", "tls_cert": "string",
		"tls_key": "string", "ssh_user": "string", "ssh_password": "secret",
		"ss_method": "string", "ss_password": "secret", "utls_fingerprint": "string",
		"ingress": "string", "service": "string", "tap_net": "string",
		"tap_net_server": "string", "tap_name": "string", "tap_mtu": "number",
		"tap_gw": "string", "tap_route": "string", "tap_route_server": "string",
		"redirect_iface": "string", "masque_user": "string", "masque_password": "secret",
	}
	phase45AssertTemplate(t, "gost", gost, wantGost)
	for _, dead := range []string{"remote_addr", "server_port", "carrier", "userpass", "local_port"} {
		for _, f := range gost {
			if f.JSON == dead {
				t.Fatalf("gost template still carries the pre-driver key %q", dead)
			}
		}
	}

	frp, ok := engineParamTemplatesFor("frp")
	if !ok {
		t.Fatalf("no frp template")
	}
	phase45AssertTemplate(t, "frp", frp, map[string]string{
		"proxy_type": "string", "transport": "string", "use_encryption": "bool",
		"use_compression": "bool", "secret_key": "secret",
		"vhost_http_port": "number", "vhost_https_port": "number",
		"tcpmux_http_connect_port": "number", "tcpmux_passthrough": "bool",
		"sub_domain_host": "string", "subdomain": "string",
		"host_header_rewrite": "string", "http_user": "string",
		"http_password": "secret",
	})

	xr, ok := engineParamTemplatesFor("xray")
	if !ok {
		t.Fatalf("no xray template")
	}
	phase45AssertTemplate(t, "xray", xr, map[string]string{
		"socks_port": "number", "http_port": "number",
	})
}

func phase45AssertTemplate(t *testing.T, engine string, fields []engineParamField, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, f := range fields {
		if f.LabelEn == "" || f.LabelFa == "" {
			t.Fatalf("%s field %s missing a label", engine, f.JSON)
		}
		got[f.JSON] = f.Kind
	}
	for k, kind := range want {
		if got[k] != kind {
			t.Fatalf("%s field %q: kind %q, want %q", engine, k, got[k], kind)
		}
	}
}

func TestPhase45SecretStripMergeNewEngines(t *testing.T) {
	// GET responses mask every secret field; updates keep stored values
	cases := []struct{ engine, params, secret string }{
		{"hysteria", `{"server_side":"f","auth_password":"hysteria-pw","obfs_password":"obfs-pw","socks_port":1080}`, "hysteria-pw"},
		{"hysteria", `{"obfs_password":"obfs-pw"}`, "obfs-pw"},
		{"singbox", `{"protocol":"vless","uuid":"9c4f1e2a-1111-2222-3333-444455556666","password":"sb-pw","ss_password":"inner-pw","tailscale_auth_key":"tsk-xyz"}`, "9c4f1e2a"},
		{"singbox", `{"tailscale_auth_key":"tsk-xyz"}`, "tsk-xyz"},
		{"gost", `{"transport":"masque","masque_password":"mq-pw","ss_password":"ss-pw","ssh_password":"ssh-pw"}`, "mq-pw"},
		{"frp", `{"proxy_type":"stcp","secret_key":"stcp-secret","http_password":"http-pw"}`, "stcp-secret"},
	}
	for _, c := range cases {
		got := string(stripEngineSecrets(c.engine, c.params))
		if strings.Contains(got, c.secret) {
			t.Fatalf("%s: secret %q leaked in GET: %s", c.engine, c.secret, got)
		}
		if !strings.Contains(got, `"__set":true`) {
			t.Fatalf("%s: secret not marked set: %s", c.engine, got)
		}
	}
	// the masked-empty merge keeps the stored value
	stored := `{"server_side":"f","auth_password":"kept-pw","obfs_password":"kept-obfs","socks_port":1080}`
	merged := string(mergeEngineSecrets("hysteria", stored, `{"server_side":"f","auth_password":"","obfs_password":"","socks_port":1081}`))
	if strings.Contains(merged, "kept-pw") == false || !strings.Contains(merged, `"socks_port":1081`) {
		t.Fatalf("hysteria merge lost stored secret or non-secret update: %s", merged)
	}
	if !strings.Contains(merged, "kept-obfs") {
		t.Fatalf("hysteria merge lost obfs secret: %s", merged)
	}
	// a non-empty new secret replaces
	merged = string(mergeEngineSecrets("hysteria", stored, `{"auth_password":"new-pw"}`))
	if !strings.Contains(merged, "new-pw") {
		t.Fatalf("new secret not applied: %s", merged)
	}
}

func TestPhase45ParamsSchemaPassthrough(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	agentID := "agt_0a0a0a0a0a0a"
	if err := s.UpsertAgent(store.Agent{ID: agentID, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	hysteriaSchema := `{"server_side":"","version":"","artifact":"..{arch}..","server_addr":"0.0.0.0:36712","auth_password":"","obfs_password":"","up_mbps":"100 mbps","tcp_forwards":[{"listen":0,"remote":"host:port"}]}`
	singboxSchema := `{"server_side":"","protocol":"shadowsocks|vless|vmess|trojan|hysteria2|hysteria|tuic|anytls|shadowtls|snell|naive|tor|tailscale","uuid":"","password":"","local_mode":"proxy|forward"}`
	catalog := `[{"id":"hysteria","version":"2.14.0","params_schema":` + hysteriaSchema + `},` +
		`{"id":"singbox","version":"1.14.4","params_schema":` + singboxSchema + `},` +
		`{"id":"matrix"}]`
	if err := s.SetAgentV2Session(agentID, 2, catalog); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerUIAPIRoutes(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := meshSession(t)
	resp, err := client.Get(srv.URL + "/api/ui/agents/" + agentID + "/engines")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Engines []struct {
			Engine       string          `json:"engine"`
			ParamsSchema json.RawMessage `json:"params_schema"`
		} `json:"engines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	byEngine := map[string]json.RawMessage{}
	for _, e := range body.Engines {
		byEngine[e.Engine] = e.ParamsSchema
	}
	// the agent's OWN schema rides the row verbatim (the UI builds the
	// wizard fields against exactly these)
	if string(byEngine["hysteria"]) != hysteriaSchema {
		t.Fatalf("hysteria params_schema not verbatim: %s", byEngine["hysteria"])
	}
	if string(byEngine["singbox"]) != singboxSchema {
		t.Fatalf("singbox params_schema not verbatim: %s", byEngine["singbox"])
	}
	// engines without a published schema carry null — never a guessed shape
	if got := byEngine["matrix"]; got != nil && string(got) != "null" {
		t.Fatalf("matrix params_schema must be null: %s", got)
	}
	// the hysteria/singbox rows exist even with NO heartbeat rows — the
	// known-engines set + the catalog union
	if _, ok := byEngine["hysteria"]; !ok {
		t.Fatalf("hysteria row missing")
	}
}

// phase45CompileFixture: one linked agent over a topology with a peer —
// the compile reads the store; no agent session is needed for the specs.
func phase45CompileFixture(t *testing.T) (*store.Store, int64, int64, int64) {
	t.Helper()
	withTempConfig(t)
	s := withMeshStore(t)
	linkedServer, err := s.UpsertServer(store.Server{Role: "iran", Name: "p45-ir"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := s.UpsertServer(store.Server{Role: "foreign", Name: "p45-fr"})
	if err != nil {
		t.Fatal(err)
	}
	topoID, err := s.UpsertTopology(store.Topology{Name: "p45-topo", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, linkedServer); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, peer); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgent(store.Agent{ID: "agt_0a0a0a0a0a0a", ServerID: linkedServer,
		Name: "p45-ir", Role: "iran", Status: "online", Arch: "amd64", OS: "linux",
		LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	return s, linkedServer, peer, topoID
}

func TestPhase45CompileDesiredNewEnginesVerbatim(t *testing.T) {
	s, sv, peer, topoID := phase45CompileFixture(t)
	ag, err := s.GetAgent("agt_0a0a0a0a0a0a")
	if err != nil {
		t.Fatal(err)
	}
	// the EXACT happy-path shapes the drivers expect (one per new
	// family — the A2/A3 additions included)
	happy := []struct{ engine, params string }{
		{"hysteria", `{"server_side":"p45-fr","version":"2.14.0","artifact":"hysteria-2.14.0-linux-{arch}.tar.gz","server_addr":"0.0.0.0:36712","auth_password":"hysteria-pw","obfs_password":"salamander-pw","tls_cert":"/etc/hyst/cert.pem","tls_key":"/etc/hyst/key.pem","up_mbps":"100 mbps","down_mbps":"500 mbps","socks_port":1080,"http_port":8080,"tcp_forwards":[{"listen":8443,"remote":"127.0.0.1:22"}],"udp_forwards":[{"listen":5353,"remote":"127.0.0.1:53"}]}`},
		{"singbox", `{"server_side":"p45-fr","version":"1.14.4","artifact":"sing-box-1.14.4-linux-{arch}.tar.gz","server_addr":"0.0.0.0:8388","protocol":"shadowtls","password":"stls-pw","ss_method":"aes-256-gcm","ss_password":"inner-pw","shadowtls_handshake":"www.apple.com:443","shadowtls_version":3,"local_mode":"proxy","local_port":10808}`},
		{"gost", `{"server_side":"p45-fr","version":"3.3.3","artifact":"gost-3.3.3-linux-{arch}.tar.gz","carrier_addr":"0.0.0.0:23344","public_port":23345,"local_addr":"127.0.0.1:5201","transport":"ws","mode":"tcp","service":"tap","tap_net":"10.66.0.1/24","tap_net_server":"10.66.0.2/24","tap_mtu":1420,"redirect_iface":"eth0","masque_user":"u","masque_password":"mq-pw"}`},
		{"frp", `{"server_side":"p45-fr","version":"0.61.1","artifact":"frp-0.61.1-linux-{arch}.tar.gz","server_addr":"p45-fr","server_port":7000,"token":"frp-token","proxy_type":"http","vhost_http_port":8080,"sub_domain_host":"example.com","subdomain":"app","locations":["/api"],"http_user":"u","http_password":"http-pw"}`},
		{"xray", `{"server_side":"p45-fr","version":"24.12.31","artifact":"xray-24.12.31-linux-{arch}.tar.gz","remote_addr":"p45-fr","carrier_port":443,"public_port":8443,"local_addr":"127.0.0.1","local_port":5201,"uuid":"9c4f1e2a-1111-2222-3333-444455556666","socks_port":10808,"http_port":10809}`},
	}
	for i, hc := range happy {
		key := "p45-ok-" + hc.engine
		id, err := s.UpsertTunnel(store.Tunnel{TunnelKey: key, TopologyID: topoID,
			LocalServerID: sv, RemoteServerID: peer, Engine: hc.engine,
			EngineParams: hc.params, Enabled: true})
		if err != nil {
			t.Fatalf("%s: store: %v", hc.engine, err)
		}
		desired, skip, warnings, err := compileDesiredV2(s, ag)
		if err != nil || skip != "" {
			t.Fatalf("%s: compile: err=%v skip=%q", hc.engine, err, skip)
		}
		if len(warnings) != 0 {
			t.Fatalf("%s: unexpected warnings %v", hc.engine, warnings)
		}
		found := false
		for _, spec := range desired.Tunnels {
			if spec.ID == key {
				found = true
				// no artifacts seeded → no stamping → the params compile
				// VERBATIM (the agent's driver consumes exactly these)
				if string(spec.EngineParams) != hc.params {
					t.Fatalf("%s: params not verbatim:\n got %s\nwant %s", hc.engine, spec.EngineParams, hc.params)
				}
				if !spec.Enabled {
					t.Fatalf("%s: spec disabled", hc.engine)
				}
			}
		}
		if !found {
			t.Fatalf("%s: spec missing from the compile", hc.engine)
		}
		// the engine rides the desired engine list, enabled (in use)
		engFound := false
		for _, e := range desired.Engines {
			if e.ID == hc.engine {
				engFound = true
				if !e.Enabled {
					t.Fatalf("%s: engine not enabled in the desired list", hc.engine)
				}
			}
		}
		if !engFound {
			t.Fatalf("%s: engine missing from the desired list", hc.engine)
		}
		if err := s.DeleteTunnel(id); err != nil {
			t.Fatalf("%s: cleanup: %v", hc.engine, err)
		}
		_ = i
	}
}

func TestPhase45CompileDesiredNewEnginesHostile(t *testing.T) {
	s, sv, peer, topoID := phase45CompileFixture(t)
	ag, _ := s.GetAgent("agt_0a0a0a0a0a0a")

	// wrong types / unknown fields / hostile junk: the panel ACCEPTS and
	// compiles VERBATIM — fail-safe happens at the agent's typed Validate
	// (never a panel-side reinterpretation)
	hostile := []struct{ engine, params, name string }{
		{"hysteria", `{"socks_port":"not-a-number","tcp_forwards":"not-an-array","up_mbps":100}`, "wrong types"},
		{"singbox", `{"protocol":42,"tailscale_ephemeral":"yes","shadowtls_version":-5,"unknown_future_field":{"deep":[1,2]}}`, "wrong types + unknown field"},
		{"gost", `{"service":"DROP TABLE tunnels","tap_mtu":-1,"redirect_iface":"eth0; rm -rf /","masque_password":null}`, "injection strings"},
		{"frp", `{"proxy_type":"<script>alert(1)</script>","vhost_http_port":-8080,"custom_domains":"not-an-array"}`, "xss strings"},
		{"xray", `{"socks_port":99999999,"uuid":{"nested":"object"}}`, "boundary + wrong type"},
	}
	for _, hc := range hostile {
		key := "p45-hostile-" + hc.name
		id, err := s.UpsertTunnel(store.Tunnel{TunnelKey: key, TopologyID: topoID,
			LocalServerID: sv, RemoteServerID: peer, Engine: hc.engine,
			EngineParams: hc.params, Enabled: true})
		if err != nil {
			t.Fatalf("%s: store accepted-hostile: %v", hc.name, err)
		}
		if _, _, _, cerr := compileDesiredV2(s, ag); cerr != nil {
			t.Fatalf("%s: compile must accept engine_params verbatim: %v", hc.name, cerr)
		}
		s.DeleteTunnel(id)
	}

	// the handler-level rejections (E-IN-06 family)
	mux := http.NewServeMux()
	registerMeshRoutes(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := meshSession(t)
	post := func(body string) int {
		resp, err := client.Post(srv.URL+"/api/mesh/tunnels", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	// oversized params (>16 KiB) rejected
	big := `{"topology_id":1,"local_server_id":` + itoa64(sv) + `,"remote_server_id":` + itoa64(sv) + `,"tunnel_key":"p45-big","engine":"hysteria","enabled":false,"engine_params":{"pad":"` + strings.Repeat("x", 17<<10) + `"}}`
	if code := post(big); code == 200 {
		t.Fatalf("oversized engine_params accepted")
	}
	// invalid JSON params rejected
	bad := `{"topology_id":1,"local_server_id":` + itoa64(sv) + `,"remote_server_id":` + itoa64(sv) + `,"tunnel_key":"p45-badjson","engine":"hysteria","enabled":false,"engine_params":{"auth_password":}}`
	if code := post(bad); code == 200 {
		t.Fatalf("invalid JSON engine_params accepted")
	}
	// hostile engine id rejected
	badEng := `{"topology_id":1,"local_server_id":` + itoa64(sv) + `,"remote_server_id":` + itoa64(sv) + `,"tunnel_key":"p45-badeng","engine":"hysteria; rm -rf /","enabled":false}`
	if code := post(badEng); code == 200 {
		t.Fatalf("hostile engine id accepted")
	}
	// a catalog EXISTS and lacks the engine → hard refusal; the catalog
	// carrying it → accepted (the catalog-driven acceptance)
	if err := s.SetAgentV2Session("agt_0a0a0a0a0a0a", 2, `[{"id":"matrix"}]`); err != nil {
		t.Fatal(err)
	}
	noCatalog := `{"topology_id":1,"local_server_id":` + itoa64(sv) + `,"remote_server_id":` + itoa64(sv) + `,"tunnel_key":"p45-nocat","engine":"hysteria","enabled":false}`
	if code := post(noCatalog); code == 200 {
		t.Fatalf("engine outside the reported catalog accepted")
	}
	if err := s.SetAgentV2Session("agt_0a0a0a0a0a0a", 2, `[{"id":"hysteria","version":"2.14.0","params_schema":{"server_addr":"0.0.0.0:36712"}}]`); err != nil {
		t.Fatal(err)
	}
	inCatalog := `{"topology_id":1,"local_server_id":` + itoa64(sv) + `,"remote_server_id":` + itoa64(sv) + `,"tunnel_key":"p45-incat","engine":"hysteria","enabled":false,"engine_params":{"server_side":"p45-fr","server_addr":"0.0.0.0:36712","auth_password":"pw"}}`
	if code := post(inCatalog); code != 200 {
		t.Fatalf("catalog-reported hysteria refused: %d", code)
	}
	// wrong-typed values ride the acceptance verbatim (agent-side fail-safe)
	wrongType := `{"topology_id":1,"local_server_id":` + itoa64(sv) + `,"remote_server_id":` + itoa64(sv) + `,"tunnel_key":"p45-wrongtype","engine":"hysteria","enabled":false,"engine_params":{"socks_port":"abc","unknown_field":[1,2,3]}}`
	if code := post(wrongType); code != 200 {
		t.Fatalf("wrong-typed params must be accepted verbatim (agent validates): %d", code)
	}
}

func TestPhase45MethodBoardNewEnginesRun(t *testing.T) {
	// TASK-3 hermetic: the board enumerates + probes the NEW engines and
	// their manifest methods (the join with the REAL landed shapes).
	st, ctl := mboardProbeFixture(t)
	ctl.arming = true
	ctl.armDelay = 40 * time.Millisecond
	ctl.latencyDelay = 20 * time.Millisecond
	ctl.autoSample = true

	// the manifest seam: two sing-box rows — vless (implemented by the
	// driver) and openvpn (PUBLISHED by the core manifest but not in the
	// driver's exposed set — the board must still enumerate it and probe
	// it honestly)
	oldMethods, oldEntry := mboardManifestMethodsFn, mboardManifestEntryFn
	t.Cleanup(func() { mboardManifestMethodsFn, mboardManifestEntryFn = oldMethods, oldEntry })
	mboardManifestMethodsFn = func(core string) []methodManifestEntry {
		if core == "singbox" {
			return []methodManifestEntry{
				{ID: "vless", Engine: "singbox", Layer: "L4", Transport: "tcp", IPVer: "both"},
				{ID: "openvpn", Engine: "singbox", Layer: "L3", Transport: "udp", IPVer: "both"},
			}
		}
		return nil
	}
	mboardManifestEntryFn = func(method string) (methodManifestEntry, bool) {
		for _, m := range mboardManifestMethodsFn("singbox") {
			if m.ID == method {
				return m, true
			}
		}
		return methodManifestEntry{}, false
	}

	// enumeration: the new engines ride the known set; the manifest methods
	// append (the engines themselves come first, so a manifest method named
	// like an engine never shadows it)
	cands := methodBoardCandidates([]string{"hysteria", "singbox"}, []string{"hysteria", "singbox"})
	known := map[string]bool{}
	for _, e := range uiKnownEngines {
		known[e] = true
	}
	var extra []string
	for _, c := range cands {
		if !known[c] {
			extra = append(extra, c)
		}
	}
	if strings.Join(extra, ",") != "vless,openvpn" {
		t.Fatalf("manifest join candidates: %v", extra)
	}

	// the hysteria ENGINE row renders the grounded metadata (methodCatalog)
	st.probeOne("hysteria")
	if len(st.rows) != 1 || st.rows[0].Engine != "hysteria" {
		t.Fatalf("hysteria rows: %+v", st.rows)
	}
	r := st.rows[0]
	if r.Reachability != "ok" || r.Layer != "l4" || r.Transport != "udp" || r.IPVer != "v4+v6" {
		t.Fatalf("hysteria row: %+v", r)
	}
	if r.Score == nil || r.Score.(int) <= 0 {
		t.Fatalf("hysteria measured row must score: %+v", r)
	}

	// the vless MANIFEST row rides engine "singbox" with the manifest's
	// own columns (layer lowercased, transport/ipver verbatim)
	st.probeOne("vless")
	if len(st.rows) != 2 {
		t.Fatalf("rows after vless: %d", len(st.rows))
	}
	r = st.rows[1]
	if r.Engine != "singbox" || r.Method != "vless" || r.Layer != "l4" || r.Transport != "tcp" || r.IPVer != "both" {
		t.Fatalf("vless manifest row: %+v", r)
	}
	if r.Reachability != "ok" || r.Score == nil {
		t.Fatalf("vless row: %+v", r)
	}

	// a protocol the driver does NOT expose probes honestly: converge
	// reaches no verdict → timeout row, score 0, tunnel torn down
	ctl.arming = false
	st.probeOne("openvpn")
	if len(st.rows) != 3 {
		t.Fatalf("rows after openvpn: %d", len(st.rows))
	}
	r = st.rows[2]
	if r.Engine != "singbox" || r.Method != "openvpn" || r.Reachability != "converge-timeout" || r.Score.(int) != 0 {
		t.Fatalf("openvpn honest row: %+v", r)
	}
	tuns, err := st.store.ListTunnels(st.topoID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tn := range tuns {
		if strings.HasPrefix(tn.TunnelKey, "mprobe-") {
			t.Fatalf("probe tunnel %s was not torn down", tn.TunnelKey)
		}
	}
}
