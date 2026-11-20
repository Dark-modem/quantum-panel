package panel

// BACKEND-34 tests: GitHub-centric agent-update discovery (contract
// §2.20). All GitHub data arrives through the swapped seams
// (releasesFetcher / devCommitFetcher) and the group queue drives the
// agentUpdateDispatch seam — no network, no real hub, no real agent, no
// store files beyond a temp agents.json.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeDiscoverReleases() []ghRelease {
	return []ghRelease{
		{Tag: "dev-r9", Name: "dev build 9", Prerelease: true, PublishedAt: "2026-11-20T01:00:00Z",
			Assets: []ghAsset{
				{Name: "quantum-agent-linux-amd64", Size: 123, BrowserDownloadURL: "https://github.com/Dark-modem/quantum/releases/download/dev-r9/quantum-agent-linux-amd64"},
				{Name: "quantum-agent-linux-arm64", Size: 124, BrowserDownloadURL: "https://github.com/Dark-modem/quantum/releases/download/dev-r9/quantum-agent-linux-arm64"},
			}},
		{Tag: "v1.29.0", Name: "v1.29.0", PublishedAt: "2026-11-19T00:00:00Z",
			Assets: []ghAsset{
				{Name: "quantum-agent-linux-amd64", Size: 120, BrowserDownloadURL: "https://github.com/Dark-modem/quantum/releases/download/v1.29.0/quantum-agent-linux-amd64"},
			}},
		{Tag: "v1.28.1", Name: "v1.28.1", PublishedAt: "2026-11-10T00:00:00Z"},
		{Tag: "draft-x", Draft: true},
	}
}

// swapDiscoverSeams swaps the GitHub seams and zeroes the commit cache so
// every test sees its own feed.
func swapDiscoverSeams(t *testing.T, rels []ghRelease, commit *ghCommit) {
	t.Helper()
	or, ol := releasesFetcher, latestTagFetcher
	releasesFetcher = func() ([]ghRelease, error) { return rels, nil }
	latestTagFetcher = func() (string, error) { return "v1.29.0", nil }
	t.Cleanup(func() { releasesFetcher, latestTagFetcher = or, ol })

	oc := devCommitFetcher
	devCommitFetcher = func() (*ghCommit, error) { return commit, nil }
	t.Cleanup(func() { devCommitFetcher = oc })

	devCommitMu.Lock()
	devCommitCache, devCommitErr, devCommitAt = nil, nil, time.Time{}
	devCommitMu.Unlock()
}

