package panel

// billing_report.go — the billing export (phase 12 TASK 4): the
// commercial report over a Jalali month (or any range) in Asia/Tehran
// boundaries. Per Iran datacenter / per server / per tunnel: billable
// download (wire bytes RECEIVED on Iran), upload, payload, overhead
// ratio, the configured price hint and the SUGGESTED Pasarguard
// coefficient — all REAL bytes, never multiplied — plus DATA-QUALITY
// flags so the operator knows when numbers are incomplete. The CSV is
// formula-injection-safe (spreadsheet cells starting with =,+,-,@ are
// prefixed with an apostrophe). Documented in ACCOUNTING-SEMANTICS.md.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// BillingReport is the full export for one window.
type BillingReport struct {
	From          int64                    `json:"from"`
	To            int64                    `json:"to"`
	FromJalali    string                   `json:"from_jalali"`
	ToJalali      string                   `json:"to_jalali"`
	Note          string                   `json:"note"`
	PerDatacenter []store.BillingDCRow     `json:"per_datacenter"`
	PerServer     []store.BillingServerRow `json:"per_server"`
	PerTunnel     []store.BillingTunnelRow `json:"per_tunnel"`
}

func jalaliOf(unix int64) string {
	t := time.Unix(unix, 0).In(tehranZone)
	jy, jm, jd, err := GregorianToJalali(t.Year(), int(t.Month()), t.Day())
	if err != nil {
		return t.Format("2006-01-02")
	}
	return fmt.Sprintf("%d-%02d-%02d", jy, jm, jd)
}

// BuildBillingReport computes the report over an explicit unix range.
func BuildBillingReport(s interface {
	BillingReport(from, to int64) ([]store.BillingDCRow, []store.BillingServerRow, []store.BillingTunnelRow, error)
}, from, to int64) (*BillingReport, error) {
	dcs, servers, tunnels, err := s.BillingReport(from, to)
	if err != nil {
		return nil, err
	}
	return &BillingReport{
		From: from, To: to,
		FromJalali: jalaliOf(from), ToJalali: jalaliOf(to),
		Note: "billable download = wire bytes RECEIVED on the Iran side; " +
			"ALL byte numbers are REAL bytes — no coefficient is ever applied to them. " +
			"quality.gaps>0 or agents_offline>0 means the numbers for that row may be INCOMPLETE.",
		PerDatacenter: dcs, PerServer: servers, PerTunnel: tunnels,
	}, nil
}

// ParseMonthSpec resolves "1405-08" (Jalali month, Tehran boundaries).
func ParseMonthSpec(spec string) (from, to int64, err error) {
	parts := strings.Split(strings.TrimSpace(spec), "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("month must look like 1405-08 (Jalali year-month)")
	}
	jy, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("bad Jalali year %q", parts[0])
	}
	jm, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("bad Jalali month %q", parts[1])
	}
	return TehranMonthRange(jy, jm)
}

// ParseDateSpec resolves "2026-10-23" as a Tehran DAY start (returns
// [day-start, next-day-start)).
func ParseDateSpec(spec string) (from, to int64, err error) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(spec), tehranZone)
	if err != nil {
		return 0, 0, fmt.Errorf("date must look like 2026-10-23 (interpreted in Asia/Tehran)")
	}
	return t.Unix(), t.Add(24 * time.Hour).Unix(), nil
}

// csvSafe prefixes spreadsheet-formula trigger characters.
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

func csvRow(fields ...string) string {
	for i, f := range fields {
		// csvSafe FIRST: the apostrophe guard must sit INSIDE any quote
		// wrapping, or a "=cmd()" cell would parse as a formula again.
		f = csvSafe(f)
		if strings.ContainsAny(f, ",\"\n") {
			f = "\"" + strings.ReplaceAll(f, "\"", "\"\"") + "\""
		}
		fields[i] = f
	}
	return strings.Join(fields, ",")
}

