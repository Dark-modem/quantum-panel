package panel

// Session 1 — Matrix Agent MVP failure-recovery audit + fix verification
// (docs/AGENT-RECOVERY-AUDIT.md).
//
// Failure-injection tests at the CONTROLLER level only. The S* tests pin
// the five recovery scenarios from the audit brief; the Fix* tests are
// regression tests for the F1/F2/F3 repairs implemented on top of the
// audit (tombstoned deletion, teardown delivery guard, stage gates,
// startup reconciliation, main_host preservation). Where the audit pinned
// a defect, the assertion was FLIPPED to assert the repaired behavior —
// the audit report records the original pin for history.
//
// Agent-side behavior (revision rejection, pending-state persistence
// inside the headless runtime) stays out of scope here and is exercised
// only through the controller-visible seams.

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// agentRecoveryWait polls cond until it holds or the timeout elapses.
func agentRecoveryWait(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	defer agentObserverWait() // ack/relay goroutines must finish before asserts
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// agentRecoveryTunnel stores one tunnel record directly, the way a create
// handler would (before the provisioning goroutine touches it).
func agentRecoveryTunnel(t *testing.T, id, instance, hubID, nodeID, state, hubRev, nodeRev string) {
	t.Helper()
	agentObserverWait()
	s := loadAgentTunnels()
	s.Tunnels = append(s.Tunnels, agentTunnel{
		ID: id, Name: id, Instance: instance, HubAgentID: hubID, NodeAgentID: nodeID,
		Family: "ipv4", MeshPort: 2097, Ports: []int{8080},
		Token: strings.Repeat("k", 32), Active: true,
		State: state, HubRevision: hubRev, NodeRevision: nodeRev,
		CreatedAt: time.Now().Unix(),
	})
	if err := saveAgentTunnels(s); err != nil {
		t.Fatal(err)
	}
}

// agentRecoverySeedRegistry writes a real agents.json with one offline
// agent so the REAL delivery path (agentHubDeliverDesired/agentPendingDesired)
// can be exercised without a live WebSocket.
func agentRecoverySeedRegistry(t *testing.T, id string) {
	t.Helper()
	agents := []agentRecord{{
		ID: id, Name: id, KeyHash: agentSha256Hex("testkey"), Role: "foreign",
		Caps: []string{agentCapMatrix}, Status: agentStatusOffline,
		CreatedAt: time.Now().Unix(),
	}}
	agentRegMu.Lock()
	defer agentRegMu.Unlock()
	if err := saveAgentRegistry(agents, nil); err != nil {
		t.Fatal(err)
	}
}

// agentRecoveryDesired builds a matrix desired with a fixed revision.
func agentRecoveryDesired(rev, instance, action string) agentDesired {
	return agentDesired{Revision: rev, Matrix: &agentDesiredMatrix{Instance: instance, Action: action}}
}

// agentRecoveryDelete runs the delete handler against a tunnel id.
func agentRecoveryDelete(t *testing.T, id string) {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/api/agent-tunnels/"+id, nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	handleAgentTunnelDelete(rr, req)
	if rr.Code != 200 {
		t.Fatalf("delete failed: %d %s", rr.Code, rr.Body.String())
	}
}

// --- S1: partial provisioning (hub ok, node fails). Unchanged by the
// fixes: state=error, no automatic rollback desired reaches the hub, no
// retry runs by itself (reconcile retries error records only on the next
// controller start — exactly once, never on a timer).

func TestAgentRecoveryS1PartialProvisioningNoRollback(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	agentRecoveryTunnel(t, "at_s1", "matrix", "hub1", "node1", agentTunnelStateProvisioning, "", "")

	go func() {
		agentRecoveryWait(t, 3*time.Second, "hub desired delivery", func() bool {
			return len(lab.desiresFor("hub1")) >= 1
		})
		hubRev := lab.desiresFor("hub1")[0].Revision
		agentTunnelAckObserved("hub1", agentAck{Revision: hubRev, OK: true,
			Detail: map[string]string{"main_host": "Subnet = 10.254.231.1/32"}})
		agentObserverWait()
		agentRecoveryWait(t, 3*time.Second, "node desired delivery", func() bool {
			return len(lab.desiresFor("node1")) >= 1
		})
		nodeRev := lab.desiresFor("node1")[0].Revision
		agentTunnelAckObserved("node1", agentAck{Revision: nodeRev, OK: false, Error: "apt install tinc failed"})
	}()
	agentObserverWait()

	agentTunnelProvision("at_s1")

	agentObserverWait()
	s := loadAgentTunnels()
	if len(s.Tunnels) != 1 || s.Tunnels[0].State != agentTunnelStateError {
		t.Fatalf("tunnel must end in error state, got %+v", s.Tunnels)
	}
	if !strings.Contains(s.Tunnels[0].Error, "apt install tinc failed") {
		t.Fatalf("error must carry the node failure: %+v", s.Tunnels[0])
	}
	// The hub side received exactly one apply and NO rollback remove after
	// the node failure: cleanup is an explicit delete (tombstoned) or the
	// one-shot reconcile retry on the next controller start.
	for _, d := range lab.desiresFor("hub1") {
		if d.Matrix != nil && d.Matrix.Action == "remove" {
			t.Fatalf("hub received an unexpected rollback remove: %+v", d)
		}
	}
	if got := len(lab.desiresFor("hub1")); got != 1 {
		t.Fatalf("hub should see exactly one apply, got %d", got)
	}
	// While the error record exists the instance stays allocated.
	if got := agentTunnelNextInstance(s.Tunnels, "hub1"); got != "matrix2" {
		t.Fatalf("instance must stay allocated while the record exists, got %q", got)
	}
}

// --- S2: deleting while an agent is offline. The remove desired IS
// retained and — since the F2 fix — can no longer be silently replaced by
// later work: the delivery guard refuses an apply (or a different
// instance's remove) over a pending teardown, while a same-instance
// remove refresh stays allowed.

func TestAgentRecoveryS2OfflineDeleteRetainedThenGuarded(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t) // sets configDir to a temp dir
	_ = lab
	agentRecoverySeedRegistry(t, "node1")

	remove := agentRecoveryDesired("1000", "matrix", "remove")
	if err := agentHubDeliverDesired("node1", remove); err != nil {
		t.Fatalf("offline delivery must not be an error, got %v", err)
	}
	pending := agentPendingDesired("node1")
	if pending == nil || pending.Matrix == nil || pending.Matrix.Action != "remove" {
		t.Fatalf("offline teardown must be retained, got %+v", pending)
	}

	// FIX F2: an apply over the pending teardown is refused and the queued
	// teardown survives untouched.
	err := agentHubDeliverDesired("node1", agentRecoveryDesired("2000", "matrix", "apply"))
	if err == nil {
		t.Fatal("apply over a pending teardown must be refused")
	}
	pending = agentPendingDesired("node1")
	if pending == nil || pending.Revision != "1000" || pending.Matrix == nil || pending.Matrix.Action != "remove" {
		t.Fatalf("queued teardown must survive the refused apply, got %+v", pending)
	}

	// A same-instance remove refresh is allowed (supersedes in place).
	if err := agentHubDeliverDesired("node1", agentRecoveryDesired("1001", "matrix", "remove")); err != nil {
		t.Fatalf("same-instance remove refresh must be allowed: %v", err)
	}
	if p := agentPendingDesired("node1"); p == nil || p.Revision != "1001" {
		t.Fatalf("same-instance remove refresh must land, got %+v", p)
	}
}

