// Package api implements the public REST API (SPEC §11): the only
// functional surface of the system (P1 — the UI, CLI, Terraform, MCP
// and AI layers are all clients of these routes). Routes register with
// metadata so the OpenAPI 3.1 document is generated from code (ADR-10).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/alerting"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/escalation"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/metrics"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/notify"
	"github.com/northplane/northplane/internal/pipeline"
	"github.com/northplane/northplane/internal/scheduler"
	"github.com/northplane/northplane/internal/sse"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tsdb"
)

// API aggregates dependencies and the route table.
type API struct {
	Cfg     config.Config
	Store   *storage.Store
	Catalog *catalog.Catalog
	Bus     *eventbus.Bus
	TSDB    *tsdb.DB
	Sched   *scheduler.Scheduler
	Pipe    *pipeline.Pipeline
	Alert   *alerting.Engine
	Escal   *escalation.Engine
	Notify  *notify.Manager
	Auth    *auth.Authenticator
	OIDC    *auth.OIDC
	Box     *auth.SecretBox
	Hub     *sse.Hub
	Metrics *metrics.Registry
	Log     *slog.Logger
	// AI is wired by the ai package (interface keeps api independent).
	AI AIService

	StartedAt time.Time
	Version   string

	mux     *http.ServeMux
	routes  []routeMeta
	actions map[string]*actionRouter // "METHOD parentPath" →
}

// actionRouter dispatches `/{wildcard}` segments that may carry a
// `:action` suffix (SPEC §11.3 URL style: POST /alerts/{id}:ack).
// Go's ServeMux forbids text after a wildcard, so the last segment is
// matched as one wildcard and split here.
type actionRouter struct {
	paramName string // wildcard name of the plain route
	plain     func(http.ResponseWriter, *http.Request)
	bySuffix  map[string]actionEntry
}

type actionEntry struct {
	paramName string
	handler   func(http.ResponseWriter, *http.Request)
}

func (ar *actionRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	seg := r.PathValue("__seg")
	if i := strings.LastIndexByte(seg, ':'); i >= 0 {
		if entry, ok := ar.bySuffix[seg[i:]]; ok {
			r.SetPathValue(entry.paramName, seg[:i])
			entry.handler(w, r)
			return
		}
	}
	if ar.plain != nil {
		r.SetPathValue(ar.paramName, seg)
		ar.plain(w, r)
		return
	}
	http.NotFound(w, r)
}

// AIService is what the AI subsystem exposes to the API layer.
type AIService interface {
	Enabled() bool
	Converse(ctx context.Context, principal *auth.Principal, conversationID, message string) (any, error)
	SummarizeIncident(ctx context.Context, tenantID, incidentID string) (string, error)
	ExecuteApproved(ctx context.Context, tenantID, actionID string, approver *auth.Principal) (any, error)
}

type routeMeta struct {
	Method  string
	Pattern string // ServeMux pattern without method
	Summary string
	Perm    model.Permission
	Tag     string
	Req     reflect.Type
	Resp    reflect.Type
}

// New assembles the router.
func New(a *API) http.Handler {
	a.mux = http.NewServeMux()
	a.registerAll()
	return a.withMiddleware(a.mux)
}

// handle registers a route with metadata (OpenAPI source of truth).
// Pattern uses ServeMux syntax plus the `:action` extension:
// "POST /api/v1/alerts/{id}:ack" routes through an actionRouter.
func (a *API) handle(pattern, summary string, perm model.Permission, req, resp any,
	h func(w http.ResponseWriter, r *http.Request, p *auth.Principal)) {
	method, path, _ := strings.Cut(pattern, " ")
	meta := routeMeta{Method: method, Pattern: path, Summary: summary,
		Perm: perm, Tag: tagOf(path)}
	if req != nil {
		meta.Req = reflect.TypeOf(req)
	}
	if resp != nil {
		meta.Resp = reflect.TypeOf(resp)
	}
	a.routes = append(a.routes, meta)

	wrapped := func(w http.ResponseWriter, r *http.Request) {
		p := auth.From(r.Context())
		// CSRF defense (SPEC §13.2): a session-cookie-authenticated request
		// the browser marks cross-site cannot be a legitimate first-party
		// call — the SPA is same-origin. This closes the SameSite=Lax gap
		// where a top-level GET navigation (e.g. the cron beat endpoint) or
		// form POST carries the ambient cookie. Token-authenticated clients
		// carry no ambient credential and are unaffected.
		if p != nil && p.SessionID != "" &&
			strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
			a.problem(w, r, http.StatusForbidden, "np:auth/csrf",
				"cross-site request blocked", "")
			return
		}
		if perm != "" {
			if p == nil {
				a.problem(w, r, http.StatusUnauthorized, "np:auth/required",
					"authentication required", "")
				return
			}
			if !p.Allow(perm) {
				a.problem(w, r, http.StatusForbidden, "np:auth/forbidden",
					"missing permission", string(perm))
				return
			}
		}
		h(w, r, p)
	}

	// Routes whose final segment is a wildcard (optionally with a
	// `:action` suffix) share one ServeMux pattern per (method, parent).
	parent, last := splitLastSegment(path)
	if strings.HasPrefix(last, "{") {
		paramName, suffix := parseWildcardSegment(last)
		if a.actions == nil {
			a.actions = map[string]*actionRouter{}
		}
		key := method + " " + parent
		ar := a.actions[key]
		if ar == nil {
			ar = &actionRouter{bySuffix: map[string]actionEntry{}}
			a.actions[key] = ar
			a.mux.HandleFunc(method+" "+parent+"/{__seg}", ar.ServeHTTP)
		}
		if suffix == "" {
			ar.paramName, ar.plain = paramName, wrapped
		} else {
			ar.bySuffix[suffix] = actionEntry{paramName: paramName, handler: wrapped}
		}
		return
	}
	a.mux.HandleFunc(pattern, wrapped)
}

