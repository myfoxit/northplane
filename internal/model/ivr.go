package model

import "time"

// IVRMenu is a configurable DTMF phone menu for alarm lines (SPEC §9.6
// evolution): inbound calls to a voice-inbound event source are answered
// with the menu's greeting and dispatch on the pressed digit. The same
// menus drive richer outbound-call flows (ack/resolve digits).
type IVRMenu struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
	// Language is the TTS language tag (Twilio <Say>), e.g. "de-DE".
	Language string `json:"language,omitempty"`
	// Voice optionally selects a provider TTS voice (e.g. "Polly.Vicki").
	Voice string `json:"voice,omitempty"`
	// Greeting is spoken before the options are offered.
	Greeting string `json:"greeting,omitempty"`
	// PIN gates the menu: callers must key it in before anything else.
	// Empty = no PIN. Callers matching a contact's phone number skip the
	// PIN when TrustCallerID is set.
	PIN           string      `json:"pin,omitempty"`
	TrustCallerID bool        `json:"trustCallerId,omitempty"`
	Options       []IVROption `json:"options"`
	Version       int64       `json:"version"`
	CreatedAt     time.Time   `json:"createdAt"`
	UpdatedAt     time.Time   `json:"updatedAt"`
}

// IVR option actions.
const (
	IVRTriggerAlarm = "trigger-alarm" // raise an alert, optionally record a voice message
	IVRListAlerts   = "list-alerts"   // read the newest open alerts aloud
	IVRAckAlert     = "ack-alert"     // acknowledge: newest, or chosen from the read list
	IVRResolveAlert = "resolve-alert" // resolve: newest, or chosen from the read list
	IVRSay          = "say"           // speak a fixed text
)

// IVROption binds one DTMF digit to an action.
type IVROption struct {
	Digit  string `json:"digit"`  // "0"-"9", "*", "#"
	Action string `json:"action"` // one of the IVR* constants
	// Label is a short description spoken in the options prompt
	// ("Für <Label> drücken Sie die <Digit>"). Defaults per action.
	Label string `json:"label,omitempty"`

	// trigger-alarm parameters:
	Severity Severity `json:"severity,omitempty"` // default critical
	// Title for the raised alert; "{caller}" and "{called}" expand to the
	// calling/called number. Default: "Phone alarm from {caller}".
	Title            string `json:"title,omitempty"`
	Labels           Labels `json:"labels,omitempty"` // e.g. np.sound=np_klaxon
	EscalationPolicy string `json:"escalationPolicy,omitempty"`
	// Record captures a voice message after the trigger; the recording URL
	// (and transcript, when the provider transcribes) is attached to the
	// alert as labels recordingUrl / transcript.
	Record bool `json:"record,omitempty"`

	// say parameters:
	Text string `json:"text,omitempty"`
}

// FindOption returns the option bound to a digit, or nil.
func (m *IVRMenu) FindOption(digit string) *IVROption {
	for i := range m.Options {
		if m.Options[i].Digit == digit {
			return &m.Options[i]
		}
	}
	return nil
}

// DefaultIVRMenu is used by voice-inbound sources without a configured
// menu: 1 = raise an alarm with a voice message, 2 = hear open alerts,
// 3 = acknowledge the newest alert.
func DefaultIVRMenu() *IVRMenu {
	return &IVRMenu{
		Name: "builtin",
		Options: []IVROption{
			{Digit: "1", Action: IVRTriggerAlarm, Record: true, Severity: SevCritical},
			{Digit: "2", Action: IVRListAlerts},
			{Digit: "3", Action: IVRAckAlert},
		},
	}
}