// --- S3: controller restart. The late hub ack keeps its artifacts
// (FIX F3: the ack observer persists main_host), the record stays parked
// until the one-shot reconciler offers it for a resume.

func TestAgentRecoveryS3RestartProvisioningPreservesHostAndResumes(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	// Fresh "process": the record was persisted by the previous process
	// and only its disk state remains; no waiter is armed for r-hub-42.
	agentRecoveryTunnel(t, "at_s3a", "matrix", "hub1", "node1", agentTunnelStateProvisioning, "r-hub-42", "")

	// The hub agent reconnects, gets the still-pending desired from the
	// registry, applies it, and acks — after the controller restarted.
	agentTunnelAckObserved("hub1", agentAck{Revision: "r-hub-42", OK: true,
		Detail: map[string]string{"main_host": "Subnet = 10.254.231.1/32"}})
	agentObserverWait()

	agentObserverWait()
	s := loadAgentTunnels()
	rec := s.Tunnels[0]
	if !rec.HubAckOK {
		t.Fatalf("late hub ack must be recorded, got %+v", rec)
	}
	// FIX F3: the observer now stores ack.Detail["main_host"], so the
	// resume data survives the restart.
	if rec.HubMainHost != "Subnet = 10.254.231.1/32" {
		t.Fatalf("main_host must survive via the ack observer, got %+v", rec)
	}
	if rec.State != agentTunnelStateProvisioning {
		t.Fatalf("provisioning record stays parked until reconcile, got %q", rec.State)
	}
	if got := len(lab.desiresFor("node1")); got != 0 {
		t.Fatalf("nothing may run before reconcile, got %d node desires", got)
	}
	// The reconciler must offer exactly this record for a resume.
	provision, teardown := reconcileAgentTunnels()
	if len(provision) != 1 || provision[0] != "at_s3a" || len(teardown) != 0 {
		t.Fatalf("reconcile must resume the parked record, got provision=%v teardown=%v", provision, teardown)
	}
	agentObserverWait()
	if s := loadAgentTunnels(); s.Tunnels[0].State != agentTunnelStateProvisioning {
		t.Fatalf("reconcile keeps the record in provisioning for the resume run, got %q", s.Tunnels[0].State)
	}
}

