package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/northplane/northplane/internal/ai"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/storage"
)

// Agent-chat routes (SPEC §10.4 evolution): multi-provider connections,
// per-user chats with per-message persistence, and the streaming agent
// endpoint speaking the UI-message-stream SSE protocol (Vercel AI SDK
// v1 wire format — a de-facto standard chat clients understand).

// aiService unwraps the concrete service; chat routes 503 when the AI
// subsystem is absent (unit-test fakes implement only AIService).
func (a *API) aiService() *ai.Service {
	svc, _ := a.AI.(*ai.Service)
	return svc
}

func (a *API) aiUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if a.aiService() == nil {
		a.problem(w, r, http.StatusServiceUnavailable, "np:ai/disabled",
			"AI subsystem not wired", "")
		return true
	}
	return false
}

func (a *API) registerAIChat() {
	// --- provider catalog + connections ---

	a.handle("GET /api/v1/ai/providers", "AI provider catalog", "events:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			a.writeList(w, ai.ProviderTypes(), "")
		})

	a.handle("GET /api/v1/ai/connections", "List AI provider connections", "events:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			conns, err := a.aiService().ListConnections(r.Context(), a.principalFor(r, p))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, conns, "")
		})

	a.handle("POST /api/v1/ai/connections", "Create AI provider connection", "events:read",
		ai.ConnectionInput{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			var in ai.ConnectionInput
			if !a.decode(w, r, &in) {
				return
			}
			if in.Shared && !p.Allow("admin:ai") {
				a.problem(w, r, http.StatusForbidden, "np:auth/forbidden",
					"shared connections require admin:ai", "")
				return
			}
			conn, err := a.aiService().CreateConnection(r.Context(), a.principalFor(r, p), in)
			if err != nil {
				a.aiChatError(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusCreated, conn)
		})

	a.handle("PUT /api/v1/ai/connections/{id}", "Update AI provider connection", "events:read",
		ai.ConnectionInput{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			var in ai.ConnectionInput
			if !a.decode(w, r, &in) {
				return
			}
			if in.Shared && !p.Allow("admin:ai") {
				a.problem(w, r, http.StatusForbidden, "np:auth/forbidden",
					"shared connections require admin:ai", "")
				return
			}
			conn, err := a.aiService().UpdateConnection(r.Context(), a.principalFor(r, p),
				param(r, "id"), in.Shared, in)
			if err != nil {
				a.aiChatError(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, conn)
		})

	a.handle("DELETE /api/v1/ai/connections/{id}", "Delete AI provider connection", "events:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			shared := r.URL.Query().Get("shared") == "true"
			if shared && !p.Allow("admin:ai") {
				a.problem(w, r, http.StatusForbidden, "np:auth/forbidden",
					"shared connections require admin:ai", "")
				return
			}
			if err := a.aiService().DeleteConnection(r.Context(), a.principalFor(r, p),
				param(r, "id"), shared); err != nil {
				a.aiChatError(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		}).Query(oaParam{Name: "shared", Desc: "delete a tenant-wide connection (admin:ai)", Type: "boolean"})

	a.handle("POST /api/v1/ai/connections/{id}:test", "Test an AI provider connection", "events:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			n, err := a.aiService().TestConnection(r.Context(), a.principalFor(r, p), param(r, "id"))
			if err != nil {
				a.aiChatError(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "models": n})
		})

	a.handle("GET /api/v1/ai/connections/{id}/models", "List models of a connection", "events:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			models, note, err := a.aiService().ConnectionModels(r.Context(), a.principalFor(r, p), param(r, "id"))
			if err != nil {
				a.aiChatError(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, map[string]any{"items": models, "note": note})
		})

	// --- tool catalog + policy ---

	a.handle("GET /api/v1/ai/tools", "Agent tool catalog with policy state", "events:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			tools, err := a.aiService().ToolCatalog(r.Context(), a.tenantOf(r, p))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, tools, "")
		})

	a.handle("GET /api/v1/ai/policy", "Agent tool policy", "admin:ai", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			pol, err := a.aiService().Policy(r.Context(), a.tenantOf(r, p))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, pol)
		})

	a.handle("PUT /api/v1/ai/policy", "Update agent tool policy", "admin:ai", ai.ToolPolicy{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			var pol ai.ToolPolicy
			if !a.decode(w, r, &pol) {
				return
			}
			if err := a.aiService().SavePolicy(r.Context(), a.principalFor(r, p), &pol); err != nil {
				a.aiChatError(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, pol)
		})

	// --- chats ---

	type chatInput struct {
		Title        string          `json:"title,omitempty"`
		ConnectionID string          `json:"connectionId,omitempty"`
		Model        string          `json:"model,omitempty"`
		Settings     json.RawMessage `json:"settings,omitempty"`
	}

	a.handle("GET /api/v1/ai/chats", "List my agent chats", "events:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			chats, err := a.Store.ListAIChats(r.Context(), a.tenantOf(r, p), p.ActorID, 100)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeList(w, chats, "")
		})

	a.handle("POST /api/v1/ai/chats", "Create an agent chat", "events:read", chatInput{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var in chatInput
			if !a.decode(w, r, &in) {
				return
			}
			chat := &storage.AIChat{
				TenantID: a.tenantOf(r, p), UserID: p.ActorID,
				Title: in.Title, ConnectionID: in.ConnectionID, Model: in.Model,
				Settings: in.Settings,
			}
			if err := a.Store.CreateAIChat(r.Context(), chat); err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusCreated, chat)
		})

	a.handle("GET /api/v1/ai/chats/{id}", "Chat with messages", "events:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			chat, err := a.Store.GetAIChat(r.Context(), tenant, p.ActorID, param(r, "id"))
			if err != nil {
				a.aiChatError(w, r, err)
				return
			}
			msgs, err := a.Store.ListAIChatMessages(r.Context(), tenant, chat.ID)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, map[string]any{"chat": chat, "messages": msgs})
		})

	a.handle("PUT /api/v1/ai/chats/{id}", "Update chat settings", "events:read", chatInput{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			chat, err := a.Store.GetAIChat(r.Context(), tenant, p.ActorID, param(r, "id"))
			if err != nil {
				a.aiChatError(w, r, err)
				return
			}
			var in chatInput
			if !a.decode(w, r, &in) {
				return
			}
			if in.Title != "" {
				chat.Title = in.Title
			}
			if in.ConnectionID != "" {
				chat.ConnectionID = in.ConnectionID
			}
			if in.Model != "" {
				chat.Model = in.Model
			}
			if len(in.Settings) > 0 {
				chat.Settings = in.Settings
			}
			if err := a.Store.UpdateAIChat(r.Context(), chat); err != nil {
				a.fail(w, r, err)
				return
			}
			a.writeJSON(w, http.StatusOK, chat)
		})

	a.handle("DELETE /api/v1/ai/chats/{id}", "Delete a chat", "events:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if err := a.Store.DeleteAIChat(r.Context(), a.tenantOf(r, p), p.ActorID, param(r, "id")); err != nil {
				a.aiChatError(w, r, err)
				return
			}
			a.audit(r, p, "ai.chat.delete", param(r, "id"), nil, nil)
			a.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		})

	a.handle("DELETE /api/v1/ai/chats/{id}/messages/{msgId}", "Delete one chat message", "events:read", nil, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			// Ownership check before touching messages.
			chat, err := a.Store.GetAIChat(r.Context(), tenant, p.ActorID, param(r, "id"))
			if err != nil {
				a.aiChatError(w, r, err)
				return
			}
			if err := a.Store.DeleteAIChatMessage(r.Context(), tenant, chat.ID, param(r, "msgId")); err != nil {
				a.aiChatError(w, r, err)
				return
			}
			a.audit(r, p, "ai.chat.message.delete", chat.ID+"/"+param(r, "msgId"), nil, nil)
			a.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		})

	// --- the streaming agent endpoint ---

	type sendInput struct {
		ChatID string `json:"chatId"`
		// Message is the new user text (submit trigger).
		Message string `json:"message,omitempty"`
		// Trigger: "submit-message" (default) | "regenerate-message".
		Trigger string `json:"trigger,omitempty"`
		// MessageID: for regenerate — the assistant message to replace.
		MessageID string `json:"messageId,omitempty"`
	}

	a.handle("POST /api/v1/ai/chat", "Stream an agent turn (SSE)", "events:read", sendInput{}, nil,
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			if a.aiUnavailable(w, r) {
				return
			}
			var in sendInput
			if !a.decode(w, r, &in) {
				return
			}
			a.streamChatTurn(w, r, p, in.ChatID, in.Message, in.Trigger, in.MessageID)
		})
}

