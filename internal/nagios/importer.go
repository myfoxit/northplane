package nagios

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/northplane/northplane/internal/bundle"
)

// Importer (SPEC §8.7): parses nagios.cfg-dialect object configuration
// plus an Icinga2-DSL subset and emits a config bundle and a deviation
// report. Goal: 95 % of typical setups automatic, the rest documented.

// Deviation is one non-mappable finding.
type Deviation struct {
	File      string `json:"file"`
	Object    string `json:"object,omitempty"`
	Directive string `json:"directive"`
	Value     string `json:"value,omitempty"`
	Advice    string `json:"advice"`
}

// ImportResult bundles the outcome.
type ImportResult struct {
	Docs       []bundle.Doc `json:"-"`
	Deviations []Deviation  `json:"deviations"`
	Stats      ImportStats  `json:"stats"`
	LabelHints []string     `json:"labelHints"` // hostgroup → label suggestions
}

// ImportStats counts what was processed.
type ImportStats struct {
	Files     int `json:"files"`
	Hosts     int `json:"hosts"`
	Services  int `json:"services"`
	Templates int `json:"templates"`
	Commands  int `json:"commands"`
	Periods   int `json:"timePeriods"`
	Contacts  int `json:"contacts"`
	Groups    int `json:"groups"`
	Skipped   int `json:"skipped"`
}

// nagiosObject is one parsed define block.
type nagiosObject struct {
	typ   string
	file  string
	props map[string]string
	order []string
}

// Import walks path (a directory or a main cfg file) and converts.
func Import(path string) (*ImportResult, error) {
	files, err := collectCfgFiles(path)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{}
	var objects []*nagiosObject
	for _, f := range files {
		objs, devs, err := parseCfgFile(f)
		if err != nil {
			return nil, fmt.Errorf("import %s: %w", f, err)
		}
		objects = append(objects, objs...)
		res.Deviations = append(res.Deviations, devs...)
		res.Stats.Files++
	}
	// Icinga2 DSL files — walk the tree (filepath.Glob does NOT support
	// "**", so a glob would silently miss conf.d/hosts/*.conf layouts).
	var icingaFiles []string
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".conf") {
			icingaFiles = append(icingaFiles, p)
		}
		return nil
	})
	for _, f := range dedupe(icingaFiles) {
		objs, devs, err := parseIcinga2File(f)
		if err != nil {
			res.Deviations = append(res.Deviations, Deviation{
				File: f, Directive: "(parse)", Value: err.Error(),
				Advice: "Datei manuell prüfen — Icinga2-DSL nur als Teilmenge unterstützt"})
			continue
		}
		objects = append(objects, objs...)
		res.Deviations = append(res.Deviations, devs...)
		res.Stats.Files++
	}

	convert(objects, res)
	return res, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// collectCfgFiles follows nagios.cfg cfg_file/cfg_dir directives or
// globs a directory.
func collectCfgFiles(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		if strings.HasSuffix(path, "nagios.cfg") || strings.HasSuffix(path, "icinga.cfg") {
			return expandMainCfg(path)
		}
		return []string{path}, nil
	}
	// directory: prefer a main cfg, else take all .cfg files recursively
	for _, main := range []string{"nagios.cfg", "icinga.cfg"} {
		if _, err := os.Stat(filepath.Join(path, main)); err == nil {
			return expandMainCfg(filepath.Join(path, main))
		}
	}
	var files []string
	err = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".cfg") {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

func expandMainCfg(main string) ([]string, error) {
	f, err := os.Open(main)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	base := filepath.Dir(main)
	var files []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "cfg_file="); ok {
			files = append(files, absTo(base, strings.TrimSpace(v)))
		}
		if v, ok := strings.CutPrefix(line, "cfg_dir="); ok {
			dir := absTo(base, strings.TrimSpace(v))
			_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && strings.HasSuffix(p, ".cfg") {
					files = append(files, p)
				}
				return nil
			})
		}
	}
	return files, sc.Err()
}

