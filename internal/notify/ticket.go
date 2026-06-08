package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Ticket-system transports (F-04.05): ServiceNow, Zendesk, Jira and a
// generic HTTP gateway. A ticket channel behaves like any notification
// channel (escalation steps, test sends) — additionally the created
// ticket is recorded on the alert (model.TicketRef) so resolution can
// auto-close it, and mirrored to the linked incident's ticketUrl.

// sendTicket creates a ticket via the channel's provider and links it
// to the alert in the render context. opts carries escalation-action
// overrides (params, autoClose); nil for plain channel notifications.
func (m *Manager) sendTicket(ctx context.Context, ch *model.NotificationChannel,
	subject, body string, rc *RenderContext, opts *model.TicketAction) (string, error) {
	if subject == "" && rc != nil {
		subject = strings.TrimSpace("[" + rc.Severity + "] " + rc.Title)
	}
	params := map[string]string{}
	autoClose := ch.Config["autoClose"] == "true"
	if opts != nil {
		autoClose = opts.AutoClose
		for k, v := range opts.Params {
			params[k] = v
		}
	}

	var ref, ticketURL string
	var err error
	switch ch.Type {
	case model.ChannelServiceNow:
		ref, ticketURL, err = m.createServiceNow(ctx, ch, subject, body, rc, params)
	case model.ChannelZendesk:
		ref, ticketURL, err = m.createZendesk(ctx, ch, subject, body, rc, params)
	case model.ChannelJira:
		ref, ticketURL, err = m.createJira(ctx, ch, subject, body, rc, params)
	case model.ChannelTicket:
		ref, ticketURL, err = m.createGenericTicket(ctx, ch, body)
	default:
		return "", fmt.Errorf("channel %q is not a ticket type", ch.Type)
	}
	if err != nil {
		return "", err
	}

	ticket := &model.TicketRef{Channel: ch.Name, Type: string(ch.Type),
		Ref: ref, URL: ticketURL, AutoClose: autoClose}
	m.attachTicket(ctx, rc, ticket)
	return ref, nil
}

// attachTicket persists the ticket on the alert and mirrors the link to
// the bundled incident (Incident.TicketURL, SPEC §6.4).
func (m *Manager) attachTicket(ctx context.Context, rc *RenderContext, t *model.TicketRef) {
	if rc == nil || rc.Alert == nil || rc.Alert.ID == "" || rc.Alert.ID == "test" {
		return // test send / direct send: nothing to link
	}
	alert := rc.Alert
	if err := m.store.SetAlertTicket(ctx, alert.TenantID, alert.ID, t); err != nil {
		m.log.Warn("notify: ticket link", "alert", alert.ID, "err", err)
	}
	if alert.IncidentID == "" || t.URL == "" {
		return
	}
	if inc, err := m.store.GetIncident(ctx, alert.TenantID, alert.IncidentID); err == nil && inc.TicketURL == "" {
		inc.TicketURL = t.URL
		_ = m.store.UpdateIncident(ctx, inc, 0)
	}
}

// --- ServiceNow (Table API) ---

func (m *Manager) snTable(ch *model.NotificationChannel) string {
	if t := ch.Config["table"]; t != "" {
		return t
	}
	return "incident"
}

