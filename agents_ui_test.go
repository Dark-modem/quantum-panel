package main

// agents_ui_test.go — the Agents tab must be wired into the page markup, the
// Persian glossary must carry the new phrases, and the new interactive
// elements must be labelled for screen readers.

import (
	"os"
	"strings"
	"testing"
)

func agentsUITestFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAgentsTabInIndexHTML(t *testing.T) {
	html := agentsUITestFile(t, "index.html")
	for _, want := range []string{
		`data-tab="agents"`,                                // sidebar nav entry (the drawer clones it)
		`aria-controls="tab-agents"`,                       // tablist wiring
		`<section id="tab-agents" role="tabpanel" hidden>`, // tab body
		`id="agentsTokenCard"`,                             // enrollment tokens card
		`id="agTokenRole"`,                                 // role selector (iran/foreign)
		`id="agTokenCreateBtn"`,                            // create-token button
		`id="agTokensBody"`,                                // token list rows
		`id="agAgentsList"`,                                // agent cards
		`id="agTunnelsList"`,                               // agent Matrix tunnel rows
		`id="agTunnelCreateBtn"`,                           // create-tunnel modal trigger
		`id="agTunnelPorts"`,                               // forwarded ports input
		`id="agTunnelHub"`, `id="agTunnelNode"`,            // hub/node selects
		`id="agTokenModal"`,  // one-time token modal
		`id="agTunnelModal"`, // create-tunnel modal
		`id="agAgentModal"`,  // agent detail drawer
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing agents UI element %s", want)
		}
	}
	// The token modal carries the one-time warning and the enroll command.
	for _, want := range []string{"agTokenValue", "agTokenCmd", "agents_token_once"} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html token modal missing %s", want)
		}
	}
}

func TestAgentsPersianStringsInGlossary(t *testing.T) {
	fa := agentsUITestFile(t, "i18n-fa.js")
	for _, want := range []string{
		`"Agents": "عوامل"`,
		`"Enrollment Tokens": "توکن‌های ثبت"`,
		`"Create Enrollment Token": "ساخت توکن ثبت"`,
		`"Create New Tunnel": "ساخت تونل جدید"`,
		`"Iran (hub)": "ایران (هاب)"`,
		`"Foreign (node)": "خارج (نود)"`,
		`"Revoke": "باطل کردن"`,
		`"Diagnostics Ping": "پینگ عیب‌یابی"`,
		// Strict traffic-ratio labeling: whole-server estimate + UNKNOWN.
		`"Server-wide estimate (whole server, not Matrix only)": "برآورد در سطح سرور (کل سرور، نه فقط ماتریکس)"`,
		`"UNKNOWN": "نامشخص"`,
		// Tunnel states and health buckets.
		`"Provisioning": "در حال آماده‌سازی"`,
		`"Registering": "در حال ثبت"`,
		`"Poor": "ضعیف"`,
	} {
		if !strings.Contains(fa, want) {
			t.Errorf("i18n-fa.js missing Persian string %s", want)
		}
	}
}

func TestAgentsI18nKeysInAppJS(t *testing.T) {
	app := agentsUITestFile(t, "app.js")
	for _, want := range []string{
		"nav_agents:", "agents_tokens_title:", "agents_tunnel_create:", "agents_tunnel_ports:",
		"agents_token_once:", "agents_diag_ping:", "agents_revoke:",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js I18N.en missing key %s", want)
		}
	}
	// The tab loader and the poll entry points must be reachable from goTab.
	if !strings.Contains(app, "if (name === 'agents') agentsLoad();") {
		t.Error("goTab must load the agents tab")
	}
	if !strings.Contains(app, "'dashboard','tunnel','agents','cluster'") {
		t.Error("goTab tab list must include the agents tab")
	}
}

func TestAgentsA11yLabels(t *testing.T) {
	html := agentsUITestFile(t, "index.html")
	// Icon-only or ambiguous new buttons carry aria-labels (wired to I18N keys).
	for _, want := range []string{
		`id="agTokenCopy" aria-label`,    // copy token
		`id="agTokenCmdCopy" aria-label`, // copy enroll command
		`id="agRefreshBtn" aria-label`,   // refresh agents
		`id="agAgentPing" aria-label`,    // diagnostics ping
		`id="agAgentRevoke" aria-label`,  // revoke agent
		`id="agTokenX" aria-label`,       // token modal close
		`id="agTunnelX" aria-label`,      // tunnel modal close
		`id="agAgentX" aria-label`,       // detail drawer close
	} {
		if !strings.Contains(html, want) {
			t.Errorf("agents UI element missing accessible name: %s", want)
		}
	}
	// Form fields are labelled for= their inputs.
	for _, pair := range [][2]string{
		{"agTokenRole", "agents_role"}, {"agTokenLabel", "agents_token_label"},
		{"agTokenDays", "agents_token_expiry"}, {"agTunnelName", "agents_tunnel_name"},
		{"agTunnelPorts", "agents_tunnel_ports"}, {"agTunnelMeshPort", "agents_tunnel_meshport"},
	} {
		if !strings.Contains(html, `for="`+pair[0]+`"`) {
			t.Errorf("agents UI field %s has no <label for>", pair[0])
		}
		if !strings.Contains(html, `id="`+pair[0]+`"`) {
			t.Errorf("agents UI field %s missing from the markup", pair[0])
		}
	}
}