// RenderBillingCSV renders the report as formula-injection-safe CSV
// (one section per scope).
func (r *BillingReport) RenderBillingCSV() string {
	var b strings.Builder
	b.WriteString("# quantum billing report\n")
	b.WriteString("# window," + r.FromJalali + " -> " + r.ToJalali +
		" (Asia/Tehran, unix " + strconv.FormatInt(r.From, 10) + ".." + strconv.FormatInt(r.To, 10) + ")\n")
	b.WriteString("# ALL bytes are REAL bytes; no coefficient is ever applied. billable download = wire bytes received on the Iran side\n")
	b.WriteString("# gaps>0 or agents_offline>0 = the row's numbers may be INCOMPLETE (agent offline / reporting gap)\n\n")

	b.WriteString("# per datacenter\n")
	b.WriteString(csvRow("datacenter_id", "name", "billable_download_bytes", "upload_bytes",
		"payload_download_bytes", "payload_upload_bytes", "overhead_ratio",
		"price_coefficient_hint", "suggested_coefficient", "gaps", "longest_gap_s",
		"unknown_counters", "agents_offline") + "\n")
	for _, d := range r.PerDatacenter {
		b.WriteString(csvRow(
			strconv.FormatInt(d.DatacenterID, 10), d.Name,
			strconv.FormatUint(d.Billable.WireRx, 10), strconv.FormatUint(d.Billable.WireTx, 10),
			strconv.FormatUint(d.Billable.PayloadRx, 10), strconv.FormatUint(d.Billable.PayloadTx, 10),
			fmt.Sprintf("%.4f", d.Overhead), fmt.Sprintf("%.4f", d.PriceHint), fmt.Sprintf("%.4f", d.Suggested),
			strconv.Itoa(d.Quality.GapCount), strconv.FormatInt(d.Quality.LongestGapS, 10),
			strconv.FormatBool(d.Quality.UnknownCounters), strconv.Itoa(d.Quality.OfflineAgents)) + "\n")
	}
	b.WriteString("\n# per server\n")
	b.WriteString(csvRow("server_id", "name", "datacenter", "billable_download_bytes", "upload_bytes",
		"payload_download_bytes", "payload_upload_bytes", "overhead_ratio",
		"gaps", "longest_gap_s", "unknown_counters", "agents_offline") + "\n")
	for _, s := range r.PerServer {
		b.WriteString(csvRow(
			strconv.FormatInt(s.ServerID, 10), s.Name, s.Datacenter,
			strconv.FormatUint(s.Billable.WireRx, 10), strconv.FormatUint(s.Billable.WireTx, 10),
			strconv.FormatUint(s.Billable.PayloadRx, 10), strconv.FormatUint(s.Billable.PayloadTx, 10),
			fmt.Sprintf("%.4f", s.Overhead),
			strconv.Itoa(s.Quality.GapCount), strconv.FormatInt(s.Quality.LongestGapS, 10),
			strconv.FormatBool(s.Quality.UnknownCounters), strconv.Itoa(s.Quality.OfflineAgents)) + "\n")
	}
	b.WriteString("\n# per tunnel\n")
	b.WriteString(csvRow("tunnel_id", "tunnel_key", "topology", "local_server", "remote_server", "engine",
		"billable_download_bytes", "upload_bytes", "payload_download_bytes", "payload_upload_bytes",
		"overhead_ratio", "price_coefficient_hint", "suggested_coefficient",
		"gaps", "longest_gap_s", "unknown_counters", "agents_offline") + "\n")
	for _, t := range r.PerTunnel {
		b.WriteString(csvRow(
			strconv.FormatInt(t.TunnelID, 10), t.TunnelKey, t.Topology, t.LocalServer, t.RemoteServer, t.Engine,
			strconv.FormatUint(t.Billable.WireRx, 10), strconv.FormatUint(t.Billable.WireTx, 10),
			strconv.FormatUint(t.Billable.PayloadRx, 10), strconv.FormatUint(t.Billable.PayloadTx, 10),
			fmt.Sprintf("%.4f", t.Overhead), fmt.Sprintf("%.4f", t.PriceHint), fmt.Sprintf("%.4f", t.Suggested),
			strconv.Itoa(t.Quality.GapCount), strconv.FormatInt(t.Quality.LongestGapS, 10),
			strconv.FormatBool(t.Quality.UnknownCounters), strconv.Itoa(t.Quality.OfflineAgents)) + "\n")
	}
	return b.String()
}

