package nagios

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// MacroContext carries everything macro expansion can reference
// (SPEC §8.2). All fields optional; absent values expand to "".
type MacroContext struct {
	Host        *model.Object
	HostState   *model.CheckState
	Service     *model.Object
	ServiceState *model.CheckState
	HostSpec    *model.ObjectSpec // effective specs (templates resolved)
	ServiceSpec *model.ObjectSpec

	Args []string // $ARG1$..$ARG32$

	NotificationType   string // PROBLEM|RECOVERY|ACKNOWLEDGEMENT|…
	NotificationNumber int
	ContactName        string
	ContactEmail       string

	Now time.Time

	// Secrets resolves $SECRET:name$ from the encrypted store; values
	// are masked in logs/UI by the caller (SPEC §8.2).
	Secrets func(name string) (string, bool)
	// User maps $USERn$ (classic resource.cfg); USER1 conventionally is
	// the plugins directory.
	User func(n int) (string, bool)
}

// Expand substitutes $MACRO$ tokens. Unknown macros stay verbatim and
// are reported (the command test console surfaces them).
func (mc *MacroContext) Expand(s string) (string, []string) {
	var unknown []string
	var b strings.Builder
	b.Grow(len(s))
	for {
		i := strings.IndexByte(s, '$')
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		rest := s[i+1:]
		j := strings.IndexByte(rest, '$')
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		name := rest[:j]
		if name == "" { // "$$" escapes a literal dollar
			b.WriteByte('$')
			s = rest[j+1:]
			continue
		}
		if v, ok := mc.resolve(name); ok {
			b.WriteString(v)
		} else {
			b.WriteString("$" + name + "$")
			unknown = append(unknown, name)
		}
		s = rest[j+1:]
	}
	return b.String(), unknown
}

// ExpandArgs expands every argv element (no shell interpolation —
// SPEC §13.1: argv array stays an array).
func (mc *MacroContext) ExpandArgs(argv []string) ([]string, []string) {
	out := make([]string, len(argv))
	var unknown []string
	for i, a := range argv {
		v, u := mc.Expand(a)
		out[i] = v
		unknown = append(unknown, u...)
	}
	return out, unknown
}

func (mc *MacroContext) now() time.Time {
	if mc.Now.IsZero() {
		return time.Now()
	}
	return mc.Now
}

