package mqttin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/cel-go/cel"

	"github.com/northplane/northplane/internal/model"
)

const (
	// maxSummaryRunes caps the plain-text summary.
	maxSummaryRunes = 200
	// maxPayloadBytes caps the archived raw payload (64 KiB).
	maxPayloadBytes = 64 << 10
	// celCostLimit mirrors internal/api/ingress.go normalize().
	celCostLimit = 5000
	// default per-source rate limit, mirrors internal/api/ingress.go
	// allowRate (50 events/s, burst 200).
	defaultRate  = 50.0
	defaultBurst = 200
)

// buildEvent converts one MQTT message into the canonical NormEvent
// (SPEC §7.5), replicating the webhook ingress normalisation
// (internal/api/ingress.go normalize): a non-empty CEL mapping is applied
// to JSON payloads, JSON already in normal form passes through, anything
// else becomes a plain-text event. The raw payload is always archived
// (capped), the source labels are merged in (source wins, matching the
// HTTP path's precedence) and the message topic becomes a label.
func buildEvent(src *model.EventSource, topic string, payload []byte, now time.Time) (*model.NormEvent, error) {
	norm, err := normalize(src, payload)
	if err != nil {
		return nil, err
	}
	norm.Source = src.ID
	norm.ReceivedAt = now.UTC()
	norm.Labels = norm.Labels.Merge(src.Labels)
	norm.Labels["topic"] = topic
	norm.Payload = rawPayload(payload)
	if !norm.Severity.Valid() {
		norm.Severity = defaultSeverity(src)
	}
	return norm, nil
}

// normalize picks the conversion path: CEL mapping for JSON payloads,
// normal-form passthrough, or plain text.
func normalize(src *model.EventSource, payload []byte) (*model.NormEvent, error) {
	if len(src.Mapping) > 0 {
		if json.Valid(payload) {
			return applyMapping(src.Mapping, payload)
		}
		// Mapping configured but the payload is not JSON: fall back to a
		// plain-text event instead of dropping the message (the caller
		// logs the fallback).
		return plainText(payload), nil
	}
	var norm model.NormEvent
	if err := json.Unmarshal(payload, &norm); err == nil &&
		(norm.Summary != "" || norm.Severity != "") {
		// Normal-form passthrough, mirroring the webhook ingest.
		if norm.Summary == "" {
			norm.Summary = "event from " + src.Name
		}
		return &norm, nil
	}
	return plainText(payload), nil
}

// applyMapping evaluates the source's CEL mapping over the decoded JSON
// payload. Semantics replicate internal/api/ingress.go normalize(): one
// expression per NormEvent field against a single variable `payload`
// (dyn), cost-limited; "labels.<key>" fields populate labels.
func applyMapping(mapping map[string]string, payload []byte) (*model.NormEvent, error) {
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("payload must be JSON: %w", err)
	}
	get := func(expr string) (any, error) {
		env, err := cel.NewEnv(cel.Variable("payload", cel.DynType))
		if err != nil {
			return nil, err
		}
		ast, iss := env.Compile(expr)
		if iss.Err() != nil {
			return nil, iss.Err()
		}
		prg, err := env.Program(ast, cel.CostLimit(celCostLimit))
		if err != nil {
			return nil, err
		}
		out, _, err := prg.Eval(map[string]any{"payload": doc})
		if err != nil {
			return nil, err
		}
		return out.Value(), nil
	}
	var norm model.NormEvent
	for field, expr := range mapping {
		v, err := get(expr)
		if err != nil {
			return nil, fmt.Errorf("mapping %s: %w", field, err)
		}
		switch field {
		case "summary":
			norm.Summary = fmt.Sprint(v)
		case "severity":
			norm.Severity = model.Severity(fmt.Sprint(v))
		case "dedupKey":
			norm.DedupKey = fmt.Sprint(v)
		case "resolve":
			b, _ := v.(bool)
			norm.Resolve = b
		default:
			if labelKey, ok := strings.CutPrefix(field, "labels."); ok {
				if norm.Labels == nil {
					norm.Labels = model.Labels{}
				}
				norm.Labels[labelKey] = fmt.Sprint(v)
			}
		}
	}
	return &norm, nil
}

// plainText builds the fallback event for non-JSON payloads: the first
// maxSummaryRunes of the message body become the summary. Severity is
// left empty so buildEvent applies the configured default.
func plainText(payload []byte) *model.NormEvent {
	s := capRunes(strings.TrimSpace(string(payload)), maxSummaryRunes)
	if s == "" {
		s = "(empty message)"
	}
	return &model.NormEvent{Summary: s}
}

// defaultSeverity reads Config["severity"], defaulting to info.
func defaultSeverity(src *model.EventSource) model.Severity {
	if sev := model.Severity(strings.TrimSpace(src.Config["severity"])); sev.Valid() {
		return sev
	}
	return model.SevInfo
}

// rawPayload archives the message body in the NormEvent, capped at
// maxPayloadBytes. Valid JSON is kept verbatim; anything else (plain
// text, or JSON broken by the cap) is stored as a JSON string so the
// enclosing NormEvent always marshals.
func rawPayload(payload []byte) json.RawMessage {
	if len(payload) > maxPayloadBytes {
		payload = payload[:maxPayloadBytes]
	}
	if json.Valid(payload) {
		return json.RawMessage(bytes.Clone(payload))
	}
	q, _ := json.Marshal(string(payload)) // quotes; invalid UTF-8 is replaced
	return q
}

// capRunes truncates s to at most n runes (UTF-8 safe).
func capRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// tokenBucket rate-limits one source, mirroring the HTTP ingress bucket
// (internal/api/ingress.go allowRate).
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

// newBucket builds a bucket; rate <= 0 and burst <= 0 fall back to the
// ingress defaults (50 events/s, burst 200).
func newBucket(rate float64, burst int) *tokenBucket {
	if rate <= 0 {
		rate = defaultRate
	}
	if burst <= 0 {
		burst = defaultBurst
	}
	return &tokenBucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

// allow consumes one token if available, refilling by elapsed time.
func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