func absTo(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// parseCfgFile parses define blocks.
func parseCfgFile(path string) ([]*nagiosObject, []Deviation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var objects []*nagiosObject
	var devs []Deviation
	var cur *nagiosObject
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		// strip comments (# and ; outside values is fine for cfg dialect)
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, " ;"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case cur == nil:
			if rest, ok := strings.CutPrefix(line, "define"); ok {
				typ := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "{"))
				cur = &nagiosObject{typ: typ, file: path, props: map[string]string{}}
			}
		case line == "}":
			objects = append(objects, cur)
			cur = nil
		default:
			line = strings.TrimSuffix(line, "}")
			fields := strings.Fields(line)
			if len(fields) >= 1 {
				key := fields[0]
				val := strings.TrimSpace(strings.TrimPrefix(line, key))
				if _, dup := cur.props[key]; !dup {
					cur.order = append(cur.order, key)
				}
				cur.props[key] = val
			}
			if strings.HasSuffix(strings.TrimSpace(sc.Text()), "}") && cur != nil {
				objects = append(objects, cur)
				cur = nil
			}
		}
	}
	if cur != nil {
		devs = append(devs, Deviation{File: path, Directive: "(syntax)",
			Advice: fmt.Sprintf("unterminated define block (line ~%d)", lineNo)})
	}
	return objects, devs, sc.Err()
}

// parseIcinga2File parses a pragmatic Icinga2-DSL subset:
// object/template blocks with scalar assignments and vars.*.
func parseIcinga2File(path string) ([]*nagiosObject, []Deviation, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var objects []*nagiosObject
	var devs []Deviation
	lines := strings.Split(string(raw), "\n")
	var cur *nagiosObject
	depth := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") {
			continue
		}
		if cur == nil {
			for _, kw := range []string{"object", "template", "apply"} {
				if rest, ok := strings.CutPrefix(t, kw+" "); ok {
					parts := strings.Fields(rest)
					if len(parts) >= 2 {
						name := strings.Trim(parts[1], `"`)
						typ := "icinga2:" + strings.ToLower(parts[0])
						cur = &nagiosObject{typ: typ, file: path, props: map[string]string{"__name": name, "__kw": kw}}
						depth = strings.Count(t, "{") - strings.Count(t, "}")
						if kw == "apply" {
							devs = append(devs, Deviation{File: path, Object: name,
								Directive: "apply", Value: rest,
								Advice: "Apply-Regeln → Northplane: Service-Template + Label-Selektor verwenden"})
						}
					}
					break
				}
			}
			continue
		}
		depth += strings.Count(t, "{") - strings.Count(t, "}")
		if depth <= 0 {
			objects = append(objects, cur)
			cur = nil
			continue
		}
		if rest, ok := strings.CutPrefix(t, "import "); ok && depth == 1 {
			tmpl := strings.Trim(rest, `"`)
			if cur.props["use"] == "" {
				cur.props["use"] = tmpl
				cur.order = append(cur.order, "use")
			} else {
				cur.props["use"] += "," + tmpl
			}
			continue
		}
		if i := strings.Index(t, "="); i > 0 && depth == 1 {
			key := strings.TrimSpace(t[:i])
			val := strings.Trim(strings.TrimSpace(t[i+1:]), `"`)
			if rest, ok := strings.CutPrefix(key, "vars."); ok {
				key = "_" + rest
			}
			if _, dup := cur.props[key]; !dup {
				cur.order = append(cur.order, key)
			}
			cur.props[key] = val
		}
	}
	return objects, devs, nil
}

