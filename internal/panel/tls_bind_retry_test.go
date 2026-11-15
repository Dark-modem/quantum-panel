package panel

// RUNBOOK-FIXES item 5 regression tests: the HTTPS listener must retry a
// bind-class failure with backoff (RC2 lost HTTPS for good after ONE
// failed bind) and fail the unit loudly when the port stays busy through
// every attempt. Real loopback binds; nothing touches the host.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testTLSCert mints a throwaway self-signed certificate so the HTTPS
// server under test can complete handshakes on loopback.
func testTLSCert(t *testing.T) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "bind-retry-test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// bindRetryTestServer builds an http.Server for addr with the test cert
// and a trivial handler.
func bindRetryTestServer(addr string, cert *tls.Certificate) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil },
		},
	}
}

// stubTLSBindFatal swaps tlsBindFatalf for a panicking stub so an
// exhausted schedule unwinds the serving goroutine instead of exiting the
// test process. Returns a channel receiving the formatted fatal message.
func stubTLSBindFatal(t *testing.T) <-chan string {
	t.Helper()
	old := tlsBindFatalf
	t.Cleanup(func() { tlsBindFatalf = old })
	ch := make(chan string, 1)
	tlsBindFatalf = func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		ch <- msg
		panic("tlsBindFatalf: " + msg)
	}
	return ch
}

func stubTLSBindDelays(t *testing.T, delays ...time.Duration) {
	t.Helper()
	old := tlsBindRetryDelays
	tlsBindRetryDelays = delays
	t.Cleanup(func() { tlsBindRetryDelays = old })
}

// TestHTTPSBindRetriesUntilPortFrees: the port is busy on the first
// attempt and frees while the retry loop is backing off — HTTPS must come
// up anyway (the RC2 defect: one failed bind disabled HTTPS forever).
func TestHTTPSBindRetriesUntilPortFrees(t *testing.T) {
	stubTLSBindDelays(t, 20*time.Millisecond, 30*time.Millisecond, 40*time.Millisecond)
	fatalMsg := stubTLSBindFatal(t)

	// Pick a loopback port and free it, then SCRIPT the first bind attempt
	// to fail with the transient EADDRINUSE the retry loop exists for;
	// later attempts serve for real. (On Windows Go's listener sets
	// SO_REUSEADDR, so a live squatter cannot simulate a busy bind — the
	// seam makes the failure deterministic on both gates, BACKEND-24.)
	addr := "127.0.0.1:" + strconv.Itoa(pickFreePortForTest(t))
	srv := bindRetryTestServer(addr, testTLSCert(t))
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	oldServe := tlsListenAndServeFn
	attempts := 0
	tlsListenAndServeFn = func(srv *http.Server) error {
		attempts++
		if attempts == 1 {
			return &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "listen", Err: syscall.EADDRINUSE}}
		}
		return oldServe(srv)
	}
	t.Cleanup(func() { tlsListenAndServeFn = oldServe })
	serving := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				serving <- fmt.Sprint(r)
			}
		}()
		tlsServeWithRetry(srv)
		serving <- "" // returned without a fatal: not serving anymore
	}()

	client := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	var lastErr error
	for i := 0; i < 100; i++ {
		resp, err := client.Get("https://" + addr + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("unexpected status %d", resp.StatusCode)
			}
			if attempts < 2 {
				t.Fatalf("endpoint answered after only %d attempts", attempts)
			}
			select {
			case msg := <-fatalMsg:
				t.Fatalf("schedule was NOT exhausted but the unit was failed loudly: %s", msg)
			case <-serving:
				t.Fatal("serve goroutine returned while the endpoint answers")
			default:
			}
			return // PASS: HTTPS came up after the scripted first failure
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("HTTPS never came up after the port freed: %v", lastErr)
}

// pickFreePortForTest picks a loopback TCP port that is free NOW (the
// caller must use it immediately; the check-then-use window is inherent).
func pickFreePortForTest(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	time.Sleep(5 * time.Millisecond) // settle before the product binds it
	return port
}

