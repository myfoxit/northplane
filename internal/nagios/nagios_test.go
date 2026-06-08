package nagios

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

func TestParseOutputBasic(t *testing.T) {
	out := ParseOutput("DISK OK - free space: / 3326 MB (56%) | /=2643MB;5948;5958;0;5968")
	if out.Text != "DISK OK - free space: / 3326 MB (56%)" {
		t.Fatalf("text: %q", out.Text)
	}
	if len(out.Metrics) != 1 || out.Metrics[0].Label != "/" || out.Metrics[0].Value != 2643 {
		t.Fatalf("metrics: %+v", out.Metrics)
	}
	if out.Metrics[0].NormUnit != "bytes" || out.Metrics[0].NormValue != 2643*1024*1024 {
		t.Fatalf("normalization: %+v", out.Metrics[0])
	}
}

func TestParseOutputMultiline(t *testing.T) {
	raw := "DISK OK - free space: / 3326 MB (56%) | /=2643MB;5948;5958;0;5968\n" +
		"/ 15272 MB (77%);\n" +
		"/boot 68 MB (69%);\n" +
		"/var/log 819 MB (84%); | /boot=68MB;88;93;0;98\n" +
		"/home=69357MB;253404;253409;0;253414\n" +
		"/var/log=818MB;970;975;0;980"
	out := ParseOutput(raw)
	if !strings.Contains(out.LongText, "/boot 68 MB") {
		t.Fatalf("long text: %q", out.LongText)
	}
	if len(out.Metrics) != 4 {
		t.Fatalf("want 4 perfdata, got %d: %+v", len(out.Metrics), out.Metrics)
	}
}

func TestParseOutputLatin1(t *testing.T) {
	out := ParseOutput("WARNUNG - Gr\xf6\xdfe \xfcberschritten")
	if !strings.Contains(out.Text, "Größe überschritten") {
		t.Fatalf("latin1 fallback: %q", out.Text)
	}
}

func TestPerfdataGrammar(t *testing.T) {
	cases := []struct {
		in    string
		label string
		val   float64
		warn  string
	}{
		{"time=0.123456s;1.0;5.0;0.0", "time", 0.123456, "1.0"},
		{"'used space'=12GB;80%;90%", "used space", 12, "80%"},
		{"load1=0.5;5;10;0;", "load1", 0.5, "5"},
		{"pct=87%;@90:100;95", "pct", 87, "@90:100"},
		{"count=42c;;;0;1000", "count", 42, ""},
		{"neg=-3;~:5;10", "neg", -3, "~:5"},
	}
	for _, c := range cases {
		perfs, warns := ParsePerfdata(c.in)
		if len(warns) != 0 {
			t.Fatalf("%q: warns %v", c.in, warns)
		}
		if len(perfs) != 1 {
			t.Fatalf("%q: n=%d", c.in, len(perfs))
		}
		p := perfs[0]
		if p.Label != c.label || p.Value != c.val || p.Warn != c.warn {
			t.Fatalf("%q: got %+v", c.in, p)
		}
	}
	// broken tokens warn, never fail
	perfs, warns := ParsePerfdata("ok=1 broken=abc also_ok=2;3;4")
	if len(perfs) != 2 || len(warns) != 1 {
		t.Fatalf("fault tolerance: perfs=%d warns=%d", len(perfs), len(warns))
	}
}

func TestRangeSemantics(t *testing.T) {
	cases := []struct {
		spec     string
		value    float64
		violated bool
	}{
		{"10", 5, false}, {"10", 11, true}, {"10", -1, true}, // 0:10
		{"10:", 9, true}, {"10:", 10, false}, // 10:inf
		{"~:10", -99, false}, {"~:10", 11, true}, // -inf:10
		{"5:8", 6, false}, {"5:8", 9, true},
		{"@5:8", 6, true}, {"@5:8", 9, false}, // inverted
	}
	for _, c := range cases {
		r, err := ParseRange(c.spec)
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		if got := r.Violated(c.value); got != c.violated {
			t.Fatalf("%q with %v: violated=%v want %v", c.spec, c.value, got, c.violated)
		}
	}
	if _, err := ParseRange("8:5"); err == nil {
		t.Fatal("start>end must error")
	}
}

