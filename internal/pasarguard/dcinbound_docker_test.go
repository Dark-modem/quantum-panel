//go:build pgintegration

// dcinbound_docker_test.go — the per-DC inbound reconciler against a REAL
// PasarGuard panel in Docker (build tag pgintegration, same family as
// integration_docker_test.go; container/image conventions follow
// tests/pasarguard_docker_integration.sh).
//
// Flow: docker available? → else SKIP. A panel is reused when
// PG_PANEL_URL/PG_ADMIN_USER/PG_ADMIN_PASS are set (script-managed),
// otherwise one is BOOTSTRAPPED in a quantum-test-dc-* container
// (self-signed TLS via docker cp, owner bootstrap through the panel CLI's
// temp key). Then: node record → core GET → per-DC diff → apply (core PUT
// with restart_nodes=true + host create) → GET /api/hosts asserts
// address/port/SNI → re-diff is clean (idempotence). Cleanup: created
// hosts + quantum nodes deleted via the API; when we own the container it
// is removed and `docker ps -a` is captured as proof.
//
// Run: go test -tags pgintegration ./internal/pasarguard/ -run TestIntegrationDCInbounds -count=1 -v
// Everything SKIPS cleanly when docker is unavailable.

package pasarguard

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// dockerCLI resolves the docker binary ("docker", or "docker.exe" for
// Docker Desktop without WSL integration). "" = unavailable.
func dockerCLI() string {
	for _, name := range []string{"docker", "docker.exe"} {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	return ""
}

func dockerRun(t *testing.T, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, dockerCLI(), args...).CombinedOutput()
	return string(out), err
}

