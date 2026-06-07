package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/cel-go/cel"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Ingress adapters (SPEC §7.5): every source is an EventSource with its
// own auth, CEL normalisation mapping and rate limit; all adapters emit
// the same NormEvent shape.

// rate limiting per source (token bucket).
type bucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

var sourceBuckets sync.Map // sourceID → *bucket

func allowRate(sourceID string, rate float64, burst int) bool {
	if rate <= 0 {
		rate = 50 // default events/s per source
	}
	if burst <= 0 {
		burst = 200
	}
	v, _ := sourceBuckets.LoadOrStore(sourceID, &bucket{tokens: float64(burst), last: time.Now()})
	b := v.(*bucket)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * rate
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (a *API) registerIngress() {
	a.resourceCRUD("event-sources", storage.KindEventSource, "config", model.EventSource{})

	// Generic webhook ingest (SPEC §7.5): POST /api/v1/ingest/{source}.
	// Auth comes from the source definition (token/hmac/basic/none),
	// not the platform RBAC — registered raw.
	a.mux.HandleFunc("POST /api/v1/ingest/{source}", func(w http.ResponseWriter, r *http.Request) {
		a.handleIngest(w, r)
	})

	// Passive results (SPEC §8.5) — Nagios-compatible fields.
	type passiveResult struct {
		Host    string `json:"host"`
		Service string `json:"service,omitempty"`
		State   any    `json:"state"` // 0..3 or "OK"…
		Output  string `json:"output"`
	}
	type resultsRequest struct {
		Results []passiveResult `json:"results"`
	}
	type resultsResponse struct {
		Accepted int      `json:"accepted"`
		Rejected []string `json:"rejected,omitempty"`
	}
	a.handle("POST /api/v1/results", "Submit passive check results (batch)",
		"objects:write", resultsRequest{}, resultsResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req resultsRequest
			if !a.decode(w, r, &req) {
				return
			}
			tenant := a.tenantOf(r, p)
			resp := resultsResponse{}
			for _, res := range req.Results {
				stateStr := fmt.Sprint(res.State)
				state, err := model.ParseState(stateStr)
				if err != nil {
					resp.Rejected = append(resp.Rejected, res.Host+": "+err.Error())
					continue
				}
				kind, hostID, name := model.KindHost, "", res.Host
				if res.Service != "" {
					host := a.Catalog.GetByName(tenant, model.KindHost, "", res.Host)
					if host == nil {
						resp.Rejected = append(resp.Rejected, "unknown host "+res.Host)
						continue
					}
					kind, hostID, name = model.KindService, host.Object.ID, res.Service
				}
				entry := a.Catalog.GetByName(tenant, kind, hostID, name)
				if entry == nil {
					resp.Rejected = append(resp.Rejected, "unknown object "+res.Host+"/"+res.Service)
					continue
				}
				parsed := parsePassiveOutput(res.Output)
				select {
				case a.Bus.Results <- &model.CheckResult{
					ObjectID: entry.Object.ID, State: state,
					Output: parsed.Text, LongOutput: parsed.LongText, Perfdata: parsed.Perfdata,
					At: time.Now().UTC(), Source: "passive",
				}:
					resp.Accepted++
				case <-r.Context().Done():
					// pipeline stalled and client gave up — stop buffering
					resp.Rejected = append(resp.Rejected, "server busy")
					a.writeJSON(w, http.StatusServiceUnavailable, resp)
					return
				}
			}
			a.writeJSON(w, http.StatusAccepted, resp)
		})

	// Heartbeats (SPEC §7.5: dead-man inputs; GET allows curl-in-cron).
	a.handle("GET /api/v1/heartbeats", "List heartbeats", "objects:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			hbs, err := a.Store.ListHeartbeats(r.Context(), a.tenantOf(r, p))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, hbs, "")
		})

	type heartbeatBody struct {
		Name        string         `json:"name"`
		ExpectEvery model.Duration `json:"expectEvery"`
		Grace       model.Duration `json:"grace,omitempty"`
		Severity    model.Severity `json:"severity,omitempty"`
		Labels      model.Labels   `json:"labels,omitempty"`
	}
	a.handle("POST /api/v1/heartbeats", "Create/update heartbeat definition",
		"config:write", heartbeatBody{}, model.Heartbeat{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req heartbeatBody
			if !a.decode(w, r, &req) {
				return
			}
			if req.Name == "" || req.ExpectEvery <= 0 {
				a.validationError(w, r, "heartbeat", "name and expectEvery required")
				return
			}
			h := &model.Heartbeat{TenantID: a.tenantOf(r, p), Name: req.Name,
				ExpectEvery: req.ExpectEvery, Grace: req.Grace,
				Severity: req.Severity, Labels: req.Labels}
			if err := a.Store.PutHeartbeat(r.Context(), h); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "heartbeat.put", h.Name, nil, h)
			a.writeJSON(w, http.StatusCreated, h)
		})

	beat := func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		tenant := a.tenantOf(r, p)
		name := param(r, "name")
		h, recovered, err := a.Store.Beat(r.Context(), tenant, name, time.Now().UTC())
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if recovered {
			raw, _ := json.Marshal(map[string]any{
				"heartbeat": h.Name, "labels": h.Labels,
				"summary": "Heartbeat " + h.Name + " recovered", "resolve": true})
			ev := &model.Event{ID: model.NewID(), TenantID: tenant, TS: time.Now().UTC(),
				Type: model.EventHeartbeatMiss, SourceID: h.ID,
				Severity: model.SevOK, Payload: raw}
			_ = a.Store.InsertEvents(r.Context(), []*model.Event{ev})
			_ = a.Bus.PublishEventCtx(r.Context(), ev)
		}
		a.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
	a.handle("POST /api/v1/heartbeats/{name}/beat", "Record a heartbeat ping", "objects:write", nil, nil, beat)
	a.handle("GET /api/v1/heartbeats/{name}/beat", "Record a heartbeat ping (GET for cron)", "objects:write", nil, nil, beat)

	a.handle("DELETE /api/v1/heartbeats/{name}", "Delete heartbeat", "config:write", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if err := a.Store.DeleteHeartbeat(r.Context(), a.tenantOf(r, p), param(r, "name")); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "heartbeat.delete", param(r, "name"), nil, nil)
			w.WriteHeader(http.StatusNoContent)
		})

	// Prometheus Alertmanager compatibility (SPEC §7.5): receiver for
	// /api/v2/alerts so existing Prometheus stacks can forward.
	a.mux.HandleFunc("POST /api/v1/ingest/{source}/alertmanager", func(w http.ResponseWriter, r *http.Request) {
		a.handleAlertmanager(w, r)
	})
}

