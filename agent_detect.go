package main

// Read-only PasarGuard Node detection. Best-effort and strictly passive:
// the agent looks for install directories and running container names and
// never starts, stops or writes anything. Any error means detected=false.

import (
	"os"
	"os/exec"
	"strings"
)

// agentDockerPS lists running container names; seam for tests. Only touched
// when a docker binary exists. Holder-guarded: heartbeats call it from the
// hub session while tests rotate the seam.
var agentDockerPS agentSeam[func() (string, error)]

func init() {
	agentDockerPS.set(func() (string, error) {
		if _, err := exec.LookPath("docker"); err != nil {
			return "", err
		}
		out, err := exec.Command("docker", "ps", "--format", "{{.Names}}").CombinedOutput()
		return string(out), err
	})
}

// agentPasarguardDirs are the well-known PasarGuard install locations.
var agentPasarguardDirs = []string{"/opt/pasarguard", "/etc/pasarguard"}

// agentDetectNode probes for a PasarGuard Node installation.
func agentDetectNode() *agentNodeDetect {
	for _, dir := range agentPasarguardDirs {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return &agentNodeDetect{Detected: true, Kind: "install-dir", Detail: dir}
		}
	}
	out, err := agentDockerPS.get()()
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			name := strings.TrimSpace(line)
			if name == "" {
				continue
			}
			low := strings.ToLower(name)
			if strings.Contains(low, "pasarguard") || strings.Contains(low, "node") {
				return &agentNodeDetect{Detected: true, Kind: "docker-container", Detail: name}
			}
		}
	}
	return &agentNodeDetect{Detected: false}
}
