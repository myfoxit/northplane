// Package checks implements the builtin in-process checks (SPEC §7.4):
// icmp, tcp, http(s), tls-cert, dns, smtp, imap, ntp, ssh-banner, snmp,
// nrpe, agent (np-agent active listener) and the multistep http-flow
// (SPEC §8.6). No fork — thousands run in parallel. Flags follow
// monitoring-plugins conventions so Nagios users feel at home
// (-H, -p, -w, -c, …).
package checks

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
)

// Target carries object context into a check.
type Target struct {
	Address string
	Vars    model.Vars
}

// Func is a builtin check implementation.
type Func func(ctx context.Context, t Target, args Args) (model.State, nagios.Output)

var registry = map[string]Func{}

func register(name string, fn Func) { registry[name] = fn }

// Names lists available builtins (admin UI test console).
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Run dispatches a builtin by name.
func Run(ctx context.Context, name string, t Target, rawArgs []string) (model.State, nagios.Output) {
	fn, ok := registry[name]
	if !ok {
		return model.StateUnknown, nagios.Output{
			Text: fmt.Sprintf("UNKNOWN - no builtin check %q (available: %s)",
				name, strings.Join(Names(), ", "))}
	}
	return fn(ctx, t, parseArgs(rawArgs))
}

// Args is a parsed flag set: "-H x", "--port=80", positionals.
type Args struct {
	flags map[string]string
	pos   []string
}

func parseArgs(raw []string) Args {
	a := Args{flags: map[string]string{}}
	for i := 0; i < len(raw); i++ {
		tok := raw[i]
		switch {
		case strings.HasPrefix(tok, "--"):
			body := tok[2:]
			if eq := strings.IndexByte(body, '='); eq >= 0 {
				a.flags[body[:eq]] = body[eq+1:]
			} else if i+1 < len(raw) && !strings.HasPrefix(raw[i+1], "-") {
				a.flags[body] = raw[i+1]
				i++
			} else {
				a.flags[body] = "true"
			}
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			key := tok[1:]
			if len(key) > 1 { // -p80 style
				a.flags[key[:1]] = key[1:]
				continue
			}
			if i+1 < len(raw) && (!strings.HasPrefix(raw[i+1], "-") || isNumber(raw[i+1])) {
				a.flags[key] = raw[i+1]
				i++
			} else {
				a.flags[key] = "true"
			}
		default:
			a.pos = append(a.pos, tok)
		}
	}
	return a
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// Get returns the first present flag of the given aliases.
func (a Args) Get(aliases ...string) string {
	for _, k := range aliases {
		if v, ok := a.flags[k]; ok {
			return v
		}
	}
	return ""
}

// Bool reports a boolean flag.
func (a Args) Bool(aliases ...string) bool {
	v := a.Get(aliases...)
	return v == "true" || v == "1" || v == "yes"
}

// Int parses an integer flag.
func (a Args) Int(def int, aliases ...string) int {
	if v := a.Get(aliases...); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Duration parses "5s"/"500ms" or bare seconds.
func (a Args) Duration(def time.Duration, aliases ...string) time.Duration {
	v := a.Get(aliases...)
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(f * float64(time.Second))
	}
	return def
}

// Host resolves the check host: -H flag beats target address.
func (a Args) Host(t Target) string {
	if h := a.Get("H", "hostname", "host"); h != "" {
		return h
	}
	return t.Address
}

// evalPerf grades a measured value against -w/-c ranges and renders the
// standard result text + perfdata.
func evalPerf(name string, value float64, unit string, warn, crit string, extra ...string) (model.State, nagios.Output) {
	st := model.StateOK
	if crit != "" || warn != "" {
		code, err := nagios.Evaluate(value, warn, crit)
		if err != nil {
			return model.StateUnknown, nagios.Output{Text: "UNKNOWN - bad threshold: " + err.Error()}
		}
		st = model.State(code)
	}
	label := map[model.State]string{0: "OK", 1: "WARNING", 2: "CRITICAL", 3: "UNKNOWN"}[st]
	text := fmt.Sprintf("%s %s - %s = %s%s", strings.ToUpper(name), label,
		name, trimFloat(value), unit)
	if len(extra) > 0 {
		text = fmt.Sprintf("%s %s - %s", strings.ToUpper(name), label, strings.Join(extra, ", "))
	}
	perf := fmt.Sprintf("%s=%s%s;%s;%s;;", perfLabel(name), trimFloat(value), unit, warn, crit)
	out := nagios.ParseOutput(text + " | " + perf)
	return st, out
}

func perfLabel(s string) string {
	if strings.ContainsAny(s, " ='") {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return s
}

func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 6, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func unknownf(format string, args ...any) (model.State, nagios.Output) {
	return model.StateUnknown, nagios.Output{Text: "UNKNOWN - " + fmt.Sprintf(format, args...)}
}

func criticalf(format string, args ...any) (model.State, nagios.Output) {
	return model.StateCritical, nagios.Output{Text: "CRITICAL - " + fmt.Sprintf(format, args...)}
}
