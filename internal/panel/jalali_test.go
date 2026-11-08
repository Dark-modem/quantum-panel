package panel

// jalali_test.go — the calendar cannot drift: known-date anchors for
// the conversion (leap years included) and the Tehran month-range math
// the billing reports depend on.

import (
	"testing"
	"time"
)

func TestJalaliKnownDates(t *testing.T) {
	// (jy, jm, jd) → Gregorian — pinned anchors.
	cases := []struct {
		jy, jm, jd int
		gy         int
		gm         time.Month
		gd         int
	}{
		{1403, 12, 30, 2025, time.March, 20}, // 1403 IS leap: the day before Nowruz 1404 (2025-03-21)
		{1404, 1, 1, 2025, time.March, 21},   // 1 Farvardin 1404
		{1405, 1, 1, 2026, time.March, 21},   // 1 Farvardin 1405
		{1405, 8, 1, 2026, time.October, 23}, // 1 Aban 1405 (a billing month)
		{1404, 12, 29, 2026, time.March, 20}, // 1404 NOT leap: Esfand has 29
		{1400, 1, 1, 2021, time.March, 21},
		{1398, 1, 1, 2019, time.March, 21}, // 1398 leap (Feb 29 2020 inside)
	}
	for _, c := range cases {
		gy, gm, gd, err := JalaliToGregorian(c.jy, c.jm, c.jd)
		if err != nil {
			t.Fatalf("%d-%d-%d: %v", c.jy, c.jm, c.jd, err)
		}
		if gy != c.gy || time.Month(gm) != c.gm || gd != c.gd {
			t.Errorf("JalaliToGregorian(%d,%d,%d) = %d-%d-%d, want %d-%s-%d",
				c.jy, c.jm, c.jd, gy, gm, gd, c.gy, c.gm, c.gd)
		}
	}
	if !JalaliIsLeap(1403) {
		t.Error("1403 must be leap (30 Esfand 1403 = 2026-03-20)")
	}
	if JalaliIsLeap(1404) || JalaliIsLeap(1405) {
		t.Error("1404/1405 must NOT be leap")
	}
	if ml, _ := jalaliMonthLen(1403, 12); ml != 30 {
		t.Errorf("Esfand 1403 = %d days, want 30", ml)
	}
	if ml, _ := jalaliMonthLen(1404, 12); ml != 29 {
		t.Errorf("Esfand 1404 = %d days, want 29", ml)
	}
}

func TestGregorianToJalaliRoundTrip(t *testing.T) {
	// Every day of Aban 1405 round-trips (1 Aban = 2026-10-23 Tehran).
	for d := 1; d <= 30; d++ {
		want := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d-1)
		jy, jm, jd, err := GregorianToJalali(want.Year(), int(want.Month()), want.Day())
		if err != nil {
			t.Fatal(err)
		}
		if jy != 1405 || jm != 8 || jd != d {
			t.Fatalf("%s → %d-%d-%d, want 1405-8-%d", want.Format("2006-01-02"), jy, jm, jd, d)
		}
		gy, gm, gd, _ := JalaliToGregorian(1405, 8, d)
		if gy != want.Year() || int(gm) != int(want.Month()) || gd != want.Day() {
			t.Fatalf("round trip broke at %d: %d-%d-%d, want %s", d, gy, gm, gd, want.Format("2006-01-02"))
		}
	}
}

func TestTehranMonthRange(t *testing.T) {
	from, to, err := TehranMonthRange(1405, 8)
	if err != nil {
		t.Fatal(err)
	}
	// 1405-08-01 00:00 Tehran = 2026-10-22 20:30 UTC (fixed +03:30).
	wantFrom := time.Date(2026, 10, 22, 20, 30, 0, 0, time.UTC).Unix()
	// 30-day month: ends 2026-11-21 20:30 UTC.
	wantTo := time.Date(2026, 11, 21, 20, 30, 0, 0, time.UTC).Unix()
	if from != wantFrom || to != wantTo {
		t.Fatalf("TehranMonthRange(1405,8) = %d..%d, want %d..%d", from, to, wantFrom, wantTo)
	}
	// A leap Esfand: 1403-12 has 30 days.
	from, to, err = TehranMonthRange(1403, 12)
	if err != nil {
		t.Fatal(err)
	}
	if to-from != 30*86400 {
		t.Fatalf("Esfand 1403 = %d s, want 30 days", to-from)
	}
	if _, _, err := TehranMonthRange(1405, 13); err == nil {
		t.Fatal("month 13 must be rejected")
	}
}