// directive → advice for known-unmappable Nagios settings.
var unmappable = map[string]string{
	"obsess_over_service":  "OCSP-Pipeline entfällt — Webhook-Subscription auf state_change-Events verwenden",
	"obsess_over_services": "OCSP-Pipeline entfällt — Webhook-Subscription auf state_change-Events verwenden",
	"obsess_over_host":     "OCHP entfällt — Webhook-Subscription verwenden",
	"event_handler":        "Event-Handler → Webhook-Subscription oder AlertRule + Webhook-Kanal",
	"stalking_options":     "Stalking entfällt — Event-Log erfasst jede Änderung ohnehin",
	"failure_prediction_enabled": "obsolet (auch in Nagios)",
	"process_perf_data":    "Perfdata fließt immer in die NP-TSDB (kein Schalter nötig)",
	"retain_status_information":    "Zustand ist immer persistent (SQLite/PostgreSQL)",
	"retain_nonstatus_information": "Zustand ist immer persistent",
	"parallelize_check":    "obsolet — Scheduler parallelisiert immer",
	"is_volatile":          "volatile Services → AlertRule mit pendingFor=0 je Event",
	"low_flap_threshold":   "→ spec.flapThresholdLow",
	"high_flap_threshold":  "→ spec.flapThresholdHigh",
	"notification_options": "Filterung → EscalationPolicy/AlertRule-Severity",
	"first_notification_delay": "→ AlertRule.pendingFor",
}

// interval converts Nagios time units (interval_length=60) to seconds.
func nagiosInterval(v string) (string, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%ds", int(f*60)), true
}

func convert(objects []*nagiosObject, res *ImportResult) {
	// index hostgroup memberships for label suggestions
	hostgroups := map[string][]string{}

	for _, o := range objects {
		switch o.typ {
		case "host", "icinga2:host":
			doc, devs := convertHost(o)
			if isTemplate(o) {
				doc.Kind = "Template"
				doc.Spec["templateKind"] = "host"
				res.Stats.Templates++
			} else {
				res.Stats.Hosts++
			}
			res.Docs = append(res.Docs, doc)
			res.Deviations = append(res.Deviations, devs...)
			if hg := o.props["hostgroups"]; hg != "" {
				for _, g := range splitList(hg) {
					hostgroups[g] = append(hostgroups[g], doc.Metadata.Name)
				}
			}
		case "service", "icinga2:service":
			docs, devs := convertService(o)
			if isTemplate(o) {
				res.Stats.Templates += len(docs)
			} else {
				res.Stats.Services += len(docs)
			}
			res.Docs = append(res.Docs, docs...)
			res.Deviations = append(res.Deviations, devs...)
		case "command":
			doc, devs := convertCommand(o)
			if doc != nil {
				res.Docs = append(res.Docs, *doc)
				res.Stats.Commands++
			}
			res.Deviations = append(res.Deviations, devs...)
		case "timeperiod":
			res.Docs = append(res.Docs, convertTimePeriod(o))
			res.Stats.Periods++
		case "contact":
			doc := bundle.Doc{Kind: "Contact", Metadata: bundle.Metadata{Name: o.props["contact_name"]},
				Spec: map[string]any{"email": o.props["email"]}}
			if doc.Metadata.Name != "" {
				res.Docs = append(res.Docs, doc)
				res.Stats.Contacts++
			}
		case "contactgroup":
			doc := bundle.Doc{Kind: "ContactGroup", Metadata: bundle.Metadata{Name: o.props["contactgroup_name"]},
				Spec: map[string]any{"members": splitList(o.props["members"])}}
			if doc.Metadata.Name != "" {
				res.Docs = append(res.Docs, doc)
				res.Stats.Groups++
			}
		case "hostgroup":
			name := o.props["hostgroup_name"]
			if name != "" {
				for _, m := range splitList(o.props["members"]) {
					hostgroups[name] = append(hostgroups[name], m)
				}
			}
		case "servicegroup", "hostextinfo", "serviceextinfo":
			res.Stats.Skipped++
		case "hostdependency", "servicedependency":
			res.Deviations = append(res.Deviations, Deviation{File: o.file,
				Directive: o.typ,
				Advice:    "Dependencies → Host-parents (Reachability) bzw. BusinessService-Baum modellieren"})
		case "hostescalation", "serviceescalation":
			res.Deviations = append(res.Deviations, Deviation{File: o.file,
				Directive: o.typ,
				Advice:    "Eskalationen → EscalationPolicy (steps mit after/unlessAcked) abbilden"})
		case "icinga2:checkcommand":
			res.Stats.Skipped++
		default:
			res.Stats.Skipped++
		}
	}

	// hostgroups → static groups + label hints (SPEC §8.7)
	var groupNames []string
	for g := range hostgroups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)
	for _, g := range groupNames {
		members := dedupe(hostgroups[g])
		sort.Strings(members)
		res.Docs = append(res.Docs, bundle.Doc{
			Kind: "StaticGroup", Metadata: bundle.Metadata{Name: g},
			Spec: map[string]any{"members": members},
		})
		if label := groupLabelHint(g); label != "" {
			res.LabelHints = append(res.LabelHints,
				fmt.Sprintf("hostgroup %q → label %s (Review-Diff, %d Hosts)", g, label, len(members)))
		}
		res.Stats.Groups++
	}
}

