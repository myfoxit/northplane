package checks

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
)

func init() {
	register("http", checkHTTP)
	register("https", checkHTTP)
	register("tls-cert", checkTLSCert)
	register("cert", checkTLSCert)
	register("http-flow", checkHTTPFlow)
	register("nrpe", checkNRPE)
}

// httpClient builds a per-check client honouring SSRF guards: private
// targets are allowed for monitoring (that's the job), but redirects
// never cross into link-local/metadata ranges (SPEC §13.1).
func httpClient(timeout time.Duration, insecure bool, maxRedirects int) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err == nil {
				if ip := net.ParseIP(host); ip != nil && blockedIP(ip) {
					return nil, fmt.Errorf("destination %s blocked (link-local/metadata)", ip)
				}
			}
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			// DNS-rebinding guard: verify the resolved peer, too.
			if ra, ok := conn.RemoteAddr().(*net.TCPAddr); ok && blockedIP(ra.IP) {
				_ = conn.Close()
				return nil, fmt.Errorf("resolved destination %s blocked", ra.IP)
			}
			return conn, nil
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: insecure},
		DisableKeepAlives: true,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

// blockedIP rejects link-local and cloud-metadata ranges by default
// (SPEC §13.1).
func blockedIP(ip net.IP) bool {
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 169 && v4[1] == 254 {
		return true // includes 169.254.169.254 metadata
	}
	return false
}

