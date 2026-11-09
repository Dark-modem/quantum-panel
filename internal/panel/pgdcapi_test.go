package panel

// pgdcapi_test.go — the per-DC PasarGuard endpoints (phase 14 task 2)
// against the fake upstream: the dry-run plan per foreign server (nothing
// touched upstream, private key never in the response), the confirm-gated
// apply (ports booked in the ledger, core PUT with restart_nodes=true,
// hosts created with the right address/port/SNI), and the client-config
// preview shape.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/pasarguard"
	"github.com/Dark-modem/quantum-panel/internal/store"
)

// pgDCFixture: 2 iran dcs (one iran server each) × 2 foreign servers and a
// mesh+UI session client (the dc endpoints ride BOTH route families).
type pgDCFixture struct {
	up        *pgFakeUpstream
	srv       *httptest.Server
	client    *http.Client
	s         *store.Store
	dcIr      int64
	dcChk     int64
	dcForeign int64
	iran      []int64
	foreign   []int64
}

func pgDCFixtureSetup(t *testing.T) *pgDCFixture {
	t.Helper()
	f := &pgDCFixture{up: newPGFakeUpstream(t)}
	withTempConfig(t)
	withMeshStore(t)
	mux := http.NewServeMux()
	registerMeshRoutes(mux, "")
	registerUIAPIRoutes(mux, "")
	f.client = meshSession(t)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.s = meshStoreMust(t)

	f.dcIr, _ = f.s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "Tehran"})
	f.dcChk, _ = f.s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "Chekavak"})
	f.dcForeign, _ = f.s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "Hetzner"})
	ir0, err := f.s.UpsertServer(store.Server{Role: "iran", Name: "ir-0", DatacenterID: f.dcIr, Address: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	ir1, err := f.s.UpsertServer(store.Server{Role: "iran", Name: "ir-1", DatacenterID: f.dcChk, Address: "10.0.0.2"})
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"hz-0", "hz-1"} {
		id, err := f.s.UpsertServer(store.Server{Role: "foreign", Name: name,
			DatacenterID: f.dcForeign, Address: fmt.Sprintf("195.201.0.%d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		f.foreign = append(f.foreign, id)
	}
	f.iran = []int64{ir0, ir1}

	// Connect the panel to the fake upstream (credentials sealed).
	if err := pgPostOK(f.client, f.srv, "/api/mesh/pasarguard/connect",
		map[string]any{"url": f.up.srv.URL, "username": "qadmin", "password": "qpass"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	// One upstream node per foreign server (the new model) + the mapping
	// rows linking them (the linkage pgDCPairs requires).
	f.up.seedNode(1, "quantum:dc-hz-0", 1)
	f.up.seedNode(2, "quantum:dc-hz-1", 1)
	for i, sid := range f.foreign {
		if _, err := f.s.UpsertPGNode(store.PGNode{ServerID: sid, DatacenterID: f.dcIr, PGNodeID: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// pgGetRaw returns the raw response body and status (secret-leak greps).
func pgGetRaw(t *testing.T, c *http.Client, srv *httptest.Server, path string) (string, int) {
	t.Helper()
	resp, err := c.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), resp.StatusCode
}

// pgGetErr reports an error when the GET answers 2xx.
func pgGetErr(c *http.Client, srv *httptest.Server, path string) error {
	resp, err := c.Get(srv.URL + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		return fmt.Errorf("HTTP %d (expected an error status)", resp.StatusCode)
	}
	return nil
}

// pgUnsealKey fetches the sealed private key for one pair and unseals it
// (the ONLY place a test holds the plaintext — to grep responses for it).
func pgUnsealKey(t *testing.T, s *store.Store, id string) (pvk, pbk string) {
	t.Helper()
	key, err := pgKey()
	if err != nil {
		t.Fatal(err)
	}
	keysJSON, _ := s.Setting("reality_keys")
	var keys map[string]struct {
		PVKSealed string `json:"pvk_sealed"`
		PBK       string `json:"pbk"`
	}
	if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
		t.Fatalf("reality_keys store: %v (%q)", err, keysJSON)
	}
	e, ok := keys[id]
	if !ok || e.PVKSealed == "" {
		t.Fatalf("no sealed key for %s in %q", id, keysJSON)
	}
	pvkB, err := pasarguard.Open(key, e.PVKSealed)
	if err != nil {
		t.Fatal(err)
	}
	return string(pvkB), e.PBK
}

func TestPGDCPlanDryRun(t *testing.T) {
	f := pgDCFixtureSetup(t)

	// Plan works end to end; the shape is asserted on the RAW body below.
	raw, status := pgGetRaw(t, f.client, f.srv, "/api/mesh/pasarguard/dc/plan")
	if status != 200 {
		t.Fatalf("plan HTTP %d: %s", status, raw)
	}
	var res struct {
		Plans []struct {
			Server string `json:"server"`
			CoreID int    `json:"core_id"`
			Plan   struct {
				Create      []map[string]any `json:"create"`
				Modify      []map[string]any `json:"modify"`
				HostsCreate []map[string]any `json:"hosts_create"`
				Stale       []string         `json:"stale"`
			} `json:"plan"`
		} `json:"plans"`
		Warnings []string `json:"warnings"`
		Drift    bool     `json:"drift"`
		Applied  bool     `json:"applied"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("plan JSON: %v", err)
	}
	if len(res.Plans) != 2 || res.Applied {
		t.Fatalf("plans: %+v", res)
	}
	for _, p := range res.Plans {
		if p.CoreID != 1 {
			t.Fatalf("%s must resolve the node's core: %d", p.Server, p.CoreID)
		}
		// One inbound + one host per iran dc (2 dcs per server).
		if len(p.Plan.Create) != 2 || len(p.Plan.HostsCreate) != 2 {
			t.Fatalf("%s plan: %+v", p.Server, p.Plan)
		}
		for _, in := range p.Plan.Create {
			tag, _ := in["tag"].(string)
			if !strings.HasPrefix(tag, "quantum:") || !strings.Contains(tag, ":hz-") {
				t.Fatalf("inbound tag %q lost the quantum:<dc>:<server> shape", tag)
			}
			if got, _ := in["listen"].(string); got != "127.0.0.1" {
				t.Fatalf("inbound %s listen: %v", tag, got)
			}
			if port, ok := in["port"].(float64); !ok || port < 20000 || port > 29999 {
				t.Fatalf("inbound %s port outside the default pool: %v", tag, in["port"])
			}
			if got, _ := in["public_key"].(string); got == "" {
				t.Fatalf("inbound %s missing the public key", tag)
			}
			if _, present := in["private_key"]; present {
				t.Fatalf("inbound %s leaked a private_key field", tag)
			}
		}
	}
	if !res.Drift {
		t.Fatal("plan must report drift")
	}

	// The sealed key store covers every pair; the private key never leaks
	// into the response, the public key does (client configs need it).
	if pvk, _ := pgUnsealKey(t, f.s, "hz-0:tehran"); strings.Contains(raw, pvk) {
		t.Fatal("the private key leaked into the plan response")
	}
	if _, pbk := pgUnsealKey(t, f.s, "hz-0:tehran"); !strings.Contains(raw, pbk) {
		t.Fatal("public key must ride the plan")
	}
	keysJSON, _ := f.s.Setting("reality_keys")
	if n := strings.Count(keysJSON, `"pbk"`); n != 4 {
		t.Fatalf("reality_keys rows: %d (%q)", n, keysJSON)
	}

	// Dry-run touched NOTHING upstream and booked no ports.
	if f.up.corePuts != 0 || f.up.hostCreates != 0 || len(f.up.hosts) != 0 {
		t.Fatalf("dry run mutated the panel: puts=%d creates=%d hosts=%d", f.up.corePuts, f.up.hostCreates, len(f.up.hosts))
	}
	if allocs, _ := f.s.ListPortAllocations(f.foreign[0]); len(allocs) != 0 {
		t.Fatalf("dry run booked ports: %v", allocs)
	}
}

func TestPGDCPlanWarnsUnlinked(t *testing.T) {
	f := pgDCFixtureSetup(t)
	// Drop the mapping rows: no foreign server is linked anymore.
	mappings, _ := f.s.ListPGNodes()
	for _, m := range mappings {
		if err := f.s.DeletePGNode(m.ID); err != nil {
			t.Fatal(err)
		}
	}
	res := pgCall(t, f.client, f.srv, "GET", "/api/mesh/pasarguard/dc/plan", nil)
	if plans := res["plans"].([]any); len(plans) != 0 {
		t.Fatalf("plans without linked nodes: %v", plans)
	}
	if warnings := res["warnings"].([]any); len(warnings) == 0 {
		t.Fatal("unlinked servers must be warned about")
	}
}

func TestPGDCApplyAndClientConfig(t *testing.T) {
	f := pgDCFixtureSetup(t)
	up := f.up

	// Apply without confirm refused.
	if err := pgPost(f.client, f.srv, "/api/mesh/pasarguard/dc/apply",
		map[string]any{"apply": true}); err == nil {
		t.Fatal("apply without confirm=yes must be refused")
	}
	if up.corePuts != 0 {
		t.Fatal("refused apply must not touch the panel")
	}

	// Apply (confirm=yes): core PUT per server, one host per pair.
	res := pgCall(t, f.client, f.srv, "POST", "/api/mesh/pasarguard/dc/apply",
		map[string]any{"apply": true, "confirm": "yes"})
	if res["applied"] != true {
		t.Fatalf("apply result: %v", res)
	}
	if up.corePuts != 2 {
		t.Fatalf("core puts: %d (one per foreign server)", up.corePuts)
	}
	if up.restartNodesLast != "restart_nodes=true" {
		t.Fatalf("core PUT query: %q", up.restartNodesLast)
	}
	if up.hostCreates != 4 { // 2 servers × 2 dcs
		t.Fatalf("host creates: %d", up.hostCreates)
	}

	// Hosts: remark = tag, address = the dc's iran server, port 443,
	// sni = q-<dcslug>.<foreign address> (no reality_sni_base set).
	wantHosts := map[string]map[string]any{
		"quantum:tehran:hz-0":   {"address": "10.0.0.1", "sni": "q-tehran.195.201.0.1"},
		"quantum:chekavak:hz-0": {"address": "10.0.0.2", "sni": "q-chekavak.195.201.0.1"},
		"quantum:tehran:hz-1":   {"address": "10.0.0.1", "sni": "q-tehran.195.201.0.2"},
		"quantum:chekavak:hz-1": {"address": "10.0.0.2", "sni": "q-chekavak.195.201.0.2"},
	}
	for wantRemark, want := range wantHosts {
		found := false
		for _, id := range up.hostOrder {
			h := up.hosts[id]
			if h["remark"] != wantRemark {
				continue
			}
			found = true
			if addr := h["address"].([]any); len(addr) != 1 || addr[0] != want["address"] {
				t.Fatalf("%s address: %v", wantRemark, h["address"])
			}
			if h["port"] != float64(443) {
				t.Fatalf("%s port: %v", wantRemark, h["port"])
			}
			if sni := h["sni"].([]any); len(sni) != 1 || sni[0] != want["sni"] {
				t.Fatalf("%s sni: %v", wantRemark, h["sni"])
			}
			if h["inbound_tag"] != wantRemark {
				t.Fatalf("%s inbound_tag: %v", wantRemark, h["inbound_tag"])
			}
		}
		if !found {
			t.Fatalf("host %s missing (have %d hosts)", wantRemark, len(up.hosts))
		}
	}

	// The core config carries the inbounds; the operator one untouched.
	cfg := up.cores[1]["config"].(map[string]any)
	tags := map[string]map[string]any{}
	for _, v := range cfg["inbounds"].([]any) {
		m := v.(map[string]any)
		tag, _ := m["tag"].(string)
		tags[tag] = m
	}
	for _, tag := range []string{"INBOUND_0",
		"quantum:tehran:hz-0", "quantum:chekavak:hz-0",
		"quantum:tehran:hz-1", "quantum:chekavak:hz-1"} {
		if _, ok := tags[tag]; !ok {
			t.Fatalf("inbound %s missing after apply (have %v)", tag, tagsOfFake(tags))
		}
	}

	// Ports booked in the ledger under the pg-inbound owners, distinct per
	// server (loopback ports are host-local, other servers may repeat them),
	// inside the default pool.
	for i, sid := range f.foreign {
		allocs, err := f.s.ListPortAllocations(sid)
		if err != nil {
			t.Fatal(err)
		}
		if len(allocs) != 2 {
			t.Fatalf("server %d ledger rows: %v", sid, allocs)
		}
		seen := map[int]bool{}
		for _, a := range allocs {
			if a.Proto != "tcp" || a.Port < 20000 || a.Port > 29999 || seen[a.Port] {
				t.Fatalf("bad ledger row: %+v", a)
			}
			if !strings.HasPrefix(a.OwnerKey, fmt.Sprintf("pg-inbound:hz-%d:", i)) {
				t.Fatalf("ledger owner: %q", a.OwnerKey)
			}
			if !strings.HasPrefix(a.Purpose, "xray-inbound:") {
				t.Fatalf("ledger purpose: %q", a.Purpose)
			}
			seen[a.Port] = true
		}
	}

	// Idempotence: a second confirmed apply changes nothing.
	res = pgCall(t, f.client, f.srv, "POST", "/api/mesh/pasarguard/dc/apply",
		map[string]any{"apply": true, "confirm": "yes"})
	if res["applied"] != true {
		t.Fatalf("second apply: %v", res)
	}
	if up.corePuts != 2 || up.hostCreates != 4 {
		t.Fatalf("second apply duplicated work: puts=%d creates=%d", up.corePuts, up.hostCreates)
	}

	// Client-config for the Tehran dc: one outbound per iran server with
	// the Reality fields of the primary foreign pair.
	res2 := pgCall(t, f.client, f.srv, "GET",
		fmt.Sprintf("/api/ui/datacenters/%d/client-config", f.dcIr), nil)
	dc := res2["datacenter"].(map[string]any)
	if dc["name"] != "Tehran" || dc["kind"] != "iran" {
		t.Fatalf("client-config datacenter: %v", dc)
	}
	outbounds := res2["outbounds"].([]any)
	if len(outbounds) != 1 {
		t.Fatalf("outbounds: %v", outbounds)
	}
	ob := outbounds[0].(map[string]any)
	if ob["tag"] != "q-tehran-ir-0" || ob["address"] != "10.0.0.1" || ob["port"] != float64(443) {
		t.Fatalf("outbound shape: %v", ob)
	}
	if ob["sni"] != "q-tehran.195.201.0.1" {
		t.Fatalf("outbound sni: %v", ob["sni"])
	}
	_, pbk := pgUnsealKey(t, f.s, "hz-0:tehran")
	if ob["publicKey"] != pbk {
		t.Fatalf("outbound publicKey: %v (want the stored pbk)", ob["publicKey"])
	}
	if sid, _ := ob["shortId"].(string); len(sid) != 8 {
		t.Fatalf("outbound shortId: %v", ob["shortId"])
	}
	// The outbound's sni is inside the inbound's serverNames (one inbound,
	// one SNI — the per-DC design's core decision).
	rs := tags["quantum:tehran:hz-0"]["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	snis := rs["serverNames"].([]any)
	if len(snis) != 1 || snis[0] != "q-tehran.195.201.0.1" {
		t.Fatalf("inbound serverNames: %v", snis)
	}
	// Two foreign servers serve the dc → the note about the primary pair.
	if notes := res2["notes"].([]any); len(notes) == 0 {
		t.Fatalf("multi-foreign dc must carry a note: %v", res2)
	}
	// Raw response must never carry the private key.
	raw2, _ := pgGetRaw(t, f.client, f.srv,
		fmt.Sprintf("/api/ui/datacenters/%d/client-config", f.dcIr))
	if pvk, _ := pgUnsealKey(t, f.s, "hz-0:tehran"); strings.Contains(raw2, pvk) {
		t.Fatal("the private key leaked into the client-config response")
	}

	// A foreign-kind dc is refused.
	raw3, status3 := pgGetRaw(t, f.client, f.srv,
		fmt.Sprintf("/api/ui/datacenters/%d/client-config", f.dcForeign))
	if status3 < 300 {
		t.Fatalf("foreign dc client-config must be refused, got HTTP %d: %s", status3, raw3)
	}
}

func tagsOfFake(tags map[string]map[string]any) []string {
	out := []string{}
	for k := range tags {
		out = append(out, k)
	}
	return out
}
