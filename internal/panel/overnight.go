package panel

// overnight.go — GET /api/overnight/checker (BACKEND-44, contract §2.24):
// the overnight program's completeness data for the UI's checker view.
// The source of truth is coordination data on disk — the overnight
// checker's own output (checker.json) plus the overnight hub's status.json
// — read at request time and cached briefly; these files are written by
// tools/overnight-check.py and the overnight sessions, never by the panel.
// Coordination files are optional by design: when the directory or either
// file is missing (or unparseable) the endpoint answers the honest empty
// shape — 200, generated:"", empty projects, three_way_match:false — which
// the UI reads as "no checker data yet", never a guessed number.
//
// Row shape (§2.24): {name, methods_found, methods_added, missing, status}.
// checker.json rows carry methods_found/methods_added/missing; status.json
// rows carry status (and methods_found for rows the checker has not
// inventoried). missing is a list for inventoried rows (never null there)
// and null for rows known only from status.json — honest unknown, NOT an
// empty "no gaps" claim. ctunnel_check passes the checker's three-way
// block through verbatim (RawMessage — the panel never reshapes it).

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// overnightReadLimit caps one coordination file: these are small
// machine-written JSON files; a runaway file must not balloon the panel.
const overnightReadLimit = 4 << 20

// overnightCacheTTL is the "cached briefly" window. The overnight sessions
// rewrite the files every few minutes; a 10s cache still cuts the read
// storm from a polling UI to at most one stat+read per interval per file.
const overnightCacheTTL = 10 * time.Second

// overnightCoordDir resolves the coordination directory: the explicit env
// override first (the laptop lab points it at 00-ADMIN/overnight), else
// coordination data lives next to the panel's own config (deployment:
// /etc/gre-panel/overnight). Empty/missing is legal everywhere — the
// endpoint degrades to the empty shape.
func overnightCoordDir() string {
	if v := strings.TrimSpace(os.Getenv("GRE_PANEL_OVERNIGHT_DIR")); v != "" {
		return v
	}
	return filepath.Join(configDir, "overnight")
}

// overnightCheckerRow mirrors the checker.json project row for the fields
// the endpoint serves (the checker also carries covered/variants/license —
// those stay in the file, not in this contract shape).
type overnightCheckerRow struct {
	Project      string   `json:"project"`
	MethodsFound *int     `json:"methods_found"`
	MethodsAdded *int     `json:"methods_added"`
	Missing      []string `json:"missing"`
}

// overnightCtunnelCheck mirrors checker.json's three-way block. The three
// catalog descriptors pass through verbatim — the panel is not the schema
// owner of their internals.
type overnightCtunnelCheck struct {
	UpstreamCatalog json.RawMessage `json:"upstream_catalog"`
	CoresManifest   json.RawMessage `json:"cores_manifest"`
	AgentDriver     json.RawMessage `json:"agent_driver"`
	ThreeWayMatch   bool            `json:"three_way_match"`
	Mismatches      []string        `json:"mismatches"`
}

type overnightCheckerFile struct {
	Generated    string                `json:"generated"`
	Projects     []overnightCheckerRow `json:"projects"`
	CtunnelCheck overnightCtunnelCheck `json:"ctunnel_check"`
}

// overnightStatusRow mirrors the hub status.json rows for the join.
type overnightStatusRow struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	MethodsFound *int   `json:"methods_found"`
}

type overnightStatusFile struct {
	Generated string               `json:"generated"`
	Projects  []overnightStatusRow `json:"projects"`
}

// overnightCheckerRow is the §2.24 served row.
type overnightServedRow struct {
	Name         string   `json:"name"`
	MethodsFound *int     `json:"methods_found"`
	MethodsAdded *int     `json:"methods_added"`
	Missing      []string `json:"missing"`
	Status       string   `json:"status"`
}

// overnightCheckerPayload is the §2.24 served shape.
type overnightCheckerPayload struct {
	Generated    string                `json:"generated"`
	Projects     []overnightServedRow  `json:"projects"`
	CtunnelCheck overnightCtunnelCheck `json:"ctunnel_check"`
}

// emptyOvernightChecker is the honest empty shape: the UI's "no checker
// data yet" marker is Generated == "" (files absent/unreadable).
func emptyOvernightChecker() overnightCheckerPayload {
	return overnightCheckerPayload{
		Projects: []overnightServedRow{},
		CtunnelCheck: overnightCtunnelCheck{
			Mismatches: []string{},
		},
	}
}

