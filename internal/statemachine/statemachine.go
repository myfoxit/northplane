// Package statemachine implements the Nagios-semantic check state
// machine (SPEC §6.3): soft/hard transitions with max_check_attempts,
// the recovery special case (recovery is always immediately hard),
// weighted flapping detection over the last 21 transitions, and
// freshness/staleness handling. The transition function is pure —
// persistence and event emission happen in the pipeline.
package statemachine

import (
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Input is one check result applied to the current state.
type Input struct {
	State      model.State
	Output     string
	LongOutput string
	Perfdata   string
	At         time.Time
	LatencyMS  int64
	ExecMS     int64
}

// Config carries the effective object settings the machine needs.
type Config struct {
	MaxCheckAttempts  int
	FlapDetection     bool
	FlapThresholdLow  float64 // % — stop flapping below (default 25)
	FlapThresholdHigh float64 // % — start flapping above (default 50)
}

// Transition describes what happened.
type Transition struct {
	StateChanged bool // raw state differs from previous raw state
	HardChange   bool // a hard state was entered (notify on this)
	Recovered    bool // hard problem → OK
	FlapStarted  bool
	FlapStopped  bool
	IsSoft       bool
}

// Apply mutates cs according to the Nagios rules and returns the
// transition summary.
func Apply(cs *model.CheckState, cfg Config, in Input) Transition {
	if cfg.MaxCheckAttempts <= 0 {
		cfg.MaxCheckAttempts = 3
	}
	if cfg.FlapThresholdLow <= 0 {
		cfg.FlapThresholdLow = 25
	}
	if cfg.FlapThresholdHigh <= 0 {
		cfg.FlapThresholdHigh = 50
	}

	prevState := cs.State
	prevType := cs.StateType
	if prevType == "" {
		prevType = model.StateHard
	}

	var tr Transition
	tr.StateChanged = in.State != prevState

	// Flap history: record whether this check changed the raw state
	// (bitfield, LSB = newest of the last 21 checks).
	recordFlap(cs, tr.StateChanged)

	switch {
	case in.State == model.StateOK:
		if prevState != model.StateOK && prevType == model.StateHard {
			tr.Recovered = true
			tr.HardChange = true
		}
		// Recovery is always immediately hard (SPEC §6.3) — also from
		// soft problem states (no notification was sent for those).
		cs.State = model.StateOK
		cs.StateType = model.StateHard
		cs.Attempt = 1
		now := in.At
		cs.LastOK = &now
		if tr.Recovered || (tr.StateChanged && prevType == model.StateSoft) {
			t := in.At
			if tr.Recovered {
				cs.LastHardChange = &t
			}
		}

	case prevState == model.StateOK || (tr.StateChanged && prevType == model.StateHard):
		// New problem (from OK) or hard problem changing severity.
		if prevState == model.StateOK {
			// entering problem state: soft until attempts exhausted
			if cfg.MaxCheckAttempts == 1 {
				cs.State = in.State
				cs.StateType = model.StateHard
				cs.Attempt = 1
				t := in.At
				cs.LastHardChange = &t
				tr.HardChange = true
			} else {
				cs.State = in.State
				cs.StateType = model.StateSoft
				cs.Attempt = 1
				tr.IsSoft = true
			}
		} else {
			// hard problem → different problem severity: stays hard
			// (Nagios: state change between non-OK hard states is an
			// immediate hard change).
			cs.State = in.State
			cs.StateType = model.StateHard
			cs.Attempt = 1
			t := in.At
			cs.LastHardChange = &t
			tr.HardChange = true
		}

	case prevType == model.StateSoft:
		// continuing problem in soft state (same or different severity)
		cs.State = in.State
		cs.Attempt++
		if cs.Attempt >= cfg.MaxCheckAttempts {
			cs.StateType = model.StateHard
			cs.Attempt = cfg.MaxCheckAttempts
			t := in.At
			cs.LastHardChange = &t
			tr.HardChange = true
		} else {
			tr.IsSoft = true
		}

	default:
		// same hard problem state continues
		cs.State = in.State
		cs.StateType = model.StateHard
		cs.Attempt = cfg.MaxCheckAttempts
	}

	cs.Output = in.Output
	cs.LongOutput = in.LongOutput
	cs.Perfdata = in.Perfdata
	cs.LatencyMS = in.LatencyMS
	cs.ExecMS = in.ExecMS
	t := in.At
	cs.LastCheck = &t

	// Flapping evaluation (SPEC §6.3): weighted change rate over the
	// last 21 checks; newer transitions weigh more (0.8 + 0.4·i/20).
	if cfg.FlapDetection {
		pct := flapPercent(cs.FlapHistory)
		cs.FlapPct = pct
		switch {
		case !cs.Flapping && pct >= cfg.FlapThresholdHigh:
			cs.Flapping = true
			tr.FlapStarted = true
		case cs.Flapping && pct < cfg.FlapThresholdLow:
			cs.Flapping = false
			tr.FlapStopped = true
		}
	} else if cs.Flapping {
		cs.Flapping = false
		tr.FlapStopped = true
	}

	// A hard recovery clears a non-sticky… stickiness is enforced by the
	// pipeline; the machine only clears the ack on recovery.
	if tr.Recovered {
		cs.AckedBy = ""
		cs.AckComment = ""
	}
	return tr
}

const flapWindow = 21

func recordFlap(cs *model.CheckState, changed bool) {
	cs.FlapHistory <<= 1
	if changed {
		cs.FlapHistory |= 1
	}
	cs.FlapHistory &= (1 << flapWindow) - 1
}

// flapPercent computes the weighted state-change percentage: each of
// the last 21 checks contributes weight 0.8+0.4·(i/20) (i=0 oldest …
// 20 newest), normalised to 0–100.
func flapPercent(history uint32) float64 {
	var sum, max float64
	for i := 0; i < flapWindow; i++ {
		weight := 0.8 + 0.4*float64(i)/float64(flapWindow-1)
		max += weight
		// bit (flapWindow-1-i) is the i-th oldest
		if history&(1<<uint(flapWindow-1-i)) != 0 {
			sum += weight
		}
	}
	return 100 * sum / max
}

// Staleness builds the synthetic UNKNOWN input for stale passive checks
// (SPEC §6.3 freshness).
func Staleness(text string, at time.Time) Input {
	if text == "" {
		text = "UNKNOWN - check result is stale (freshness threshold exceeded)"
	}
	return Input{State: model.StateUnknown, Output: text, At: at}
}
