# Northplane × Stept — Backlog aus der Tour-Aufnahme (2026-08-23)

Ergebnis einer Session, in der die laufende App auf **doktrace.com** (englische
UI, als `admin@doktrace.com`) mit der **Stept-Chrome-Extension** über MCP
gefahren wurde (`browser_record_start` → `browser_act` → `browser_record_stop`,
danach `update_tour` / `publish_tour`). Zwei Fliegen mit einer Klappe: neue
Touren + Doku für Northplane **und** Fehler im Recorder/Player von Stept.

**Geliefert (live im Stept-Workspace `wk_As5wfPgMuFbm3VtLaaKEB4xm`):**

| # | Tour | Trigger | ID |
|---|------|---------|----|
| 11 | Build a dashboard | `/dashboards*` | `01a02da2-80f2-7ea6-9eeb-b7d761d544cf` |
| 12 | Model a business service | `/business*` | `01a02da9-5340-733c-84b0-a65924defe0c` |
| 13 | Schedule a report | `/reports*` | `01a02dac-566b-7ecd-8aa1-1ed1650990b1` |
| 14 | Read the event stream | `/events*` | `01a02dad-8849-7837-9149-bc6957fff14d` |
| 15 | People, roles and channels (Admin 1) | `/admin*` (prio 10) | `01a02db0-42bb-7fdd-ab96-59a61ea4a850` |
| 16 | Ingest, integrate, operate (Admin 2) | `/admin*` (prio 5) | `01a02db1-fca4-73db-b905-82c1507a9a9b` |
| 17 | Build a phone menu (IVR) | `/alerting*` (prio −5) | `01a02db3-81c2-78d1-8267-49333b1d0276` |
| 18 | Read an object in depth | `/objects/*` (prio 20) | `01a02db5-77d9-7fe6-8af6-b69548079768` |

Repariert (waren rot): **1 · Welcome** (v5) und **6 · On-call rotation** (v4) —
sprachunabhängige `fallback_selectors` (DE-Labels *Anlegen/Bearbeiten/Kunde/
Suchen…/Assistent* + strukturelle Selektoren). Ursache war mit hoher
Wahrscheinlichkeit ein de-DE-Besucher: die Touren hängen an englischen
Accessible-Names.

