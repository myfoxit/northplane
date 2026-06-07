package api

import (
	"context"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
)

// RefreshDowntimeDepths recomputes check_state.downtime_depth for all
// objects of a tenant from the active downtime windows (fixed windows +
// triggered flexible ones; selector downtimes expand via the catalog).
func RefreshDowntimeDepths(ctx context.Context, a *API, tenantID string) {
	now := time.Now().UTC()
	downtimes, err := a.Store.ListDowntimes(ctx, tenantID, true)
	if err != nil {
		a.Log.Error("janitor: downtimes", "err", err)
		return
	}
	depth := map[string]int{}
	for _, d := range downtimes {
		if !d.ActiveAt(now) {
			continue
		}
		switch {
		case d.ObjectID != "":
			depth[d.ObjectID]++
			// host downtime cascades to its services (trigger chain)
			for _, childID := range a.Catalog.Children(d.ObjectID) {
				depth[childID]++
			}
		case d.Selector != "":
			sel, err := selector.Parse(d.Selector)
			if err != nil {
				continue
			}
			for _, e := range a.Catalog.Select(tenantID, sel) {
				depth[e.Object.ID]++
			}
		}
	}
	// write deltas only, column-scoped (must not clobber pipeline-owned
	// result columns nor acks)
	deltas := map[string]int{}
	for _, e := range a.Catalog.All() {
		if e.Object.TenantID != tenantID {
			continue
		}
		want := depth[e.Object.ID]
		cs, err := a.Store.GetCheckState(ctx, e.Object.ID)
		if err != nil {
			continue
		}
		if cs.DowntimeDepth != want {
			deltas[e.Object.ID] = want
		}
	}
	if len(deltas) > 0 {
		if err := a.Store.SetDowntimeDepths(ctx, deltas); err != nil {
			a.Log.Error("janitor: depth save", "err", err)
		}
	}
}

// Janitor runs periodic maintenance: downtime depths, flexible-downtime
// triggers, expired sessions/idempotency rows, event retention and TSDB
// maintenance (the "planbare Batch-Jobs", A-15.23).
func (a *API) Janitor(ctx context.Context) {
	depthTicker := time.NewTicker(30 * time.Second)
	cleanupTicker := time.NewTicker(10 * time.Minute)
	nightlyTicker := time.NewTicker(time.Hour)
	defer depthTicker.Stop()
	defer cleanupTicker.Stop()
	defer nightlyTicker.Stop()
	var lastNightly time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-depthTicker.C:
			tenants, err := a.Store.Tenants(ctx)
			if err != nil {
				continue
			}
			for _, t := range tenants {
				RefreshDowntimeDepths(ctx, a, t.ID)
				a.triggerFlexDowntimes(ctx, t.ID)
			}
		case <-cleanupTicker.C:
			if err := a.Store.CleanupExpired(ctx); err != nil {
				a.Log.Error("janitor: cleanup", "err", err)
			}
		case <-nightlyTicker.C:
			// run heavy compaction once per night (02:00–03:59 local)
			now := time.Now()
			if now.Hour() >= 2 && now.Hour() < 4 && now.Sub(lastNightly) > 20*time.Hour {
				lastNightly = now
				if a.TSDB != nil {
					if err := a.TSDB.Maintain(ctx, now); err != nil {
						a.Log.Error("janitor: tsdb maintain", "err", err)
					}
				}
				if dropped, err := a.Store.EnforceEventRetention(ctx); err != nil {
					a.Log.Error("janitor: event retention", "err", err)
				} else if len(dropped) > 0 {
					a.Log.Info("janitor: event segments dropped", "segments", dropped)
				}
			} else if a.TSDB != nil {
				// hourly: flush closed TSDB windows (cheap)
				if err := a.TSDB.Flush(now); err != nil {
					a.Log.Error("janitor: tsdb flush", "err", err)
				}
			}
		}
	}
}

// triggerFlexDowntimes starts flexible downtimes when an object inside
// the window enters a problem state (SPEC §6.3).
func (a *API) triggerFlexDowntimes(ctx context.Context, tenantID string) {
	downtimes, err := a.Store.ListDowntimes(ctx, tenantID, true)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, d := range downtimes {
		if d.Type != model.DowntimeFlexible || d.StartedAt != nil {
			continue
		}
		if now.Before(d.Start) || now.After(d.End) {
			continue
		}
		problem := false
		if d.ObjectID != "" {
			if cs, err := a.Store.GetCheckState(ctx, d.ObjectID); err == nil && cs.State != model.StateOK {
				problem = true
			}
		} else if d.Selector != "" {
			if sel, err := selector.Parse(d.Selector); err == nil {
				for _, e := range a.Catalog.Select(tenantID, sel) {
					if cs, err := a.Store.GetCheckState(ctx, e.Object.ID); err == nil && cs.State != model.StateOK {
						problem = true
						break
					}
				}
			}
		}
		if problem {
			_ = a.Store.MarkFlexDowntimeStarted(ctx, d.ID, now)
		}
	}
}
