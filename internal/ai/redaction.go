package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync"

	"github.com/northplane/northplane/internal/config"
)

// Redactor implements the privacy pipeline run before every LLM call
// (SPEC §10.1/§13.6): secrets/PII patterns always, optional hostname
// pseudonymisation, custom patterns. Pseudonyms are stable within a
// session so the model can reason about "host-a1b2" consistently.
type Redactor struct {
	pseudoHosts bool
	patterns    []*regexp.Regexp

	mu      sync.Mutex
	mapping map[string]string
	counter int
}

// alwaysPatterns are redacted regardless of configuration.
var alwaysPatterns = []*regexp.Regexp{
	regexp.MustCompile(`np_[A-Za-z0-9]{16,}`),                              // API tokens
	regexp.MustCompile(`(?i)(password|passwd|secret|api[_-]?key|token)\s*[:=]\s*\S+`),
	regexp.MustCompile(`-----BEGIN [A-Z ]+PRIVATE KEY-----`),
	regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`), // emails
	regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`),                        // IPv4
	regexp.MustCompile(`\b[A-Fa-f0-9]{2}(?::[A-Fa-f0-9]{2}){5}\b`),          // MAC
}

var hostnamePattern = regexp.MustCompile(`\b[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+\b`)

// NewRedactor builds the pipeline from config.
func NewRedactor(cfg config.RedactionConfig) *Redactor {
	r := &Redactor{
		pseudoHosts: cfg.Hostnames == "pseudonymize",
		mapping:     map[string]string{},
	}
	for _, p := range cfg.CustomPatterns {
		if re, err := regexp.Compile(p); err == nil {
			r.patterns = append(r.patterns, re)
		}
	}
	return r
}

// Redact returns the sanitised text. Order matters: specific secrets
// first, then optional hostnames (IPs already gone).
func (r *Redactor) Redact(text string) string {
	for _, re := range alwaysPatterns {
		text = re.ReplaceAllStringFunc(text, func(m string) string {
			return "[REDACTED:" + hashTag(m) + "]"
		})
	}
	for _, re := range r.patterns {
		text = re.ReplaceAllString(text, "[REDACTED]")
	}
	if r.pseudoHosts {
		text = hostnamePattern.ReplaceAllStringFunc(text, r.pseudoHost)
	}
	return text
}

func (r *Redactor) pseudoHost(host string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.mapping[host]; ok {
		return p
	}
	r.counter++
	// Full counter width so the mapping stays injective beyond 65 535
	// distinct hosts in a session.
	p := fmt.Sprintf("host-%04x", r.counter)
	r.mapping[host] = p
	return p
}

func hashTag(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:6]
}
