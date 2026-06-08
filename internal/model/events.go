package model

import (
	"encoding/json"
	"time"
)

// Event is an immutable fact in the unified pipeline (SPEC §6.4):
// check transitions, ingress payloads, notifications, config changes,
// AI actions. Events are append-only and time-partitioned (ADR-13).
type Event struct {
	ID       string          `json:"id"`
	TenantID string          `json:"tenantId"`
	TS       time.Time       `json:"ts"`
	Type     EventType       `json:"type"`
	ObjectID string          `json:"objectId,omitempty"`
	SourceID string          `json:"sourceId,omitempty"`
	Severity Severity        `json:"severity,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

// NormEvent is the canonical normal form every ingress adapter emits
// (SPEC §7.5). The raw original payload is archived unmodified.
type NormEvent struct {
	Source     string          `json:"source"`
	ReceivedAt time.Time       `json:"receivedAt"`
	DedupKey   string          `json:"dedupKey,omitempty"`
	Severity   Severity        `json:"severity"`
	Summary    string          `json:"summary"`
	Labels     Labels          `json:"labels,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Resolve    bool            `json:"resolve,omitempty"` // clears the dedupKey alert
}

// StateChangePayload is the payload of state_change events.
type StateChangePayload struct {
	ObjectName string    `json:"object"`
	HostName   string    `json:"host,omitempty"`
	Kind       Kind      `json:"kind"`
	From       State     `json:"fromState"`
	To         State     `json:"toState"`
	FromLabel  string    `json:"from"`
	ToLabel    string    `json:"to"`
	StateType  StateType `json:"stateType"`
	Attempt    int       `json:"attempt"`
	Output     string    `json:"output"`
	Labels     Labels    `json:"labels,omitempty"`
	Metric     string    `json:"metric,omitempty"` // dominant perfdata label, for rules
}

// Alert is a stateful evaluated incident precursor (SPEC §6.4/§6.5).
type Alert struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenantId"`
	RuleID     string          `json:"ruleId,omitempty"`
	ObjectID   string          `json:"objectId,omitempty"`
	IncidentID string          `json:"incidentId,omitempty"`
	Status     AlertStatus     `json:"status"`
	Severity   Severity        `json:"severity"`
	Title      string          `json:"title"`
	DedupKey   string          `json:"dedupKey,omitempty"`
	OpenedAt   time.Time       `json:"openedAt"`
	AckedAt    *time.Time      `json:"ackedAt,omitempty"`
	AckedBy    string          `json:"ackedBy,omitempty"`
	ResolvedAt *time.Time      `json:"resolvedAt,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	Labels     Labels          `json:"labels,omitempty"`
	EventIDs   []string        `json:"eventIds,omitempty"` // triggering events
	// Ticket links the external ticket created for this alert (F-04.05);
	// resolution auto-closes it when Ticket.AutoClose is set.
	Ticket *TicketRef `json:"ticket,omitempty"`
}

// IncidentStatus tracks the bundled-incident lifecycle.
type IncidentStatus string

const (
	IncidentOpen     IncidentStatus = "open"
	IncidentResolved IncidentStatus = "resolved"
)

// Incident bundles alerts, carries timeline, impact, AI summary and an
// external ticket link (SPEC §6.4).
type Incident struct {
	ID         string         `json:"id"`
	TenantID   string         `json:"tenantId"`
	Status     IncidentStatus `json:"status"`
	Severity   Severity       `json:"severity"`
	Title      string         `json:"title"`
	Summary    string         `json:"summary,omitempty"` // AI or human
	Impact     string         `json:"impact,omitempty"`
	TicketURL  string         `json:"ticketUrl,omitempty"` // ServiceNow etc.
	CreatedBy  string         `json:"createdBy"`           // user|ai_agent|correlation
	OpenedAt   time.Time      `json:"openedAt"`
	ResolvedAt *time.Time     `json:"resolvedAt,omitempty"`
	Version    int64          `json:"version"`
}

// EventSource is an ingress adapter instance (SPEC §7.5).
type EventSource struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
	Type     string `json:"type"` // webhook|alertmanager|email|snmp-trap|heartbeat|agent|…
	Enabled  bool   `json:"enabled"`
	// Auth: token (hashed at rest), hmac secret, or basic credentials.
	AuthMode  string `json:"authMode"` // token|hmac|basic|none
	SecretRef string `json:"secretRef,omitempty"`
	// Mapping: CEL expressions producing the NormEvent fields from the
	// raw payload (SPEC §9.2). Empty mapping = identity for JSON in
	// normal form already.
	Mapping map[string]string `json:"mapping,omitempty"`
	// Config: transport-specific settings (mirrors NotificationChannel.Config).
	// snmp-trap: listen ("udp://:9162"), community, severity, v3 user/auth/priv.
	// imap: host, port, tls, username, passwordSecretRef, folder, pollInterval.
	Config    map[string]string `json:"config,omitempty"`
	RateLimit float64           `json:"rateLimit,omitempty"` // events/s, 0 = default
	Burst     int               `json:"burst,omitempty"`
	Labels    Labels            `json:"labels,omitempty"` // merged into events
	Version   int64             `json:"version"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// Heartbeat is a dead-man source: absence of a beat raises an event