// overnightCheckerCache is the brief request-time cache: keyed by resolved
// dir + the two files' mtimes so a rewrite is visible immediately once the
// TTL lapses, and a dir switch never serves the old tree.
type overnightCheckerCache struct {
	mu      sync.Mutex
	payload overnightCheckerPayload
	have    bool
	dir     string
	modSum  int64
	at      time.Time
}

var overnightCache overnightCheckerCache

// overnightFileStat sums the two files' mtimes; a missing file contributes
// its name-tagged zero so (dir, sum) distinguishes "both missing" from
// "one present".
func overnightFileStat(dir string) (int64, bool) {
	var sum int64
	any := false
	for i, name := range []string{"checker.json", "status.json"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		any = true
		sum += fi.ModTime().UnixNano() + int64(i)<<62
	}
	return sum, any
}

// readLimited reads a coordination file with the size cap.
func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, overnightReadLimit))
}

// loadOvernightChecker rebuilds the payload from disk. Any read/parse
// failure degrades to the empty shape — coordination data must never 500
// the dashboard (honest-empty is the contract, §2.24).
func loadOvernightChecker(dir string) overnightCheckerPayload {
	empty := emptyOvernightChecker()
	raw, err := readLimited(filepath.Join(dir, "checker.json"))
	if err != nil {
		return empty
	}
	var ck overnightCheckerFile
	if err := json.Unmarshal(raw, &ck); err != nil {
		return empty
	}
	if ck.CtunnelCheck.Mismatches == nil {
		ck.CtunnelCheck.Mismatches = []string{}
	}
	// status.json is optional relative to checker.json: without it the
	// checker rows still serve, with empty status (honest: hub state unknown).
	var st overnightStatusFile
	if raw, err := readLimited(filepath.Join(dir, "status.json")); err == nil {
		_ = json.Unmarshal(raw, &st) // unparseable status degrades to empty join rows
	}
	statusBy := make(map[string]overnightStatusRow, len(st.Projects))
	for _, r := range st.Projects {
		statusBy[r.Name] = r
	}
	seen := make(map[string]bool, len(ck.Projects))
	rows := make([]overnightServedRow, 0, len(ck.Projects)+len(st.Projects))
	for _, c := range ck.Projects {
		missing := c.Missing
		if missing == nil {
			missing = []string{}
		}
		row := overnightServedRow{
			Name:         c.Project,
			MethodsFound: c.MethodsFound,
			MethodsAdded: c.MethodsAdded,
			Missing:      missing,
		}
		if s, ok := statusBy[c.Project]; ok {
			row.Status = s.Status
			if row.MethodsFound == nil {
				row.MethodsFound = s.MethodsFound
			}
			seen[c.Project] = true
		}
		rows = append(rows, row)
	}
	// Rows the checker has not inventoried (e.g. the audit-existing engines
	// before their checker file lands) still appear — status joined,
	// missing left null (honest unknown, NOT "no gaps").
	names := make([]string, 0, len(st.Projects))
	for name := range statusBy {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		s := statusBy[name]
		rows = append(rows, overnightServedRow{
			Name:         name,
			MethodsFound: s.MethodsFound,
			Status:       s.Status,
		})
	}
	gen := ck.Generated
	if gen == "" {
		gen = st.Generated
	}
	return overnightCheckerPayload{
		Generated:    gen,
		Projects:     rows,
		CtunnelCheck: ck.CtunnelCheck,
	}
}

// overnightCheckerGet builds the payload under the cache discipline.
func overnightCheckerGet() overnightCheckerPayload {
	overnightCache.mu.Lock()
	defer overnightCache.mu.Unlock()
	dir := overnightCoordDir()
	sum, any := overnightFileStat(dir)
	now := time.Now()
	c := &overnightCache
	if c.have && c.dir == dir && c.modSum == sum && now.Sub(c.at) < overnightCacheTTL {
		return c.payload
	}
	if !any {
		c.payload = emptyOvernightChecker()
	} else {
		c.payload = loadOvernightChecker(dir)
	}
	c.have = true
	c.dir = dir
	c.modSum = sum
	c.at = now
	return c.payload
}

// handleOvernightChecker serves GET /api/overnight/checker — read-only
// coordination data behind requireAuth (§2.24).
func handleOvernightChecker(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, overnightCheckerGet())
}
