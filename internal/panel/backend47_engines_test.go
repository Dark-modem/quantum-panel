// backend47_engines_test.go — BACKEND-47 acceptance: the panel side is
// resynced to the agent's OVERNIGHT-A6 driver surface (WaterWall incl. the
// reverse chain, hysteria gecko/udphop, sing-box L3/transparent ingress)
// and the naive core's own release-tag shape.
//
//   - the typed-form templates cover every driver-consumable field of the
//     new/resynced engines (waterwall NEW; hysteria/singbox extended; frp
//     resynced to server_addr/dashboard_port/allow_port_*);
//   - secret strip/merge covers the WaterWall secrets and the new
//     hysteria/singbox fields;
//   - compileDesiredV2 accepts the new params VERBATIM (happy) and never
//     reinterprets hostile engine_params (the agent's typed Validate is
//     the fail-safe);
//   - the naive tag contract `naive-v<a.b.c.d>-<build>` is accepted while
//     EVERY other core keeps the historical coresTagRe.

package panel

import (
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestBackend47Templates(t *testing.T) {
	// WaterWall was absent from the fallback table entirely: a wizard form
	// could not render it and secret masking never covered its keys.
	ww, ok := engineParamTemplatesFor("waterwall")
	if !ok {
		t.Fatal("no waterwall template")
	}
	phase45AssertTemplate(t, "waterwall", ww, map[string]string{
		"server_side": "string", "version": "string", "artifact": "string",
		"sha256_amd64": "secret", "sha256_arm64": "secret",
		"signature_amd64": "secret", "signature_arm64": "secret",
		"method": "string", "server_addr": "string", "local_addr": "string",
		"local_port": "number", "auth_user": "string", "auth_password": "secret",
		"uuid": "secret", "tls_cert": "string", "tls_key": "string", "sni": "string",
		"wg_private_key": "secret", "wg_auth_secret": "secret",
		"obfs_method": "string", "obfs_key": "number", "send_mtu": "number",
		"kcp_fec": "bool", "rtmp_app": "string", "rtmp_stream": "string",
		"no_auth": "bool", "fake_tcp_port": "number", "ww_insecure": "bool",
		"reverse_port": "number",
	})
	// the 18-method allowlist + the reverse chain land in the hint verbatim
	var methodField *engineParamField
	for i := range ww {
		if ww[i].JSON == "method" {
			methodField = &ww[i]
		}
	}
	if methodField == nil {
		t.Fatal("waterwall method field missing")
	}
	for _, m := range []string{"quic", "kcp", "enet", "reality", "trojan", "vless",
		"vmess", "wireguard", "rtmp", "gns", "bgp4", "http", "socks5", "fake-tcp",
		"obfuscator", "connection-fisher", "connection-stripe", "mux", "reverse"} {
		if !strings.Contains(methodField.Hint, m) {
			t.Fatalf("waterwall method hint missing %q: %s", m, methodField.Hint)
		}
	}

	// hysteria: the gecko/udphop additions
	hys, ok := engineParamTemplatesFor("hysteria")
	if !ok {
		t.Fatal("no hysteria template")
	}
	phase45AssertTemplate(t, "hysteria", hys, map[string]string{
		"obfs_type": "string", "obfs_min_pkt": "number",
		"obfs_max_pkt": "number", "hop_interval": "string",
	})

	// sing-box: the OVERNIGHT-A6 L3/transparent ingress
	sb, ok := engineParamTemplatesFor("singbox")
	if !ok {
		t.Fatal("no singbox template")
	}
	phase45AssertTemplate(t, "singbox", sb, map[string]string{
		"ingress": "string",
	})

	// frp: resynced to the DRIVER's own names (the pre-driver spellings
	// remote_addr/dash_port produced params the driver ignored)
	frp, ok := engineParamTemplatesFor("frp")
	if !ok {
		t.Fatal("no frp template")
	}
	phase45AssertTemplate(t, "frp", frp, map[string]string{
		"server_addr": "string", "server_port": "number",
		"dashboard_port": "number", "allow_port_min": "number",
		"allow_port_max": "number",
	})
	for _, dead := range []string{"remote_addr", "dash_port"} {
		for _, f := range frp {
			if f.JSON == dead {
				t.Fatalf("frp template still carries the pre-driver key %q", dead)
			}
		}
	}
}

func TestBackend47SecretStripMerge(t *testing.T) {
	wwParams := `{"server_side":"fr","method":"wireguard","auth_password":"ww-auth",` +
		`"wg_private_key":"ww-private","wg_auth_secret":"ww-shared",` +
		`"uuid":"9c4f1e2a-1111-2222-3333-444455556666","local_port":4433,` +
		`"sha256_amd64":"` + strings.Repeat("a", 64) + `","obfs_key":90}`
	got := string(stripEngineSecrets("waterwall", wwParams))
	for _, secret := range []string{"ww-auth", "ww-private", "ww-shared", "9c4f1e2a", strings.Repeat("a", 64)} {
		if strings.Contains(got, secret) {
			t.Fatalf("waterwall secret %q leaked in GET: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"method":"wireguard"`) || !strings.Contains(got, `"obfs_key":90`) {
		t.Fatalf("non-secret waterwall fields must survive: %s", got)
	}
	if !strings.Contains(got, `"__set":true`) {
		t.Fatalf("waterwall secrets not marked set: %s", got)
	}
	// masked-empty update keeps every stored secret; a new value replaces
	merged := string(mergeEngineSecrets("waterwall",
		`{"auth_password":"kept-auth","wg_private_key":"kept-priv","wg_auth_secret":"kept-shared","local_port":1}`,
		`{"auth_password":"","wg_private_key":"","wg_auth_secret":"","local_port":2}`))
	for _, kept := range []string{"kept-auth", "kept-priv", "kept-shared"} {
		if !strings.Contains(merged, kept) {
			t.Fatalf("waterwall merge lost %q: %s", kept, merged)
		}
	}
	if !strings.Contains(merged, `"local_port":2`) {
		t.Fatalf("waterwall merge lost the non-secret update: %s", merged)
	}
	if !strings.Contains(string(mergeEngineSecrets("waterwall",
		`{"auth_password":"old"}`, `{"auth_password":"new-auth"}`)), "new-auth") {
		t.Fatal("waterwall replacement secret not applied")
	}

	// hysteria: the new gecko fields are non-secret; the secrets still mask
	hys := string(stripEngineSecrets("hysteria",
		`{"obfs_password":"hys-obfs","obfs_type":"gecko","obfs_min_pkt":64,"hop_interval":"30s"}`))
	if strings.Contains(hys, "hys-obfs") || !strings.Contains(hys, "gecko") || !strings.Contains(hys, `"hop_interval":"30s"`) {
		t.Fatalf("hysteria gecko masking: %s", hys)
	}
	// sing-box: ingress is non-secret and must survive masking
	sb := string(stripEngineSecrets("singbox", `{"password":"sb-pw","ingress":"tproxy"}`))
	if strings.Contains(sb, "sb-pw") || !strings.Contains(sb, `"ingress":"tproxy"`) {
		t.Fatalf("singbox ingress masking: %s", sb)
	}
}

func TestBackend47CompileDesiredVerbatim(t *testing.T) {
	s, sv, peer, topoID := phase45CompileFixture(t)
	ag, err := s.GetAgent("agt_0a0a0a0a0a0a")
	if err != nil {
		t.Fatal(err)
	}
	happy := []struct{ engine, params string }{
		{"waterwall", `{"server_side":"b47-fr","version":"1.6.0","artifact":"waterwall-linux-{arch}.tar.gz","method":"reverse","server_addr":"0.0.0.0:4433","local_addr":"127.0.0.1","local_port":8443,"auth_password":"ww-pw","reverse_port":9000}`},
		{"hysteria", `{"server_side":"b47-fr","version":"2.14.0","artifact":"hysteria-2.14.0-linux-{arch}.tar.gz","server_addr":"0.0.0.0:36712","auth_password":"hys-pw","obfs_type":"gecko","obfs_min_pkt":64,"obfs_max_pkt":1200,"hop_interval":"30s"}`},
		{"singbox", `{"server_side":"b47-fr","version":"1.14.4","artifact":"sing-box-1.14.4-linux-{arch}.tar.gz","server_addr":"0.0.0.0:8388","protocol":"vless","uuid":"9c4f1e2a-1111-2222-3333-444455556666","ingress":"tproxy","local_port":10808}`},
	}
	for _, hc := range happy {
		key := "b47-ok-" + hc.engine
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
			if spec.ID != key {
				continue
			}
			found = true
			if string(spec.EngineParams) != hc.params {
				t.Fatalf("%s: params not verbatim:\n got %s\nwant %s", hc.engine, spec.EngineParams, hc.params)
			}
		}
		if !found {
			t.Fatalf("%s: spec missing from the compile", hc.engine)
		}
		if err := s.DeleteTunnel(id); err != nil {
			t.Fatalf("%s: cleanup: %v", hc.engine, err)
		}
	}

	// hostile shapes ride through untranslated; the agent's typed Validate
	// is the single fail-safe (never a panel-side reinterpretation)
	hostile := []struct{ engine, params string }{
		{"waterwall", `{"method":"reverse; rm -rf /","reverse_port":-5,"auth_password":{"nested":true},"unknown":[1,2]}`},
		{"hysteria", `{"obfs_type":"<script>","obfs_min_pkt":"NaN","hop_interval":{"deep":1}}`},
		{"singbox", `{"ingress":"DROP TABLE","local_port":"eighty","password":null}`},
	}
	for _, hc := range hostile {
		key := "b47-hostile-" + hc.engine
		id, err := s.UpsertTunnel(store.Tunnel{TunnelKey: key, TopologyID: topoID,
			LocalServerID: sv, RemoteServerID: peer, Engine: hc.engine,
			EngineParams: hc.params, Enabled: true})
		if err != nil {
			t.Fatalf("%s: store accepted-hostile: %v", hc.engine, err)
		}
		if _, _, _, cerr := compileDesiredV2(s, ag); cerr != nil {
			t.Fatalf("%s: compile must accept engine_params verbatim: %v", hc.engine, cerr)
		}
		s.DeleteTunnel(id)
	}
}

func TestBackend47NaiveTagShape(t *testing.T) {
	// the naive contract: exactly four numeric segments + a numeric build
	for tag, ok := range map[string]bool{
		"naive-v154.0.8037.49-5": true, // the real published tag
		"naive-v1.0.0.0-1":       true,
		"naive-v1.2.3.4":         false, // no build
		"naive-v1.2.3-1":         false, // only 3 segments
		"naive-v1.2.3.4-":        false, // empty build
		"naive-vA.2.3.4-1":       false, // non-numeric
		"naive-v1.2.3.4-1.2":     false, // non-integer build
		"frp-v1.2.3.4-1":         false, // naive-only shape
		"frp-v0.61.2":            false, // naive regex never accepts other cores
	} {
		if got := coresNaiveTagRe.MatchString(tag); got != ok {
			t.Fatalf("coresNaiveTagRe(%q) = %v, want %v", tag, got, ok)
		}
	}
	// every other core keeps the historical regex unchanged
	if !coresTagRe.MatchString("frp-v0.61.2") || !coresTagRe.MatchString("udp2raw-v20230206.0") {
		t.Fatal("coresTagRe lost its existing shapes")
	}
	if coresTagRe.MatchString("frp-v1.2.3.4-1") {
		t.Fatal("coresTagRe must NOT accept the naive shape for other cores")
	}

	if core, ver, ok := coresTagParts("naive-v154.0.8037.49-5"); !ok || core != "naive" || ver != "154.0.8037.49-5" {
		t.Fatalf("coresTagParts naive: %q %q %v", core, ver, ok)
	}
	if core, ver, ok := coresTagParts("frp-v0.61.2"); !ok || core != "frp" || ver != "0.61.2" {
		t.Fatalf("coresTagParts frp: %q %q %v", core, ver, ok)
	}
	if _, _, ok := coresTagParts("naive-v154.0.8037.49"); ok {
		t.Fatal("malformed naive tag accepted")
	}
}

func TestBackend47NaiveDiscovery(t *testing.T) {
	rel := func(tag string, assets ...ghAsset) ghRelease {
		return ghRelease{Tag: tag, PublishedAt: "2026-11-23T00:00:00Z", Assets: assets}
	}
	asset := func(name string, size int64) ghAsset {
		return ghAsset{Name: name, Size: size,
			BrowserDownloadURL: "https://github.com/Dark-modem/quantum-cores/releases/download/x/" + name}
	}
	rels := []ghRelease{
		rel("naive-v154.0.8037.49-5",
			asset("naive-linux-amd64.tar.gz", 100),
			asset("naive-linux-arm64.tar.gz", 90),
			asset("naive-linux-amd64.tar.gz.sig", 64)),
		rel("naive-v154.0.8037.49-10", asset("naive-linux-amd64.tar.gz", 101)),
		rel("naive-v154.0.8038.1-1", asset("naive-linux-amd64.tar.gz", 102)),
		// malformed naive tags must never shadow the valid ones
		rel("naive-v154.0.8038.1", asset("naive-linux-amd64.tar.gz", 103)),
		rel("naive-v154.0.8038.1-1-extra", asset("naive-linux-amd64.tar.gz", 104)),
		rel("frp-v0.61.2", asset("frp-linux-amd64.tar.gz", 105)),
	}
	rows := discoverCores(rels)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want naive + frp only: %+v", len(rows), rows)
	}
	var naive *coreRow
	for i := range rows {
		if rows[i].Core == "naive" {
			naive = &rows[i]
		}
	}
	if naive == nil {
		t.Fatalf("naive row missing: %+v", rows)
	}
	if naive.LatestTag != "naive-v154.0.8038.1-1" || naive.LatestVersion != "154.0.8038.1-1" {
		t.Fatalf("naive pick wrong: tag=%q version=%q", naive.LatestTag, naive.LatestVersion)
	}
	if len(naive.Assets) != 1 || naive.Assets[0].Name != "naive-linux-amd64.tar.gz" {
		t.Fatalf("naive assets wrong: %+v", naive.Assets)
	}

	// explicit version resolution (with and without the leading v)
	if tag, _, err := pickCoreRelease("naive", "154.0.8037.49-10", rels); err != nil || tag != "naive-v154.0.8037.49-10" {
		t.Fatalf("pickCoreRelease naive explicit: %q %v", tag, err)
	}
	if tag, _, err := pickCoreRelease("naive", "v154.0.8037.49-5", rels); err != nil || tag != "naive-v154.0.8037.49-5" {
		t.Fatalf("pickCoreRelease naive v-prefixed: %q %v", tag, err)
	}
	if tag, _, err := pickCoreRelease("naive", "", rels); err != nil || tag != "naive-v154.0.8038.1-1" {
		t.Fatalf("pickCoreRelease naive latest: %q %v", tag, err)
	}
	if _, _, err := pickCoreRelease("naive", "154.0.8037.49", rels); err == nil {
		t.Fatal("pickCoreRelease accepted a malformed naive version")
	}
	// other cores keep the historical behavior (4-segment tags never resolve)
	if _, _, err := pickCoreRelease("frp", "1.2.3.4", rels); err == nil {
		t.Fatal("frp accepted a 4-segment version")
	}
}

func TestBackend47NaiveVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"154.0.8038.1-1", "154.0.8037.49-10", 1},
		{"154.0.8037.49-10", "154.0.8037.49-5", 1},
		{"154.0.8037.49", "154.0.8037.49-5", -1},
		{"154.0.8037.49-5", "154.0.8037.49-5", 0},
		{"0.61.2", "0.61.1", 1},
		{"not-a-version", "1.2.3", 0},
	}
	for _, c := range cases {
		if got := coresVersionCompare(c.a, c.b); got != c.want {
			t.Fatalf("coresVersionCompare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