func TestAgentRecoveryS3RestartRegisteringRecoversViaLateAck(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	agentRecoveryTunnel(t, "at_s3b", "matrix", "hub1", "node1", agentTunnelStateRegistering, "r-hub-1", "r-node-77")

	// Node reconnects post-restart, re-applies the pending bundle (equal
	// revision = agent-side no-op) and acks again; the ack observer still
	// matches by revision and completes the tunnel.
	agentTunnelAckObserved("node1", agentAck{Revision: "r-node-77", OK: true})

	agentObserverWait()
	s := loadAgentTunnels()
	agentObserverWait()
	if s.Tunnels[0].State != agentTunnelStateActive {
		t.Fatalf("registering tunnel must complete via the late ack, got %+v", s.Tunnels[0])
	}
}

// --- S4a (audit F1, fixed): deleting a tunnel mid-provisioning must never
// orphan agent-side instances. The tombstone takes ownership, the stage
// gates abort the in-flight pipeline before the node stage, both endpoints
// receive the remove desired, and the tombstone is dropped once both
// teardown acks arrive.

func TestAgentRecoveryS4DeleteDuringProvisioningNoOrphan(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	agentRecoveryTunnel(t, "at_s4", "matrix", "hub1", "node1", agentTunnelStateProvisioning, "", "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		agentTunnelProvision("at_s4")
	}()
	agentRecoveryWait(t, 3*time.Second, "hub desired delivery", func() bool {
		return len(lab.desiresFor("hub1")) >= 1
	})

	// Delete while the provisioning goroutine waits for the hub ack.
	agentRecoveryDelete(t, "at_s4")
	agentRecoveryWait(t, 3*time.Second, "node teardown desired", func() bool {
		for _, d := range lab.desiresFor("node1") {
			if d.Matrix != nil && d.Matrix.Action == "remove" {
				return true
			}
		}
		return false
	})

	// Let the hub ack land; the stage gate must abort the pipeline.
	hubApplyRev := lab.desiresFor("hub1")[0].Revision
	agentTunnelAckObserved("hub1", agentAck{Revision: hubApplyRev, OK: true,
		Detail: map[string]string{"main_host": "Subnet = 10.254.231.1/32"}})
	agentObserverWait()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("provisioning goroutine did not finish")
	}

	// The node NEVER received the apply for the deleted tunnel.
	for _, d := range lab.desiresFor("node1") {
		if d.Matrix != nil && d.Matrix.Action == "apply" {
			t.Fatalf("node must not receive an apply after delete, got %+v", d)
		}
	}
	// The hub — which did apply — receives its remove (delete flow).
	hubSawRemove := false
	for _, d := range lab.desiresFor("hub1") {
		if d.Matrix != nil && d.Matrix.Action == "remove" {
			hubSawRemove = true
		}
	}
	if !hubSawRemove {
		t.Fatalf("hub must receive the teardown remove, got %+v", lab.desiresFor("hub1"))
	}
	// The record is a tombstone, not gone.
	agentObserverWait()
	s := loadAgentTunnels()
	if len(s.Tunnels) != 1 || s.Tunnels[0].State != agentTunnelStateDeleting {
		t.Fatalf("record must be tombstoned, got %+v", s.Tunnels)
	}
	// Both teardown acks complete the cleanup: record dropped, instance freed.
	hubRemRev, nodeRemRev := s.Tunnels[0].HubRevision, s.Tunnels[0].NodeRevision
	agentTunnelAckObserved("hub1", agentAck{Revision: hubRemRev, OK: true})
	agentTunnelAckObserved("node1", agentAck{Revision: nodeRemRev, OK: true})
	agentObserverWait()
	s = loadAgentTunnels()
	if len(s.Tunnels) != 0 {
		t.Fatalf("tombstone must be dropped after both teardown acks, got %+v", s.Tunnels)
	}
	if got := agentTunnelNextInstance(s.Tunnels, "hub1"); got != "matrix" {
		t.Fatalf("instance name must be freed after the tombstone is dropped, got %q", got)
	}
}

