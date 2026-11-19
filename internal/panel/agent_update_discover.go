package panel

// GitHub-centric agent-update discovery (BACKEND-34, UI-API-CONTRACT
// §2.20 — UI session request): the operator sees what GitHub has (latest
// stable, latest dev-rN prerelease, latest dev-branch commit) and
// per-agent outdatedness with the arch-resolved release asset URL. The
// update itself still rides the M2 verify-then-hand-off flow unchanged:
// the panel verifies the pinned ed25519 signature + digest BEFORE the
// agent is told anything (agentUpdatePrepareFor), the agent downloads
// from the URL and re-verifies on its side, and the linux-<arch> gate is
// re-checked per agent. No shell execution anywhere in this flow.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// agentReleaseArtifactName is the release asset name the update flow
// expects for one agent architecture (quantum-release build-release.sh
// uploads quantum-agent-linux-{amd64,arm64} + a detached .sig).
func agentReleaseArtifactName(arch string) string {
	return "quantum-agent-linux-" + arch
}

// agentReleaseArtifact is the suggested per-arch agent artifact for one
// release tag — exactly what POST /api/agents/{id}/update accepts
// ({version, url}; the panel resolves + verifies the rest).
type agentReleaseArtifact struct {
	Channel string `json:"channel"`
	Tag     string `json:"tag"`
	Version string `json:"version"`
	URL     string `json:"url"`
	Asset   string `json:"asset_name"`
	Size    int64  `json:"size,omitempty"`
}

// releaseInfo is the discovery view of one release: tag, date and the
// release's own asset list (name/size/url).
type releaseInfo struct {
	Tag         string    `json:"tag"`
	Name        string    `json:"name,omitempty"`
	PublishedAt string    `json:"published_at,omitempty"`
	Assets      []ghAsset `json:"assets"`
}

// latestChannelRelease picks the newest release of a channel from the
// (already cached) release list and returns its discovery view. nil = no
// usable release for that channel.
func latestChannelRelease(ch string, rels []ghRelease) *releaseInfo {
	tag := pickChannelTag(ch, rels)
	if tag == "" || !validTag(tag) {
		return nil
	}
	for i := range rels {
		if rels[i].Tag == tag && !rels[i].Draft {
			assets := append([]ghAsset(nil), rels[i].Assets...)
			if assets == nil {
				assets = []ghAsset{}
			}
			return &releaseInfo{Tag: tag, Name: rels[i].Name, PublishedAt: rels[i].PublishedAt, Assets: assets}
		}
	}
	// Tag known but the release body unavailable (should not happen with a
	// fresh list): still answer the tag, honestly without assets.
	return &releaseInfo{Tag: tag, Assets: []ghAsset{}}
}

// agentReleaseArtifactFor resolves the release asset for one arch under a
// channel tag, preferring the release's own asset list for the size and
// falling back to the canonical download URL. nil = not resolvable
// (unknown arch or non-updatable tag shape).
func agentReleaseArtifactFor(ch, tag, arch string, rel *releaseInfo) *agentReleaseArtifact {
	if arch == "" || tag == "" || !agentUpdateVersionRe.MatchString(tag) {
		return nil
	}
	asset := agentReleaseArtifactName(arch)
	a := &agentReleaseArtifact{
		Channel: ch,
		Tag:     tag,
		Version: tag,
		URL:     releaseAssetURL(tag, asset),
		Asset:   asset,
	}
	if rel != nil {
		for _, ga := range rel.Assets {
			if ga.Name == asset {
				a.Size = ga.Size
				if ga.BrowserDownloadURL != "" {
					a.URL = ga.BrowserDownloadURL
				}
				break
			}
		}
	}
	return a
}

// ghCommit is the subset of the commits API the discovery surfaces.
type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	HTMLURL string `json:"html_url"`
}

// commitView is the wire shape of the dev-branch commit row.
type commitView struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Date    string `json:"date"`
	URL     string `json:"url,omitempty"`
}

// devCommitFetcher is swapped in tests (never touches the network).
var devCommitFetcher = fetchDevCommit

