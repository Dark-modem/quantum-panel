// client.go — the typed PasarGuard admin-API client. Endpoint contract in
// 00-ADMIN/PASARGUARD-INTEGRATION.md §4 (verified against
// pasargurad-panel-5.2.1 routers). Auth is a Bearer token acquired from
// POST /api/admin/token (OAuth2 password form) and re-acquired
// transparently on 401; upstream has no refresh endpoint.

package pasarguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults.
const (
	DefaultTimeout   = 10 * time.Second
	DefaultKeepAlive = 60
	DefaultNodePort  = 62050
	DefaultAPIPort   = 62051
	maxRetries       = 3
	maxBodyBytes     = 4 << 20
)

// TLSOptions harden the transport: custom CA, or SPKI SHA-256 pinning
// (base64), or both. Empty options = system roots (the panel usually sits
// behind a real cert or plain HTTP on a trusted LAN).
type TLSOptions struct {
	CAPem     string `json:"ca_pem,omitempty"`     // custom root(s), PEM
	PinSHA256 string `json:"pin_sha256,omitempty"` // base64 SPKI sha256 pin
}

// Credentials are the panel admin login. They never get logged and never
// appear in errors.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Client is one panel connection. Safe for concurrent use.
type Client struct {
	baseURL     string // normalized, no trailing slash
	creds       Credentials
	http        *http.Client
	tokenMu     sync.Mutex
	token       string
	tokenExpies time.Time
	now         func() time.Time // test seam
}

// New builds a client for baseURL (http/https). TLSOptions apply to
// https; insecureSkipVerify is available for the rare self-signed panel
// front but ALWAYS pairs with a pin in production use.
func New(baseURL string, creds Credentials, tlsOpts TLSOptions, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("pasarguard: invalid panel URL %q", baseURL)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if tlsOpts.CAPem != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(tlsOpts.CAPem)) {
			return nil, errors.New("pasarguard: no usable certificate in ca_pem")
		}
		tlsCfg.RootCAs = pool
	}
	if tlsOpts.PinSHA256 != "" {
		pin, err := base64.StdEncoding.DecodeString(tlsOpts.PinSHA256)
		if err != nil || len(pin) != sha256.Size {
			return nil, errors.New("pasarguard: pin_sha256 must be base64 of a SHA-256 digest")
		}
		tlsCfg.VerifyPeerCertificate = pinVerifier(tlsOpts.PinSHA256, tlsCfg)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		baseURL: u.Scheme + "://" + u.Host,
		creds:   creds,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
		now: time.Now,
	}, nil
}

// pinVerifier returns a VerifyPeerCertificate that checks the leaf SPKI
// hash against the pin (system/custom-CA verification still runs first
// unless RootCAs+InsecureSkipVerify say otherwise).
func pinVerifier(pinB64 string, cfg *tls.Config) func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("pasarguard: no server certificate to pin")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("pasarguard: parse server cert: %v", err)
		}
		sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
		if base64.StdEncoding.EncodeToString(sum[:]) != pinB64 {
			return errors.New("pasarguard: server certificate pin mismatch")
		}
		return nil
	}
}

// APIError is a typed non-2xx answer.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("pasarguard: HTTP %d: %s", e.Status, e.Detail)
}

// NotFound reports a 404 (missing node).
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// ---- token --------------------------------------------------------------

// tokenValid returns the cached token while it has ≥60 s of assumed life
// (upstream default expiry is 24 h; we re-login well before the edge).
func (c *Client) tokenValid() (string, bool) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && c.now().Before(c.tokenExpies.Add(-60*time.Second)) {
		return c.token, true
	}
	return "", false
}

// login exchanges the credentials for a fresh token. The request body is a
// form (upstream OAuth2PasswordRequestForm), never JSON.
func (c *Client) login(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.creds.Username)
	form.Set("password", c.creds.Password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/admin/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Content-Length", strconv.Itoa(len(form.Encode())))
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("pasarguard: login: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Detail: "authentication failed"}
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return errors.New("pasarguard: login response missing access_token")
	}
	c.tokenMu.Lock()
	c.token = tok.AccessToken
	// The token body does not carry its expiry; assume the upstream default
	// (24 h) and let the 401 path re-login earlier if the operator lowered it.
	c.tokenExpies = c.now().Add(24 * time.Hour)
	c.tokenMu.Unlock()
	return nil
}

// do executes one authenticated call with transparent re-login on 401 and
// retries (idempotent calls only). The token is redacted from any error.
func (c *Client) do(ctx context.Context, method, path string, body any, into any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	idempotent := method == http.MethodGet || method == http.MethodDelete || method == http.MethodPut
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if !idempotent {
				break
			}
			// Exponential backoff, capped; ctx-aware.
			backoff := time.Duration(1<<uint(attempt-1)) * 300 * time.Millisecond
			if backoff > 3*time.Second {
				backoff = 3 * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		tok, ok := c.tokenValid()
		if !ok {
			if err := c.login(ctx); err != nil {
				lastErr = err
				continue // login itself may be a transient failure (GET retry only)
			}
			tok, _ = c.tokenValid()
		}
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if into != nil && len(bodyBytes) > 0 {
				if err := decodeJSON(bodyBytes, into); err != nil {
					return err
				}
			}
			return nil
		case resp.StatusCode == http.StatusUnauthorized:
			// Token expired/revoked mid-flight: drop it and let the next
			// attempt re-login (idempotent paths only; a mutating call
			// reports the auth failure instead of risking a double apply —
			// upstream mutations are not idempotent on our side either).
			c.tokenMu.Lock()
			c.token = ""
			c.tokenMu.Unlock()
			if !idempotent {
				return &APIError{Status: resp.StatusCode, Detail: authDetail(bodyBytes)}
			}
			lastErr = &APIError{Status: resp.StatusCode, Detail: authDetail(bodyBytes)}
			continue
		case resp.StatusCode >= 500 && idempotent:
			// Transient panel-side failure: retry with backoff.
			lastErr = &APIError{Status: resp.StatusCode, Detail: trimDetail(bodyBytes)}
			continue
		default:
			return &APIError{Status: resp.StatusCode, Detail: trimDetail(bodyBytes)}
		}
	}
	return lastErr
}

func authDetail(body []byte) string {
	// Upstream sends {"detail": "Incorrect username or password"} — pass the
	// upstream detail through but never echo credentials (they are not in
	// responses anyway).
	return trimDetail(body)
}

func trimDetail(body []byte) string {
	var e struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Detail) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.Detail, &s) == nil && strings.TrimSpace(s) != "" {
		return truncateDetail(s)
	}
	// FastAPI 422: [{"loc": [...], "msg": "..."}, …]
	var arr []struct {
		Msg string `json:"msg"`
		Loc []any  `json:"loc"`
	}
	if json.Unmarshal(e.Detail, &arr) == nil && len(arr) > 0 {
		var msgs []string
		for _, a := range arr {
			if a.Msg != "" {
				msgs = append(msgs, a.Msg)
			}
		}
		return truncateDetail(strings.Join(msgs, "; "))
	}
	// Pydantic field validators: {"field": "Value error, msg", …}
	var dict map[string]string
	if json.Unmarshal(e.Detail, &dict) == nil && len(dict) > 0 {
		var msgs []string
		for k, v := range dict {
			msgs = append(msgs, k+": "+v)
		}
		sort.Strings(msgs)
		return truncateDetail(strings.Join(msgs, "; "))
	}
	return ""
}

func truncateDetail(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