// --- S4b (audit F2, fixed): the instance name stays reserved while the
// tombstone waits for teardown acks, and is freed only afterwards.

func TestAgentRecoveryS4InstanceReservedWhileTeardownPending(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	agentRecoveryTunnel(t, "at_old", "matrix", "hub1", "node1", agentTunnelStateActive, "r1", "r2")

	agentRecoveryDelete(t, "at_old")
	agentRecoveryWait(t, 3*time.Second, "teardown desired", func() bool {
		return len(lab.desiresFor("node1")) >= 1 && len(lab.desiresFor("hub1")) >= 1
	})

	agentObserverWait()
	s := loadAgentTunnels()
	if len(s.Tunnels) != 1 || s.Tunnels[0].State != agentTunnelStateDeleting {
		t.Fatalf("tombstone must exist, got %+v", s.Tunnels)
	}
	// FIX F2: no reuse while the old instance may still run.
	if got := agentTunnelNextInstance(s.Tunnels, "hub1"); got != "matrix2" {
		t.Fatalf("instance must stay reserved while the tombstone is pending, got %q", got)
	}
	// Teardown acks drop the tombstone and free the name again.
	hubRemRev, nodeRemRev := s.Tunnels[0].HubRevision, s.Tunnels[0].NodeRevision
	agentTunnelAckObserved("hub1", agentAck{Revision: hubRemRev, OK: true})
	agentTunnelAckObserved("node1", agentAck{Revision: nodeRemRev, OK: true})
	agentObserverWait()
	s = loadAgentTunnels()
	if len(s.Tunnels) != 0 {
		t.Fatalf("tombstone must be dropped after both acks, got %+v", s.Tunnels)
	}
	if got := agentTunnelNextInstance(s.Tunnels, "hub1"); got != "matrix" {
		t.Fatalf("instance must be freed after the tombstone is dropped, got %q", got)
	}
}

// --- S4c: duplicate acks for the same revision must not corrupt records.

func TestAgentRecoveryS4DuplicateAckIdempotent(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	agentRecoveryTunnel(t, "at_s4c", "matrix", "hub1", "node1", agentTunnelStateRegistering, "rh", "rn")

	ack := agentAck{Revision: "rn", OK: true}
	agentTunnelAckObserved("node1", ack)
	agentTunnelAckObserved("node1", ack) // duplicate delivery over WS reconnect

	agentObserverWait()
	s := loadAgentTunnels()
	agentObserverWait()
	if len(s.Tunnels) != 1 || s.Tunnels[0].State != agentTunnelStateActive || !s.Tunnels[0].NodeAckOK {
		t.Fatalf("duplicate ack must be a no-op, got %+v", s.Tunnels)
	}
}

// --- S5: after an agent reconnects it must converge to the LATEST desired
// state without replaying stale intermediates (single pending slot,
// last-write-wins for applies).

func TestAgentRecoveryS5ReconnectLatestWins(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	_ = lab
	agentRecoverySeedRegistry(t, "node1")

	if err := agentHubDeliverDesired("node1", agentRecoveryDesired("1000", "matrix", "apply")); err != nil {
		t.Fatal(err)
	}
	if err := agentHubDeliverDesired("node1", agentRecoveryDesired("2000", "matrix", "apply")); err != nil {
		t.Fatal(err)
	}
	pending := agentPendingDesired("node1")
	if pending == nil || pending.Revision != "2000" {
		t.Fatalf("reconnect delivery must carry only the latest revision, got %+v", pending)
	}
	if err := agentHubDeliverDesired("node1", agentRecoveryDesired("2000", "matrix", "apply")); err != nil {
		t.Fatal(err)
	}
	if p := agentPendingDesired("node1"); p == nil || p.Revision != "2000" {
		t.Fatalf("equal-revision redelivery must stay idempotent, got %+v", p)
	}
}

// --- FIX F2 regression: the full delivery-guard matrix.

