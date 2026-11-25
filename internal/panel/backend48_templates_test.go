// backend48_templates_test.go — BACKEND-48 TASK 2: the fallback form
// templates for the landed drivers must use the keys those drivers
// actually decode. The stale backhaul/rathole/wireguard rows used
// pre-driver spellings (server_port/local_port/listen_port) that the
// agent ignores, so a form-built tunnel could never validate.

package panel

import "testing"

func backend48AssertTemplateFields(t *testing.T, engine string, want map[string]string, dead []string) {
	t.Helper()
	fields, ok := engineParamTemplatesFor(engine)
	if !ok {
		t.Fatalf("no %s template", engine)
	}
	got := map[string]string{}
	for _, f := range fields {
		got[f.JSON] = f.Kind
	}
	for key, kind := range want {
		if got[key] != kind {
			t.Fatalf("%s template key %q: got %q, want %q", engine, key, got[key], kind)
		}
	}
	for _, key := range dead {
		if _, ok := got[key]; ok {
			t.Fatalf("%s template still carries the dead key %q", engine, key)
		}
	}
}

func TestBackend48BackhaulTemplateMatchesDriver(t *testing.T) {
	backend48AssertTemplateFields(t, "backhaul", map[string]string{
		"server_side": "string", "version": "string", "artifact": "string",
		"sha256_amd64": "secret", "sha256_arm64": "secret",
		"signature_amd64": "secret", "signature_arm64": "secret",
		"transport": "string", "token": "secret", "bind_addr": "string",
		"remote_addr": "string", "web_port": "number", "accept_udp": "bool",
	}, []string{"server_port", "local_addr", "local_port"})
}

func TestBackend48RatholeTemplateMatchesDriver(t *testing.T) {
	backend48AssertTemplateFields(t, "rathole", map[string]string{
		"server_side": "string", "version": "string", "artifact": "string",
		"sha256_amd64": "secret", "sha256_arm64": "secret",
		"signature_amd64": "secret", "signature_arm64": "secret",
		"server_addr": "string", "service_addr": "string", "local_addr": "string",
		"token": "secret", "transport": "string", "service_type": "string",
		"prefer_ipv6": "bool", "noise_server_public": "string",
		"pkcs12_path": "string", "pkcs12_password": "secret",
	}, []string{"remote_addr", "server_port", "local_port"})
}

func TestBackend48WireguardTemplateMatchesDriver(t *testing.T) {
	backend48AssertTemplateFields(t, "wireguard", map[string]string{
		"server_side": "string", "version": "string", "carrier_ip": "string",
		"carrier_port": "number", "mtu": "number", "peer_public": "string",
	}, []string{"listen_port", "peer_public_key"})
}