func (mc *MacroContext) resolve(name string) (string, bool) {
	// $SECRET:name$ (SPEC §8.2)
	if rest, ok := strings.CutPrefix(name, "SECRET:"); ok {
		if mc.Secrets != nil {
			if v, ok := mc.Secrets(rest); ok {
				return v, true
			}
		}
		return "", false
	}
	// $ARGn$
	if rest, ok := strings.CutPrefix(name, "ARG"); ok {
		if n, err := strconv.Atoi(rest); err == nil && n >= 1 && n <= 32 {
			if n <= len(mc.Args) {
				return mc.Args[n-1], true
			}
			return "", true // unset args expand empty (Nagios semantics)
		}
	}
	// $USERn$
	if rest, ok := strings.CutPrefix(name, "USER"); ok {
		if n, err := strconv.Atoi(rest); err == nil && mc.User != nil {
			if v, ok := mc.User(n); ok {
				return v, true
			}
			return "", false
		}
	}
	// Custom variables: $_HOSTFOO$ / $_SERVICEFOO$
	if rest, ok := strings.CutPrefix(name, "_HOST"); ok {
		if mc.HostSpec != nil {
			if v, ok := lookupVar(mc.HostSpec.Vars, rest); ok {
				return v, true
			}
		}
		return "", true
	}
	if rest, ok := strings.CutPrefix(name, "_SERVICE"); ok {
		if mc.ServiceSpec != nil {
			if v, ok := lookupVar(mc.ServiceSpec.Vars, rest); ok {
				return v, true
			}
		}
		return "", true
	}

	host, hs := mc.Host, mc.HostState
	svc, ss := mc.Service, mc.ServiceState
	switch name {
	case "HOSTNAME":
		if host != nil {
			return host.Name, true
		}
	case "HOSTALIAS", "HOSTDISPLAYNAME":
		if host != nil {
			return host.Name, true
		}
	case "HOSTADDRESS":
		if mc.HostSpec != nil && mc.HostSpec.Address != "" {
			return mc.HostSpec.Address, true
		}
		if host != nil {
			return host.Name, true
		}
	case "HOSTSTATE":
		if hs != nil {
			return hs.State.HostLabel(), true
		}
	case "HOSTSTATEID":
		if hs != nil {
			return strconv.Itoa(int(hs.State)), true
		}
	case "HOSTSTATETYPE":
		if hs != nil {
			return strings.ToUpper(string(hs.StateType)), true
		}
	case "HOSTATTEMPT":
		if hs != nil {
			return strconv.Itoa(hs.Attempt), true
		}
	case "MAXHOSTATTEMPTS":
		if mc.HostSpec != nil {
			return strconv.Itoa(mc.HostSpec.MaxCheckAttempts), true
		}
	case "HOSTOUTPUT":
		if hs != nil {
			return hs.Output, true
		}
	case "LONGHOSTOUTPUT":
		if hs != nil {
			return hs.LongOutput, true
		}
	case "HOSTPERFDATA":
		if hs != nil {
			return hs.Perfdata, true
		}
	case "HOSTLATENCY":
		if hs != nil {
			return fmt.Sprintf("%.3f", float64(hs.LatencyMS)/1000), true
		}
	case "HOSTEXECUTIONTIME":
		if hs != nil {
			return fmt.Sprintf("%.3f", float64(hs.ExecMS)/1000), true
		}
	case "LASTHOSTCHECK":
		if hs != nil && hs.LastCheck != nil {
			return strconv.FormatInt(hs.LastCheck.Unix(), 10), true
		}
	case "LASTHOSTSTATECHANGE":
		if hs != nil && hs.LastHardChange != nil {
			return strconv.FormatInt(hs.LastHardChange.Unix(), 10), true
		}

	case "SERVICEDESC":
		if svc != nil {
			return svc.Name, true
		}
	case "SERVICEDISPLAYNAME":
		if svc != nil {
			return svc.Name, true
		}
	case "SERVICESTATE":
		if ss != nil {
			return ss.State.ServiceLabel(), true
		}
	case "SERVICESTATEID":
		if ss != nil {
			return strconv.Itoa(int(ss.State)), true
		}
	case "SERVICESTATETYPE":
		if ss != nil {
			return strings.ToUpper(string(ss.StateType)), true
		}
	case "SERVICEATTEMPT":
		if ss != nil {
			return strconv.Itoa(ss.Attempt), true
		}
	case "MAXSERVICEATTEMPTS":
		if mc.ServiceSpec != nil {
			return strconv.Itoa(mc.ServiceSpec.MaxCheckAttempts), true
		}
	case "SERVICEOUTPUT":
		if ss != nil {
			return ss.Output, true
		}
	case "LONGSERVICEOUTPUT":
		if ss != nil {
			return ss.LongOutput, true
		}
	case "SERVICEPERFDATA":
		if ss != nil {
			return ss.Perfdata, true
		}
	case "SERVICELATENCY":
		if ss != nil {
			return fmt.Sprintf("%.3f", float64(ss.LatencyMS)/1000), true
		}
	case "SERVICEEXECUTIONTIME":
		if ss != nil {
			return fmt.Sprintf("%.3f", float64(ss.ExecMS)/1000), true
		}
	case "LASTSERVICECHECK":
		if ss != nil && ss.LastCheck != nil {
			return strconv.FormatInt(ss.LastCheck.Unix(), 10), true
		}
	case "LASTSERVICESTATECHANGE":
		if ss != nil && ss.LastHardChange != nil {
			return strconv.FormatInt(ss.LastHardChange.Unix(), 10), true
		}

	case "NOTIFICATIONTYPE":
		return mc.NotificationType, true
	case "NOTIFICATIONNUMBER":
		return strconv.Itoa(mc.NotificationNumber), true
	case "CONTACTNAME":
		return mc.ContactName, true
	case "CONTACTEMAIL":
		return mc.ContactEmail, true

	case "TIMET":
		return strconv.FormatInt(mc.now().Unix(), 10), true
	case "LONGDATETIME":
		return mc.now().Format("Mon Jan 2 15:04:05 MST 2006"), true
	case "SHORTDATETIME":
		return mc.now().Format("01-02-2006 15:04:05"), true
	case "DATE":
		return mc.now().Format("01-02-2006"), true
	case "TIME":
		return mc.now().Format("15:04:05"), true
	}
	return "", false
}

func lookupVar(vars model.Vars, suffix string) (string, bool) {
	if vars == nil {
		return "", false
	}
	// $_HOSTSSH_PORT$ ↔ vars["ssh_port"] — case-insensitive,
	// underscores preserved.
	want := strings.ToLower(suffix)
	for k, v := range vars {
		if strings.ToLower(k) == want {
			return v, true
		}
	}
	return "", false
}

// EnvVars renders macros as environment variables (NAGIOS_* plus
// NORTHPLANE_* aliases) — switchable per command (SPEC §8.2).
func (mc *MacroContext) EnvVars() []string {
	names := []string{
		"HOSTNAME", "HOSTADDRESS", "HOSTSTATE", "HOSTSTATEID", "HOSTOUTPUT",
		"SERVICEDESC", "SERVICESTATE", "SERVICESTATEID", "SERVICEOUTPUT",
		"SERVICEPERFDATA", "TIMET",
	}
	var env []string
	for _, n := range names {
		if v, ok := mc.resolve(n); ok && v != "" {
			env = append(env, "NAGIOS_"+n+"="+v, "NORTHPLANE_"+n+"="+v)
		}
	}
	return env
}
