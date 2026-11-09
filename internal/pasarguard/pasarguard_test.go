// pasarguard_test.go — unit tests: fake panel HTTP server (token + node
// CRUD + usage), retry/backoff, TLS pinning, credentials encryption at
// rest, and the reconciler diff/apply (never-delete contract).

package pasarguard

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testUser = "quantum-admin"
const testPass = "q-secret-9"

// fakePanel is a minimal upstream-shaped panel: OAuth2 token endpoint,
// node CRUD, reconnect, usage, and (phase 14) the core-config + host
// records the per-DC inbound reconciler drives.
type fakePanel struct {
	t           *testing.T
	srv         *httptest.Server
	nodes       map[int]Node
	nextID      int
	tokenCalls  int
	failNextGet int // make N GETs return 500 (retry test)

	// Per-DC state: one default core (upstream ships core id 1) plus host
	// records, with the counters the no-op/dry-run assertions read.
	cores            map[int]*fakeCore
	hosts            map[int]Host
	hostOrder        []int
	nextHostID       int
	corePuts         int
	hostCreates      int
	hostModifies     int
	hostDeletes      int
	restartNodesLast string // raw query of the last core PUT
}

// fakeCore is the upstream core record (app/models/core.py CoreResponse:
// id/name/type/config + the exclude/fallback tag sets).
type fakeCore struct {
	ID                   int            `json:"id"`
	Name                 string         `json:"name"`
	Type                 string         `json:"type"`
	Config               map[string]any `json:"config"`
	ExcludeInboundTags   []string       `json:"exclude_inbound_tags"`
	FallbacksInboundTags []string       `json:"fallbacks_inbound_tags"`
}

func newFakePanel(t *testing.T) *fakePanel {
	t.Helper()
	f := &fakePanel{t: t, nodes: map[int]Node{}, nextID: 1}
	// The upstream default core: one operator-owned inbound Quantum must
	// never touch.
	f.cores = map[int]*fakeCore{1: {
		ID: 1, Name: "default-core", Type: "xray",
		Config: map[string]any{"inbounds": []any{
			map[string]any{
				"tag": "INBOUND_0", "listen": "0.0.0.0", "port": 62050,
				"protocol": "vless",
				"settings": map[string]any{"clients": []any{}, "decryption": "none"},
			},
		}},
		ExcludeInboundTags: []string{}, FallbacksInboundTags: []string{},
	}}
	f.hosts = map[int]Host{}
	f.nextHostID = 1
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/token", f.handleToken)
	mux.HandleFunc("GET /api/nodes", f.handleList)
	// Upstream 5.2.1/5.6: create/modify/delete/reconnect are SINGULAR
	// (/api/node...); only the collection GET is /api/nodes.
	mux.HandleFunc("POST /api/node", f.handleCreate)
	mux.HandleFunc("PUT /api/node/", f.handleModify)
	mux.HandleFunc("DELETE /api/node/", f.handleDelete)
	mux.HandleFunc("POST /api/node/{id}/reconnect", f.handleReconnect)
	mux.HandleFunc("GET /api/node/usage", f.handleUsage)
	// Core config (the Xray JSON lives INSIDE the core) + host records.
	mux.HandleFunc("GET /api/core/{id}", f.handleCoreGet)
	mux.HandleFunc("PUT /api/core/{id}", f.handleCorePut)
	mux.HandleFunc("POST /api/host/", f.handleHostCreate)
	mux.HandleFunc("GET /api/hosts", f.handleHostsList)
	mux.HandleFunc("PUT /api/host/{id}", f.handleHostModify)
	mux.HandleFunc("DELETE /api/host/{id}", f.handleHostDelete)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePanel) coreByID(r *http.Request) (*fakeCore, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		return nil, false
	}
	c, ok := f.cores[id]
	return c, ok
}

func (f *fakePanel) handleCoreGet(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	c, ok := f.coreByID(r)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Core config not found"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(c)
}

func (f *fakePanel) handleCorePut(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	c, ok := f.coreByID(r)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Core config not found"}`))
		return
	}
	var body CoreModifyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Config == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.corePuts++
	f.restartNodesLast = r.URL.RawQuery
	c.Name = body.Name
	c.Type = body.Type
	c.Config = body.Config
	c.ExcludeInboundTags = body.ExcludeInboundTags
	c.FallbacksInboundTags = body.FallbacksInboundTags
	_ = json.NewEncoder(w).Encode(c)
}

