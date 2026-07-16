package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Streaming provider layer for the agent chat (SPEC §10.4 evolution).
// One neutral event vocabulary — closely modelled on the Vercel AI SDK
// UI-message stream, the de-facto shape for chat UIs — that every
// provider adapter emits and the agent loop consumes/forwards. The
// legacy non-streaming Provider stays for background jobs.

// StreamEventType enumerates provider/loop events.
type StreamEventType string

const (
	EvMessageStart   StreamEventType = "message-start"    // model turn begins
	EvTextStart      StreamEventType = "text-start"       // a text part begins
	EvTextDelta      StreamEventType = "text-delta"       // text increment
	EvTextEnd        StreamEventType = "text-end"         // text part complete
	EvReasoningStart StreamEventType = "reasoning-start"  // thinking part begins
	EvReasoningDelta StreamEventType = "reasoning-delta"  // thinking increment
	EvReasoningEnd   StreamEventType = "reasoning-end"    // thinking part complete
	EvToolInputStart StreamEventType = "tool-input-start" // model starts a tool call
	EvToolInputDelta StreamEventType = "tool-input-delta" // partial tool-args JSON
	EvToolInput      StreamEventType = "tool-input-available"
	EvToolOutput     StreamEventType = "tool-output-available"
	EvToolError      StreamEventType = "tool-output-error"
	EvStepStart      StreamEventType = "start-step"  // one provider round begins
	EvStepFinish     StreamEventType = "finish-step" // one provider round done
	EvFinish         StreamEventType = "finish"      // whole agent turn done
	EvError          StreamEventType = "error"
)

// StreamEvent is one incremental chunk. Field usage depends on Type;
// unused fields stay empty and are omitted on the wire.
type StreamEvent struct {
	Type StreamEventType `json:"type"`
	// ID identifies the part (text/reasoning) or the persisted message
	// (message-start carries the assistant message id).
	ID    string `json:"id,omitempty"`
	Delta string `json:"delta,omitempty"`
	// Tool call fields.
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	ErrorText  string          `json:"errorText,omitempty"`
	// Proposal state (northplane approval gate).
	Proposed bool   `json:"proposed,omitempty"`
	ActionID string `json:"actionId,omitempty"`
	// Finish metadata.
	StopReason string `json:"stopReason,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
	ChatID     string `json:"chatId,omitempty"`
	Model      string `json:"model,omitempty"`
}

// Usage counts one model turn (cumulative on finish).
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// StreamRequest is one provider round.
type StreamRequest struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	// Effort is a provider-specific reasoning-depth hint
	// ("low"|"medium"|"high"; "" = provider default).
	Effort string
}

// StreamResult accumulates what the round produced (the loop feeds tool
// calls back and persists the parts).
type StreamResult struct {
	Text       string
	Reasoning  string
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
	// Meta is provider round-trip state to attach to the assistant
	// Message (see Message.Meta).
	Meta json.RawMessage
}

// StreamSink receives events as they happen. Implementations must be
// cheap; the HTTP layer forwards to the client.
type StreamSink func(ev StreamEvent)

// StreamProvider is a provider that can stream one model round.
type StreamProvider interface {
	// StreamRound performs one streaming completion, emitting deltas to
	// sink and returning the accumulated result.
	StreamRound(ctx context.Context, req StreamRequest, sink StreamSink) (*StreamResult, error)
	// ListModels asks the provider for its live model catalog; adapters
	// return ErrNoModelListing when the API has none.
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ModelInfo is one selectable model.
type ModelInfo struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Curated bool   `json:"curated,omitempty"` // from the built-in catalog
}

// ErrNoModelListing marks providers without a listing endpoint.
var ErrNoModelListing = fmt.Errorf("provider has no model-listing endpoint")

// streamHTTPClient: no global timeout — streams are long-lived; per-call
// contexts bound connect + total time instead.
var streamHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

// sseScan reads Server-Sent Events from r and calls handle for every
// data payload (event name passed alongside; "" when the server sends
// bare data lines). Returns on EOF, ctx cancel or handler error.
func sseScan(ctx context.Context, r io.Reader, handle func(event string, data []byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var event string
	var data bytes.Buffer
	flush := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		payload := bytes.TrimSpace(bytes.Clone(data.Bytes()))
		ev := event
		event = ""
		data.Reset()
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return nil
		}
		return handle(ev, payload)
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			// Lines with other prefixes (":" comments/heartbeats, "id:",
			// "retry:") are ignored.
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return scanner.Err()
}

// doStream POSTs a JSON body and hands back the response for SSE
// consumption; non-2xx responses become descriptive errors.
func doStream(ctx context.Context, url string, headers map[string]string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := streamHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(msg)), 400))
	}
	return resp, nil
}

// doJSON performs a plain JSON request (model listings, connection tests).
func doJSON(ctx context.Context, method, url string, headers map[string]string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 400))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