Knowledge-Base-Dokumente (Stept, Quelle „MCP documents"): Dashboards ·
Business services/SLA · Reports · Event stream · Admin 1 · Admin 2 (inkl.
Tenants/Sites) · IVR/Inbound · Object detail · Tour-Index.

**Beim Aufnehmen in Prod angelegt (bewusst belassen, damit Touren etwas zeigen):**
Dashboard *Ops wall* (shared, KPI + Status donut „Service health"), Business
Service *Doktrace platform* (Worst, SLA 99,9 %) mit Blatt *Agent fleet*
(Selector `agent=np-agent`, Quorum 50 %), Report *Monthly availability*
(Availability, `env=prod`, 30 d, monatlich 1. 07:00 → info@myfoxit.com).

**Legende** — Schwere: `Hoch` / `Mittel` / `Niedrig` · Typ: `Bug` / `UX` /
`Enhancement` · Aufwand: `S` / `M` / `L`.

---

## Teil A — Stept (Recorder · Player · MCP)

| ID | Punkt | Schwere | Typ |
|----|-------|---------|-----|
| REC-1 | Player-UI unbenutzbar, solange ein Radix/shadcn-Modal offen ist | Hoch | Bug |
| REC-2 | Recorder-Selektoren kollabieren auf `#root button` / `button[id^="radix-"]` (nicht eindeutig) | Hoch | Bug |
| REC-3 | Tooltip rendert bei (0,0), wenn der Anker in einem Dialog liegt | Mittel | Bug |
| REC-4 | „Can't find this element" ist endgültig — keine Re-Resolution, wenn das Element später erscheint | Mittel | Bug |
| REC-5 | Keine MCP-Möglichkeit, rohe aufgenommene Steps (target, fallbacks, url, ids) zu lesen → `update_tour` zerstört Healing-Daten | Mittel | Enh |
| REC-6 | Eine Interaktion → mehrere Steps (Switch 2, Radix-Select 4, Radio 3 inkl. `Type "on"`) | Mittel | Bug |
| REC-7 | Tailwind-Klassen-/nth-of-type-Selektoren statt Accessible Name | Mittel | Bug |
| REC-8 | `browser_act` nach `name`/`role` dokumentiert, aber abgelehnt | Mittel | Bug |
| REC-9 | `browser_act` scrollt Ziel nicht in den Viewport (Tabs im Overflow, Zeilen unter dem Fold) | Mittel | Bug |
| REC-10 | `browser_open` / `browser_wait_for` liefern leere Snapshots; `wait_for` meldet nie `met` | Mittel | Bug |
| REC-11 | Kein `delete_tour` — misslungene Aufnahmen bleiben als Drafts | Niedrig | Enh |
| REC-12 | Fortschritts-Pill hinkt der Karte einen Step hinterher | Niedrig | Bug |
| REC-13 | Snapshot-Hinweis verweist auf nicht existentes `page_snapshot` mit `offset` | Niedrig | Bug |
| REC-14 | `<details>`/`<summary>` doppelt im Snapshot (426 Elemente auf /events) | Niedrig | UX |
| REC-15 | Step-Titel rendern Markdown wörtlich (`*business*`) | Niedrig | UX |
| REC-16 | Nach „Dismiss" wird eine `until_completed`-Tour in derselben Session nicht mehr angeboten | Niedrig | ? |
| REC-17 | „the click produced no visible change" auch bei echter (kleiner) Änderung | Niedrig | Bug |

### REC-1 — Tour-Karte unbenutzbar bei offenem Modal · Hoch · Bug
Radix `Dialog` (modal) setzt `pointer-events: none` auf `<body>`; das Stept-
Widget lebt im Body → Klicks auf *Next/Skip/End* fallen **durch** auf die App.
Beobachtet: *Next* im „Name it"-Step schloss den „New dashboard"-Dialog
(Pointer-down outside) und die Tour blieb bei 3/14; *Skip step* toggelte den
darunterliegenden *Shared*-Switch (Label ist volle Zeilenbreite) — zweimal.
→ Stept: `pointer-events: auto` explizit auf dem Widget-Host (wie Radix es für
seinen eigenen Content tut). App-seitige Mitigation (optional): Dialog-
`onPointerDownOutside` für `[data-stept]`-Ziele unterdrücken.
**Workaround in den neuen Touren:** In Dialogen nur *ein* Tooltip auf dem
Submit-Button mit `advance: element_click` (Besucher klickt den echten Button),
davor ein `wait`-Step. Funktioniert (Tour 12 verifiziert).

### REC-2 — Nicht eindeutige Selektoren · Hoch · Bug
„New dashboard", „Edit", „+ Add widget", „Tidy", „Save" → alle `#root button`
(= erster Button im Root, der Mandanten-Switcher); alle Admin-Tabs →
`button[id^="radix-"]`. Replay ankert garantiert falsch. Die Elemente haben
saubere Accessible Names — die sollte der Generalizer bevorzugen
(`aria/Save[role="button"]`, `[role=tab]:has-text('Roles')`, `data-testid`).

### REC-3 — Tooltip bei (0,0) · Mittel · Bug
Step „Name it" (Anker `div[role="dialog"] input[type="text"]`): Input wurde
gefunden und hervorgehoben, Karte stand aber oben links im Viewport; ebenso
ein Step, dessen Anker fehlte (Nullrect). Vermutlich Messung während der
Dialog-Open-Animation / Nullrect ohne Re-Measure. Der Submit-Button im selben
Dialog wird korrekt positioniert.

### REC-4 — Keine Re-Resolution · Mittel · Bug
Nach Timeout („Finding it on this page…" → „Can't find this element") bleibt
der Step tot, auch wenn das Element danach erscheint (Dialog erneut geöffnet).
Ein MutationObserver-Retry oder ein „Retry"-Button würde das lösen.

### REC-5 — Rohdaten nicht lesbar · Mittel · Enh
`get_tour_steps` liefert nur `n/kind/title/body/selector/url`, keine Step-IDs,
keine `target`-Deskriptoren, keine `fallback_selectors`, keine per-Step-URLs.
`update_tour.steps` ist Full-Replace → jede Textkorrektur an einer Aufnahme
verwirft das Healing. Gewünscht: `get_tour(raw=true)` oder Patch-Semantik
per Step-ID.

### REC-6/7 — Step-Rauschen und fragile Selektoren · Mittel · Bug
Radix-Select: *Click „— Root —"* · *Hover „— Root —"* · *Select „X" in …* (auf
dem versteckten `<select>`) · *Click „X"* = 4 Steps. Radio: *Click* · *Type
„on"* · *Check* = 3 Steps. Switch: *Click „no"* + *Click „yes"*. Selektoren
wie `div.grid.grid-cols-2 > div.block.text-sm:nth-of-type(1) > div.mt-1 >
input.h-9.w-full` oder `tbody.\[\&_tr\:last-child\]\:border-0 > tr.border-b:…`
überleben keinen Build.

### REC-8/9/10 — Driver · Mittel · Bug
`browser_act(name=…, role=…)` → „act needs an element index (from snapshot) or
x/y coordinates". Klicks auf Elemente außerhalb des Viewports (Admin-Tabs
*System health/Appearance* im horizontalen Overflow, Objektzeilen unter dem
Fold) bewirken nichts — kein `scrollIntoView`. `browser_open` auf eine noch
ladende SPA → `url: ""`, 0 Elemente; `browser_wait_for(text=…)` → `url: null`,
0 Elemente, kein `met`-Flag — selbst wenn die Seite längst gerendert ist.

