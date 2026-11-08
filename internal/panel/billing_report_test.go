package panel

// billing_report_test.go — TASK 4: the CSV export is safe against
// spreadsheet formula injection and the CLI month parsing produces the
// exact Tehran window.

import (
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func tehranTimeUTC(y int, mo time.Month, d, h, mi int) int64 {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC).Unix()
}

func TestBillingCSVFormulaInjection(t *testing.T) {
	r := &BillingReport{
		FromJalali: "1405-08-01", ToJalali: "1405-08-30",
		PerTunnel: []store.BillingTunnelRow{
			{TunnelKey: "=HYPERLINK(\"http://evil\",\"x\")", Topology: "+SUM(A1:A9)",
				LocalServer: "-2+3", Engine: "@cmd|rm"},
		},
	}
	csv := r.RenderBillingCSV()
	for _, bad := range []string{"\n=HYPERLINK", ",+SUM", ",-2+3", ",@cmd"} {
		if strings.Contains(csv, bad) {
			t.Fatalf("formula trigger survived in CSV: %q in\n%s", bad, csv)
		}
	}
	if !strings.Contains(csv, "'=HYPERLINK") || !strings.Contains(csv, ",'+SUM") {
		t.Fatalf("dangerous cells must be apostrophe-prefixed:\n%s", csv)
	}
}

func TestParseMonthSpec(t *testing.T) {
	from, to, err := ParseMonthSpec("1405-08")
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := tehranTimeUTC(2026, 10, 22, 20, 30)
	wantTo := tehranTimeUTC(2026, 11, 21, 20, 30)
	if from != wantFrom || to != wantTo {
		t.Fatalf("1405-08 = %d..%d, want %d..%d", from, to, wantFrom, wantTo)
	}
	if _, _, err := ParseMonthSpec("1405-13"); err == nil {
		t.Fatal("month 13 must fail")
	}
	if _, _, err := ParseMonthSpec("nonsense"); err == nil {
		t.Fatal("garbage must fail")
	}
}
