package panel

// jalali.go — Jalali (Solar Hijri) calendar for the billing reports
// (phase 12 TASK 4). A direct port of the standard jalaali algorithm
// (Borkowski 2820-year cycle with the breaks table, as used by
// jalaali-js). Iran abolished DST in 2022, so Asia/Tehran is the FIXED
// +03:30 offset for every date the panel bills; month ranges snap to
// Tehran midnight, never UTC. The tests pin known dates (1 Farvardin
// 1405 = 2026-03-21, 1 Aban 1405 = 2026-10-23, 29 Esfand 1403 =
// 2026-03-20 — 1403 is leap) so the conversion cannot silently drift.

import (
	"fmt"
	"time"
)

// tehranZone is fixed +03:30 (no DST since 2022-03-21).
var tehranZone = time.FixedZone("Asia/Tehran", 3*3600+1800)

func jdiv(a, b int) int { return a / b } // args are positive here

func jmod(a, b int) int {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

var jalaliBreaks = []int{-61, 9, 38, 199, 426, 686, 756, 818, 1111, 1181, 1210,
	1635, 1701, 1817, 1865, 1900, 1931, 1972, 2000, 2057, 2147, 2203, 2301,
	2405, 2518, 2684, 2745, 2802}

// jalCal returns the leap flag of jy and its Gregorian March-equivalent
// (the day of March on which Farvardin 1 falls).
func jalCal(jy int) (leap int, gy, march int, err error) {
	bl := len(jalaliBreaks)
	gy = jy + 621
	leapJ := -14
	jp := jalaliBreaks[0]
	jump := 0
	if jy < jp || jy >= jalaliBreaks[bl-1] {
		return 0, gy, 0, fmt.Errorf("jalali year %d outside the supported range", jy)
	}
	for i := 1; i < bl; i++ {
		jm := jalaliBreaks[i]
		jump = jm - jp
		if jy < jm {
			break
		}
		leapJ += jdiv(jump, 33)*8 + jdiv(jmod(jump, 33), 4)
		jp = jm
	}
	n := jy - jp
	leapJ += jdiv(n, 33)*8 + jdiv(jmod(n, 33)+3, 4)
	if jmod(jump, 33) == 4 && jump-n == 4 {
		leapJ++
	}
	leapG := jdiv(gy, 4) - jdiv((jdiv(gy, 100)+1)*3, 4) - 150
	march = 20 + leapJ - leapG
	leap = jmod(jmod(n+1, 33)-1, 4)
	if leap == -1 {
		leap = 4
	}
	return leap, gy, march, nil
}

// g2d converts a Gregorian date to the jalaali day number.
func g2d(gy, gm, gd int) int {
	d := jdiv((gy+jdiv(gm-8, 6)+100100)*1461, 4) +
		jdiv(153*jmod(gm+9, 12)+2, 5) + gd - 34840408
	d = d - jdiv(jdiv(gy+100100+jdiv(gm-8, 6), 100)*3, 4) + 752
	return d
}

// d2g converts the jalaali day number back to a Gregorian date.
func d2g(jdn int) (gy, gm, gd int) {
	j := 4*jdn + 139361631
	j += jdiv(jdiv(4*jdn+183187720, 146097)*3, 4)*4 - 3908
	i := jdiv(jmod(j, 1461), 4)*5 + 308
	gd = jdiv(jmod(i, 153), 5) + 1
	gm = jmod(jdiv(i, 153), 12) + 1
	gy = jdiv(j, 1461) - 100100 + jdiv(8-gm, 6)
	return gy, gm, gd
}

// j2d converts a Jalali date to the day number.
func j2d(jy, jm, jd int) (int, error) {
	_, gy, march, err := jalCal(jy)
	if err != nil {
		return 0, err
	}
	return g2d(gy, 3, march) + (jm-1)*31 - jdiv(jm, 7)*(jm-7) + jd - 1, nil
}

// d2j converts a day number to a Jalali date.
func d2j(jdn int) (jy, jm, jd int, err error) {
	gy, _, _ := d2g(jdn)
	jy = gy - 621
	leap, _, march, err := jalCal(jy)
	if err != nil {
		return 0, 0, 0, err
	}
	jdn1f := g2d(gy, 3, march)
	k := jdn - jdn1f
	if k >= 0 {
		if k <= 185 {
			return jy, 1 + jdiv(k, 31), jmod(k, 31) + 1, nil
		}
		k -= 186
	} else {
		jy--
		k += 179
		if leap == 1 {
			k++
		}
	}
	return jy, 7 + jdiv(k, 30), jmod(k, 30) + 1, nil
}

// JalaliIsLeap reports whether the Jalali year has 366 days.
func JalaliIsLeap(jy int) bool {
	leap, _, _, err := jalCal(jy)
	return err == nil && leap == 0
}

func jalaliMonthLen(jy, jm int) (int, error) {
	if jm < 1 || jm > 12 {
		return 0, fmt.Errorf("invalid Jalali month %d", jm)
	}
	if jm <= 6 {
		return 31, nil
	}
	if jm <= 11 {
		return 30, nil
	}
	if JalaliIsLeap(jy) {
		return 30, nil
	}
	return 29, nil
}

// JalaliToGregorian converts a Jalali date to a Gregorian date.
func JalaliToGregorian(jy, jm, jd int) (gy, gm, gd int, err error) {
	d, err := j2d(jy, jm, jd)
	if err != nil {
		return 0, 0, 0, err
	}
	gy, gm, gd = d2g(d)
	return gy, gm, gd, nil
}

// GregorianToJalali converts a Gregorian date to a Jalali date.
func GregorianToJalali(gy, gm, gd int) (jy, jm, jd int, err error) {
	jy, jm, jd, err = d2j(g2d(gy, gm, gd))
	return jy, jm, jd, err
}

// TehranMonthRange returns [start, end) unix seconds of a Jalali month
// at Tehran local midnight (end exclusive — the billing window).
func TehranMonthRange(jy, jm int) (from, to int64, err error) {
	ml, err := jalaliMonthLen(jy, jm)
	if err != nil {
		return 0, 0, err
	}
	gy1, gm1, gd1, err := JalaliToGregorian(jy, jm, 1)
	if err != nil {
		return 0, 0, err
	}
	gy2, gm2, gd2, err := JalaliToGregorian(jy, jm, ml)
	if err != nil {
		return 0, 0, err
	}
	start := time.Date(gy1, time.Month(gm1), gd1, 0, 0, 0, 0, tehranZone)
	end := time.Date(gy2, time.Month(gm2), gd2, 0, 0, 0, 0, tehranZone).Add(24 * time.Hour)
	return start.Unix(), end.Unix(), nil
}

// TehranDayRange returns [start, end) unix seconds of one Tehran day.
func TehranDayRange(y, m, d int) (from, to int64) {
	start := time.Date(y, time.Month(m), d, 0, 0, 0, 0, tehranZone)
	return start.Unix(), start.Add(24 * time.Hour).Unix()
}