func FuzzParsePerfdata(f *testing.F) {
	f.Add("load1=0.5;5;10;0;100 load5=0.3")
	f.Add("'a b'=1MB;;;;")
	f.Add("x=@~:5;;")
	f.Add("=;;; '=' ''=''")
	f.Add("a=1e308TB;@~:;@:~")
	f.Fuzz(func(t *testing.T, in string) {
		perfs, _ := ParsePerfdata(in) // must never panic
		for _, p := range perfs {
			if p.Label == "" {
				t.Fatal("empty label escaped the parser")
			}
			if math.IsNaN(p.Value) {
				t.Fatal("NaN value escaped the parser")
			}
		}
	})
}

func TestMacroExpansion(t *testing.T) {
	host := &model.Object{Name: "db-prod-01", Kind: model.KindHost}
	hostSpec := &model.ObjectSpec{Address: "10.20.1.15", MaxCheckAttempts: 3,
		Vars: model.Vars{"ssh_port": "2222"}}
	hs := &model.CheckState{State: model.HostUp, StateType: model.StateHard, Attempt: 1,
		Output: "PING OK"}
	svc := &model.Object{Name: "postgres-connections", Kind: model.KindService}
	ss := &model.CheckState{State: model.StateCritical, StateType: model.StateHard, Attempt: 3,
		Output: "CRITICAL - 98 connections", Perfdata: "conns=98;80;95"}

	mc := &MacroContext{
		Host: host, HostSpec: hostSpec, HostState: hs,
		Service: svc, ServiceState: ss, ServiceSpec: &model.ObjectSpec{},
		Args: []string{"-w", "80%"},
		Now:  time.Unix(1750000000, 0).UTC(),
		Secrets: func(name string) (string, bool) {
			if name == "pgpass" {
				return "s3cret", true
			}
			return "", false
		},
	}

	got, unknown := mc.Expand("$HOSTNAME$/$HOSTADDRESS$ $SERVICEDESC$=$SERVICESTATE$ ($SERVICEATTEMPT$) port=$_HOSTSSH_PORT$ arg1=$ARG1$ pw=$SECRET:pgpass$ t=$TIMET$ lit=$$ unknown=$NOPE$")
	want := "db-prod-01/10.20.1.15 postgres-connections=CRITICAL (3) port=2222 arg1=-w pw=s3cret t=1750000000 lit=$ unknown=$NOPE$"
	if got != want {
		t.Fatalf("expand:\n got %q\nwant %q", got, want)
	}
	if len(unknown) != 1 || unknown[0] != "NOPE" {
		t.Fatalf("unknown: %v", unknown)
	}
	// unset args expand empty
	got2, _ := mc.Expand("[$ARG5$]")
	if got2 != "[]" {
		t.Fatalf("unset arg: %q", got2)
	}
}

