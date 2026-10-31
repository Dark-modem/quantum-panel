//go:build linux

package main

// Linux side of the agent self-update: the atomic swap and the detached
// self-restart. Runs entirely after signature verification; nothing here
// interprets Controller-supplied strings beyond the validated fields.

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
)

// agentSelfUnit determines the systemd unit this agent runs under by
// reading the cgroup path (systemd names the leaf after the unit), so the
// restart targets the right unit even for lab variants (gre-agent-lab).
func agentSelfUnit() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "gre-agent"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.LastIndex(line, "/"); i >= 0 {
			leaf := strings.TrimSpace(line[i+1:])
			if strings.HasSuffix(leaf, ".service") {
				return strings.TrimSuffix(leaf, ".service")
			}
		}
	}
	return "gre-agent"
}

// agentUpdateRun performs the full update. Called in its own goroutine by
// the command handler (which already answered the Controller); progress is
// published via heartbeats. ident supplies the download credentials.
func agentUpdateRun(req agentUpdateRequest, ident agentIdentity) {
	agentUpdateSetProgress("downloading", req.Version, "")
	artifact, err := agentUpdateDownload(req.URL, ident.ID, ident.Key)
	if err != nil {
		agentUpdateSetProgress("failed", req.Version, "download: "+err.Error())
		return
	}
	agentUpdateSetProgress("verifying", req.Version, "")
	if err := agentUpdateVerifyArtifact(artifact, req); err != nil {
		agentUpdateSetProgress("failed", req.Version, err.Error())
		return
	}
	reported, err := agentUpdateSelfReportVersion(artifact)
	if err != nil {
		agentUpdateSetProgress("failed", req.Version, err.Error())
		return
	}
	if reported != req.Version {
		agentUpdateSetProgress("failed", req.Version, fmt.Sprintf("artifact reports %s, requested %s", reported, req.Version))
		return
	}

	exe, err := os.Executable()
	if err != nil {
		agentUpdateSetProgress("failed", req.Version, "cannot locate own binary: "+err.Error())
		return
	}
	exe, _ = filepathAbs(exe)
	fromSHA := agentFileSHA(exe)
	prevPath := exe + ".prev"
	if err := os.WriteFile(prevPath, mustRead(exe), 0755); err != nil {
		agentUpdateSetProgress("failed", req.Version, "cannot save the previous binary: "+err.Error())
		return
	}
	newPath := exe + ".new"
	if err := os.WriteFile(newPath, artifact, 0755); err != nil {
		agentUpdateSetProgress("failed", req.Version, "cannot write the artifact: "+err.Error())
		return
	}
	if err := os.Rename(newPath, exe); err != nil { // atomic on the same filesystem
		agentUpdateSetProgress("failed", req.Version, "atomic swap failed: "+err.Error())
		return
	}
	_ = agentUpdateMarkerSave(&agentUpdateMarker{Version: req.Version, FromSHA: fromSHA, PrevPath: prevPath, StartedAt: timeNow().Unix()})
	agentUpdateSetProgress("restarting", req.Version, "")

	// Detached restart: a new session, so it survives this process exiting.
	cmd := execCommand("systemctl", "restart", agentSelfUnit())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
	// Give the result/heartbeat write a moment, then the restart lands.
	timeSleep(700 * time.Millisecond)
}

// agentUpdateSupported reports whether this build can self-update.
func agentUpdateSupported() bool { return true }
