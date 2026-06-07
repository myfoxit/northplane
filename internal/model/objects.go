package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Kind discriminates the unified objects table (SPEC §6.5).
type Kind string

const (
	KindHost    Kind = "host"
	KindService Kind = "service"
)

// Labels is the primary grouping mechanism (SPEC §6.2).
type Labels map[string]string

// Clone returns a deep copy.
func (l Labels) Clone() Labels {
	if l == nil {
		return nil
	}
	c := make(Labels, len(l))
	for k, v := range l {
		c[k] = v
	}
	return c
}

// Merge returns l overlaid with over (over wins). Never mutates receivers.
func (l Labels) Merge(over Labels) Labels {
	c := make(Labels, len(l)+len(over))
	for k, v := range l {
		c[k] = v
	}
	for k, v := range over {
		c[k] = v
	}
	return c
}

// String renders "k=v,k2=v2" sorted by key (stable for hashing/display).
func (l Labels) String() string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(l[k])
	}
	return b.String()
}

// Vars are custom variables exposed as $_HOSTFOO$ / $_SERVICEFOO$ macros
// (SPEC §8.2). Values are stringified scalars.
type Vars map[string]string

// Object is a Host or Service (unified storage, SPEC §6.5).
type Object struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Kind      Kind      `json:"kind"`
	Name      string    `json:"name"`
	HostID    string    `json:"hostId,omitempty"` // services only
	Folder    string    `json:"folder"`
	Labels    Labels    `json:"labels"`
	Spec      ObjectSpec `json:"spec"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ThresholdMode selects static vs. AI-baselined thresholds (SPEC §6.2/§10.6).
type ThresholdMode string

const (
	ThresholdStatic   ThresholdMode = "static"
	ThresholdAdaptive ThresholdMode = "adaptive"
)

// ObjectSpec is the declarative check configuration. All fields are
// optional at rest — effective values are resolved through the template
// chain (SPEC §6.2) and finally clamped by Defaults().
type ObjectSpec struct {
	Address   string   `json:"address,omitempty"   yaml:"address,omitempty"`
	Templates []string `json:"templates,omitempty" yaml:"templates,omitempty"`
	Parents   []string `json:"parents,omitempty"   yaml:"parents,omitempty"` // reachability graph (hosts)

	CheckCommand string   `json:"checkCommand,omitempty" yaml:"checkCommand,omitempty"` // "builtin:icmp" | "exec:check_postgres" | "agent:exec:…" | "passive"
	Args         []string `json:"args,omitempty"         yaml:"args,omitempty"`

	Interval         Duration `json:"interval,omitempty"         yaml:"interval,omitempty"`
	RetryInterval    Duration `json:"retryInterval,omitempty"    yaml:"retryInterval,omitempty"`
	MaxCheckAttempts int      `json:"maxCheckAttempts,omitempty" yaml:"maxCheckAttempts,omitempty"`
	Timeout          Duration `json:"timeout,omitempty"          yaml:"timeout,omitempty"`
	CheckPeriod      string   `json:"checkPeriod,omitempty"      yaml:"checkPeriod,omitempty"`

	NotificationPeriod  string   `json:"notificationPeriod,omitempty" yaml:"notificationPeriod,omitempty"`
	EnableNotifications *bool    `json:"enableNotifications,omitempty" yaml:"enableNotifications,omitempty"`
	// Contacts / ContactGroups are notified directly on hard state
	// changes (Nagios contact_groups semantics — SPEC §9, F-04) without
	// requiring an alert rule. Resolution honours NotificationPeriod,
	// EnableNotifications and NotifyOn; delivery uses each contact's
	// channel preferences. Values are contact / contact-group names (or ids).
	Contacts      []string `json:"contacts,omitempty"      yaml:"contacts,omitempty"`
	ContactGroups []string `json:"contactGroups,omitempty" yaml:"contactGroups,omitempty"`
	// NotifyOn filters which hard transitions notify: "warning",
	// "critical", "unknown" (services), "down", "unreachable" (hosts),
	// "recovery". Empty = all problem states + recovery (Nagios default).
	NotifyOn []string `json:"notifyOn,omitempty" yaml:"notifyOn,omitempty"`
	EnableChecks        *bool    `json:"enableChecks,omitempty"        yaml:"enableChecks,omitempty"`
	EnableFlapDetection *bool    `json:"enableFlapDetection,omitempty" yaml:"enableFlapDetection,omitempty"`
	FlapThresholdLow    float64  `json:"flapThresholdLow,omitempty"    yaml:"flapThresholdLow,omitempty"`  // % (default 25)
	FlapThresholdHigh   float64  `json:"flapThresholdHigh,omitempty"   yaml:"flapThresholdHigh,omitempty"` // % (default 50)

	StalenessAfter Duration      `json:"stalenessAfter,omitempty" yaml:"stalenessAfter,omitempty"` // passive freshness
	StalenessText  string        `json:"stalenessText,omitempty"  yaml:"stalenessText,omitempty"`
	ThresholdMode  ThresholdMode `json:"thresholdMode,omitempty"  yaml:"thresholdMode,omitempty"`

	Zone    string `json:"zone,omitempty"    yaml:"zone,omitempty"`    // satellite zone (SPEC §7.7)
	Runbook string `json:"runbook,omitempty" yaml:"runbook,omitempty"` // markdown (SPEC §10.5)

	Vars Vars `json:"vars,omitempty" yaml:"vars,omitempty"`
}

// SpecDefaults are applied after template resolution.
var SpecDefaults = ObjectSpec{
	Interval:         Duration(60 * time.Second),
	RetryInterval:    Duration(15 * time.Second),
	MaxCheckAttempts: 3,
	Timeout:          Duration(30 * time.Second),
	CheckPeriod:      "24x7",
	FlapThresholdLow: 25, FlapThresholdHigh: 50,
	ThresholdMode: ThresholdStatic,
}

// MergeSpec overlays child onto base: scalar fields are replaced when set,
// Vars are merged key-wise, Templates/Parents/Args replaced wholesale —
// matching Nagios `use` semantics (SPEC §6.2).
func MergeSpec(base, child ObjectSpec) ObjectSpec {
	out := base
	if child.Address != "" {
		out.Address = child.Address
	}
	if child.Templates != nil {
		out.Templates = child.Templates
	}
	if child.Parents != nil {
		out.Parents = child.Parents
	}
	if child.CheckCommand != "" {
		out.CheckCommand = child.CheckCommand
	}
	if child.Args != nil {
		out.Args = child.Args
	}
	if child.Interval != 0 {
		out.Interval = child.Interval
	}
	if child.RetryInterval != 0 {
		out.RetryInterval = child.RetryInterval
	}
	if child.MaxCheckAttempts != 0 {
		out.MaxCheckAttempts = child.MaxCheckAttempts
	}
	if child.Timeout != 0 {
		out.Timeout = child.Timeout
	}
	if child.CheckPeriod != "" {
		out.CheckPeriod = child.CheckPeriod
	}
	if child.NotificationPeriod != "" {
		out.NotificationPeriod = child.NotificationPeriod
	}
	if child.EnableNotifications != nil {
		out.EnableNotifications = child.EnableNotifications
	}
	if child.Contacts != nil {
		out.Contacts = child.Contacts
	}
	if child.ContactGroups != nil {
		out.ContactGroups = child.ContactGroups
	}
	if child.NotifyOn != nil {
		out.NotifyOn = child.NotifyOn
	}
	if child.EnableChecks != nil {
		out.EnableChecks = child.EnableChecks
	}
	if child.EnableFlapDetection != nil {
		out.EnableFlapDetection = child.EnableFlapDetection
	}
	if child.FlapThresholdLow != 0 {
		out.FlapThresholdLow = child.FlapThresholdLow
	}
	if child.FlapThresholdHigh != 0 {
		out.FlapThresholdHigh = child.FlapThresholdHigh
	}
	if child.StalenessAfter != 0 {
		out.StalenessAfter = child.StalenessAfter
	}
	if child.StalenessText != "" {
		out.StalenessText = child.StalenessText
	}
	if child.ThresholdMode != "" {
		out.ThresholdMode = child.ThresholdMode
	}
	if child.Zone != "" {
		out.Zone = child.Zone
	}
	if child.Runbook != "" {
		out.Runbook = child.Runbook
	}
	if child.Vars != nil {
		merged := make(Vars, len(out.Vars)+len(child.Vars))
		for k, v := range out.Vars {
			merged[k] = v
		}
		for k, v := range child.Vars {
			merged[k] = v
		}
		out.Vars = merged
	}
	return out
}

// TemplateKind scopes templates to an object class (SPEC §6.1).
type TemplateKind string

const (
	TemplateHost    TemplateKind = "host"
	TemplateService TemplateKind = "service"
	TemplateCommand TemplateKind = "command"
)

// Template carries inheritable spec fragments with multi-inheritance in
// declared order, cycle-free (SPEC §6.2).
type Template struct {
	ID        string       `json:"id"`
	TenantID  string       `json:"tenantId"`
	Kind      TemplateKind `json:"kind"`
	Name      string       `json:"name"`
	Labels    Labels       `json:"labels,omitempty"` // merged into objects
	Spec      ObjectSpec   `json:"spec"`
	Version   int64        `json:"version"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// CommandType discriminates check command execution classes (SPEC §6.1).