### Positiv
`aria/Name[role=…]`-Selektoren, `:has-text()`-Fallbacks, `wait`-Steps
(`for: element`), `advance: element_click`, `url_match`+`priority`, die
„Finding it…"-Karte und `validate_experience` haben sauber funktioniert;
Tour 18 lief end-to-end fehlerfrei.

---

## Teil B — Northplane (App)

| ID | Punkt | Schwere | Typ | Aufwand |
|----|-------|---------|-----|---------|
| INC-1 | Incidents-Leerzustand verspricht „open one manually" — es gibt keinen Button; API hat `POST /incidents` und `:merge`, UI weder Anlegen noch Merge | Mittel | Bug | M |
| NAV-2 | Admin- (21) und Alerting-Tabs nur in `useState`, nicht in der URL → keine Deep-Links, Back-Button verliert Tab, Touren/Doku können nicht auf „Admin → Channels" verlinken | Mittel | UX | S |
| A11Y-2 | Dialog-Inputs ohne verknüpftes Label (New dashboard *Name*, Business service *Name*, Report *Name*, Channel *Name*, Downtime *Comment* …) → Accessible Name leer („field") | Mittel | Bug | S |
| SEC-1 | Channel-Dialog: *Password* ist `type=text` (Klartext sichtbar); *TLS mode* und *Allow plaintext (true/false)* sind Freitextfelder statt Select/Switch | Mittel | Bug | S |
| EVENT-2 | Notification/Escalation/Ack/Config-Zeilen ohne Zusammenfassung (nur Zeit+Badges); Payload nur als rohes, abgeschnittenes JSON; Typfilter kennt `flapping_*` nicht; Objektfilter nur per ID | Mittel | UX | M |
| I18N-2 | Report-Rendering deutsch („Typ · Zeitraum: 30 Tage · Erstellt") in englischer UI | Niedrig | Bug | S |
| I18N-3 | Agents-Tab: `agent.yaml`-Snippet mit deutschen Kommentaren in englischer UI | Niedrig | Bug | S |
| DASH-7 | Neues Dashboard: Default-KPI-Widget zu niedrig (Kacheln abgeschnitten, Scrollbar); *Time range*-Select wirkt deaktiviert | Niedrig | UX | S |
| AUDIT-1 | Audit-Log-Spalte *Actor* zeigt nur „user"/„token", nicht welche(r); Seq-Lücken (143→146) in der mandantengefilterten Ansicht irritieren Auditoren | Niedrig | UX | S |
| FORM-5 | IVR-Editor höher als der Viewport, *Save* unter dem Fold, kein Sticky-Footer (bekannt aus Juli, weiter offen) | Niedrig | UX | S |
| DETAIL-6 | Object-Detail *History*: „No entries." ohne Hinweis, was hier stünde | Niedrig | UX | S |
| DIALOG-1 | Radix-Modal sperrt `pointer-events` am Body → Drittanbieter-Overlays (Stept-Touren) unbedienbar; Mitigation siehe REC-1 | Niedrig | Enh | S |
| TOURS-1 | Tour-Inhalte datenabhängig: Tour 4 ankert *Acknowledge/Resolve* (nur mit offenen Alarmen vorhanden); Tour 3 (`/objects*`) matcht auch Detailseiten | Niedrig | UX | S |
| ROLES-1 | Roles-Tab zeigt nur System-Rollen des aktiven Mandanten; Users-Tab nennt `tenant-admin`, das in der Rollenliste (Mandant *Default*) fehlt — prüfen, ob gewollt | Niedrig | ? | S |

Weiter offen aus [UX-BACKLOG.md](./UX-BACKLOG.md) und beim Begehen erneut
gesehen: **NAV-1** (System health/Appearance im Tab-Overflow, auch für den
Driver unerreichbar), **AGENT-1** (Install-URL → privates Repo, 404),
**WIDGET-1** (Stept-Pill/Chat unten rechts überlagert Inhalte).

**Positiv bestätigt:** DASH-1 (Widget-Datenbindung: Objektsuche, Selector,
Metrik, Warn/Crit, Live-Preview) ist umgesetzt; DETAIL-1 (Token-Redaction
`•••` in der Effective-Config) und DETAIL-4 (Breadcrumb, *Host:*-Chip, „Other
services on this host") sind behoben; kein nativer `confirm()` mehr.

### INC-1 — Incidents ohne UI-Anlage · Mittel · Bug · M
`web/src/pages/Alerts.tsx` (`IncidentsPage`) rendert nur Karten mit
*Resolve*/*AI summary*; `internal/api/alerts.go` bietet `POST /api/v1/incidents`
und `:merge`. → Button „New incident" (Titel, Severity, Impact, Ticket-URL) und
Merge-Aktion in der Karte; Leerzustandstext bis dahin korrigieren.

### NAV-2 — Tabs in die URL · Mittel · UX · S
`Admin.tsx`/`AlertingConfig.tsx`: `const [tab, setTab] = useState(...)` → auf
`?tab=channels` (Search-Param) umstellen; dann sind Touren-Steps, Doku-Links
und Bookmarks möglich und der Recorder bekommt pro Tab eine eigene Step-URL.

### A11Y-2 — Labels verknüpfen · Mittel · Bug · S
Die `<label>`-Elemente in den Dialogen sind nicht mit ihrem Input verbunden
(kein `htmlFor`/`id`, kein `aria-label`). Screenreader nennen das Feld nicht;
Recorder/Healer können es nicht benennen („Click on „field""). In `kit.tsx`
bzw. den Dialog-Formularen `id`+`htmlFor` generieren.

### SEC-1 — Passwortfeld · Mittel · Bug · S
`components/admin/Channels.tsx`: Feld *Password* als `type=password` mit
„anzeigen"-Toggle; *TLS mode* → Select (`starttls | implicit | none`),
*Allow plaintext* → Switch.

### EVENT-2 — Events lesbar machen · Mittel · UX · M
Pro Typ eine einzeilige Zusammenfassung (Notification: `contact → channel ·
status · latency`; Escalation: `policy/step`; Config: `actor · action ·
resource`), JSON erst beim Aufklappen ohne Höhenbegrenzung; `flapping_start/
end` in den Typfilter; Objektfilter mit Namens-Autocomplete statt UUID.