// pgPanelEnv either picks up the script-managed panel (PG_PANEL_URL env
// family) or bootstraps a fresh one in Docker. Returns the client, the
// panel URL, and whether WE own the container (cleanup removes it).
func pgPanelEnv(t *testing.T) (*Client, string, bool) {
	t.Helper()
	if url, user, pass := os.Getenv("PG_PANEL_URL"), os.Getenv("PG_ADMIN_USER"), os.Getenv("PG_ADMIN_PASS"); url != "" && user != "" && pass != "" {
		tlsOpts := TLSOptions{}
		if ca := os.Getenv("PG_PANEL_CA"); ca != "" {
			b, err := os.ReadFile(ca)
			if err != nil {
				t.Fatalf("panel CA: %v", err)
			}
			tlsOpts.CAPem = string(b)
		}
		c, err := New(url, Credentials{Username: user, Password: pass}, tlsOpts, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return c, url, false
	}

	const (
		container = "quantum-test-dc-panel"
		volume    = "quantum-test-dc-panel-data"
	)
	image := os.Getenv("PG_IMAGE_PANEL")
	if image == "" {
		image = "ghcr.io/pasarguard/panel:latest"
	}
	user, pass := "quantum-admin", "QA-9x7-Truth-2026"

	// Panel TLS material (upstream refuses self-signed without
	// UVICORN_SSL_CA_TYPE=private; the cert files go in via docker cp
	// before the first start, exactly like the shell script does).
	dir := t.TempDir()
	certPEM, keyPEM := panelCertPEM(t)
	writeFile(t, filepath.Join(dir, "ssl_cert.pem"), certPEM)
	writeFile(t, filepath.Join(dir, "ssl_key.pem"), keyPEM)

	// A free host port, bound to loopback only.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	if out, err := dockerRun(t, 60*time.Second, "volume", "create", volume); err != nil {
		t.Fatalf("volume create: %v: %s", err, out)
	}
	if _, err := dockerRun(t, 120*time.Second, "create", "--name", container,
		"-p", fmt.Sprintf("127.0.0.1:%d:8000", port),
		"-v", volume+":/var/lib/pasarguard",
		"-e", "UVICORN_HOST=0.0.0.0", "-e", "UVICORN_PORT=8000",
		"-e", "UVICORN_SSL_CA_TYPE=private",
		"-e", "UVICORN_SSL_CERTFILE=/code/ssl_cert.pem",
		"-e", "UVICORN_SSL_KEYFILE=/code/ssl_key.pem",
		image); err != nil {
		t.Fatalf("panel create: %v", err)
	}
	for _, f := range []string{"ssl_cert.pem", "ssl_key.pem"} {
		if _, err := dockerRun(t, 60*time.Second, "cp", filepath.Join(dir, f), container+":/code/"+f); err != nil {
			t.Fatalf("docker cp %s: %v", f, err)
		}
	}
	if _, err := dockerRun(t, 60*time.Second, "start", container); err != nil {
		t.Fatalf("panel start: %v", err)
	}
	panelURL := fmt.Sprintf("https://127.0.0.1:%d", port)

	// Wait for the TLS endpoint (first boot runs migrations; 2 min budget).
	insecure := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // bootstrap probe only
	}
	up := false
	for i := 0; i < 60; i++ {
		resp, err := insecure.Get(panelURL + "/api/admin/token")
		if err == nil {
			_ = resp.Body.Close()
			up = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !up {
		logs, _ := dockerRun(t, 30*time.Second, "logs", "--tail", "30", container)
		t.Fatalf("panel never came up at %s; logs:\n%s", panelURL, logs)
	}

	// Owner bootstrap: temp key from the panel CLI, then POST /api/setup/owner.
	out, err := dockerRun(t, 60*time.Second, "exec", "-w", "/code", container,
		"python", "-m", "cli.main", "generate-temp-key")
	if err != nil {
		t.Fatalf("temp key: %v: %s", err, out)
	}
	key := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).FindString(out)
	if key == "" {
		t.Fatalf("no temp key in CLI output: %s", out)
	}
	body, _ := json.Marshal(map[string]string{"key": key, "username": user, "password": pass})
	resp, err := insecure.Post(panelURL+"/api/setup/owner", "application/json", bytes.NewReader(body))
	if err == nil {
		_ = resp.Body.Close()
		if code := resp.StatusCode; code != 200 && code != 201 && code != 409 {
			t.Fatalf("owner bootstrap: HTTP %d", code)
		}
	} else {
		t.Fatalf("owner bootstrap request: %v", err)
	}

	c, err := New(panelURL, Credentials{Username: user, Password: pass},
		TLSOptions{CAPem: string(certPEM)}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bootstrapped panel at %s (container %s)", panelURL, container)
	return c, panelURL, true
}

func panelCertPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "quantum-test-dc-panel"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:     true, BasicConstraintsValid: true,
		// The published loopback port: the client dials https://127.0.0.1:.
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"quantum-test-dc-panel"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationDCInbounds(t *testing.T) {
	cli := dockerCLI()
	if cli == "" {
		t.Skip("docker CLI not available")
	}
	if out, err := dockerRun(t, 30*time.Second, "info"); err != nil {
		t.Skipf("docker daemon not reachable: %v: %s", err, out)
	}
	c, _, owned := pgPanelEnv(t)
	ctx := context.Background()

	// Cleanup: OUR hosts + quantum nodes via the API (safe in both modes);
	// when we own the container, remove it and prove with docker ps -a.
	createdHosts := map[int]bool{}
	t.Cleanup(func() {
		hosts, err := c.ListHosts(ctx)
		if err == nil {
			for _, h := range hosts {
				if strings.HasPrefix(h.Remark, NamePrefix) || createdHosts[h.ID] {
					if err := c.DeleteHost(ctx, h.ID); err != nil {
						t.Logf("cleanup: host %d delete: %v", h.ID, err)
					}
				}
			}
		}
		nodes, err := c.ListNodes(ctx, "")
		if err == nil {
			for _, n := range nodes {
				if n.QuantumOwned() {
					_ = c.DeleteNode(ctx, n.ID)
				}
			}
		}
		if owned {
			_, _ = dockerRun(t, 60*time.Second, "rm", "-f", "quantum-test-dc-panel")
			_, _ = dockerRun(t, 60*time.Second, "volume", "rm", "quantum-test-dc-panel-data")
			out, _ := dockerRun(t, 30*time.Second, "ps", "-a", "--format", "{{.Names}}")
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "quantum-test-dc") {
					t.Errorf("cleanup left a container behind: %s", line)
				}
			}
			t.Logf("docker ps -a proof: no quantum-test-dc-* containers remain")
		}
	})

	// 1. One node record (per-DC model: ONE node per foreign server). The
	// node cannot actually dial in here — irrelevant: cores and hosts are
	// panel-side records.
	node, err := c.CreateNode(ctx, CreateRequest{
		Name: "quantum:docktest", Address: "127.0.0.1", Port: 62050, APIPort: 62051,
		UsageCoefficient: 1, ConnectionType: ConnectionTypeGRPC,
		ServerCA: selfSignedPEM(t), KeepAlive: 60, CoreConfigID: 1,
		APIKey: "11111111-2222-3333-4444-555555555555",
	})
	if err != nil {
		t.Fatalf("node create: %v", err)
	}
	t.Logf("node %d created (status %s)", node.ID, node.Status)

	// 2. Core snapshot + desired per-DC inbound.
	core, err := c.GetCore(ctx, node.CoreConfigID)
	if err != nil {
		t.Fatalf("core get: %v", err)
	}
	hostsBefore, err := c.ListHosts(ctx)
	if err != nil {
		t.Fatalf("hosts list: %v", err)
	}
	t.Logf("baseline: core %d config, %d hosts", node.CoreConfigID, len(hostsBefore))

	pvk, pbk, err := GenerateRealityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := RealityShortID(pvk)
	if err != nil {
		t.Fatal(err)
	}
	want := InboundPlan{
		Tag: DCInboundTag("docktest", "xray"), Listen: "127.0.0.1", Port: 24500,
		SNIs: []string{"q-docktest.example.com"}, Dest: "www.microsoft.com:443",
		ShortIDs: []string{sid}, PrivateKey: pvk, PublicKey: pbk,
		ClientAddress: "203.0.113.10",
	}

	// 3. Diff → exactly one create (dry-run touches nothing).
	plan := DiffDCInbounds(core["config"].(map[string]any), []InboundPlan{want}, hostsBefore, NamePrefix)
	if len(plan.Create) != 1 || plan.Create[0].Tag != want.Tag {
		t.Fatalf("plan: %+v", plan)
	}
	if !plan.Drift() || len(plan.HostsCreate) != 1 {
		t.Fatalf("plan shape: %+v", plan)
	}

	// 4. Apply: core PUT (restart_nodes=true) + host create.
	results, err := ApplyDCInbounds(ctx, c, node.CoreConfigID, plan)
	if err != nil {
		t.Fatalf("apply: %v (%v)", results, err)
	}
	t.Logf("apply: %v", results)

	// 5. Verify via GET /api/core: the inbound exists with the exact fields
	// and the operator's inbounds survive.
	core2, err := c.GetCore(ctx, node.CoreConfigID)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	for _, in := range extractInbounds(core2["config"].(map[string]any)) {
		if tag, _ := in["tag"].(string); tag == want.Tag {
			got = in
		}
	}
	if got == nil {
		t.Fatalf("inbound %s missing after apply", want.Tag)
	}
	if p, _ := numericInt(got["port"]); p != want.Port {
		t.Fatalf("inbound port: %v", got["port"])
	}
	rs := got["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	if rs["dest"] != want.Dest || rs["privateKey"] != want.PrivateKey {
		t.Fatalf("reality settings: %v", rs)
	}
	if snis, _ := stringSlice(rs["serverNames"]); len(snis) != 1 || snis[0] != want.SNIs[0] {
		t.Fatalf("serverNames: %v", snis)
	}
	if sids, _ := stringSlice(rs["shortIds"]); len(sids) != 1 || sids[0] != want.ShortIDs[0] {
		t.Fatalf("shortIds: %v", sids)
	}

	// 6. Verify via GET /api/hosts: address/port/SNI match.
	hostsAfter, err := c.ListHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var gotHost *Host
	for i := range hostsAfter {
		if hostsAfter[i].Remark == want.Tag {
			gotHost = &hostsAfter[i]
			createdHosts[hostsAfter[i].ID] = true
		}
	}
	if gotHost == nil {
		t.Fatalf("host %s missing after apply (have %d hosts)", want.Tag, len(hostsAfter))
	}
	if gotHost.Port != 443 || gotHost.InboundTag != want.Tag ||
		len(gotHost.Address) != 1 || gotHost.Address[0] != want.ClientAddress ||
		len(gotHost.SNI) != 1 || gotHost.SNI[0] != want.SNIs[0] {
		t.Fatalf("host mismatch: %+v (want addr=%s sni=%s)", gotHost, want.ClientAddress, want.SNIs[0])
	}
	t.Logf("host %d verified: remark=%s address=%s port=%d sni=%s",
		gotHost.ID, gotHost.Remark, gotHost.Address, gotHost.Port, gotHost.SNI)

	// 7. Idempotence: re-diff against the new state is clean; the second
	// apply touches nothing.
	plan2 := DiffDCInbounds(core2["config"].(map[string]any), []InboundPlan{want}, hostsAfter, NamePrefix)
	if plan2.Drift() {
		t.Fatalf("re-diff must be clean: %+v", plan2)
	}
	if _, err := ApplyDCInbounds(ctx, c, node.CoreConfigID, plan2); err != nil {
		t.Fatal(err)
	}
	final, err := c.ListHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != len(hostsAfter) {
		t.Fatalf("second apply changed the host count: %d -> %d", len(hostsAfter), len(final))
	}
	fmt.Printf("integration dc-inbounds: PASS (node %d, inbound %s, host verified, no-op apply clean)\n",
		node.ID, want.Tag)
	_ = io.Discard
}
