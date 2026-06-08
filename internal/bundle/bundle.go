// Package bundle implements declarative config bundles (SPEC §6.2/§11.6):
// multi-document YAML keyed by `kind`, the canonical export format and
// the vehicle of the server-side plan/apply two-step. `np apply`,
// Terraform, the importer and the AI config layer all speak this format.
package bundle

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is one YAML document.
type Doc struct {
	Kind     string         `yaml:"kind"               json:"kind"`
	Metadata Metadata       `yaml:"metadata"           json:"metadata"`
	Spec     map[string]any `yaml:"spec,omitempty"     json:"spec,omitempty"`
	// Non-spec payloads (Dashboard layouts, Report params, …).
	Data map[string]any `yaml:"data,omitempty" json:"data,omitempty"`
}

// Metadata identifies the object.
type Metadata struct {
	Name   string            `yaml:"name"             json:"name"`
	Host   string            `yaml:"host,omitempty"   json:"host,omitempty"` // services
	Folder string            `yaml:"folder,omitempty" json:"folder,omitempty"`
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// Known kinds, in apply order (dependencies before dependents).
var KindOrder = []string{
	"Tenant", "Role", "TimePeriod", "CheckCommand", "Template",
	"Contact", "ContactGroup", "Channel", "Schedule",
	"EscalationPolicy", "EventSource", "AlertGroup", "AlertRule",
	"Host", "Service", "BusinessService", "Heartbeat",
	"Dashboard", "Report", "StaticGroup", "WebhookSubscription",
	"SavedFilter",
}

var kindRank = func() map[string]int {
	m := map[string]int{}
	for i, k := range KindOrder {
		m[k] = i
	}
	return m
}()

// KnownKind reports whether kind is part of the bundle vocabulary.
func KnownKind(kind string) bool {
	_, ok := kindRank[kind]
	return ok
}

// Parse reads a multi-document YAML stream.
func Parse(r io.Reader) ([]Doc, error) {
	dec := yaml.NewDecoder(r)
	var docs []Doc
	for i := 0; ; i++ {
		var d Doc
		err := dec.Decode(&d)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bundle: document %d: %w", i+1, err)
		}
		if d.Kind == "" && d.Metadata.Name == "" && d.Spec == nil {
			continue // empty doc (--- separators)
		}
		if d.Kind == "" {
			return nil, fmt.Errorf("bundle: document %d: missing kind", i+1)
		}
		if !KnownKind(d.Kind) {
			return nil, fmt.Errorf("bundle: document %d: unknown kind %q", i+1, d.Kind)
		}
		if d.Metadata.Name == "" {
			return nil, fmt.Errorf("bundle: document %d (%s): missing metadata.name", i+1, d.Kind)
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// ParseBytes is Parse over a byte slice.
func ParseBytes(b []byte) ([]Doc, error) { return Parse(bytes.NewReader(b)) }

// Render writes docs as a canonical multi-document YAML stream, sorted
// by apply order then name (round-trip stable, SPEC §11.6).
func Render(docs []Doc) ([]byte, error) {
	sorted := make([]Doc, len(docs))
	copy(sorted, docs)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := kindRank[sorted[i].Kind], kindRank[sorted[j].Kind]
		if ri != rj {
			return ri < rj
		}
		if sorted[i].Metadata.Host != sorted[j].Metadata.Host {
			return sorted[i].Metadata.Host < sorted[j].Metadata.Host
		}
		return sorted[i].Metadata.Name < sorted[j].Metadata.Name
	})
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, d := range sorted {
		if err := enc.Encode(d); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Ident returns the unique identity of a doc within a bundle.
func (d Doc) Ident() string {
	if d.Kind == "Service" {
		return d.Kind + "/" + d.Metadata.Host + "/" + d.Metadata.Name
	}
	return d.Kind + "/" + d.Metadata.Name
}

// Validate performs structural validation common to all consumers.
func Validate(docs []Doc) []string {
	var errs []string
	seen := map[string]bool{}
	for _, d := range docs {
		id := d.Ident()
		if seen[id] {
			errs = append(errs, fmt.Sprintf("duplicate %s", id))
		}
		seen[id] = true
		if d.Kind == "Service" && d.Metadata.Host == "" {
			errs = append(errs, fmt.Sprintf("%s: service requires metadata.host", id))
		}
		if strings.ContainsAny(d.Metadata.Name, "\n\t") {
			errs = append(errs, fmt.Sprintf("%s: invalid name", id))
		}
	}
	return errs
}
