package model

import "time"

// ChannelType enumerates notification transports (SPEC §9.6).
type ChannelType string

const (
	ChannelEmail   ChannelType = "email"
	ChannelWebhook ChannelType = "webhook"
	ChannelTeams   ChannelType = "teams"
	ChannelSlack   ChannelType = "slack"
	ChannelNtfy    ChannelType = "ntfy"
	ChannelPush    ChannelType = "push" // Web Push / PWA (ADR-12)
	ChannelSMS     ChannelType = "sms"
	ChannelVoice   ChannelType = "voice"
)

// NotificationChannel is a configured transport instance.
type NotificationChannel struct {
	ID       string      `json:"id"`
	TenantID string      `json:"tenantId"`
	Name     string      `json:"name"`
	Type     ChannelType `json:"type"`
	Enabled  bool        `json:"enabled"`
	// Config is transport-specific (SMTP relay, webhook URL, Slack hook,
	// SMS provider …). Secret values are stored as $SECRET:name$
	// references and resolved at send time (SPEC §8.2/§13.2).
	Config map[string]string `json:"config"`
	// Template overrides the default message template (Go template over
	// the notification context, SPEC §9.6 / F-04.09).
	Template  string    `json:"template,omitempty"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Contact is a notifiable person (the system's main PII class, §13.4).
type Contact struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Phone    string `json:"phone,omitempty"` // E.164
	UserID   string `json:"userId,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
	// Preferences: ordered channel preferences with time profiles
	// (F-04.08): e.g. worktime → [teams, email], night → [push, sms].
	Preferences []ChannelPreference `json:"preferences,omitempty"`
	Version     int64               `json:"version"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
}

// ChannelPreference binds channel types to a time profile.
type ChannelPreference struct {
	Profile  string        `json:"profile"`            // "default" | "worktime" | "night" | custom
	Period   string        `json:"period,omitempty"`   // TimePeriod name, empty = always
	Channels []ChannelType `json:"channels"`           // ordered
	Severity Severity      `json:"severity,omitempty"` // minimum severity, empty = all
}

// ContactGroup groups contacts; may mirror an IdP group (SPEC §6.1).
type ContactGroup struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Name      string    `json:"name"`
	Members   []string  `json:"members"`            // contact IDs
	IdPGroup  string    `json:"idpGroup,omitempty"` // Entra/Keycloak group id
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EscalationPolicy is a multi-step notification chain (SPEC §9.4).
type EscalationPolicy struct {
	ID        string           `json:"id"`
	TenantID  string           `json:"tenantId"`
	Name      string           `json:"name"`
	Steps     []EscalationStep `json:"steps"`
	Version   int64            `json:"version"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// EscalationTarget selects who/what a step notifies.
type EscalationTarget struct {
	Schedule     string `json:"schedule,omitempty"     yaml:"schedule,omitempty"`     // whoever is on call
	EscalateTo   string `json:"escalateTo,omitempty"   yaml:"escalateTo,omitempty"`   // "" | "backup" (second in rotation)
	Contact      string `json:"contact,omitempty"      yaml:"contact,omitempty"`
	ContactGroup string `json:"contactGroup,omitempty" yaml:"contactGroup,omitempty"`
}

// EscalationAction is a non-notification side effect (ServiceNow…, F-04.05).
type EscalationAction struct {
	ServiceNow *ServiceNowAction `json:"servicenow,omitempty" yaml:"servicenow,omitempty"`
	Webhook    string            `json:"webhook,omitempty"    yaml:"webhook,omitempty"` // channel name
}

// ServiceNowAction creates/updates a ServiceNow incident.
type ServiceNowAction struct {
	AssignmentGroup string `json:"assignmentGroup" yaml:"assignmentGroup"`
	AutoClose       bool   `json:"autoClose"       yaml:"autoClose"`
}

// EscalationStep fires After the alert opened, unless acked (SPEC §9.4).
type EscalationStep struct {
	After       Duration          `json:"after"                 yaml:"after"`
	UnlessAcked bool              `json:"unlessAcked,omitempty" yaml:"unlessAcked,omitempty"`
	Notify      *EscalationTarget `json:"notify,omitempty"      yaml:"notify,omitempty"`
	Channels    []ChannelType     `json:"channels,omitempty"    yaml:"channels,omitempty"` // override personal prefs
	RepeatEvery Duration          `json:"repeatEvery,omitempty" yaml:"repeatEvery,omitempty"`
	MaxRepeats  int               `json:"maxRepeats,omitempty"  yaml:"maxRepeats,omitempty"`
	Action      *EscalationAction `json:"action,omitempty"      yaml:"action,omitempty"`
}

// NotificationStatus tracks a single delivery attempt (F-05.09:
// immutable history — persisted as notification events).
type NotificationStatus string

const (
	NotifyPending   NotificationStatus = "pending"
	NotifySent      NotificationStatus = "sent"
	NotifyFailed    NotificationStatus = "failed"
	NotifyDead      NotificationStatus = "dead" // moved to DLQ after retries
	NotifySuppress  NotificationStatus = "suppressed"
)

// NotificationRecord is the payload of notification events and the DLQ row.
type NotificationRecord struct {
	AlertID    string             `json:"alertId"`
	StepIndex  int                `json:"stepIndex"`
	Repeat     int                `json:"repeat,omitempty"`
	ContactID  string             `json:"contactId,omitempty"`
	Contact    string             `json:"contact,omitempty"`
	Channel    ChannelType        `json:"channel"`
	ChannelID  string             `json:"channelId,omitempty"`
	Target     string             `json:"target,omitempty"` // masked address
	Status     NotificationStatus `json:"status"`
	Attempt    int                `json:"attempt"`
	Error      string             `json:"error,omitempty"`
	ProviderID string             `json:"providerId,omitempty"` // provider message id
	LatencyMS  int64              `json:"latencyMs,omitempty"`
}