func splitLastSegment(path string) (parent, last string) {
	i := strings.LastIndexByte(path, '/')
	return path[:i], path[i+1:]
}

// parseWildcardSegment splits "{id}:ack" → ("id", ":ack"), "{name}" →
// ("name", "").
func parseWildcardSegment(seg string) (param, suffix string) {
	end := strings.IndexByte(seg, '}')
	if end < 0 {
		return seg, ""
	}
	return seg[1:end], seg[end+1:]
}

func tagOf(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) > 0 && parts[0] != "" {
		return parts[0]
	}
	return "system"
}

// --- middleware ---

func (a *API) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := model.NewID()
		w.Header().Set("X-Request-Id", reqID)
		ctx := context.WithValue(r.Context(), ctxRequestID, reqID)

		defer func() {
			if rec := recover(); rec != nil {
				a.Log.Error("api: panic", "err", rec, "path", r.URL.Path)
				a.problem(w, r, http.StatusInternalServerError, "np:internal",
					"internal error", "")
			}
		}()

		principal, err := a.Auth.Authenticate(r)
		if err != nil {
			a.problem(w, r, http.StatusUnauthorized, "np:auth/invalid", "invalid credentials", err.Error())
			return
		}
		if principal != nil {
			ctx = auth.WithPrincipal(ctx, principal)
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		routed := r.WithContext(ctx)
		next.ServeHTTP(rec, routed)

		if strings.HasPrefix(r.URL.Path, "/api/") {
			// Label by method + status code + matched route template
			// (routed.Pattern, set by ServeMux — bounded cardinality, never
			// the raw path). Both the request counter and the latency
			// histogram carry the same label set so they correlate.
			labels := fmt.Sprintf(`{method=%q,status=%q,route=%q}`,
				r.Method, strconv.Itoa(rec.status), routeLabel(routed))
			a.Metrics.Counter("np_http_requests_total"+labels, "API requests").Inc()
			a.Metrics.Histogram("np_http_request_duration_seconds"+labels,
				"API request latency in seconds").Observe(time.Since(start).Seconds())
		}
	})
}

// routeLabel is the matched ServeMux pattern (method-stripped) for the
// request — a low-cardinality template like "/api/v1/objects/{id}", not
// the raw path. Empty when nothing matched (404).
func routeLabel(r *http.Request) string {
	pat := r.Pattern
	if pat == "" {
		return ""
	}
	if _, path, ok := strings.Cut(pat, " "); ok { // strip leading "METHOD "
		return path
	}
	return pat
}

// statusRecorder captures the response status code (and whether the
// body was started) for request metrics while transparently forwarding
// to the underlying writer. It preserves http.Flusher so SSE streaming
// (internal/sse) keeps working through the middleware.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.written = true // implicit 200 on first write without WriteHeader
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the wrapped writer so SSE/streaming handlers, which
// type-assert http.Flusher, continue to flush.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type ctxKeyType int

const ctxRequestID ctxKeyType = 1

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(ctxRequestID).(string)
	return id
}

// --- responses (RFC 9457 problem details, SPEC §11.1) ---

type problemDoc struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Code     string `json:"code"`
	Instance string `json:"instance,omitempty"`
}

func (a *API) problem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemDoc{
		Type:  "https://northplane.dev/problems/" + strings.ReplaceAll(code, ":", "/"),
		Title: title, Status: status, Detail: detail, Code: code,
		Instance: r.URL.Path,
	})
}