// parsePassiveOutput splits "text | perfdata" like active checks.
func parsePassiveOutput(out string) struct {
	Text, LongText, Perfdata string
} {
	parsed := struct{ Text, LongText, Perfdata string }{}
	lines := strings.SplitN(out, "\n", 2)
	first := lines[0]
	if i := strings.IndexByte(first, '|'); i >= 0 {
		parsed.Text = strings.TrimSpace(first[:i])
		parsed.Perfdata = strings.TrimSpace(first[i+1:])
	} else {
		parsed.Text = strings.TrimSpace(first)
	}
	if len(lines) > 1 {
		parsed.LongText = lines[1]
	}
	return parsed
}

// handleIngest authenticates against the EventSource and normalises the
// payload (identity or CEL mapping) into a NormEvent.
func (a *API) handleIngest(w http.ResponseWriter, r *http.Request) {
	sourceRef := param(r, "source")
	src, tenantID, err := a.findEventSource(r, sourceRef)
	if err != nil {
		a.problem(w, r, http.StatusNotFound, "np:ingress/unknown-source", "unknown event source", "")
		return
	}
	if !src.Enabled {
		a.problem(w, r, http.StatusForbidden, "np:ingress/disabled", "event source disabled", "")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		a.problem(w, r, http.StatusRequestEntityTooLarge, "np:ingress/size", "payload too large", "")
		return
	}
	if !a.ingressAuth(r, src, body) {
		a.problem(w, r, http.StatusUnauthorized, "np:ingress/auth", "ingress authentication failed", "")
		return
	}
	if !allowRate(src.ID, src.RateLimit, src.Burst) {
		a.Metrics.Counter(`np_ingress_dropped_total{reason="rate"}`, "Ingress drops").Inc()
		w.Header().Set("Retry-After", "5")
		a.problem(w, r, http.StatusTooManyRequests, "np:ingress/rate", "rate limit exceeded", "")
		return
	}

	norm, err := a.normalize(src, body)
	if err != nil {
		a.problem(w, r, http.StatusUnprocessableEntity, "np:ingress/mapping", "payload mapping failed", err.Error())
		return
	}
	norm.Source = src.ID
	norm.ReceivedAt = time.Now().UTC()
	norm.Labels = norm.Labels.Merge(src.Labels)

	a.publishNorm(r, tenantID, src, norm)
	a.Metrics.Counter(`np_ingress_events_total{type="webhook"}`, "Ingress events").Inc()
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) publishNorm(r *http.Request, tenantID string, src *model.EventSource, norm *model.NormEvent) {
	raw, _ := json.Marshal(norm)
	sev := norm.Severity
	if sev == "" {
		sev = model.SevInfo
	}
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: norm.ReceivedAt,
		Type: model.EventIngress, SourceID: src.ID, Severity: sev, Payload: raw}
	_ = a.Store.InsertEvents(r.Context(), []*model.Event{ev})
	_ = a.Bus.PublishEventCtx(r.Context(), ev)
}

