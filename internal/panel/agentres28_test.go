package panel

// agentres28_test.go — BACKEND-28 (agents pages backend audit): the §2.3
// agents rows carry the SAME cpu/mem/disk resource views the §2.4 detail
// object serves. Both accepted agents pages render their resource columns
// from the list; a metric stays null until the agent's heartbeat reported
// it (never a fake 0%).

import (
	"reflect"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func agentRow28(t *testing.T, rows map[string]any, name string) map[string]any {
	t.Helper()
	ags, _ := rows["agents"].([]any)
	for _, raw := range ags {
		if m, ok := raw.(map[string]any); ok && m["name"] == name {
			return m
		}
	}
	t.Fatalf("agent %q missing from the rows: %v", name, rows)
	return nil
}

// TestAgentsListResourceViewsMatchDetail — a reported agent carries
// cpu{cores,percent,load1} / mem{used_mb,total_mb} / disk{used_gb,total_gb}
// on its row, DeepEqual to the detail object's maps; an agent without a
// heartbeat carries all three as null.
func TestAgentsListResourceViewsMatchDetail(t *testing.T) {
	srv := uiServer(t)
	st := meshStoreMust(t)

	if err := st.UpsertAgent(store.Agent{ID: "agt_res28", Name: "res-28", Role: "iran",
		Status: "online", Version: "v28", Arch: "amd64", LastSeen: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAgentResources("agt_res28", 4, 37.5, 1.25,
		8192, 4096, 512, 128, 3600, "kvm"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAgent(store.Agent{ID: "agt_res28_bare", Name: "bare-28", Role: "foreign",
		Status: "online", Version: "v28", Arch: "amd64", LastSeen: 1}); err != nil {
		t.Fatal(err)
	}

	rows := uiGet26(t, srv, "/api/ui/agents")
	row := agentRow28(t, rows, "res-28")
	cpu, _ := row["cpu"].(map[string]any)
	mem, _ := row["mem"].(map[string]any)
	disk, _ := row["disk"].(map[string]any)
	if cpu == nil || mem == nil || disk == nil {
		t.Fatalf("reported agent must carry cpu/mem/disk views: %v", row)
	}
	if cpu["cores"] != float64(4) || cpu["percent"] != 37.5 || cpu["load1"] != 1.25 {
		t.Fatalf("row cpu view wrong: %v", cpu)
	}
	if mem["used_mb"] != float64(4096) || mem["total_mb"] != float64(8192) {
		t.Fatalf("row mem view wrong: %v", mem)
	}
	if disk["used_gb"] != 128.0 || disk["total_gb"] != 512.0 {
		t.Fatalf("row disk view wrong: %v", disk)
	}

	// the row's views must be the DETAIL object's views (same shape, same
	// values — the UI relies on the two spots agreeing)
	detail := uiGet26(t, srv, "/api/ui/agents/agt_res28")
	ag, _ := detail["agent"].(map[string]any)
	if ag == nil {
		t.Fatalf("detail missing agent: %v", detail)
	}
	if !reflect.DeepEqual(row["cpu"], ag["cpu"]) || !reflect.DeepEqual(row["mem"], ag["mem"]) ||
		!reflect.DeepEqual(row["disk"], ag["disk"]) {
		t.Fatalf("row views != detail views: row cpu=%v mem=%v disk=%v detail cpu=%v mem=%v disk=%v",
			row["cpu"], row["mem"], row["disk"], ag["cpu"], ag["mem"], ag["disk"])
	}

	// no heartbeat yet → all three null (columns hide, never a fake 0%)
	bare := agentRow28(t, rows, "bare-28")
	if bare["cpu"] != nil || bare["mem"] != nil || bare["disk"] != nil {
		t.Fatalf("unreported agent must carry null views: %v", bare)
	}
}