func (f *fakePanel) handleHostCreate(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req HostCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	h := Host{
		ID: f.nextHostID, Remark: req.Remark, Address: req.Address,
		InboundTag: req.InboundTag, Port: req.Port, SNI: req.SNI,
		Security: req.Security,
	}
	if h.Security == "" {
		h.Security = "inbound_default"
	}
	f.nextHostID++
	f.hosts[h.ID] = h
	f.hostOrder = append(f.hostOrder, h.ID)
	f.hostCreates++
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(h)
}

func (f *fakePanel) handleHostsList(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	out := []Host{}
	for _, id := range f.hostOrder {
		out = append(out, f.hosts[id])
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakePanel) handleHostModify(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h, ok := f.hosts[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Host not found"}`))
		return
	}
	var req HostCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.hostModifies++
	h.Remark = req.Remark
	h.Address = req.Address
	h.InboundTag = req.InboundTag
	h.Port = req.Port
	h.SNI = req.SNI
	h.Security = req.Security
	f.hosts[id] = h
	_ = json.NewEncoder(w).Encode(h)
}

func (f *fakePanel) handleHostDelete(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if _, ok := f.hosts[id]; !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	delete(f.hosts, id)
	for i, oid := range f.hostOrder {
		if oid == id {
			f.hostOrder = append(f.hostOrder[:i], f.hostOrder[i+1:]...)
			break
		}
	}
	f.hostDeletes++
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakePanel) handleToken(w http.ResponseWriter, r *http.Request) {
	f.tokenCalls++
	if err := r.ParseForm(); err != nil || r.Form.Get("username") != testUser || r.Form.Get("password") != testPass {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Incorrect username or password"}`))
		return
	}
	_, _ = w.Write([]byte(`{"access_token":"tok-1","token_type":"bearer"}`))
}

func (f *fakePanel) authed(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-1")
}

func (f *fakePanel) handleList(w http.ResponseWriter, r *http.Request) {
	if f.failNextGet > 0 {
		f.failNextGet--
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"transient"}`))
		return
	}
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	out := struct {
		Nodes []Node `json:"nodes"`
		Total int    `json:"total"`
	}{Nodes: []Node{}}
	for _, n := range f.nodes {
		out.Nodes = append(out.Nodes, n)
	}
	out.Total = len(out.Nodes)
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakePanel) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	for _, n := range f.nodes {
		if n.Name == req.Name {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"detail":"Node already exists"}`))
			return
		}
	}
	n := Node{
		ID: f.nextID, Name: req.Name, Address: req.Address, Port: req.Port,
		APIPort: req.APIPort, UsageCoefficient: req.UsageCoefficient,
		ConnectionType: req.ConnectionType, ServerCA: req.ServerCA,
		KeepAlive: req.KeepAlive, CoreConfigID: req.CoreConfigID,
		APIKey: req.APIKey, Status: StatusConnecting,
	}
	f.nextID++
	f.nodes[n.ID] = n
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(n)
}

