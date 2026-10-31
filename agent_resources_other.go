//go:build !linux

package main

import "runtime"

// Non-Linux build of the resource reporter (the lab harness builds Windows
// binaries for enroll/status; production endpoints are Linux). Only the
// platform-independent facts are reported.
func agentCollectResources() *agentResourceReport {
	return &agentResourceReport{
		CPUModel: runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
	}
}

// Keep the seam symbol present so tests compile on every platform.
var agentHostExec = func(name string, args ...string) (string, error) { return "", nil }
