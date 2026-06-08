package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/eventbus"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
	"github.com/northplane/northplane/internal/scheduler"
)

// newExec builds a minimal executor with the given options for unit tests.
// catalog/store are nil because the helpers under test (timeoutFor,
// macroContext, resolvePlugin, runExec) never touch them.
func newExec(opt Options) *Executor {
	return New(opt, nil, eventbus.New(), nil)
}

// hostEntry assembles a host catalog.Entry with an effective spec.
func hostEntry(name string, spec model.ObjectSpec) *catalog.Entry {
	obj := &model.Object{ID: name, TenantID: model.DefaultTenant, Kind: model.KindHost, Name: name}
	return &catalog.Entry{Object: obj, Effective: spec}
}

func TestTimeoutFor(t *testing.T) {
	ex := newExec(Options{DefaultTimeout: 7 * time.Second})

	tests := []struct {
		name    string
		timeout model.Duration
		want    time.Duration
	}{
		{"zero spec timeout falls back to default", 0, 7 * time.Second},
		{"explicit spec timeout wins", model.Duration(3 * time.Second), 3 * time.Second},
		{"large spec timeout wins", model.Duration(45 * time.Second), 45 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := hostEntry("h", model.ObjectSpec{Timeout: tc.timeout})
			if got := ex.timeoutFor(e); got != tc.want {
				t.Fatalf("timeoutFor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTimeoutForDefaultClamp(t *testing.T) {
	// New() clamps a non-positive DefaultTimeout to 30s.
	ex := newExec(Options{DefaultTimeout: 0})
	e := hostEntry("h", model.ObjectSpec{})
	if got := ex.timeoutFor(e); got != 30*time.Second {
		t.Fatalf("default clamp = %v, want 30s", got)
	}
}

func TestCappedWriter(t *testing.T) {
	tests := []struct {
		name   string
		max    int
		writes []string
		want   string
	}{
		{"short output untouched", 1024, []string{"OK - fine"}, "OK - fine"},
		{"exact boundary kept whole", 5, []string{"12345"}, "12345"},
		{"single oversized write truncates at boundary", 5, []string{"1234567890"}, "12345"},
		{"multiple writes truncate at boundary", 5, []string{"123", "456", "789"}, "12345"},
		{"writes after cap are swallowed", 3, []string{"abc", "def"}, "abc"},
		{"empty writes leave nothing", 4, []string{"", ""}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := newCappedWriter(&buf, tc.max)
			total := 0
			for _, s := range tc.writes {
				n, err := w.Write([]byte(s))
				if err != nil {
					t.Fatalf("write %q: %v", s, err)
				}
				// cappedWriter always reports the full length consumed so the
				// child process is never told its pipe is backed up.
				if n != len(s) {
					t.Fatalf("Write returned %d, want %d (full length)", n, len(s))
				}
				total += n
			}
			if got := buf.String(); got != tc.want {
				t.Fatalf("buffer = %q, want %q", got, tc.want)
			}
			if buf.Len() > tc.max {
				t.Fatalf("buffer length %d exceeds cap %d", buf.Len(), tc.max)
			}
		})
	}
}

func TestCappedWriterPreservesBytesUpToCap(t *testing.T) {
	// A write that straddles the cap must keep exactly the prefix that fits.
	var buf bytes.Buffer
	w := newCappedWriter(&buf, 8)
	if _, err := w.Write([]byte("AAAA")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("BBBBBB")); err != nil { // 6 bytes, only 4 fit
		t.Fatal(err)
	}
	if got := buf.String(); got != "AAAABBBB" {
		t.Fatalf("buffer = %q, want %q", got, "AAAABBBB")
	}
}

func TestMacroContextExpansion(t *testing.T) {
	ex := newExec(Options{
		PluginsDir: "/opt/plugins",
		Secrets: func(tenantID, name string) (string, bool) {
			if tenantID == model.DefaultTenant && name == "db_pass" {
				return "s3cr3t", true
			}
			return "", false
		},
	})

	host := &model.Object{
		ID: "h1", TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "web-01",
	}
	spec := model.ObjectSpec{
		Address: "10.0.0.5",
		Args:    []string{"-w", "80"},
		Vars:    model.Vars{"ssh_port": "2222"},
	}
	e := &catalog.Entry{Object: host, Effective: spec, MacroArgs: spec.Args}
	mc := ex.macroContext(e)

	// Known host macros expand.
	if got, _ := mc.Expand("$HOSTNAME$ @ $HOSTADDRESS$"); got != "web-01 @ 10.0.0.5" {
		t.Fatalf("host macros = %q", got)
	}
	// $ARGn$ pulls from MacroArgs.
	if got, _ := mc.Expand("$ARG1$=$ARG2$"); got != "-w=80" {
		t.Fatalf("arg macros = %q", got)
	}
	// $USER1$ maps to the plugins dir (resolver wired by macroContext).
	if got, _ := mc.Expand("$USER1$/check"); got != "/opt/plugins/check" {
		t.Fatalf("USER1 macro = %q", got)
	}
	// Custom host var via $_HOSTSSH_PORT$ (case-insensitive).
	if got, _ := mc.Expand("port=$_HOSTSSH_PORT$"); got != "port=2222" {
		t.Fatalf("custom var macro = %q", got)
	}
	// Secret resolves through the tenant-bound closure.
	if got, _ := mc.Expand("$SECRET:db_pass$"); got != "s3cr3t" {
		t.Fatalf("secret macro = %q", got)
	}
	// Unknown secret resolves to empty and is reported unknown (left as the
	// resolver returns false → token stays verbatim).
	if got, unknown := mc.Expand("$SECRET:missing$"); got != "$SECRET:missing$" || len(unknown) != 1 {
		t.Fatalf("missing secret = %q unknown=%v", got, unknown)
	}
	// Truly unknown macro is left verbatim and reported.
	if got, unknown := mc.Expand("$BOGUS$ tail"); got != "$BOGUS$ tail" || len(unknown) != 1 || unknown[0] != "BOGUS" {
		t.Fatalf("unknown macro = %q unknown=%v", got, unknown)
	}
	// $$ escapes a literal dollar.
	if got, _ := mc.Expand("cost $$5"); got != "cost $5" {
		t.Fatalf("dollar escape = %q", got)
	}
	// Unbalanced trailing $ is left intact.
	if got, _ := mc.Expand("price $5"); got != "price $5" {
		t.Fatalf("unbalanced dollar = %q", got)
	}
}

func TestMacroContextServiceUsesHost(t *testing.T) {
	ex := newExec(Options{})
	hostObj := &model.Object{ID: "h", TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "db-01"}
	hostEnt := &catalog.Entry{Object: hostObj, Effective: model.ObjectSpec{Address: "10.1.1.1"}}
	svcObj := &model.Object{ID: "s", TenantID: model.DefaultTenant, Kind: model.KindService, Name: "pg", HostID: "h"}
	svcEnt := &catalog.Entry{
		Object:    svcObj,
		Effective: model.ObjectSpec{},
		Host:      hostEnt,
	}
	mc := ex.macroContext(svcEnt)
	// Service context still resolves the parent host's name/address.
	if got, _ := mc.Expand("$SERVICEDESC$ on $HOSTNAME$ ($HOSTADDRESS$)"); got != "pg on db-01 (10.1.1.1)" {
		t.Fatalf("service+host macros = %q", got)
	}
}

func TestResolvePlugin(t *testing.T) {
	t.Run("no allowlist", func(t *testing.T) {
		ex := newExec(Options{PluginsDir: "/opt/plugins"})

		// Relative names join under PluginsDir.
		if got, err := ex.resolvePlugin("check_http"); err != nil || got != "/opt/plugins/check_http" {
			t.Fatalf("relative = %q err=%v", got, err)
		}
		// Absolute paths pass through unchanged.
		if got, err := ex.resolvePlugin("/usr/lib/nagios/check_ping"); err != nil || got != "/usr/lib/nagios/check_ping" {
			t.Fatalf("absolute = %q err=%v", got, err)
		}
		// Traversal in a relative path is rejected.
		if _, err := ex.resolvePlugin("../../etc/passwd"); err != os.ErrPermission {
			t.Fatalf("traversal err = %v, want ErrPermission", err)
		}
		if _, err := ex.resolvePlugin("sub/../../escape"); err != os.ErrPermission {
			t.Fatalf("nested traversal err = %v, want ErrPermission", err)
		}
	})

	t.Run("with allowlist", func(t *testing.T) {
		ex := newExec(Options{
			PluginsDir:   "/opt/plugins",
			PluginsAllow: []string{"check_http", "check_ping"},
		})
		// Allowlisted basename resolves.
		if got, err := ex.resolvePlugin("check_http"); err != nil || got != "/opt/plugins/check_http" {
			t.Fatalf("allowed = %q err=%v", got, err)
		}
		// Allowlist matches by basename even for absolute paths.
		if got, err := ex.resolvePlugin("/elsewhere/check_ping"); err != nil || got != "/elsewhere/check_ping" {
			t.Fatalf("allowed abs = %q err=%v", got, err)
		}
		// Not on the allowlist → permission denied.
		if _, err := ex.resolvePlugin("check_secret"); err != os.ErrPermission {
			t.Fatalf("denied err = %v, want ErrPermission", err)
		}
		// An attacker who sneaks an allowed basename onto a traversal path is
		// still stopped by the basename allowlist iff the basename differs;
		// here basename is allowed, so the allowlist passes — but traversal
		// is then caught by the ".." guard.
		if _, err := ex.resolvePlugin("../check_http"); err != os.ErrPermission {
			t.Fatalf("allowed-but-traversal err = %v, want ErrPermission", err)
		}
	})
}

// --- real exec round-trips -------------------------------------------------

// drainResult runs fn (which emits exactly one result on bus.Results) and
// returns it, failing if nothing is emitted promptly.
func drainResult(t *testing.T, ex *Executor, fn func()) *model.CheckResult {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case res := <-ex.bus.Results:
		<-done
		return res
	case <-time.After(10 * time.Second):
		t.Fatal("no result emitted within 10s")
		return nil
	}
}

// execEntry builds an exec-class entry whose argv is an absolute command
// (resolvePlugin passes absolute paths through, so PluginsDir is irrelevant).
func execEntry(argv []string, timeout time.Duration) *catalog.Entry {
	obj := &model.Object{ID: "exec-obj", TenantID: model.DefaultTenant, Kind: model.KindHost, Name: "exec-obj"}
	return &catalog.Entry{
		Object:    obj,
		Effective: model.ObjectSpec{Timeout: model.Duration(timeout)},
		Class:     model.CommandExec,
		Argv:      argv,
	}
}

func TestRunExecRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell semantics required")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	ex := newExec(Options{DefaultTimeout: 5 * time.Second})
	job := scheduler.Job{ObjectID: "exec-obj", Planned: time.Now()}

	t.Run("exit 0 maps to OK and captures stdout", func(t *testing.T) {
		e := execEntry([]string{"/bin/sh", "-c", "echo 'OK - all good | x=1;2;3'; exit 0"}, 5*time.Second)
		res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
		if res.State != model.StateOK {
			t.Fatalf("state = %v, want OK", res.State)
		}
		if res.Output != "OK - all good" {
			t.Fatalf("output = %q", res.Output)
		}
		if res.Perfdata != "x=1;2;3" {
			t.Fatalf("perfdata = %q", res.Perfdata)
		}
		if res.Timeout {
			t.Fatal("Timeout flag set on a clean run")
		}
	})

	t.Run("exit 2 maps to CRITICAL", func(t *testing.T) {
		e := execEntry([]string{"/bin/sh", "-c", "echo 'CRITICAL - down'; exit 2"}, 5*time.Second)
		res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
		if res.State != model.StateCritical {
			t.Fatalf("state = %v, want CRITICAL", res.State)
		}
		if res.Output != "CRITICAL - down" {
			t.Fatalf("output = %q", res.Output)
		}
	})

	t.Run("exit 1 maps to WARNING", func(t *testing.T) {
		e := execEntry([]string{"/bin/sh", "-c", "echo 'WARNING - high'; exit 1"}, 5*time.Second)
		res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
		if res.State != model.StateWarning {
			t.Fatalf("state = %v, want WARNING", res.State)
		}
	})

	t.Run("out-of-range exit code maps to UNKNOWN", func(t *testing.T) {
		e := execEntry([]string{"/bin/sh", "-c", "exit 7"}, 5*time.Second)
		res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
		if res.State != model.StateUnknown {
			t.Fatalf("state = %v, want UNKNOWN", res.State)
		}
	})

	t.Run("stderr surfaces as long output detail", func(t *testing.T) {
		e := execEntry([]string{"/bin/sh", "-c", "echo 'OK - up'; echo 'noise' 1>&2; exit 0"}, 5*time.Second)
		res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
		if res.State != model.StateOK {
			t.Fatalf("state = %v, want OK", res.State)
		}
		if !strings.Contains(res.LongOutput, "[stderr] noise") {
			t.Fatalf("long output = %q, want [stderr] noise", res.LongOutput)
		}
	})

	t.Run("non-existent plugin maps to UNKNOWN", func(t *testing.T) {
		e := execEntry([]string{"/nonexistent/np-plugin-xyz"}, 5*time.Second)
		res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
		if res.State != model.StateUnknown {
			t.Fatalf("state = %v, want UNKNOWN", res.State)
		}
	})

	t.Run("empty command maps to UNKNOWN", func(t *testing.T) {
		e := execEntry([]string{""}, 5*time.Second)
		res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
		if res.State != model.StateUnknown {
			t.Fatalf("state = %v, want UNKNOWN", res.State)
		}
	})
}

func TestRunExecTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell semantics required")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	ex := newExec(Options{DefaultTimeout: 5 * time.Second})
	job := scheduler.Job{ObjectID: "exec-obj", Planned: time.Now()}

	// A sleep far longer than the 150ms timeout exercises the
	// context-cancel + SIGKILL-to-process-group path.
	e := execEntry([]string{"/bin/sh", "-c", "sleep 30"}, 150*time.Millisecond)
	start := time.Now()
	res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
	elapsed := time.Since(start)

	if !res.Timeout {
		t.Fatalf("Timeout flag = false, want true")
	}
	if res.State != model.StateUnknown {
		t.Fatalf("state = %v, want UNKNOWN on timeout", res.State)
	}
	if !strings.Contains(res.Output, "timed out") {
		t.Fatalf("output = %q, want timeout text", res.Output)
	}
	// Must return promptly after the deadline (well under the 30s sleep),
	// allowing for the 2s WaitDelay grace.
	if elapsed > 5*time.Second {
		t.Fatalf("runExec took %v, expected prompt timeout kill", elapsed)
	}
	if ex.Stats().Timeouts == 0 {
		t.Fatal("timeout counter not bumped")
	}
}

