package alerting

// Object-level notification routing (Nagios contact_groups semantics):
// a HARD state change on an object whose effective spec carries
// contacts/contactGroups notifies those contacts directly — no alert
// rule required. The route honours enableNotifications, the
// notificationPeriod (resolved via the catalog), the notifyOn state
// filter, ack/downtime suppression (SPEC §9.1) and each contact's
// channel preferences. Deliveries ride the outbox like every other
// notification (retry/backoff/DLQ, immutable notification events).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// objectNotifyJob is the outbox payload for kind "object-notification"
// (mirrored in internal/notify — kept in sync via JSON shape).
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

// notifyObjectContacts dispatches direct object notifications for a
// state_change event. Called from handleEvent for every event; all
// filtering happens here.
func (en *Engine) notifyObjectContacts(ctx context.Context, e *model.Event) {
	if e.Type != model.EventStateChange || e.ObjectID == "" || en.cat == nil {
		return
	}
	var p model.StateChangePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return
	}
	// Only hard transitions notify (soft retries stay quiet, SPEC §6.3).
	if p.StateType != model.StateHard {
		return
	}
	entry := en.cat.Get(e.ObjectID)
	if entry == nil {
		return
	}
	eff := entry.Effective
	if len(eff.Contacts) == 0 && len(eff.ContactGroups) == 0 {
		return
	}
	if eff.EnableNotifications != nil && !*eff.EnableNotifications {
		return
	}
	token := model.NotifyToken(p.To, p.Kind)
	recovery := token == "recovery"
	// A "recovery" is only meaningful after a problem state.
	if recovery && p.From == model.StateOK {
		return
	}
	if !eff.WantsNotify(token) {
		return
	}

	now := time.Now().UTC()
	if eff.NotificationPeriod != "" {
		if tp := en.cat.Period(e.TenantID, eff.NotificationPeriod); tp != nil && !tp.Contains(now) {
			return // outside the notification window
		}
	}
	// Suppression (SPEC §9.1): objects in downtime never notify; acked
	// problems stay quiet (the recovery still goes out — ack is cleared
	// by the pipeline on hard recovery).
	if cs, err := en.store.GetCheckState(ctx, e.ObjectID); err == nil && cs != nil {
		if cs.DowntimeDepth > 0 {
			return
		}
		if !recovery && cs.AckedBy != "" {
			return
		}
	}

	contacts := en.resolveObjectContacts(ctx, e.TenantID, eff.Contacts, eff.ContactGroups)
	if len(contacts) == 0 {
		en.log.Warn("alerting: object has no resolvable contacts",
			"object", p.ObjectName, "groups", eff.ContactGroups)
		return
	}

	// Preference filtering uses the problem's severity: for recoveries
	// the severity of the state we recovered FROM, so a contact paged
	// for critical also receives its recovery.
	displaySev := model.SeverityFromState(p.To, p.Kind)
	filterSev := displaySev
	if recovery {
		filterSev = model.SeverityFromState(p.From, p.Kind)
	}
	lookup := func(name string) *model.TimePeriod { return en.cat.Period(e.TenantID, name) }

	for _, contact := range contacts {
		channels := model.PreferredChannels(contact, filterSev, now, lookup)
		if len(channels) == 0 {
			channels = []model.ChannelType{model.ChannelEmail}
		}
		for _, ch := range channels {
			payload, _ := json.Marshal(objectNotifyJob{
				TenantID: e.TenantID, ObjectID: e.ObjectID,
				ObjectName: p.ObjectName, HostName: p.HostName, Kind: p.Kind,
				FromLabel: p.FromLabel, ToLabel: p.ToLabel, Output: p.Output,
				Severity: displaySev, Recovery: recovery,
				ContactID: contact.ID, Channel: ch, Labels: p.Labels,
			})
			item := &storage.OutboxItem{TenantID: e.TenantID,
				Kind: "object-notification", Payload: payload}
			if err := en.store.EnqueueOutbox(ctx, item); err != nil {
				en.log.Error("alerting: object notification enqueue", "err", err)
				continue
			}
			select { // wake the notifier
			case en.bus.Notifications <- item.ID:
			default:
			}
		}
	}
}

// resolveObjectContacts expands contact + group references (names or
// ids) into unique contacts.
func (en *Engine) resolveObjectContacts(ctx context.Context, tenantID string,
	contactRefs, groupRefs []string) []*model.Contact {
	seen := map[string]*model.Contact{}
	add := func(ref string) {
		c, err := storage.LoadOne[model.Contact](ctx, en.store, tenantID,
			storage.KindContact, ref)
		if err != nil {
			en.log.Warn("alerting: object contact missing", "ref", ref)
			return
		}
		seen[c.ID] = c
	}
	for _, ref := range contactRefs {
		add(ref)
	}
	for _, ref := range groupRefs {
		grp, err := storage.LoadOne[model.ContactGroup](ctx, en.store, tenantID,
			storage.KindContactGroup, ref)
		if err != nil {
			en.log.Warn("alerting: object contact group missing", "group", ref)
			continue
		}
		for _, m := range grp.Members {
			add(m)
		}
	}
	out := make([]*model.Contact, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	return out
}
