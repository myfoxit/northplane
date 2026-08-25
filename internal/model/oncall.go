package model

import (
	"fmt"
	"sort"
	"time"
)

// Schedule is an on-call plan composed of layered rotations plus
// overrides ("Bereitschaftsrad", SPEC §9.5 / F-03.x).
type Schedule struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenantId"`
	Name      string     `json:"name"`
	TimeZone  string     `json:"timeZone"` // IANA name; rotations anchor here
	Layers    []Rotation `json:"layers"`
	Version   int64      `json:"version"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// RotationUnit is the shift cadence.
type RotationUnit string

const (
	RotateDaily  RotationUnit = "daily"
	RotateWeekly RotationUnit = "weekly"
	RotateCustom RotationUnit = "custom"
)

// Rotation cycles participants in order starting at Anchor (the "wheel",
// F-03.02). Restriction limits duty to time windows within a shift
// (e.g. nights + weekend only).
type Rotation struct {
	Name         string       `json:"name,omitempty"`
	Participants []string     `json:"participants"` // contact IDs, wheel order
	Unit         RotationUnit `json:"unit"`
	Length       Duration     `json:"length,omitempty"` // custom unit only
	Anchor       time.Time    `json:"anchor"`           // start of participant[0]'s first shift
	// Restriction: optional TimePeriod-style day/time windows during
	// which this layer is actually on duty ("19:00-07:00 + weekend").
	Restriction map[string][]string `json:"restriction,omitempty"`
}

// Override replaces whoever is on duty during [Start,End) (vacation,
// swap — F-03.03).
type Override struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	ScheduleID string    `json:"scheduleId"`
	ContactID  string    `json:"contactId"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Reason     string    `json:"reason,omitempty"`
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
}

// shiftLength resolves the rotation cadence.
func (r *Rotation) shiftLength() time.Duration {
	switch r.Unit {
	case RotateDaily:
		return 24 * time.Hour
	case RotateWeekly:
		return 7 * 24 * time.Hour
	default:
		if r.Length > 0 {
			return r.Length.D()
		}
		return 24 * time.Hour
	}
}

// OnCallAt resolves who is on duty at t for the rotation (ignoring
// overrides; the schedule-level resolver applies them). Returns the
// contact ID and the shift bounds, or "" when the layer's restriction
// excludes t. offset shifts the wheel (0 = primary, 1 = backup/next —
// SPEC §9.4 escalateTo: backup).
func (r *Rotation) OnCallAt(t time.Time, loc *time.Location, offset int) (string, time.Time, time.Time) {
	if len(r.Participants) == 0 {
		return "", time.Time{}, time.Time{}
	}
	if r.Restriction != nil {
		tp := TimePeriod{Days: r.Restriction}
		if !tp.Contains(t.In(loc)) {
			return "", time.Time{}, time.Time{}
		}
	}
	var idx int
	var start, end time.Time
	switch r.Unit {
	case RotateDaily, RotateWeekly:
		// Daily/weekly handoffs happen at a fixed wall-clock time in the
		// schedule's timezone. Use calendar arithmetic (AddDate) in loc so
		// a DST transition does not shift the handover by an hour or drift
		// the "every Monday 08:00" boundary over the year.
		step := 1
		if r.Unit == RotateWeekly {
			step = 7
		}
		idx, start, end = calendarShift(r.Anchor.In(loc), t.In(loc), step)
	default:
		sl := r.shiftLength()
		elapsed := t.Sub(r.Anchor)
		idx = int(elapsed / sl)
		if elapsed < 0 {
			idx-- // floor division for pre-anchor times
		}
		start = r.Anchor.Add(time.Duration(idx) * sl)
		end = start.Add(sl)
	}
	who := ((idx+offset)%len(r.Participants) + len(r.Participants)) % len(r.Participants)
	return r.Participants[who], start, end
}

// calendarShift returns the rotation index and [start,end) bounds of the
// shift containing t, where each shift is stepDays calendar days long
// anchored to anchor's wall-clock time. anchor and t must already be in
// the target location so AddDate preserves the local time-of-day across
// DST.
func calendarShift(anchor, t time.Time, stepDays int) (int, time.Time, time.Time) {
	idx := int(t.Sub(anchor).Hours()/24) / stepDays // close approximation
	start := anchor.AddDate(0, 0, idx*stepDays)
	// Converge to the exact period: start <= t < end.
	for next := anchor.AddDate(0, 0, (idx+1)*stepDays); !next.After(t); next = anchor.AddDate(0, 0, (idx+1)*stepDays) {
		idx++
		start = next
	}
	for start.After(t) {
		idx--
		start = anchor.AddDate(0, 0, idx*stepDays)
	}
	end := anchor.AddDate(0, 0, (idx+1)*stepDays)
	return idx, start, end
}