// findEventSource resolves by id or name across tenants (ingest URLs
// carry no tenant context; source IDs are unique).
func (a *API) findEventSource(r *http.Request, ref string) (*model.EventSource, string, error) {
	tenants, err := a.Store.Tenants(r.Context())
	if err != nil {
		return nil, "", err
	}
	for _, t := range tenants {
		src, err := storage.LoadOne[model.EventSource](r.Context(), a.Store, t.ID,
			storage.KindEventSource, ref)
		if err == nil {
			return src, t.ID, nil
		}
	}
	return nil, "", storage.ErrNotFound
}

// ingressAuth enforces the source's auth mode.
func (a *API) ingressAuth(r *http.Request, src *model.EventSource, body []byte) bool {
	secret := ""
	if src.SecretRef != "" && a.Box != nil {
		blob, err := a.Store.GetSecret(r.Context(), src.TenantID, src.SecretRef)
		if err == nil {
			secret, _ = a.Box.Open(blob)
		}
	}
	switch src.AuthMode {
	case "none":
		return true
	case "hmac":
		sig := r.Header.Get("X-Northplane-Signature")
		if sig == "" {
			sig = r.Header.Get("X-Hub-Signature-256")
		}
		sig = strings.TrimPrefix(sig, "sha256=")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		want := hex.EncodeToString(mac.Sum(nil))
		return secret != "" && hmac.Equal([]byte(want), []byte(sig))
	case "basic":
		_, pass, ok := r.BasicAuth()
		// constant-time compare (the hmac/token branches already do)
		return ok && secret != "" && hmac.Equal([]byte(pass), []byte(secret))
	default: // token
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		return secret != "" && hmac.Equal([]byte(tok), []byte(secret))
	}
}