func TestRunExecStdoutCapped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell semantics required")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	ex := newExec(Options{DefaultTimeout: 5 * time.Second})
	job := scheduler.Job{ObjectID: "exec-obj", Planned: time.Now()}

	// Emit far more than MaxOutput so the cappedWriter must truncate; the
	// parsed result text still must not exceed the cap.
	script := "yes ABCDEFGHIJ | head -c 200000"
	e := execEntry([]string{"/bin/sh", "-c", script}, 5*time.Second)
	res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
	if len(res.Output)+len(res.LongOutput) > nagios.MaxOutput {
		t.Fatalf("captured %d bytes, exceeds MaxOutput %d",
			len(res.Output)+len(res.LongOutput), nagios.MaxOutput)
	}
}

// resolvePlugin is exercised via runExec for the allowlist-denied path too.
func TestRunExecPluginNotAllowed(t *testing.T) {
	ex := newExec(Options{PluginsDir: "/opt/plugins", PluginsAllow: []string{"check_http"}})
	job := scheduler.Job{ObjectID: "exec-obj", Planned: time.Now()}
	e := execEntry([]string{filepath.Join("/opt/plugins", "check_forbidden")}, 5*time.Second)
	res := drainResult(t, ex, func() { ex.runExec(context.Background(), job, e) })
	if res.State != model.StateUnknown {
		t.Fatalf("state = %v, want UNKNOWN for denied plugin", res.State)
	}
	if !strings.Contains(res.Output, "not allowed") {
		t.Fatalf("output = %q, want 'not allowed'", res.Output)
	}
}
