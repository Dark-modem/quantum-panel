package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func topologyListTestSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTopologyListSemanticTable(t *testing.T) {
	html := topologyListTestSource(t, "index.html")
	table := regexp.MustCompile(`(?s)<table\b[^>]*class="topo-list-table"[^>]*>(.*?)</table>`).FindStringSubmatch(html)
	if len(table) != 2 {
		t.Fatal("topology overview must remain a semantic table")
	}
	if !regexp.MustCompile(`<tbody\b[^>]*id="topologiesList"`).MatchString(table[1]) {
		t.Error("generated topology rows must remain inside the table body")
	}
	headers := regexp.MustCompile(`(?s)<th\b([^>]*)>(.*?)</th>`).FindAllStringSubmatch(table[1], -1)
	want := []string{"Name", "Iran", "Foreign", "Tunnels", "Status", "Actions"}
	if len(headers) != len(want) {
		t.Fatalf("got %d topology columns, want %d", len(headers), len(want))
	}
	for i, header := range headers {
		if !strings.Contains(header[1], `scope="col"`) || strings.TrimSpace(header[2]) != want[i] {
			t.Errorf("column %d must be a scoped %q header", i, want[i])
		}
	}
	for _, id := range []string{"topologyListTable", "topologySearch", "topologyViewListBtn", "topologyViewGridBtn"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("topology overview must keep %s on the list card", id)
		}
	}
	if !strings.Contains(html, `id="topologyCountry"`) {
		t.Error("topology form must offer a foreign country selector")
	}
	if strings.Contains(html, `data-i18n="topology_list_status"`) || strings.Contains(html, "topology_list_servers") {
		t.Error("status and server-pair columns must be gone from the header row")
	}
	// The Topology sidebar entry draws a network-topology icon (nodes joined by
	// links) in the same stroked style as the other nav icons; the lightning
	// bolt stays reserved for the other panel sections that still use it.
	nav := regexp.MustCompile(`(?s)<button\b[^>]*data-tab="tunnel"[^>]*>(.*?)</button>`).FindStringSubmatch(html)
	if len(nav) != 2 {
		t.Fatal("topology sidebar nav item must exist")
	}
	for _, part := range []string{
		`fill="none" stroke="currentColor"`,
		`<circle cx="5" cy="6" r="2.5"/>`,
		`<circle cx="19" cy="6" r="2.5"/>`,
		`<circle cx="12" cy="19" r="2.5"/>`,
		`M7.5 6h9M6.3 8l4.7 8.8M17.7 8l-4.7 8.8`,
	} {
		if !strings.Contains(nav[1], part) {
			t.Errorf("topology nav icon must draw nodes joined by links, missing %q", part)
		}
	}
	if strings.Contains(nav[1], "M13 2L3 14h7l-1 8 10-12h-7l1-8z") {
		t.Error("topology nav item must not use the lightning bolt icon")
	}
	js := topologyListTestSource(t, "topology.js")
	for _, want := range []string{
		`country: $('topologyCountry').value`,  // PATCH persists the country
		`topologyFlagSvg(flag)`,                // embedded SVG flag per server column
		`tabindex="0"`,                         // rows are keyboard reachable
		`$('topologiesList').addEventListener`, // whole-row click opens the topology
		`toastWarn(tr('', 'Delete topology'), topologyDeleteNotice(remaining))`, // delete explains blocked state
		`quantum-btn-danger topo-delete`,                                        // delete is a danger button, never disabled
		`let topologyLoadedOnce = false`,                                        // first-load gate: skeleton instead of empty-state flash
		`if (!topologyLoadedOnce) topologyRender()`,                             // first load paints the skeleton immediately
		`topologyLoading && !topologyLoadedOnce`,                                // skeleton branch replaces the "No topology yet" row
		`${skel(2)}`,                                                            // skeleton rows reuse the shared skel() helper
	} {
		if !strings.Contains(js, want) {
			t.Errorf("topology.js lacks %q", want)
		}
	}
	if strings.Contains(js, "topologyListStatus") {
		t.Error("topologyListStatus must be removed with the status column")
	}
}

func TestTopologyListModernChrome(t *testing.T) {
	css := topologyListTestSource(t, "enterprise.css")
	for _, rule := range []struct{ name, pattern string }{
		{"uppercase column headers", `\.topo-list-head\s+th\s*\{[^}]*text-transform\s*:\s*uppercase`},
		{"plain blue tunnel count", `\.topo-count\s*\{[^}]*color\s*:\s*var\(--ac\)`},
		{"no circular count badge", `^//`},
		{"clickable rows", `\.topo-list-row\s*\{[^}]*cursor\s*:\s*pointer`},
		{"keyboard focus ring on rows", `\.topo-list-row:focus-visible\s*\{[^}]*outline\s*:\s*2px\s+solid\s+var\(--ent-focus\)`},
		{"flag plus IP server cell", `\.topo-server\s*\{[^}]*display\s*:\s*flex`},
		{"embedded SVG flag footprint", `\.topo-flag-svg\s*\{[^}]*width\s*:\s*20px[^}]*height\s*:\s*14px`},
		{"pill action buttons", `\.topo-actions\s+\.quantum-btn\s*\{[^}]*border-radius\s*:\s*999px`},
		{"red danger delete button", `\.topo-actions\s+\.topo-delete\s*\{[^}]*border\s*:\s*1px\s+solid\s+var\(--er\)`},
		{"search box styling", `\.topo-search\s*\{[^}]*min-height\s*:`},
		{"view toggle active state", `\.topo-view-toggle\s+button\[aria-pressed="true"\]`},
		{"grid card mode", `\.topo-list-table\.topo-grid\s+\.topo-list-row\s*\{[^}]*border-radius\s*:\s*var\(--r-md\)`},
		{"subtle row hover", `\.topo-list-row:hover\s*\{[^}]*background\s*:\s*var\(--sunk\)\s*;?\s*\}`},
		{"first-load skeleton bars", `\.topo-list-skel\s+\.ent-skel\s*\{[^}]*width\s*:\s*100%`},
	} {
		if rule.name == "no circular count badge" {
			// The count is plain bold blue text: no border, circle or background.
			if regexp.MustCompile(`\.topo-count\s*\{[^}]*border-radius\s*:\s*50%`).MatchString(css) {
				t.Error("topology count must not render as a circular badge")
			}
			continue
		}
		if !regexp.MustCompile(rule.pattern).MatchString(css) {
			t.Errorf("topology list CSS lacks %s", rule.name)
		}
	}
}

