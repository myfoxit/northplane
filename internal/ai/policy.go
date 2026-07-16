package ai

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/storage"
)

// Tool policy (SPEC §10.1 "configurable what the agent can do"): a
// per-tenant document an admin edits. It can only narrow the built-in
// gates — RBAC and the propose/approve queue stay in force; autoApprove
// is the single deliberate widening (skip the human approval for a
// named mutating tool) and is admin-only by construction.
type ToolPolicy struct {
	// Disabled tools are neither advertised to models nor executable.
	Disabled []string `json:"disabled,omitempty"`
	// AutoApprove lets a mutating tool execute without the human
	// approval queue (still RBAC-checked + audited).
	AutoApprove []string `json:"autoApprove,omitempty"`
	// MaxRounds caps the agent loop per user turn (default 10, max 24).
	MaxRounds int   `json:"maxRounds,omitempty"`
	Version   int64 `json:"version,omitempty"`
}

func policyKey(tenantID string) string { return "ai:policy:" + tenantID }

// Policy loads the tenant's tool policy (zero value = defaults).
func (s *Service) Policy(ctx context.Context, tenantID string) (*ToolPolicy, error) {
	var p ToolPolicy
	err := s.store.KVGet(ctx, policyKey(tenantID), &p)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	return &p, nil
}

// SavePolicy validates and stores the policy.
func (s *Service) SavePolicy(ctx context.Context, p *auth.Principal, pol *ToolPolicy) error {
	for _, name := range append(append([]string{}, pol.Disabled...), pol.AutoApprove...) {
		if s.byName[name] == nil {
			return fmt.Errorf("unknown tool %q", name)
		}
	}
	for _, name := range pol.AutoApprove {
		if t := s.byName[name]; t != nil && !t.Mutating {
			return fmt.Errorf("tool %q is read-only — autoApprove applies to mutating tools", name)
		}
	}
	if pol.MaxRounds < 0 || pol.MaxRounds > 24 {
		return fmt.Errorf("maxRounds must be between 0 (default) and 24")
	}
	pol.Version++
	if err := s.store.KVPut(ctx, policyKey(p.TenantID), pol); err != nil {
		return err
	}
	s.audit(ctx, p, "ai.policy.update", "", mustJSON(pol))
	return nil
}

func (pol *ToolPolicy) disabled(name string) bool {
	for _, d := range pol.Disabled {
		if d == name {
			return true
		}
	}
	return false
}

func (pol *ToolPolicy) autoApproved(name string) bool {
	for _, a := range pol.AutoApprove {
		if a == name {
			return true
		}
	}
	return false
}

// maxRounds resolves the loop cap.
func (pol *ToolPolicy) maxRounds() int {
	if pol.MaxRounds > 0 {
		return pol.MaxRounds
	}
	return 10
}

// ToolInfo describes one tool for the settings UI.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Mutating    bool   `json:"mutating"`
	AutoOK      bool   `json:"autoOk"`      // executes without approval by design
	Disabled    bool   `json:"disabled"`    // switched off by policy
	AutoApprove bool   `json:"autoApprove"` // policy skips the approval queue
}

// ToolCatalog lists every registered tool with its effective policy.
func (s *Service) ToolCatalog(ctx context.Context, tenantID string) ([]ToolInfo, error) {
	pol, err := s.Policy(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]ToolInfo, 0, len(s.tools))
	for i := range s.tools {
		t := &s.tools[i]
		out = append(out, ToolInfo{
			Name: t.Def.Name, Description: t.Def.Description,
			Mutating: t.Mutating, AutoOK: t.AutoOK,
			Disabled:    pol.disabled(t.Def.Name),
			AutoApprove: pol.autoApproved(t.Def.Name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// enabledToolDefs returns the defs advertised to the model: policy
// filter first, then the optional per-chat allow-list (which can only
// narrow further).
func (s *Service) enabledToolDefs(pol *ToolPolicy, allowed []string) []ToolDef {
	allow := map[string]bool{}
	for _, a := range allowed {
		allow[a] = true
	}
	var defs []ToolDef
	for i := range s.tools {
		t := &s.tools[i]
		if pol.disabled(t.Def.Name) {
			continue
		}
		if len(allowed) > 0 && !allow[t.Def.Name] {
			continue
		}
		defs = append(defs, t.Def)
	}
	return defs
}
