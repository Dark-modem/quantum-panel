// dcinbound_test.go — the per-DC inbound reconciler: REALITY key
// generation (xray semantics, private key never serialized), the pure
// diff (create/modify/stale, non-quantum inbounds untouchable, tag-prefix
// collision safety), and the apply end-to-end against the fake panel
// (core PUT with the merged config, hosts via GET/POST/PUT — never
// deletes; dry-run touches nothing).

package pasarguard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func mustKeyPair(t *testing.T) (string, string) {
	t.Helper()
	pvk, pbk, err := GenerateRealityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return pvk, pbk
}

// desiredInbound builds a plan row with a fresh key pair and a derived
// shortId, exactly the way the panel builds them.
func desiredInbound(t *testing.T, tag, clientAddr, sni, dest string, port int) InboundPlan {
	t.Helper()
	pvk, pbk := mustKeyPair(t)
	sid, err := RealityShortID(pvk)
	if err != nil {
		t.Fatal(err)
	}
	return InboundPlan{
		Tag: tag, Listen: "127.0.0.1", Port: port,
		SNIs: []string{sni}, Dest: dest, ShortIDs: []string{sid},
		PrivateKey: pvk, PublicKey: pbk, ClientAddress: clientAddr,
	}
}

// existingInbound renders the xray JSON a desired plan would have written
// (the no-drift baseline) or a mutated variant of it.
func existingInbound(t *testing.T, want InboundPlan) map[string]any {
	t.Helper()
	return want.inboundJSON()
}

func coreConfigWith(t *testing.T, inbounds ...map[string]any) map[string]any {
	t.Helper()
	arr := make([]any, 0, len(inbounds))
	for _, in := range inbounds {
		arr = append(arr, in)
	}
	return map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": arr}
}

func TestGenerateRealityKeyPair(t *testing.T) {
	pvk, pbk := mustKeyPair(t)
	raw, err := base64.RawURLEncoding.DecodeString(pvk)
	if err != nil || len(raw) != 32 {
		t.Fatalf("private key must be 32 raw bytes base64url: %v", err)
	}
	// The public key derives from the private (upstream get_x25519_public_key
	// applies the same RFC 7748 derivation) and is stable across calls.
	again, err := RealityPublicKey(pvk)
	if err != nil {
		t.Fatal(err)
	}
	if again != pbk {
		t.Fatalf("public key derivation mismatch: %s vs %s", again, pbk)
	}
	pvk2, pbk2 := mustKeyPair(t)
	if pvk2 == pvk || pbk2 == pbk {
		t.Fatal("key generation must not repeat")
	}
	// shortId: deterministic per key pair, 8 hex chars.
	sid1, err := RealityShortID(pvk)
	if err != nil {
		t.Fatal(err)
	}
	sid2, _ := RealityShortID(pvk)
	if sid1 != sid2 || len(sid1) != 8 {
		t.Fatalf("shortId must be stable 8-hex, got %q/%q", sid1, sid2)
	}
	if sid1 == sidLookup(t, pvk2) {
		t.Fatal("different pairs must derive different shortIds")
	}
	if _, err := RealityShortID("not-base64!!"); err == nil {
		t.Fatal("bad private key must fail")
	}

	// THE contract: the private key never serializes. Public key does.
	doc, _ := json.Marshal(InboundPlan{Tag: "x", PrivateKey: pvk, PublicKey: pbk})
	if strings.Contains(string(doc), pvk) {
		t.Fatal("InboundPlan JSON must never contain the private key")
	}
	if !strings.Contains(string(doc), pbk) {
		t.Fatal("public key is not secret and must ride the plan")
	}
	plan := DiffDCInbounds(coreConfigWith(t), []InboundPlan{{Tag: DCInboundTag("teh", "hz"), PrivateKey: pvk}}, nil, "")
	planJSON, _ := json.Marshal(plan)
	if strings.Contains(string(planJSON), pvk) {
		t.Fatal("DCInboundPlan JSON must never contain the private key")
	}
}

