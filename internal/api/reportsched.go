package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// Scheduled reports (SPEC §9.8, CMP-Reports parity: "Berichte … automatisiert
// per E-Mail in zuvor definierten Intervallen", plus Archivierung). A
// per-minute loop renders every tenant's reports when their schedule slot
// comes due, archives the bytes (HTML + CSV) and e-mails the HTML.
//
// Once a slot is archived it is never rendered again (the archive row is the
// dedup gate). A failed mail does NOT block archival: the slot is marked done
// after a successful render, so a transient SMTP outage is logged and the
// report stays downloadable — it is not retried in a tight loop. A failed
// render leaves the slot unmarked and is retried next tick.

// --- schedule grammar -------------------------------------------------------

// schedKind is the period a report repeats on.
type schedKind int

const (
	schedDaily schedKind = iota
	schedWeekly
	schedMonthly
)

// sched is a parsed schedule spec. Hour/Minute default to 07:00 local.
// Weekday applies to weekly; Day (1..31) applies to monthly.
type sched struct {
	Kind    schedKind
	Weekday time.Weekday // weekly
	Day     int          // monthly day-of-month (1..31)
	Hour    int
	Minute  int
}

var weekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
	"wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday,
	// short forms
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// parseSchedule parses the schedule grammar (SPEC §9.8):
//
//	daily | daily@HH:MM
//	weekly:<weekday> | weekly:<weekday>@HH:MM
//	monthly | monthly:<day> | monthly:<day>@HH:MM   (default day = 1)
//
// Time defaults to 07:00 local. An empty spec is not schedulable.
func parseSchedule(spec string) (sched, error) {
	s := sched{Hour: 7, Minute: 0}
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return s, fmt.Errorf("empty schedule")
	}
	// split off optional @HH:MM
	body := spec
	if at := strings.IndexByte(spec, '@'); at >= 0 {
		body = spec[:at]
		h, m, err := parseHHMM(spec[at+1:])
		if err != nil {
			return s, err
		}
		s.Hour, s.Minute = h, m
	}
	// split off optional :<arg>
	head := body
	arg := ""
	if c := strings.IndexByte(body, ':'); c >= 0 {
		head, arg = body[:c], body[c+1:]
	}
	switch head {
	case "daily":
		if arg != "" {
			return s, fmt.Errorf("daily takes no argument: %q", spec)
		}
		s.Kind = schedDaily
	case "weekly":
		s.Kind = schedWeekly
		wd, ok := weekdays[arg]
		if !ok {
			return s, fmt.Errorf("weekly needs a weekday (e.g. weekly:monday), got %q", arg)
		}
		s.Weekday = wd
	case "monthly":
		s.Kind = schedMonthly
		s.Day = 1
		if arg != "" {
			d, err := strconv.Atoi(arg)
			if err != nil || d < 1 || d > 31 {
				return s, fmt.Errorf("monthly day must be 1..31, got %q", arg)
			}
			s.Day = d
		}
	default:
		return s, fmt.Errorf("unknown schedule %q (daily | weekly:<weekday> | monthly[:<day>])", spec)
	}
	return s, nil
}

func parseHHMM(s string) (int, int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, fmt.Errorf("time must be HH:MM, got %q", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("invalid time %q", s)
	}
	return hh, mm, nil
}

// slotFor returns the current period's slot key and the local time at which
// that slot becomes due, computed against now (local). The scheduler renders
// when now is at/after the returned at and the slot has not been archived.
//
//	daily   → "2006-01-02"  at today HH:MM
//	weekly  → "2006-Www"    at the configured weekday HH:MM of this ISO week
//	monthly → "2006-01"     at the configured day HH:MM of this month
//
// For weekly/monthly the at time can fall earlier in the current period than
// now (e.g. a Monday report evaluated on Wednesday) — that is intended: the
// slot is due and, if not yet archived, fires immediately (a missed run is
// caught up once, not skipped).
func slotFor(s sched, now time.Time) (slotKey string, at time.Time) {
	loc := now.Location()
	switch s.Kind {
	case schedDaily:
		at = time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, 0, 0, loc)
		return now.Format("2006-01-02"), at
	case schedWeekly:
		// anchor to the configured weekday within now's ISO week
		delta := int(s.Weekday) - int(now.Weekday())
		day := now.AddDate(0, 0, delta)
		at = time.Date(day.Year(), day.Month(), day.Day(), s.Hour, s.Minute, 0, 0, loc)
		iy, iw := now.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", iy, iw), at
	case schedMonthly:
		day := s.Day
		if dim := daysInMonth(now.Year(), now.Month()); day > dim {
			day = dim // clamp e.g. day 31 in February
		}
		at = time.Date(now.Year(), now.Month(), day, s.Hour, s.Minute, 0, 0, loc)
		return now.Format("2006-01"), at
	}
	return now.Format("2006-01-02"), now
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// --- scheduler loop ---------------------------------------------------------

// ReportScheduler runs scheduled report delivery (SPEC §9.8). It ticks once
// a minute (first tick ~10 s after start) and drives runDue for the current
// wall clock. Mirror of Janitor's shape; wire with `go a.ReportScheduler(ctx)`.
func (a *API) ReportScheduler(ctx context.Context) {
	first := time.NewTimer(10 * time.Second)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
		a.runDue(ctx, time.Now())
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runDue(ctx, time.Now())
		}
	}
}

