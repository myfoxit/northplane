package ai

// Agent-chat core: history→provider conversion, tool policy gating,
// both wire adapters against fake SSE servers, key sealing on
// connections, and the full streaming loop end-to-end (fake provider →
// tool round → persisted assistant message).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

func bootChatSvc(t *testing.T) (*Service, *fakeResources, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := auth.NewSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeResources{docs: map[string]json.RawMessage{}}
	svc := New(Deps{Cfg: config.AIConfig{}, Store: store, Bus: eventbus.New(),
		Resources: fr, Box: box})
	return svc, fr, ctx
}

// --- policy ---

func TestPolicyDisablesTool(t *testing.T) {
	svc, _, ctx := bootChatSvc(t)
	p := principalWith("objects:read")
	if err := svc.SavePolicy(ctx, p, &ToolPolicy{Disabled: []string{"list_config_resources"}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.RunTool(ctx, p, "list_config_resources", json.RawMessage(`{"kind":"channel"}`))
	if err == nil || !strings.Contains(err.Error(), "disabled by policy") {
		t.Fatalf("want policy denial, got %v", err)
	}
}

func TestPolicyAutoApproveSkipsQueue(t *testing.T) {
	svc, fr, ctx := bootChatSvc(t)
	p := principalWith("config:write")
	input := json.RawMessage(`{"kind":"channel","name":"mail","doc":{"name":"mail"}}`)

	// Default: mutating tool rides the approval queue.
	_, proposed, err := svc.RunTool(ctx, p, "upsert_config_resource", input)
	if err != nil || !proposed {
		t.Fatalf("expected proposal, got proposed=%v err=%v", proposed, err)
	}
	// autoApprove executes directly.
	if err := svc.SavePolicy(ctx, p, &ToolPolicy{AutoApprove: []string{"upsert_config_resource"}}); err != nil {
		t.Fatal(err)
	}
	_, proposed, err = svc.RunTool(ctx, p, "upsert_config_resource", input)
	if err != nil || proposed {
		t.Fatalf("expected direct execution, got proposed=%v err=%v", proposed, err)
	}
	if len(fr.calls) == 0 || fr.calls[len(fr.calls)-1] != "upsert:channel/mail" {
		t.Fatalf("tool did not execute: %v", fr.calls)
	}
}

func TestPolicyValidation(t *testing.T) {
	svc, _, ctx := bootChatSvc(t)
	p := principalWith("admin:ai")
	if err := svc.SavePolicy(ctx, p, &ToolPolicy{Disabled: []string{"nope"}}); err == nil {
		t.Fatal("unknown tool accepted")
	}
	if err := svc.SavePolicy(ctx, p, &ToolPolicy{AutoApprove: []string{"get_overview"}}); err == nil {
		t.Fatal("read-only tool accepted for autoApprove")
	}
	if err := svc.SavePolicy(ctx, p, &ToolPolicy{MaxRounds: 99}); err == nil {
		t.Fatal("maxRounds out of range accepted")
	}
}

// --- history conversion ---

func TestMessagesFromHistory(t *testing.T) {
	mk := func(role string, parts []Part) *storage.AIChatMessage {
		raw, _ := json.Marshal(parts)
		return &storage.AIChatMessage{Role: role, Parts: raw}
	}
	history := []*storage.AIChatMessage{
		mk("user", []Part{{Type: "text", Text: "hi"}}),
		mk("assistant", []Part{
			{Type: "step-start"},
			{Type: "reasoning", Text: "think"},
			{Type: "text", Text: "checking"},
			{Type: "dynamic-tool", ToolCallID: "c1", ToolName: "get_alerts",
				Input: json.RawMessage(`{"limit":5}`), State: "output-available",
				Output: json.RawMessage(`{"count":0}`)},
			{Type: "dynamic-tool", ToolCallID: "c2", ToolName: "get_overview",
				State: "input-available"}, // interrupted before execution
		}),
		mk("assistant", []Part{}), // aborted empty turn → dropped
	}
	msgs := messagesFromHistory(history)
	if len(msgs) != 3 { // user, assistant, tool-results
		t.Fatalf("got %d messages: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hi" {
		t.Fatalf("user msg: %+v", msgs[0])
	}
	am := msgs[1]
	if am.Reasoning != "think" || am.Content != "checking" || len(am.ToolCalls) != 2 {
		t.Fatalf("assistant msg: %+v", am)
	}
	tr := msgs[2]
	if tr.Role != "tool" || len(tr.ToolResults) != 2 {
		t.Fatalf("tool msg: %+v", tr)
	}
	if !tr.ToolResults[1].IsError || !strings.Contains(tr.ToolResults[1].Content, "interrupted") {
		t.Fatalf("interrupted call must get an error result: %+v", tr.ToolResults[1])
	}
}

// --- OpenAI-dialect adapter ---

func sseHandler(lines []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func TestOpenAIStreamRound(t *testing.T) {
	ts := httptest.NewServer(sseHandler([]string{
		`{"choices":[{"delta":{"reasoning_content":"hm"}}]}`,
		`{"choices":[{"delta":{"content":"Hello "}}]}`,
		`{"choices":[{"delta":{"content":"world"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_alerts","arguments":"{\"lim"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"it\":5}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":7}}`,
	}))
	defer ts.Close()

	adapter := &openAIStreamAdapter{endpoint: ts.URL, apiKey: "k"}
	var events []StreamEvent
	res, err := adapter.StreamRound(context.Background(), StreamRequest{Model: "m"},
		func(ev StreamEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello world" || res.Reasoning != "hm" {
		t.Fatalf("accumulation: %+v", res)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "get_alerts" ||
		string(res.ToolCalls[0].Input) != `{"limit":5}` {
		t.Fatalf("tool call: %+v", res.ToolCalls)
	}
	if res.StopReason != "tool_use" || res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 7 {
		t.Fatalf("meta: %+v", res)
	}
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, string(ev.Type))
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"reasoning-start", "reasoning-delta", "text-start",
		"text-delta", "tool-input-start", "tool-input-delta", "tool-input-available"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing event %s in %s", want, joined)
		}
	}
}

func TestOpenAIBuildBodyQuirks(t *testing.T) {
	adapter := &openAIStreamAdapter{endpoint: "x", quirks: openAIQuirks{
		reasoningEffortParam: true, maxCompletionTokens: true, echoReasoningContent: true,
	}}
	body := adapter.buildBody(StreamRequest{
		Model: "m", Effort: "high", MaxTokens: 111,
		Messages: []Message{
			{Role: "assistant", Content: "used tool", Reasoning: "cot",
				ToolCalls: []ToolCall{{ID: "c", Name: "t", Input: json.RawMessage(`{}`)}}},
			{Role: "tool", ToolResults: []ToolResult{{ID: "c", Content: "ok"}}},
		},
	})
	if body["reasoning_effort"] != "high" {
		t.Fatalf("effort missing: %v", body)
	}
	if body["max_completion_tokens"] != 111 {
		t.Fatalf("max_completion_tokens missing: %v", body)
	}
	if _, has := body["max_tokens"]; has {
		t.Fatal("max_tokens must not be sent with the quirk")
	}
	raw, _ := json.Marshal(body["messages"])
	if !strings.Contains(string(raw), `"reasoning_content":"cot"`) {
		t.Fatalf("DeepSeek reasoning_content not echoed: %s", raw)
	}
}

// --- Anthropic adapter ---

func TestAnthropicStreamRound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["stream"] != true {
			t.Errorf("stream flag missing")
		}
		if _, has := req["thinking"]; !has {
			t.Errorf("adaptive thinking missing for claude-sonnet-5")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":21}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Servus"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu_1","name":"get_alerts"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"limit\":3}"}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", l)
		}
	}))
	defer ts.Close()

	adapter := &anthropicStream{endpoint: ts.URL, apiKey: "k"}
	var events []StreamEvent
	res, err := adapter.StreamRound(context.Background(),
		StreamRequest{Model: "claude-sonnet-5"},
		func(ev StreamEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Servus" || res.Reasoning != "plan" || res.StopReason != "tool_use" {
		t.Fatalf("result: %+v", res)
	}
	if res.Usage.InputTokens != 21 || res.Usage.OutputTokens != 9 {
		t.Fatalf("usage: %+v", res.Usage)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "tu_1" {
		t.Fatalf("tool call: %+v", res.ToolCalls)
	}
	// Verbatim replay meta keeps the thinking signature.
	var meta anthropicMeta
	if err := json.Unmarshal(res.Meta, &meta); err != nil || len(meta.AnthropicContent) != 3 {
		t.Fatalf("meta blocks: %s", res.Meta)
	}
	if !strings.Contains(string(meta.AnthropicContent[0]), "sig123") {
		t.Fatalf("thinking signature lost: %s", meta.AnthropicContent[0])
	}
	// And buildBody replays them untouched.
	body := adapter.buildBody(StreamRequest{Model: "claude-sonnet-5", Messages: []Message{
		{Role: "assistant", Content: "Servus", ToolCalls: res.ToolCalls, Meta: res.Meta},
	}}, false)
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "sig123") {
		t.Fatalf("replay lost thinking block: %s", raw)
	}
}

