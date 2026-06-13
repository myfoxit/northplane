package model

import "time"

// DowntimeType per Nagios semantics (SPEC §6.3).
type DowntimeType string

const (
	DowntimeFixed    DowntimeType = "fixed"
	DowntimeFlexible DowntimeType = "flexible"
)

// Downtime suppresses notifications for matching objects during a window.
// Selector-based downtimes cover dynamic sets (SPEC §11.4 example).
type Downtime struct {
	ID       string       `json:"id"`
	TenantID string       `json:"tenantId"`
	ObjectID string       `json:"objectId,omitempty"` // either fixed object…
	Selector string       `json:"selector,omitempty"` // …or label selector
	Type     DowntimeType `json:"type"`
	Start    time.Time    `json:"start"`
	End      time.Time    `json:"end"`
	// Flexible: actual window starts at first problem state within
	// [Start,End] and lasts Duration.
	FlexDuration Duration `json:"duration,omitempty"`
	TriggeredBy  string   `json:"triggeredBy,omitempty"` // parent downtime id (chains, §6.3)
	// Recurrence: RRULE subset ("FREQ=WEEKLY;BYDAY=SA", §6.3).
	RRule     string     `json:"rrule,omitempty"`
	Comment   string     `json:"comment"`
	CreatedBy string     `json:"createdBy"`
	StartedAt *time.Time `json:"startedAt,omitempty"` // flexible: trigger time
	Version   int64      `json:"version"`
	CreatedAt time.Time  `json:"createdAt"`
}

// ActiveAt reports whether the downtime suppresses at t.
func (d *Downtime) ActiveAt(t time.Time) bool {
	switch d.Type {
	case DowntimeFlexible:
		if d.StartedAt == nil {
			return false
		}
		return !t.Before(*d.StartedAt) && t.Before(d.StartedAt.Add(d.FlexDuration.D()))
	default:
		// A recurring downtime repeats its [Start,End] window on the RRULE
		// cadence; without this it would suppress only its first window and
		// then page through every later occurrence (SPEC §6.3).
		if d.RRule != "" {
			return d.recurringActiveAt(t)
		}
		return !t.Before(d.Start) && t.Before(d.End)
	}
}

// Silence is an ad-hoc suppression with mandatory TTL ("kein für immer
// vergessen", SPEC §9.2): label selector + optional regex on event text.
type Silence struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Selector  string    `json:"selector,omitempty"`
	TextRegex string    `json:"textRegex,omitempty"`
	Comment   string    `json:"comment"`
	CreatedBy string    `json:"createdBy"`
	StartsAt  time.Time `json:"startsAt"`
	ExpiresAt time.Time `json:"expiresAt"` // mandatory
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
}

// ActiveAt reports whether the silence applies at t.
func (s *Silence) ActiveAt(t time.Time) bool {
	return !t.Before(s.StartsAt) && t.Before(s.ExpiresAt)
}

// Acknowledgement is sticky & persistent with optional expiry (SPEC §6.3).
type Acknowledgement struct {
	ObjectID  string     `json:"objectId,omitempty"`
	AlertID   string     `json:"alertId,omitempty"`
	By        string     `json:"by"`
	Comment   string     `json:"comment"`
	Sticky    bool       `json:"sticky"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	At        time.Time  `json:"at"`
}
