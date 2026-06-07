// Package pipeline is the result path (SPEC §7.4):
//
//	Result → StateMachine → (state_change? → Event) → TSDB-Ingest → SSE-Fanout
//
// with batch commits every 250 ms or 500 results. It owns check_state
// (in-memory working set, persisted in batches), applies reachability
// (SPEC §6.3) and freshness, and feeds perfdata into the NP-TSDB.
package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
	"github.com/northplane/northplane/internal/scheduler"
	"github.com/northplane/northplane/internal/statemachine"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tsdb"
)

const (
	batchInterval = 250 * time.Millisecond
	batchSize     = 500
)

// Pipeline consumes bus.Results.
type Pipeline struct {
	store *storage.Store
	cat   *catalog.Catalog
	bus   *eventbus.Bus
	ts    *tsdb.DB
	sched *scheduler.Scheduler
	log   *slog.Logger

	mu            sync.Mutex
	states        map[string]*model.CheckState // working set
	dirty         map[string]bool
	eventsPending []*model.Event

	statProcessed uint64
}

// New builds the pipeline.
func New(store *storage.Store, cat *catalog.Catalog, bus *eventbus.Bus,
	ts *tsdb.DB, sched *scheduler.Scheduler, log *slog.Logger) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{
		store: store, cat: cat, bus: bus, ts: ts, sched: sched, log: log,
		states: map[string]*model.CheckState{}, dirty: map[string]bool{},
	}
}

// Run processes results until ctx ends.
func (p *Pipeline) Run(ctx context.Context) {
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()
	pending := 0
	for {
		select {
		case <-ctx.Done():
			p.flush(context.Background())
			return
		case res := <-p.bus.Results:
			p.process(ctx, res)
			pending++
			if pending >= batchSize {
				p.flush(ctx)
				pending = 0
			}
		case <-ticker.C:
			if pending > 0 || len(p.dirty) > 0 {
				p.flush(ctx)
				pending = 0
			}
		}
	}
}

// state returns the working-set row, loading it once from storage.
func (p *Pipeline) state(ctx context.Context, objectID string) *model.CheckState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cs, ok := p.states[objectID]; ok {
		return cs
	}
	cs, err := p.store.GetCheckState(ctx, objectID)
	if err != nil {
		cs = &model.CheckState{ObjectID: objectID, State: model.StateUnknown,
			StateType: model.StateHard, Attempt: 1}
	}
	p.states[objectID] = cs
	return cs
}

// Forget drops an object from the working set (deletion hook).
func (p *Pipeline) Forget(objectID string) {
	p.mu.Lock()
	delete(p.states, objectID)
	delete(p.dirty, objectID)
	p.mu.Unlock()
}

