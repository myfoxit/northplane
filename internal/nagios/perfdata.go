package nagios

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Perf is one parsed perfdata token:
// 'label'=value[UOM];[warn];[crit];[min];[max] (SPEC §8.3).
type Perf struct {
	Label string   `json:"label"`
	Value float64  `json:"value"`
	UOM   string   `json:"uom,omitempty"` // original unit
	Warn  string   `json:"warn,omitempty"`
	Crit  string   `json:"crit,omitempty"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`

	// Normalised (SPEC §8.3): us|ms|s → s; B|KB|MB|GB|TB → bytes; % kept;
	// c marks a counter (rate derivation downstream).
	NormValue float64 `json:"normValue"`
	NormUnit  string  `json:"normUnit,omitempty"` // "s" | "bytes" | "%" | "c" | ""
	Counter   bool    `json:"counter,omitempty"`
}

// ParsePerfdata tokenises and parses a perfdata string. Broken tokens
// produce warnings and are skipped — never errors (SPEC §8.3).
func ParsePerfdata(s string) ([]Perf, []string) {
	var out []Perf
	var warns []string
	for _, tok := range tokenizePerf(s) {
		p, err := parsePerfToken(tok)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%q: %v", tok, err))
			continue
		}
		out = append(out, p)
	}
	return out, warns
}

// tokenizePerf splits on spaces outside single quotes.
func tokenizePerf(s string) []string {
	var toks []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inQuote = !inQuote
			cur.WriteByte(c)
		case (c == ' ' || c == '\t') && !inQuote:
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

func parsePerfToken(tok string) (Perf, error) {
	var p Perf
	rest := tok
	// label: quoted (may contain spaces/=; '' escapes a quote) or bare
	// (up to first =)
	if strings.HasPrefix(rest, "'") {
		var label strings.Builder
		i, closed := 1, false
		for i < len(rest) {
			if rest[i] == '\'' {
				if i+1 < len(rest) && rest[i+1] == '\'' {
					label.WriteByte('\'')
					i += 2
					continue
				}
				closed = true
				break
			}
			label.WriteByte(rest[i])
			i++
		}
		if !closed {
			return p, fmt.Errorf("unterminated quote")
		}
		p.Label = label.String()
		rest = rest[i+1:]
		if !strings.HasPrefix(rest, "=") {
			return p, fmt.Errorf("missing '=' after label")
		}
		rest = rest[1:]
	} else {
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 {
			return p, fmt.Errorf("missing '='")
		}
		p.Label = rest[:eq]
		rest = rest[eq+1:]
	}
	if p.Label == "" {
		return p, fmt.Errorf("empty label")
	}

	parts := strings.Split(rest, ";")
	valUOM := parts[0]
	if valUOM == "" {
		return p, fmt.Errorf("empty value")
	}
	// split numeric prefix from UOM suffix
	numEnd := len(valUOM)
	for i := 0; i < len(valUOM); i++ {
		c := valUOM[i]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' ||
			c == 'e' || c == 'E' {
			continue
		}
		// 'e'/'E' ambiguity is handled by ParseFloat retry below
		numEnd = i
		break
	}
	val, err := strconv.ParseFloat(valUOM[:numEnd], 64)
	if err != nil {
		// retry: exponent letters may have confused the scan
		if v2, err2 := strconv.ParseFloat(valUOM, 64); err2 == nil {
			val, numEnd = v2, len(valUOM)
		} else {
			return p, fmt.Errorf("bad value %q", valUOM)
		}
	}
	// Reject non-finite values: "NaN"/"Inf" parse cleanly via ParseFloat
	// and an overflowing literal normalizes to ±Inf — both would poison
	// TSDB aggregation and break JSON encoding of the series.
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return p, fmt.Errorf("non-finite value %q", valUOM)
	}
	p.Value = val
	p.UOM = valUOM[numEnd:]

	get := func(i int) string {
		if i < len(parts) {
			return strings.TrimSpace(parts[i])
		}
		return ""
	}
	p.Warn, p.Crit = get(1), get(2)
	if v := get(3); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			p.Min = &f
		}
	}
	if v := get(4); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			p.Max = &f
		}
	}
	if p.Warn != "" {
		if _, err := ParseRange(p.Warn); err != nil {
			return p, fmt.Errorf("bad warn range: %v", err)
		}
	}
	if p.Crit != "" {
		if _, err := ParseRange(p.Crit); err != nil {
			return p, fmt.Errorf("bad crit range: %v", err)
		}
	}
	normalize(&p)
	return p, nil
}