// normalize applies the CEL mapping (SPEC §9.2: Extraktion aus Events)
// or accepts payloads already in normal form.
func (a *API) normalize(src *model.EventSource, body []byte) (*model.NormEvent, error) {
	var norm model.NormEvent
	if len(src.Mapping) == 0 {
		if err := json.Unmarshal(body, &norm); err != nil {
			return nil, fmt.Errorf("payload is not normal-form JSON and source has no mapping: %w", err)
		}
		if norm.Summary == "" {
			norm.Summary = "event from " + src.Name
		}
		norm.Payload = body
		return &norm, nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("payload must be JSON: %w", err)
	}
	get := func(expr string) (any, error) {
		env, err := cel.NewEnv(cel.Variable("payload", cel.DynType))
		if err != nil {
			return nil, err
		}
		ast, iss := env.Compile(expr)
		if iss.Err() != nil {
			return nil, iss.Err()
		}
		prg, err := env.Program(ast, cel.CostLimit(5000))
		if err != nil {
			return nil, err
		}
		out, _, err := prg.Eval(map[string]any{"payload": payload})
		if err != nil {
			return nil, err
		}
		return out.Value(), nil
	}
	norm.Payload = body
	for field, expr := range src.Mapping {
		v, err := get(expr)
		if err != nil {
			return nil, fmt.Errorf("mapping %s: %w", field, err)
		}
		switch field {
		case "summary":
			norm.Summary = fmt.Sprint(v)
		case "severity":
			norm.Severity = model.Severity(fmt.Sprint(v))
		case "dedupKey":
			norm.DedupKey = fmt.Sprint(v)
		case "resolve":
			b, _ := v.(bool)
			norm.Resolve = b
		default:
			if labelKey, ok := strings.CutPrefix(field, "labels."); ok {
				if norm.Labels == nil {
					norm.Labels = model.Labels{}
				}
				norm.Labels[labelKey] = fmt.Sprint(v)
			}
		}
	}
	if !norm.Severity.Valid() {
		norm.Severity = model.SevInfo
	}
	return &norm, nil
}

// handleAlertmanager accepts Prometheus Alertmanager v2 webhook payloads.
func (a *API) handleAlertmanager(w http.ResponseWriter, r *http.Request) {
	sourceRef := param(r, "source")
	src, tenantID, err := a.findEventSource(r, sourceRef)
	if err != nil {
		a.problem(w, r, http.StatusNotFound, "np:ingress/unknown-source", "unknown event source", "")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || !a.ingressAuth(r, src, body) {
		a.problem(w, r, http.StatusUnauthorized, "np:ingress/auth", "ingress authentication failed", "")
		return
	}
	// Alertmanager webhook format {alerts:[{status,labels,annotations,…}]}
	var payload struct {
		Alerts []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			Fingerprint string            `json:"fingerprint"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		a.problem(w, r, http.StatusUnprocessableEntity, "np:ingress/format", "not an Alertmanager payload", err.Error())
		return
	}
	for _, am := range payload.Alerts {
		if !allowRate(src.ID, src.RateLimit, src.Burst) {
			break
		}
		sev := model.SevWarning
		switch am.Labels["severity"] {
		case "critical", "page":
			sev = model.SevCritical
		case "info", "none":
			sev = model.SevInfo
		}
		summary := am.Annotations["summary"]
		if summary == "" {
			summary = am.Labels["alertname"]
		}
		norm := &model.NormEvent{
			Source: src.ID, ReceivedAt: time.Now().UTC(),
			DedupKey: "am-" + am.Fingerprint, Severity: sev,
			Summary: summary, Labels: model.Labels(am.Labels).Merge(src.Labels),
			Resolve: am.Status == "resolved",
		}
		norm.Payload, _ = json.Marshal(am)
		if norm.Resolve {
			norm.Severity = model.SevOK
		}
		a.publishNorm(r, tenantID, src, norm)
	}
	a.Metrics.Counter(`np_ingress_events_total{type="alertmanager"}`, "Ingress events").Inc()
	w.WriteHeader(http.StatusAccepted)
}
