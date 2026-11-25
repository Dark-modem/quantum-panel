// backend48_frp_compat_test.go — BACKEND-48: legacy FRP engine_params
// stored before BACKEND-47 (remote_addr/dash_port) must reach the agent
// in the driver's own keys (server_addr/dashboard_port). The translation
// is boundary-only: the stored tunnel row is never rewritten.

package panel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
	"github.com/Dark-modem/quantum-protocol"
)

func backend48FRPCompile(t *testing.T, params string) (protocol.TunnelSpec, error) {
	t.Helper()
	s, sv, peer, topoID := phase45CompileFixture(t)
	key := "b48-frp"
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: key, TopologyID: topoID,
		LocalServerID: sv, RemoteServerID: peer, Engine: "frp",
		EngineParams: params, Enabled: true}); err != nil {
		t.Fatalf("store: %v", err)
	}
	ag, err := s.GetAgent("agt_0a0a0a0a0a0a")
	if err != nil {
		t.Fatal(err)
	}
	desired, skip, warnings, err := compileDesiredV2(s, ag)
	if err != nil {
		return protocol.TunnelSpec{}, err
	}
	if skip != "" || len(warnings) != 0 {
		t.Fatalf("skip=%q warnings=%v", skip, warnings)
	}
	for _, spec := range desired.Tunnels {
		if spec.ID == key {
			return spec, nil
		}
	}
	t.Fatalf("compiled spec %q missing", key)
	return protocol.TunnelSpec{}, nil
}

func backend48FRPMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("compiled params are not a JSON object: %v (%s)", err, raw)
	}
	return m
}

func TestBackend48FRPLegacyParamsTranslated(t *testing.T) {
	old := `{"server_side":"p48-fr","version":"0.61.1","artifact":"frp-0.61.1-linux-{arch}.tar.gz","remote_addr":"old-frps-host","server_port":7000,"token":"frp-token","remote_port":8080,"local_addr":"127.0.0.1","local_port":80,"dash_port":7500,"allow_port_min":8000,"allow_port_max":9000}`
	spec, err := backend48FRPCompile(t, old)
	if err != nil {
		t.Fatalf("legacy params must compile after the boundary translation: %v", err)
	}
	m := backend48FRPMap(t, string(spec.EngineParams))
	if m["server_addr"] != "old-frps-host" {
		t.Fatalf("remote_addr not translated to server_addr: %v", m["server_addr"])
	}
	if m["dashboard_port"] != float64(7500) {
		t.Fatalf("dash_port not translated to dashboard_port: %v", m["dashboard_port"])
	}
	for _, dead := range []string{"remote_addr", "dash_port"} {
		if _, ok := m[dead]; ok {
			t.Fatalf("legacy key %q survived the boundary translation: %s", dead, spec.EngineParams)
		}
	}
	for _, kept := range []string{"server_side", "version", "artifact", "server_port", "token", "remote_port", "local_addr", "local_port", "allow_port_min", "allow_port_max"} {
		if _, ok := m[kept]; !ok {
			t.Fatalf("non-legacy key %q lost: %s", kept, spec.EngineParams)
		}
	}
}

func TestBackend48FRPNewParamsVerbatim(t *testing.T) {
	next := `{"server_side":"p48-fr","version":"0.61.1","artifact":"frp-0.61.1-linux-{arch}.tar.gz","server_addr":"new-frps-host","server_port":7000,"token":"frp-token","remote_port":8080,"local_addr":"127.0.0.1","local_port":80,"dashboard_port":7500,"allow_port_min":8000,"allow_port_max":9000}`
	spec, err := backend48FRPCompile(t, next)
	if err != nil {
		t.Fatalf("new params: %v", err)
	}
	if string(spec.EngineParams) != next {
		t.Fatalf("new params must stay verbatim:\n got %s\nwant %s", spec.EngineParams, next)
	}
}

func TestBackend48FRPMixedParams(t *testing.T) {
	// one side of each pair is empty: fill it from the legacy spelling.
	mixed := `{"server_side":"p48-fr","version":"0.61.1","artifact":"frp-0.61.1-linux-{arch}.tar.gz","server_addr":"","remote_addr":"old-frps-host","server_port":7000,"token":"frp-token","remote_port":8080,"local_addr":"127.0.0.1","local_port":80,"dashboard_port":0,"dash_port":7500,"allow_port_min":8000,"allow_port_max":9000}`
	spec, err := backend48FRPCompile(t, mixed)
	if err != nil {
		t.Fatalf("mixed params: %v", err)
	}
	m := backend48FRPMap(t, string(spec.EngineParams))
	if m["server_addr"] != "old-frps-host" || m["dashboard_port"] != float64(7500) {
		t.Fatalf("mixed params not filled: %s", spec.EngineParams)
	}
	if _, ok := m["remote_addr"]; ok {
		t.Fatalf("legacy remote_addr survived: %s", spec.EngineParams)
	}
	if _, ok := m["dash_port"]; ok {
		t.Fatalf("legacy dash_port survived: %s", spec.EngineParams)
	}

	// both spellings carry the same value: harmless, deduplicated.
	same := `{"server_side":"p48-fr","version":"0.61.1","artifact":"frp-0.61.1-linux-{arch}.tar.gz","server_addr":"same-host","remote_addr":"same-host","server_port":7000,"token":"frp-token","remote_port":8080,"local_addr":"127.0.0.1","local_port":80,"dashboard_port":7500,"dash_port":7500,"allow_port_min":8000,"allow_port_max":9000}`
	spec, err = backend48FRPCompile(t, same)
	if err != nil {
		t.Fatalf("equal aliases must not conflict: %v", err)
	}
	m = backend48FRPMap(t, string(spec.EngineParams))
	if m["server_addr"] != "same-host" || m["dashboard_port"] != float64(7500) {
		t.Fatalf("equal aliases resolved wrong: %s", spec.EngineParams)
	}
}

func TestBackend48FRPConflictRefused(t *testing.T) {
	cases := []struct {
		name, params, want string
	}{
		{
			name: "host conflict",
			params: `{"server_side":"p48-fr","server_addr":"new-frps-host","remote_addr":"old-frps-host",` +
				`"dashboard_port":7500}`,
			want: "remote_addr",
		},
		{
			name: "dashboard conflict",
			params: `{"server_side":"p48-fr","server_addr":"new-frps-host",` +
				`"dashboard_port":7501,"dash_port":7500}`,
			want: "dash_port",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := backend48FRPCompile(t, c.params)
			if err == nil {
				t.Fatal("conflicting legacy/new values must be refused, not guessed")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error must name the conflicting key %q: %v", c.want, err)
			}
		})
	}
}

func TestBackend48FRPStoredParamsUntouched(t *testing.T) {
	s, sv, peer, topoID := phase45CompileFixture(t)
	old := `{"server_side":"p48-fr","remote_addr":"old-frps-host","dash_port":7500}`
	id, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "b48-store", TopologyID: topoID,
		LocalServerID: sv, RemoteServerID: peer, Engine: "frp",
		EngineParams: old, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ag, _ := s.GetAgent("agt_0a0a0a0a0a0a")
	if _, _, _, err := compileDesiredV2(s, ag); err != nil {
		t.Fatalf("compile: %v", err)
	}
	got, err := s.GetTunnel(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.EngineParams != old {
		t.Fatalf("the boundary translation must not rewrite the stored row:\n got %s\nwant %s", got.EngineParams, old)
	}
}
