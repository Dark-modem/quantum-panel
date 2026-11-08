// enginetemplates_test.go — phase 9 TASK 2a tests: secret stripping from
// GET responses, masked-secret merge on updates, the templates endpoint,
// and the topology GET path (no secret ever echoed).

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestStripEngineSecrets(t *testing.T) {
	params := `{"server_side":"germany","version":"0.61.1","token":"super-secret-token","remote_port":7000}`
	got := string(stripEngineSecrets("frp", params))
	if strings.Contains(got, "super-secret-token") {
		t.Fatalf("secret value leaked: %s", got)
	}
	if !strings.Contains(got, `{"__set":true}`) {
		t.Fatalf("secret not marked set: %s", got)
	}
	if !strings.Contains(got, `"remote_port":7000`) {
		t.Fatalf("non-secret field stripped: %s", got)
	}
	if got2 := string(stripEngineSecrets("unknown-eng", params)); got2 != params {
		t.Fatalf("unknown engine must pass through: %s", got2)
	}
	empty := `{"token":""}`
	if got3 := string(stripEngineSecrets("frp", empty)); !strings.Contains(got3, `{"__set":false}`) {
		t.Fatalf("empty secret not marked unset: %s", got3)
	}
}

func TestMergeEngineSecrets(t *testing.T) {
	stored := `{"server_side":"germany","token":"stored-token","remote_port":7000}`
	update := `{"server_side":"germany","remote_port":7100}`
	got := string(mergeEngineSecrets("frp", stored, update))
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if m["token"] != "stored-token" {
		t.Fatalf("stored token lost: %v", m["token"])
	}
	if m["remote_port"] != float64(7100) {
		t.Fatalf("non-secret update lost: %v", m["remote_port"])
	}
	repl := `{"token":"brand-new-token"}`
	if got2 := string(mergeEngineSecrets("frp", stored, repl)); !strings.Contains(got2, "brand-new-token") {
		t.Fatalf("new secret not applied: %s", got2)
	}
	if got3 := string(mergeEngineSecrets("unknown-eng", stored, `{"a":1}`)); got3 != `{"a":1}` {
		t.Fatalf("unknown engine must pass through: %s", got3)
	}
}

func TestEngineTemplatesEndpoint(t *testing.T) {
	withTempConfig(t)
	mux := http.NewServeMux()
	registerEngineTemplateRoutes(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := meshSession(t)
	resp, err := client.Get(srv.URL + "/api/mesh/engines/templates")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Templates []struct {
			Engine string             `json:"engine"`
			Fields []engineParamField `json:"fields"`
		} `json:"templates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	engines := map[string]bool{}
	for _, tp := range got.Templates {
		engines[tp.Engine] = true
		for _, f := range tp.Fields {
			if f.LabelEn == "" || f.LabelFa == "" {
				t.Fatalf("engine %s field %s missing a label", tp.Engine, f.JSON)
			}
		}
	}
	for _, want := range []string{"frp", "wstunnel", "chisel", "xray", "wireguard", "udp2raw", "matrix", "backhaul", "rathole", "gost"} {
		if !engines[want] {
			t.Fatalf("template for %q missing", want)
		}
	}
	resp2, err := http.Get(srv.URL + "/api/mesh/engines/templates")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == 200 {
		t.Fatal("templates endpoint must require auth")
	}
}

func TestTunnelDetailStripsSecrets(t *testing.T) {
	env := newEngineTestEnv(t)
	dc, _ := env.store.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "dsec"})
	sv1, _ := env.store.UpsertServer(store.Server{Name: "ir-sec", Role: "iran", DatacenterID: dc})
	sv2, _ := env.store.UpsertServer(store.Server{Name: "fr-sec", Role: "foreign"})
	topo, _ := env.store.UpsertTopology(store.Topology{Name: "sec-topo"})
	secretParams := `{"server_side":"fr-sec","version":"0.61.1","artifact":"frp-0.61.1-linux-{arch}.tar.gz","sha256_amd64":"` +
		strings.Repeat("a", 64) + `","token":"frp-token-value"}`
	if _, err := env.store.UpsertTunnel(store.Tunnel{TunnelKey: "sec-t1", TopologyID: topo,
		LocalServerID: sv1, RemoteServerID: sv2, Engine: "frp", EngineParams: secretParams, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerMeshRoutes(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := meshSession(t)
	resp, err := client.Get(srv.URL + "/api/mesh/topologies/" + strconv.FormatInt(topo, 10))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Tunnels []struct {
			TunnelKey    string `json:"tunnel_key"`
			EngineParams string `json:"engine_params"`
		} `json:"tunnels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	found := false
	for _, tn := range body.Tunnels {
		if tn.TunnelKey != "sec-t1" {
			continue
		}
		found = true
		if strings.Contains(tn.EngineParams, "frp-token-value") || strings.Contains(tn.EngineParams, strings.Repeat("a", 64)) {
			t.Fatalf("secret leaked in topology GET: %s", tn.EngineParams)
		}
		if !strings.Contains(tn.EngineParams, `"__set":true`) {
			t.Fatalf("secret not marked set: %s", tn.EngineParams)
		}
		if !strings.Contains(tn.EngineParams, `"server_side":"fr-sec"`) {
			t.Fatalf("non-secret fields must survive: %s", tn.EngineParams)
		}
	}
	if !found {
		t.Fatal("tunnel not in the topology GET response")
	}
}
