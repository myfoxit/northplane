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
	ChannelPush    ChannelType = "push" // Web Push / PWA (ADR-12) + FCM/APNs mobile
	ChannelSMS     ChannelType = "sms"
	ChannelVoice   ChannelType = "voice"
	ChannelMQTT    ChannelType = "mqtt" // publish alarms to an MQTT broker
	// Ticket-system transports (F-04.05): create an external ticket and
	// optionally auto-close it when the alert resolves.
	ChannelServiceNow ChannelType = "servicenow"
	ChannelZendesk    ChannelType = "zendesk"
	ChannelJira       ChannelType = "jira"
	ChannelTicket     ChannelType = "ticket" // generic HTTP ticket gateway
)

// IsTicket reports whether a channel type creates external tickets.
func (t ChannelType) IsTicket() bool {
	switch t {
	case ChannelServiceNow, ChannelZendesk, ChannelJira, ChannelTicket:
		return true
	}
	return false
}

// TicketRef records the external ticket created for an alert (F-04.05).
// Stored on the alert so resolution can auto-close the ticket.
type TicketRef struct {
	Channel   string `json:"channel"`       // NotificationChannel name
	Type      string `json:"type"`          // servicenow|zendesk|jira|ticket
	Ref       string `json:"ref"`           // provider id (sys_id, ticket id, issue key)
	URL       string `json:"url,omitempty"` // human link
	AutoClose bool   `json:"autoClose,omitempty"`
}

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

// PreferredChannels picks a contact's channels for the active time
// profile (F-04.08), filtered by minimum severity. Named periods are
// resolved through lookup (stored TimePeriods win); the built-in
// profiles "worktime"/"arbeitszeit" (Mo–Fr 08:00–18:00 local) and
// "night"/"nacht" (inverse) apply when no stored period exists.
// A preference without a period is the fallback when no period matches.
func PreferredChannels(c *Contact, sev Severity, now time.Time,
	lookup func(name string) *TimePeriod) []ChannelType {
	loc := time.UTC
	if c.TimeZone != "" {
		if l, err := time.LoadLocation(c.TimeZone); err == nil {
			loc = l
		}
	}
	local := now.In(loc)
	var fallback []ChannelType
	for _, pref := range c.Preferences {
		if pref.Severity != "" && sev.Rank() < pref.Severity.Rank() {
			continue
		}
		if pref.Period == "" {
			if fallback == nil {
				fallback = pref.Channels
			}
			continue
		}
		if lookup != nil {
			if tp := lookup(pref.Period); tp != nil {
				if tp.Contains(local) {
					return pref.Channels
				}
				continue
			}
		}
		if matchBuiltinProfile(pref.Period, local) {
			return pref.Channels
		}
	}
	return fallback
}

// matchBuiltinProfile implements the two well-known time profiles.
func matchBuiltinProfile(period string, t time.Time) bool {
	hour := t.Hour()
	isWeekday := t.Weekday() >= time.Monday && t.Weekday() <= time.Friday
	switch period {
	case "worktime", "arbeitszeit":
		return isWeekday && hour >= 8 && hour < 18
	case "night", "nacht":
		return !isWeekday || hour < 8 || hour >= 18
	default:
		return false
	}
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
	Schedule     string `json:"schedule,omitempty"     yaml:"schedule,omitempty"`   // whoever is on call
	EscalateTo   string `json:"escalateTo,omitempty"   yaml:"escalateTo,omitempty"` // "" | "backup" (second in rotation)
	Contact      string `json:"contact,omitempty"      yaml:"contact,omitempty"`
	ContactGroup string `json:"contactGroup,omitempty" yaml:"contactGroup,omitempty"`
}

// EscalationAction is a non-notification side effect (ServiceNow…, F-04.05).
type EscalationAction struct {
	ServiceNow *ServiceNowAction `json:"servicenow,omitempty" yaml:"servicenow,omitempty"`
	// Ticket creates an external ticket through a named ticket channel
	// (servicenow | zendesk | jira | generic "ticket").
	Ticket  *TicketAction `json:"ticket,omitempty"  yaml:"ticket,omitempty"`
	Webhook string        `json:"webhook,omitempty" yaml:"webhook,omitempty"` // channel name
}

// ServiceNowAction creates/updates a ServiceNow incident.
type ServiceNowAction struct {
	AssignmentGroup string `json:"assignmentGroup" yaml:"assignmentGroup"`
	AutoClose       bool   `json:"autoClose"       yaml:"autoClose"`
}

// TicketAction creates a ticket via a configured ticket channel.
type TicketAction struct {
	Channel   string `json:"channel"             yaml:"channel"` // NotificationChannel name
	AutoClose bool   `json:"autoClose,omitempty" yaml:"autoClose,omitempty"`
	// Params are merged into the provider payload (assignment_group,
	// priority, tags …) — provider-specific, see channel docs.
	Params map[string]string `json:"params,omitempty" yaml:"params,omitempty"`
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
	NotifyPending  NotificationStatus = "pending"
	NotifySent     NotificationStatus = "sent"
	NotifyFailed   NotificationStatus = "failed"
	NotifyDead     NotificationStatus = "dead" // moved to DLQ after retries
	NotifySuppress NotificationStatus = "suppressed"
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
