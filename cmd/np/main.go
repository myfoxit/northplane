// np is the Northplane CLI (SPEC §7.1/§11.6): a thin client of the
// public API — everything np does, any API client can do (P1).
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

var version = "1.0.0-dev"

type cli struct {
	server   string
	token    string
	insecure bool
	jsonOut  bool
	client   *http.Client
}

func main() {
	c := &cli{
		server: envOr("NP_SERVER", "https://localhost:8443"),
		token:  os.Getenv("NP_TOKEN"),
	}
	args := os.Args[1:]
	// global flags before the command — accept both "--flag value" and
	// "--flag=value" (the latter is a common scripting idiom that would
	// otherwise be silently dropped, making the command a no-op with exit 0).
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		flag, val, hasVal := strings.Cut(args[0], "=")
		take := func() (string, bool) { // resolve "=value" or next arg
			if hasVal {
				return val, true
			}
			if len(args) > 1 {
				args = args[1:]
				return args[0], true
			}
			return "", false
		}
		switch flag {
		case "--json":
			c.jsonOut = true
		case "--insecure":
			c.insecure = true
		case "--server":
			if v, ok := take(); ok {
				c.server = v
			} else {
				fatalf("--server requires a value")
			}
		case "--token":
			if v, ok := take(); ok {
				c.token = v
			} else {
				fatalf("--token requires a value")
			}
		case "--version":
			fmt.Println("np", version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", flag)
			usage()
			os.Exit(2)
		}
		args = args[1:]
	}
	c.server = strings.TrimSuffix(c.server, "/")
	c.client = &http.Client{Timeout: 60 * time.Second}
	if c.insecure {
		c.client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	if len(args) == 0 {
		usage()
		return
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "get":
		err = c.get(rest)
	case "describe":
		err = c.describe(rest)
	case "apply":
		err = c.apply(rest)
	case "export":
		err = c.export(rest)
	case "ack":
		err = c.ack(rest)
	case "resolve":
		err = c.resolveAlert(rest)
	case "downtime":
		err = c.downtime(rest)
	case "silence":
		err = c.silence(rest)
	case "check-now":
		err = c.checkNow(rest)
	case "oncall":
		err = c.oncall(rest)
	case "audit":
		err = c.audit(rest)
	case "doctor":
		err = c.doctor(rest)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "np: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "np: %v\n", err)
		os.Exit(1)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "np: "+format+"\n", args...)
	os.Exit(2)
}

func usage() {
	fmt.Print(`np — Northplane CLI (` + version + `)

Usage: np [--server URL] [--token np_…] [--json] [--insecure] <command>

Commands:
  get hosts|services|problems|alerts|incidents|silences|downtimes|events
  describe <object-id|host-name>           object detail + effective config
  apply -f bundle.yaml [--prune] [--dry-run]
  export [> bundle.yaml]                   canonical config bundle
  ack <alert-id> [-m comment]              acknowledge (stops escalation)
  resolve <alert-id>
  downtime --selector 'k=v'|--object <id> --hours 2 -m comment
  silence  --selector 'k=v' --hours 2 -m comment
  check-now <object-id>
  oncall                                   who is on call
  audit verify | audit tail
  doctor                                   server health summary

Environment: NP_SERVER, NP_TOKEN
`)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// --- HTTP helpers ---

func (c *cli) do(method, path string, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequest(method, c.server+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		var prob struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
			Code   string `json:"code"`
		}
		if json.Unmarshal(data, &prob) == nil && prob.Title != "" {
			return nil, fmt.Errorf("%s (%s) %s", prob.Title, prob.Code, prob.Detail)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, firstLine(string(data)))
	}
	return data, nil
}

func (c *cli) getJSON(path string, dst any) error {
	data, err := c.do(http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	if c.jsonOut {
		os.Stdout.Write(pretty(data))
		return errPrinted
	}
	return json.Unmarshal(data, dst)
}

var errPrinted = fmt.Errorf("")

func handled(err error) error {
	if err == errPrinted {
		return nil
	}
	return err
}

func pretty(data []byte) []byte {
	var buf bytes.Buffer
	if json.Indent(&buf, data, "", "  ") == nil {
		buf.WriteByte('\n')
		return buf.Bytes()
	}
	return data
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// --- commands ---

type objView struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	HostName string            `json:"hostName"`
	Folder   string            `json:"folder"`
	Labels   map[string]string `json:"labels"`
	State    *struct {
		State         int    `json:"state"`
		StateType     string `json:"stateType"`
		Output        string `json:"output"`
		AckedBy       string `json:"ackedBy"`
		DowntimeDepth int    `json:"downtimeDepth"`
	} `json:"state"`
}

var svcStates = []string{"OK", "WARN", "CRIT", "UNKN"}
var hostStates = []string{"UP", "DOWN", "UNREACH", "UNKN"}

func stateLabel(kind string, s int) string {
	if s < 0 || s > 3 {
		return "?"
	}
	if kind == "host" {
		return hostStates[s]
	}
	return svcStates[s]
}

func (c *cli) get(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: np get <resource>")
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	defer w.Flush()
	switch args[0] {
	case "hosts", "services":
		var resp struct{ Items []objView }
		if err := c.getJSON("/api/v1/"+args[0]+"?limit=500", &resp); err != nil {
			return handled(err)
		}
		fmt.Fprintln(w, "STATE\tNAME\tHOST\tLABELS")
		for _, o := range resp.Items {
			state := "PEND"
			flags := ""
			if o.State != nil {
				state = stateLabel(o.Kind, o.State.State)
				if o.State.AckedBy != "" {
					flags += " (ack)"
				}
				if o.State.DowntimeDepth > 0 {
					flags += " (downtime)"
				}
			}
			labels := labelStr(o.Labels)
			fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\n", state, flags, o.Name, o.HostName, labels)
		}
	case "problems":
		var resp struct {
			Items []struct {
				Object objView `json:"object"`
				State  struct {
					State  int    `json:"state"`
					Output string `json:"output"`
				} `json:"state"`
			}
		}
		if err := c.getJSON("/api/v1/problems", &resp); err != nil {
			return handled(err)
		}
		fmt.Fprintln(w, "STATE\tOBJECT\tOUTPUT")
		for _, p := range resp.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\n", stateLabel(p.Object.Kind, p.State.State),
				p.Object.Name, truncate(p.State.Output, 80))
		}
	case "alerts":
		var resp struct {
			Items []struct {
				ID       string `json:"id"`
				Status   string `json:"status"`
				Severity string `json:"severity"`
				Title    string `json:"title"`
				OpenedAt string `json:"openedAt"`
			}
		}
		if err := c.getJSON("/api/v1/alerts?status=open,acked", &resp); err != nil {
			return handled(err)
		}
		fmt.Fprintln(w, "SEVERITY\tSTATUS\tTITLE\tID")
		for _, a := range resp.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", strings.ToUpper(a.Severity), a.Status,
				truncate(a.Title, 60), a.ID)
		}
	case "incidents":
		var resp struct {
			Items []struct {
				ID, Status, Severity, Title string
			}
		}
		if err := c.getJSON("/api/v1/incidents?open=true", &resp); err != nil {
			return handled(err)
		}
		fmt.Fprintln(w, "SEVERITY\tSTATUS\tTITLE\tID")
		for _, i := range resp.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", i.Severity, i.Status, truncate(i.Title, 60), i.ID)
		}
	case "silences", "downtimes":
		data, err := c.do(http.MethodGet, "/api/v1/"+args[0]+"?active=true", nil, "")
		if err != nil {
			return err
		}
		os.Stdout.Write(pretty(data))
	case "events":
		var resp struct {
			Items []struct {
				TS       string          `json:"ts"`
				Type     string          `json:"type"`
				Severity string          `json:"severity"`
				Payload  json.RawMessage `json:"payload"`
			}
		}
		if err := c.getJSON("/api/v1/events?limit=50", &resp); err != nil {
			return handled(err)
		}
		fmt.Fprintln(w, "TIME\tTYPE\tSEVERITY\tPAYLOAD")
		for _, e := range resp.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.TS, e.Type, e.Severity,
				truncate(string(e.Payload), 90))
		}
	default:
		return fmt.Errorf("unknown resource %q", args[0])
	}
	return nil
}