// fail maps storage errors onto HTTP semantics.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve validationErr
	switch {
	case errors.Is(err, storage.ErrNotFound):
		a.problem(w, r, http.StatusNotFound, "np:not-found", "resource not found", "")
	case errors.Is(err, storage.ErrConflict):
		a.problem(w, r, http.StatusConflict, "np:conflict/version", "version conflict", err.Error())
	case errors.Is(err, storage.ErrDuplicate):
		a.problem(w, r, http.StatusConflict, "np:conflict/duplicate", "already exists", err.Error())
	case errors.As(err, &ve):
		a.problem(w, r, http.StatusUnprocessableEntity, "np:validation/"+ve.code,
			"validation failed", ve.detail)
	default:
		a.Log.Error("api: handler error", "err", err, "path", r.URL.Path)
		a.problem(w, r, http.StatusInternalServerError, "np:internal", "internal error", "")
	}
}

func (a *API) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// listResponse is the cursor-pagination envelope (SPEC §11.1).
type listResponse struct {
	Items      any    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func (a *API) writeList(w http.ResponseWriter, items any, nextCursor string) {
	a.writeJSON(w, http.StatusOK, listResponse{Items: items, NextCursor: nextCursor})
}

// decode reads a JSON body (1 MB cap; strict content type).
func (a *API) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		a.problem(w, r, http.StatusUnprocessableEntity, "np:validation/body",
			"invalid request body", err.Error())
		return false
	}
	return true
}

// validationError emits 422 with a machine-readable code.
func (a *API) validationError(w http.ResponseWriter, r *http.Request, code, detail string) {
	a.problem(w, r, http.StatusUnprocessableEntity, "np:validation/"+code,
		"validation failed", detail)
}

// queryInt parses an integer query param.
func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ifMatchVersion parses If-Match: "v" → optimistic-lock version
// (SPEC §11.1: mutations require If-Match; 0 = header absent).
func ifMatchVersion(r *http.Request) int64 {
	h := strings.Trim(r.Header.Get("If-Match"), `W/" `)
	if h == "" {
		return 0
	}
	v, _ := strconv.ParseInt(h, 10, 64)
	return v
}

func etag(w http.ResponseWriter, version int64) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(version, 10)+`"`)
}

// requireIfMatch enforces the lost-update guard for mutating handlers.
func (a *API) requireIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := ifMatchVersion(r)
	if v <= 0 {
		a.problem(w, r, http.StatusPreconditionRequired, "np:precondition/if-match",
			"If-Match header with the current version is required", "")
		return 0, false
	}
	return v, true
}