// fetchDevCommit returns the newest commit on the dev branch of the same
// repository the release feed watches. nil+nil = branch exists, no commits.
func fetchDevCommit() (*ghCommit, error) {
	var commits []ghCommit
	if err := getJSON("/commits?sha=dev&per_page=1", &commits); err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, nil
	}
	c := commits[0]
	return &c, nil
}

var (
	devCommitMu    sync.Mutex
	devCommitCache *ghCommit
	devCommitErr   error
	devCommitAt    time.Time
)

// dropDevCommitCache makes the next commit fetch hit GitHub again (Check
// Now), throttled to 30 s like the release cache.
func dropDevCommitCache() {
	devCommitMu.Lock()
	defer devCommitMu.Unlock()
	if time.Since(devCommitAt) > 30*time.Second {
		devCommitAt = time.Time{}
	}
}

// fetchDevCommitCached caches the dev commit for 5 minutes (failures for
// 1 minute) — the commits API shares the release feed's rate limit.
func fetchDevCommitCached() (*ghCommit, error) {
	devCommitMu.Lock()
	defer devCommitMu.Unlock()
	ttl := 5 * time.Minute
	if devCommitErr != nil {
		ttl = time.Minute
	}
	if !devCommitAt.IsZero() && time.Since(devCommitAt) < ttl {
		return devCommitCache, devCommitErr
	}
	c, err := devCommitFetcher()
	devCommitCache, devCommitErr, devCommitAt = c, err, time.Now()
	return c, err
}

// commitWire maps a fetched commit to the wire view (message cut to the
// first line, ≤200 chars — the UI shows a one-liner).
func commitWire(c *ghCommit) *commitView {
	if c == nil {
		return nil
	}
	msg := c.Commit.Message
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.TrimSpace(msg)
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return &commitView{SHA: c.SHA, Message: msg, Date: c.Commit.Committer.Date, URL: c.HTMLURL}
}

// handleUpdateAvailable is GET /api/update/available (§2.20.1): the
// active channel, the latest stable, the latest dev-rN prerelease and the
// latest dev-branch commit. Caching: releases ride the shared 10-min
// feed cache (L23 rate-limit protection), commits their own 5-min cache;
// ?refresh=1 drops both (throttled to 30 s like /api/version).
func handleUpdateAvailable(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		dropReleaseCache()
		dropDevCommitCache()
	}
	rels, rerr := releasesFetcher()
	resp := map[string]any{
		"channel":    updateChannel(),
		"stable":     latestChannelRelease(channelStable, rels),
		"prerelease": latestChannelRelease(channelDev, rels),
	}
	if rerr != nil {
		resp["error"] = rerr.Error()
	}
	if t := releaseCheckedAt(); !t.IsZero() {
		resp["checked_at"] = t.Unix()
	}
	commit, cerr := fetchDevCommitCached()
	resp["commit"] = commitWire(commit)
	if cerr != nil {
		resp["commit_error"] = cerr.Error()
	}
	writeJSON(w, resp)
}

// agentUpdateRow is one agent's discovery view (§2.20.2).
type agentUpdateRow struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Arch    string `json:"arch,omitempty"`
	OS      string `json:"os,omitempty"`
	Version string `json:"version"`
	Status  string `json:"status"`

	OutdatedStable     bool                  `json:"outdated_stable"`
	OutdatedPrerelease bool                  `json:"outdated_prerelease"`
	ArtifactStable     *agentReleaseArtifact `json:"artifact_stable"`
	ArtifactPrerelease *agentReleaseArtifact `json:"artifact_prerelease"`
}