// resetGroupState gives each group test a clean queue state.
func resetGroupState(t *testing.T) {
	t.Helper()
	reset := func() {
		agentGroupMu.Lock()
		agentGroupRun, agentGroupLast = false, nil
		agentGroupMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// seedAgents points configDir at a temp dir with a hand-written registry.
func seedAgents(t *testing.T, recs []agentRecord) {
	t.Helper()
	old := configDir
	dir := t.TempDir()
	configDir = dir
	t.Cleanup(func() { configDir = old })
	data := mustJSON(agentRegistryFile{Version: agentRegistryVersion, Agents: recs})
	if err := os.WriteFile(filepath.Join(dir, "agents.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sampleAgents() []agentRecord {
	return []agentRecord{
		{ID: "agt_a", Name: "outdated-amd", Arch: "amd64", Version: "v1.28.1", Status: agentStatusOnline},
		{ID: "agt_b", Name: "current-arm", Arch: "arm64", Version: "v1.29.0", Status: agentStatusOnline},
		{ID: "agt_c", Name: "revoked", Arch: "amd64", Version: "v1.28.0", Status: agentStatusRevoked, Revoked: true},
		{ID: "agt_d", Name: "no-version", Arch: "amd64", Status: agentStatusOnline},
	}
}

func getJSON2(t *testing.T, handler func(http.ResponseWriter, *http.Request), path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != 200 {
		t.Fatalf("GET %s: status %d: %s", path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
	return out
}

// TestUpdateAvailableDiscovery: latest stable, latest dev-rN prerelease
// and the dev-branch commit, with the release's own asset list.
func TestUpdateAvailableDiscovery(t *testing.T) {
	c := &ghCommit{SHA: "abc123", HTMLURL: "https://github.com/Dark-modem/quantum/commit/abc123"}
	c.Commit.Message = "fix the thing\n\nlong body"
	c.Commit.Committer.Date = "2026-11-20T02:00:00Z"
	swapDiscoverSeams(t, fakeDiscoverReleases(), c)

	out := getJSON2(t, handleUpdateAvailable, "/api/update/available")
	if out["channel"] != channelStable {
		t.Fatalf("channel = %v", out["channel"])
	}
	stable := out["stable"].(map[string]any)
	if stable["tag"] != "v1.29.0" || stable["published_at"] != "2026-11-19T00:00:00Z" {
		t.Fatalf("stable wrong: %v", stable)
	}
	assets := stable["assets"].([]any)
	if len(assets) != 1 || assets[0].(map[string]any)["size"].(float64) != 120 {
		t.Fatalf("stable assets wrong: %v", assets)
	}
	pre := out["prerelease"].(map[string]any)
	if pre["tag"] != "dev-r9" || len(pre["assets"].([]any)) != 2 {
		t.Fatalf("prerelease wrong: %v", pre)
	}
	commit := out["commit"].(map[string]any)
	if commit["sha"] != "abc123" || commit["message"] != "fix the thing" || commit["date"] != "2026-11-20T02:00:00Z" {
		t.Fatalf("commit wrong: %v", commit)
	}
	if _, has := out["error"]; has {
		t.Fatalf("unexpected error field: %v", out["error"])
	}
}

// TestUpdateAvailableCommitCache: the commit fetch rides the 5-minute
// cache; ?refresh=1 forces one new fetch, throttled to 30 s like
// /api/version (Check Now cannot hammer the API).
func TestUpdateAvailableCommitCache(t *testing.T) {
	swapDiscoverSeams(t, fakeDiscoverReleases(), nil)
	calls := 0
	devCommitFetcher = func() (*ghCommit, error) {
		calls++
		return nil, nil
	}
	getJSON2(t, handleUpdateAvailable, "/api/update/available")
	getJSON2(t, handleUpdateAvailable, "/api/update/available")
	if calls != 1 {
		t.Fatalf("commit fetches = %d, want 1 (cached)", calls)
	}
	getJSON2(t, handleUpdateAvailable, "/api/update/available?refresh=1")
	if calls != 1 {
		t.Fatalf("refresh inside the 30 s throttle refetched (%d)", calls)
	}
	devCommitMu.Lock()
	devCommitAt = time.Now().Add(-31 * time.Second) // age out the throttle
	devCommitMu.Unlock()
	getJSON2(t, handleUpdateAvailable, "/api/update/available?refresh=1")
	if calls != 2 {
		t.Fatalf("commit fetches after aged refresh = %d, want 2", calls)
	}
}

// TestAgentUpdatesRows: per-agent outdatedness + arch-resolved artifacts.
func TestAgentUpdatesRows(t *testing.T) {
	swapDiscoverSeams(t, fakeDiscoverReleases(), nil)
	seedAgents(t, sampleAgents())

	out := getJSON2(t, handleAgentUpdates, "/api/agents/updates")
	rows := map[string]map[string]any{}
	for _, r := range out["agents"].([]any) {
		m := r.(map[string]any)
		rows[m["id"].(string)] = m
	}
	if len(rows) != 4 {
		t.Fatalf("row count = %d", len(rows))
	}
	a := rows["agt_a"]
	if a["outdated_stable"] != true || a["outdated_prerelease"] != true {
		t.Fatalf("outdated-amd flags wrong: %v", a)
	}
	art := a["artifact_stable"].(map[string]any)
	if art["url"] != "https://github.com/Dark-modem/quantum/releases/download/v1.29.0/quantum-agent-linux-amd64" ||
		art["version"] != "v1.29.0" || art["size"].(float64) != 120 {
		t.Fatalf("amd artifact wrong: %v", art)
	}
	pre := a["artifact_prerelease"].(map[string]any)
	if pre["tag"] != "dev-r9" || !strings.Contains(pre["url"].(string), "linux-amd64") {
		t.Fatalf("prerelease artifact wrong: %v", pre)
	}
	b := rows["agt_b"]
	if b["outdated_stable"] != false {
		t.Fatalf("current agent flagged outdated: %v", b)
	}
	if b["outdated_prerelease"] != true {
		t.Fatalf("stable agent should be offered the dev prerelease: %v", b)
	}
	if arm, ok := b["artifact_stable"].(map[string]any); !ok || !strings.Contains(arm["url"].(string), "linux-arm64") {
		t.Fatalf("arm64 artifact wrong: %v", b["artifact_stable"])
	}
	d := rows["agt_d"]
	if d["outdated_stable"] != false || d["artifact_stable"] != nil {
		t.Fatalf("no-version agent wrong: %v", d)
	}
	if rows["agt_c"]["status"] != agentStatusRevoked {
		t.Fatalf("revoked agent status wrong: %v", rows["agt_c"])
	}
}

// TestAgentUpdateVersionReAcceptsDevTags: the version gate now covers the
// dev-rN prerelease tags (BACKEND-34) and stays a strict allow-list.
func TestAgentUpdateVersionReAcceptsDevTags(t *testing.T) {
	for _, ok := range []string{"v1.29.0", "v1.2", "dev-r9", "dev-r148", "dev-r123456"} {
		if !agentUpdateVersionRe.MatchString(ok) {
			t.Fatalf("%q should match", ok)
		}
	}
	for _, bad := range []string{"dev-r", "dev-rabc", "dev-r9; rm -rf", "1.29.0", "v1.2.3 spaces", "dev-r99999999"} {
		if agentUpdateVersionRe.MatchString(bad) {
			t.Fatalf("%q should NOT match", bad)
		}
	}
}

// TestAgentUpdateOutdatedGroupQueue: only outdated, non-revoked agents
// with a reported version and a known arch are queued; the queue drives
// the unchanged verify-then-hand-off dispatch one agent at a time.
func TestAgentUpdateOutdatedGroupQueue(t *testing.T) {
	swapDiscoverSeams(t, fakeDiscoverReleases(), nil)
	seedAgents(t, sampleAgents())
	resetGroupState(t)

	var mu sync.Mutex
	var dispatched []string
	done := make(chan struct{})
	old := agentUpdateDispatch
	agentUpdateDispatch = func(id, version, url string) (agentCommandResult, error) {
		mu.Lock()
		dispatched = append(dispatched, id+"|"+version+"|"+url)
		mu.Unlock()
		done <- struct{}{}
		return agentCommandResult{}, nil
	}
	t.Cleanup(func() { agentUpdateDispatch = old })

	req := httptest.NewRequest("POST", "/api/agents/update-outdated", strings.NewReader(`{"channel":"stable"}`))
	w := httptest.NewRecorder()
	handleAgentUpdateOutdatedPost(w, req)
	if w.Code != 200 {
		t.Fatalf("POST status %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "queued" || out["tag"] != "v1.29.0" {
		t.Fatalf("queue response wrong: %v", out)
	}
	queued := out["queued"].([]any)
	if len(queued) != 1 || queued[0].(map[string]any)["agent_id"] != "agt_a" {
		t.Fatalf("queued wrong (want only agt_a): %v", queued)
	}
	if len(out["skipped"].([]any)) != 2 {
		t.Fatalf("skipped wrong (revoked + no-version): %v", out["skipped"])
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("group queue did not dispatch in time")
	}
	// The report lands after the dispatch returns — poll for the run to
	// finish instead of racing the bookkeeping.
	var rep *agentGroupUpdateReport
	deadline := time.Now().Add(2 * time.Second)
	for {
		rep = agentGroupUpdateSnapshot()
		if rep != nil && rep.FinishedAt != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("group run did not finish in time: %+v", rep)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rep.Tag != "v1.29.0" {
		t.Fatalf("group report wrong: %+v", rep)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != "ok" || rep.Results[0].AgentID != "agt_a" {
		t.Fatalf("group results wrong: %+v", rep.Results)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "agt_a|v1.29.0|https://github.com/Dark-modem/quantum/releases/download/v1.29.0/quantum-agent-linux-amd64"
	if len(dispatched) != 1 || dispatched[0] != want {
		t.Fatalf("dispatch wrong: %v", dispatched)
	}

	// The row view surfaces the finished run.
	out2 := getJSON2(t, handleAgentUpdates, "/api/agents/updates")
	lg := out2["last_group_update"].(map[string]any)
	if lg["tag"] != "v1.29.0" {
		t.Fatalf("last_group_update wrong: %v", lg)
	}
}

// TestAgentUpdateOutdatedDevChannel: the prerelease target rides the
// widened version gate (dev-rN); both semver agents count as outdated for
// the prerelease (panel dev-channel semantics).
func TestAgentUpdateOutdatedDevChannel(t *testing.T) {
	swapDiscoverSeams(t, fakeDiscoverReleases(), nil)
	seedAgents(t, sampleAgents())
	resetGroupState(t)

	dispatched := make(chan string, 4)
	old := agentUpdateDispatch
	agentUpdateDispatch = func(id, version, url string) (agentCommandResult, error) {
		dispatched <- version + "|" + url
		return agentCommandResult{}, nil
	}
	t.Cleanup(func() { agentUpdateDispatch = old })

	req := httptest.NewRequest("POST", "/api/agents/update-outdated", strings.NewReader(`{"channel":"dev"}`))
	w := httptest.NewRecorder()
	handleAgentUpdateOutdatedPost(w, req)
	if w.Code != 200 {
		t.Fatalf("POST status %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["tag"] != "dev-r9" || len(out["queued"].([]any)) != 2 {
		t.Fatalf("dev queue wrong: %v", out)
	}
	var got []string
	for i := 0; i < 2; i++ {
		select {
		case g := <-dispatched:
			got = append(got, g)
		case <-time.After(2 * time.Second):
			t.Fatal("dev group queue did not dispatch in time")
		}
	}
	sort.Strings(got)
	wantAmd := "dev-r9|https://github.com/Dark-modem/quantum/releases/download/dev-r9/quantum-agent-linux-amd64"
	wantArm := "dev-r9|https://github.com/Dark-modem/quantum/releases/download/dev-r9/quantum-agent-linux-arm64"
	if len(got) != 2 || got[0] != wantAmd || got[1] != wantArm {
		t.Fatalf("dev dispatch wrong: %v", got)
	}
}

// TestAgentUpdateOutdatedFailureRecorded: a failed dispatch lands in the
// report with its error, not a silent skip.
func TestAgentUpdateOutdatedFailureRecorded(t *testing.T) {
	swapDiscoverSeams(t, fakeDiscoverReleases(), nil)
	seedAgents(t, sampleAgents())
	resetGroupState(t)

	done := make(chan struct{})
	old := agentUpdateDispatch
	agentUpdateDispatch = func(id, version, url string) (agentCommandResult, error) {
		if id == "agt_a" {
			close(done)
			return agentCommandResult{}, errors.New("agent is offline")
		}
		return agentCommandResult{}, nil
	}
	t.Cleanup(func() { agentUpdateDispatch = old })

	req := httptest.NewRequest("POST", "/api/agents/update-outdated", strings.NewReader(`{"channel":"stable"}`))
	w := httptest.NewRecorder()
	handleAgentUpdateOutdatedPost(w, req)
	if w.Code != 200 {
		t.Fatalf("POST status %d", w.Code)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("queue did not run")
	}
	var rep *agentGroupUpdateReport
	deadline := time.Now().Add(2 * time.Second)
	for {
		rep = agentGroupUpdateSnapshot()
		if rep != nil && rep.FinishedAt != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("group run did not finish in time: %+v", rep)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != "failed" || rep.Results[0].Error == "" {
		t.Fatalf("failure not recorded: %+v", rep.Results)
	}
}

// TestAgentUpdateOutdatedAlreadyRunning: a second concurrent run is
// refused with E-AGENT-08 / 409.
func TestAgentUpdateOutdatedAlreadyRunning(t *testing.T) {
	swapDiscoverSeams(t, fakeDiscoverReleases(), nil)
	seedAgents(t, sampleAgents())
	resetGroupState(t)

	agentGroupMu.Lock()
	agentGroupRun = true
	agentGroupMu.Unlock()

	req := httptest.NewRequest("POST", "/api/agents/update-outdated", strings.NewReader(`{"channel":"stable"}`))
	w := httptest.NewRecorder()
	handleAgentUpdateOutdatedPost(w, req)
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "E-AGENT-08") {
		t.Fatalf("error code missing: %s", w.Body.String())
	}
	// Bad channel is rejected before any queueing.
	req2 := httptest.NewRequest("POST", "/api/agents/update-outdated", strings.NewReader(`{"channel":"nightly"}`))
	w2 := httptest.NewRecorder()
	handleAgentUpdateOutdatedPost(w2, req2)
	if w2.Code != 400 {
		t.Fatalf("bad channel status = %d", w2.Code)
	}
}

// TestAgentUpdateOutdatedHostileInput: the POST body and the GET query
// are hostile-input surfaces (round-4 rules). Malformed / wrong-typed /
// hostile / oversized bodies answer the structured E-AGENT-08 envelope
// with 400 and NEVER reach the queue; null and empty-object bodies take
// the contract's default-channel path; hostile query values are inert.
func TestAgentUpdateOutdatedHostileInput(t *testing.T) {
	swapDiscoverSeams(t, fakeDiscoverReleases(), nil)
	seedAgents(t, sampleAgents())
	resetGroupState(t)

	dispatched := make(chan string, 8)
	old := agentUpdateDispatch
	agentUpdateDispatch = func(id, version, url string) (agentCommandResult, error) {
		dispatched <- id
		return agentCommandResult{}, nil
	}
	t.Cleanup(func() { agentUpdateDispatch = old })

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/agents/update-outdated", strings.NewReader(body))
		w := httptest.NewRecorder()
		handleAgentUpdateOutdatedPost(w, req)
		return w
	}

	reject := []struct {
		name string
		body string
	}{
		{"malformed json", `{"channel":"stable"`},
		{"empty body", ``},
		{"array body", `["stable"]`},
		{"string body", `"stable"`},
		{"wrong type", `{"channel":123}`},
		{"hostile channel", `{"channel":"stable; rm -rf /"}`},
		{"channel with newline", "{\"channel\":\"stable\n\"}"},
		{"channel with nul", "{\"channel\":\"stab\u0000le\"}"},
		{"channel lookalike", `{"channel":"stable "}`},
		{"oversized body", `{"channel":"stable","pad":"` + strings.Repeat("x", 4096) + `"}`},
	}
	for _, c := range reject {
		w := post(c.body)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400 (body: %.80s)", c.name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "E-AGENT-08") {
			t.Errorf("%s: missing E-AGENT-08 envelope: %.120s", c.name, w.Body.String())
		}
	}
	select {
	case id := <-dispatched:
		t.Fatalf("hostile input reached the dispatch queue: %q", id)
	default:
	}

	// Default-channel path: null (JSON null leaves the string field
	// untouched = absent) / empty-object bodies are valid requests
	// (channel defaults to the active channel = stable in the temp dir).
	for _, c := range []struct{ name, body string }{
		{"json null", `null`},
		{"null channel", `{"channel":null}`},
		{"empty object", `{}`},
		{"unknown fields only", `{"foo":{"bar":1}}`},
	} {
		resetGroupState(t)
		w := post(c.body)
		if w.Code != 200 {
			t.Fatalf("%s: status = %d, want 200: %s", c.name, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		if out["status"] != "queued" || out["channel"] != channelStable {
			t.Fatalf("%s: response wrong: %v", c.name, out)
		}
		select {
		case id := <-dispatched:
			if id != "agt_a" {
				t.Fatalf("%s: dispatched %q", c.name, id)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: queue did not dispatch", c.name)
		}
	}
}

// TestDiscoveryQueryHostileInput: hostile/inert query values on the
// discovery GETs are harmless — the endpoints answer 200 with the same
// shape, and only refresh=1 acts.
func TestDiscoveryQueryHostileInput(t *testing.T) {
	swapDiscoverSeams(t, fakeDiscoverReleases(), nil)
	seedAgents(t, sampleAgents())
	resetGroupState(t)
	for _, q := range []string{
		"?refresh=1",
		"?refresh=true",
		"?refresh=0",
		"?refresh=<script>alert(1)</script>",
		"?refresh=1&x=" + strings.Repeat("y", 4096),
		"?channel=dev&refresh=1",
	} {
		out := getJSON2(t, handleUpdateAvailable, "/api/update/available"+q)
		if out["channel"] != channelStable {
			t.Fatalf("query %q: channel = %v", q, out["channel"])
		}
	}
	out := getJSON2(t, handleAgentUpdates, "/api/agents/updates?refresh=1&callback=fn")
	if _, ok := out["agents"]; !ok {
		t.Fatal("agents updates query variant lost the agents field")
	}
}
