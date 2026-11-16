package panel

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
	want := []string{"", "Topology name", "Iran server", "Foreign server", "Tunnels", "Status", "Actions"}
	if len(headers) != len(want) {
		t.Fatalf("got %d topology columns, want %d", len(headers), len(want))
	}
	for i, header := range headers {
		// the label must be present and in order; the th may also carry a glyph
		// wrapper (svg + inner data-i18n span) around it
		if !strings.Contains(header[1], `scope="col"`) || !strings.Contains(header[2], want[i]) {
			t.Errorf("column %d must be a scoped %q header", i, want[i])
		}
	}
	for _, id := range []string{"topologyListTable", "topologySearch"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("topology overview must keep %s on the list card", id)
		}
	}
	// The list is the only topology view: the list/grid toggle that could turn
	// rows into half-empty cards is gone for good.
	for _, gone := range []string{"topologyViewListBtn", "topologyViewGridBtn"} {
		if strings.Contains(html, gone) || strings.Contains(topologyListTestSource(t, "topology.js"), gone) {
			t.Errorf("topology list must not keep the %s view toggle", gone)
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
	// cells no matter how the flexible name/server columns squeeze. The
	// mockup redesign (2026-11-16) restored # and the two server columns;
	// the fixed trio settled at 90px (Tunnels), 150px (Status), 72px (Actions).
	if !regexp.MustCompile(`grid-template-columns\s*:[^}]*90px\s+150px\s+72px`).MatchString(shared[1]) {
		t.Error("shared grid must keep fixed 90px (Tunnels), 150px (Status) and 72px (Actions) tracks")
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
	for _, id := range []string{"topoWizModal", "topoWizProgress", "topoWizSteps", "topoWizBody", "topoWizErr", "topoWizBack", "topoWizNext", "topoWizX", "topoWizFoot"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("index.html must keep the wizard element %s", id)
		}
	}
	// The footer Cancel was redundant next to Back/Next: the header × is the
	// only cancel affordance in both wizards.
	for _, gone := range []string{"topoWizCancelBtn", "topoCreateCancelBtn"} {
		if strings.Contains(html, `id="`+gone+`"`) {
			t.Errorf("wizard footer must not keep the redundant %s button", gone)
		}
		if strings.Contains(topologyListTestSource(t, "topology.js"), gone) {
			t.Errorf("topology.js must not reference the removed %s", gone)
		}
	}
	// Back renders before Next in both footers, so the flex space-between rule
	// pulls them to opposite, direction-mirrored ends of the bar.
	for _, foot := range []struct{ id, back, next string }{
		{"topoWizFoot", "topoWizBack", "topoWizNext"},
		{"topoCreateFoot", "topoCreateBack", "topoCreateNext"},
	} {
		re := regexp.MustCompile(`(?s)<div class="ent-modal-f" id="` + foot.id + `">(.*?)</div>`)
		m := re.FindStringSubmatch(html)
		if m == nil {
			t.Errorf("index.html must keep the %s footer wrapper", foot.id)
			continue
		}
		back, next := strings.Index(m[1], `id="`+foot.back+`"`), strings.Index(m[1], `id="`+foot.next+`"`)
		if back < 0 || next < 0 || back > next {
			t.Errorf("%s must render %s first and %s last so space-between separates them", foot.id, foot.back, foot.next)
		}
		if strings.Contains(m[1], "Cancel") {
			t.Errorf("%s must hold only Back and Next, got a Cancel button too", foot.id)
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
		`$('topoWizFoot').classList.toggle('next-only'`, // Back hidden: Next keeps the reading end
		`$('topoCreateFoot').classList.toggle('next-only'`,
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
	// The Matrix wizard is role-aware: the Iran/hub side ends on a bundle
	// hand-off screen (the hmx1_ bundle to paste on the foreign server), while
	// the foreign/node side pairs through /api/matrix/foreign-setup.
	for _, want := range []string{
		`function topoWizForeignSide()`,  // role detection from window._lastLocal
		`window._lastLocal`,              // the dashboard snapshot carries the role
		`/api/matrix/foreign-setup`,      // node side pairs with the hub bundle
		`/^hmx1_/`,                       // inline bundle validation before any API call
		`function topoWizRenderBundle()`, // hub-side bundle hand-off screen
		`id="topoWizBundleOut"`,          // readonly bundle output textarea
		`id="topoWizFBundle"`,            // node-side bundle paste textarea
		`topoWizCopyBundle`,              // copy button wired to the clipboard helper
		`copyTextSafe`,                   // the shared clipboard helper is reused
		`topo-wiz-bundle`,                // shared bundle textarea styling class
	} {
		if !strings.Contains(js, want) {
			t.Errorf("topology.js lacks Matrix bundle wiring: %s", want)
		}
	}
	css := topologyListTestSource(t, "enterprise.css")
	if !regexp.MustCompile(`\.topo-wiz-step\.done\s*\{[^}]*`).MatchString(css) ||
		!strings.Contains(css, `content: "\2713"`) {
		t.Error("enterprise.css must style completed wizard steps with a checkmark")
	}
	if !regexp.MustCompile(`\.topo-wiz-bundle\s*\{[^}]*min-height`).MatchString(css) ||
		!strings.Contains(css, `.topo-wiz-bundle[readonly]`) ||
		!strings.Contains(css, `.topo-wiz-bundle-actions`) {
		t.Error("enterprise.css must style the wizard Matrix bundle textareas and copy action")
	}
}

// The native country <select> opened as a plain OS popup outside the dialog.
// Every country selector now renders through the custom dropdown component:
// a flag+name trigger with an absolutely-positioned listbox inside the modal.
func TestTopologyCountryDropdown(t *testing.T) {
	html := topologyListTestSource(t, "index.html")
	js := topologyListTestSource(t, "topology.js")
	css := topologyListTestSource(t, "enterprise.css")

	// No native select may remain for the country, in the edit form markup or
	// in the create wizard's generated step markup.
	if strings.Contains(html, `<select id="topologyCountry"`) {
		t.Error("the topology form country field must not be a native select")
	}
	if strings.Contains(js, `<select class="quantum-input" id="topoCreateCountry"`) {
		t.Error("the create wizard country step must not generate a native select")
	}
	// The settings form keeps its value wiring: the hidden input carries the
	// code that the save PATCH reads, the dropdown host renders beside it.
	for _, want := range []string{`id="topologyCountry"`, `id="topologyCountryDD"`, `id="topologyCountryLbl"`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html must keep the country field element %s", want)
		}
	}
	for _, want := range []string{
		`function topoCountryMount(`,             // the shared component entry point
		`topoCountryMount('topoCreateCountryDD'`, // add-topology wizard country step
		`topoCountryMount('topologyCountryDD'`,   // edit-topology form
		`role="listbox"`,                         // listbox semantics
		`role="option"`,                          // option semantics
		`aria-haspopup="listbox"`,                // trigger announces the popup
		`aria-expanded=`,                         // open state on the trigger
		`aria-activedescendant`,                  // keyboard highlight without moving focus
		`aria-selected=`,                         // the picked option is marked
		`data-dd-value=`,                         // option rows carry the ISO code
		`topologyFlagSvg(cc)`,                    // trigger uses the embedded SVG flags
		`topologyFlagSvg(c[0])`,                  // so does every option row
		`'ArrowDown'`, `'ArrowUp'`,               // arrows navigate
		`'Escape'`,                      // Escape closes
		`topoCountrySetOpen(id, false)`, // click-outside / Escape close path
		`e.stopImmediatePropagation`,    // Escape must not also close the wizard
	} {
		if !strings.Contains(js, want) {
			t.Errorf("topology.js lacks country dropdown wiring/markup: %s", want)
		}
	}
	for _, rule := range []struct{ name, pattern string }{
		{"host is the positioning context", `\.topo-dd\s*\{[^}]*position\s*:\s*relative`},
		{"listbox is absolutely positioned inside the modal", `\.topo-dd-list\s*\{[^}]*position\s*:\s*absolute`},
		{"listbox scrolls internally with a max height", `\.topo-dd-list\s*\{[^}]*max-height\s*:[^}]*overflow(?:-y)?\s*:\s*auto`},
		{"listbox spans the trigger per direction", `\.topo-dd-list\s*\{[^}]*inset-inline\s*:\s*0`},
		{"listbox keeps the modal radius", `\.topo-dd-list\s*\{[^}]*border-radius\s*:\s*var\(--r-lg\)`},
		{"selected option carries the checkmark", `\.topo-dd-opt\.on::after\s*\{[^}]*content\s*:\s*"\\2713"`},
		{"checkmark aligns to the reading end", `\.topo-dd-opt\.on::after\s*\{[^}]*margin-inline-start\s*:\s*auto`},
		{"wizard footer spreads Back and Next", `#topoWizModal \.ent-modal-f[^}]*justify-content\s*:\s*space-between`},
		{"next-only footer keeps the reading end", `\.ent-modal-f\.next-only[^}]*justify-content\s*:\s*flex-end`},
	} {
		if !regexp.MustCompile(rule.pattern).MatchString(css) {
			t.Errorf("enterprise.css lacks country dropdown/footer rule: %s", rule.name)
		}
	}
}