func (f *fakePanel) handleModify(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var id int
	if _, err := fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/api/node/"), "%d", &id); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	n, ok := f.nodes[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Node not found"}`))
		return
	}
	var req ModifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if req.UsageCoefficient != nil {
		n.UsageCoefficient = *req.UsageCoefficient
	}
	if req.Address != nil {
		n.Address = *req.Address
	}
	f.nodes[id] = n
	_ = json.NewEncoder(w).Encode(n)
}

func (f *fakePanel) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var id int
	if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/node/"), "/"), "%d", &id); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if _, ok := f.nodes[id]; !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	delete(f.nodes, id)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakePanel) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}

func (f *fakePanel) handleUsage(w http.ResponseWriter, r *http.Request) {
	if !f.authed(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var nodeID int
	if v := r.URL.Query().Get("node_id"); v != "" {
		fmt.Sscanf(v, "%d", &nodeID)
	}
	_ = json.NewEncoder(w).Encode([]UsageRow{{NodeID: nodeID, Uplink: 1111, Downlink: 2222}})
}

func testClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(base, Credentials{Username: testUser, Password: testPass}, TLSOptions{}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClientTokenAndCRUD(t *testing.T) {
	f := newFakePanel(t)
	c := testClient(t, f.srv.URL)
	ctx := context.Background()

	n, err := c.CreateNode(ctx, CreateRequest{
		Name: NamePrefix + "drak", Address: "195.201.0.1", Port: 62050, APIPort: 62051,
		UsageCoefficient: 1.5, ConnectionType: ConnectionTypeGRPC,
		ServerCA:  "-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----",
		KeepAlive: 60, CoreConfigID: 1, APIKey: "12345678-1234-1234-1234-123456789abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != 1 || n.Name != "quantum:drak" {
		t.Fatalf("create: %+v", n)
	}
	if f.tokenCalls != 1 {
		t.Fatalf("token calls = %d, want 1 (reused)", f.tokenCalls)
	}
	got, err := c.ListNodes(ctx, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v %v", got, err)
	}
	coef := 2.25
	if _, err := c.ModifyNode(ctx, n.ID, ModifyRequest{UsageCoefficient: &coef}); err != nil {
		t.Fatal(err)
	}
	if f.nodes[1].UsageCoefficient != 2.25 {
		t.Fatalf("coefficient not applied: %+v", f.nodes[1])
	}
	if err := c.ReconnectNode(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	up, down, degraded, err := c.NodeUsage(ctx, n.ID, time.Now().Add(-time.Hour), time.Now())
	if err != nil || degraded || up != 1111 || down != 2222 {
		t.Fatalf("usage: %d %d degraded=%v err=%v", up, down, degraded, err)
	}
}

func TestClientRetriesOnTransient5xx(t *testing.T) {
	f := newFakePanel(t)
	c := testClient(t, f.srv.URL)
	f.failNextGet = 2
	nodes, err := c.ListNodes(context.Background(), "")
	if err != nil || len(nodes) != 0 {
		t.Fatalf("list after retries: %v %v", nodes, err)
	}
	if f.failNextGet != 0 {
		t.Fatal("retry did not consume the injected failures")
	}
}

func TestClientBadLoginIsTyped(t *testing.T) {
	f := newFakePanel(t)
	c, err := New(f.srv.URL, Credentials{Username: testUser, Password: "WRONG"}, TLSOptions{}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListNodes(context.Background(), "")
	if err == nil {
		t.Fatal("bad credentials must fail")
	}
	if !strings.Contains(fmt.Sprint(err), "401") {
		t.Fatalf("error should mention HTTP 401, got: %v", err)
	}
	if strings.Contains(fmt.Sprint(err), "WRONG") {
		t.Fatal("error must never contain the password")
	}
}

func TestClientTLSPinning(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "panel-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:     true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	_ = der // (the httptest server uses its own cert; pinned below)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"t","token_type":"bearer"}`))
	}))
	defer srv.Close()

	leaf := srv.Certificate()
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	pin := base64.StdEncoding.EncodeToString(sum[:])
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
	cc, err := New(srv.URL, Credentials{Username: testUser, Password: testPass},
		TLSOptions{CAPem: string(caPEM), PinSHA256: pin}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := cc.login(context.Background()); err != nil {
		t.Fatalf("login with correct pin failed: %v", err)
	}
	bad, err := New(srv.URL, Credentials{Username: testUser, Password: testPass},
		TLSOptions{CAPem: string(caPEM), PinSHA256: base64.StdEncoding.EncodeToString(make([]byte, 32))}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.login(context.Background()); err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("wrong pin must fail with a pin error, got: %v", err)
	}
}