// handleAgentUpdates is GET /api/agents/updates (§2.20.2): per-agent
// version/arch, outdatedness vs the latest stable and the latest dev-rN
// prerelease, and the arch-resolved artifact URLs. Group-update state
// rides along so the UI can render progress without a second poll.
func handleAgentUpdates(w http.ResponseWriter, r *http.Request) {
	agentRegMu.Lock()
	agents, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		agentAPIError(w, r, "E-AGENT-06", http.StatusServiceUnavailable, "cannot read the agent registry")
		return
	}
	rels, rerr := releasesFetcher()
	stable := latestChannelRelease(channelStable, rels)
	prerelease := latestChannelRelease(channelDev, rels)
	latestStable, latestDev := "", ""
	if stable != nil {
		latestStable = stable.Tag
	}
	if prerelease != nil {
		latestDev = prerelease.Tag
	}
	resp := map[string]any{
		"stable":     stable,
		"prerelease": prerelease,
		"commit":     commitWire(mustDevCommitCached()),
		"agents":     []agentUpdateRow{},
	}
	if rerr != nil {
		resp["error"] = rerr.Error()
	}
	if rep := agentGroupUpdateSnapshot(); rep != nil {
		resp["last_group_update"] = rep
	}
	rows := []agentUpdateRow{}
	for _, a := range agents {
		row := agentUpdateRow{
			ID:      a.ID,
			Name:    a.Name,
			Arch:    a.Arch,
			OS:      a.OS,
			Version: a.Version,
			Status:  agentRowStatus(a),
		}
		// Outdatedness only means something when the agent reported a
		// version; unknown versions stay false (honest unknown).
		if a.Version != "" && rerr == nil {
			row.OutdatedStable = latestStable != "" &&
				updateAvailable(a.Version, latestStable, channelStable)
			row.OutdatedPrerelease = latestDev != "" &&
				updateAvailable(a.Version, latestDev, channelDev)
			row.ArtifactStable = agentReleaseArtifactFor(channelStable, latestStable, a.Arch, stable)
			row.ArtifactPrerelease = agentReleaseArtifactFor(channelDev, latestDev, a.Arch, prerelease)
		}
		rows = append(rows, row)
	}
	resp["agents"] = rows
	writeJSON(w, resp)
}

// mustDevCommitCached fetches the dev commit ignoring the error (nil on
// failure — the response stays honest via the absence, the /available
// endpoint carries the error text).
func mustDevCommitCached() *ghCommit {
	c, _ := fetchDevCommitCached()
	return c
}

// agentRowStatus mirrors agentPublicView's live-wins rule without
// building the whole public view.
func agentRowStatus(a agentRecord) string {
	status := a.Status
	if !a.Revoked && agentHubOnline(a.ID) {
		status = agentStatusOnline
	}
	if status == "" {
		status = agentStatusOffline
	}
	return status
}

// ---- group update (§2.20.3): the light queue ------------------------------

// agentGroupUpdateResult is one agent's outcome in a group run.
type agentGroupUpdateResult struct {
	AgentID string `json:"agent_id"`
	Agent   string `json:"agent,omitempty"`
	Status  string `json:"status"` // "ok" | "failed"
	Error   string `json:"error,omitempty"`
}

// agentGroupUpdateReport is one group run's outcome (surfaced via
// GET /api/agents/updates "last_group_update").
type agentGroupUpdateReport struct {
	Channel    string                   `json:"channel"`
	Tag        string                   `json:"tag"`
	StartedAt  int64                    `json:"started_at"`
	FinishedAt int64                    `json:"finished_at,omitempty"`
	Results    []agentGroupUpdateResult `json:"results"`
}

// agentUpdateDispatch is the seam the group queue drives (tests swap it;
// prod = the unchanged M2 verify-then-hand-off path).
var agentUpdateDispatch = agentUpdatePrepareFor

var (
	agentGroupMu   sync.Mutex
	agentGroupRun  bool
	agentGroupLast *agentGroupUpdateReport
)

// agentGroupUpdateSnapshot returns the last finished (or running) group
// report, or nil.
func agentGroupUpdateSnapshot() *agentGroupUpdateReport {
	agentGroupMu.Lock()
	defer agentGroupMu.Unlock()
	return agentGroupLast
}

