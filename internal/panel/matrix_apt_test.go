package panel

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMatrixAptWaitsForLockWithoutBreakingIt(t *testing.T) {
	oldExec, oldSleep, oldNow := matrixExec, matrixAptSleep, matrixAptNow
	t.Cleanup(func() { matrixExec, matrixAptSleep, matrixAptNow = oldExec, oldSleep, oldNow })
	now := time.Unix(0, 0)
	matrixAptNow = func() time.Time { return now }
	matrixAptSleep = func(d time.Duration) { now = now.Add(d) }
	var calls []string
	matrixExec = func(name string, args ...string) (string, error) {
		cmd := name + " " + strings.Join(args, " ")
		calls = append(calls, cmd)
		if !strings.Contains(cmd, "DPkg::Lock::Timeout=90") {
			t.Fatal("missing dpkg native lock wait")
		}
		if len(calls) == 1 {
			return "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 18922 (apt)", fmt.Errorf("exit status 100")
		}
		return "", nil
	}
	if e := matrixAptGet("install", "-y", "tinc"); e != nil {
		t.Fatal(e)
	}
	if len(calls) != 2 {
		t.Fatalf("wanted retry, got %d calls", len(calls))
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "apt-get ") || strings.Contains(c, "kill") || strings.Contains(c, "rm ") {
			t.Fatal("unsafe recovery", c)
		}
	}
}
func TestMatrixAptLockTimeoutHasActionableError(t *testing.T) {
	oldExec, oldSleep, oldNow := matrixExec, matrixAptSleep, matrixAptNow
	t.Cleanup(func() { matrixExec, matrixAptSleep, matrixAptNow = oldExec, oldSleep, oldNow })
	now := time.Unix(0, 0)
	matrixAptNow = func() time.Time { return now }
	matrixAptSleep = func(d time.Duration) { now = now.Add(90 * time.Second) }
	matrixExec = func(string, ...string) (string, error) {
		return "Unable to acquire the dpkg frontend lock", fmt.Errorf("exit status 100")
	}
	e := matrixAptGet("install", "tinc")
	if !errors.Is(e, errMatrixPackageBusy) {
		t.Fatal(e)
	}
	rr := httptest.NewRecorder()
	writeMatrixSetupError(rr, httptest.NewRequest("POST", "/api/matrix/setup", nil), e)
	if rr.Code != 409 || !strings.Contains(rr.Body.String(), "E-MATRIX-09") {
		t.Fatal(rr.Code, rr.Body.String())
	}
}
func TestMatrixAptNonLockFailureDoesNotRetry(t *testing.T) {
	old := matrixExec
	t.Cleanup(func() { matrixExec = old })
	calls := 0
	matrixExec = func(string, ...string) (string, error) {
		calls++
		return "E: Unable to locate package tinc", fmt.Errorf("exit status 100")
	}
	if e := matrixAptGet("install", "tinc"); e == nil || errors.Is(e, errMatrixPackageBusy) {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("unexpected retry", calls)
	}
}
func TestMatrixTincAlreadyInstalledSkipsApt(t *testing.T) {
	oldExec, oldPath := matrixExec, matrixTincPath
	t.Cleanup(func() { matrixExec, matrixTincPath = oldExec, oldPath })
	matrixTincPath = func() (string, error) { return "/usr/sbin/tincd", nil }
	matrixExec = func(string, ...string) (string, error) { t.Fatal("apt called despite existing tinc"); return "", nil }
	if e := matrixInstallTinc(); e != nil {
		t.Fatal(e)
	}
}
