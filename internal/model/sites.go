package model

import "time"

// Site is a customer-site (edge) instance registered on this main
// instance (SPEC §7.7 deployment variant B). The edge dials out only:
// it heartbeats status up and pulls its config bundle down, so the
// customer firewall needs no inbound rule. Sites are tenant-scoped —
// one tenant per customer keeps RBAC and data isolation intact.
type Site struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	// Bundle is the declarative config (SPEC §11.6 multi-doc YAML) the
	// edge instance pulls and applies. Validated on save.
	Bundle string `json:"bundle,omitempty"`
	// Disabled refuses pulls and heartbeats without deleting history.
	Disabled bool  `json:"disabled,omitempty"`
	Version  int64 `json:"version,omitempty"`
}

// SiteHeartbeat is what an edge reports each interval.
type SiteHeartbeat struct {
	Version    string           `json:"version,omitempty"`    // edge northplaned version
	BundleETag string           `json:"bundleEtag,omitempty"` // bundle revision last applied
	ApplyError string           `json:"applyError,omitempty"` // last bundle apply failure
	Stats      map[string]int64 `json:"stats,omitempty"`      // hosts, services, alertsOpen, …
}

// SiteStatus is the stored runtime state of a site (kv, not versioned —
// heartbeats must not churn the config document).
type SiteStatus struct {
	LastSeenAt *time.Time       `json:"lastSeenAt,omitempty"`
	Version    string           `json:"version,omitempty"`
	BundleETag string           `json:"bundleEtag,omitempty"`
	ApplyError string           `json:"applyError,omitempty"`
	Stats      map[string]int64 `json:"stats,omitempty"`
	SourceIP   string           `json:"sourceIp,omitempty"`
}

// SiteView merges config and runtime state for list/overview responses.
type SiteView struct {
	Site
	Connected bool       `json:"connected"`
	Status    SiteStatus `json:"status"`
}