func TestAgentRecoveryFixF2DeliveryGuardMatrix(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	_ = lab
	agentRecoverySeedRegistry(t, "a1")

	mustOK := func(rev, instance, action string) {
		t.Helper()
		if err := agentHubDeliverDesired("a1", agentRecoveryDesired(rev, instance, action)); err != nil {
			t.Fatalf("deliver %s %s (%s) must be allowed: %v", action, instance, rev, err)
		}
	}
	mustRefused := func(rev, instance, action string) {
		t.Helper()
		if err := agentHubDeliverDesired("a1", agentRecoveryDesired(rev, instance, action)); err == nil {
			t.Fatalf("deliver %s %s (%s) over a pending teardown must be refused", action, instance, rev)
		}
	}

	// apply over apply: allowed (last write wins).
	mustOK("1000", "matrix", "apply")
	mustOK("2000", "matrix", "apply")
	// remove over apply: allowed (delete supersedes a mid-flight apply).
	mustOK("3000", "matrix", "remove")
	// apply over remove: refused (would orphan the old instance).
	mustRefused("4000", "matrix", "apply")
	// a different instance's remove over a pending remove: refused (it
	// would silently cancel the first teardown).
	mustRefused("5000", "matrix2", "remove")
	// same-instance remove refresh: allowed.
	mustOK("6000", "matrix", "remove")
	if p := agentPendingDesired("a1"); p == nil || p.Revision != "6000" {
		t.Fatalf("pending slot must hold the refreshed remove, got %+v", p)
	}
}

// --- FIX F3 regression: reconcile resumes a hub-complete record WITHOUT
// re-applying to the hub (no needless tinc restart), straight to the node
// stage, and the tunnel completes.

func TestAgentRecoveryFixF3ResumeSkipsHubStage(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	// Post-restart shape: hub stage done, host file on the record.
	agentObserverWait()
	s := loadAgentTunnels()
	s.Tunnels = append(s.Tunnels, agentTunnel{
		ID: "at_fix3", Name: "resume", Instance: "matrix", HubAgentID: "hub1", NodeAgentID: "node1",
		Family: "ipv4", MeshPort: 2097, Ports: []int{8080},
		Token: strings.Repeat("k", 32), Active: true,
		State: agentTunnelStateProvisioning, HubRevision: "r-old", HubAckOK: true,
		HubMainHost: "Subnet = 10.254.231.1/32", CreatedAt: time.Now().Unix(),
	})
	if err := saveAgentTunnels(s); err != nil {
		t.Fatal(err)
	}

	provision, teardown := reconcileAgentTunnels()
	if len(provision) != 1 || provision[0] != "at_fix3" || len(teardown) != 0 {
		t.Fatalf("reconcile must resume the record, got provision=%v teardown=%v", provision, teardown)
	}

	go func() {
		agentRecoveryWait(t, 3*time.Second, "node desired delivery", func() bool {
			return len(lab.desiresFor("node1")) >= 1
		})
		nodeRev := lab.desiresFor("node1")[0].Revision
		agentTunnelAckObserved("node1", agentAck{Revision: nodeRev, OK: true})
	}()
	agentObserverWait()
	agentTunnelProvision("at_fix3")

	// The resume skipped the hub stage entirely.
	if got := len(lab.desiresFor("hub1")); got != 0 {
		t.Fatalf("resume must not re-apply to the hub, got %d hub desires", got)
	}
	nodeDesires := lab.desiresFor("node1")
	if len(nodeDesires) != 1 || nodeDesires[0].Matrix == nil || nodeDesires[0].Matrix.Bundle == "" {
		t.Fatalf("node must receive exactly one bundle, got %+v", nodeDesires)
	}
	agentObserverWait()
	agentObserverWait()
	if s := loadAgentTunnels(); len(s.Tunnels) != 1 || s.Tunnels[0].State != agentTunnelStateActive {
		t.Fatalf("resumed tunnel must end active, got %+v", s.Tunnels)
	}
}

// --- FIX F3 regression: reconcile retries powered-on error records
// exactly once per controller start; powered-off and active records are
// left alone; deleting tombstones get teardown re-delivery.