// principalFor scopes the principal to the effective tenant of the
// request (multi-tenant admin switching, same rule as tenantOf).
func (a *API) principalFor(r *http.Request, p *auth.Principal) *auth.Principal {
	tenant := a.tenantOf(r, p)
	if tenant == p.TenantID {
		return p
	}
	cp := *p
	cp.TenantID = tenant
	return &cp
}

// aiChatError maps storage/service errors onto problem responses.
func (a *API) aiChatError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		a.problem(w, r, http.StatusNotFound, "np:not-found", "not found", "")
	case errors.Is(err, storage.ErrDuplicate):
		a.problem(w, r, http.StatusConflict, "np:conflict", "already exists", err.Error())
	case errors.Is(err, ai.ErrChatBusy):
		a.problem(w, r, http.StatusConflict, "np:ai/busy", "chat is already streaming", "")
	default:
		a.problem(w, r, http.StatusBadRequest, "np:ai/invalid", "request failed", err.Error())
	}
}

// streamChatTurn runs the agent loop and writes the UI message stream
// (SSE; header x-vercel-ai-ui-message-stream: v1).
func (a *API) streamChatTurn(w http.ResponseWriter, r *http.Request, p *auth.Principal,
	chatID, message, trigger, messageID string) {

	svc := a.aiService()
	tenant := a.tenantOf(r, p)
	pp := a.principalFor(r, p)

	if chatID == "" {
		a.validationError(w, r, "chatId", "chatId required")
		return
	}
	chat, err := a.Store.GetAIChat(r.Context(), tenant, p.ActorID, chatID)
	if err != nil {
		a.aiChatError(w, r, err)
		return
	}
	if chat.ConnectionID == "" {
		a.problem(w, r, http.StatusBadRequest, "np:ai/no-connection",
			"chat has no provider connection", "")
		return
	}

	switch trigger {
	case "", "submit-message":
		message = strings.TrimSpace(message)
		if message == "" {
			a.validationError(w, r, "message", "message required")
			return
		}
		if len(message) > 32<<10 {
			a.validationError(w, r, "message", "message too long (max 32 KiB)")
			return
		}
		parts, _ := json.Marshal([]ai.Part{{Type: "text", Text: message}})
		userMsg := &storage.AIChatMessage{ChatID: chat.ID, TenantID: tenant,
			Role: "user", Parts: parts}
		if err := a.Store.AppendAIChatMessage(r.Context(), userMsg); err != nil {
			a.fail(w, r, err)
			return
		}
		if chat.Title == "" {
			chat.Title = message
			if len(chat.Title) > 80 {
				chat.Title = chat.Title[:80] + "…"
			}
			_ = a.Store.UpdateAIChat(r.Context(), chat)
		}
	case "regenerate-message":
		if messageID == "" {
			a.validationError(w, r, "messageId", "messageId required for regenerate")
			return
		}
		if err := a.Store.DeleteAIChatMessagesFrom(r.Context(), tenant, chat.ID, messageID); err != nil {
			a.fail(w, r, err)
			return
		}
	default:
		a.validationError(w, r, "trigger", "unknown trigger")
		return
	}

	history, err := a.Store.ListAIChatMessages(r.Context(), tenant, chat.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		a.problem(w, r, http.StatusBadRequest, "np:ai/no-user-message",
			"nothing to answer — last message is not a user message", "")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		a.problem(w, r, http.StatusInternalServerError, "np:ai/stream", "streaming unsupported", "")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	h.Set("x-vercel-ai-ui-message-stream", "v1")
	w.WriteHeader(http.StatusOK)

	writeChunk := func(v map[string]any) {
		raw, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}

	sink := func(ev ai.StreamEvent) {
		if chunk := uiChunk(ev); chunk != nil {
			writeChunk(chunk)
		}
	}

	_, err = svc.StreamChat(r.Context(), pp, chat, history, sink)
	if err != nil {
		// Loop-level failure before anything streamed: still SSE-shaped.
		writeChunk(map[string]any{"type": "error", "errorText": err.Error()})
		writeChunk(map[string]any{"type": "finish", "finishReason": "error"})
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// uiChunk maps loop events onto UI-message-stream chunks (v1 wire
// names; tool parts flagged dynamic since tools are runtime-defined).
func uiChunk(ev ai.StreamEvent) map[string]any {
	switch ev.Type {
	case ai.EvMessageStart:
		return map[string]any{"type": "start", "messageId": ev.ID}
	case ai.EvStepStart:
		return map[string]any{"type": "start-step"}
	case ai.EvStepFinish:
		return map[string]any{"type": "finish-step"}
	case ai.EvTextStart:
		return map[string]any{"type": "text-start", "id": ev.ID}
	case ai.EvTextDelta:
		return map[string]any{"type": "text-delta", "id": ev.ID, "delta": ev.Delta}
	case ai.EvTextEnd:
		return map[string]any{"type": "text-end", "id": ev.ID}
	case ai.EvReasoningStart:
		return map[string]any{"type": "reasoning-start", "id": ev.ID}
	case ai.EvReasoningDelta:
		return map[string]any{"type": "reasoning-delta", "id": ev.ID, "delta": ev.Delta}
	case ai.EvReasoningEnd:
		return map[string]any{"type": "reasoning-end", "id": ev.ID}
	case ai.EvToolInputStart:
		return map[string]any{"type": "tool-input-start",
			"toolCallId": ev.ToolCallID, "toolName": ev.ToolName, "dynamic": true}
	case ai.EvToolInputDelta:
		return map[string]any{"type": "tool-input-delta",
			"toolCallId": ev.ToolCallID, "inputTextDelta": ev.Delta}
	case ai.EvToolInput:
		return map[string]any{"type": "tool-input-available",
			"toolCallId": ev.ToolCallID, "toolName": ev.ToolName,
			"input": ev.Input, "dynamic": true}
	case ai.EvToolOutput:
		chunk := map[string]any{"type": "tool-output-available",
			"toolCallId": ev.ToolCallID, "output": ev.Output, "dynamic": true}
		if ev.Proposed {
			chunk["toolMetadata"] = map[string]any{"proposed": true, "actionId": ev.ActionID}
		}
		return chunk
	case ai.EvToolError:
		return map[string]any{"type": "tool-output-error",
			"toolCallId": ev.ToolCallID, "errorText": ev.ErrorText, "dynamic": true}
	case ai.EvError:
		return map[string]any{"type": "error", "errorText": ev.ErrorText}
	case ai.EvFinish:
		chunk := map[string]any{"type": "finish", "finishReason": finishReason(ev.StopReason)}
		if ev.Usage != nil {
			chunk["messageMetadata"] = map[string]any{
				"inputTokens": ev.Usage.InputTokens, "outputTokens": ev.Usage.OutputTokens,
				"stopReason": ev.StopReason,
			}
		}
		return chunk
	}
	return nil
}

// finishReason maps provider stop reasons onto the UI vocabulary.
func finishReason(stop string) string {
	switch stop {
	case "end_turn", "stop", "":
		return "stop"
	case "tool_use", "tool-calls", "tool_calls":
		return "tool-calls"
	case "max_tokens", "length":
		return "length"
	case "refusal", "content-filter":
		return "content-filter"
	case "aborted":
		return "other"
	case "error", "budget":
		return "error"
	default:
		return "other"
	}
}