func (p *Pipeline) process(ctx context.Context, res *model.CheckResult) {
	entry := p.cat.Get(res.ObjectID)
	if entry == nil && res.ObjectID == "" && res.Host != "" {
		// passive submission by name (SPEC §8.5)
		kind := model.KindHost
		hostID := ""
		name := res.Host
		if res.Service != "" {
			host := p.cat.GetByName(model.DefaultTenant, model.KindHost, "", res.Host)
			if host == nil {
				return
			}
			kind, hostID, name = model.KindService, host.Object.ID, res.Service
		}
		entry = p.cat.GetByName(model.DefaultTenant, kind, hostID, name)
	}
	if entry == nil {
		return
	}
	obj := entry.Object
	eff := entry.Effective
	cs := p.state(ctx, obj.ID)
	now := res.At
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// Freshness probe marker: only act when actually stale (SPEC §6.3).
	if res.Source == "freshness" {
		if eff.StalenessAfter <= 0 {
			return
		}
		if cs.LastCheck != nil && now.Sub(*cs.LastCheck) < eff.StalenessAfter.D() {
			return // fresh enough
		}
		in := statemachine.Staleness(eff.StalenessText, now)
		res.State, res.Output = in.State, in.Output
	}

	// Host result mapping: WARNING counts as UP (classic Nagios default).
	inState := res.State
	if obj.Kind == model.KindHost {
		switch res.State {
		case model.StateOK, model.StateWarning:
			inState = model.HostUp
		default:
			inState = model.HostDown
		}
		// Reachability: a DOWN host whose parents are all non-UP is
		// UNREACHABLE (SPEC §6.3).
		if inState == model.HostDown && p.allParentsDown(ctx, obj.ID) {
			inState = model.HostUnreachable
		}
	}

	cfg := statemachine.Config{
		MaxCheckAttempts:  eff.MaxCheckAttempts,
		FlapDetection:     eff.EnableFlapDetection == nil || *eff.EnableFlapDetection,
		FlapThresholdLow:  eff.FlapThresholdLow,
		FlapThresholdHigh: eff.FlapThresholdHigh,
	}
	prevState := cs.State
	tr := statemachine.Apply(cs, cfg, statemachine.Input{
		State: inState, Output: res.Output, LongOutput: res.LongOutput,
		Perfdata: res.Perfdata, At: now, LatencyMS: res.LatencyMS, ExecMS: res.ExecMS,
	})
	if next, ok := p.sched.NextDue(obj.ID); ok {
		cs.NextCheck = &next
	}

	p.mu.Lock()
	p.dirty[obj.ID] = true
	p.statProcessed++
	p.mu.Unlock()

	// Retry cadence: soft problems reschedule at retryInterval
	// (SPEC §6.3). The wheel runs at interval; fire a quicker recheck.
	if tr.IsSoft && eff.RetryInterval > 0 && res.Source == "scheduler" {
		go func(id string, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
				p.sched.CheckNow(id)
			}
		}(obj.ID, eff.RetryInterval.D())
	}

	// Events (SPEC §6.4): every state change, plus flapping edges.
	if tr.StateChanged || tr.HardChange {
		payload := model.StateChangePayload{
			ObjectName: obj.Name, Kind: obj.Kind,
			From: prevState, To: cs.State,
			FromLabel: prevState.Label(obj.Kind), ToLabel: cs.State.Label(obj.Kind),
			StateType: cs.StateType, Attempt: cs.Attempt,
			Output: res.Output, Labels: obj.Labels,
		}
		if entry.Host != nil {
			payload.HostName = entry.Host.Object.Name
		}
		if perfs, _ := nagios.ParsePerfdata(res.Perfdata); len(perfs) > 0 {
			payload.Metric = perfs[0].Label
		}
		raw, _ := json.Marshal(payload)
		sev := model.SeverityFromState(cs.State, obj.Kind)
		if cs.State == model.HostUnreachable && obj.Kind == model.KindHost {
			sev = model.SevWarning // separate, default-quiet handling (SPEC §6.3)
		}
		p.emitEvent(&model.Event{
			ID: model.NewID(), TenantID: obj.TenantID, TS: now,
			Type: model.EventStateChange, ObjectID: obj.ID,
			Severity: sev, Payload: raw,
		})
	}
	if tr.FlapStarted || tr.FlapStopped {
		typ := model.EventFlappingStart
		if tr.FlapStopped {
			typ = model.EventFlappingEnd
		}
		raw, _ := json.Marshal(map[string]any{
			"object": obj.Name, "flapPct": cs.FlapPct, "labels": obj.Labels})
		p.emitEvent(&model.Event{
			ID: model.NewID(), TenantID: obj.TenantID, TS: now,
			Type: typ, ObjectID: obj.ID, Severity: model.SevInfo, Payload: raw,
		})
	}

	// Reachability cascade: a hard host transition triggers immediate
	// rechecks of dependent hosts so they flip DOWN/UNREACHABLE quickly.
	if obj.Kind == model.KindHost && tr.HardChange {
		for _, childID := range p.cat.Children(obj.ID) {
			_ = childID // services of this host inherit via UI; no recheck needed
		}
		for _, e := range p.cat.All() {
			if e.Object.Kind != model.KindHost {
				continue
			}
			for _, parentID := range p.cat.Parents(e.Object.ID) {
				if parentID == obj.ID {
					p.sched.CheckNow(e.Object.ID)
				}
			}
		}
	}

	// TSDB ingest (SPEC §8.3): perfdata series + executor meta-metrics.
	if p.ts != nil {
		perfs, _ := nagios.ParsePerfdata(res.Perfdata)
		for _, perf := range perfs {
			unit := perf.NormUnit
			var minP, maxP *float64
			if perf.Min != nil {
				minP = perf.Min
			}
			if perf.Max != nil {
				maxP = perf.Max
			}
			p.ts.Append(obj.ID, perf.Label, unit, nil, perf.Warn, perf.Crit,
				minP, maxP, now, perf.NormValue)
		}
		if res.ExecMS > 0 {
			p.ts.Append(obj.ID, "np_exec_time", "s", nil, "", "", nil, nil,
				now, float64(res.ExecMS)/1000)
		}
	}
}

// allParentsDown: ≥1 parent and none of them UP.
func (p *Pipeline) allParentsDown(ctx context.Context, hostID string) bool {
	parents := p.cat.Parents(hostID)
	if len(parents) == 0 {
		return false
	}
	for _, pid := range parents {
		ps := p.state(ctx, pid)
		if ps.State == model.HostUp || ps.StateType == model.StateSoft {
			return false
		}
	}
	return true
}

func (p *Pipeline) emitEvent(e *model.Event) {
	// persist + alerting + SSE; persistence batched with state flush
	p.mu.Lock()
	p.eventsPending = append(p.eventsPending, e)
	p.mu.Unlock()
	p.bus.PublishEvent(e)
}

func (p *Pipeline) flush(ctx context.Context) {
	p.mu.Lock()
	var states []*model.CheckState
	for id := range p.dirty {
		if cs := p.states[id]; cs != nil {
			states = append(states, cs)
		}
	}
	p.dirty = map[string]bool{}
	events := p.eventsPending
	p.eventsPending = nil
	p.mu.Unlock()

	if len(states) > 0 {
		if err := p.store.SaveCheckStates(ctx, states); err != nil {
			p.log.Error("pipeline: state flush failed", "err", err, "n", len(states))
		}
	}
	if len(events) > 0 {
		if err := p.store.InsertEvents(ctx, events); err != nil {
			p.log.Error("pipeline: event flush failed", "err", err, "n", len(events))
		}
	}
}

// Stats snapshot.
type Stats struct {
	Processed uint64 `json:"processed"`
	WorkingSet int   `json:"workingSet"`
}

// Stats for self-metrics.
func (p *Pipeline) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Processed: p.statProcessed, WorkingSet: len(p.states)}
}