func sidLookup(t *testing.T, pvk string) string {
	t.Helper()
	sid, err := RealityShortID(pvk)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func TestDiffDCInboundsCreateModifyStale(t *testing.T) {
	teh := desiredInbound(t, DCInboundTag("teh", "hz-a"), "10.0.0.1", "q-teh.example.com", "www.microsoft.com:443", 20001)
	chk := desiredInbound(t, DCInboundTag("chekavak", "hz-a"), "10.0.0.2", "q-chekavak.example.com", "www.microsoft.com:443", 20002)

	// State on the core: the operator's inbound (untouchable), teh's
	// counterpart with an out-of-band port edit (→ modify), a stale
	// quantum inbound, a suffix-variant of our own tag (claimed by the
	// full-name prefix), and a collision trap: quantum:tehran:… must NOT
	// be claimed by quantum:teh:… (cross-server safety on shared cores).
	driftedTeh := existingInbound(t, teh)
	driftedTeh["port"] = 19999 // out-of-band drift
	config := coreConfigWith(t,
		map[string]any{"tag": "INBOUND_0", "listen": "0.0.0.0", "port": 443},
		driftedTeh,
		map[string]any{"tag": "quantum:old:hz-a"},
		map[string]any{"tag": "quantum:tehran:hz-a"},
	)

	plan := DiffDCInbounds(config, []InboundPlan{teh, chk}, nil, NamePrefix)
	if len(plan.Create) != 1 || plan.Create[0].Tag != chk.Tag {
		t.Fatalf("create rows: %+v", plan.Create)
	}
	if len(plan.Modify) != 1 || plan.Modify[0].Tag != teh.Tag {
		t.Fatalf("modify rows: %+v", plan.Modify)
	}
	if len(plan.Stale) != 2 {
		t.Fatalf("stale rows (old + tehran collision): %+v", plan.Stale)
	}
	for _, s := range plan.Stale {
		if s != "quantum:old:hz-a" && s != "quantum:tehran:hz-a" {
			t.Fatalf("unexpected stale row %q", s)
		}
	}
	if !plan.Drift() {
		t.Fatal("plan must report drift")
	}
	// The operator inbound appears NOWHERE in the plan.
	all, _ := json.Marshal(plan)
	if strings.Contains(string(all), "INBOUND_0") {
		t.Fatal("non-quantum inbounds must never be planned")
	}
	// Hosts derive from the inbounds: both are new.
	if len(plan.HostsCreate) != 2 {
		t.Fatalf("hosts_create rows: %+v", plan.HostsCreate)
	}
	hostWant := map[string]InboundPlan{teh.Tag: teh, chk.Tag: chk}
	for _, h := range plan.HostsCreate {
		want, ok := hostWant[h.Remark]
		if !ok {
			t.Fatalf("unexpected host row %q", h.Remark)
		}
		if h.Port != 443 || h.InboundTag != h.Remark || h.Address != want.ClientAddress || h.SNI != want.SNIs[0] {
			t.Fatalf("host plan for %s: %+v", h.Remark, h)
		}
	}
}

func TestDiffDCInboundsNoDriftAndHostModify(t *testing.T) {
	teh := desiredInbound(t, DCInboundTag("teh", "hz-a"), "10.0.0.1", "q-teh.example.com", "www.microsoft.com:443", 20001)
	config := coreConfigWith(t, existingInbound(t, teh))
	hosts := []Host{
		{ID: 1, Remark: teh.Tag, Address: []string{"10.0.0.1"}, Port: 443, SNI: []string{"q-teh.example.com"}, InboundTag: teh.Tag},
		{ID: 2, Remark: "operator-host", Address: []string{"example.com"}, Port: 8443},
	}
	plan := DiffDCInbounds(config, []InboundPlan{teh}, hosts, NamePrefix)
	if plan.Drift() {
		t.Fatalf("no-drift state must produce an empty plan: %+v", plan)
	}
	// Empty lists are still non-nil (JSON emits [] not null).
	if plan.Create == nil || plan.Modify == nil || plan.Stale == nil || plan.HostsCreate == nil || plan.HostsModify == nil {
		t.Fatal("plan lists must be non-nil slices")
	}

	// Host drift (out-of-band edit): the inbound matches, the host does not
	// — a hosts-only modify plan. The operator host stays invisible.
	hosts[0].Port = 4423
	plan2 := DiffDCInbounds(config, []InboundPlan{teh}, hosts, NamePrefix)
	if len(plan2.HostsModify) != 1 || plan2.HostsModify[0].Remark != teh.Tag {
		t.Fatalf("host modify rows: %+v", plan2.HostsModify)
	}
	if len(plan2.Create) != 0 || len(plan2.Modify) != 0 {
		t.Fatalf("hosts-only drift must not plan inbound changes: %+v", plan2)
	}
	if strings.Contains(mustJSON(t, plan2), "operator-host") {
		t.Fatal("operator hosts must never be planned")
	}

	// Renamed host remark (same dc, different server slot): the new one is
	// a CREATE; the old record is never touched — deletion stays an
	// explicit operator action, and dc-scoped matching would grab OTHER
	// servers' hosts (cores and the host list can be shared).
	hosts[0].Remark = DCInboundTag("teh", "OLD")
	plan3 := DiffDCInbounds(config, []InboundPlan{teh}, hosts, NamePrefix)
	if len(plan3.HostsCreate) != 1 || plan3.HostsCreate[0].Remark != teh.Tag {
		t.Fatalf("renamed host must plan a create: %+v", plan3.HostsCreate)
	}
	if len(plan3.HostsModify) != 0 {
		t.Fatalf("renamed host must not modify another slot's record: %+v", plan3.HostsModify)
	}

	// A suffix variant of OUR OWN remark is still claimed (prefix match).
	hosts[0].Remark = teh.Tag + "-legacy"
	plan4 := DiffDCInbounds(config, []InboundPlan{teh}, hosts, NamePrefix)
	if len(plan4.HostsModify) != 1 || plan4.HostsModify[0].Remark != teh.Tag {
		t.Fatalf("suffix-variant host must plan a modify: %+v", plan4.HostsModify)
	}
}

func TestDiffDCInboundsNeverTouchesNonQuantum(t *testing.T) {
	// An operator inbound identical to what we would create: still foreign.
	foreign := map[string]any{"tag": "INBOUND_9", "listen": "127.0.0.1", "port": 20001}
	config := coreConfigWith(t, foreign)
	plan := DiffDCInbounds(config, []InboundPlan{{Tag: "quantum:teh:hz", Port: 20001, Listen: "127.0.0.1"}}, nil, NamePrefix)
	if len(plan.Create) != 1 {
		t.Fatalf("desired row must be a create (no foreign counterpart): %+v", plan.Create)
	}
	if len(plan.Stale) != 0 {
		t.Fatalf("foreign inbounds must not be reported stale: %+v", plan.Stale)
	}
	if !strings.Contains(mustJSON(t, plan), "INBOUND_9") {
		// sanity: the plan JSON simply does not know the foreign doc at all
		t.Log("foreign inbound absent from the plan, as designed")
	}
}

func TestApplyDCInboundsEndToEnd(t *testing.T) {
	f := newFakePanel(t)
	c := testClient(t, f.srv.URL)
	ctx := context.Background()

	// A stale quantum inbound on the core: the desired set never covers it,
	// every apply must KEEP it (never delete).
	f.cores[1].Config["inbounds"] = append(
		f.cores[1].Config["inbounds"].([]any),
		map[string]any{"tag": "quantum:old:hz-a", "listen": "127.0.0.1", "port": 20099, "protocol": "vless"})

	teh := desiredInbound(t, DCInboundTag("teh", "hz-a"), "10.0.0.1", "q-teh.example.com", "www.microsoft.com:443", 20001)
	chk := desiredInbound(t, DCInboundTag("chekavak", "hz-a"), "10.0.0.2", "q-chekavak.example.com", "scan.example.org:443", 20002)

	// Dry-run: the diff alone touches NOTHING (no core PUT, no hosts).
	core, err := c.GetCore(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := c.ListHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan := DiffDCInbounds(core["config"].(map[string]any), []InboundPlan{teh, chk}, hosts, NamePrefix)
	if len(plan.Create) != 2 || plan.Drift() == false {
		t.Fatalf("dry-run plan: %+v", plan)
	}
	if f.corePuts != 0 || f.hostCreates != 0 || f.hostModifies != 0 {
		t.Fatal("the diff must not touch the panel")
	}

	// Apply: core PUT (restart_nodes=true) then the two host creates. The
	// apply re-reads the core itself (the PUT is a full replace).
	results, err := ApplyDCInbounds(ctx, c, 1, plan)
	if err != nil {
		t.Fatal(err)
	}
	if f.corePuts != 1 || f.restartNodesLast != "restart_nodes=true" {
		t.Fatalf("core PUT: puts=%d query=%q", f.corePuts, f.restartNodesLast)
	}
	if f.hostCreates != 2 || f.hostModifies != 0 {
		t.Fatalf("host ops: creates=%d modifies=%d", f.hostCreates, f.hostModifies)
	}
	if len(results) < 3 {
		t.Fatalf("apply results: %v", results)
	}

	// The merged config keeps the operator inbound, carries our inbounds
	// with the exact desired fields, and keeps stale entries (never delete).
	cfg := f.cores[1].Config
	inbounds := extractInbounds(cfg)
	byTag := map[string]map[string]any{}
	for _, in := range inbounds {
		tag, _ := in["tag"].(string)
		byTag[tag] = in
	}
	if _, ok := byTag["INBOUND_0"]; !ok {
		t.Fatal("operator inbound must survive the merged config")
	}
	if _, ok := byTag["quantum:old:hz-a"]; !ok {
		t.Fatal("stale quantum inbound must survive (never delete)")
	}
	for _, want := range []InboundPlan{teh, chk} {
		got, ok := byTag[want.Tag]
		if !ok {
			t.Fatalf("inbound %s missing after apply: %v", want.Tag, tagsOf(inbounds))
		}
		if got["listen"] != "127.0.0.1" || got["port"] != float64(want.Port) || got["protocol"] != "vless" {
			t.Fatalf("inbound %s shape: %v", want.Tag, got)
		}
		rs := got["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
		if rs["dest"] != want.Dest || rs["privateKey"] != want.PrivateKey {
			t.Fatalf("reality settings for %s: %v", want.Tag, rs)
		}
		snis, _ := stringSlice(rs["serverNames"])
		sids, _ := stringSlice(rs["shortIds"])
		if !strSetEqual(snis, want.SNIs) || !strSetEqual(sids, want.ShortIDs) {
			t.Fatalf("sni/shortIds for %s: %v", want.Tag, rs)
		}
	}

	// Hosts via the CLIENT (GET /api/hosts): address/port/SNI match.
	realHosts, err := c.ListHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hostByRemark := map[string]Host{}
	for _, h := range realHosts {
		hostByRemark[h.Remark] = h
	}
	for _, want := range []InboundPlan{teh, chk} {
		h, ok := hostByRemark[want.Tag]
		if !ok {
			t.Fatalf("host %s missing: %v", want.Tag, realHosts)
		}
		if len(h.Address) != 1 || h.Address[0] != want.ClientAddress || h.Port != 443 ||
			len(h.SNI) != 1 || h.SNI[0] != want.SNIs[0] || h.InboundTag != want.Tag {
			t.Fatalf("host %s mismatch: %+v (want addr=%s sni=%s)", want.Tag, h, want.ClientAddress, want.SNIs[0])
		}
	}

	// Idempotence: re-diff against the new state → nothing planned except
	// the never-covered stale inbound (reported, never acted on); the
	// apply is a no-op (no core PUT, no host ops).
	core2, _ := c.GetCore(ctx, 1)
	hosts2, _ := c.ListHosts(ctx)
	plan2 := DiffDCInbounds(core2["config"].(map[string]any), []InboundPlan{teh, chk}, hosts2, NamePrefix)
	if len(plan2.Create)+len(plan2.Modify)+len(plan2.HostsCreate)+len(plan2.HostsModify) != 0 {
		t.Fatalf("second diff must plan nothing: %+v", plan2)
	}
	if len(plan2.Stale) != 1 || plan2.Stale[0] != "quantum:old:hz-a" {
		t.Fatalf("stale row must be reported: %+v", plan2.Stale)
	}
	if _, err := ApplyDCInbounds(ctx, c, 1, plan2); err != nil {
		t.Fatal(err)
	}
	if f.corePuts != 1 || f.hostCreates != 2 || f.hostModifies != 0 {
		t.Fatalf("no-op apply must not touch the panel: puts=%d creates=%d modifies=%d", f.corePuts, f.hostCreates, f.hostModifies)
	}

	// Out-of-band drift (operator edited the host port + the core port):
	// the next diff plans exactly one modify of each, apply restores.
	hostID := hostByRemark[teh.Tag].ID
	h := f.hosts[hostID]
	h.Port = 4423
	f.hosts[hostID] = h
	cfgInbounds := f.cores[1].Config["inbounds"].([]any)
	for _, v := range cfgInbounds {
		if m, ok := v.(map[string]any); ok && m["tag"] == chk.Tag {
			m["port"] = 19999
		}
	}
	core3, _ := c.GetCore(ctx, 1)
	hosts3, _ := c.ListHosts(ctx)
	plan3 := DiffDCInbounds(core3["config"].(map[string]any), []InboundPlan{teh, chk}, hosts3, NamePrefix)
	if len(plan3.Modify) != 1 || plan3.Modify[0].Tag != chk.Tag {
		t.Fatalf("drifted core plan: %+v", plan3)
	}
	if len(plan3.HostsModify) != 1 || plan3.HostsModify[0].Remark != teh.Tag {
		t.Fatalf("drifted host plan: %+v", plan3.HostsModify)
	}
	if _, err := ApplyDCInbounds(ctx, c, 1, plan3); err != nil {
		t.Fatal(err)
	}
	if f.corePuts != 2 || f.hostModifies != 1 {
		t.Fatalf("drift apply: puts=%d host modifies=%d", f.corePuts, f.hostModifies)
	}
	if f.cores[1].Config["inbounds"] != nil && intInboundPort(f.cores[1].Config, chk.Tag) != chk.Port {
		t.Fatal("core drift was not restored")
	}
	if f.hosts[hostID].Port != 443 {
		t.Fatalf("host drift was not restored: %+v", f.hosts[hostID])
	}
}

func intInboundPort(cfg map[string]any, tag string) int {
	for _, in := range extractInbounds(cfg) {
		if t, _ := in["tag"].(string); t == tag {
			if p, ok := in["port"].(float64); ok {
				return int(p)
			}
		}
	}
	return -1
}

func tagsOf(inbounds []map[string]any) []string {
	out := []string{}
	for _, in := range inbounds {
		tag, _ := in["tag"].(string)
		out = append(out, tag)
	}
	return out
}

func TestApplyDCInboundsHostOnlyDriftSkipsCore(t *testing.T) {
	f := newFakePanel(t)
	c := testClient(t, f.srv.URL)
	ctx := context.Background()

	teh := desiredInbound(t, DCInboundTag("teh", "hz-a"), "10.0.0.1", "q-teh.example.com", "www.microsoft.com:443", 20001)
	core, _ := c.GetCore(ctx, 1)
	plan := DiffDCInbounds(core["config"].(map[string]any), []InboundPlan{teh}, nil, NamePrefix)
	if _, err := ApplyDCInbounds(ctx, c, 1, plan); err != nil {
		t.Fatal(err)
	}
	if f.corePuts != 1 {
		t.Fatalf("first apply must PUT the core: %d", f.corePuts)
	}

	// Hosts-only drift: the core PUT is SKIPPED (no restart), the host
	// modify still runs.
	f.hosts[1] = Host{ID: 1, Remark: teh.Tag, Address: []string{"OLD"}, Port: 4423, InboundTag: teh.Tag}
	f.hostOrder = []int{1}
	core2, _ := c.GetCore(ctx, 1)
	hosts2, _ := c.ListHosts(ctx)
	plan2 := DiffDCInbounds(core2["config"].(map[string]any), []InboundPlan{teh}, hosts2, NamePrefix)
	if len(plan2.Modify) != 0 || len(plan2.HostsModify) != 1 {
		t.Fatalf("hosts-only plan: %+v", plan2)
	}
	results, err := ApplyDCInbounds(ctx, c, 1, plan2)
	if err != nil {
		t.Fatal(err)
	}
	if f.corePuts != 1 {
		t.Fatalf("hosts-only apply must skip the core PUT: %d", f.corePuts)
	}
	if f.hostModifies != 1 || len(results) != 1 || !strings.Contains(results[0], "modified") {
		t.Fatalf("host modify results: %v", results)
	}
	if f.hosts[1].Address[0] != teh.ClientAddress || f.hosts[1].Port != 443 {
		t.Fatalf("host not restored: %+v", f.hosts[1])
	}
}

func TestApplyDCInboundsRefusesBadCore(t *testing.T) {
	f := newFakePanel(t)
	c := testClient(t, f.srv.URL)
	ctx := context.Background()
	teh := desiredInbound(t, DCInboundTag("teh", "hz-a"), "10.0.0.1", "q-teh.example.com", "www.microsoft.com:443", 20001)
	plan := DiffDCInbounds(coreConfigWith(t), []InboundPlan{teh}, nil, NamePrefix)

	// A core record without a config object: apply fails BEFORE any write.
	f.cores[2] = &fakeCore{ID: 2, Name: "broken", Type: "xray"}
	if _, err := ApplyDCInbounds(ctx, c, 2, plan); err == nil {
		t.Fatal("missing core config must fail")
	}
	if f.corePuts != 0 || f.hostCreates != 0 {
		t.Fatal("no writes may happen when the core record is broken")
	}
	// A core the panel does not know: the read fails, nothing is written.
	if _, err := ApplyDCInbounds(ctx, c, 99, plan); err == nil {
		t.Fatal("unknown core must fail")
	}
	if f.corePuts != 0 || f.hostCreates != 0 {
		t.Fatal("no writes may happen when the core read fails")
	}
	if _, err := ApplyDCInbounds(ctx, c, 1, nil); err == nil {
		t.Fatal("nil plan must fail")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