func TestAgentRecoveryFixF3ReconcileRetriesOnce(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	_ = lab
	agentObserverWait()
	s := loadAgentTunnels()
	s.Tunnels = append(s.Tunnels,
		agentTunnel{ID: "at_e1", Instance: "matrix", HubAgentID: "h", NodeAgentID: "n", State: agentTunnelStateError, Active: true, CreatedAt: time.Now().Unix()},
		agentTunnel{ID: "at_e2", Instance: "matrix2", HubAgentID: "h", NodeAgentID: "n", State: agentTunnelStateError, Active: false, CreatedAt: time.Now().Unix()},
		agentTunnel{ID: "at_a", Instance: "matrix3", HubAgentID: "h", NodeAgentID: "n", State: agentTunnelStateActive, Active: true, CreatedAt: time.Now().Unix()},
		agentTunnel{ID: "at_d", Instance: "matrix4", HubAgentID: "h", NodeAgentID: "n", State: agentTunnelStateDeleting, Active: true, CreatedAt: time.Now().Unix()},
	)
	if err := saveAgentTunnels(s); err != nil {
		t.Fatal(err)
	}

	provision, teardown := reconcileAgentTunnels()
	if len(provision) != 1 || provision[0] != "at_e1" {
		t.Fatalf("only the powered-on error record must be retried, got provision=%v", provision)
	}
	if len(teardown) != 1 || teardown[0] != "at_d" {
		t.Fatalf("the tombstone must be re-delivered, got teardown=%v", teardown)
	}
	s = loadAgentTunnels()
	byID := map[string]agentTunnel{}
	for _, x := range s.Tunnels {
		byID[x.ID] = x
	}
	if byID["at_e1"].State != agentTunnelStateProvisioning || byID["at_e1"].Error != "" {
		t.Fatalf("retried record must be back to provisioning, got %+v", byID["at_e1"])
	}
	if byID["at_e2"].State != agentTunnelStateError || byID["at_e2"].Active {
		t.Fatalf("powered-off error record must stay untouched, got %+v", byID["at_e2"])
	}
	if byID["at_a"].State != agentTunnelStateActive {
		t.Fatalf("active record must stay untouched, got %+v", byID["at_a"])
	}
	if byID["at_d"].HubRevision == "" || byID["at_d"].NodeRevision == "" {
		t.Fatalf("tombstone must carry fresh teardown revisions, got %+v", byID["at_d"])
	}
}

// --- FIX F2 regression: a create is refused while an endpoint still has a
// pending teardown, instead of parking a doomed tunnel in error.

func TestAgentRecoveryFixF2CreateRefusedWhileTeardownPending(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	agentRecoverySeedRegistry(t, "hub1")
	if err := agentHubDeliverDesired("hub1", agentRecoveryDesired("1000", "matrix", "remove")); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/agent-tunnels",
		strings.NewReader(`{"name":"t","hub_agent_id":"hub1","node_agent_id":"node1","family":"ipv4","port":2097,"ports":[8080]}`))
	rr := httptest.NewRecorder()
	handleAgentTunnelsPost(rr, req)
	if rr.Code == 200 {
		t.Fatalf("create over a pending teardown must be refused, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "pending teardown") {
		t.Fatalf("refusal must explain the pending teardown, got %s", rr.Body.String())
	}
	agentObserverWait()
	agentObserverWait()
	if s := loadAgentTunnels(); len(s.Tunnels) != 0 {
		t.Fatalf("no tunnel record may be created, got %+v", s.Tunnels)
	}
}

// --- FIX F2 regression: power/restart on a deleting tombstone is refused.

func quiesceObservers() { agentObserverWait() }

func newAgentTunnelLabWithDrain(t *testing.T) *agentTunnelLab {
	lab := newAgentTunnelLab(t)
	t.Cleanup(func() {
		agentHubDrainAll()
		agentTunnelWG.Wait()
		agentObserverWait()
	})
	return lab
}

func TestAgentRecoveryFixPowerRejectedWhileDeleting(t *testing.T) {
	lab := newAgentTunnelLabWithDrain(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	agentRecoveryTunnel(t, "at_pd", "matrix", "hub1", "node1", agentTunnelStateActive, "r1", "r2")

	agentRecoveryDelete(t, "at_pd")

	req := httptest.NewRequest("POST", "/api/agent-tunnels/at_pd/power", strings.NewReader(`{"active":false}`))
	req.SetPathValue("id", "at_pd")
	rr := httptest.NewRecorder()
	handleAgentTunnelPower(rr, req)
	if rr.Code == 200 {
		t.Fatalf("power on a deleting tombstone must be refused, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "being deleted") {
		t.Fatalf("refusal must mention the deletion, got %s", rr.Body.String())
	}
}
