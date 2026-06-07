package model

import (
	"encoding/json"
	"time"
)

// BSRule aggregates child states in a BusinessService node (SPEC §9.7).
type BSRule string

const (
	BSWorst    BSRule = "worst"
	BSBest     BSRule = "best"
	BSQuorum   BSRule = "quorum"   // healthy if ≥ QuorumPct % children OK
	BSWeighted BSRule = "weighted" // severity = max weight-passing child
)

// BusinessService is a node in the BPI tree/DAG. Leaves reference
// objects (ID or selector); inner nodes aggregate children.
type BusinessService struct {
	ID        string  `json:"id"`
	TenantID  string  `json:"tenantId"`
	Name      string  `json:"name"`
	ParentID  string  `json:"parentId,omitempty"`
	Rule      BSRule  `json:"rule,omitempty"`
	QuorumPct float64 `json:"quorumPct,omitempty"`
	// Leaf bindings:
	ObjectID string  `json:"objectId,omitempty"`
	Selector string  `json:"selector,omitempty"`
	Weight   float64 `json:"weight,omitempty"`
	// SLA definition (SPEC §9.7): target %, window, planned downtimes excluded.
	SLATarget   float64 `json:"slaTarget,omitempty"` // e.g. 99.9
	SLAWindow   string  `json:"slaWindow,omitempty"` // "month" | "quarter" | "year"
	ExclDowntime bool   `json:"excludeDowntimes,omitempty"`
	Version     int64   `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Dashboard is a grid of widgets (SPEC §12.3).
type Dashboard struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenantId"`
	Name      string          `json:"name"`
	OwnerID   string          `json:"ownerId,omitempty"` // empty = shared/global
	Shared    bool            `json:"shared"`
	// Layout/widgets are an opaque, schema-validated JSON document owned
	// by the frontend (widget types: chart, status-map, list, markdown).
	Spec      json.RawMessage `json:"spec"`
	ShareToken string         `json:"shareToken,omitempty"` // read-only wallboard link
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// ReportType enumerates built-in reports (SPEC §9.8).
type ReportType string

const (
	ReportAvailability ReportType = "availability"
	ReportSLA          ReportType = "sla"
	ReportAlertStats   ReportType = "alert-stats" // MTTA/MTTR, top offenders
	ReportOnCall       ReportType = "oncall"
	ReportAudit        ReportType = "audit" // permissions/revision (A-15.07)
)

// Report is a stored report definition; rendering happens on demand or
// per schedule (POST /reports:render).
type Report struct {
	ID       string          `json:"id"`
	TenantID string          `json:"tenantId"`
	Name     string          `json:"name"`
	Type     ReportType      `json:"type"`
	// Params: selector, business service, window ("30d"), include
	// downtimes, …
	Params   json.RawMessage `json:"params"`
	Schedule string          `json:"schedule,omitempty"` // cron-ish: "monthly", "weekly:monday", "daily"
	Email    []string        `json:"email,omitempty"`    // recipients
	Keep     int             `json:"keep,omitempty"`     // archive retention count
	Version  int64           `json:"version"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// CheckResult is the executor → pipeline unit (SPEC §7.4) — also the
// passive-submission body (§8.5).
type CheckResult struct {
	ObjectID   string    `json:"objectId,omitempty"`
	Host       string    `json:"host,omitempty"`    // passive: by name
	Service    string    `json:"service,omitempty"` // passive: by name
	State      State     `json:"state"`
	Output     string    `json:"output"`
	LongOutput string    `json:"longOutput,omitempty"`
	Perfdata   string    `json:"perfdata,omitempty"`
	At         time.Time `json:"at,omitempty"`
	LatencyMS  int64     `json:"latencyMs,omitempty"` // schedule → start
	ExecMS     int64     `json:"execMs,omitempty"`    // start → done
	Timeout    bool      `json:"timeout,omitempty"`
	Source     string    `json:"source,omitempty"` // scheduler|passive|agent|satellite:<zone>
}

// CheckState is the hot current-state row (SPEC §6.5).
type CheckState struct {
	ObjectID      string     `json:"objectId"`
	State         State      `json:"state"`
	StateType     StateType  `json:"stateType"`
	Attempt       int        `json:"attempt"`
	Output        string     `json:"output,omitempty"`
	LongOutput    string     `json:"longOutput,omitempty"`
	Perfdata      string     `json:"perfdata,omitempty"`
	LatencyMS     int64      `json:"latencyMs,omitempty"`
	ExecMS        int64      `json:"execMs,omitempty"`
	LastCheck     *time.Time `json:"lastCheck,omitempty"`
	NextCheck     *time.Time `json:"nextCheck,omitempty"`
	LastHardChange *time.Time `json:"lastHardChange,omitempty"`
	LastOK        *time.Time `json:"lastOk,omitempty"`
	Flapping      bool       `json:"flapping"`
	FlapPct       float64    `json:"flapPct,omitempty"`
	AckedBy       string     `json:"ackedBy,omitempty"`
	AckComment    string     `json:"ackComment,omitempty"`
	DowntimeDepth int        `json:"downtimeDepth"`
	// FlapHistory: bitfield of the last 21 transitions (LSB = newest).
	FlapHistory uint32 `json:"-"`
}

// InDowntime reports whether the object is currently in scheduled downtime.
func (cs *CheckState) InDowntime() bool { return cs.DowntimeDepth > 0 }

// Problem reports whether the state is a non-OK hard state.
func (cs *CheckState) Problem() bool {
	return cs.State != StateOK && cs.StateType == StateHard
}