// agentGroupUpdateStart queues a group update for one channel. It
// validates the request, snapshots the work list synchronously (so the
// response is exact) and runs the updates one at a time in the
// background; a second concurrent run is refused.
func agentGroupUpdateStart(ch string) (map[string]any, error) {
	agentRegMu.Lock()
	agents, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		return nil, errors.New("cannot read the agent registry")
	}
	rels, rerr := releasesFetcher()
	if rerr != nil {
		return nil, errors.New("cannot reach the release feed: " + rerr.Error())
	}
	tag := pickChannelTag(ch, rels)
	if tag == "" || !validTag(tag) {
		return nil, errors.New("no usable " + ch + " release discovered yet")
	}
	if !agentUpdateVersionRe.MatchString(tag) {
		return nil, errors.New("discovered tag is not an updatable version shape")
	}
	rel := latestChannelRelease(ch, rels)

	agentGroupMu.Lock()
	if agentGroupRun {
		agentGroupMu.Unlock()
		return nil, errors.New("a group update is already running")
	}
	// Snapshot the work list: every agent that is non-revoked, reports a
	// version, is outdated for this target, and has a resolvable artifact.
	type queued struct {
		id, name, url string
	}
	var work []queued
	var skipped []map[string]any
	for _, a := range agents {
		if a.Revoked || a.Status == agentStatusRevoked {
			skipped = append(skipped, map[string]any{"agent_id": a.ID, "reason": "revoked"})
			continue
		}
		if a.Version == "" {
			skipped = append(skipped, map[string]any{"agent_id": a.ID, "reason": "no reported version"})
			continue
		}
		if !updateAvailable(a.Version, tag, ch) {
			continue // up to date — silently not part of this run
		}
		art := agentReleaseArtifactFor(ch, tag, a.Arch, rel)
		if art == nil {
			skipped = append(skipped, map[string]any{"agent_id": a.ID, "reason": "unknown agent arch"})
			continue
		}
		work = append(work, queued{id: a.ID, name: a.Name, url: art.URL})
	}
	rep := &agentGroupUpdateReport{
		Channel:   ch,
		Tag:       tag,
		StartedAt: time.Now().Unix(),
		Results:   []agentGroupUpdateResult{},
	}
	agentGroupRun = true
	agentGroupLast = rep
	agentGroupMu.Unlock()

	queuedOut := []map[string]any{}
	for _, w := range work {
		queuedOut = append(queuedOut, map[string]any{
			"agent_id": w.id, "name": w.name, "url": w.url, "tag": tag,
		})
	}

	// The light queue: one agent at a time, each through the unchanged
	// verify-then-hand-off path. Progress/outcome is visible via
	// last_group_update and the regular agent state.
	go func(work []queued, tag string) {
		for _, w := range work {
			res := agentGroupUpdateResult{AgentID: w.id, Agent: w.name}
			if _, err := agentUpdateDispatch(w.id, tag, w.url); err != nil {
				res.Status = "failed"
				res.Error = err.Error()
			} else {
				res.Status = "ok"
			}
			agentGroupMu.Lock()
			agentGroupLast.Results = append(agentGroupLast.Results, res)
			agentGroupMu.Unlock()
		}
		agentGroupMu.Lock()
		agentGroupLast.FinishedAt = time.Now().Unix()
		agentGroupRun = false
		agentGroupMu.Unlock()
	}(work, tag)

	return map[string]any{
		"status":  "queued",
		"channel": ch,
		"tag":     tag,
		"queued":  queuedOut,
		"skipped": skipped,
	}, nil
}

// handleAgentUpdateOutdatedPost is POST /api/agents/update-outdated
// (§2.20.3): {"channel":"stable"|"dev"} — default = the active channel.
func handleAgentUpdateOutdatedPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel string `json:"channel"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		agentAPIError(w, r, "E-AGENT-08", http.StatusBadRequest, `body must be {"channel":"stable"|"dev"}`)
		return
	}
	if req.Channel == "" {
		req.Channel = updateChannel()
	}
	if req.Channel != channelStable && req.Channel != channelDev {
		agentAPIError(w, r, "E-AGENT-08", http.StatusBadRequest, "channel must be stable or dev")
		return
	}
	out, err := agentGroupUpdateStart(req.Channel)
	if err != nil {
		if strings.Contains(err.Error(), "already running") {
			agentAPIError(w, r, "E-AGENT-08", http.StatusConflict, err.Error())
			return
		}
		agentAPIError(w, r, "E-AGENT-08", http.StatusBadRequest, err.Error())
		return
	}
	LogSecurityAudit("agent_update_group", cfg.Username, ClientIP(r),
		"channel="+req.Channel+" queued="+itoaFast(len(out["queued"].([]map[string]any))))
	writeJSON(w, out)
}
