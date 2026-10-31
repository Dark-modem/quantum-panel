//go:build !linux

package main

// Non-Linux builds (the Windows lab harness) cannot self-update; the
// command is refused with a clear error instead of misbehaving.
func agentUpdateRun(req agentUpdateRequest, ident agentIdentity) {
	agentUpdateSetProgress("failed", req.Version, "self-update is supported on linux builds only")
}

func agentUpdateSupported() bool { return false }
