package model

import (
	"strings"
	"testing"
	"time"
)

func mkSchedule() *Schedule {
	anchor := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC) // Monday 08:00
	return &Schedule{
		ID: "s1", Name: "netz", TimeZone: "UTC",
		Layers: []Rotation{{
			Name:         "primary",
			Participants: []string{"alice", "bob", "carol"},
			Unit:         RotateWeekly,
			Anchor:       anchor,
		}},
	}
}

func TestRotationWheel(t *testing.T) {
	s := mkSchedule()
	// week 1: alice, week 2: bob, week 3: carol, week 4: alice again
	cases := []struct {
		day  int
		want string
	}{{0, "alice"}, {6, "alice"}, {7, "bob"}, {13, "bob"}, {14, "carol"}, {21, "alice"}}
	for _, c := range cases {
		at := s.Layers[0].Anchor.Add(time.Duration(c.day)*24*time.Hour + time.Hour)
		shifts := ResolveOnCall(s, nil, at, 0)
		if len(shifts) != 1 || shifts[0].ContactID != c.want {
			t.Fatalf("day %d: got %+v want %s", c.day, shifts, c.want)
		}
	}
	// backup (escalateTo: backup) = next on the wheel
	at := s.Layers[0].Anchor.Add(time.Hour)
	shifts := ResolveOnCall(s, nil, at, 1)
	if len(shifts) != 1 || shifts[0].ContactID != "bob" {
		t.Fatalf("backup: %+v", shifts)
	}
	// pre-anchor times walk the wheel backwards without panicking
	pre := ResolveOnCall(s, nil, s.Layers[0].Anchor.Add(-time.Hour), 0)
	if len(pre) != 1 || pre[0].ContactID != "carol" {
		t.Fatalf("pre-anchor: %+v", pre)
	}
}

func TestOverridesWin(t *testing.T) {
	s := mkSchedule()
	at := s.Layers[0].Anchor.Add(2 * time.Hour)
	ov := []Override{{ContactID: "dave",
		Start: at.Add(-time.Hour), End: at.Add(time.Hour)}}
	shifts := ResolveOnCall(s, ov, at, 0)
	if len(shifts) != 1 || shifts[0].ContactID != "dave" || !shifts[0].Override {
		t.Fatalf("override: %+v", shifts)
	}
	// outside the override window the wheel resumes
	shifts = ResolveOnCall(s, ov, at.Add(2*time.Hour), 0)
	if shifts[0].ContactID != "alice" {
		t.Fatalf("after override: %+v", shifts)
	}
}

func TestTimelineMergesAndSplits(t *testing.T) {
	s := mkSchedule()
	from := s.Layers[0].Anchor
	to := from.Add(21 * 24 * time.Hour)
	ov := []Override{{ContactID: "dave",
		Start: from.Add(8 * 24 * time.Hour), End: from.Add(9 * 24 * time.Hour)}}
	tl := ScheduleTimeline(s, ov, from, to)
	// expected: alice w1, bob (split by dave), carol w3 → 5 segments
	if len(tl) != 5 {
		for _, sh := range tl {
			t.Logf("%s — %s: %s ov=%v", sh.Start, sh.End, sh.ContactID, sh.Override)
		}
		t.Fatalf("segments: %d want 5", len(tl))
	}
	if tl[1].ContactID != "bob" || tl[2].ContactID != "dave" || tl[3].ContactID != "bob" {
		t.Fatalf("split wrong: %+v", tl)
	}
	// contiguity
	for i := 1; i < len(tl); i++ {
		if !tl[i].Start.Equal(tl[i-1].End) {
			t.Fatalf("gap between %d and %d", i-1, i)
		}
	}
}

func TestICSRendering(t *testing.T) {
	s := mkSchedule()
	from := s.Layers[0].Anchor
	tl := ScheduleTimeline(s, nil, from, from.Add(14*24*time.Hour))
	ics := ICS(s, tl, func(id string) string { return strings.ToUpper(id) })
	for _, want := range []string{"BEGIN:VCALENDAR", "SUMMARY:On-Call: ALICE",
		"SUMMARY:On-Call: BOB", "END:VCALENDAR"} {
		if !strings.Contains(ics, want) {
			t.Fatalf("ICS missing %q:\n%s", want, ics)
		}
	}
}

func TestRestrictionWindows(t *testing.T) {
	s := mkSchedule()
	s.Layers[0].Restriction = map[string][]string{
		"saturday": {"00:00-24:00"}, "sunday": {"00:00-24:00"},
	}
	// Monday 09:00: restricted layer yields nobody
	monday := s.Layers[0].Anchor.Add(time.Hour)
	if shifts := ResolveOnCall(s, nil, monday, 0); len(shifts) != 0 {
		t.Fatalf("weekday should be off-duty: %+v", shifts)
	}
	// Saturday: on duty
	saturday := s.Layers[0].Anchor.Add(5 * 24 * time.Hour)
	if shifts := ResolveOnCall(s, nil, saturday, 0); len(shifts) != 1 {
		t.Fatalf("saturday should be on duty")
	}
}