func labelStr(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (c *cli) describe(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: np describe <object-id>")
	}
	data, err := c.do(http.MethodGet, "/api/v1/objects/"+args[0], nil, "")
	if err != nil {
		return err
	}
	os.Stdout.Write(pretty(data))
	eff, err := c.do(http.MethodGet, "/api/v1/objects/"+args[0]+"/effective-config", nil, "")
	if err == nil {
		fmt.Println("--- effective config (templates resolved) ---")
		os.Stdout.Write(pretty(eff))
	}
	return nil
}

func (c *cli) apply(args []string) error {
	var file string
	prune, dry := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--file":
			if i+1 < len(args) {
				file = args[i+1]
				i++
			}
		case "--prune":
			prune = true
		case "--dry-run":
			dry = true
		}
	}
	if file == "" {
		return fmt.Errorf("usage: np apply -f bundle.yaml [--prune] [--dry-run]")
	}
	var body io.Reader
	if file == "-" {
		body = os.Stdin
	} else {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		body = f
	}
	path := "/api/v1/config/bundles:apply?"
	if dry {
		path += "dryRun=true&"
	}
	if prune {
		path += "prune=true&"
	}
	data, err := c.do(http.MethodPost, strings.TrimSuffix(path, "&"), body, "application/yaml")
	if err != nil {
		return err
	}
	var result struct {
		Plan []struct {
			Action, Kind, Name, Host string
		} `json:"plan"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &result); err != nil || c.jsonOut {
		os.Stdout.Write(pretty(data))
		return nil
	}
	verb := "applied"
	if dry {
		verb = "would apply"
	}
	if len(result.Plan) == 0 {
		fmt.Println("no changes")
	}
	for _, p := range result.Plan {
		name := p.Name
		if p.Host != "" {
			name = p.Host + "/" + p.Name
		}
		fmt.Printf("%s %s %s/%s\n", verb, p.Action, p.Kind, name)
	}
	for _, warning := range result.Warnings {
		fmt.Println("warning:", warning)
	}
	return nil
}

func (c *cli) export(args []string) error {
	data, err := c.do(http.MethodGet, "/api/v1/config/bundles:export", nil, "")
	if err != nil {
		return err
	}
	os.Stdout.Write(data)
	return nil
}

func (c *cli) ack(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: np ack <alert-id> [-m comment]")
	}
	comment := flagValue(args[1:], "-m")
	body, _ := json.Marshal(map[string]string{"comment": comment})
	data, err := c.do(http.MethodPost, "/api/v1/alerts/"+args[0]+":ack",
		bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	var alert struct{ Title string }
	_ = json.Unmarshal(data, &alert)
	fmt.Printf("acknowledged: %s\n", alert.Title)
	return nil
}

func (c *cli) resolveAlert(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: np resolve <alert-id>")
	}
	_, err := c.do(http.MethodPost, "/api/v1/alerts/"+args[0]+":resolve", nil, "")
	if err == nil {
		fmt.Println("resolved")
	}
	return err
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func (c *cli) downtime(args []string) error {
	sel := flagValue(args, "--selector")
	obj := flagValue(args, "--object")
	hours := flagValue(args, "--hours")
	comment := flagValue(args, "-m")
	if (sel == "" && obj == "") || comment == "" {
		return fmt.Errorf("usage: np downtime --selector 'k=v'|--object <id> --hours 2 -m comment")
	}
	h := 2.0
	if hours != "" {
		fmt.Sscanf(hours, "%f", &h)
	}
	start := time.Now().UTC()
	payload, _ := json.Marshal(map[string]any{
		"objectId": obj, "selector": sel, "type": "fixed",
		"start": start, "end": start.Add(time.Duration(h * float64(time.Hour))),
		"comment": comment,
	})
	data, err := c.do(http.MethodPost, "/api/v1/downtimes",
		bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	var d struct{ ID string }
	_ = json.Unmarshal(data, &d)
	fmt.Printf("downtime %s until %s\n", d.ID, start.Add(time.Duration(h*float64(time.Hour))).Format(time.RFC3339))
	return nil
}

func (c *cli) silence(args []string) error {
	sel := flagValue(args, "--selector")
	hours := flagValue(args, "--hours")
	comment := flagValue(args, "-m")
	if sel == "" || comment == "" {
		return fmt.Errorf("usage: np silence --selector 'k=v' --hours 2 -m comment")
	}
	h := 2.0
	if hours != "" {
		fmt.Sscanf(hours, "%f", &h)
	}
	payload, _ := json.Marshal(map[string]any{
		"selector": sel, "comment": comment,
		"expiresAt": time.Now().UTC().Add(time.Duration(h * float64(time.Hour))),
	})
	data, err := c.do(http.MethodPost, "/api/v1/silences",
		bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	var s struct{ ID string }
	_ = json.Unmarshal(data, &s)
	fmt.Printf("silence %s for %gh\n", s.ID, h)
	return nil
}

func (c *cli) checkNow(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: np check-now <object-id>")
	}
	_, err := c.do(http.MethodPost, "/api/v1/objects/"+args[0]+"/check-now", nil, "")
	if err == nil {
		fmt.Println("recheck queued")
	}
	return err
}

func (c *cli) oncall(args []string) error {
	var resp []struct {
		Schedule string `json:"schedule"`
		Contacts []struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Phone string `json:"phone"`
		} `json:"contacts"`
	}
	if err := c.getJSON("/api/v1/oncall/now", &resp); err != nil {
		return handled(err)
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "SCHEDULE\tON CALL\tCONTACT")
	for _, s := range resp {
		for _, contact := range s.Contacts {
			reach := contact.Email
			if contact.Phone != "" {
				reach += " / " + contact.Phone
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", s.Schedule, contact.Name, reach)
		}
	}
	return nil
}

func (c *cli) audit(args []string) error {
	if len(args) > 0 && args[0] == "verify" {
		data, err := c.do(http.MethodPost, "/api/v1/audit:verify", nil, "")
		if err != nil {
			return err
		}
		var res struct {
			Intact   bool   `json:"intact"`
			Verified int64  `json:"verified"`
			Error    string `json:"error"`
		}
		_ = json.Unmarshal(data, &res)
		if res.Intact {
			fmt.Printf("audit chain intact (%d entries verified)\n", res.Verified)
			return nil
		}
		return fmt.Errorf("AUDIT CHAIN BROKEN after %d entries: %s", res.Verified, res.Error)
	}
	var resp struct {
		Items []struct {
			TS        string `json:"ts"`
			ActorType string `json:"actorType"`
			ActorID   string `json:"actorId"`
			Action    string `json:"action"`
			Resource  string `json:"resource"`
		}
	}
	if err := c.getJSON("/api/v1/audit?limit=30", &resp); err != nil {
		return handled(err)
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "TIME\tACTOR\tACTION\tRESOURCE")
	for _, e := range resp.Items {
		fmt.Fprintf(w, "%s\t%s:%s\t%s\t%s\n", e.TS, e.ActorType,
			truncate(e.ActorID, 12), e.Action, truncate(e.Resource, 36))
	}
	return nil
}

func (c *cli) doctor(args []string) error {
	info, err := c.do(http.MethodGet, "/api/v1/system/info", nil, "")
	if err != nil {
		return fmt.Errorf("server unreachable: %w", err)
	}
	health, err := c.do(http.MethodGet, "/api/v1/system/health", nil, "")
	if err != nil {
		return err
	}
	fmt.Println("--- system/info ---")
	os.Stdout.Write(pretty(info))
	fmt.Println("--- system/health ---")
	os.Stdout.Write(pretty(health))
	return nil
}
