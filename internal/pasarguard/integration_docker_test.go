//go:build pgintegration

// integration_docker_test.go — the reconciler against a REAL PasarGuard
// panel (started by tests/pasarguard_docker_integration.sh). Env:
//   PG_PANEL_URL, PG_ADMIN_USER, PG_ADMIN_PASS
// Flow: connect → 2 nodes exist on the panel (created directly with this
// client, real self-signed certs) → coefficient drift via a raw modify →
// Diff detects it → apply restores → reconnect → delete-orphans refuses a
// foreign node. Never runs anything on other hosts; only talks HTTP.

package pasarguard

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

func integrationEnv(t *testing.T) (*Client, string, string) {
	t.Helper()
	url := os.Getenv("PG_PANEL_URL")
	user := os.Getenv("PG_ADMIN_USER")
	pass := os.Getenv("PG_ADMIN_PASS")
	if url == "" || user == "" || pass == "" {
		t.Skip("integration env not set (run tests/pasarguard_docker_integration.sh)")
	}
	tlsOpts := TLSOptions{}
	if ca := os.Getenv("PG_PANEL_CA"); ca != "" {
		b, err := os.ReadFile(ca)
		if err != nil {
			t.Fatalf("panel CA: %v", err)
		}
		tlsOpts.CAPem = string(b)
	}
	c, err := New(url, Credentials{Username: user, Password: pass}, tlsOpts, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c, user, pass
}

func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "quantum-test-node"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:     true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestIntegrationRealPanel(t *testing.T) {
	cl, _, _ := integrationEnv(t)
	ctx := context.Background()

	// 0. The panel answers: list (may be empty or contain leftovers from a
	// previous run — record the baseline).
	before, err := cl.ListNodes(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	t.Logf("baseline: %d nodes on the panel", len(before))

	// 1. Create two REAL node records (upstream-validated: real certs, UUID
	// api keys matching the two quantum-test node containers).
	addrA := getenvOr(t, "PG_NODE_A_ADDR", "172.20.0.11")
	addrB := getenvOr(t, "PG_NODE_B_ADDR", "172.20.0.12")
	desired := []DesiredNode{
		{Name: DesiredName("drak", "hz-a"), Address: addrA, Coefficient: 1.5,
			ServerCA: selfSignedPEM(t), APIKey: "11111111-2222-3333-4444-555555555555"},
		{Name: DesiredName("chekavak", "hz-b"), Address: addrB, Coefficient: 1.0,
			ServerCA: selfSignedPEM(t), APIKey: "aaaaaaaabbbbccccddddeeeeffff0000"},
	}
	plan := Diff(desired, before)
	if plan.Creates == 0 {
		t.Logf("nodes already exist (re-run): %d creates skipped", plan.Creates)
	}
	for _, r := range Apply(ctx, plan, desired, cl) {
		if strings.HasPrefix(r.Detail, "FAILED") {
			t.Fatalf("apply row %s failed: %s", r.Name, r.Detail)
		}
		t.Logf("apply: %s -> %s", r.Name, r.Detail)
	}

	// 2. Both records exist upstream with the right coefficients.
	real, err := cl.ListNodes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Node{}
	for _, n := range real {
		byName[n.Name] = n
	}
	for _, d := range desired {
		n, ok := byName[d.Name]
		if !ok {
			t.Fatalf("node %s missing after apply (panel has %d nodes)", d.Name, len(real))
		}
		if n.UsageCoefficient != d.Coefficient {
			t.Fatalf("%s coefficient = %v, want %v", d.Name, n.UsageCoefficient, d.Coefficient)
		}
		t.Logf("%s: id=%d status=%s coefficient=%v", d.Name, n.ID, n.Status, n.UsageCoefficient)
	}

	// 3. Drift: change a coefficient OUT-OF-BAND (raw modify, like a
	// PasarGuard-side operator edit), then Diff must flag exactly that.
	drak := byName[desired[0].Name]
	newCoef := 9.0
	if _, err := cl.ModifyNode(ctx, drak.ID, ModifyRequest{UsageCoefficient: &newCoef}); err != nil {
		t.Fatalf("out-of-band coefficient edit: %v", err)
	}
	real, _ = cl.ListNodes(ctx, "")
	plan2 := Diff(desired, real)
	if plan2.Updates != 1 {
		t.Fatalf("drift not detected: %d updates (want 1): %+v", plan2.Updates, plan2.Actions)
	}
	// 4. Apply restores the desired coefficient.
	Apply(ctx, plan2, desired, cl)
	real, _ = cl.ListNodes(ctx, "")
	for _, n := range real {
		if n.Name == desired[0].Name && n.UsageCoefficient != 1.5 {
			t.Fatalf("coefficient restore failed: %+v", n)
		}
	}
	// 5. Reconnect path (the node containers may or may not have completed
	// the gRPC handshake yet — the panel answers regardless).
	if err := cl.ReconnectNode(ctx, drak.ID); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	// 6. A foreign node is never deletable through DeleteOrphans: create one
	// with a bare name, then attempt its id.
	foreign, err := cl.CreateNode(ctx, CreateRequest{
		Name: "operator-foreign", Address: "127.0.0.1", Port: 62050, APIPort: 62051,
		UsageCoefficient: 1, ConnectionType: ConnectionTypeGRPC,
		ServerCA: selfSignedPEM(t), KeepAlive: 60, CoreConfigID: 1,
		APIKey: "99999999-8888-7777-6666-555555555555",
	})
	if err != nil {
		t.Fatalf("foreign create: %v", err)
	}
	out, err := DeleteOrphans(ctx, []int{foreign.ID}, cl)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 || !containsRefused(out) {
		t.Fatalf("foreign node must be REFUSED: %+v", out)
	}
	after, _ := cl.ListNodes(ctx, "")
	if !nodeExists(after, "operator-foreign") {
		t.Fatal("foreign node must survive delete-orphans")
	}
	// 7. Cleanup of OUR nodes (explicit, owned-only).
	var ours []int
	for _, n := range after {
		if n.QuantumOwned() {
			ours = append(ours, n.ID)
		}
	}
	if _, err := DeleteOrphans(ctx, ours, cl); err != nil {
		t.Fatal(err)
	}
	final, _ := cl.ListNodes(ctx, "")
	for _, n := range final {
		if n.QuantumOwned() {
			t.Fatalf("our node %s survived cleanup", n.Name)
		}
	}
	fmt.Printf("integration: PASS (%d nodes touched, all quantum nodes cleaned)\n", len(ours))
}

func containsRefused(actions []PlanAction) bool {
	for _, a := range actions {
		if len(a.Detail) > 0 && (a.Detail == "REFUSED: node is not Quantum-owned (missing quantum: prefix)" ||
			len(a.Detail) > 7 && a.Detail[:7] == "REFUSED") {
			return true
		}
	}
	return false
}

func getenvOr(t *testing.T, key, def string) string {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func nodeExists(nodes []Node, name string) bool {
	for _, n := range nodes {
		if n.Name == name {
			return true
		}
	}
	return false
}
