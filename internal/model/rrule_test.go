package model

import (
	"testing"
	"time"
)

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

// TestRecurringDailyRepeats is the core regression: a daily window must
// suppress on every later day, not just its first occurrence.
func TestRecurringDailyRepeats(t *testing.T) {
	d := &Downtime{
		Type:  DowntimeFixed,
		Start: utc(2026, time.January, 1, 3, 0),
		End:   utc(2026, time.January, 1, 4, 0),
		RRule: "FREQ=DAILY;BYHOUR=3;BYMINUTE=0",
	}
	cases := []struct {
		t    time.Time
		want bool
	}{
		{utc(2026, time.January, 1, 3, 30), true},    // first window
		{utc(2026, time.January, 1, 4, 30), false},   // after first window
		{utc(2026, time.January, 1, 2, 30), false},   // before first window
		{utc(2026, time.March, 15, 3, 30), true},     // months later, in window
		{utc(2026, time.March, 15, 5, 0), false},     // months later, out of window
		{utc(2027, time.December, 31, 3, 1), true},   // a year+ later, in window
		{utc(2025, time.December, 31, 3, 30), false}, // before the series starts
	}
	for _, c := range cases {
		if got := d.ActiveAt(c.t); got != c.want {
			t.Errorf("ActiveAt(%s) = %v, want %v", c.t.Format(time.RFC3339), got, c.want)
		}
	}
}

func TestRecurringWeeklyByDay(t *testing.T) {
	// 2026-01-03 is a Saturday. Window 10:00–12:00 every Sat & Sun.
	d := &Downtime{
		Type:  DowntimeFixed,
		Start: utc(2026, time.January, 3, 10, 0),
		End:   utc(2026, time.January, 3, 12, 0),
		RRule: "FREQ=WEEKLY;BYDAY=SA,SU",
	}
	cases := []struct {
		t    time.Time
		want bool
	}{
		{utc(2026, time.January, 3, 11, 0), true},   // Sat in window
		{utc(2026, time.January, 4, 11, 0), true},   // Sun in window
		{utc(2026, time.January, 5, 11, 0), false},  // Mon — not a chosen weekday
		{utc(2026, time.January, 10, 10, 30), true}, // next Sat
		{utc(2026, time.January, 10, 13, 0), false}, // next Sat, out of window
		{utc(2026, time.February, 7, 11, 0), true},  // weeks later, Sat
	}
	for _, c := range cases {
		if got := d.ActiveAt(c.t); got != c.want {
			t.Errorf("ActiveAt(%s, %s) = %v, want %v",
				c.t.Format(time.RFC3339), c.t.Weekday(), got, c.want)
		}
	}
}

func TestRecurringDailyInterval(t *testing.T) {
	// Every other day, 00:00–01:00, starting 2026-01-01.
	d := &Downtime{
		Type:  DowntimeFixed,
		Start: utc(2026, time.January, 1, 0, 0),
		End:   utc(2026, time.January, 1, 1, 0),
		RRule: "FREQ=DAILY;INTERVAL=2",
	}
	if !d.ActiveAt(utc(2026, time.January, 1, 0, 30)) {
		t.Error("day 0 should be active")
	}
	if d.ActiveAt(utc(2026, time.January, 2, 0, 30)) {
		t.Error("day 1 (odd) should NOT be active")
	}
	if !d.ActiveAt(utc(2026, time.January, 3, 0, 30)) {
		t.Error("day 2 should be active")
	}
}

func TestRecurringMonthly(t *testing.T) {
	// 15th of each month, 09:00–10:00.
	d := &Downtime{
		Type:  DowntimeFixed,
		Start: utc(2026, time.January, 15, 9, 0),
		End:   utc(2026, time.January, 15, 10, 0),
		RRule: "FREQ=MONTHLY",
	}
	if !d.ActiveAt(utc(2026, time.April, 15, 9, 30)) {
		t.Error("Apr 15 should be active")
	}
	if d.ActiveAt(utc(2026, time.April, 16, 9, 30)) {
		t.Error("Apr 16 should NOT be active")
	}
}

func TestRecurringUntilAndCount(t *testing.T) {
	until := &Downtime{
		Type:  DowntimeFixed,
		Start: utc(2026, time.January, 1, 3, 0),
		End:   utc(2026, time.January, 1, 4, 0),
		RRule: "FREQ=DAILY;UNTIL=20260105T000000Z",
	}
	if !until.ActiveAt(utc(2026, time.January, 4, 3, 30)) {
		t.Error("within UNTIL should be active")
	}
	if until.ActiveAt(utc(2026, time.January, 6, 3, 30)) {
		t.Error("past UNTIL should NOT be active")
	}

	count := &Downtime{
		Type:  DowntimeFixed,
		Start: utc(2026, time.January, 1, 3, 0),
		End:   utc(2026, time.January, 1, 4, 0),
		RRule: "FREQ=DAILY;COUNT=3", // occurrences on Jan 1,2,3 only
	}
	if !count.ActiveAt(utc(2026, time.January, 3, 3, 30)) {
		t.Error("3rd occurrence should be active")
	}
	if count.ActiveAt(utc(2026, time.January, 4, 3, 30)) {
		t.Error("4th occurrence should NOT be active (COUNT=3)")
	}
}

func TestRecurringFallbacks(t *testing.T) {
	// Unparseable rule → at least the first window still suppresses.
	bad := &Downtime{
		Type:  DowntimeFixed,
		Start: utc(2026, time.January, 1, 3, 0),
		End:   utc(2026, time.January, 1, 4, 0),
		RRule: "FREQ=GIBBERISH;FOO=BAR",
	}
	if !bad.ActiveAt(utc(2026, time.January, 1, 3, 30)) {
		t.Error("unparseable rule must still honour the first window")
	}
	if bad.ActiveAt(utc(2026, time.January, 2, 3, 30)) {
		t.Error("unparseable rule must not recur")
	}

	// No RRule → unchanged one-shot behaviour.
	once := &Downtime{
		Type:  DowntimeFixed,
		Start: utc(2026, time.January, 1, 3, 0),
		End:   utc(2026, time.January, 1, 4, 0),
	}
	if once.ActiveAt(utc(2026, time.January, 2, 3, 30)) {
		t.Error("non-recurring downtime must not repeat")
	}
}
