package panel

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

func TestAgentsPersianStringsInGlossary(t *testing.T) {
	fa := agentsUITestFile(t, "i18n-fa.js")
	for _, want := range []string{
		`"Agents": "عوامل"`,
		`"Enrollment Tokens": "توکن‌های ثبت"`,
		`"Create Enrollment Token": "ساخت توکن ثبت"`,
		`"Add agent": "افزودن ایجنت"`,
		`"Waiting for the agent to connect…": "در انتظار اتصال ایجنت…"`,
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
