package model

import (
	"strconv"
	"strings"
	"time"
)

// RRULE evaluation for recurring downtimes (SPEC §6.3). This is a focused
// subset of RFC 5545 sufficient for maintenance windows:
//
//	FREQ=HOURLY|DAILY|WEEKLY|MONTHLY, INTERVAL, BYDAY (weekly weekday filter),
//	BYMONTHDAY (monthly), COUNT and UNTIL.
//
// The window length and the series anchor (including time-of-day) come from
// the downtime's Start/End — Start already encodes the exact first instant,
// so each occurrence is "the [Start,End] window shifted onto the next time
// the rule fires". Everything is evaluated in UTC (no DST ambiguity).
// BYHOUR/BYMINUTE are tolerated in the string but not used for positioning,
// since Start fixes the clock-time already.

type rrule struct {
	freq       string
	interval   int
	byDay      map[time.Weekday]bool
	byMonthDay int // 0 = unset
	count      int // 0 = unbounded
	until      time.Time
}

var weekdayCodes = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

// parseRRule parses the supported subset. It returns ok=false when the rule
// has no usable FREQ, so callers can fall back safely.
func parseRRule(s string) (*rrule, bool) {
	r := &rrule{interval: 1}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "FREQ":
			r.freq = strings.ToUpper(strings.TrimSpace(v))
		case "INTERVAL":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				r.interval = n
			}
		case "BYDAY":
			r.byDay = map[time.Weekday]bool{}
			for _, code := range strings.Split(v, ",") {
				code = strings.ToUpper(strings.TrimSpace(code))
				if len(code) >= 2 { // drop any ordinal prefix like "1MO"/"-1FR"
					code = code[len(code)-2:]
				}
				if wd, ok := weekdayCodes[code]; ok {
					r.byDay[wd] = true
				}
			}
		case "BYMONTHDAY":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				r.byMonthDay = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				r.count = n
			}
		case "UNTIL":
			r.until = parseRRuleUntil(v)
		}
	}
	switch r.freq {
	case "HOURLY", "DAILY", "WEEKLY", "MONTHLY":
		return r, true
	default:
		return nil, false
	}
}

func parseRRuleUntil(v string) time.Time {
	v = strings.TrimSpace(v)
	for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
		if tm, err := time.Parse(layout, v); err == nil {
			return tm.UTC()
		}
	}
	return time.Time{}
}

// recurringActiveAt reports whether any RRULE occurrence's window contains t.
func (d *Downtime) recurringActiveAt(t time.Time) bool {
	start := d.Start.UTC()
	end := d.End.UTC()
	tt := t.UTC()
	dur := end.Sub(start)
	if dur <= 0 || tt.Before(start) {
		// No positive window or before the series starts: only the literal
		// first window can apply.
		return !tt.Before(start) && tt.Before(end)
	}
	r, ok := parseRRule(d.RRule)
	if !ok {
		// Unparseable rule: honour at least the first explicit window so a
		// typo never silently disables suppression entirely (tt >= start).
		return tt.Before(end)
	}
	if !r.until.IsZero() && tt.After(r.until.Add(dur)) {
		return false
	}
	for _, occ := range r.occurrenceStarts(start, tt, dur) {
		if occ.Before(start) || occ.After(tt) {
			continue
		}
		if !r.until.IsZero() && occ.After(r.until) {
			continue
		}
		if tt.Before(occ.Add(dur)) && r.withinCount(start, occ) {
			return true
		}
	}
	return false
}

// occurrenceStarts returns candidate occurrence start instants that could
// contain tt — i.e. those in (tt-dur, tt] — aligned to the rule.
func (r *rrule) occurrenceStarts(start, tt time.Time, dur time.Duration) []time.Time {
	if r.freq == "HOURLY" {
		step := time.Duration(r.interval) * time.Hour
		if step <= 0 {
			return nil
		}
		k := int(tt.Sub(start) / step)
		out := make([]time.Time, 0, 2)
		for _, kk := range []int{k, k + 1} { // k+1 guards integer-division edges
			if kk >= 0 {
				out = append(out, start.Add(time.Duration(kk)*step))
			}
		}
		return out
	}
	// DAILY/WEEKLY/MONTHLY: scan each calendar day in the window at the
	// anchor's clock-time and keep the days the rule fires on.
	var out []time.Time
	last := utcMidnight(tt)
	for day := utcMidnight(tt.Add(-dur)); !day.After(last); day = day.AddDate(0, 0, 1) {
		occ := atClock(day, start)
		if r.matchesDay(start, occ) {
			out = append(out, occ)
		}
	}
	return out
}

// matchesDay reports whether an occurrence anchored on occ's date is a valid
// firing of the rule relative to the series start.
func (r *rrule) matchesDay(start, occ time.Time) bool {
	switch r.freq {
	case "DAILY":
		days := daysBetween(start, occ)
		return days >= 0 && days%r.interval == 0
	case "WEEKLY":
		if len(r.byDay) > 0 {
			if !r.byDay[occ.Weekday()] {
				return false
			}
		} else if occ.Weekday() != start.Weekday() {
			return false
		}
		w := weeksBetween(start, occ)
		return w >= 0 && w%r.interval == 0
	case "MONTHLY":
		md := r.byMonthDay
		if md == 0 {
			md = start.Day()
		}
		if occ.Day() != md {
			return false
		}
		m := monthsBetween(start, occ)
		return m >= 0 && m%r.interval == 0
	}
	return false
}

// withinCount enforces a COUNT bound by computing occ's 0-based ordinal.
func (r *rrule) withinCount(start, occ time.Time) bool {
	if r.count == 0 {
		return true
	}
	var idx int
	switch r.freq {
	case "HOURLY":
		idx = int(occ.Sub(start) / (time.Duration(r.interval) * time.Hour))
	case "DAILY":
		idx = daysBetween(start, occ) / r.interval
	case "MONTHLY":
		idx = monthsBetween(start, occ) / r.interval
	case "WEEKLY":
		if len(r.byDay) <= 1 {
			idx = weeksBetween(start, occ) / r.interval
		} else {
			// Several weekdays per week: count matching firings up to occ.
			n := 0
			for day := utcMidnight(start); !day.After(utcMidnight(occ)); day = day.AddDate(0, 0, 1) {
				o := atClock(day, start)
				if !o.Before(start) && r.matchesDay(start, o) {
					n++
				}
			}
			idx = n - 1
		}
	}
	return idx >= 0 && idx < r.count
}

// --- UTC date arithmetic (no DST: day/week boundaries are exact) ---

func utcMidnight(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func atClock(day, ref time.Time) time.Time {
	h, m, s := ref.Clock()
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, s, ref.Nanosecond(), time.UTC)
}

func daysBetween(a, b time.Time) int {
	return int(utcMidnight(b).Sub(utcMidnight(a)) / (24 * time.Hour))
}

func weekStart(t time.Time) time.Time {
	m := utcMidnight(t)
	return m.AddDate(0, 0, -int(m.Weekday())) // back to Sunday
}

func weeksBetween(a, b time.Time) int {
	return daysBetween(weekStart(a), weekStart(b)) / 7
}

func monthsBetween(a, b time.Time) int {
	return (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
}
