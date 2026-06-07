package statemachine

import (
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Table-driven over all Nagios transitions (SPEC §16).
func TestTransitions(t *testing.T) {
	type step struct {
		in         model.State
		wantState  model.State
		wantType   model.StateType
		wantAtt    int
		hardChange bool
		recovered  bool
	}
	cases := []struct {
		name  string
		maxAtt int
		steps []step
	}{
		{
			name: "ok stays ok", maxAtt: 3,
			steps: []step{
				{model.StateOK, model.StateOK, model.StateHard, 1, false, false},
				{model.StateOK, model.StateOK, model.StateHard, 1, false, false},
			},
		},
		{
			name: "classic soft to hard critical", maxAtt: 3,
			steps: []step{
				{model.StateCritical, model.StateCritical, model.StateSoft, 1, false, false},
				{model.StateCritical, model.StateCritical, model.StateSoft, 2, false, false},
				{model.StateCritical, model.StateCritical, model.StateHard, 3, true, false},
				{model.StateCritical, model.StateCritical, model.StateHard, 3, false, false},
			},
		},
		{
			name: "soft recovery never notifies", maxAtt: 3,
			steps: []step{
				{model.StateWarning, model.StateWarning, model.StateSoft, 1, false, false},
				{model.StateOK, model.StateOK, model.StateHard, 1, false, false}, // recovery from soft: no hard change event
			},
		},
		{
			name: "hard recovery notifies", maxAtt: 2,
			steps: []step{
				{model.StateCritical, model.StateCritical, model.StateSoft, 1, false, false},
				{model.StateCritical, model.StateCritical, model.StateHard, 2, true, false},
				{model.StateOK, model.StateOK, model.StateHard, 1, true, true}, // recovery always immediately hard
			},
		},
		{
			name: "severity change within soft keeps counting", maxAtt: 3,
			steps: []step{
				{model.StateWarning, model.StateWarning, model.StateSoft, 1, false, false},
				{model.StateCritical, model.StateCritical, model.StateSoft, 2, false, false},
				{model.StateCritical, model.StateCritical, model.StateHard, 3, true, false},
			},
		},
		{
			name: "hard severity change is immediate hard", maxAtt: 3,
			steps: []step{
				{model.StateCritical, model.StateCritical, model.StateSoft, 1, false, false},
				{model.StateCritical, model.StateCritical, model.StateSoft, 2, false, false},
				{model.StateCritical, model.StateCritical, model.StateHard, 3, true, false},
				{model.StateWarning, model.StateWarning, model.StateHard, 1, true, false},
			},
		},
		{
			name: "max attempts 1 is immediately hard", maxAtt: 1,
			steps: []step{
				{model.StateCritical, model.StateCritical, model.StateHard, 1, true, false},
				{model.StateOK, model.StateOK, model.StateHard, 1, true, true},
			},
		},
		{
			name: "unknown follows same soft hard rules", maxAtt: 2,
			steps: []step{
				{model.StateUnknown, model.StateUnknown, model.StateSoft, 1, false, false},
				{model.StateUnknown, model.StateUnknown, model.StateHard, 2, true, false},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cs := &model.CheckState{State: model.StateOK, StateType: model.StateHard, Attempt: 1}
			at := time.Unix(1750000000, 0)
			for i, st := range c.steps {
				at = at.Add(time.Minute)
				tr := Apply(cs, Config{MaxCheckAttempts: c.maxAtt}, Input{State: st.in, At: at, Output: "x"})
				if cs.State != st.wantState || cs.StateType != st.wantType || cs.Attempt != st.wantAtt {
					t.Fatalf("step %d: got %v/%s/%d want %v/%s/%d",
						i, cs.State, cs.StateType, cs.Attempt, st.wantState, st.wantType, st.wantAtt)
				}
				if tr.HardChange != st.hardChange {
					t.Fatalf("step %d: hardChange=%v want %v", i, tr.HardChange, st.hardChange)
				}
				if tr.Recovered != st.recovered {
					t.Fatalf("step %d: recovered=%v want %v", i, tr.Recovered, st.recovered)
				}
			}
		})
	}
}

func TestRecoveryClearsAck(t *testing.T) {
	cs := &model.CheckState{State: model.StateCritical, StateType: model.StateHard,
		Attempt: 3, AckedBy: "murat", AckComment: "looking"}
	tr := Apply(cs, Config{MaxCheckAttempts: 3}, Input{State: model.StateOK, At: time.Now()})
	if !tr.Recovered || cs.AckedBy != "" {
		t.Fatalf("ack must clear on recovery: %+v", cs)
	}
}

func TestFlappingDetection(t *testing.T) {
	cs := &model.CheckState{State: model.StateOK, StateType: model.StateHard, Attempt: 1}
	cfg := Config{MaxCheckAttempts: 1, FlapDetection: true}
	at := time.Unix(1750000000, 0)
	states := []model.State{model.StateOK, model.StateCritical} // alternate every check

	flapStarted := false
	for i := 0; i < 21; i++ {
		at = at.Add(time.Minute)
		tr := Apply(cs, cfg, Input{State: states[i%2], At: at})
		if tr.FlapStarted {
			flapStarted = true
		}
	}
	if !flapStarted || !cs.Flapping {
		t.Fatalf("constant alternation must flap: pct=%.1f", cs.FlapPct)
	}
	if cs.FlapPct < 90 {
		t.Fatalf("alternation should be ~100%%, got %.1f", cs.FlapPct)
	}
	// Stability brings it back down below the low threshold.
	flapStopped := false
	for i := 0; i < 21; i++ {
		at = at.Add(time.Minute)
		tr := Apply(cs, cfg, Input{State: model.StateOK, At: at})
		if tr.FlapStopped {
			flapStopped = true
		}
	}
	if !flapStopped || cs.Flapping {
		t.Fatalf("stable series must stop flapping: pct=%.1f", cs.FlapPct)
	}
}

func TestFlapWeightingFavorsRecent(t *testing.T) {
	// Changes only in the recent half must score higher than changes
	// only in the old half.
	var oldHalf, newHalf uint32
	for i := 0; i < 10; i++ {
		oldHalf |= 1 << uint(flapWindow-1-i) // oldest 10 changed
		newHalf |= 1 << uint(i)              // newest 10 changed
	}
	if flapPercent(newHalf) <= flapPercent(oldHalf) {
		t.Fatalf("recent changes must weigh more: new=%.1f old=%.1f",
			flapPercent(newHalf), flapPercent(oldHalf))
	}
}

func TestStaleness(t *testing.T) {
	cs := &model.CheckState{State: model.StateOK, StateType: model.StateHard, Attempt: 1}
	in := Staleness("", time.Now())
	Apply(cs, Config{MaxCheckAttempts: 1}, in)
	if cs.State != model.StateUnknown {
		t.Fatalf("stale → UNKNOWN, got %v", cs.State)
	}
}