// --- connections ---

func TestConnectionSealsKey(t *testing.T) {
	svc, _, ctx := bootChatSvc(t)
	p := principalWith("events:read")
	conn, err := svc.CreateConnection(ctx, p, ConnectionInput{
		Name: "acc", Provider: "anthropic", APIKey: strPtr("sk-ant-secret-abcd"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn.KeyHint != "…abcd" || !conn.HasKey {
		t.Fatalf("hint: %+v", conn)
	}
	stored, err := svc.store.GetAIConnection(ctx, p.TenantID, p.ActorID, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.APIKeySealed), "sk-ant-secret") {
		t.Fatal("key stored in plaintext")
	}
	adapter, _, err := svc.adapterFor(stored)
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := adapter.(*anthropicStream); !ok || a.apiKey != "sk-ant-secret-abcd" {
		t.Fatalf("unsealed key wrong: %#v", adapter)
	}
}

func TestConnectionEndpointNeedsConfigWrite(t *testing.T) {
	svc, _, ctx := bootChatSvc(t)
	_, err := svc.CreateConnection(ctx, principalWith("events:read"), ConnectionInput{
		Name: "own", Provider: "openai-compat", Endpoint: "http://127.0.0.1:9999/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "config:write") {
		t.Fatalf("custom endpoint must require config:write, got %v", err)
	}
	if _, err := svc.CreateConnection(ctx, principalWith("events:read", "config:write"), ConnectionInput{
		Name: "own", Provider: "openai-compat", Endpoint: "http://127.0.0.1:9999/v1",
	}); err != nil {
		t.Fatalf("with config:write: %v", err)
	}
}

func strPtr(s string) *string { return &s }

// --- full streaming loop ---

func TestStreamChatLoopWithToolRound(t *testing.T) {
	// Fake OpenAI-compatible provider: first round calls a tool, second
	// round answers with text.
	round := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		w.Header().Set("Content-Type", "text/event-stream")
		if round == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"list_config_resources\",\"arguments\":\"{\\\"kind\\\":\\\"channel\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Fertig: 1 Channel.\"}}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	svc, fr, ctx := bootChatSvc(t)
	fr.docs["channel/mail"] = json.RawMessage(`{"name":"mail"}`)
	p := principalWith("events:read", "objects:read", "config:write")

	conn, err := svc.CreateConnection(ctx, p, ConnectionInput{
		Name: "local", Provider: "openai-compat", Endpoint: ts.URL, DefaultModel: "test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := &storage.AIChat{TenantID: p.TenantID, UserID: p.ActorID,
		ConnectionID: conn.ID, Model: "test-model"}
	if err := svc.store.CreateAIChat(ctx, chat); err != nil {
		t.Fatal(err)
	}
	userParts, _ := json.Marshal([]Part{{Type: "text", Text: "wie viele channels?"}})
	user := &storage.AIChatMessage{ChatID: chat.ID, TenantID: p.TenantID, Role: "user", Parts: userParts}
	if err := svc.store.AppendAIChatMessage(ctx, user); err != nil {
		t.Fatal(err)
	}

	var events []StreamEvent
	saved, err := svc.StreamChat(ctx, p, chat, []*storage.AIChatMessage{user},
		func(ev StreamEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if round != 2 {
		t.Fatalf("expected 2 provider rounds, got %d", round)
	}
	var parts []Part
	if err := json.Unmarshal(saved.Parts, &parts); err != nil {
		t.Fatal(err)
	}
	var toolPart, textPart *Part
	for i := range parts {
		switch parts[i].Type {
		case "dynamic-tool":
			toolPart = &parts[i]
		case "text":
			textPart = &parts[i]
		}
	}
	if toolPart == nil || toolPart.State != "output-available" ||
		!strings.Contains(string(toolPart.Output), `"count":1`) {
		t.Fatalf("tool part: %+v", toolPart)
	}
	if textPart == nil || textPart.Text != "Fertig: 1 Channel." {
		t.Fatalf("text part: %+v", textPart)
	}
	// Persisted + retrievable.
	msgs, err := svc.store.ListAIChatMessages(ctx, p.TenantID, chat.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages: %d err=%v", len(msgs), err)
	}
	// Second concurrent stream on the same chat is rejected.
	svc.chatStreams.Store(chat.ID, struct{}{})
	if _, err := svc.StreamChat(ctx, p, chat, []*storage.AIChatMessage{user},
		func(StreamEvent) {}); err != ErrChatBusy {
		t.Fatalf("want ErrChatBusy, got %v", err)
	}
	svc.chatStreams.Delete(chat.ID)

	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, string(ev.Type))
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"message-start", "start-step", "tool-input-available",
		"tool-output-available", "finish-step", "text-delta", "finish"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
}

// TestStreamChatDisabledToolsAdvertisesNone: chat settings can switch
// tools off — the request must carry no tool defs.
func TestStreamChatToolsToggle(t *testing.T) {
	var sawTools bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, sawTools = req["tools"]
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	svc, _, ctx := bootChatSvc(t)
	p := principalWith("events:read", "config:write")
	conn, err := svc.CreateConnection(ctx, p, ConnectionInput{
		Name: "local", Provider: "openai-compat", Endpoint: ts.URL, DefaultModel: "m"})
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(ChatSettings{ToolsEnabled: boolPtr(false)})
	chat := &storage.AIChat{TenantID: p.TenantID, UserID: p.ActorID,
		ConnectionID: conn.ID, Model: "m", Settings: settings}
	if err := svc.store.CreateAIChat(ctx, chat); err != nil {
		t.Fatal(err)
	}
	userParts, _ := json.Marshal([]Part{{Type: "text", Text: "hallo"}})
	user := &storage.AIChatMessage{ChatID: chat.ID, TenantID: p.TenantID, Role: "user", Parts: userParts}
	if err := svc.store.AppendAIChatMessage(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StreamChat(ctx, p, chat, []*storage.AIChatMessage{user},
		func(StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	if sawTools {
		t.Fatal("tools were advertised although the chat disabled them")
	}
}

func boolPtr(b bool) *bool { return &b }

var _ = model.DefaultTenant
