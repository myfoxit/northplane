// Package ai is the AI subsystem (SPEC §10): a thin provider client
// (Anthropic Messages + OpenAI-compatible schema, streaming, tool-use —
// no LangChain-style framework, P3/P5), the redaction pipeline, the
// assistant with action-cards, the propose/approve gates and the
// deterministic statistics engine (EWMA/MAD baselines, adaptive
// thresholds, forecasts). Everything degrades cleanly to provider=none.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/northplane/northplane/internal/config"
)

// Message is a provider-agnostic chat message.
type Message struct {
	Role    string `json:"role"` // system|user|assistant|tool
	Content string `json:"content"`
	// ToolCalls present on assistant turns; ToolResults on tool turns.
	ToolCalls   []ToolCall   `json:"toolCalls,omitempty"`
	ToolResults []ToolResult `json:"toolResults,omitempty"`
}

// ToolDef describes a callable tool (MCP tool surfaced to the model).
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"inputSchema"`
}

// ToolCall is a model request to invoke a tool.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is the response fed back to the model.
type ToolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
}

// Completion is one model turn.
type Completion struct {
	Text         string     `json:"text"`
	ToolCalls    []ToolCall `json:"toolCalls,omitempty"`
	StopReason   string     `json:"stopReason"`
	InputTokens  int64      `json:"inputTokens"`
	OutputTokens int64      `json:"outputTokens"`
}

// Provider is the LLM backend.
type Provider interface {
	Name() string
	Complete(ctx context.Context, system string, msgs []Message, tools []ToolDef, deep bool) (*Completion, error)
}

// NewProvider builds the configured backend (nil for "none").
func NewProvider(cfg config.AIConfig) Provider {
	apiKey := cfg.APIKey
	if cfg.APIKeyEnv != "" {
		if v := os.Getenv(cfg.APIKeyEnv); v != "" {
			apiKey = v
		}
	}
	switch cfg.Provider {
	case "anthropic":
		ep := cfg.Endpoint
		if ep == "" {
			ep = "https://api.anthropic.com"
		}
		return &anthropicProvider{endpoint: ep, apiKey: apiKey,
			model:     orModel(cfg.Model, "claude-sonnet-4-6"),
			modelDeep: orModel(cfg.ModelDeep, cfg.Model)}
	case "openai-compat", "azure-openai":
		return &openAIProvider{endpoint: cfg.Endpoint, apiKey: apiKey,
			model: orModel(cfg.Model, "gpt-4o"), modelDeep: orModel(cfg.ModelDeep, cfg.Model),
			azure: cfg.Provider == "azure-openai"}
	default:
		return nil
	}
}

func orModel(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

var httpClient = &http.Client{Timeout: 120 * time.Second}

// --- Anthropic Messages API ---

type anthropicProvider struct {
	endpoint, apiKey, model, modelDeep string
}

func (p *anthropicProvider) Name() string { return "anthropic" }

func (p *anthropicProvider) Complete(ctx context.Context, system string, msgs []Message,
	tools []ToolDef, deep bool) (*Completion, error) {
	model := p.model
	if deep {
		model = p.modelDeep
	}
	type aContent struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		ToolUseID string          `json:"tool_use_id,omitempty"`
		Content   string          `json:"content,omitempty"`
		IsError   bool            `json:"is_error,omitempty"`
	}
	type aMessage struct {
		Role    string     `json:"role"`
		Content []aContent `json:"content"`
	}
	var amsgs []aMessage
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			var content []aContent
			for _, tr := range m.ToolResults {
				content = append(content, aContent{Type: "tool_result",
					ToolUseID: tr.ID, Content: tr.Content, IsError: tr.IsError})
			}
			amsgs = append(amsgs, aMessage{Role: "user", Content: content})
		case "assistant":
			content := []aContent{}
			if m.Content != "" {
				content = append(content, aContent{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				content = append(content, aContent{Type: "tool_use",
					ID: tc.ID, Name: tc.Name, Input: tc.Input})
			}
			amsgs = append(amsgs, aMessage{Role: "assistant", Content: content})
		default:
			amsgs = append(amsgs, aMessage{Role: "user",
				Content: []aContent{{Type: "text", Text: m.Content}}})
		}
	}
	type aTool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	var atools []aTool
	for _, t := range tools {
		atools = append(atools, aTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	body := map[string]any{
		"model": model, "max_tokens": 2048, "system": system, "messages": amsgs,
	}
	if len(atools) > 0 {
		body["tools"] = atools
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/v1/messages",
		bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}
	var out struct {
		Content    []aContent `json:"content"`
		StopReason string     `json:"stop_reason"`
		Usage      struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	c := &Completion{StopReason: out.StopReason,
		InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}
	for _, blk := range out.Content {
		switch blk.Type {
		case "text":
			c.Text += blk.Text
		case "tool_use":
			c.ToolCalls = append(c.ToolCalls, ToolCall{ID: blk.ID, Name: blk.Name, Input: blk.Input})
		}
	}
	return c, nil
}

// --- OpenAI-compatible (incl. Azure, Ollama) ---

type openAIProvider struct {
	endpoint, apiKey, model, modelDeep string
	azure                              bool
}

func (p *openAIProvider) Name() string { return "openai-compat" }

func (p *openAIProvider) Complete(ctx context.Context, system string, msgs []Message,
	tools []ToolDef, deep bool) (*Completion, error) {
	model := p.model
	if deep {
		model = p.modelDeep
	}
	type oTool struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	type oToolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type oMessage struct {
		Role       string      `json:"role"`
		Content    string      `json:"content,omitempty"`
		ToolCalls  []oToolCall `json:"tool_calls,omitempty"`
		ToolCallID string      `json:"tool_call_id,omitempty"`
	}
	omsgs := []oMessage{{Role: "system", Content: system}}
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			for _, tr := range m.ToolResults {
				omsgs = append(omsgs, oMessage{Role: "tool", ToolCallID: tr.ID, Content: tr.Content})
			}
		case "assistant":
			om := oMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				var otc oToolCall
				otc.ID, otc.Type = tc.ID, "function"
				otc.Function.Name = tc.Name
				otc.Function.Arguments = string(tc.Input)
				om.ToolCalls = append(om.ToolCalls, otc)
			}
			omsgs = append(omsgs, om)
		default:
			omsgs = append(omsgs, oMessage{Role: "user", Content: m.Content})
		}
	}
	var otools []oTool
	for _, t := range tools {
		var ot oTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Schema
		otools = append(otools, ot)
	}
	body := map[string]any{"model": model, "messages": omsgs, "max_tokens": 2048}
	if len(otools) > 0 {
		body["tools"] = otools
	}
	raw, _ := json.Marshal(body)
	url := p.endpoint + "/v1/chat/completions"
	if p.azure {
		url = p.endpoint // azure deployment URL is complete
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	if p.azure {
		req.Header.Set("api-key", p.apiKey)
	} else {
		req.Header.Set("authorization", "Bearer "+p.apiKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string      `json:"content"`
				ToolCalls []oToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty response")
	}
	choice := out.Choices[0]
	c := &Completion{Text: choice.Message.Content, StopReason: choice.FinishReason,
		InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	for _, tc := range choice.Message.ToolCalls {
		c.ToolCalls = append(c.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments)})
	}
	return c, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