func isTemplate(o *nagiosObject) bool {
	if o.props["register"] == "0" {
		return true
	}
	return strings.HasPrefix(o.typ, "icinga2:") && o.props["__kw"] == "template"
}

// groupLabelHint heuristically suggests labels for common group names.
func groupLabelHint(group string) string {
	g := strings.ToLower(group)
	switch {
	case strings.Contains(g, "linux"):
		return "os=linux"
	case strings.Contains(g, "windows"):
		return "os=windows"
	case strings.Contains(g, "prod"):
		return "env=prod"
	case strings.Contains(g, "test") || strings.Contains(g, "stag"):
		return "env=test"
	case strings.Contains(g, "db") || strings.Contains(g, "sql"):
		return "role=database"
	case strings.Contains(g, "web"):
		return "role=web"
	}
	return ""
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" && p != "*" {
			out = append(out, p)
		}
	}
	return out
}

func convertHost(o *nagiosObject) (bundle.Doc, []Deviation) {
	var devs []Deviation
	name := o.props["host_name"]
	if name == "" {
		name = o.props["name"] // template
	}
	if name == "" {
		name = o.props["__name"] // icinga2
	}
	doc := bundle.Doc{Kind: "Host", Metadata: bundle.Metadata{Name: name, Labels: map[string]string{}}, Spec: map[string]any{}}
	for _, key := range o.order {
		val := o.props[key]
		switch key {
		case "host_name", "name", "alias", "display_name", "register", "__name", "__kw":
		case "address":
			doc.Spec["address"] = val
		case "use":
			doc.Spec["templates"] = splitList(val)
		case "parents":
			doc.Spec["parents"] = splitList(val)
		case "check_command":
			cmd, args := splitCommandRef(val)
			doc.Spec["checkCommand"] = cmd
			if len(args) > 0 {
				doc.Spec["args"] = args
			}
		case "check_interval", "normal_check_interval":
			if v, ok := nagiosInterval(val); ok {
				doc.Spec["interval"] = v
			}
		case "retry_interval", "retry_check_interval":
			if v, ok := nagiosInterval(val); ok {
				doc.Spec["retryInterval"] = v
			}
		case "max_check_attempts":
			if n, err := strconv.Atoi(val); err == nil {
				doc.Spec["maxCheckAttempts"] = n
			}
		case "check_period":
			doc.Spec["checkPeriod"] = val
		case "notification_period":
			doc.Spec["notificationPeriod"] = val
		case "notifications_enabled":
			doc.Spec["enableNotifications"] = val == "1"
		case "active_checks_enabled":
			doc.Spec["enableChecks"] = val == "1"
		case "flap_detection_enabled":
			doc.Spec["enableFlapDetection"] = val == "1"
		case "hostgroups", "contact_groups", "contacts", "notification_interval",
			"notification_options", "icon_image", "icon_image_alt", "statusmap_image",
			"check_freshness", "freshness_threshold", "passive_checks_enabled":
			// group/contact wiring happens at alerting level; freshness below
			if key == "freshness_threshold" {
				if n, err := strconv.Atoi(val); err == nil && n > 0 {
					doc.Spec["stalenessAfter"] = fmt.Sprintf("%ds", n)
				}
			}
		default:
			if rest, ok := strings.CutPrefix(key, "_"); ok {
				vars, _ := doc.Spec["vars"].(map[string]any)
				if vars == nil {
					vars = map[string]any{}
					doc.Spec["vars"] = vars
				}
				vars[strings.ToLower(rest)] = val
				continue
			}
			advice, known := unmappable[key]
			if !known {
				advice = "Direktive ohne Northplane-Pendant — prüfen"
			}
			devs = append(devs, Deviation{File: o.file, Object: name, Directive: key, Value: val, Advice: advice})
		}
	}
	return doc, devs
}

