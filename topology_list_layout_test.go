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
	want := []string{"Name", "Server pair", "Tunnels", "Status", "Actions"}
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
}

func TestTopologyListModernChrome(t *testing.T) {
	css := topologyListTestSource(t, "enterprise.css")
	for _, rule := range []struct{ name, pattern string }{
		{"uppercase column headers", `\.topo-list-head\s+th\s*\{[^}]*text-transform\s*:\s*uppercase`},
		{"circular tunnel count badge", `\.topo-count\s*\{[^}]*border-radius\s*:\s*50%`},
		{"status dot next to the name", `\.topo-dot\s*\{[^}]*border-radius\s*:\s*50%`},
		{"healthy dot color", `\.topo-dot\[data-topo-status="healthy"\]\s*\{[^}]*background\s*:\s*var\(--ok\)`},
		{"degraded dot color", `\.topo-dot\[data-topo-status="degraded"\]\s*\{[^}]*background\s*:\s*var\(--wr\)`},
		{"down dot color", `\.topo-dot\[data-topo-status="down"\]\s*\{[^}]*background\s*:\s*var\(--er\)`},
		{"accent location label", `\.topo-location\s*\{[^}]*color\s*:\s*var\(--ac\)`},
		{"pill action buttons", `\.topo-actions\s+\.hashem-btn\s*\{[^}]*border-radius\s*:\s*999px`},
		{"search box styling", `\.topo-search\s*\{[^}]*min-height\s*:`},
		{"view toggle active state", `\.topo-view-toggle\s+button\[aria-pressed="true"\]`},
		{"grid card mode", `\.topo-list-table\.topo-grid\s+\.topo-list-row\s*\{[^}]*border-radius\s*:\s*var\(--r-md\)`},
		{"subtle row hover", `\.topo-list-row:hover\s*\{[^}]*background\s*:\s*var\(--sunk\)\s*;?\s*\}`},
	} {
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
	for _, selector := range []string{".topo-name bdi", ".topo-ips bdi"} {
		rule := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(selector) + `\s*\{[^}]*overflow-wrap\s*:\s*anywhere`)
		if !rule.MatchString(css) {
			t.Errorf("long names and IPv6 endpoints must wrap: %s", selector)
		}
	}
}
