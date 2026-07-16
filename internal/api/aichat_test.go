package api

// Agent-chat routes end-to-end: connections (scoping + admin:ai for
// shared), chats + per-message delete with strict per-user isolation,
// the policy endpoints, and one full SSE turn against a fake provider.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/northplane/northplane/internal/ai"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// bootAIChatAPI extends bootAPI with the agent-chat routes and a real
// ai.Service (SecretBox included).
func bootAIChatAPI(t *testing.T) *testAPI {
	t.Helper()
	ta := bootAPI(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := auth.NewSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	ta.a.AI = ai.New(ai.Deps{
		Cfg: config.AIConfig{}, Store: ta.store, Bus: eventbus.New(), Box: box,
	})
	ta.a.registerAIChat()
	ta.h = ta.a.withMiddleware(ta.a.mux)
	return ta
}

func TestAIChatConnectionScoping(t *testing.T) {
	ta := bootAIChatAPI(t)

	// reader creates a personal keyless connection (ollama default endpoint).
	code, body := ta.read("POST", "/api/v1/ai/connections",
		map[string]any{"name": "local", "provider": "ollama"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	connID := ta.id(body)

	// admin does not see reader's personal connection…
	code, body = ta.admin("GET", "/api/v1/ai/connections", nil)
	if code != 200 || strings.Contains(string(body), connID) {
		t.Fatalf("cross-user visibility: %d %s", code, body)
	}
	// …and cannot delete it as their own.
	code, _ = ta.admin("DELETE", "/api/v1/ai/connections/"+connID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-user delete: %d", code)
	}

	// shared connections need admin:ai.
	code, body = ta.read("POST", "/api/v1/ai/connections",
		map[string]any{"name": "team", "provider": "ollama", "shared": true})
	if code != http.StatusForbidden {
		t.Fatalf("shared as reader: %d %s", code, body)
	}
	code, body = ta.admin("POST", "/api/v1/ai/connections",
		map[string]any{"name": "team", "provider": "ollama", "shared": true})
	if code != http.StatusCreated {
		t.Fatalf("shared as admin: %d %s", code, body)
	}
	// reader sees the shared one.
	code, body = ta.read("GET", "/api/v1/ai/connections", nil)
	if code != 200 || !strings.Contains(string(body), `"team"`) {
		t.Fatalf("reader shared visibility: %d %s", code, body)
	}

	// keyed provider without key → 400.
	code, body = ta.read("POST", "/api/v1/ai/connections",
		map[string]any{"name": "claude", "provider": "anthropic"})
	if code != http.StatusBadRequest {
		t.Fatalf("keyless anthropic: %d %s", code, body)
	}
	// with key → created, key never echoed.
	code, body = ta.read("POST", "/api/v1/ai/connections",
		map[string]any{"name": "claude", "provider": "anthropic", "apiKey": "sk-ant-xyz-1234"})
	if code != http.StatusCreated {
		t.Fatalf("anthropic create: %d %s", code, body)
	}
	if strings.Contains(string(body), "sk-ant-xyz") {
		t.Fatalf("api key echoed: %s", body)
	}
	if !strings.Contains(string(body), `"keyHint":"…1234"`) {
		t.Fatalf("key hint missing: %s", body)
	}
}

func TestAIChatOwnershipAndMessageDelete(t *testing.T) {
	ta := bootAIChatAPI(t)

	code, body := ta.read("POST", "/api/v1/ai/chats", map[string]any{"title": "meiner"})
	if code != http.StatusCreated {
		t.Fatalf("create chat: %d %s", code, body)
	}
	chatID := ta.id(body)

	// other user (admin token = other actor) must not see it.
	code, _ = ta.admin("GET", "/api/v1/ai/chats/"+chatID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-user chat read: %d", code)
	}
	code, _ = ta.admin("DELETE", "/api/v1/ai/chats/"+chatID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-user chat delete: %d", code)
	}

	// seed two messages directly (storage-level) and delete one via API.
	m1 := seedChatMessage(t, ta, chatID, "user", "eins")
	m2 := seedChatMessage(t, ta, chatID, "assistant", "zwei")
	code, _ = ta.read("DELETE", "/api/v1/ai/chats/"+chatID+"/messages/"+m1, nil)
	if code != 200 {
		t.Fatalf("delete message: %d", code)
	}
	code, body = ta.read("GET", "/api/v1/ai/chats/"+chatID, nil)
	if code != 200 || strings.Contains(string(body), m1) || !strings.Contains(string(body), m2) {
		t.Fatalf("after delete: %d %s", code, body)
	}
}

func seedChatMessage(t *testing.T, ta *testAPI, chatID, role, text string) string {
	t.Helper()
	parts, _ := json.Marshal([]ai.Part{{Type: "text", Text: text}})
	msg := &storage.AIChatMessage{ChatID: chatID, TenantID: model.DefaultTenant,
		Role: role, Parts: parts}
	if err := ta.store.AppendAIChatMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	return msg.ID
}

func TestAIPolicyEndpoints(t *testing.T) {
	ta := bootAIChatAPI(t)

	// reader may inspect the tool catalog but not the policy.
	code, body := ta.read("GET", "/api/v1/ai/tools", nil)
	if code != 200 || !strings.Contains(string(body), "acknowledge_alert") {
		t.Fatalf("tool catalog: %d %s", code, body)
	}
	code, _ = ta.read("GET", "/api/v1/ai/policy", nil)
	if code != http.StatusForbidden {
		t.Fatalf("policy as reader: %d", code)
	}
	code, body = ta.admin("PUT", "/api/v1/ai/policy",
		map[string]any{"disabled": []string{"apply_config_change"}, "maxRounds": 6})
	if code != 200 {
		t.Fatalf("policy save: %d %s", code, body)
	}
	code, body = ta.admin("GET", "/api/v1/ai/policy", nil)
	if code != 200 || !strings.Contains(string(body), "apply_config_change") {
		t.Fatalf("policy read: %d %s", code, body)
	}
	// catalog reflects the disabled state.
	code, body = ta.read("GET", "/api/v1/ai/tools", nil)
	if code != 200 || !strings.Contains(string(body), `"disabled":true`) {
		t.Fatalf("catalog after policy: %d %s", code, body)
	}
	// invalid policy rejected.
	code, _ = ta.admin("PUT", "/api/v1/ai/policy", map[string]any{"disabled": []string{"nope"}})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid policy: %d", code)
	}
}

func TestAIChatStreamTurn(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hallo \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Welt\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	ta := bootAIChatAPI(t)
	// custom endpoint → needs config:write → admin token.
	code, body := ta.admin("POST", "/api/v1/ai/connections",
		map[string]any{"name": "fake", "provider": "openai-compat", "endpoint": ts.URL, "defaultModel": "m"})
	if code != http.StatusCreated {
		t.Fatalf("conn: %d %s", code, body)
	}
	connID := ta.id(body)
	code, body = ta.admin("POST", "/api/v1/ai/chats", map[string]any{"connectionId": connID})
	if code != http.StatusCreated {
		t.Fatalf("chat: %d %s", code, body)
	}
	chatID := ta.id(body)

	rec := ta.raw("POST", "/api/v1/ai/chat",
		map[string]any{"chatId": chatID, "message": "sag hallo"}, bearer(ta.adminToken))
	if rec.Code != 200 {
		t.Fatalf("stream: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("x-vercel-ai-ui-message-stream"); got != "v1" {
		t.Fatalf("stream header: %q", got)
	}
	out := rec.Body.String()
	for _, want := range []string{
		`"type":"start"`, `"type":"text-delta"`, `"delta":"Hallo "`,
		`"type":"finish"`, "data: [DONE]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %s:\n%s", want, out)
		}
	}
	// chat title auto-set + assistant persisted.
	code, body = ta.admin("GET", "/api/v1/ai/chats/"+chatID, nil)
	if code != 200 || !strings.Contains(string(body), `"sag hallo"`) ||
		!strings.Contains(string(body), "Hallo Welt") {
		t.Fatalf("persisted chat: %d %s", code, body)
	}
	// second user message while nothing is streaming → works again
	rec = ta.raw("POST", "/api/v1/ai/chat",
		map[string]any{"chatId": chatID, "message": "nochmal"}, bearer(ta.adminToken))
	if rec.Code != 200 {
		t.Fatalf("second turn: %d", rec.Code)
	}
	// regenerate: replaces the last assistant message.
	var detail struct {
		Messages []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"messages"`
	}
	_, body = ta.admin("GET", "/api/v1/ai/chats/"+chatID, nil)
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	last := detail.Messages[len(detail.Messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("last message: %+v", last)
	}
	rec = ta.raw("POST", "/api/v1/ai/chat",
		map[string]any{"chatId": chatID, "trigger": "regenerate-message", "messageId": last.ID},
		bearer(ta.adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delta":"Hallo "`) {
		t.Fatalf("regenerate: %d %s", rec.Code, rec.Body.String())
	}
	_, body = ta.admin("GET", "/api/v1/ai/chats/"+chatID, nil)
	if strings.Contains(string(body), last.ID) {
		t.Fatalf("old assistant message still present after regenerate")
	}
}
