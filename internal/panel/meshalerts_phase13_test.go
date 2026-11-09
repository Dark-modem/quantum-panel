package panel

// meshalerts_phase13_test.go — TASK 2 (phase 13): the new alert rules
// with a fake notifier and seeded states; maintenance mode; structured
// fa/en views.

import (
	"sync"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

type fakeNotifier13 struct {
	mu   sync.Mutex
	sent []meshAlertEvent
}

func (f *fakeNotifier13) Name() string { return "fake13" }
func (f *fakeNotifier13) Send(ev meshAlertEvent) error {
	f.mu.Lock()
	f.sent = append(f.sent, ev)
	f.mu.Unlock()
	return nil
}

func newTestEngine13(t *testing.T) (*meshAlertEngine, *fakeNotifier13) {
	t.Helper()
	withTempConfig(t)
	withMeshStore(t)
	e := newMeshAlertEngine()
	n := &fakeNotifier13{}
	e.notifyMu.Lock()
	e.notifiers = []meshAlertNotifier{n}
	e.notifyMu.Unlock()
	return e, n
}

func TestAlertPhase13Rules(t *testing.T) {
	e, _ := newTestEngine13(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)

	// agent_cpu_high: persist a hot ResourceReport.
	if err := s.UpdateAgentResources(agIr, 4, 96, 0.5, 8192, 8100, 100, 50, 86400, "kvm"); err != nil {
		t.Fatal(err)
	}
	// engine enabled but idle.
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: agIr, Engine: "chisel",
		Installed: true, Desired: true, Instances: 0, UpdatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	// flapping: 4 state flips in the window.
	tun, _ := s.GetTunnelByKey("tl-a")
	now := time.Now()
	for i := 0; i < 4; i++ {
		_ = s.AddTunnelEvent(store.TunnelEvent{TunnelID: tun.ID, At: now.Add(-time.Duration(i) * time.Minute).Unix(),
			Kind: "state", FromState: "up", ToState: "down"})
	}
	// billing anomaly: last week carried 4 MB, this hour 0.
	weekAgo := now.Add(-7 * 24 * time.Hour).Unix()
	_ = s.AddTrafficSample(store.TrafficSample{TunnelID: tun.ID, At: weekAgo,
		WireRx: 4 << 20, WireTx: 1 << 20, PayloadRx: 3 << 20, PayloadTx: 900000,
		CountersKnown: true, PayloadKnown: true, AgentID: agIr})

	evs := e.evaluatePhase13(now)
	got := map[string]bool{}
	for _, ev := range evs {
		got[ev.Rule] = true
		if ev.UI == nil {
			t.Fatalf("rule %s must carry the structured fa/en view", ev.Rule)
		}
		if title, ok := ev.UI["title"].(map[string]string); ok {
			if title["fa"] == "" || title["en"] == "" {
				t.Fatalf("rule %s title must have BOTH languages: %v", ev.Rule, title)
			}
		}
	}
	for _, want := range []string{"agent_cpu_high", "agent_mem_high", "engine_not_running", "tunnel_flapping", "billing_drop"} {
		if !got[want] {
			t.Fatalf("rule %s did not fire; fired: %v", want, got)
		}
	}
}

func TestAlertMaintenanceModeSilences(t *testing.T) {
	e, n := newTestEngine13(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)
	if err := s.UpdateAgentResources(agIr, 4, 96, 0.5, 8192, 8100, 100, 50, 86400, "kvm"); err != nil {
		t.Fatal(err)
	}

	// Maintenance ON for this agent: the rule fires but NOTHING is sent.
	e.SetMaintenance(agIr, time.Now().Add(time.Hour))
	evs := e.evaluatePhase13(time.Now())
	fired := false
	for _, ev := range evs {
		if ev.Rule == "agent_cpu_high" {
			fired = true
			e.mu.Lock()
			e.dispatchLocked(ev, false)
			e.mu.Unlock()
		}
	}
	if !fired {
		t.Fatal("the rule must still fire (maintenance affects DELIVERY)")
	}
	if len(n.sent) != 0 {
		t.Fatalf("maintenance mode must silence delivery, sent %d", len(n.sent))
	}
	// OFF again → delivery resumes.
	e.SetMaintenance(agIr, time.Now().Add(-time.Minute))
	e.mu.Lock()
	e.dispatchLocked(evs[0], false)
	e.mu.Unlock()
	if len(n.sent) == 0 {
		t.Fatal("delivery must resume after maintenance ends")
	}
}

func TestAlertUIViewCarriesBothLanguages(t *testing.T) {
	// The phase-13 formatter: every catalog rule carries BOTH languages.
	for _, rule := range []string{"agent_cpu_high", "agent_mem_high", "agent_disk_high",
		"engine_not_running", "tunnel_flapping", "billing_drop", "billing_spike",
		"backup_old", "panel_disk_full", "desired_stuck"} {
		ui := alertUI(rule, "formatted text")
		title, ok := ui["title"].(map[string]string)
		if !ok || title["en"] == "" || title["fa"] == "" {
			t.Fatalf("%s: title must carry fa+en: %v", rule, ui["title"])
		}
		for _, field := range []string{"what", "likely_cause", "what_to_do"} {
			m, ok := ui[field].(map[string]string)
			if !ok || m["en"] == "" || m["fa"] == "" {
				t.Fatalf("%s.%s must carry fa+en: %v", rule, field, ui[field])
			}
		}
	}
}
