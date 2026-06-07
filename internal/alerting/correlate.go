package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Correlator implements the deterministic correlation stages
// (SPEC §9.3): storm clustering of alerts sharing a dominant label
// within a window → one incident proposal. Stage 1 (topology) happens
// in the pipeline (reachability), stage 3 (flap) in the state machine.
type Correlator struct {
	store *storage.Store
	bus   *eventbus.Bus
	log   *slog.Logger

	Window    time.Duration // default 120 s (SPEC §9.3)
	Threshold int           // default ≥ 5

	mu     sync.Mutex
	window map[string][]openedAlert // tenant →
}

type openedAlert struct {
	alertID string
	at      time.Time
	labels  model.Labels
}

// NewCorrelator builds the correlator.
func NewCorrelator(store *storage.Store, bus *eventbus.Bus, log *slog.Logger) *Correlator {
	if log == nil {
		log = slog.Default()
	}
	return &Correlator{store: store, bus: bus, log: log,
		Window: 120 * time.Second, Threshold: 5, window: map[string][]openedAlert{}}
}

// Run consumes alert_opened events from a bus subscription.
func (c *Correlator) Run(ctx context.Context) {
	sub := c.bus.Subscribe(1024)
	defer sub.Close()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C:
			if e.Type != model.EventAlertOpened {
				continue
			}
			var payload struct {
				AlertID string       `json:"alertId"`
				Labels  model.Labels `json:"labels"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				continue
			}
			c.mu.Lock()
			c.window[e.TenantID] = append(c.window[e.TenantID],
				openedAlert{payload.AlertID, e.TS, payload.Labels})
			c.mu.Unlock()
		case <-ticker.C:
			c.sweep(ctx, time.Now().UTC())
		}
	}
}

// sweep trims the window and clusters what remains.
func (c *Correlator) sweep(ctx context.Context, now time.Time) {
	c.mu.Lock()
	clusters := map[string]map[string][]openedAlert{} // tenant → labelKV → alerts
	for tenant, list := range c.window {
		var keep []openedAlert
		for _, oa := range list {
			if now.Sub(oa.at) <= c.Window {
				keep = append(keep, oa)
			}
		}
		c.window[tenant] = keep
		// dominant label clustering: count label k=v pairs
		byKV := map[string][]openedAlert{}
		for _, oa := range keep {
			for k, v := range oa.labels {
				if k == "" || v == "" {
					continue
				}
				byKV[k+"="+v] = append(byKV[k+"="+v], oa)
			}
		}
		clusters[tenant] = byKV
	}
	c.mu.Unlock()

	for tenant, byKV := range clusters {
		// pick the dominant pair above threshold
		var bestKV string
		var bestList []openedAlert
		for kv, list := range byKV {
			if len(list) >= c.Threshold && len(list) > len(bestList) {
				bestKV, bestList = kv, list
			}
		}
		if bestKV == "" {
			continue
		}
		c.cluster(ctx, tenant, bestKV, bestList, now)
	}
}

func (c *Correlator) cluster(ctx context.Context, tenant, kv string, list []openedAlert, now time.Time) {
	// Skip alerts already in incidents; attach the rest.
	var fresh []string
	existingIncident := ""
	for _, oa := range list {
		a, err := c.store.GetAlert(ctx, tenant, oa.alertID)
		if err != nil {
			continue
		}
		if a.IncidentID != "" {
			existingIncident = a.IncidentID
			continue
		}
		if a.Status == model.AlertOpen || a.Status == model.AlertAcked {
			fresh = append(fresh, a.ID)
		}
	}
	if len(fresh) == 0 {
		return
	}
	incidentID := existingIncident
	if incidentID == "" {
		if len(fresh) < c.Threshold {
			return
		}
		inc := &model.Incident{
			TenantID: tenant, Status: model.IncidentOpen, Severity: model.SevCritical,
			Title:     fmt.Sprintf("Alarm storm: %d alerts sharing %s", len(fresh), kv),
			Impact:    kv,
			CreatedBy: "correlation",
		}
		if err := c.store.CreateIncident(ctx, inc); err != nil {
			c.log.Error("correlate: incident create", "err", err)
			return
		}
		incidentID = inc.ID
		raw, _ := json.Marshal(map[string]any{
			"incidentId": inc.ID, "title": inc.Title, "alerts": len(fresh), "cluster": kv})
		ev := &model.Event{ID: model.NewID(), TenantID: tenant, TS: now,
			Type: model.EventIncidentUpdate, Severity: model.SevCritical, Payload: raw}
		_ = c.store.InsertEvents(ctx, []*model.Event{ev})
		c.bus.FanoutOnly(ev)
		// AI naming/summary is additive, never required (SPEC §9.3/§10.5)
		c.bus.TryAI(eventbus.AIJob{Kind: "incident-summary", TenantID: tenant,
			IncidentID: inc.ID, AlertIDs: fresh})
	}
	sort.Strings(fresh)
	for _, id := range fresh {
		if err := c.store.AssignAlertIncident(ctx, tenant, id, incidentID); err != nil {
			c.log.Warn("correlate: assign", "alert", id, "err", err)
		}
	}
}