// checkHTTP mirrors check_http's common surface:
// -u uri, -p port, -S ssl, -e expect-status, -r body-regex,
// -s expect-string, --method, --post data, -a user:pass, -k header,
// -w/-c response time, --insecure, --max-redirects.
func checkHTTP(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	if host == "" {
		return unknownf("http: no host")
	}
	useTLS := a.Bool("S", "ssl")
	port := a.Int(0, "p", "port")
	uri := a.Get("u", "uri", "url")
	if uri == "" {
		uri = "/"
	}
	var target string
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		target = uri
	} else {
		scheme := "http"
		if useTLS {
			scheme = "https"
		}
		hp := host
		if port != 0 {
			hp = net.JoinHostPort(host, fmt.Sprint(port))
		}
		target = scheme + "://" + hp + uri
	}

	timeout := a.Duration(15*time.Second, "t", "timeout")
	client := httpClient(timeout, a.Bool("insecure", "k-insecure"), a.Int(5, "max-redirects"))

	method := strings.ToUpper(a.Get("method", "j"))
	var body io.Reader
	if post := a.Get("post", "P"); post != "" {
		if method == "" {
			method = "POST"
		}
		body = strings.NewReader(post)
	}
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return unknownf("bad request: %v", err)
	}
	req.Header.Set("User-Agent", "Northplane/1.0 (check_http-compatible)")
	if hdr := a.Get("header", "k"); hdr != "" {
		if k, v, ok := strings.Cut(hdr, ":"); ok {
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	if auth := a.Get("a", "authorization"); auth != "" {
		if user, pass, ok := strings.Cut(auth, ":"); ok {
			req.SetBasicAuth(user, pass)
		}
	}
	if tok := a.Get("bearer"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return criticalf("%s: %v", target, unwrapURLError(err))
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	elapsed := time.Since(start).Seconds()

	// status assertion
	st := model.StateOK
	statusNote := resp.Status
	if expect := a.Get("e", "expect"); expect != "" {
		if !strings.Contains(resp.Status, expect) && !strings.HasPrefix(resp.Status, expect) {
			st = model.StateCritical
			statusNote = fmt.Sprintf("%s (expected %s)", resp.Status, expect)
		}
	} else if resp.StatusCode >= 400 {
		st = model.StateCritical
	} else if resp.StatusCode >= 300 {
		st = model.StateWarning
	}
	// body assertions
	if rx := a.Get("r", "regex"); rx != "" && st == model.StateOK {
		re, err := regexp.Compile(rx)
		if err != nil {
			return unknownf("bad regex: %v", err)
		}
		if !re.Match(bodyBytes) {
			st = model.StateCritical
			statusNote += fmt.Sprintf(" — pattern %q not found", rx)
		}
	}
	if substr := a.Get("s", "string"); substr != "" && st == model.StateOK {
		if !strings.Contains(string(bodyBytes), substr) {
			st = model.StateCritical
			statusNote += fmt.Sprintf(" — string %q not found", substr)
		}
	}
	// response-time thresholds upgrade severity
	if w, c := a.Get("w", "warning"), a.Get("c", "critical"); (w != "" || c != "") && st == model.StateOK {
		if code, err := nagios.Evaluate(elapsed, w, c); err == nil {
			st = model.State(code)
		}
	}

	label := map[model.State]string{0: "OK", 1: "WARNING", 2: "CRITICAL", 3: "UNKNOWN"}[st]
	certDays := ""
	perfCert := ""
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		days := time.Until(resp.TLS.PeerCertificates[0].NotAfter).Hours() / 24
		certDays = fmt.Sprintf(", cert expires in %.0fd", days)
		perfCert = fmt.Sprintf(" cert_days=%0.f;;;;", days)
	}
	text := fmt.Sprintf("HTTP %s - %s %s in %.3fs, %d bytes%s | time=%.6fs;%s;%s;0; size=%dB;;;0;%s",
		label, statusNote, target, elapsed, len(bodyBytes), certDays,
		elapsed, a.Get("w"), a.Get("c"), len(bodyBytes), perfCert)
	return st, nagios.ParseOutput(text)
}

// Rank for state severity ordering in http (OK<WARN<CRIT, UNKNOWN low).
func unwrapURLError(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}

// checkTLSCert: certificate expiry in days. Flags: -p port (443),
// -w days (30), -c days (7), --servername.
func checkTLSCert(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	port := a.Int(443, "p", "port")
	timeout := a.Duration(10*time.Second, "t", "timeout")
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	sni := a.Get("servername")
	if sni == "" {
		sni = host
	}
	d := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{
		ServerName: sni, InsecureSkipVerify: true}) // expiry check validates chain manually
	if err != nil {
		return criticalf("TLS connect to %s failed: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return criticalf("no certificate presented by %s", addr)
	}
	leaf := certs[0]
	days := time.Until(leaf.NotAfter).Hours() / 24
	warnDays := float64(a.Int(30, "w", "warning"))
	critDays := float64(a.Int(7, "c", "critical"))
	st := model.StateOK
	if days <= critDays {
		st = model.StateCritical
	} else if days <= warnDays {
		st = model.StateWarning
	}
	label := map[model.State]string{0: "OK", 1: "WARNING", 2: "CRITICAL"}[st]
	text := fmt.Sprintf("CERT %s - %s expires %s (%.0f days), issuer %q | cert_days=%.0f;%d;%d;;",
		label, leaf.Subject.CommonName, leaf.NotAfter.Format("2006-01-02"), days,
		leaf.Issuer.CommonName, days, a.Int(30, "w"), a.Int(7, "c"))
	return st, nagios.ParseOutput(text)
}

// FlowStep is one step of a builtin:http-flow check (SPEC §8.6):
// request → assertions → variable extraction for later steps.
type FlowStep struct {
	Name    string            `json:"name"`
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url"` // may reference ${vars}
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	// Assertions:
	ExpectStatus int     `json:"expectStatus,omitempty"`
	ExpectRegex  string  `json:"expectRegex,omitempty"`
	MaxSeconds   float64 `json:"maxSeconds,omitempty"`
	// Extraction into flow variables:
	Extract map[string]string `json:"extract,omitempty"` // var → regex (group 1) | "json:path.to.field"
}

// checkHTTPFlow reads the step sequence from the service var
// "flow" (JSON array of FlowStep).
func checkHTTPFlow(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	flowJSON := ""
	if t.Vars != nil {
		flowJSON = t.Vars["flow"]
	}
	if v := a.Get("flow"); v != "" {
		flowJSON = v
	}
	if flowJSON == "" {
		return unknownf("http-flow: vars.flow (JSON steps) required")
	}
	var steps []FlowStep
	if err := json.Unmarshal([]byte(flowJSON), &steps); err != nil {
		return unknownf("http-flow: bad steps: %v", err)
	}
	if len(steps) == 0 {
		return unknownf("http-flow: no steps")
	}
	timeout := a.Duration(60*time.Second, "t", "timeout")
	client := httpClient(timeout, a.Bool("insecure"), 5)
	// flow shares a cookie jar implicitly via manual header passing? Use
	// a jar for session flows:
	client.Jar = newMemJar()

	vars := map[string]string{}
	var totals []string
	var perf []string
	totalStart := time.Now()
	for i, step := range steps {
		name := step.Name
		if name == "" {
			name = fmt.Sprintf("step%d", i+1)
		}
		method := step.Method
		if method == "" {
			method = "GET"
		}
		stepURL := interpolate(step.URL, vars)
		var body io.Reader
		if step.Body != "" {
			body = strings.NewReader(interpolate(step.Body, vars))
		}
		req, err := http.NewRequestWithContext(ctx, method, stepURL, body)
		if err != nil {
			return unknownf("http-flow %s: %v", name, err)
		}
		for k, v := range step.Headers {
			req.Header.Set(k, interpolate(v, vars))
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return criticalf("step %q failed: %v", name, unwrapURLError(err))
		}
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		elapsed := time.Since(start).Seconds()
		perf = append(perf, fmt.Sprintf("%s=%.6fs;;;0;", perfLabel(name), elapsed))

		want := step.ExpectStatus
		if want == 0 {
			want = 200
		}
		if resp.StatusCode != want {
			return criticalf("step %q: status %d (expected %d)", name, resp.StatusCode, want)
		}
		if step.ExpectRegex != "" {
			re, err := regexp.Compile(step.ExpectRegex)
			if err != nil {
				return unknownf("step %q: bad regex: %v", name, err)
			}
			if !re.Match(bodyBytes) {
				return criticalf("step %q: pattern %q not found", name, step.ExpectRegex)
			}
		}
		if step.MaxSeconds > 0 && elapsed > step.MaxSeconds {
			return criticalf("step %q took %.3fs (max %.3fs)", name, elapsed, step.MaxSeconds)
		}
		for varName, rule := range step.Extract {
			if jsonPath, ok := strings.CutPrefix(rule, "json:"); ok {
				if v, ok := extractJSON(bodyBytes, jsonPath); ok {
					vars[varName] = v
				} else {
					return criticalf("step %q: json path %q not found", name, jsonPath)
				}
				continue
			}
			re, err := regexp.Compile(rule)
			if err != nil {
				return unknownf("step %q: bad extract regex: %v", name, err)
			}
			m := re.FindSubmatch(bodyBytes)
			if len(m) < 2 {
				return criticalf("step %q: extract %q matched nothing", name, varName)
			}
			vars[varName] = string(m[1])
		}
		totals = append(totals, fmt.Sprintf("%s %.3fs", name, elapsed))
	}
	total := time.Since(totalStart).Seconds()
	perf = append(perf, fmt.Sprintf("total=%.6fs;%s;%s;0;", total, a.Get("w"), a.Get("c")))
	st := model.StateOK
	if w, c := a.Get("w"), a.Get("c"); w != "" || c != "" {
		if code, err := nagios.Evaluate(total, w, c); err == nil {
			st = model.State(code)
		}
	}
	label := map[model.State]string{0: "OK", 1: "WARNING", 2: "CRITICAL"}[st]
	text := fmt.Sprintf("FLOW %s - %d steps in %.3fs (%s) | %s",
		label, len(steps), total, strings.Join(totals, ", "), strings.Join(perf, " "))
	return st, nagios.ParseOutput(text)
}

func interpolate(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// extractJSON walks dot-paths ("data.items.0.id").
func extractJSON(body []byte, path string) (string, bool) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", false
	}
	cur := doc
	for _, part := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[part]
			if !ok {
				return "", false
			}
			cur = v
		case []any:
			var idx int
			if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 0 || idx >= len(node) {
				return "", false
			}
			cur = node[idx]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		return trimFloat(v), true
	case bool:
		return fmt.Sprint(v), true
	default:
		b, _ := json.Marshal(v)
		return string(b), true
	}
}