func convertService(o *nagiosObject) ([]bundle.Doc, []Deviation) {
	var devs []Deviation
	desc := o.props["service_description"]
	if desc == "" {
		desc = o.props["name"]
	}
	if desc == "" {
		desc = o.props["__name"]
	}
	hosts := splitList(o.props["host_name"])
	if isTemplate(o) {
		hosts = nil
	}
	base := bundle.Doc{Kind: "Service", Metadata: bundle.Metadata{Name: desc, Labels: map[string]string{}}, Spec: map[string]any{}}
	if isTemplate(o) {
		base.Kind = "Template"
		base.Spec["templateKind"] = "service"
	}
	for _, key := range o.order {
		val := o.props[key]
		switch key {
		case "service_description", "name", "host_name", "register", "alias", "display_name", "__name", "__kw":
		case "use":
			base.Spec["templates"] = splitList(val)
		case "check_command":
			cmd, args := splitCommandRef(val)
			base.Spec["checkCommand"] = cmd
			if len(args) > 0 {
				base.Spec["args"] = args
			}
		case "check_interval", "normal_check_interval":
			if v, ok := nagiosInterval(val); ok {
				base.Spec["interval"] = v
			}
		case "retry_interval", "retry_check_interval":
			if v, ok := nagiosInterval(val); ok {
				base.Spec["retryInterval"] = v
			}
		case "max_check_attempts":
			if n, err := strconv.Atoi(val); err == nil {
				base.Spec["maxCheckAttempts"] = n
			}
		case "check_period":
			base.Spec["checkPeriod"] = val
		case "notification_period":
			base.Spec["notificationPeriod"] = val
		case "notifications_enabled":
			base.Spec["enableNotifications"] = val == "1"
		case "active_checks_enabled":
			base.Spec["enableChecks"] = val == "1"
		case "passive_checks_enabled":
			if val == "1" && o.props["active_checks_enabled"] == "0" {
				base.Spec["checkCommand"] = "passive"
			}
		case "check_freshness":
		case "freshness_threshold":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				base.Spec["stalenessAfter"] = fmt.Sprintf("%ds", n)
			}
		case "hostgroup_name":
			devs = append(devs, Deviation{File: o.file, Object: desc, Directive: key, Value: val,
				Advice: "Service auf Hostgruppe → Template + Label-Selektor oder je Host instanziieren (Importer legt je Mitglied an, wenn Gruppe bekannt)"})
		case "contact_groups", "contacts", "notification_interval", "notification_options",
			"flap_detection_enabled", "process_perf_data", "servicegroups":
			if key == "flap_detection_enabled" {
				base.Spec["enableFlapDetection"] = val == "1"
			}
		default:
			if rest, ok := strings.CutPrefix(key, "_"); ok {
				vars, _ := base.Spec["vars"].(map[string]any)
				if vars == nil {
					vars = map[string]any{}
					base.Spec["vars"] = vars
				}
				vars[strings.ToLower(rest)] = val
				continue
			}
			advice, known := unmappable[key]
			if !known {
				advice = "Direktive ohne Northplane-Pendant — prüfen"
			}
			devs = append(devs, Deviation{File: o.file, Object: desc, Directive: key, Value: val, Advice: advice})
		}
	}
	if base.Kind == "Template" || len(hosts) == 0 {
		if base.Kind != "Template" {
			devs = append(devs, Deviation{File: o.file, Object: desc, Directive: "host_name",
				Advice: "Service ohne host_name — als Template importiert"})
			base.Kind = "Template"
			base.Spec["templateKind"] = "service"
		}
		return []bundle.Doc{base}, devs
	}
	docs := make([]bundle.Doc, 0, len(hosts))
	for _, h := range hosts {
		d := base
		d.Metadata.Host = h
		// spec map is shared by value-copy of map header — clone
		spec := make(map[string]any, len(base.Spec))
		for k, v := range base.Spec {
			spec[k] = v
		}
		d.Spec = spec
		docs = append(docs, d)
	}
	return docs, devs
}