// (F-02.02 "no event from X in N minutes", SPEC §7.5).
type Heartbeat struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenantId"`
	Name        string     `json:"name"`
	ExpectEvery Duration   `json:"expectEvery"`
	Grace       Duration   `json:"grace,omitempty"`
	Severity    Severity   `json:"severity"`
	Labels      Labels     `json:"labels,omitempty"`
	LastBeat    *time.Time `json:"lastBeat,omitempty"`
	Missing     bool       `json:"missing"`
	Version     int64      `json:"version"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// AlertRule turns events into alerts (SPEC §9.2). Exactly one of Match
// (CEL over events) or Heartbeat must be set.
type AlertRule struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled,omitempty"`

	// Match is a CEL expression over `event` (sandboxed, cost-limited).
	Match string `json:"match,omitempty" yaml:"match,omitempty"`
	// Heartbeat alternative: alert when source is silent too long.
	Heartbeat *RuleHeartbeat `json:"heartbeat,omitempty" yaml:"heartbeat,omitempty"`

	PendingFor     Duration `json:"pendingFor,omitempty"     yaml:"pendingFor,omitempty"`
	DedupKey       string   `json:"dedupKey,omitempty"       yaml:"dedupKey,omitempty"` // Go template
	Severity       Severity `json:"severity"                 yaml:"severity"`
	Title          string   `json:"title,omitempty"          yaml:"title,omitempty"` // Go template, default = summary
	AutoCloseAfter Duration `json:"autoCloseAfter,omitempty" yaml:"autoCloseAfter,omitempty"`
	ResolveOnOK    *bool    `json:"resolveOnOk,omitempty"    yaml:"resolveOnOk,omitempty"` // default true

	EscalationPolicy string `json:"escalationPolicy,omitempty" yaml:"escalationPolicy,omitempty"`
	GroupID          string `json:"groupId,omitempty"          yaml:"groupId,omitempty"`
	SetLabels        Labels `json:"setLabels,omitempty"        yaml:"setLabels,omitempty"`
	// Incident automatically opens an incident for each alert this rule
	// opens, and resolves it once all of its alerts resolved (F-04.05).
	Incident bool `json:"incident,omitempty" yaml:"incident,omitempty"`

	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RuleHeartbeat configures silence detection for a source.
type RuleHeartbeat struct {
	Source      string   `json:"source"      yaml:"source"`
	ExpectEvery Duration `json:"expectEvery" yaml:"expectEvery"`
}

// AggregateFunc for alert-group aggregation (F-02.03).
type AggregateFunc string

const (
	AggCount  AggregateFunc = "count"
	AggMin    AggregateFunc = "min"
	AggMax    AggregateFunc = "max"
	AggAvg    AggregateFunc = "avg"
	AggSum    AggregateFunc = "sum"
	AggMedian AggregateFunc = "median"
)

// AlertGroup aggregates alerts sharing a grouping key within a window
// (SPEC §9.2/§9.3).
type AlertGroup struct {
	ID        string        `json:"id"`
	TenantID  string        `json:"tenantId"`
	Name      string        `json:"name"`
	GroupBy   []string      `json:"groupBy"` // label keys
	Window    Duration      `json:"window"`
	Aggregate AggregateFunc `json:"aggregate,omitempty"`
	ValuePath string        `json:"valuePath,omitempty"` // JSON path into payload for min/max/…
	MinCount  int           `json:"minCount,omitempty"`  // threshold to open the grouped alert
	Version   int64         `json:"version"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
}
