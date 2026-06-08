// Package mcp exposes the Northplane tool surface over the Model
// Context Protocol (SPEC §10.3) using the official Go SDK: stdio for
// local agents (`northplaned mcp`) and Streamable HTTP at /mcp. Tokens
// are ordinary Northplane API tokens — the AI stays a privilege-less,
// audited API client (P2).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/northplane/northplane/internal/ai"
	"github.com/northplane/northplane/internal/auth"
)

// Prompts shipped with the server (SPEC §10.3).
var prompts = []*sdk.Prompt{
	{Name: "morning-briefing",
		Description: "Summarise the overnight state: new problems, incidents, SLA risks."},
	{Name: "incident-triage",
		Description: "Triage the currently open incidents: cluster, name, rank by impact."},
	{Name: "config-review",
		Description: "Review monitoring coverage: untemplated objects, missing checks, noisy rules."},
}

var promptText = map[string]string{
	"morning-briefing": "Use get_overview, get_alerts and get_incidents to compile a concise morning briefing: what broke since yesterday evening, what is still open, who is on call today (who_is_oncall). End with the top 3 action items.",
	"incident-triage":  "List open incidents (get_incidents) and their alerts. For each: name the likely common cause (use explain_alert on a representative alert), the blast radius, and whether it can be acknowledged or needs escalation.",
	"config-review":    "Search objects (search_objects) and review the monitoring configuration: objects without templates, hosts without services, rules that opened the most alerts (get_alerts). Propose concrete improvements as bundle fragments via propose_config_change.",
}

// Build assembles the MCP server around an authenticated principal.
// Every session is bound to one principal; tool execution flows through
// the same RunTool gate as the in-UI assistant.
func Build(svc *ai.Service, principal *auth.Principal, version string) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{
		Name: "northplane", Title: "Northplane Monitoring", Version: version,
	}, nil)

	// Tools that perform destructive (non-additive) updates to the
	// environment when executed. The SDK's DestructiveHint is only
	// meaningful for write tools, so this set is intersected with the
	// registry's Mutating flag below (it never marks a read tool).
	destructive := map[string]bool{
		"apply_config_change": true,
		"create_downtime":     true,
		"create_silence":      true,
	}

	for _, tool := range svc.Tools() {
		t := tool // capture
		desc := t.Def.Description
		if t.Mutating && !t.AutoOK {
			desc += " (mutating: returns a proposal that requires human approval)"
		} else if t.Mutating {
			desc += " (mutating: executes immediately, audited)"
		}
		// Annotations are advisory hints (MCP 2025-06-18). Derive read-vs-write
		// straight from the registry's Mutating flag so the hint can never
		// drift from the actual tool behaviour. AutoOK mutating tools (ack,
		// recheck) are idempotent — repeating them has no extra effect.
		ann := &sdk.ToolAnnotations{ReadOnlyHint: !t.Mutating}
		if t.Mutating {
			if destructive[t.Def.Name] {
				yes := true
				ann.DestructiveHint = &yes
			} else {
				no := false
				ann.DestructiveHint = &no
			}
			if t.AutoOK {
				ann.IdempotentHint = true
			}
		}
		server.AddTool(&sdk.Tool{
			Name:        t.Def.Name,
			Description: desc,
			InputSchema: json.RawMessage(t.Def.Schema),
			Annotations: ann,
		}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			args, err := json.Marshal(req.Params.Arguments)
			if err != nil {
				args = []byte("{}")
			}
			result, _, err := svc.RunTool(ctx, principal, t.Def.Name, args)
			res := &sdk.CallToolResult{}
			if err != nil {
				res.IsError = true
				res.Content = []sdk.Content{&sdk.TextContent{Text: "error: " + err.Error()}}
				return res, nil
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			res.Content = []sdk.Content{&sdk.TextContent{Text: string(raw)}}
			return res, nil
		})
	}

	for _, p := range prompts {
		prompt := p
		server.AddPrompt(prompt, func(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{
				Description: prompt.Description,
				Messages: []*sdk.PromptMessage{{
					Role:    "user",
					Content: &sdk.TextContent{Text: promptText[prompt.Name]},
				}},
			}, nil
		})
	}
	return server
}

// RunStdio serves a local stdio session (`northplaned mcp` — Claude
// Desktop & friends). The principal comes from a token passed via env.
func RunStdio(ctx context.Context, svc *ai.Service, principal *auth.Principal, version string) error {
	server := Build(svc, principal, version)
	return server.Run(ctx, &sdk.StdioTransport{})
}

// HTTPHandler serves Streamable HTTP at /mcp. Authentication: standard
// Northplane bearer tokens resolved per request (SPEC §10.3: tokens =
// normale API-Tokens mit Scopes). One SHARED SDK handler holds the
// session table — sessions span requests (initialize → tools/*), each
// bound at creation to the authenticated principal. A session-id→actor
// binding prevents one token riding another token's session.
func HTTPHandler(svc *ai.Service, authn *auth.Authenticator, version string) http.Handler {
	var sessions sync.Map // session id → actor id
	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		// Called for NEW sessions only; the session keeps this server
		// (and its principal) for its lifetime.
		principal, err := authn.Authenticate(r)
		if err != nil || principal == nil {
			return nil // SDK answers 400
		}
		return Build(svc, principal, version)
	}, nil)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := authn.Authenticate(r)
		if err != nil || principal == nil {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="/api/v1/whoami"`)
			http.Error(w, "MCP requires a Northplane API token", http.StatusUnauthorized)
			return
		}
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			if actor, ok := sessions.Load(sid); ok && actor != principal.ActorID {
				http.Error(w, "session belongs to another token", http.StatusForbidden)
				return
			}
			if r.Method == http.MethodDelete {
				sessions.Delete(sid)
			}
		}
		handler.ServeHTTP(w, r)
		if sid := w.Header().Get("Mcp-Session-Id"); sid != "" {
			sessions.Store(sid, principal.ActorID)
		}
	})
}

var _ = fmt.Sprintf
