package api

import (
	"encoding/json"
	"net/http"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/storage"
)

// AI routes (SPEC §10): conversations and the approval queue. The
// provider logic lives in internal/ai — these endpoints stay functional
// without it (the queue is plain data; Converse 503s cleanly).

func (a *API) registerAI() {
	type converseRequest struct {
		ConversationID string `json:"conversationId,omitempty"`
		Message        string `json:"message"`
	}
	a.handle("POST /api/v1/ai/conversations", "Talk to the assistant", "events:read",
		converseRequest{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.AI == nil || !a.AI.Enabled() {
				a.problem(w, r, http.StatusServiceUnavailable, "np:ai/disabled",
					"AI provider not configured (ai.provider=none)", "")
				return
			}
			var req converseRequest
			if !a.decode(w, r, &req) {
				return
			}
			if req.Message == "" {
				a.validationError(w, r, "message", "message required")
				return
			}
			resp, err := a.AI.Converse(r.Context(), p, req.ConversationID, req.Message)
			if err != nil {
				a.problem(w, r, http.StatusBadGateway, "np:ai/provider", "AI provider error", err.Error())
				return
			}
			a.writeJSON(w, http.StatusOK, resp)
		})

	a.handle("GET /api/v1/ai/conversations", "List conversations", "events:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			rows, err := a.Store.DB().QueryContext(r.Context(), a.Store.Q(
				`SELECT id, title, created_at, updated_at FROM ai_conversations
				 WHERE tenant_id = ? AND user_id = ? ORDER BY updated_at DESC LIMIT 50`),
				a.tenantOf(r, p), p.ActorID)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer rows.Close()
			type convMeta struct {
				ID        string `json:"id"`
				Title     string `json:"title"`
				CreatedAt string `json:"createdAt"`
				UpdatedAt string `json:"updatedAt"`
			}
			var out []convMeta
			for rows.Next() {
				var c convMeta
				var created, updated storage.ScanTime
				if err := rows.Scan(&c.ID, &c.Title, &created, &updated); err != nil {
					a.fail(w, r, err)
					return
				}
				c.CreatedAt, c.UpdatedAt = created.T.String(), updated.T.String()
				out = append(out, c)
			}
			a.writeList(w, out, "")
		})

	a.handle("GET /api/v1/ai/conversations/{id}", "Conversation transcript", "events:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var messages, title string
			err := a.Store.DB().QueryRowContext(r.Context(), a.Store.Q(
				`SELECT title, messages FROM ai_conversations WHERE tenant_id = ? AND user_id = ? AND id = ?`),
				a.tenantOf(r, p), p.ActorID, param(r, "id")).Scan(&title, &messages)
			if err != nil {
				a.problem(w, r, http.StatusNotFound, "np:not-found", "conversation not found", "")
				return
			}
			a.writeJSON(w, http.StatusOK, map[string]any{
				"id": param(r, "id"), "title": title,
				"messages": json.RawMessage(messages)})
		})

	// approval queue (SPEC §10.1: propose → human decision → execute)
	a.handle("GET /api/v1/ai/actions", "AI action approval queue", "alerts:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			status := storage.AIActionStatus(r.URL.Query().Get("status"))
			actions, err := a.Store.ListAIActions(r.Context(), a.tenantOf(r, p), status, 100)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, actions, "")
		})

	a.handle("POST /api/v1/ai/actions/{id}:approve", "Approve and execute a proposed action",
		"config:write", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			if err := a.Store.DecideAIAction(r.Context(), tenant, param(r, "id"),
				storage.AIApproved, p.Name); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "ai.action.approve", param(r, "id"), nil, nil)
			// Execution needs only the tool registry, not a configured
			// ai.provider — approvals from the agent chat and MCP (which
			// bring their own model) must run too.
			if a.AI != nil {
				// Execute under the approver's scopes (not god-mode): the human
				// approval authorises only what the approver may themselves do.
				result, err := a.AI.ExecuteApproved(r.Context(), tenant, param(r, "id"), p)
				if err != nil {
					a.problem(w, r, http.StatusBadGateway, "np:ai/execute",
						"approved but execution failed", err.Error())
					return
				}
				a.writeJSON(w, http.StatusOK, map[string]any{"status": "executed", "result": result})
				return
			}
			a.writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
		})

	a.handle("POST /api/v1/ai/actions/{id}:deny", "Deny a proposed action", "alerts:ack", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if err := a.Store.DecideAIAction(r.Context(), a.tenantOf(r, p), param(r, "id"),
				storage.AIDenied, p.Name); err != nil {
				a.fail(w, r, err)
				return
			}
			a.audit(r, p, "ai.action.deny", param(r, "id"), nil, nil)
			a.writeJSON(w, http.StatusOK, map[string]string{"status": "denied"})
		})
}