func (m *Manager) createServiceNow(ctx context.Context, ch *model.NotificationChannel,
	subject, body string, rc *RenderContext, params map[string]string) (ref, ticketURL string, err error) {
	base := strings.TrimSuffix(ch.Config["url"], "/")
	if base == "" {
		return "", "", fmt.Errorf("servicenow: config.url required (https://<instance>.service-now.com)")
	}
	urgency := "3"
	if rc != nil {
		switch rc.Severity {
		case "CRITICAL":
			urgency = "1"
		case "WARNING":
			urgency = "2"
		}
	}
	payload := map[string]string{
		"short_description": subject,
		"description":       body,
		"urgency":           urgency,
		"impact":            urgency,
	}
	if rc != nil && rc.Alert != nil {
		payload["correlation_id"] = rc.Alert.ID
	}
	if g := params["assignmentGroup"]; g != "" {
		payload["assignment_group"] = g
		delete(params, "assignmentGroup")
	}
	for k, v := range params {
		payload[k] = v
	}
	table := m.snTable(ch)
	raw, status, err := m.ticketRequest(ctx, ch, http.MethodPost,
		base+"/api/now/table/"+table, payload)
	if err != nil {
		return "", "", err
	}
	if status >= 300 {
		return "", "", fmt.Errorf("servicenow: HTTP %d: %s", status, firstLine(string(raw)))
	}
	var out struct {
		Result struct {
			SysID  string `json:"sys_id"`
			Number string `json:"number"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Result.SysID == "" {
		return "", "", fmt.Errorf("servicenow: unexpected response: %s", firstLine(string(raw)))
	}
	return out.Result.SysID, base + "/" + table + ".do?sys_id=" + out.Result.SysID, nil
}

func (m *Manager) closeServiceNow(ctx context.Context, ch *model.NotificationChannel,
	t *model.TicketRef, note string) error {
	base := strings.TrimSuffix(ch.Config["url"], "/")
	state := ch.Config["closeState"]
	if state == "" {
		state = "6" // incident: Resolved
	}
	code := ch.Config["closeCode"]
	if code == "" {
		code = "Solution provided"
	}
	payload := map[string]string{"state": state, "close_code": code, "close_notes": note}
	raw, status, err := m.ticketRequest(ctx, ch, http.MethodPatch,
		base+"/api/now/table/"+m.snTable(ch)+"/"+t.Ref, payload)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("servicenow close: HTTP %d: %s", status, firstLine(string(raw)))
	}
	return nil
}

// --- Zendesk (Tickets API) ---

func (m *Manager) createZendesk(ctx context.Context, ch *model.NotificationChannel,
	subject, body string, rc *RenderContext, params map[string]string) (ref, ticketURL string, err error) {
	base := strings.TrimSuffix(ch.Config["url"], "/")
	if base == "" {
		return "", "", fmt.Errorf("zendesk: config.url required (https://<subdomain>.zendesk.com)")
	}
	priority := "normal"
	if rc != nil {
		switch rc.Severity {
		case "CRITICAL":
			priority = "urgent"
		case "WARNING":
			priority = "high"
		}
	}
	ticket := map[string]any{
		"subject": subject, "priority": priority,
		"comment": map[string]string{"body": body},
		"tags":    []string{"northplane"},
	}
	if rc != nil && rc.Alert != nil {
		ticket["external_id"] = rc.Alert.ID
	}
	for k, v := range params {
		ticket[k] = v
	}
	raw, status, err := m.ticketRequest(ctx, ch, http.MethodPost,
		base+"/api/v2/tickets.json", map[string]any{"ticket": ticket})
	if err != nil {
		return "", "", err
	}
	if status >= 300 {
		return "", "", fmt.Errorf("zendesk: HTTP %d: %s", status, firstLine(string(raw)))
	}
	var out struct {
		Ticket struct {
			ID int64 `json:"id"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Ticket.ID == 0 {
		return "", "", fmt.Errorf("zendesk: unexpected response: %s", firstLine(string(raw)))
	}
	id := fmt.Sprint(out.Ticket.ID)
	return id, base + "/agent/tickets/" + id, nil
}

func (m *Manager) closeZendesk(ctx context.Context, ch *model.NotificationChannel,
	t *model.TicketRef, note string) error {
	base := strings.TrimSuffix(ch.Config["url"], "/")
	status := ch.Config["closeStatus"]
	if status == "" {
		status = "solved"
	}
	raw, code, err := m.ticketRequest(ctx, ch, http.MethodPut,
		base+"/api/v2/tickets/"+t.Ref+".json", map[string]any{
			"ticket": map[string]any{"status": status,
				"comment": map[string]any{"body": note, "public": false}}})
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("zendesk close: HTTP %d: %s", code, firstLine(string(raw)))
	}
	return nil
}

// --- Jira (REST API v2 — Cloud and Server) ---

func (m *Manager) createJira(ctx context.Context, ch *model.NotificationChannel,
	subject, body string, rc *RenderContext, params map[string]string) (ref, ticketURL string, err error) {
	base := strings.TrimSuffix(ch.Config["url"], "/")
	project := ch.Config["project"]
	if base == "" || project == "" {
		return "", "", fmt.Errorf("jira: config.url and config.project required")
	}
	issueType := ch.Config["issueType"]
	if issueType == "" {
		issueType = "Task"
	}
	fields := map[string]any{
		"project":     map[string]string{"key": project},
		"summary":     subject,
		"description": body,
		"issuetype":   map[string]string{"name": issueType},
		"labels":      []string{"northplane"},
	}
	for k, v := range params {
		fields[k] = v
	}
	raw, status, err := m.ticketRequest(ctx, ch, http.MethodPost,
		base+"/rest/api/2/issue", map[string]any{"fields": fields})
	if err != nil {
		return "", "", err
	}
	if status >= 300 {
		return "", "", fmt.Errorf("jira: HTTP %d: %s", status, firstLine(string(raw)))
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Key == "" {
		return "", "", fmt.Errorf("jira: unexpected response: %s", firstLine(string(raw)))
	}
	return out.Key, base + "/browse/" + out.Key, nil
}

func (m *Manager) closeJira(ctx context.Context, ch *model.NotificationChannel,
	t *model.TicketRef, note string) error {
	base := strings.TrimSuffix(ch.Config["url"], "/")
	// Workflow transitions are instance-specific: closing requires the
	// configured transition id (config.closeTransitionId). Without one we
	// still leave a resolution comment.
	if id := ch.Config["closeTransitionId"]; id != "" {
		raw, status, err := m.ticketRequest(ctx, ch, http.MethodPost,
			base+"/rest/api/2/issue/"+t.Ref+"/transitions",
			map[string]any{"transition": map[string]string{"id": id}})
		if err != nil {
			return err
		}
		if status >= 300 {
			return fmt.Errorf("jira transition: HTTP %d: %s", status, firstLine(string(raw)))
		}
	}
	raw, status, err := m.ticketRequest(ctx, ch, http.MethodPost,
		base+"/rest/api/2/issue/"+t.Ref+"/comment", map[string]string{"body": note})
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("jira comment: HTTP %d: %s", status, firstLine(string(raw)))
	}
	return nil
}

// --- generic HTTP ticket gateway ---

// createGenericTicket POSTs the rendered channel template (JSON) to
// config.url. The ticket id is extracted from the response via
// config.refField (dot path, default "id"); config.ticketUrlTemplate
// with a {ref} placeholder yields the human link.
func (m *Manager) createGenericTicket(ctx context.Context, ch *model.NotificationChannel,
	body string) (ref, ticketURL string, err error) {
	u := ch.Config["url"]
	if u == "" {
		return "", "", fmt.Errorf("ticket channel: config.url required")
	}
	if !json.Valid([]byte(body)) {
		return "", "", fmt.Errorf("ticket template must render valid JSON")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Northplane-Ticket/1.0")
	m.ticketAuth(req, ch)
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("ticket gateway: HTTP %d: %s", resp.StatusCode, firstLine(string(raw)))
	}
	field := ch.Config["refField"]
	if field == "" {
		field = "id"
	}
	ref = jsonField(raw, field)
	if ref == "" {
		return "", "", fmt.Errorf("ticket gateway: response has no %q field", field)
	}
	if tpl := ch.Config["ticketUrlTemplate"]; tpl != "" {
		ticketURL = strings.ReplaceAll(tpl, "{ref}", ref)
	}
	return ref, ticketURL, nil
}

func (m *Manager) closeGenericTicket(ctx context.Context, ch *model.NotificationChannel,
	t *model.TicketRef, note string) error {
	u := ch.Config["closeUrl"]
	if u == "" {
		return nil // close not configured: nothing to do
	}
	u = strings.ReplaceAll(u, "{ref}", t.Ref)
	method := ch.Config["closeMethod"]
	if method == "" {
		method = http.MethodPost
	}
	body := ch.Config["closeBody"]
	if body == "" {
		body = `{"status":"closed","note":` + jsonString(note) + `}`
	} else {
		body = strings.NewReplacer("{ref}", t.Ref, "{note}", note).Replace(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	m.ticketAuth(req, ch)
	resp, err := hookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // drain for keep-alive
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ticket close: HTTP %d", resp.StatusCode)
	}
	return nil
}

// --- shared transport helpers ---

// ticketAuth applies the channel's auth config: basic (username/password,
// Zendesk "email/token" style works verbatim) or bearer token.
func (m *Manager) ticketAuth(req *http.Request, ch *model.NotificationChannel) {
	if user := ch.Config["username"]; user != "" {
		req.SetBasicAuth(user, m.resolveSecret(ch.TenantID, ch.Config["password"]))
	}
	// Zendesk API-token convention: email + apiToken → "email/token:token"
	if email, apiTok := ch.Config["email"], m.resolveSecret(ch.TenantID, ch.Config["apiToken"]); email != "" && apiTok != "" {
		req.SetBasicAuth(email+"/token", apiTok)
	}
	if tok := m.resolveSecret(ch.TenantID, ch.Config["token"]); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// ticketRequest sends a JSON payload and returns the (bounded) response.
func (m *Manager) ticketRequest(ctx context.Context, ch *model.NotificationChannel,
	method, url string, payload any) ([]byte, int, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Northplane-Ticket/1.0")
	m.ticketAuth(req, ch)
	resp, err := hookClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return body, resp.StatusCode, nil
}

// closeTicket dispatches the provider-specific close.
func (m *Manager) closeTicket(ctx context.Context, tenantID string,
	t *model.TicketRef, note string) error {
	ch, err := m.channelByName(ctx, tenantID, t.Channel)
	if err != nil {
		return err
	}
	switch ch.Type {
	case model.ChannelServiceNow:
		return m.closeServiceNow(ctx, ch, t, note)
	case model.ChannelZendesk:
		return m.closeZendesk(ctx, ch, t, note)
	case model.ChannelJira:
		return m.closeJira(ctx, ch, t, note)
	case model.ChannelTicket:
		return m.closeGenericTicket(ctx, ch, t, note)
	default:
		return fmt.Errorf("channel %q (%s) cannot close tickets", t.Channel, ch.Type)
	}
}

// deliverTicketClose handles outbox kind "ticket-close" (enqueued by
// storage.ResolveAlert for auto-close tickets).
func (m *Manager) deliverTicketClose(ctx context.Context, item *storage.OutboxItem) (string, error) {
	var job struct {
		TenantID string           `json:"tenantId"`
		AlertID  string           `json:"alertId"`
		Title    string           `json:"title"`
		Ticket   *model.TicketRef `json:"ticket"`
	}
	if err := json.Unmarshal(item.Payload, &job); err != nil {
		return "", fmt.Errorf("bad payload: %w", err)
	}
	if job.Ticket == nil || job.Ticket.Ref == "" {
		return "", nil
	}
	note := "Resolved by Northplane: " + job.Title + " (alert " + job.AlertID + ")"
	if m.SendHook != nil { // tests intercept transports
		ch := &model.NotificationChannel{Name: job.Ticket.Channel,
			Type: model.ChannelType(job.Ticket.Type), TenantID: job.TenantID}
		_, err := m.SendHook(ch, "close:"+job.Ticket.Ref, "", note)
		return "", err
	}
	return "", m.closeTicket(ctx, job.TenantID, job.Ticket, note)
}

// jsonField extracts a dot-path string/number field from a JSON body.
func jsonField(raw []byte, path string) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	for _, part := range strings.Split(path, ".") {
		obj, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = obj[part]
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strings.TrimSuffix(fmt.Sprint(t), ".0")
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