// idempotent wraps POST handlers honouring Idempotency-Key (SPEC §11.1).
func (a *API) idempotent(w http.ResponseWriter, r *http.Request, p *auth.Principal,
	fn func(body []byte) (int, any, error)) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		a.problem(w, r, http.StatusRequestEntityTooLarge, "np:validation/size", "body too large", "")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		status, stored, found, err := a.Store.IdempotencyCheck(r.Context(), p.TenantID, key, body)
		if err != nil {
			a.problem(w, r, http.StatusConflict, "np:conflict/idempotency",
				"idempotency key reused with different body", err.Error())
			return
		}
		if found {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotency-Replayed", "true")
			w.WriteHeader(status)
			_, _ = w.Write(stored)
			return
		}
	}
	status, resp, err := fn(body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	respBody, _ := json.Marshal(resp)
	if key != "" {
		_ = a.Store.IdempotencyStore(r.Context(), p.TenantID, key, body, status, respBody)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

// audit writes the hash-chained audit entry for a mutation (SPEC §13.5).
func (a *API) audit(r *http.Request, p *auth.Principal, action, resource string, before, after any) {
	e := &model.AuditEntry{
		TenantID: p.TenantID, ActorType: p.ActorType, ActorID: p.ActorID,
		Action: action, Resource: resource,
		SourceIP: remoteHost(r), RequestID: requestID(r),
	}
	if before != nil {
		b, _ := json.Marshal(before)
		e.BeforeJSON = string(b)
	}
	if after != nil {
		b, _ := json.Marshal(after)
		e.AfterJSON = string(b)
	}
	if _, err := a.Store.AppendAudit(r.Context(), e); err != nil {
		a.Log.Error("api: audit append failed", "err", err, "action", action)
	}
}

func remoteHost(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

// configChanged notifies live subsystems about config changes and emits
// a config event. The expensive full-tenant catalog reload + reschedule
// only runs for kinds the catalog actually caches (templates, commands,
// time periods — they fan out into many effective specs) or when no
// kind is given (bulk object paths). Saving a contact, dashboard,
// channel or rule must NOT pay for a tenant-wide reload (write latency
// — SPEC §11.2); single-object mutations use objectChanged instead.
func (a *API) configChanged(ctx context.Context, tenantID string, kinds ...string) {
	if catalogAffecting(kinds) {
		if err := a.Catalog.ReloadTenant(ctx, tenantID); err != nil {
			a.Log.Error("api: catalog reload", "err", err)
		}
		for _, e := range a.Catalog.All() {
			if e.Object.TenantID == tenantID {
				a.Sched.Upsert(e)
			}
		}
	}
	for _, k := range kinds {
		if k == storage.KindAlertRule {
			_ = a.Alert.ReloadRules(ctx, tenantID)
		}
	}
	a.emitConfigEvent(ctx, tenantID, kinds)
}

// catalogAffecting reports whether a change to these kinds invalidates
// cached effective specs. No kinds = object bulk path = yes. The
// pseudo-kind "object" marks bulk object mutations (bundles, batch).
func catalogAffecting(kinds []string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, k := range kinds {
		switch k {
		case storage.KindTemplate, storage.KindCheckCommand, storage.KindTimePeriod, "object":
			return true
		}
	}
	return false
}

// objectChanged incrementally refreshes catalog + scheduler for one
// object mutation — O(1) instead of a full tenant reload per save.
func (a *API) objectChanged(ctx context.Context, obj *model.Object) {
	if err := a.Catalog.UpsertObject(obj); err != nil {
		a.Log.Warn("api: object reindex", "object", obj.Name, "err", err)
	}
	if e := a.Catalog.Get(obj.ID); e != nil {
		a.Sched.Upsert(e)
	}
	a.emitConfigEvent(ctx, obj.TenantID, []string{"object"})
}

// objectRemoved drops the object (and cascaded service children) from
// catalog, scheduler and pipeline working set.
func (a *API) objectRemoved(ctx context.Context, obj *model.Object) {
	for _, id := range a.Catalog.RemoveObject(obj.TenantID, obj.ID) {
		a.Sched.Remove(id)
		a.Pipe.Forget(id)
	}
	a.emitConfigEvent(ctx, obj.TenantID, []string{"object"})
}

func (a *API) emitConfigEvent(ctx context.Context, tenantID string, kinds []string) {
	raw, _ := json.Marshal(map[string]any{"kinds": kinds})
	ev := &model.Event{ID: model.NewID(), TenantID: tenantID, TS: time.Now().UTC(),
		Type: model.EventConfig, Severity: model.SevInfo, Payload: raw}
	a.insertEvents(ctx, ev)
	a.Bus.FanoutOnly(ev)
}

// insertEvents persists best-effort audit/lifecycle events. Insert
// failures are intentionally non-fatal (the fanout to live subscribers
// still happens), but must not be silent: log a warning and bump the
// dropped-events counter so the loss is observable on /metrics rather
// than vanishing into a discarded error.
func (a *API) insertEvents(ctx context.Context, events ...*model.Event) {
	if err := a.Store.InsertEvents(ctx, events); err != nil {
		typ := ""
		if len(events) > 0 {
			typ = string(events[0].Type)
		}
		a.Log.Warn("api: event insert dropped", "err", err, "count", len(events), "type", typ)
		a.Metrics.Counter(`np_events_dropped_total{source="api"}`,
			"events dropped on insert (best-effort, not persisted)").Add(uint64(len(events)))
	}
}

// registerAll wires every resource group (SPEC §11.3 catalog).
func (a *API) registerAll() {
	a.registerObjects()
	a.registerAlerts()
	a.registerRules()
	a.registerMaintenance()
	a.registerOnCall()
	a.registerContacts()
	a.registerEvents()
	a.registerMetricsQuery()
	a.registerIngress()
	a.registerBundles()
	a.registerBusiness()
	a.registerReportsDashboards()
	a.registerAdmin()
	a.registerAI()
	a.registerSystem()
	a.registerOpenAPI()
	a.registerDiscovery()
	a.registerWebhookSubs()
}

// param fetches a path wildcard.
func param(r *http.Request, name string) string { return r.PathValue(name) }

// tenantOf is the request tenant (operators may switch via header when
// multi-tenant — checked against permission admin:tenants).
func (a *API) tenantOf(r *http.Request, p *auth.Principal) string {
	if t := r.Header.Get("X-Northplane-Tenant"); t != "" && p.Allow("admin:tenants") {
		return t
	}
	return p.TenantID
}

var startTime = time.Now()

func fmtUptime() string {
	d := time.Since(startTime).Round(time.Second)
	return d.String()
}

var _ = fmt.Sprintf