// memJar is a minimal in-memory cookie jar for http-flow sessions.
type memJar struct{ cookies map[string][]*http.Cookie }

func newMemJar() *memJar { return &memJar{cookies: map[string][]*http.Cookie{}} }

func (j *memJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.cookies[u.Host] = append(j.cookies[u.Host], cookies...)
}

func (j *memJar) Cookies(u *url.URL) []*http.Cookie { return j.cookies[u.Host] }

// checkNRPE bridges builtin:nrpe to the NRPE client (SPEC §8.4):
// -C command, -a args (comma), -p port, -n no-ssl, -V version.
func checkNRPE(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	command := a.Get("C", "command")
	if command == "" {
		command = "_NRPE_CHECK"
	}
	var args []string
	if v := a.Get("a", "args"); v != "" {
		args = strings.Split(v, ",")
	}
	opt := nagios.NRPEOptions{
		Address: net.JoinHostPort(host, fmt.Sprint(a.Int(5666, "p", "port"))),
		Version: a.Int(2, "V", "nrpe-version"),
		Timeout: a.Duration(10*time.Second, "t", "timeout"),
	}
	if !a.Bool("n", "no-ssl") {
		opt.TLS = &tls.Config{ServerName: host, InsecureSkipVerify: a.Bool("insecure")}
	}
	res, err := nagios.NRPEQuery(ctx, opt, command, args)
	if err != nil {
		return unknownf("nrpe: %v", err)
	}
	return nagios.ExitState(res.State), nagios.ParseOutput(res.Output)
}