// cliReport implements `quantum-panel report --month 1405-08
// --format csv|json [--out FILE]` (or --from/--to Tehran dates).
func cliReport(args []string) int {
	if v := os.Getenv("GRE_PANEL_DIR"); v != "" {
		configDir = v
	}
	month, format, out := "", "json", ""
	var fromSpec, toSpec string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--month":
			i++
			if i < len(args) {
				month = args[i]
			}
		case "--format":
			i++
			if i < len(args) {
				format = args[i]
			}
		case "--out", "-o":
			i++
			if i < len(args) {
				out = args[i]
			}
		case "--from":
			i++
			if i < len(args) {
				fromSpec = args[i]
			}
		case "--to":
			i++
			if i < len(args) {
				toSpec = args[i]
			}
		}
	}
	var from, to int64
	var err error
	switch {
	case month != "":
		from, to, err = ParseMonthSpec(month)
	case fromSpec != "" && toSpec != "":
		from, _, err = ParseDateSpec(fromSpec)
		if err == nil {
			_, to, err = ParseDateSpec(toSpec)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: quantum-panel report --month 1405-08 [--format csv|json] [--out FILE]")
		fmt.Fprintln(os.Stderr, "   or: quantum-panel report --from 2026-10-23 --to 2026-11-21 [--format csv|json] [--out FILE]")
		fmt.Fprintln(os.Stderr, "       (dates and months are interpreted in Asia/Tehran)")
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 2
	}
	if format != "csv" && format != "json" {
		fmt.Fprintln(os.Stderr, "report: --format must be csv or json")
		return 2
	}
	s, serr := MeshStore()
	if serr != nil {
		fmt.Fprintln(os.Stderr, "report: store:", serr)
		return 1
	}
	rep, rerr := BuildBillingReport(s, from, to)
	if rerr != nil {
		fmt.Fprintln(os.Stderr, "report:", rerr)
		return 1
	}
	var payload string
	if format == "csv" {
		payload = rep.RenderBillingCSV()
	} else {
		b, jerr := json.MarshalIndent(rep, "", "  ")
		if jerr != nil {
			fmt.Fprintln(os.Stderr, "report:", jerr)
			return 1
		}
		payload = string(b)
	}
	if out != "" {
		if werr := os.WriteFile(out, []byte(payload), 0o600); werr != nil {
			fmt.Fprintln(os.Stderr, "report: write:", werr)
			return 1
		}
		fmt.Printf("report written: %s (%d bytes)\n", out, len(payload))
	} else {
		fmt.Print(payload)
	}
	return 0
}

// handleUIBillingReport serves GET /api/ui/reports/billing?month=1405-08
// (or from/to dates) as JSON, or CSV with format=csv.
func handleUIBillingReport(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var from, to int64
	var err error
	if m := q.Get("month"); m != "" {
		from, to, err = ParseMonthSpec(m)
	} else if f, t := q.Get("from"), q.Get("to"); f != "" && t != "" {
		from, _, err = ParseDateSpec(f)
		if err == nil {
			_, to, err = ParseDateSpec(t)
		}
	} else {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "month"}, "month=1405-08 or from=2026-10-23&to=2026-11-21 required (Asia/Tehran)")
		return
	}
	if err != nil {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "month"}, err.Error())
		return
	}
	rep, rerr := BuildBillingReport(h.Store, from, to)
	if rerr != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "billing report"}, rerr.Error())
		return
	}
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=billing-"+strings.ReplaceAll(rep.FromJalali, "-", "")+".csv")
		_, _ = w.Write([]byte(rep.RenderBillingCSV()))
		return
	}
	writeJSON(w, rep)
}
