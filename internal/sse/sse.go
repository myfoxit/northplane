// Package sse implements the realtime hub (SPEC §7.6, ADR-04):
// Server-Sent Events with Last-Event-ID resume, 15 s heartbeat
// comments, per-subscription filters (event types + label selector),
// tenant scoping enforced by RBAC, and a resync hint instead of
// unbounded buffering for slow clients.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
)

// Hub serves /api/v1/stream.
type Hub struct {
	Bus   *eventbus.Bus
	Store *storage.Store

	clients atomic.Int64
}

// Clients returns the current connection count (SSE fanout metric).
func (h *Hub) Clients() int64 { return h.clients.Load() }

// ServeHTTP streams events. Query: ?types=a,b&selector=… — the tenant
// comes from the authenticated principal (enforced upstream).
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request, tenantID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	var types map[string]bool
	if t := r.URL.Query().Get("types"); t != "" {
		types = map[string]bool{}
		for _, x := range strings.Split(t, ",") {
			types[strings.TrimSpace(x)] = true
		}
	}
	var sel selector.Selector
	if s := r.URL.Query().Get("selector"); s != "" {
		parsed, err := selector.Parse(s)
		if err != nil {
			http.Error(w, "bad selector: "+err.Error(), http.StatusBadRequest)
			return
		}
		sel = parsed
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	h.clients.Add(1)
	defer h.clients.Add(-1)

	// Last-Event-ID resume (SPEC §7.6): replay persisted events after
	// the client's last seen UUIDv7 (time-ordered).
	if last := r.Header.Get("Last-Event-ID"); last != "" && model.ValidID(last) {
		since := model.IDTime(last)
		if !since.IsZero() {
			missed, err := h.Store.QueryEvents(r.Context(), storage.EventFilter{
				TenantID: tenantID, From: since.Add(-time.Second),
				Limit: 500, Asc: true,
			})
			if err == nil {
				for _, e := range missed {
					if e.ID <= last {
						continue
					}
					if !matches(e, types, sel) {
						continue
					}
					writeEvent(w, e)
				}
				flusher.Flush()
			}
		}
	}

	sub := h.Bus.Subscribe(512)
	defer sub.Close()
	fmt.Fprintf(w, ": connected %s\n\n", time.Now().UTC().Format(time.RFC3339))
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if sub.NeedsResync() {
				fmt.Fprint(w, "event: resync\ndata: {}\n\n")
			} else {
				fmt.Fprint(w, ": ping\n\n")
			}
			flusher.Flush()
		case e := <-sub.C:
			if e.TenantID != tenantID || !matches(e, types, sel) {
				continue
			}
			writeEvent(w, e)
			flusher.Flush()
		}
	}
}

func matches(e *model.Event, types map[string]bool, sel selector.Selector) bool {
	if types != nil && !types[string(e.Type)] {
		return false
	}
	if sel.Empty() {
		return true
	}
	var payload struct {
		Labels model.Labels `json:"labels"`
	}
	_ = json.Unmarshal(e.Payload, &payload)
	return sel.Matches(payload.Labels)
}

func writeEvent(w http.ResponseWriter, e *model.Event) {
	data, _ := json.Marshal(e)
	fmt.Fprintf(w, "event: %s\nid: %s\ndata: %s\n\n", e.Type, e.ID, data)
}
