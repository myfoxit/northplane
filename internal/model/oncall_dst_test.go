package model

import (
	"testing"
	"time"
)

// TestInRangesOvernight guards the wrapping-window fix: "19:00-07:00" must
// match late-night and early-morning, and not the afternoon.
func TestInRangesOvernight(t *testing.T) {
	day := func(h, m int) time.Time { return time.Date(2026, 6, 1, h, m, 0, 0, time.UTC) }
	r := []string{"19:00-07:00"}
	cases := map[time.Time]bool{
		day(20, 0): true,  // evening
		day(2, 0):  true,  // small hours
		day(6, 59): true,  // just before handover
		day(7, 0):  false, // exactly at end → out
		day(12, 0): false, // afternoon
		day(18, 59): false,
	}
	for ts, want := range cases {
		if got := inRanges(r, ts); got != want {
			t.Errorf("inRanges(19:00-07:00, %s) = %v, want %v", ts.Format("15:04"), got, want)
		}
	}
}

// TestOnCallDailyHandoffDST guards that a daily rotation hands over at the
// same local wall-clock time across a DST transition rather than drifting
// by an hour (Europe/Vienna springs forward 2026-03-29 02:00→03:00).
func TestOnCallDailyHandoffDST(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	anchor := time.Date(2026, 3, 27, 8, 0, 0, 0, vienna) // Fri 08:00 local
	rot := &Rotation{Participants: []string{"a", "b", "c"}, Unit: RotateDaily, Anchor: anchor}

	// Sample just after 08:00 local on each of the next four days; the
	// shift start must always be 08:00 local (DST boundary is 03-29).
	for i := 0; i < 4; i++ {
		day := anchor.AddDate(0, 0, i).Add(30 * time.Minute) // 08:30 local
		_, start, _ := rot.OnCallAt(day, vienna, 0)
		h, m, _ := start.In(vienna).Clock()
		if h != 8 || m != 0 {
			t.Errorf("day %d: handoff at %02d:%02d local, want 08:00", i, h, m)
		}
	}
}
