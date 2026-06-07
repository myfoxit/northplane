package model

import "fmt"

// State is the Nagios-semantic check state (SPEC §6.3).
// Services: OK(0) WARNING(1) CRITICAL(2) UNKNOWN(3).
// Hosts map UP/DOWN/UNREACHABLE onto 0/1/2 with the same storage type.
type State int

const (
	StateOK       State = 0
	StateWarning  State = 1
	StateCritical State = 2
	StateUnknown  State = 3
)

// Host-state aliases (hosts reuse the numeric space).
const (
	HostUp          State = 0
	HostDown        State = 1
	HostUnreachable State = 2
)

// ServiceLabel renders the service-state name.
func (s State) ServiceLabel() string {
	switch s {
	case StateOK:
		return "OK"
	case StateWarning:
		return "WARNING"
	case StateCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// HostLabel renders the host-state name.
func (s State) HostLabel() string {
	switch s {
	case HostUp:
		return "UP"
	case HostDown:
		return "DOWN"
	case HostUnreachable:
		return "UNREACHABLE"
	default:
		return "UNKNOWN"
	}
}

// Label renders the state name for the given object kind.
func (s State) Label(kind Kind) string {
	if kind == KindHost {
		return s.HostLabel()
	}
	return s.ServiceLabel()
}

// ParseState accepts numeric and symbolic state notations
// (passive-result ingestion is deliberately lenient, SPEC §8.5).
func ParseState(v string) (State, error) {
	switch v {
	case "0", "OK", "ok", "UP", "up":
		return StateOK, nil
	case "1", "WARNING", "warning", "WARN", "warn", "DOWN", "down":
		return StateWarning, nil
	case "2", "CRITICAL", "critical", "CRIT", "crit", "UNREACHABLE", "unreachable":
		return StateCritical, nil
	case "3", "UNKNOWN", "unknown":
		return StateUnknown, nil
	}
	return StateUnknown, fmt.Errorf("invalid state %q", v)
}

// StateType distinguishes soft from hard states (SPEC §6.3).
type StateType string

const (
	StateSoft StateType = "soft"
	StateHard StateType = "hard"
)

// Severity classifies events and alerts.
type Severity string

const (
	SevCritical Severity = "critical"
	SevWarning  Severity = "warning"
	SevInfo     Severity = "info"
	SevOK       Severity = "ok" // recovery / clear
)

// SeverityFromState maps check states onto alert severities.
func SeverityFromState(s State, kind Kind) Severity {
	if kind == KindHost {
		switch s {
		case HostUp:
			return SevOK
		default:
			return SevCritical // DOWN and UNREACHABLE are host-critical
		}
	}
	switch s {
	case StateOK:
		return SevOK
	case StateWarning:
		return SevWarning
	case StateCritical:
		return SevCritical
	default:
		return SevWarning // UNKNOWN alerts as warning by default
	}
}

// Rank orders severities for sorting (higher = more urgent).
func (s Severity) Rank() int {
	switch s {
	case SevCritical:
		return 3
	case SevWarning:
		return 2
	case SevInfo:
		return 1
	default:
		return 0
	}
}

func (s Severity) Valid() bool {
	switch s {
	case SevCritical, SevWarning, SevInfo, SevOK:
		return true
	}
	return false
}

// NotifyToken maps a hard state onto its ObjectSpec.NotifyOn filter
// token ("recovery", "warning", "critical", "unknown", "down",
// "unreachable").
func NotifyToken(s State, kind Kind) string {
	if kind == KindHost {
		switch s {
		case HostUp:
			return "recovery"
		case HostDown:
			return "down"
		case HostUnreachable:
			return "unreachable"
		default:
			return "unknown"
		}
	}
	switch s {
	case StateOK:
		return "recovery"
	case StateWarning:
		return "warning"
	case StateCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// ValidNotifyOn enumerates the accepted NotifyOn tokens (validation).
var ValidNotifyOn = map[string]bool{
	"warning": true, "critical": true, "unknown": true,
	"down": true, "unreachable": true, "recovery": true,
}

// WantsNotify reports whether the spec's NotifyOn filter includes the
// token. An empty filter notifies on everything (Nagios default).
func (s *ObjectSpec) WantsNotify(token string) bool {
	if len(s.NotifyOn) == 0 {
		return true
	}
	for _, t := range s.NotifyOn {
		if t == token {
			return true
		}
	}
	return false
}

// AlertStatus is the alert lifecycle (SPEC §6.5).
type AlertStatus string

const (
	AlertOpen     AlertStatus = "open"
	AlertAcked    AlertStatus = "acked"
	AlertResolved AlertStatus = "resolved"
	AlertExpired  AlertStatus = "expired"
)

// EventType enumerates the event stream taxonomy (SPEC §6.5).
type EventType string

const (
	EventStateChange    EventType = "state_change"
	EventNotification   EventType = "notification"
	EventIngress        EventType = "ingress"
	EventConfig         EventType = "config"
	EventAIAction       EventType = "ai_action"
	EventAck            EventType = "ack"
	EventComment        EventType = "comment"
	EventDowntime       EventType = "downtime"
	EventSilence        EventType = "silence"
	EventEscalation     EventType = "escalation"
	EventAnomaly        EventType = "anomaly"
	EventForecast       EventType = "forecast"
	EventHeartbeatMiss  EventType = "heartbeat_missed"
	EventFlappingStart  EventType = "flapping_start"
	EventFlappingEnd    EventType = "flapping_end"
	EventSystem         EventType = "system"
	EventAlertOpened    EventType = "alert_opened"
	EventAlertResolved  EventType = "alert_resolved"
	EventIncidentUpdate EventType = "incident_update"
)