func TestSecretboxRoundTripAndKeyFile(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SecretKeyPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	sealed, err := Seal(key, []byte(`{"password":"hunter2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "hunter2") {
		t.Fatal("sealed value must not contain the plaintext")
	}
	pt, err := Open(key, sealed)
	if err != nil || string(pt) != `{"password":"hunter2"}` {
		t.Fatalf("open: %s %v", pt, err)
	}
	other := make([]byte, 32)
	_, _ = rand.Read(other)
	if _, err := Open(other, sealed); err == nil {
		t.Fatal("wrong key must fail")
	}
	if _, err := Open(key, sealed[:len(sealed)-4]+"AAAA"); err == nil {
		t.Fatal("tampered value must fail")
	}
}

func TestDiffAndApply(t *testing.T) {
	f := newFakePanel(t)
	c := testClient(t, f.srv.URL)
	ctx := context.Background()

	desired := []DesiredNode{
		{Name: "quantum:drak", Address: "195.201.0.1", Coefficient: 1.5,
			ServerCA: "-----BEGIN CERTIFICATE-----X-----END CERTIFICATE-----",
			APIKey:   "12345678-1234-1234-1234-123456789abc"},
		{Name: "quantum:chekavak", Address: "195.201.0.2", Coefficient: 1.0,
			ServerCA: "-----BEGIN CERTIFICATE-----Y-----END CERTIFICATE-----",
			APIKey:   "abcdef01-2345-6789-abcd-ef0123456789"},
	}

	plan := Diff(desired, nil)
	if plan.Creates != 2 || !plan.Drift {
		t.Fatalf("empty plan: %+v", plan)
	}
	Apply(ctx, plan, desired, c)
	if len(f.nodes) != 2 {
		t.Fatalf("nodes after apply: %d", len(f.nodes))
	}

	// Coefficient drift on drak (by name — creation order is alphabetical)
	// + a foreign node + a stale quantum node. Phase 14 data-path decision:
	// the coefficient is NO LONGER part of the desired state, so drift on
	// it is invisible to the diff and Apply never touches it.
	var drakID int
	for _, n := range f.nodes {
		if n.Name == "quantum:drak" {
			drakID = n.ID
		}
	}
	if drakID == 0 {
		t.Fatal("drak node missing")
	}
	drifted := f.nodes[drakID]
	drifted.UsageCoefficient = 9.9
	f.nodes[drakID] = drifted
	f.nodes[f.nextID] = Node{ID: f.nextID, Name: "operator-node", Status: StatusConnected}
	f.nextID++
	f.nodes[f.nextID] = Node{ID: f.nextID, Name: "quantum:old", Status: StatusConnected}
	f.nextID++

	real, err := c.ListNodes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	plan2 := Diff(desired, real)
	if plan2.Updates != 0 {
		t.Fatalf("coefficient drift must NOT produce an update (phase 14): %+v", plan2)
	}
	stale, foreign, okRows := 0, 0, 0
	for _, a := range plan2.Actions {
		switch a.Action {
		case ActionStale:
			stale++
		case ActionForeign:
			foreign++
		case ActionOK:
			okRows++
		}
	}
	if stale != 1 || foreign != 1 || okRows != 2 {
		t.Fatalf("stale=%d foreign=%d ok=%d, want 1/1/2: %+v", stale, foreign, okRows, plan2.Actions)
	}
	if !plan2.Drift {
		t.Fatalf("stale node must still mark drift: %+v", plan2)
	}
	// Apply NEVER deletes: stale/foreign rows pass through untouched.
	Apply(ctx, plan2, desired, c)
	if len(f.nodes) != 4 {
		t.Fatalf("apply must not delete: %d nodes on the panel", len(f.nodes))
	}

	// The operator's coefficient is THEIRS: Apply left the drifted value
	// alone (no restore, no modify).
	for _, n := range mustList(t, c) {
		if n.Name == "quantum:drak" && n.UsageCoefficient != 9.9 {
			t.Fatalf("apply must not manage the upstream coefficient: %+v", n)
		}
	}

	// DeleteOrphans refuses foreign nodes and removes owned ones.
	out, err := DeleteOrphans(ctx, []int{4, 3}, c) // 4=quantum:old (ours), 3=operator-node
	if err != nil {
		t.Fatal(err)
	}
	if len(f.nodes) != 3 {
		t.Fatalf("after delete-orphans: %d nodes (want 3)", len(f.nodes))
	}
	for _, a := range out {
		if a.NodeID == 2 && !strings.Contains(a.Detail, "REFUSED") {
			t.Fatalf("foreign node must be REFUSED: %+v", a)
		}
	}
}

func TestDesiredNameKeepsPrefixContract(t *testing.T) {
	// The reconciler-side ownership contract lives in DesiredName (client
	// create deliberately accepts bare names: it is a transport).
	if n := DesiredName("drak", "hz-a"); !strings.HasPrefix(n, NamePrefix) {
		t.Fatalf("DesiredName lost the prefix: %q", n)
	}
}

func mustList(t *testing.T, c *Client) []Node {
	t.Helper()
	nodes, err := c.ListNodes(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}
