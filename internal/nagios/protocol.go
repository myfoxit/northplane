// Package nagios implements the Nagios compatibility layer (SPEC §8):
// the plugin protocol (exit codes, output, perfdata), the macro engine,
// an NRPE client and the configuration importer. The
// Monitoring-Plugins specification is the norm.
package nagios

import (
	"strings"
	"unicode/utf8"

	"github.com/northplane/northplane/internal/model"
)

// MaxOutput caps plugin output (SPEC §8.1, configurable upstream).
const MaxOutput = 64 * 1024

// ExitState maps a process exit code to a check state (SPEC §8.1):
// 0/1/2/3 → OK/WARNING/CRITICAL/UNKNOWN; anything else → UNKNOWN.
func ExitState(code int) model.State {
	switch code {
	case 0:
		return model.StateOK
	case 1:
		return model.StateWarning
	case 2:
		return model.StateCritical
	default:
		return model.StateUnknown
	}
}

// Output is the parsed plugin stdout.
type Output struct {
	Text       string // first line status text
	LongText   string // additional lines
	Perfdata   string // raw perfdata (joined)
	Metrics    []Perf // parsed perfdata
	ParseWarns []string
}

// ParseOutput implements the full output grammar (SPEC §8.1) including
// multiline perfdata continuation:
//
//	TEXT OUTPUT | OPTIONAL PERFDATA
//	LONG TEXT LINE 1
//	LONG TEXT LINE N | PERFDATA LINE 2
//	PERFDATA LINE 3 …
//
// Input is sanitised: capped at MaxOutput, invalid UTF-8 is re-decoded
// as Latin-1 (SPEC §8.1).
func ParseOutput(raw string) Output {
	if len(raw) > MaxOutput {
		raw = raw[:MaxOutput]
	}
	raw = sanitizeUTF8(raw)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")

	var out Output
	lines := strings.Split(raw, "\n")

	first := lines[0]
	if i := strings.IndexByte(first, '|'); i >= 0 {
		out.Text = strings.TrimSpace(first[:i])
		out.Perfdata = strings.TrimSpace(first[i+1:])
	} else {
		out.Text = strings.TrimSpace(first)
	}

	var longLines []string
	var perfLines []string
	inPerf := false
	for _, line := range lines[1:] {
		if inPerf {
			perfLines = append(perfLines, strings.TrimSpace(line))
			continue
		}
		if i := strings.IndexByte(line, '|'); i >= 0 {
			longLines = append(longLines, strings.TrimRight(line[:i], " \t"))
			perfLines = append(perfLines, strings.TrimSpace(line[i+1:]))
			inPerf = true
			continue
		}
		longLines = append(longLines, line)
	}
	out.LongText = strings.TrimRight(strings.Join(longLines, "\n"), "\n \t")
	if len(perfLines) > 0 {
		if out.Perfdata != "" {
			out.Perfdata += " "
		}
		out.Perfdata += strings.Join(perfLines, " ")
	}
	if out.Perfdata != "" {
		// Fault tolerance (SPEC §8.3): broken perfdata yields parse
		// warnings, never check errors.
		out.Metrics, out.ParseWarns = ParsePerfdata(out.Perfdata)
	}
	return out
}

// sanitizeUTF8 keeps valid UTF-8 untouched and falls back to a Latin-1
// reinterpretation for legacy plugin output (SPEC §8.1).
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	// Latin-1 → UTF-8: each byte maps to the same code point.
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	for i := 0; i < len(s); i++ {
		b.WriteRune(rune(s[i]))
	}
	return b.String()
}