func TestTopologyListSharedResponsiveColumns(t *testing.T) {
	css := topologyListTestSource(t, "enterprise.css")
	shared := regexp.MustCompile(`(?s)\.topo-list-head\s*,\s*\.topo-list-row\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if len(shared) != 2 {
		t.Fatal("topology header and rows must share their grid definition")
	}
	for _, property := range []string{`display\s*:\s*grid`, `grid-template-columns\s*:`, `gap\s*:`, `padding\s*:`} {
		if !regexp.MustCompile(property).MatchString(shared[1]) {
			t.Errorf("shared header/row rule lacks %s", property)
		}
	}
	// Tunnels and Actions sit on fixed tracks so the headers stay over their
	// cells no matter how the flexible name/endpoints columns squeeze.
	if !regexp.MustCompile(`grid-template-columns\s*:[^}]*64px\s+96px\s+56px`).MatchString(shared[1]) {
		t.Error("shared grid must keep fixed 64px (Tunnels), 96px (Status) and 56px (Actions) tracks")
	}

	media := regexp.MustCompile(`@media\s*\(\s*max-width\s*:\s*799px\s*\)`).FindStringIndex(css)
	if media == nil {
		t.Fatal("topology rows need a narrow-screen layout below 800px")
	}
	mobile := css[media[1]:]
	if next := strings.Index(mobile, "@media"); next >= 0 {
		mobile = mobile[:next]
	}
	for _, rule := range []string{
		`(?s)\.topo-list-table\s+thead\s*\{[^}]*display\s*:\s*none`,
		`(?s)\.topo-list-row\s*\{[^}]*grid-template-columns\s*:\s*minmax\(\s*0\s*,\s*1fr\s*\)`,
		`(?s)\.topo-list-row\s+td::before\s*\{[^}]*content\s*:\s*attr\(data-label\)`,
		`(?s)\.topo-actions\s*\{[^}]*flex-wrap\s*:\s*wrap`,
	} {
		if !regexp.MustCompile(rule).MatchString(mobile) {
			t.Errorf("narrow-screen topology layout lacks %s", rule)
		}
	}
	for _, selector := range []string{".topo-name bdi", ".topo-server bdi"} {
		rule := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(selector) + `\s*\{[^}]*overflow-wrap\s*:\s*anywhere`)
		if !rule.MatchString(css) {
			t.Errorf("long names and IPv6 endpoints must wrap: %s", selector)
		}
	}
}

// The new-tunnel wizard opens in a modal on the topology view: its markup,
// step indicators and event wiring must stay in place.
func TestTopologyWizardMarkup(t *testing.T) {
	html := topologyListTestSource(t, "index.html")
	for _, id := range []string{"topoWizModal", "topoWizProgress", "topoWizSteps", "topoWizBody", "topoWizErr", "topoWizCancelBtn", "topoWizBack", "topoWizNext", "topoWizX"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("index.html must keep the wizard element %s", id)
		}
	}
	if strings.Contains(html, `id="tunnelsAddBtn" onclick`) {
		t.Error("tunnelsAddBtn must not jump to Setup inline; topology.js opens the wizard")
	}
	js := topologyListTestSource(t, "topology.js")
	for _, want := range []string{
		`$('tunnelsAddBtn').onclick = topologyNewTunnel`,
		`$('topologyAddTunnelBtn').onclick = topologyNewTunnel`,
		`$('topoWizNext').onclick = topoWizNext`,
		`$('topoWizBack').onclick = topoWizBack`,
		`$('topoWizCancelBtn').onclick = topoWizCancel`,
		`data-wiz-type=`,
		`data-wiz-step=`,
		`Step {x} of {n}`,
		`' done' : ''`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("topology.js lacks wizard wiring/markup: %s", want)
		}
	}
	if strings.Contains(js, "goTab('setup')") {
		t.Error("topologyNewTunnel must not navigate to the Setup tab anymore")
	}
	css := topologyListTestSource(t, "enterprise.css")
	if !regexp.MustCompile(`\.topo-wiz-step\.done\s*\{[^}]*`).MatchString(css) ||
		!strings.Contains(css, `content: "\2713"`) {
		t.Error("enterprise.css must style completed wizard steps with a checkmark")
	}
}
