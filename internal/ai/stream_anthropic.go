package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicStream speaks the native Anthropic Messages API with SSE
// streaming, tool use and adaptive thinking. Raw assistant content
// blocks (incl. thinking signatures) are preserved via Message.Meta so
// tool loops replay them verbatim, as the API requires.
type anthropicStream struct {
	endpoint string
	apiKey   string
}

// anthropicMeta is the Meta payload this adapter round-trips.
type anthropicMeta struct {
	AnthropicContent []json.RawMessage `json:"anthropicContent,omitempty"`
}

// adaptiveThinkingModels: model-id prefixes that accept
// thinking:{type:"adaptive"} (4.6+ families; Fable/Mythos are always-on
// but accept an explicit adaptive too).
var adaptiveThinkingPrefixes = []string{
	"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8",
	"claude-sonnet-4-6", "claude-sonnet-5",
	"claude-fable", "claude-mythos",
}

func supportsAdaptiveThinking(model string) bool {
	for _, p := range adaptiveThinkingPrefixes {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

// effortValues accepted by output_config.effort.
var anthropicEfforts = map[string]string{
	"low": "low", "medium": "medium", "high": "high",
	"xhigh": "xhigh", "max": "max",
}

func (p *anthropicStream) buildBody(req StreamRequest, stream bool) map[string]any {
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
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	var amsgs []aMessage
	for _, m := range req.Messages {
		switch m.Role {
		case "tool":
			var content []aContent
			for _, tr := range m.ToolResults {
				content = append(content, aContent{Type: "tool_result",
					ToolUseID: tr.ID, Content: tr.Content, IsError: tr.IsError})
			}
			amsgs = append(amsgs, aMessage{Role: "user", Content: content})
		case "assistant":
			// Replay the raw blocks when we have them (same-dialect turn):
			// thinking blocks must go back unchanged or tool loops 400.
			var meta anthropicMeta
			if len(m.Meta) > 0 {
				_ = json.Unmarshal(m.Meta, &meta)
			}
			if len(meta.AnthropicContent) > 0 {
				amsgs = append(amsgs, aMessage{Role: "assistant", Content: meta.AnthropicContent})
				continue
			}
			var content []aContent
			if m.Content != "" {
				content = append(content, aContent{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				in := tc.Input
				if len(in) == 0 {
					in = json.RawMessage(`{}`)
				}
				content = append(content, aContent{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: in})
			}
			if len(content) == 0 {
				continue
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
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	body := map[string]any{
		"model": req.Model, "max_tokens": maxTokens,
		"system": req.System, "messages": amsgs,
	}
	if stream {
		body["stream"] = true
	}
	if len(req.Tools) > 0 {
		var atools []aTool
		for _, t := range req.Tools {
			atools = append(atools, aTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
		}
		body["tools"] = atools
	}
	if supportsAdaptiveThinking(req.Model) {
		body["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
	}
	if e, ok := anthropicEfforts[req.Effort]; ok && supportsAdaptiveThinking(req.Model) {
		body["output_config"] = map[string]any{"effort": e}
	}
	return body
}

// StreamRound implements StreamProvider.
func (p *anthropicStream) StreamRound(ctx context.Context, req StreamRequest, sink StreamSink) (*StreamResult, error) {
	body := p.buildBody(req, true)
	resp, err := doStream(ctx, p.endpoint+"/v1/messages", map[string]string{
		"x-api-key":         p.apiKey,
		"anthropic-version": "2023-06-01",
	}, body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()

	res := &StreamResult{}
	// Per-index accumulation of raw content blocks for verbatim replay.
	type blockAcc struct {
		typ       string
		id, name  string // tool_use
		text      strings.Builder
		partialIn strings.Builder // tool input JSON
		signature strings.Builder // thinking signature
		partID    string          // UI part id
	}
	blocks := map[int]*blockAcc{}
	order := []int{}

	err = sseScan(ctx, resp.Body, func(event string, data []byte) error {
		var head struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			return nil // tolerate unknown payloads
		}
		switch head.Type {
		case "message_start":
			var ms struct {
				Message struct {
					Usage struct {
						InputTokens int64 `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			_ = json.Unmarshal(data, &ms)
			res.Usage.InputTokens = ms.Message.Usage.InputTokens
		case "content_block_start":
			var cb struct {
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			_ = json.Unmarshal(data, &cb)
			acc := &blockAcc{typ: cb.ContentBlock.Type, id: cb.ContentBlock.ID,
				name: cb.ContentBlock.Name, partID: fmt.Sprintf("blk_%d", head.Index)}
			blocks[head.Index] = acc
			order = append(order, head.Index)
			switch acc.typ {
			case "text":
				sink(StreamEvent{Type: EvTextStart, ID: acc.partID})
			case "thinking":
				sink(StreamEvent{Type: EvReasoningStart, ID: acc.partID})
			case "tool_use":
				sink(StreamEvent{Type: EvToolInputStart, ToolCallID: acc.id, ToolName: acc.name})
			}
		case "content_block_delta":
			acc := blocks[head.Index]
			if acc == nil {
				return nil
			}
			var d struct {
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					Thinking    string `json:"thinking"`
					PartialJSON string `json:"partial_json"`
					Signature   string `json:"signature"`
				} `json:"delta"`
			}
			_ = json.Unmarshal(data, &d)
			switch d.Delta.Type {
			case "text_delta":
				acc.text.WriteString(d.Delta.Text)
				sink(StreamEvent{Type: EvTextDelta, ID: acc.partID, Delta: d.Delta.Text})
			case "thinking_delta":
				acc.text.WriteString(d.Delta.Thinking)
				sink(StreamEvent{Type: EvReasoningDelta, ID: acc.partID, Delta: d.Delta.Thinking})
			case "input_json_delta":
				acc.partialIn.WriteString(d.Delta.PartialJSON)
				sink(StreamEvent{Type: EvToolInputDelta, ToolCallID: acc.id, Delta: d.Delta.PartialJSON})
			case "signature_delta":
				acc.signature.WriteString(d.Delta.Signature)
			}
		case "content_block_stop":
			acc := blocks[head.Index]
			if acc == nil {
				return nil
			}
			switch acc.typ {
			case "text":
				sink(StreamEvent{Type: EvTextEnd, ID: acc.partID})
			case "thinking":
				sink(StreamEvent{Type: EvReasoningEnd, ID: acc.partID})
			case "tool_use":
				input := json.RawMessage(acc.partialIn.String())
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				res.ToolCalls = append(res.ToolCalls, ToolCall{ID: acc.id, Name: acc.name, Input: input})
				sink(StreamEvent{Type: EvToolInput, ToolCallID: acc.id, ToolName: acc.name, Input: input})
			}
		case "message_delta":
			var md struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			_ = json.Unmarshal(data, &md)
			if md.Delta.StopReason != "" {
				res.StopReason = md.Delta.StopReason
			}
			res.Usage.OutputTokens = md.Usage.OutputTokens
		case "error":
			var ae struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &ae)
			return fmt.Errorf("anthropic: %s: %s", ae.Error.Type, ae.Error.Message)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Assemble text/reasoning and the verbatim replay blocks.
	var raws []json.RawMessage
	for _, idx := range order {
		acc := blocks[idx]
		switch acc.typ {
		case "text":
			res.Text += acc.text.String()
			raw, _ := json.Marshal(map[string]any{"type": "text", "text": acc.text.String()})
			raws = append(raws, raw)
		case "thinking":
			res.Reasoning += acc.text.String()
			blk := map[string]any{"type": "thinking", "thinking": acc.text.String()}
			if s := acc.signature.String(); s != "" {
				blk["signature"] = s
			}
			raw, _ := json.Marshal(blk)
			raws = append(raws, raw)
		case "tool_use":
			input := acc.partialIn.String()
			if input == "" {
				input = "{}"
			}
			raw, _ := json.Marshal(map[string]any{"type": "tool_use",
				"id": acc.id, "name": acc.name, "input": json.RawMessage(input)})
			raws = append(raws, raw)
		default:
			// Unknown block types (fallback markers, compaction) are
			// skipped — forward nothing, replay nothing.
		}
	}
	if res.StopReason == "refusal" {
		// Safety classifiers declined (Fable 5): surface a readable note.
		if res.Text == "" {
			res.Text = "Die Anfrage wurde vom Modell-Sicherheitsfilter abgelehnt (stop_reason: refusal)."
		}
	}
	if len(raws) > 0 {
		meta, _ := json.Marshal(anthropicMeta{AnthropicContent: raws})
		res.Meta = meta
	}
	return res, nil
}

// ListModels queries GET /v1/models.
func (p *anthropicStream) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	err := doJSON(ctx, "GET", p.endpoint+"/v1/models?limit=100", map[string]string{
		"x-api-key":         p.apiKey,
		"anthropic-version": "2023-06-01",
	}, nil, &out)
	if err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		models = append(models, ModelInfo{ID: m.ID, Label: m.DisplayName})
	}
	return models, nil
}
