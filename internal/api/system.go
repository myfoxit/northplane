package api

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func (a *API) registerSystem() {
	// overview tiles (wallboard, SPEC §12.3)
	type overview struct {
		Summary   *storage.StateSummary    `json:"summary"`
		Alerts    map[model.Severity]int64 `json:"openAlerts"`
		Incidents []*model.Incident        `json:"openIncidents"`
		Bus       eventbus.Stats           `json:"queues"`
	}
	a.handle("GET /api/v1/overview", "Wallboard counters", "objects:read", nil, overview{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			sum, err := a.Store.Summary(r.Context(), tenant)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			alerts, _ := a.Store.OpenAlertStats(r.Context(), tenant)
			incidents, _ := a.Store.ListIncidents(r.Context(), tenant, true, "", 10)
			a.writeJSON(w, http.StatusOK, overview{
				Summary: sum, Alerts: alerts, Incidents: incidents, Bus: a.Bus.Stats()})
		})

	// health endpoints (SPEC §7.2: /readyz aggregates subsystems)
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	a.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		type sub struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
			Info string `json:"info,omitempty"`
		}
		var subs []sub
		dbOK := a.Store.DB().PingContext(r.Context()) == nil
		subs = append(subs, sub{"storage", dbOK, a.Store.Dialect().Name()})
		busStats := a.Bus.Stats()
		subs = append(subs, sub{"eventbus", busStats.ResultsDepth < 8000, ""})
		subs = append(subs, sub{"scheduler", true, ""})
		allOK := true
		for _, s := range subs {
			if !s.OK {
				allOK = false
			}
		}
		status := http.StatusOK
		if !allOK {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": allOK, "subsystems": subs})
	})

	a.handle("GET /api/v1/system/health", "Subsystem health", "", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			a.writeJSON(w, http.StatusOK, map[string]any{
				"queues":    a.Bus.Stats(),
				"scheduler": a.Sched.Stats(),
				"pipeline":  a.Pipe.Stats(),
				"alerting":  a.Alert.Stats(),
				"notify":    a.Notify.Stats(),
				"tsdb":      a.TSDB.Stats(),
				"catalog":   a.Catalog.Size(),
				"sse":       a.Hub.Clients(),
			})
		})

	a.handle("GET /api/v1/system/info", "Version and runtime info", "", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			a.writeJSON(w, http.StatusOK, map[string]any{
				"version":    a.Version,
				"goVersion":  runtime.Version(),
				"goroutines": runtime.NumGoroutine(),
				"heapMB":     mem.HeapAlloc / 1024 / 1024,
				"startedAt":  a.StartedAt.Format(time.RFC3339),
				"uptime":     time.Since(a.StartedAt).Round(time.Second).String(),
				"storage":    a.Store.Dialect().Name(),
				"aiEnabled":  a.AI != nil && a.AI.Enabled(),
			})
		})

	// OpenMetrics exposition (SPEC §15.4). Deliberately unauthenticated so
	// a Prometheus scraper needs no API credential — access is expected to
	// be restricted at the network/proxy layer (bind address, firewall, or
	// reverse-proxy allowlist), NOT by app auth. Do not add a perm check
	// here without coordinating scraper config; it would silently break
	// metric collection.
	a.mux.Handle("GET /metrics", a.Metrics.Handler())
	a.Metrics.Collect(func(set func(string, float64)) {
		bus := a.Bus.Stats()
		set("np_queue_results_depth", float64(bus.ResultsDepth))
		set("np_queue_events_depth", float64(bus.EventsDepth))
		set("np_queue_notifications_depth", float64(bus.NotifyDepth))
		set("np_sse_clients", float64(a.Hub.Clients()))
		sched := a.Sched.Stats()
		set("np_scheduler_objects", float64(sched.Scheduled))
		set("np_scheduler_lag_ms_max", float64(sched.MaxLagMS))
		set(`np_checks_dispatched_total`, float64(sched.Dispatched))
		pipe := a.Pipe.Stats()
		set("np_results_processed_total", float64(pipe.Processed))
		al := a.Alert.Stats()
		set("np_alert_rules", float64(al.Rules))
		set("np_alerts_opened_total", float64(al.Opened))
		nf := a.Notify.Stats()
		set(`np_notifications_total{result="sent"}`, float64(nf.Sent))
		set(`np_notifications_total{result="failed"}`, float64(nf.Failed))
		set(`np_notifications_total{result="dead"}`, float64(nf.Dead))
		set(`np_events_dropped_total{source="notify"}`, float64(nf.Dropped))
		ts := a.TSDB.Stats()
		set("np_tsdb_series", float64(ts.Series))
		set("np_tsdb_samples_total", float64(ts.Samples))
		set("np_tsdb_wal_bytes", float64(ts.WALBytes))
		set("np_catalog_objects", float64(a.Catalog.Size()))
	})
}