// runDue is one scheduler step (extracted for tests): for every tenant and
// every scheduled report, render+archive+send any slot that is due and not
// yet archived. Errors are logged and never abort the sweep.
func (a *API) runDue(ctx context.Context, now time.Time) {
	tenants, err := a.Store.Tenants(ctx)
	if err != nil {
		a.Log.Error("reportsched: tenants", "err", err)
		return
	}
	for _, t := range tenants {
		reports, err := storage.LoadAll[model.Report](ctx, a.Store, t.ID, storage.KindReport)
		if err != nil {
			a.Log.Error("reportsched: load reports", "err", err, "tenant", t.ID)
			continue
		}
		for _, rep := range reports {
			if strings.TrimSpace(rep.Schedule) == "" {
				continue
			}
			a.runReportIfDue(ctx, t.ID, rep, now)
		}
	}
}

// runReportIfDue renders+archives+sends one report iff its current slot is
// due (now >= at) and not already archived. It is the per-report body of the
// sweep; a bad schedule spec is logged once and skipped.
func (a *API) runReportIfDue(ctx context.Context, tenantID string, rep *model.Report, now time.Time) {
	spec, err := parseSchedule(rep.Schedule)
	if err != nil {
		a.Log.Error("reportsched: bad schedule", "report", rep.Name, "spec", rep.Schedule, "err", err)
		return
	}
	slot, at := slotFor(spec, now)
	if now.Before(at) {
		return // not due yet this period
	}
	done, err := a.Store.HasReportArchiveSlot(ctx, tenantID, rep.Name, slot)
	if err != nil {
		a.Log.Error("reportsched: dedup check", "report", rep.Name, "err", err)
		return
	}
	if done {
		return // already produced this slot
	}
	if _, err := a.runReportOnce(ctx, tenantID, rep, slot, now); err != nil {
		// render failure: slot stays unmarked → retried next tick.
		a.Log.Error("reportsched: run", "report", rep.Name, "slot", slot, "err", err)
	}
}

// reportRunResult is the JSON summary of a render+archive+send.
type reportRunResult struct {
	Report     string `json:"report"`
	Slot       string `json:"slot"`
	Archived   bool   `json:"archived"`
	Recipients int    `json:"recipients"`
	Sent       int    `json:"sent"`
	MailError  string `json:"mailError,omitempty"`
}

// runReportOnce renders the report (HTML + CSV), archives both under slot,
// then — if recipients are configured — e-mails the HTML to each. A render
// error is returned (caller decides retry); a mail error is recorded in the
// result and logged but does NOT fail the call, so the archive survives.
// Shared by the scheduler and POST /reports/{name}:run.
func (a *API) runReportOnce(ctx context.Context, tenantID string, rep *model.Report, slot string, now time.Time) (*reportRunResult, error) {
	data, err := a.buildReportData(ctx, tenantID, rep, nil)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	html, err := renderReportHTML(data)
	if err != nil {
		return nil, fmt.Errorf("html: %w", err)
	}
	csv := renderReportCSV(data)

	res := &reportRunResult{Report: rep.Name, Slot: slot}
	// archive HTML then CSV under the same slot (one retention bucket).
	if err := a.Store.PutReportArchive(ctx, &storage.ReportArchiveEntry{
		TenantID: tenantID, ReportName: rep.Name, Slot: slot,
		Format: "html", Content: html, CreatedAt: now.UTC(),
	}, rep.Keep); err != nil {
		return nil, fmt.Errorf("archive html: %w", err)
	}
	if err := a.Store.PutReportArchive(ctx, &storage.ReportArchiveEntry{
		TenantID: tenantID, ReportName: rep.Name, Slot: slot,
		Format: "csv", Content: csv, CreatedAt: now.UTC(),
	}, rep.Keep); err != nil {
		// HTML already archived ⇒ slot is "done"; log and continue.
		a.Log.Error("reportsched: archive csv", "report", rep.Name, "err", err)
	}
	res.Archived = true

	if len(rep.Email) == 0 {
		return res, nil
	}
	res.Recipients = len(rep.Email)
	ch, err := a.reportEmailChannel(ctx, tenantID, rep)
	if err != nil {
		res.MailError = err.Error()
		a.Log.Error("reportsched: mail channel", "report", rep.Name, "err", err)
		return res, nil
	}
	subject := fmt.Sprintf("[Northplane] Report %s %s", rep.Name, slot)
	for _, to := range rep.Email {
		if _, err := a.Notify.SendDirect(ctx, ch, to, subject, string(html)); err != nil {
			res.MailError = err.Error()
			a.Log.Error("reportsched: send", "report", rep.Name, "to", to, "err", err)
			continue
		}
		res.Sent++
	}
	return res, nil
}

// reportEmailChannel resolves the e-mail channel for scheduled delivery: the
// channel named in rep.Params ("channel":"<name>") if present and an enabled
// e-mail channel, else the first enabled e-mail channel in the tenant.
func (a *API) reportEmailChannel(ctx context.Context, tenantID string, rep *model.Report) (*model.NotificationChannel, error) {
	channels, err := storage.LoadAll[model.NotificationChannel](ctx, a.Store, tenantID, storage.KindChannel)
	if err != nil {
		return nil, err
	}
	var named string
	if len(rep.Params) > 0 {
		var p reportParams
		_ = json.Unmarshal(rep.Params, &p)
		named = p.Channel
	}
	if named != "" {
		for _, c := range channels {
			if c.Name == named {
				if c.Type != model.ChannelEmail || !c.Enabled {
					return nil, fmt.Errorf("channel %q is not an enabled e-mail channel", named)
				}
				return c, nil
			}
		}
		return nil, fmt.Errorf("channel %q not found", named)
	}
	for _, c := range channels {
		if c.Type == model.ChannelEmail && c.Enabled {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no enabled e-mail channel configured")
}