// OnCallShift is a resolved duty assignment.
type OnCallShift struct {
	ContactID string    `json:"contactId"`
	Layer     string    `json:"layer,omitempty"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Override  bool      `json:"override,omitempty"`
}

// ResolveOnCall computes who is on duty at t: overrides win, then layers
// in order (first layer with an active restriction window wins).
// offset selects the Nth person of the winning wheel (escalation).
func ResolveOnCall(s *Schedule, overrides []Override, t time.Time, offset int) []OnCallShift {
	loc := time.UTC
	if s.TimeZone != "" {
		if l, err := time.LoadLocation(s.TimeZone); err == nil {
			loc = l
		}
	}
	// An active override prepends its contact to the duty chain: offset 0
	// is the override person, deeper offsets walk the regular wheel shifted
	// by one — so escalateTo: backup honours the override (it pages the
	// regular rotation) instead of resolving as if none existed.
	for _, o := range overrides {
		if !t.Before(o.Start) && t.Before(o.End) {
			if offset == 0 {
				return []OnCallShift{{ContactID: o.ContactID, Start: o.Start, End: o.End, Override: true}}
			}
			offset--
			break
		}
	}
	for _, layer := range s.Layers {
		if who, start, end := layer.OnCallAt(t, loc, offset); who != "" {
			return []OnCallShift{{ContactID: who, Layer: layer.Name, Start: start, End: end}}
		}
	}
	return nil
}

// ScheduleTimeline renders resolved shifts over [from,to) for calendar
// views and the ICS feed (SPEC §9.5; F-05.06). Shifts are clipped to the
// window, overrides split underlying shifts.
func ScheduleTimeline(s *Schedule, overrides []Override, from, to time.Time) []OnCallShift {
	if !from.Before(to) {
		return nil
	}
	var out []OnCallShift
	// Walk in steps bounded by the smallest layer cadence; resolution
	// per minute would be wasteful, per shift-boundary is exact: collect
	// all boundaries in window, resolve each interval.
	bounds := map[time.Time]bool{from: true, to: true}
	for _, l := range s.Layers {
		sl := l.shiftLength()
		if sl <= 0 || len(l.Participants) == 0 {
			continue
		}
		first := l.Anchor.Add(time.Duration(int64(from.Sub(l.Anchor)/sl)) * sl)
		for b := first; b.Before(to); b = b.Add(sl) {
			if b.After(from) {
				bounds[b] = true
			}
		}
		// Restriction windows change duty within shifts — sample hourly
		// boundaries when restricted (windows are minute-granular but
		// hour-aligned in practice; minute-walk only at edges).
		if l.Restriction != nil {
			for b := from.Truncate(time.Hour); b.Before(to); b = b.Add(time.Hour) {
				if b.After(from) {
					bounds[b] = true
				}
			}
		}
	}
	for _, o := range overrides {
		if o.Start.After(from) && o.Start.Before(to) {
			bounds[o.Start] = true
		}
		if o.End.After(from) && o.End.Before(to) {
			bounds[o.End] = true
		}
	}
	pts := make([]time.Time, 0, len(bounds))
	for b := range bounds {
		pts = append(pts, b)
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Before(pts[j]) })

	for i := 0; i+1 < len(pts); i++ {
		seg0, seg1 := pts[i], pts[i+1]
		shifts := ResolveOnCall(s, overrides, seg0, 0)
		if len(shifts) == 0 {
			continue
		}
		sh := shifts[0]
		sh.Start, sh.End = seg0, seg1
		// Merge with previous segment when same person & contiguity.
		if n := len(out); n > 0 && out[n-1].ContactID == sh.ContactID &&
			out[n-1].End.Equal(sh.Start) && out[n-1].Override == sh.Override {
			out[n-1].End = sh.End
			continue
		}
		out = append(out, sh)
	}
	return out
}

// ICS renders the timeline as an iCalendar feed (SPEC §9.5).
func ICS(s *Schedule, shifts []OnCallShift, contactName func(id string) string) string {
	const stamp = "20060102T150405Z"
	b := &stringsBuilder{}
	b.line("BEGIN:VCALENDAR")
	b.line("VERSION:2.0")
	b.line("PRODID:-//northplane//oncall//EN")
	b.line("X-WR-CALNAME:On-Call " + icsEscape(s.Name))
	for i, sh := range shifts {
		b.line("BEGIN:VEVENT")
		b.line(fmt.Sprintf("UID:%s-%d@northplane", s.ID, i))
		b.line("DTSTAMP:" + sh.Start.UTC().Format(stamp))
		b.line("DTSTART:" + sh.Start.UTC().Format(stamp))
		b.line("DTEND:" + sh.End.UTC().Format(stamp))
		name := contactName(sh.ContactID)
		sum := "On-Call: " + name
		if sh.Override {
			sum += " (Override)"
		}
		b.line("SUMMARY:" + icsEscape(sum))
		b.line("END:VEVENT")
	}
	b.line("END:VCALENDAR")
	return b.String()
}

type stringsBuilder struct{ b []byte }

func (s *stringsBuilder) line(l string)  { s.b = append(s.b, l...); s.b = append(s.b, '\r', '\n') }
func (s *stringsBuilder) String() string { return string(s.b) }

func icsEscape(s string) string {
	var out []byte
	for _, r := range s {
		switch r {
		case '\\', ';', ',':
			out = append(out, '\\', byte(r))
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, string(r)...)
		}
	}
	return string(out)
}
