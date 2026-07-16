package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// openAIStreamAdapter speaks the OpenAI Chat Completions SSE dialect —
// the shared wire format of OpenAI, xAI, Mistral, DeepSeek, Groq,
// OpenRouter, Ollama (compat) and Google's OpenAI-compatible endpoint.
// Per-provider deviations ride on openAIQuirks from the catalog.
type openAIStreamAdapter struct {
	endpoint string // base URL incl. /v1 (no trailing slash)
	apiKey   string
	quirks   openAIQuirks
	baseURL  string // northplane base URL for OpenRouter attribution
}

// openAIMeta is the Meta payload this adapter round-trips.
type openAIMeta struct {
	// ReasoningDetails: OpenRouter requires these echoed unmodified and
	// in order on assistant turns so upstream thinking survives.
	ReasoningDetails []json.RawMessage `json:"reasoningDetails,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

func (p *openAIStreamAdapter) headers() map[string]string {
	h := map[string]string{}
	if p.apiKey != "" {
		h["authorization"] = "Bearer " + p.apiKey
	}
	if p.quirks.openRouterHeaders {
		ref := p.baseURL
		if ref == "" {
			ref = "https://github.com/northplane/northplane"
		}
		h["HTTP-Referer"] = ref
		h["X-Title"] = "Northplane"
	}
	return h
}

func (p *openAIStreamAdapter) buildBody(req StreamRequest) map[string]any {
	type oaMessage struct {
		Role             string            `json:"role"`
		Content          string            `json:"content"`
		ToolCalls        []oaToolCall      `json:"tool_calls,omitempty"`
		ToolCallID       string            `json:"tool_call_id,omitempty"`
		ReasoningContent string            `json:"reasoning_content,omitempty"`
		ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
	}
	msgs := []oaMessage{}
	if req.System != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "tool":
			for _, tr := range m.ToolResults {
				msgs = append(msgs, oaMessage{Role: "tool", ToolCallID: tr.ID, Content: tr.Content})
			}
		case "assistant":
			om := oaMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				var otc oaToolCall
				otc.ID, otc.Type = tc.ID, "function"
				otc.Function.Name = tc.Name
				otc.Function.Arguments = string(tc.Input)
				om.ToolCalls = append(om.ToolCalls, otc)
			}
			// DeepSeek: tool-loop assistant turns must carry
			// reasoning_content or the API rejects the request.
			if p.quirks.echoReasoningContent && m.Reasoning != "" {
				om.ReasoningContent = m.Reasoning
			}
			if p.quirks.echoReasoningDetails && len(m.Meta) > 0 {
				var meta openAIMeta
				if json.Unmarshal(m.Meta, &meta) == nil {
					om.ReasoningDetails = meta.ReasoningDetails
				}
			}
			msgs = append(msgs, om)
		default:
			msgs = append(msgs, oaMessage{Role: "user", Content: m.Content})
		}
	}
	body := map[string]any{
		"model": req.Model, "messages": msgs, "stream": true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.MaxTokens > 0 {
		if p.quirks.maxCompletionTokens {
			body["max_completion_tokens"] = req.MaxTokens
		} else {
			body["max_tokens"] = req.MaxTokens
		}
	}
	if len(req.Tools) > 0 {
		type oaTool struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		var tools []oaTool
		for _, t := range req.Tools {
			var ot oaTool
			ot.Type = "function"
			ot.Function.Name = t.Name
			ot.Function.Description = t.Description
			ot.Function.Parameters = t.Schema
			tools = append(tools, ot)
		}
		body["tools"] = tools
	}
	if req.Effort != "" && p.quirks.reasoningEffortParam {
		body["reasoning_effort"] = req.Effort
	}
	return body
}

// StreamRound implements StreamProvider.
func (p *openAIStreamAdapter) StreamRound(ctx context.Context, req StreamRequest, sink StreamSink) (*StreamResult, error) {
	resp, err := doStream(ctx, p.endpoint+"/chat/completions", p.headers(), p.buildBody(req))
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	defer resp.Body.Close()

	res := &StreamResult{}
	type callAcc struct {
		id, name string
		args     strings.Builder
		started  bool
	}
	calls := map[int]*callAcc{}
	callOrder := []int{}
	textStarted, reasoningStarted := false, false
	const textID, reasoningID = "txt_0", "rsn_0"
	// OpenRouter reasoning_details accumulation, merged by index.
	type rdAcc struct {
		fields map[string]json.RawMessage
		text   map[string]*strings.Builder // text|summary|data fragments
	}
	rds := map[int]*rdAcc{}
	rdOrder := []int{}

	err = sseScan(ctx, resp.Body, func(_ string, data []byte) error {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string            `json:"content"`
					ReasoningContent string            `json:"reasoning_content"`
					Reasoning        string            `json:"reasoning"`
					ToolCalls        []json.RawMessage `json:"tool_calls"`
					ReasoningDetails []json.RawMessage `json:"reasoning_details"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			return nil // tolerate keep-alives/unknown chunks
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return fmt.Errorf("provider: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			res.Usage.InputTokens = chunk.Usage.PromptTokens
			res.Usage.OutputTokens = chunk.Usage.CompletionTokens
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		c := chunk.Choices[0]
		if r := c.Delta.ReasoningContent + c.Delta.Reasoning; r != "" {
			if !reasoningStarted {
				reasoningStarted = true
				sink(StreamEvent{Type: EvReasoningStart, ID: reasoningID})
			}
			res.Reasoning += r
			sink(StreamEvent{Type: EvReasoningDelta, ID: reasoningID, Delta: r})
		}
		if c.Delta.Content != "" {
			if reasoningStarted && !textStarted {
				sink(StreamEvent{Type: EvReasoningEnd, ID: reasoningID})
			}
			if !textStarted {
				textStarted = true
				sink(StreamEvent{Type: EvTextStart, ID: textID})
			}
			res.Text += c.Delta.Content
			sink(StreamEvent{Type: EvTextDelta, ID: textID, Delta: c.Delta.Content})
		}
		for _, rawTC := range c.Delta.ToolCalls {
			var frag struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if err := json.Unmarshal(rawTC, &frag); err != nil {
				continue
			}
			acc := calls[frag.Index]
			if acc == nil {
				acc = &callAcc{}
				calls[frag.Index] = acc
				callOrder = append(callOrder, frag.Index)
			}
			if frag.ID != "" {
				acc.id = frag.ID
			}
			if frag.Function.Name != "" {
				acc.name = frag.Function.Name
			}
			if !acc.started && acc.name != "" {
				acc.started = true
				sink(StreamEvent{Type: EvToolInputStart, ToolCallID: acc.id, ToolName: acc.name})
			}
			if frag.Function.Arguments != "" {
				acc.args.WriteString(frag.Function.Arguments)
				sink(StreamEvent{Type: EvToolInputDelta, ToolCallID: acc.id, Delta: frag.Function.Arguments})
			}
		}
		for _, rawRD := range c.Delta.ReasoningDetails {
			var frag map[string]json.RawMessage
			if err := json.Unmarshal(rawRD, &frag); err != nil {
				continue
			}
			idx := 0
			if v, ok := frag["index"]; ok {
				_ = json.Unmarshal(v, &idx)
			}
			acc := rds[idx]
			if acc == nil {
				acc = &rdAcc{fields: map[string]json.RawMessage{},
					text: map[string]*strings.Builder{}}
				rds[idx] = acc
				rdOrder = append(rdOrder, idx)
			}
			for k, v := range frag {
				switch k {
				case "text", "summary", "data":
					var s string
					if json.Unmarshal(v, &s) == nil {
						b := acc.text[k]
						if b == nil {
							b = &strings.Builder{}
							acc.text[k] = b
						}
						b.WriteString(s)
					}
				default:
					acc.fields[k] = v
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, idx := range callOrder {
		acc := calls[idx]
		args := strings.TrimSpace(acc.args.String())
		if args == "" {
			args = "{}"
		}
		if acc.id == "" {
			acc.id = fmt.Sprintf("call_%d", idx)
		}
		if acc.name == "" {
			continue // fragment never completed
		}
		input := json.RawMessage(args)
		res.ToolCalls = append(res.ToolCalls, ToolCall{ID: acc.id, Name: acc.name, Input: input})
		sink(StreamEvent{Type: EvToolInput, ToolCallID: acc.id, ToolName: acc.name, Input: input})
	}
	if textStarted {
		sink(StreamEvent{Type: EvTextEnd, ID: textID})
	} else if reasoningStarted {
		sink(StreamEvent{Type: EvReasoningEnd, ID: reasoningID})
	}
	if len(res.ToolCalls) > 0 {
		res.StopReason = "tool_use"
	} else if res.StopReason == "" {
		res.StopReason = "end_turn"
	}
	if len(rdOrder) > 0 {
		sort.Ints(rdOrder)
		var details []json.RawMessage
		for _, idx := range rdOrder {
			acc := rds[idx]
			obj := map[string]json.RawMessage{}
			for k, v := range acc.fields {
				obj[k] = v
			}
			for k, b := range acc.text {
				raw, _ := json.Marshal(b.String())
				obj[k] = raw
			}
			raw, _ := json.Marshal(obj)
			details = append(details, raw)
		}
		meta, _ := json.Marshal(openAIMeta{ReasoningDetails: details})
		res.Meta = meta
	}
	return res, nil
}

// ListModels queries the OpenAI-shaped GET {base}/models.
func (p *openAIStreamAdapter) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := doJSON(ctx, "GET", p.endpoint+"/models", p.headers(), nil, &out); err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		id := strings.TrimPrefix(m.ID, "models/")
		models = append(models, ModelInfo{ID: id})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
