package notify

// Delivery of object-level state notifications (outbox kind
// "object-notification", enqueued by internal/alerting/objnotify.go):
// direct Nagios-style contact/contact-group routing on hard state
// changes. Rendering reuses the channel templates over a synthetic,
// non-persisted alert so per-channel template overrides (F-04.09)
// apply unchanged; there is no ack link (nothing to acknowledge).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// objectNotifyJob mirrors alerting.objectNotifyJob (kept in sync via
// JSON shape, same convention as notifyJob).
type objectNotifyJob struct {
	TenantID   string            `json:"tenantId"`
	ObjectID   string            `json:"objectId"`
	ObjectName string            `json:"objectName"`
	HostName   string            `json:"hostName,omitempty"`
	Kind       model.Kind        `json:"kind"`
	FromLabel  string            `json:"from"`
	ToLabel    string            `json:"to"`
	Output     string            `json:"output,omitempty"`
	Severity   model.Severity    `json:"severity"`
	Recovery   bool              `json:"recovery,omitempty"`
	ContactID  string            `json:"contactId"`
	Channel    model.ChannelType `json:"channel"`
	Labels     model.Labels      `json:"labels,omitempty"`
}

func (m *Manager) deliverObjectNotification(ctx context.Context, item *storage.OutboxItem) (string, error) {
	var job objectNotifyJob
	if err := json.Unmarshal(item.Payload, &job); err != nil {
		return "", fmt.Errorf("bad payload: %w", err)
	}
	contact, err := storage.LoadOne[model.Contact](ctx, m.store, job.TenantID,
		storage.KindContact, job.ContactID)
	if err != nil {
		return "", fmt.Errorf("contact %s: %w", job.ContactID, err)
	}
	channel, err := m.channelFor(ctx, job.TenantID, job.Channel)
	if err != nil {
		return "", err
	}

	name := job.ObjectName
	if job.HostName != "" {
		name = job.HostName + " / " + job.ObjectName
	}
	title := fmt.Sprintf("%s is %s", name, job.ToLabel)
	if job.Recovery {
		title = fmt.Sprintf("%s recovered (%s → %s)", name, job.FromLabel, job.ToLabel)
	}
	if job.Output != "" {
		title += ": " + job.Output
	}
	// Synthetic alert: never persisted, exists only as render context so
	// the standard channel templates work unchanged.
	alert := &model.Alert{
		TenantID: job.TenantID, ObjectID: job.ObjectID,
		Severity: job.Severity, Status: model.AlertOpen,
		Title: title, Labels: job.Labels, OpenedAt: time.Now().UTC(),
	}
	rctx := m.renderContext(alert, contact, notifyJob{
		AlertID: "", TenantID: job.TenantID, ContactID: job.ContactID,
		Channel: job.Channel, Policy: "object",
	})
	// State notifications have no alert to acknowledge; deep-link to the
	// object instead of a (nonexistent) alert page.
	rctx.AckURL = ""
	if m.BaseURL != "" {
		rctx.AlertURL = m.BaseURL + "/objects/" + job.ObjectID
	}

	subject, body, err := m.render(channel, rctx)
	if err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	target := targetFor(channel.Type, contact, channel)
	if target == "" {
		return "", fmt.Errorf("contact %q has no %s target", contact.Name, channel.Type)
	}

	start := time.Now()
	var providerID string
	if m.SendHook != nil {
		providerID, err = m.SendHook(channel, target, subject, body)
	} else {
		providerID, err = m.send(ctx, channel, target, subject, body, rctx)
	}
	status := model.NotifySent
	errText := ""
	if err != nil {
		status, errText = model.NotifyFailed, err.Error()
	}
	rec := model.NotificationRecord{
		ContactID: contact.ID, Contact: contact.Name,
		Channel: channel.Type, ChannelID: channel.ID,
		Target: mask(target), Status: status, Attempt: item.Attempts + 1,
		Error: errText, ProviderID: providerID,
		LatencyMS: time.Since(start).Milliseconds(),
	}
	raw, _ := json.Marshal(rec)
	ev := &model.Event{ID: model.NewID(), TenantID: job.TenantID, TS: time.Now().UTC(),
		Type: model.EventNotification, ObjectID: job.ObjectID,
		Severity: model.SevInfo, Payload: raw}
	m.recordEvent(ctx, ev)
	m.bus.FanoutOnly(ev)
	return providerID, err
}