func normalize(p *Perf) {
	switch strings.ToLower(p.UOM) {
	case "":
		p.NormValue, p.NormUnit = p.Value, ""
	case "s":
		p.NormValue, p.NormUnit = p.Value, "s"
	case "ms":
		p.NormValue, p.NormUnit = p.Value/1e3, "s"
	case "us", "µs":
		p.NormValue, p.NormUnit = p.Value/1e6, "s"
	case "%":
		p.NormValue, p.NormUnit = p.Value, "%"
	case "b":
		p.NormValue, p.NormUnit = p.Value, "bytes"
	case "kb":
		p.NormValue, p.NormUnit = p.Value*1024, "bytes"
	case "mb":
		p.NormValue, p.NormUnit = p.Value*1024*1024, "bytes"
	case "gb":
		p.NormValue, p.NormUnit = p.Value*1024*1024*1024, "bytes"
	case "tb":
		p.NormValue, p.NormUnit = p.Value*1024*1024*1024*1024, "bytes"
	case "c":
		p.NormValue, p.NormUnit, p.Counter = p.Value, "c", true
	default:
		p.NormValue, p.NormUnit = p.Value, p.UOM // unknown: pass through
	}
}

// Range is a Nagios threshold range: [@]start:end with ~ = -inf
// (SPEC §8.3). Alert when the value lies outside [start,end] — inverted
// by '@' (alert when inside).
type Range struct {
	Start  float64
	End    float64
	Inside bool // '@' prefix
	raw    string
}

func (r Range) String() string { return r.raw }

// ParseRange parses the threshold range grammar.
func ParseRange(s string) (Range, error) {
	r := Range{raw: s, Start: 0, End: math.Inf(1)}
	body := s
	if strings.HasPrefix(body, "@") {
		r.Inside = true
		body = body[1:]
	}
	if body == "" {
		return r, fmt.Errorf("empty range")
	}
	// tildeVal is the infinity "~" resolves to at this position: -Inf for a
	// start bound, +Inf for an end bound. (The old code always returned
	// -Inf, so "~:~" became [-Inf,-Inf] and alerted on every value.)
	parse := func(v string, emptyDef, tildeVal float64) (float64, error) {
		switch v {
		case "":
			return emptyDef, nil
		case "~":
			return tildeVal, nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			// Fault tolerance (SPEC §8.3): real-world plugins emit UOM
			// suffixes in range bounds ("80%", "5948MB") — strip them.
			end := len(v)
			for end > 0 && (v[end-1] < '0' || v[end-1] > '9') && v[end-1] != '.' {
				end--
			}
			if end == 0 {
				return 0, err
			}
			return strconv.ParseFloat(v[:end], 64)
		}
		return f, nil
	}
	if i := strings.IndexByte(body, ':'); i >= 0 {
		start, err := parse(body[:i], 0, math.Inf(-1))
		if err != nil {
			return r, fmt.Errorf("bad start: %v", err)
		}
		end, err := parse(body[i+1:], math.Inf(1), math.Inf(1))
		if err != nil {
			return r, fmt.Errorf("bad end: %v", err)
		}
		r.Start, r.End = start, end
	} else {
		end, err := parse(body, 0, math.Inf(1))
		if err != nil {
			return r, fmt.Errorf("bad value: %v", err)
		}
		r.Start, r.End = 0, end
	}
	if r.Start > r.End {
		return r, fmt.Errorf("start > end")
	}
	return r, nil
}

// Violated reports whether v triggers the range.
func (r Range) Violated(v float64) bool {
	outside := v < r.Start || v > r.End
	if r.Inside {
		return !outside
	}
	return outside
}

// Evaluate applies warn/crit ranges to a value (builtin checks use this
// for Nagios-faithful thresholds).
func Evaluate(v float64, warn, crit string) (state int, err error) {
	if crit != "" {
		r, err := ParseRange(crit)
		if err != nil {
			return 3, err
		}
		if r.Violated(v) {
			return 2, nil
		}
	}
	if warn != "" {
		r, err := ParseRange(warn)
		if err != nil {
			return 3, err
		}
		if r.Violated(v) {
			return 1, nil
		}
	}
	return 0, nil
}
