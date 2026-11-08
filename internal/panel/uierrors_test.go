package panel

// uierrors_test.go — the structured error catalog (Phase 11 req 6):
// EVERY code carries fa AND en for all four text fields; details are
// redacted by allowlist + scrubbing; agent error texts map to codes.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIErrorCatalogComplete(t *testing.T) {
	if len(uiErrCatalog) < 20 {
		t.Fatalf("catalog suspiciously small: %d codes", len(uiErrCatalog))
	}
	for code, e := range uiErrCatalog {
		for field, txt := range map[string]uiErrText{
			"title": e.title, "what_happened": e.happened,
			"likely_cause": e.cause, "what_to_do": e.todo,
		} {
			if strings.TrimSpace(txt.en) == "" || strings.TrimSpace(txt.fa) == "" {
				t.Errorf("%s.%s: en=%q fa=%q — both languages required", code, field, txt.en, txt.fa)
			}
		}
		if e.status < 400 || e.status > 599 {
			t.Errorf("%s: bad HTTP status %d", code, e.status)
		}
	}
	// The codes the contract names must exist.
	for _, code := range []string{"E-ENGINE-NOT-ENABLED", "E-ENGINE-NO-ARTIFACT",
		"E-AGENT-OFFLINE", "E-ENGINE-PORT-CONFLICT", "E-ENGINE-SIGNATURE",
		"E-ENGINE-KEY-MISMATCH", "E-ENGINE-PEER-UNREACH", "E-ENGINE-PERMISSION",
		"E-ENGINE-KERNEL-MODULE", "E-ENGINE-UNKNOWN-ERROR"} {
		if _, ok := uiErrCatalog[code]; !ok {
			t.Errorf("contract code %s missing from catalog", code)
		}
	}
}

func TestUIErrorRedaction(t *testing.T) {
	// Seeded secrets must never survive uiErrWrite.
	secretKey := "SUPER-SECRET-AGENT-KEY-0123456789"
	psk := "MATRIX-PSK-SECRET-9876543210"
	hexMaterial := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/ui/x", nil)
	uiErrWrite(rec, req, "E-ENGINE-START-FAILED", map[string]any{
		"engine":        "chisel",
		"agent":         "agt_ok",
		"private_field": secretKey, // NOT in the allowlist → dropped
		"raw":           "start failed: key=" + secretKey + " psk:" + psk + " sha " + hexMaterial,
	}, "")
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	text := rec.Body.String()
	for _, secret := range []string{secretKey, psk, hexMaterial} {
		if strings.Contains(text, secret) {
			t.Fatalf("SECRET %q leaked into the error envelope", secret)
		}
	}
	ui, ok := resp["ui"].(map[string]any)
	if !ok {
		t.Fatalf("no ui object: %s", text)
	}
	if ui["code"] != "E-ENGINE-START-FAILED" {
		t.Fatalf("code: %v", ui["code"])
	}
	for _, field := range []string{"title", "what_happened", "likely_cause", "what_to_do", "details", "docs_ref"} {
		if _, ok := ui[field]; !ok {
			t.Fatalf("ui.%s missing: %s", field, text)
		}
	}
	details := ui["details"].(map[string]any)
	if _, ok := details["private_field"]; ok {
		t.Fatal("non-allowlisted detail key must be dropped")
	}
	if details["engine"] != "chisel" || details["agent"] != "agt_ok" {
		t.Fatalf("allowlisted details lost: %v", details)
	}
	if raw, _ := details["raw"].(string); !strings.Contains(raw, "[redacted]") {
		t.Fatalf("raw reason not scrubbed: %q", raw)
	}
	// Legacy fields survive for old consumers.
	if resp["error_code"] != "E-ENGINE-START-FAILED" || resp["error"] == nil || resp["hint"] == nil {
		t.Fatalf("legacy flat fields missing: %s", text)
	}
}

func TestClassifyAgentError(t *testing.T) {
	cases := map[string]string{
		"listen tcp :7500: bind: address already in use":     "E-ENGINE-PORT-CONFLICT",
		"verify artifact: sha256 mismatch":                   "E-ENGINE-SIGNATURE",
		"artifact missing for arch arm64":                    "E-ENGINE-INSTALL-FAILED",
		"exec: no such file or directory":                    "E-ENGINE-INSTALL-FAILED",
		"handshake failed: bad key":                          "E-ENGINE-KEY-MISMATCH",
		"dial tcp 1.2.3.4:7443: connect: connection refused": "E-ENGINE-PEER-UNREACH",
		"open /dev/net/tun: permission denied":               "E-ENGINE-PERMISSION",
		"wireguard: kernel module not loaded":                "E-ENGINE-KERNEL-MODULE",
		"something entirely new happened here":               "",
	}
	for text, want := range cases {
		if got := classifyAgentError(text); got != want {
			t.Errorf("classify(%q) = %q, want %q", text, got, want)
		}
	}
}