type CommandType string

const (
	CommandExec    CommandType = "exec"    // Nagios plugin on server/satellite
	CommandBuiltin CommandType = "builtin" // in-process Go check
	CommandAgent   CommandType = "agent"   // executed by np-agent
	CommandPassive CommandType = "passive" // results submitted externally
)

// CheckCommand is a reusable command definition with $ARGn$ substitution
// (SPEC §8.2).
type CheckCommand struct {
	ID        string      `json:"id"`
	TenantID  string      `json:"tenantId"`
	Name      string      `json:"name"`
	Type      CommandType `json:"type"`
	// Line is the command line for exec commands; argv[0] is resolved
	// against the plugins directory unless absolute. For builtin commands
	// it names the check ("icmp", "http", …).
	Line      []string  `json:"line"`
	Env       bool      `json:"env"`            // export NAGIOS_*/NORTHPLANE_* env macros (§8.2)
	Timeout   Duration  `json:"timeout,omitempty"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ParseCommandRef splits an object's checkCommand reference
// ("builtin:icmp", "exec:check_postgres", "agent:exec:check_disk",
// "passive") into its type and remainder.
func ParseCommandRef(ref string) (CommandType, string, error) {
	switch {
	case ref == "passive" || ref == "":
		return CommandPassive, "", nil
	case strings.HasPrefix(ref, "builtin:"):
		return CommandBuiltin, strings.TrimPrefix(ref, "builtin:"), nil
	case strings.HasPrefix(ref, "exec:"):
		return CommandExec, strings.TrimPrefix(ref, "exec:"), nil
	case strings.HasPrefix(ref, "agent:"):
		return CommandAgent, strings.TrimPrefix(ref, "agent:"), nil
	}
	// Bare name → named CheckCommand lookup (importer output).
	return "", ref, nil
}

// TimePeriod is a Nagios-style time period ("24x7", business hours)
// with weekday ranges and date exceptions.
type TimePeriod struct {
	ID        string              `json:"id"`
	TenantID  string              `json:"tenantId"`
	Name      string              `json:"name"`
	Alias     string              `json:"alias,omitempty"`
	Days      map[string][]string `json:"days,omitempty"`       // "monday" → ["09:00-17:00", …]
	Exceptions map[string][]string `json:"exceptions,omitempty"` // "2026-12-24" → ["00:00-00:00"] (closed) or ranges
	Exclude   []string            `json:"exclude,omitempty"`    // names of periods subtracted from this one
	Version   int64               `json:"version"`
	CreatedAt time.Time           `json:"createdAt"`
	UpdatedAt time.Time           `json:"updatedAt"`
}

var weekdayNames = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
	"wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday,
}

// Contains reports whether t falls inside the period. The zero-value
// period and the well-known name "24x7" always match. Exceptions
// (exact dates) override weekday rules; Exclude is applied by the caller
// (requires period lookup).
func (p *TimePeriod) Contains(t time.Time) bool {
	if p == nil || p.Name == "24x7" || (len(p.Days) == 0 && len(p.Exceptions) == 0) {
		return true
	}
	if ranges, ok := p.Exceptions[t.Format("2006-01-02")]; ok {
		return inRanges(ranges, t)
	}
	for name, wd := range weekdayNames {
		if wd == t.Weekday() {
			if ranges, ok := p.Days[name]; ok {
				return inRanges(ranges, t)
			}
		}
	}
	return false
}

func inRanges(ranges []string, t time.Time) bool {
	minutes := t.Hour()*60 + t.Minute()
	for _, r := range ranges {
		var h1, m1, h2, m2 int
		if _, err := fmt.Sscanf(r, "%d:%d-%d:%d", &h1, &m1, &h2, &m2); err != nil {
			continue
		}
		start, end := h1*60+m1, h2*60+m2
		if end == 0 && start == 0 {
			continue // "00:00-00:00" = closed
		}
		if end == 0 {
			end = 24 * 60
		}
		if end <= start {
			// Wrapping window like "19:00-07:00": match either side of
			// midnight (≥ start OR < end).
			if minutes >= start || minutes < end {
				return true
			}
			continue
		}
		if minutes >= start && minutes < end {
			return true
		}
	}
	return false
}

// EffectiveSpec resolves the template chain for an object. Templates are
// applied in declared order (later wins over earlier), the object's own
// spec wins over all templates, defaults fill remaining gaps
// (SPEC §6.2 — exposed via GET …/effective-config).
// lookup resolves a template name; resolution is cycle-guarded.
func EffectiveSpec(obj *Object, lookup func(name string) *Template) (ObjectSpec, []string, error) {
	seen := map[string]bool{}
	var chain []string
	var resolve func(names []string) (ObjectSpec, error)
	resolve = func(names []string) (ObjectSpec, error) {
		acc := ObjectSpec{}
		for _, n := range names {
			if seen[n] {
				return acc, fmt.Errorf("template cycle or duplicate via %q", n)
			}
			seen[n] = true
			t := lookup(n)
			if t == nil {
				return acc, fmt.Errorf("unknown template %q", n)
			}
			chain = append(chain, n)
			base, err := resolve(t.Spec.Templates)
			if err != nil {
				return acc, err
			}
			acc = MergeSpec(acc, MergeSpec(base, t.Spec))
		}
		return acc, nil
	}
	fromTemplates, err := resolve(obj.Spec.Templates)
	if err != nil {
		return ObjectSpec{}, chain, err
	}
	eff := MergeSpec(MergeSpec(SpecDefaults, fromTemplates), obj.Spec)
	eff.Templates = obj.Spec.Templates // keep declaration, not inherited noise
	return eff, chain, nil
}

// MarshalSpec/UnmarshalSpec are the canonical JSON forms stored in the
// objects.spec column.
func MarshalSpec(s ObjectSpec) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func UnmarshalSpec(raw string) (ObjectSpec, error) {
	var s ObjectSpec
	if raw == "" {
		return s, nil
	}
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}
