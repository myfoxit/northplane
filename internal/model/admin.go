package model

import (
	"strings"
	"time"
)

// Tenant is the top-level isolation unit (SPEC §13.3). The bootstrap
// tenant is "default".
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Disabled  bool      `json:"disabled,omitempty"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DefaultTenant is created by migrations; single-tenant installs never
// see another one (A-15.12).
const DefaultTenant = "00000000-0000-7000-8000-000000000001"

// User is a human principal. SSO users are provisioned on first login
// (OIDC claims); local users exist for break-glass only (§13.2).
//
// Roles are the role names bound to the account (§11.2). For local users
// they are authoritative — local login expands them into the session. For
// SSO users the effective roles are derived from IdP groups at login
// (oidc.mapGroups), so Roles is normally empty on those rows.
type User struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Email      string     `json:"email"`
	Subject    string     `json:"subject,omitempty"`  // OIDC iss+sub
	TenantID   string     `json:"tenantId,omitempty"` // home tenant; local login lands here (empty = Default)
	Local      bool       `json:"local,omitempty"`
	PassHash   string     `json:"-"` // argon2id, local users only — never serialised
	Roles      []string   `json:"roles,omitempty"`
	Disabled   bool       `json:"disabled,omitempty"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	Version    int64      `json:"version"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// Preferences are per-actor UI settings (P1 parity: every knob the UI
// offers is equally readable and writable via API and MCP). Stored as
// one generic resource per actor (resource name = actor ID), served at
// /api/v1/users/{id|me}/preferences.
type Preferences struct {
	// RefreshIntervalMs is the live-view polling cadence in milliseconds.
	// 0 disables auto-refresh (manual only); nil/absent = client default.
	RefreshIntervalMs *int `json:"refreshIntervalMs,omitempty"`
	// Extra carries additional client settings without a schema change.
	Extra map[string]string `json:"extra,omitempty"`
}

// Permission is "resource:action" ("objects:read", "alerts:ack",
// "config:write", "admin:*" — SPEC §11.2).
type Permission string

// Implies reports whether p grants want, honouring wildcards
// ("admin:*" ⊇ "admin:ai"; "*:*" ⊇ everything).
func (p Permission) Implies(want Permission) bool {
	if p == want || p == "*:*" || p == "*" {
		return true
	}
	pr, pa, ok := cutPerm(string(p))
	wr, wa, ok2 := cutPerm(string(want))
	if !ok || !ok2 {
		return false
	}
	return (pr == "*" || pr == wr) && (pa == "*" || pa == wa)
}

func cutPerm(s string) (string, string, bool) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

// RoleScope confines a role to tenant + folder subtree + optional label
// selector (SPEC §11.2).
type RoleScope struct {
	TenantID string `json:"tenantId,omitempty"` // empty = all tenants (system roles)
	Folder   string `json:"folder,omitempty"`   // subtree prefix, "" or "/" = all
	Selector string `json:"selector,omitempty"` // label selector
}

// Role bundles permissions; roles nest via Includes (inheritance,
// A-15.07). IdP groups map onto roles (IdPGroups).
type Role struct {
	ID          string       `json:"id"`
	TenantID    string       `json:"tenantId"`
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
	Scope       RoleScope    `json:"scope"`
	Includes    []string     `json:"includes,omitempty"` // nested role names
	IdPGroups   []string     `json:"idpGroups,omitempty"`
	System      bool         `json:"system,omitempty"` // built-in, immutable
	Version     int64        `json:"version"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

// Built-in roles created by migrations.
var BuiltinRoles = []Role{
	{Name: "admin", System: true, Permissions: []Permission{"*:*"}},
	{Name: "operator", System: true, Permissions: []Permission{
		"objects:read", "objects:write", "checks:run", "alerts:read", "alerts:ack",
		"alerts:write", "incidents:read", "incidents:write", "downtimes:write",
		"silences:write", "events:read", "metrics:read", "oncall:read", "oncall:write",
		"dashboards:read", "dashboards:write", "reports:read", "reports:render",
	}},
	{Name: "viewer", System: true, Permissions: []Permission{
		"objects:read", "alerts:read", "incidents:read", "events:read",
		"metrics:read", "oncall:read", "dashboards:read", "reports:read",
	}},
	{Name: "ai-agent", System: true, Permissions: []Permission{
		"objects:read", "alerts:read", "alerts:ack", "incidents:read", "incidents:write",
		"events:read", "metrics:read", "oncall:read", "checks:run",
		"downtimes:write", "silences:write", "config:propose", "reports:render",
	}},
}

// ActorType for audit attribution (SPEC §6.5).
type ActorType string

const (
	ActorUser   ActorType = "user"
	ActorToken  ActorType = "token"
	ActorAI     ActorType = "ai_agent"
	ActorSystem ActorType = "system"
)

// APIToken is a machine credential ("np_…", argon2id-hashed at rest,
// SPEC §11.2).
type APIToken struct {
	ID        string       `json:"id"`
	TenantID  string       `json:"tenantId"`
	Name      string       `json:"name"`
	Prefix    string       `json:"prefix"` // first 8 chars after np_, for lookup
	Hash      string       `json:"-"`
	Scopes    []Permission `json:"scopes"`
	RoleNames []string     `json:"roles,omitempty"`   // alternative to raw scopes
	IPBind    []string     `json:"ipBind,omitempty"`  // CIDRs
	AIAgent   bool         `json:"aiAgent,omitempty"` // audit as ai_agent actor
	ExpiresAt *time.Time   `json:"expiresAt,omitempty"`
	LastUsed  *time.Time   `json:"lastUsedAt,omitempty"`
	CreatedBy string       `json:"createdBy"`
	Version   int64        `json:"version"`
	CreatedAt time.Time    `json:"createdAt"`
}

// AuditEntry is one link of the hash chain (SPEC §13.5).
type AuditEntry struct {
	Seq        int64     `json:"seq"`
	TS         time.Time `json:"ts"`
	TenantID   string    `json:"tenantId,omitempty"`
	ActorType  ActorType `json:"actorType"`
	ActorID    string    `json:"actorId"`
	Action     string    `json:"action"` // "host.create", "alert.ack", …
	Resource   string    `json:"resource,omitempty"`
	SourceIP   string    `json:"sourceIp,omitempty"`
	RequestID  string    `json:"requestId,omitempty"`
	BeforeJSON string    `json:"before,omitempty"`
	AfterJSON  string    `json:"after,omitempty"`
	PrevHash   string    `json:"prevHash"`
	Hash       string    `json:"hash"`
}