// splitCommandRef splits Nagios "cmd!arg1!arg2" into reference + args.
func splitCommandRef(v string) (string, []string) {
	parts := strings.Split(v, "!")
	return parts[0], parts[1:]
}

// convertCommand maps a command definition; shell metacharacters make
// the command unsafe for argv execution and yield a deviation.
func convertCommand(o *nagiosObject) (*bundle.Doc, []Deviation) {
	name := o.props["command_name"]
	line := o.props["command_line"]
	if name == "" || line == "" {
		return nil, nil
	}
	var devs []Deviation
	if strings.ContainsAny(line, "|&;<>`") || strings.Contains(line, "$(") {
		devs = append(devs, Deviation{File: o.file, Object: name, Directive: "command_line", Value: line,
			Advice: "Shell-Konstrukte werden nicht emuliert (argv-Array, §13.1) — Wrapper-Skript anlegen"})
		return nil, devs
	}
	argv := strings.Fields(line)
	doc := &bundle.Doc{Kind: "CheckCommand", Metadata: bundle.Metadata{Name: name},
		Spec: map[string]any{"type": "exec", "line": argv}}
	return doc, devs
}

func convertTimePeriod(o *nagiosObject) bundle.Doc {
	days := map[string]any{}
	for _, d := range []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"} {
		if v := o.props[d]; v != "" {
			days[d] = splitList(v)
		}
	}
	return bundle.Doc{Kind: "TimePeriod",
		Metadata: bundle.Metadata{Name: o.props["timeperiod_name"]},
		Spec:     map[string]any{"alias": o.props["alias"], "days": days}}
}

// RenderReport renders the deviation report as text (CLI output).
func (r *ImportResult) RenderReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Import: %d Dateien — %d Hosts, %d Services, %d Templates, %d Commands, %d TimePeriods, %d Contacts, %d Gruppen (%d übersprungen)\n",
		r.Stats.Files, r.Stats.Hosts, r.Stats.Services, r.Stats.Templates,
		r.Stats.Commands, r.Stats.Periods, r.Stats.Contacts, r.Stats.Groups, r.Stats.Skipped)
	if len(r.LabelHints) > 0 {
		b.WriteString("\nLabel-Vorschläge (heuristisch, als Review-Diff):\n")
		for _, h := range r.LabelHints {
			b.WriteString("  • " + h + "\n")
		}
	}
	if len(r.Deviations) > 0 {
		fmt.Fprintf(&b, "\nAbweichungsbericht (%d Einträge):\n", len(r.Deviations))
		for _, d := range r.Deviations {
			obj := d.Object
			if obj != "" {
				obj = " [" + obj + "]"
			}
			fmt.Fprintf(&b, "  • %s%s: %s — %s\n", d.Directive, obj, d.Value, d.Advice)
		}
	} else {
		b.WriteString("\nKeine Abweichungen — vollständig mappbar.\n")
	}
	return b.String()
}