func TestImporterNagiosCfg(t *testing.T) {
	dir := t.TempDir()
	cfg := `
define host {
    host_name        web01
    alias            Webserver 1
    address          192.168.1.10
    use              linux-server
    parents          gw01
    hostgroups       web-servers,linux-servers
    check_command    check-host-alive
    check_interval   5
    retry_interval   1
    max_check_attempts 3
    notification_period 24x7
    _RACK            R42
    obsess_over_host 1
}
define host {
    name             linux-server
    register         0
    check_interval   10
    max_check_attempts 5
}
define service {
    host_name            web01
    service_description  HTTP
    check_command        check_http!-w 5 -c 10!--ssl
    check_interval       2
    use                  generic-service
}
define command {
    command_name  check_http
    command_line  $USER1$/check_http -H $HOSTADDRESS$ $ARG1$ $ARG2$
}
define command {
    command_name  bad_shell
    command_line  /bin/sh -c "check_foo | grep OK"
}
define timeperiod {
    timeperiod_name  workhours
    alias            Work Hours
    monday           09:00-17:00
    tuesday          09:00-17:00
}
`
	if err := os.WriteFile(filepath.Join(dir, "objects.cfg"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Import(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Hosts != 1 || res.Stats.Services != 1 || res.Stats.Templates != 1 ||
		res.Stats.Commands != 1 || res.Stats.Periods != 1 {
		t.Fatalf("stats: %+v", res.Stats)
	}
	var hostDoc, svcDoc, tmplDoc bool
	for _, d := range res.Docs {
		switch {
		case d.Kind == "Host" && d.Metadata.Name == "web01":
			hostDoc = true
			if d.Spec["interval"] != "300s" || d.Spec["address"] != "192.168.1.10" {
				t.Fatalf("host spec: %+v", d.Spec)
			}
			vars := d.Spec["vars"].(map[string]any)
			if vars["rack"] != "R42" {
				t.Fatalf("custom var: %+v", vars)
			}
		case d.Kind == "Service" && d.Metadata.Name == "HTTP":
			svcDoc = true
			if d.Metadata.Host != "web01" {
				t.Fatalf("service host: %+v", d.Metadata)
			}
			args := d.Spec["args"].([]string)
			if len(args) != 2 || args[0] != "-w 5 -c 10" {
				t.Fatalf("service args: %+v", args)
			}
		case d.Kind == "Template" && d.Metadata.Name == "linux-server":
			tmplDoc = true
		}
	}
	if !hostDoc || !svcDoc || !tmplDoc {
		t.Fatalf("docs missing: host=%v svc=%v tmpl=%v", hostDoc, svcDoc, tmplDoc)
	}
	// deviations: obsess_over_host + bad_shell command
	foundObsess, foundShell := false, false
	for _, d := range res.Deviations {
		if d.Directive == "obsess_over_host" {
			foundObsess = true
		}
		if d.Object == "bad_shell" {
			foundShell = true
		}
	}
	if !foundObsess || !foundShell {
		t.Fatalf("deviations: %+v", res.Deviations)
	}
	// label hints from hostgroups
	if len(res.LabelHints) == 0 {
		t.Fatal("expected label hints for web-servers/linux-servers")
	}
	if !strings.Contains(res.RenderReport(), "Abweichungsbericht") {
		t.Fatal("report rendering")
	}
}

func TestImporterIcinga2(t *testing.T) {
	dir := t.TempDir()
	conf := `
object Host "app01" {
  import "generic-host"
  address = "10.1.2.3"
  vars.os = "Linux"
  check_command = "hostalive"
}
template Host "generic-host" {
  max_check_attempts = 3
}
apply Service "ssh" {
  check_command = "ssh"
  assign where host.vars.os == "Linux"
}
`
	if err := os.WriteFile(filepath.Join(dir, "hosts.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Import(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Hosts != 1 {
		t.Fatalf("stats: %+v", res.Stats)
	}
	var found bool
	for _, d := range res.Docs {
		if d.Kind == "Host" && d.Metadata.Name == "app01" {
			found = true
			if d.Spec["address"] != "10.1.2.3" {
				t.Fatalf("spec: %+v", d.Spec)
			}
			tmpls, _ := d.Spec["templates"].([]string)
			if len(tmpls) != 1 || tmpls[0] != "generic-host" {
				t.Fatalf("templates: %+v", d.Spec["templates"])
			}
		}
	}
	if !found {
		t.Fatal("app01 not imported")
	}
	// apply rule must be flagged
	var applyFlagged bool
	for _, d := range res.Deviations {
		if d.Directive == "apply" {
			applyFlagged = true
		}
	}
	if !applyFlagged {
		t.Fatalf("apply not flagged: %+v", res.Deviations)
	}
}
