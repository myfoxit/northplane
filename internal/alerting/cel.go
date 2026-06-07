// Package alerting turns events into alerts (SPEC §9): CEL-matched
// rules with pending periods, dedup, auto-close and heartbeat detection,
// deterministic correlation into incidents, and suppression.
package alerting

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/northplane/northplane/internal/model"
)

// celEnv is the sandboxed expression environment (ADR-06): no I/O, a
// cost limit, and exactly one input — `event`.
var celEnv = func() *cel.Env {
	env, err := cel.NewEnv(
		cel.Variable("event", cel.DynType),
	)
	if err != nil {
		panic(err)
	}
	return env
}()

// CompiledRule caches the parsed program.
type CompiledRule struct {
	Rule    *model.AlertRule
	program cel.Program
}

// CompileRule validates and compiles the match expression.
func CompileRule(r *model.AlertRule) (*CompiledRule, error) {
	cr := &CompiledRule{Rule: r}
	if r.Heartbeat != nil {
		if r.Match != "" {
			return nil, fmt.Errorf("rule %q: match and heartbeat are mutually exclusive", r.Name)
		}
		if r.Heartbeat.Source == "" || r.Heartbeat.ExpectEvery <= 0 {
			return nil, fmt.Errorf("rule %q: heartbeat needs source and expectEvery", r.Name)
		}
		return cr, nil
	}
	if r.Match == "" {
		return nil, fmt.Errorf("rule %q: match expression required", r.Name)
	}
	ast, iss := celEnv.Compile(r.Match)
	if iss.Err() != nil {
		return nil, fmt.Errorf("rule %q: %w", r.Name, iss.Err())
	}
	prg, err := celEnv.Program(ast,
		cel.EvalOptions(cel.OptOptimize),
		cel.CostLimit(10000), // sandbox cost budget (SPEC §9.2)
	)
	if err != nil {
		return nil, fmt.Errorf("rule %q: %w", r.Name, err)
	}
	cr.program = prg
	return cr, nil
}

// Matches evaluates the rule against an event view. A missing key is a
// legitimate no-match (event shapes vary by type); any other evaluation
// error is returned so the engine can log it and — crucially — NOT treat
// the event as a clear (which would let a broken rule resolve real
// alerts).
func (cr *CompiledRule) Matches(view map[string]any) (bool, error) {
	if cr.program == nil {
		return false, nil // heartbeat rules never match events directly
	}
	out, _, err := cr.program.Eval(map[string]any{"event": view})
	if err != nil {
		if isMissingKeyErr(err) {
			return false, nil
		}
		return false, fmt.Errorf("rule %q eval: %w", cr.Rule.Name, err)
	}
	b, ok := out.(ref.Val)
	if !ok {
		return false, fmt.Errorf("rule %q: non-boolean result", cr.Rule.Name)
	}
	if b == types.True {
		return true, nil
	}
	return false, nil
}

// isMissingKeyErr reports whether a CEL runtime error is just an absent
// field/key/index — the normal "this event type doesn't have that field"
// case — as opposed to a genuine type or evaluation error.
func isMissingKeyErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "no such key") ||
		strings.Contains(s, "no such attribute") ||
		strings.Contains(s, "no such field") ||
		strings.Contains(s, "index out of range")
}

// EventView renders the CEL-visible shape of an event (documented in
// the rule editor):
//
//	event.type, event.severity, event.ts, event.objectId, event.source,
//	event.labels.<k>, event.summary, event.output, event.state,
//	event.stateType, event.kind, event.host, event.object, event.metric,
//	event.attempt, event.payload.<raw…>
func EventView(e *model.Event) map[string]any {
	view := map[string]any{
		"type":     string(e.Type),
		"severity": string(e.Severity),
		"ts":       e.TS.Format(time.RFC3339),
		"objectId": e.ObjectID,
		"source":   e.SourceID,
		"labels":   map[string]any{},
	}
	var payload map[string]any
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &payload)
	}
	view["payload"] = orMap(payload)
	if payload != nil {
		if l, ok := payload["labels"].(map[string]any); ok {
			view["labels"] = l
		}
		for src, dst := range map[string]string{
			"object": "object", "host": "host", "kind": "kind",
			"from": "fromState", "to": "state", "stateType": "stateType",
			"output": "output", "summary": "summary", "metric": "metric",
			"dedupKey": "dedupKey",
		} {
			if v, ok := payload[src]; ok {
				view[dst] = v
			}
		}
		if v, ok := payload["attempt"].(float64); ok {
			view["attempt"] = int64(v)
		}
	}
	// Ingress NormEvents carry severity/summary at the top level of the
	// payload already mapped above; expose state == severity for them.
	if _, ok := view["state"]; !ok {
		view["state"] = severityState(e.Severity)
	}
	if _, ok := view["summary"]; !ok {
		if out, ok := view["output"]; ok {
			view["summary"] = out
		} else {
			view["summary"] = ""
		}
	}
	return view
}

func orMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func severityState(s model.Severity) string {
	switch s {
	case model.SevCritical:
		return "CRITICAL"
	case model.SevWarning:
		return "WARNING"
	case model.SevOK:
		return "OK"
	default:
		return "INFO"
	}
}