// TestHTTPSBindFatalAfterExhaustedRetries: a port that stays busy through
// every attempt fails the unit LOUDLY (systemd restarts it; the journal
// shows why) instead of silently serving plain HTTP forever.
func TestHTTPSBindFatalAfterExhaustedRetries(t *testing.T) {
	stubTLSBindDelays(t, 10*time.Millisecond)
	fatalMsg := stubTLSBindFatal(t)

	addr := "127.0.0.1:" + strconv.Itoa(pickFreePortForTest(t))
	srv := bindRetryTestServer(addr, testTLSCert(t))
	done := make(chan struct{})
	oldServe := tlsListenAndServeFn
	tlsListenAndServeFn = func(*http.Server) error {
		return &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "listen", Err: syscall.EADDRINUSE}} // every attempt fails: the schedule must exhaust
	}
	t.Cleanup(func() { tlsListenAndServeFn = oldServe })
	go func() {
		defer close(done)
		defer func() { _ = recover() }() // the stub panics to unwind
		tlsServeWithRetry(srv)
	}()

	select {
	case msg := <-fatalMsg:
		if !strings.Contains(msg, addr) {
			t.Fatalf("fatal message lacks the bind address: %s", msg)
		}
		if !strings.Contains(msg, "failed after 1 attempts") {
			t.Fatalf("fatal message lacks the attempt count: %s", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tlsBindFatalf never called after the schedule was exhausted")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serve goroutine did not unwind after the fatal stub panicked")
	}
}

// TestHTTPSNonBindErrorRecorded: a configuration error (no certificate in
// the TLS config) will not heal by retrying — it is recorded once as
// E-TLS-08 and the loop returns without failing the unit.
func TestHTTPSNonBindErrorRecorded(t *testing.T) {
	stubTLSBindDelays(t, 10*time.Millisecond, 10*time.Millisecond)
	stubTLSBindFatal(t)

	srv := &http.Server{
		Addr:      "127.0.0.1:0",
		Handler:   http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, // no cert, no GetCertificate
	}
	// ListenAndServeTLS("", "") with no certificate configured fails with
	// a fs.PathError ("open : no such file or directory") — a non-bind
	// error: recorded once, no retry, no unit failure. The failure is
	// INJECTED via the serve seam so the message is identical on both
	// gates (the real Windows error text differs, BACKEND-24).
	oldServe := tlsListenAndServeFn
	tlsListenAndServeFn = func(*http.Server) error {
		// A plain non-bind error whose TEXT is identical on both gates (a
		// real syscall.ENOENT renders the platform's own message: "no such
		// file or directory" on Linux, "The system cannot find the file
		// specified." on Windows).
		return &fs.PathError{Op: "open", Path: "", Err: errNoSuchFile}
	}
	t.Cleanup(func() { tlsListenAndServeFn = oldServe })
	tlsServeWithRetry(srv) // must return on its own

	errMu.Lock()
	defer errMu.Unlock()
	for i := len(errEvents) - 1; i >= 0; i-- {
		if errEvents[i].Code == "E-TLS-08" && strings.Contains(errEvents[i].Detail, "no such file") {
			return // PASS
		}
	}
	t.Fatal("E-TLS-08 not recorded for the non-bind failure")
}

// errNoSuchFile: a non-bind error with the UNIX message text so the
// E-TLS-08 assertion is platform-neutral (BACKEND-24).
var errNoSuchFile = errors.New("no such file or directory")

// TestTLSIsBindErr: exactly the transient bind-family errors are
// retryable; everything else takes the record-once path.
func TestTLSIsBindErr(t *testing.T) {
	cases := []struct {
		err   error
		want  bool
		label string
	}{
		{&net.OpError{Op: "listen", Err: syscall.EADDRINUSE}, true, "EADDRINUSE wrapped in OpError (real bind shape)"},
		{syscall.EADDRINUSE, true, "bare EADDRINUSE"},
		{syscall.EACCES, true, "EACCES"},
		{syscall.EADDRNOTAVAIL, true, "EADDRNOTAVAIL"},
		{http.ErrServerClosed, false, "ErrServerClosed"},
		{errors.New("http: TLS configuration has no certificate"), false, "config error"},
		{nil, false, "nil"},
	}
	for _, c := range cases {
		if got := tlsIsBindErr(c.err); got != c.want {
			t.Errorf("%s: tlsIsBindErr = %v, want %v", c.label, got, c.want)
		}
	}
}
