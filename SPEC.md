# Northplane — Systemspezifikation

**Ein API-first / AI-first Monitoring- und Alarmierungssystem**
Arbeitstitel: `northplane` · Version 0.4 (Draft) · Stand: 2026-06-07

> Referenzrahmen: Cancom-CMP-Produktfamilie (Datenblätter Monitoring, Monitoring Admin,
> Wizard, Alarmserver, Alarm App, CMP Agent, End-to-End, Visualization Dashboard, Reports,
> BPI, IPAM, Consumption, DEM) sowie die funktionalen (F-xx) und nicht-funktionalen
> (A-15.xx) Anforderungen der VERBUND-Ausschreibung „Alarmierungslösung".

---

## Inhaltsverzeichnis

1. [Executive Summary](#1-executive-summary)
2. [Kontext & Referenzanalyse](#2-kontext--referenzanalyse)
3. [Produktvision & Leitprinzipien](#3-produktvision--leitprinzipien)
4. [Ziele und Nicht-Ziele](#4-ziele-und-nicht-ziele)
5. [Personas & Kernszenarien](#5-personas--kernszenarien)
6. [Domänenmodell](#6-domänenmodell)
7. [Systemarchitektur](#7-systemarchitektur)
8. [Nagios-Kompatibilität im Detail](#8-nagios-kompatibilität-im-detail)
9. [Alerting, Eskalation & On-Call](#9-alerting-eskalation--on-call)
10. [AI-Subsystem](#10-ai-subsystem)
11. [API-Spezifikation](#11-api-spezifikation)
12. [Frontend-Spezifikation](#12-frontend-spezifikation)
13. [Sicherheit & Compliance](#13-sicherheit--compliance)
14. [Nicht-funktionale Anforderungen](#14-nicht-funktionale-anforderungen)
15. [Deployment & Betrieb](#15-deployment--betrieb)
16. [Teststrategie & Qualitätssicherung](#16-teststrategie--qualitätssicherung)
17. [Roadmap & Phasenplan](#17-roadmap--phasenplan)
18. [Entscheidungsregister (ADRs)](#18-entscheidungsregister-adrs)
19. [Anhang A: CMP → Northplane Capability-Mapping](#anhang-a-cmp--northplane-capability-mapping)
20. [Anhang B: Anforderungs-Traceability](#anhang-b-anforderungs-traceability)
21. [Anhang C: Glossar](#anhang-c-glossar)

---

## 1. Executive Summary

Northplane ist ein modernes Infrastruktur-Monitoring- und Alarmierungssystem, das die
funktionale Breite der Cancom-CMP-Suite (Monitoring + Admin + Wizard + Alarmserver +
Dashboards + Reports + BPI + Agent) in **einem kohärenten Produkt** abbildet — statt als
Sammlung historisch gewachsener Einzelwerkzeuge auf Nagios/Icinga-Basis.

**Drei Grundsatzentscheidungen definieren das Produkt:**

1. **API-first.** Jede Funktion existiert zuerst als versionierte REST-API (OpenAPI 3.1).
   Web-UI, CLI, Terraform-Provider und AI-Agenten sind gleichberechtigte Clients derselben
   API. Es gibt keinen UI-only-Funktionspfad und keinen Konfigurations-Reload-Zyklus —
   Konfiguration ist eine Datenbank-Transaktion, keine Datei plus Daemon-Restart.

2. **AI-first.** Das System ist von Grund auf so gebaut, dass LLM-Agenten es vollständig
   bedienen können: eingebauter MCP-Server, strukturierte und selbstbeschreibende APIs,
   ein Assistent für Triage, Korrelation, Root-Cause-Hypothesen und Konfiguration per
   natürlicher Sprache. AI ist dabei ein **privilegienloser API-Client** mit Audit-Pflicht
   und Approval-Gates — und das System bleibt ohne LLM zu 100 % funktional.

3. **Nagios-kompatibel, aber nicht Nagios-geformt.** Das Nagios-Plugin-Protokoll
   (Exit-Codes, Output, Perfdata, Makros) wird vollständig unterstützt — Tausende
   existierender Checks (`check_disk`, `check_http`, lokale Eigenentwicklungen) laufen
   unverändert. Das Objektmodell darüber ist jedoch modern: Labels statt starrer
   Hostgruppen, Templates mit Vererbung, deklarative Config-Bundles, Event-Streaming
   statt Logfile-Tailing.

**Technologie:** Go-Backend als statisches Single-Binary (Server, Satellit und CLI in
einem Artefakt; Agent als zweites, minimales Binary), relationaler Storage wahlweise
eingebettet (SQLite — Default für Einzelinstanzen) oder PostgreSQL (Server-Modus —
empfohlen für HA, hohe Event-Raten und Konzern-/Ausschreibungsprofile; beide Backends
ab M0 gleichwertig getestet, §7.3), dazu eigene Zeitreihen-Engine für Metriken,
React/TypeScript-SPA mit Tailwind und shadcn/ui, ausgeliefert via `go:embed` aus
demselben Binary.
Dependency-Politik: Standardbibliothek zuerst, jede externe Abhängigkeit wird einzeln
begründet (siehe §7.9).

---

## 2. Kontext & Referenzanalyse

### 2.1 Die CMP-Suite als funktionale Referenz

Die Analyse der CMP-Datenblätter ergibt folgende Capability-Landschaft. Sie definiert,
was ein konkurrenzfähiges Produkt in diesem Marktsegment können muss:

| CMP-Produkt | Kernfunktion laut Datenblatt |
|---|---|
| **CMP Monitoring** | Werkzeugsammlung zur Darstellung, Überwachung und zielgerichteten Alarmierung komplexer Infrastrukturen; historisierte Zustandsdaten je Gerät/Service; Abhängigkeiten zwischen Geräten/Services (Icinga-basiert) |
| **CMP Monitoring Admin** | Monitoring-Administration über Web-GUI: Hosts/Services anlegen und pflegen, Abhängigkeiten konfigurieren und darstellen |
| **CMP Monitoring Wizard** | Command-, Service- und Host-Templates; vereinfachte (Massen-)Anlage von Objekten |
| **CMP Alarmserver** (+ Webmin, Alarm App) | Alarm-Eingänge: HTTP(S)/Webhook, SNMP-Trap, E-Mail (SMTP/Exchange/Graph), SMS (GSM-Gateway), Telefon (SIP/ISDN), MQTT, Datenbank (ODBC: PostgreSQL/MSSQL/Oracle), Monitoring-Events, Alleinarbeiterschutz. Klassifikation/Labelling, Alarmregeln (Pending Period, Autoclose, Wiederholung, „kein Event in X min"), Alarmgruppen mit Aggregation, Eskalationsketten, Bereitschaftsgruppen + Dienstpläne („Bereitschaftsrad", Ad-hoc-Übernahme per SMS/Anruf), Mandantenfähigkeit, Ausgänge: SMS, Voice, E-Mail, App-Push, Webhook, ServiceNow (bidirektional) |
| **CMP Agent (CMPA)** | Ein Agent für Inventarisierung **und** Monitoring; Abfrage aller systemrelevanten Informationen über eine „intuitive API"; automatisches Ausrollen |
| **CMP End-to-End Monitoring** | Simulation von Desktop-Interaktionen mit einem Service (Robot) und Verarbeitung der Ergebnisse |
| **CMP Visualization Dashboard** | Personalisierte Dashboards, Verlinkungen, Farb-Semantik, Schnittstellen-Widgets |
| **CMP Reports** | Performance-/Verfügbarkeitsberichte (grafisch, PDF), interaktive Reports, automatische Archivierung und E-Mail-Versand |
| **CMP Monitoring BPI** | Geschäftsprozess-Sicht: bei Ausfällen schnell betroffene Services identifizieren (Impact-Analyse) |
| **CMP IPAM / IPAM Docu / MAC Finder** | Netzwerk-/Subnetz-Dokumentation, IP-Auslastung, Geräte-Discovery |
| **CMP Consumption Monitoring** | Verbrauchs-/Nutzungsdaten |
| **DEM** | Synthetisches Monitoring von Netzwerkverbindungen, -diensten und Web-Applikationen („Digital Experience") |

### 2.2 Abgeleitete Anforderungslage (Ausschreibung)

Aus den Anforderungstabellen (F-01…F-06 funktional, A-15.01…A-15.4x nicht-funktional)
sind insbesondere relevant:

- **Event-Eingänge** über HTTPS/Webhook, SNMP („Event-Sammler" im RZ), E-Mail, SMS,
  Telefon, MQTT, Datenbank-Polling (F-01.x)
- **Alarmregeln**: Klassifikation/Labelling mit Extraktion aus Events, Severity,
  Aggregation (min/max/sum/avg/median), Pending Periods, Autoclose, Heartbeat-Erkennung
  („kein Event von Quelle X über 10 min"), logische Operatoren, temporäre Suppression
  per Regex; ausdrücklich gewünscht: **Konfiguration auch deklarativ als
  Infrastructure-as-Code, z. B. Terraform** (F-02.x)
- **Bereitschaft**: Gruppen, Dienstpläne (semi-automatisch via „Rad"), Ad-hoc-Wechsel,
  Benachrichtigung bei Beginn/Ende, Abos, regelbasierte Planung, SAP-HCM-Export (F-03.x)
- **Benachrichtigung/Eskalation**: MS Teams, SMS, Telefonanlage, Webhook (konfigurierbarer
  Payload, Retry, Basic/Token/OAuth), ServiceNow bidirektional mit Status-Sync und
  Autoclose, persönliche Kanal-Präferenzen, Nachrichten-Templates pro Kanal, mehrstufige
  Eskalationsketten mit Quittierungs-Routing (F-04.x)
- **Frontend**: Event-/Alarm-Ansichten mit speicherbaren Filtervorlagen, Detailansicht mit
  vollständiger Historie, Regel-Tests mit Demo-/historischen Events, Dienstplan-Ansichten,
  unveränderbare Audit-Historie (F-05.x)
- **NFRs**: EU-Datenhaltung, DSGVO (Beauskunftung, Löschkonzept), RBAC mit verschachtelten
  Rollen und Entra-ID-Gruppen, SSO (OIDC/SAML; Entra ID, Keycloak), Security-by-Design,
  TLS nach Stand der Technik ohne Fallback, Mandantenarchitektur, korrekte HTTP-Semantik,
  OpenShift-Kompatibilität bzw. RHEL 9, responsive UI (Chrome/Edge, iOS), Antwortzeiten
  < 1 s, horizontale/vertikale Skalierung, planbare Batch-Jobs, Zero-Downtime-Design,
  Fallback-Benachrichtigung bei Eigenausfall, Backup/Recovery mit Self-Service-Restore,
  Audit/SIEM-Export, ISO-9241-Dialogprinzipien (A-15.x)

### 2.3 Schwächen des Referenzansatzes, die Northplane adressiert

| Beobachtung an CMP/Icinga-Generation | Northplane-Antwort |
|---|---|
| Suite aus ~12 separat lizenzierten Einzelprodukten mit je eigener UI (Webmin, Admin, Dashboard, Reports …) | Ein Produkt, eine UI, eine API; Module sind Feature-Flags, keine Produkte |
| Konfiguration dateibasiert mit Reload/Restart-Zyklen; IaC „nicht out-of-the-box, klären" | Konfiguration transaktional via API; deklarative Bundles + Terraform-Provider nativ |
| Alarmserver getrennt vom Monitoring (zwei Systeme, zwei Datenmodelle) | Eine Event-Pipeline: Check-Ergebnisse und externe Events fließen in dieselbe Alerting-Engine |
| UI-zentrierte Administration, API nachgerüstet und lückenhaft | API-first: UI ist beweisbar vollständig API-basiert (die UI nutzt ausschließlich die öffentliche API) |
| Keine AI-Integration | MCP-Server, Assistent, Korrelation, Anomalie-Erkennung als Kernbestandteil |
| Skalierung über manuell verwaltete Worker/Satelliten | Satelliten mit Auto-Registrierung, zentralem Config-Push und Store-and-Forward |

---

## 3. Produktvision & Leitprinzipien

### 3.1 Vision

> „Das Monitoring-System, das ein erfahrener Admin in 15 Minuten produktiv hat, das ein
> Konzern mandantenfähig und revisionssicher betreiben kann — und das ein AI-Agent
> genauso vollständig bedienen kann wie ein Mensch."

### 3.2 Leitprinzipien (verbindlich für alle Designentscheidungen)

**P1 — API ist das Produkt.** Jedes Feature wird als API-Ressource entworfen, bevor UI
entsteht. Die UI konsumiert ausschließlich die öffentliche, dokumentierte API (kein
internes „Privat-API"). Was nicht per API geht, existiert nicht.

**P2 — AI als gleichberechtigter, aber auditierter Akteur.** Alles, was die UI kann,
kann ein AI-Agent über MCP/REST — mit derselben RBAC, demselben Audit-Trail und
expliziten Approval-Gates für mutierende Aktionen. Kein Feature setzt ein LLM voraus.

**P3 — Ein Binary, wenige Abhängigkeiten.** `northplaned` ist ein statisches Go-Binary
inklusive UI, Migrationen, Docs. Externe Laufzeit-Abhängigkeiten: keine (SQLite-Modus)
bzw. genau eine (PostgreSQL im Server-Modus). Go-Dependencies unterliegen einer
strikten Policy (§7.9).

**P4 — Kompatibilität als Brücke, nicht als Käfig.** Nagios-Plugin-Protokoll, Perfdata,
NRPE und passive Checks werden vollständig unterstützt, damit Migration trivial ist.
Interne Modelle (Labels, Templates, Event-Pipeline) sind davon entkoppelt.

**P5 — Boring Technology, ehrliche Limits.** SQLite + eigene TSDB statt Kafka + Cluster-DB.
Wo etwas bewusst nicht gebaut wird (eigener SIP-Stack, eigenes ML-Framework), wird die
Integration spezifiziert statt die Illusion verkauft.

**P6 — Sicherheit und Nachvollziehbarkeit by Default.** TLS überall, Least-Privilege,
append-only Audit mit Hash-Verkettung, DSGVO-konforme Datenhaltung (EU), PII-Schutz vor
LLM-Aufrufen.

**P7 — Der Zustand des Monitorings ist selbst überwacht.** Selbst-Metriken, Watchdog,
Dead-Man-Switch zu externem Heartbeat — ein Monitoring, das stumm stirbt, ist wertlos
(vgl. A-15.24 Fallback-Benachrichtigung).

---

## 4. Ziele und Nicht-Ziele

### 4.1 Ziele (v1.0)

- G1: Vollständiger Ersatz eines Nagios/Icinga-Kerns für bis zu **10.000 Hosts /
  100.000 Services bei 60-s-Intervallen auf einer einzelnen Node** (8 vCPU / 16 GB)
- G2: Ausführung unmodifizierter Nagios-Plugins inkl. Perfdata-Übernahme in die TSDB
- G3: Event-Ingestion (HTTP/Webhook, passive Checks, Agent, E-Mail*, SNMP-Trap*) und
  regelbasierte Alarmierung mit mehrstufigen Eskalationsketten (*Phase 2)
- G4: On-Call-Verwaltung (Bereitschaftsgruppen, Rotationen, Overrides, Übergabe-Flows)
- G5: REST-API mit OpenAPI 3.1, SSE-Streams, Webhooks; CLI `np`; Config-Bundles
- G6: Eingebauter MCP-Server + AI-Assistent (Triage, Korrelation, NL-Query, NL-Config)
- G7: React-SPA (Dashboards, Problems-View, Objektverwaltung, On-Call-Kalender, Admin)
  + server-gerenderte öffentliche Status-Page
- G8: RBAC mit verschachtelten Rollen, OIDC-SSO (Entra ID, Keycloak), API-Tokens,
  Mandantenfähigkeit (architektonisch ab v1, UI-Vollausbau v1.x)
- G9: Verfügbarkeits-/SLA-Reports (HTML nativ, PDF via optionalem Renderer)
- G10: Migrations-Importer für bestehende Nagios/Icinga-Konfigurationen

### 4.2 Nicht-Ziele (v1, bewusst)

- N1: **Kein eigener SIP/RTP-Stack** für Sprachalarmierung — Voice via Provider-API
  (Twilio et al.) oder Hardware-/SIP-Gateway mit HTTP-Schnittstelle (§9.6)
- N2: **Kein IPAM-Modul** — Discovery liefert zwar Subnetz-/Geräteinformationen, aber
  keine IP-Verwaltung als Feature; Integrationspunkt zu NetBox/phpIPAM dokumentiert
- N3: **Kein Browser-Robot für E2E-Monitoring** — stattdessen: synthetische HTTP-Checks
  nativ + Ausführung von Playwright-/Selenium-Skripten über das Plugin-Protokoll (§8.6)
- N4: **Kein eigenes Log-Management** (kein Loki/Elastic-Ersatz) — Events ja, Logs nein
- N5: **Kein Metrik-Langzeitarchiv über 5 Jahre+** im Kern — Remote-Write-Export
  (Prometheus-Format) für externe LTS vorhanden
- N6: Kein selbst trainiertes ML-Modell — Anomalie-Erkennung ist deterministische
  Statistik (EWMA, MAD, Saisonalität); LLMs erklären, sie raten keine Schwellwerte ins Blaue
- N7: Keine Windows-Server-Variante des Servers (Agent: ja; Server: Linux only)

---

## 5. Personas & Kernszenarien

| Persona | Beschreibung | Top-Szenarien |
|---|---|---|
| **Ops-Engineer „Sandra"** | betreibt 3.000 Server, heute Icinga2 + Thruk | Migration per Importer; Problems-View; Ack/Downtime in < 3 Klicks; `np` CLI in Skripten |
| **Bereitschafts-Techniker „Murat"** | 24/7-Rufbereitschaft, mobil | Push/SMS mit Ack-Link; „Was ist betroffen?"-Impact-Ansicht; Übergabe per App/Anruf |
| **Plattform-Team „Verbund-Style"** | Konzern, Ausschreibungs-NFRs, OpenShift, Entra ID | SSO + RBAC-Gruppen-Mapping; Mandanten; Audit/SIEM; Config-as-Code im Git; Reports an Management |
| **MSP-Admin „Claudia"** | Managed Service Provider, 40 Kunden | Mandanten mit getrennten Sichten; Templates konzernweit; kundenindividuelle Eskalation |
| **AI-Agent „Klaus" (Claude)** | LLM-Agent via MCP | nächtliche Triage; Korrelation von Alarm-Stürmen; Downtime-Planung auf Zuruf; Config-Review |
| **Auditor:in** | Revision/DSGVO | lückenlose Alarm-Historie; Beauskunftungs-Export; Berechtigungs-Reports |

**Kernszenario „Alarm-Sturm, 03:12 Uhr":** Ein Core-Switch fällt aus, 240 Services
melden CRITICAL. Northplane: (1) Host-Dependency-Graph unterdrückt 230 Folge-Alarme
(UNREACHABLE statt DOWN), (2) Korrelations-Engine clustert die Rest-Events zu einem
Incident, (3) der AI-Layer benennt den Incident („Switch sw-core-02 down, 12 Standorte
betroffen") und verlinkt die Top-Verdachtsursache, (4) genau **eine** Eskalation läuft
an die Netzwerk-Bereitschaft, (5) Ack per Push-Link stoppt die Kette, (6) alles ist im
Audit-Log und im Incident-Zeitstrahl nachvollziehbar.

---

## 6. Domänenmodell

### 6.1 Objektmodell (vereinfachtes ER)

```
Tenant ─┬─< Folder (Hierarchie, Berechtigungsanker)
        ├─< Host ──< Service        (Check-Objekte)
        ├─< Template (host|service|command, Vererbungskette)
        ├─< CheckCommand (exec|builtin|agent|passive)
        ├─< TimePeriod
        ├─< EventSource (Ingress-Adapter-Instanz)
        ├─< AlertRule / AlertGroup
        ├─< EscalationPolicy ──< EscalationStep
        ├─< Schedule (On-Call) ──< Rotation / Override
        ├─< Contact / ContactGroup (↔ User, ↔ IdP-Gruppen)
        ├─< NotificationChannel (email|sms|voice|webhook|teams|slack|push|…)
        ├─< Silence / Downtime
        ├─< BusinessService (BPI-Baum, Impact-Regeln)
        ├─< Dashboard / Report / ReportSchedule
        └─< ApiToken / Role / AuditEntry
Host/Service ──< CheckResult (State-Historie)
            ──< MetricSeries ──< Samples (TSDB)
Event ──(Korrelation)──> Alert ──> Incident
```

### 6.2 Host & Service

Nagios-kompatibel im Verhalten, modern in der Verwaltung:

```yaml
# Beispiel: deklaratives Bundle-Fragment (np apply)
kind: Host
metadata:
  name: db-prod-01.example.net
  folder: /prod/wien
  labels: { env: prod, role: postgres, site: wien, owner: team-data }
spec:
  address: 10.20.1.15
  templates: [linux-base, postgres-server]
  parents: [sw-core-02]                    # Reachability-Graph
  checkCommand: builtin:icmp
  interval: 60s
  retryInterval: 15s
  maxCheckAttempts: 3
  notificationPeriod: 24x7
  vars:                                    # Custom-Makros ($_HOST…$)
    ssh_port: 22
---
kind: Service
metadata:
  name: postgres-connections
  host: db-prod-01.example.net
  labels: { tier: database }
spec:
  checkCommand: exec:check_postgres        # Nagios-Plugin
  args: ["--action=backends", "--warning=80%", "--critical=95%"]
  interval: 120s
  thresholdMode: static                    # static | adaptive (AI-Baseline, §10.6)
```

- **Identität:** UUIDv7 intern; `name` + `tenant` eindeutig; Hosts adressierbar über
  Name, Adresse oder Label-Selektor.
- **Labels** sind der primäre Gruppierungsmechanismus (`env=prod, site=wien`).
  Klassische Host-/Servicegruppen existieren als **gespeicherte Label-Selektoren**
  (dynamisch) oder statische Listen (für Nagios-Import-Treue).
- **Templates** mit Mehrfachvererbung in deklarierter Reihenfolge (wie Nagios `use`),
  zyklenfrei validiert; effektive Konfiguration ist per API einsehbar
  (`GET …/effective-config`) — kein Rätselraten über Vererbungsergebnisse.
- **Folder-Hierarchie** ist Berechtigungs- und Organisationsanker (RBAC-Scope),
  unabhängig von Labels.

### 6.3 Zustandsmodell (Nagios-semantisch)

- Service-States: `OK(0) WARNING(1) CRITICAL(2) UNKNOWN(3)`;
  Host-States: `UP DOWN UNREACHABLE` (abgeleitet 0/1/2).
- `soft`/`hard` State mit `max_check_attempts` und `retry_interval` — identisch zu
  Nagios, inklusive Recovery-Sonderfall (Recovery ist immer sofort hard).
- **Flapping-Erkennung**: gewichtete Zustandswechselrate über die letzten 21 Checks,
  Schwellen 25 %/50 % (konfigurierbar), Flapping unterdrückt Notifications, nicht Checks.
- **Reachability**: fällt ein Parent-Host, werden Kinder `UNREACHABLE`;
  Notifications dafür sind getrennt steuerbar (Default: aus).
- **Freshness**: für passive/Agent-Checks definierbar (`stalenessAfter: 300s` →
  Übergang in UNKNOWN mit konfigurierbarem Text — entspricht Nagios freshness checking).
- **Downtimes**: fixed & flexible, mit Trigger-Verkettung (Downtime löst Kind-Downtimes
  aus), wiederkehrend (RRULE-Subset); **Acknowledgements**: sticky, persistent,
  mit Kommentar und optionalem Ablauf (`expiresAt`).

### 6.4 Event, Alert, Incident

| Entität | Bedeutung | Quelle |
|---|---|---|
| **Event** | Unveränderliches Faktum („Check X ging CRITICAAL→OK", „Webhook von Quelle Y", „Trap empfangen") | Checks, Ingress-Adapter, System |
| **Alert** | Bewerteter, zustandsbehafteter Vorfall (offen/quittiert/geschlossen), erzeugt durch Alert-Rules über Events | Alerting-Engine |
| **Incident** | Gebündelte Alerts (manuell oder durch Korrelation), Träger von Timeline, Impact, AI-Zusammenfassung, externem Ticket-Link | Korrelation / Mensch / AI |

Events sind append-only (Audit-tauglich); Alerts referenzieren auslösende Events;
Incidents referenzieren Alerts. Diese Dreiteilung bildet sowohl klassische
Monitoring-Notifications als auch die Alarmserver-Welt (externe Events) **in einer
Pipeline** ab — der zentrale Strukturvorteil gegenüber der CMP-Zweiteilung.

### 6.5 SQL-Schema (Auszug, SQLite/PostgreSQL-kompatibel)

```sql
CREATE TABLE objects (            -- Hosts & Services unifiziert
  id          TEXT PRIMARY KEY,   -- UUIDv7
  tenant_id   TEXT NOT NULL,
  kind        TEXT NOT NULL CHECK (kind IN ('host','service')),
  name        TEXT NOT NULL,
  host_id     TEXT REFERENCES objects(id),     -- für Services
  folder      TEXT NOT NULL DEFAULT '/',
  labels      TEXT NOT NULL DEFAULT '{}',      -- JSON, indiziert über Hilfstabelle
  spec        TEXT NOT NULL,                   -- JSON (validiert gegen Schema)
  version     INTEGER NOT NULL DEFAULT 1,      -- Optimistic Locking / ETag
  created_at  TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (tenant_id, kind, host_id, name)
);
CREATE TABLE object_labels (object_id TEXT, k TEXT, v TEXT,
  PRIMARY KEY (k, v, object_id));              -- Selector-Index

CREATE TABLE check_state (        -- aktueller Zustand, heiß
  object_id   TEXT PRIMARY KEY REFERENCES objects(id),
  state       INTEGER NOT NULL, state_type TEXT NOT NULL,  -- soft|hard
  attempt     INTEGER NOT NULL,
  output      TEXT, long_output TEXT, perfdata TEXT,
  latency_ms  INTEGER, exec_ms INTEGER,
  last_check  TEXT, next_check TEXT,
  flapping    INTEGER NOT NULL DEFAULT 0,
  acked_by    TEXT, downtime_depth INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE events (             -- append-only
  id          TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL,
  ts          TEXT NOT NULL,
  type        TEXT NOT NULL,      -- state_change|notification|ingress|config|ai_action|…
  object_id   TEXT, source_id TEXT,
  severity    TEXT,
  payload     TEXT NOT NULL       -- JSON
);
CREATE INDEX events_ts ON events (tenant_id, ts);

CREATE TABLE alerts (
  id          TEXT PRIMARY KEY, tenant_id TEXT NOT NULL,
  rule_id     TEXT, object_id TEXT, incident_id TEXT,
  status      TEXT NOT NULL,      -- open|acked|resolved|expired
  severity    TEXT NOT NULL,
  title       TEXT NOT NULL, dedup_key TEXT,
  opened_at   TEXT NOT NULL, acked_at TEXT, resolved_at TEXT,
  payload     TEXT NOT NULL
);
CREATE UNIQUE INDEX alerts_dedup ON alerts (tenant_id, dedup_key)
  WHERE status IN ('open','acked');

CREATE TABLE audit_log (          -- hash-verkettete Revisionskette, §13.5
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          TEXT NOT NULL, tenant_id TEXT,
  actor_type  TEXT NOT NULL,      -- user|token|ai_agent|system
  actor_id    TEXT NOT NULL,
  action      TEXT NOT NULL, resource TEXT,
  before_json TEXT, after_json TEXT,
  prev_hash   TEXT NOT NULL, hash TEXT NOT NULL
);
```

**Dialekt-Politik:** eine logische Schemadefinition, daraus generierte DDL je Backend
(SQLite: `AUTOINCREMENT` / RFC-3339-`TEXT` / json1 — PostgreSQL: `BIGINT GENERATED
ALWAYS AS IDENTITY` / `timestamptz` / `jsonb`); DML strikt im gemeinsamen Subset
(`ON CONFLICT`, partielle Indizes, CTEs, `RETURNING` — beide Engines können alles
Benötigte). Kein Backend-Exklusiv-Feature im Kernpfad; einzige additive Ausnahme:
`LISTEN/NOTIFY` im Multi-Node-Betrieb (§7.8).

---

## 7. Systemarchitektur

### 7.1 Komponentenübersicht

```
                                    ┌──────────────────────────────────────────┐
                                    │  northplaned  (ein statisches Go-Binary) │
   Browser ── HTTPS ──────────────► │  ┌────────┐ ┌─────────┐ ┌─────────────┐  │
   (React-SPA aus go:embed)         │  │ HTTP/  │ │ Alerting│ │  Scheduler   │  │
                                    │  │ API +  │ │ Engine  │ │  + Executor  │  │
   np (CLI) ── HTTPS ─────────────► │  │ SSE    │ │ +Escal. │ │  (Worker-    │  │
   Terraform ── HTTPS ────────────► │  └────────┘ └─────────┘ │   Pools)     │  │
   AI-Agent ── MCP (stdio/HTTP) ──► │  ┌────────┐ ┌─────────┐ └─────────────┘  │
                                    │  │ Ingress│ │ Notifier│ ┌─────────────┐  │
   Webhooks/Events ── HTTPS ──────► │  │ Adapter│ │ (Kanäle)│ │ AI-Layer +  │  │
   Agents/Satellites ── mTLS ─────► │  └────────┘ └─────────┘ │ MCP-Server  │  │
                                    │  ┌──────────────────────┴──────────────┐ │
                                    │  │ Storage: SQLite (Config/State/Events│ │
                                    │  │ /Audit) + NP-TSDB (Metriken)        │ │
                                    │  │ [alternativ: PostgreSQL, vgl. §7.3] │ │
                                    │  └─────────────────────────────────────┘ │
                                    └──────────────────────────────────────────┘
        ▲ mTLS, Store-and-Forward                      │ SMTP, HTTPS (Provider),
┌───────┴───────┐   ┌────────────────┐                 ▼ Teams/Slack/SMS/Voice…
│ np-satellite  │   │ np-agent       │          externe Kanäle & Systeme
│ (Zonen-Poller,│   │ (Inventar,     │          (ServiceNow, Git, SIEM/Syslog,
│  gleiches     │   │  Metriken,     │           Prometheus Remote-Write)
│  Binary)      │   │  lokale Checks)│
└───────────────┘   └────────────────┘
```

**Artefakte:**

| Binary | Rolle | Größe (Ziel) |
|---|---|---|
| `northplaned` | Server; via Subcommand auch `satellite`, `mcp`, `migrate`, `storage migrate`, `import nagios`, `backup` | < 80 MB inkl. UI |
| `np` | CLI für Mensch & Automatisierung (gleiches Repo, eigenes kleines Binary) | < 20 MB |
| `np-agent` | Host-Agent (Linux, Windows, macOS; amd64/arm64) | < 15 MB |

### 7.2 Prozess- & Nebenläufigkeitsmodell

- Ein Prozess, strukturierte Goroutine-Bäume mit Context-Cancellation; Subsysteme
  (Scheduler, Executor-Pools, Ingress, Notifier, SSE-Hub, AI-Queue) als supervisierte
  Services mit Health-Status (`/readyz` aggregiert).
- Interne Kommunikation über typisierte In-Memory-Queues (channels) mit definierter
  Backpressure-Policy: Check-Ergebnisse > Events > Notifications > AI-Jobs;
  bei Überlast werden AI-Jobs zuerst verworfen, Check-Verarbeitung nie.
- Alles Persistente geht durch eine einzige Write-Serialisierung pro Storage
  (SQLite-WAL-freundlich); Reads sind parallel (WAL: readers don't block writer).

### 7.3 Storage-Architektur

**Konfig/State/Events/Audit — relational, zwei gleichwertige Backends:**

Beide Backends bedienen dieselbe schmale Storage-Schnittstelle (`database/sql`, kein
ORM) und durchlaufen ab M0 dieselbe Integrations-Suite (§16). PostgreSQL ist also
kein nachgerüsteter „HA-Sonderfall", sondern von Tag 1 erster Klasse — die Wahl ist
eine **Betriebsentscheidung, keine Architekturentscheidung**:
`northplaned storage migrate --to <dsn>` kopiert den relationalen Bestand in beide
Richtungen (offline; Downtime = Kopierzeit; die NP-TSDB ist backend-unabhängig und
bleibt unberührt).

- **SQLite — Embedded-Default.** Pure-Go-Treiber `modernc.org/sqlite` (kein CGO), WAL,
  `synchronous=NORMAL`, eine Write-Goroutine je Datei mit Batch-Commits (§7.4).
  Begründung: keine externe Abhängigkeit (P3), 15-Minuten-Setup, Backup = Datei + WAL.
  Die G1-Checklast erzeugt nach Pipeline-Batching < 2.000 Zeilen/s in 4–8 Commits/s —
  weit innerhalb der WAL-Komfortzone. Dateilayout: `core.db` (Config, State, Alerts,
  Audit) + **zeitpartitionierte Event-Segmente** (`events-YYYYMM.db`, per ATTACH
  abgefragt): Retention = Segmentdatei löschen statt Massen-DELETE + VACUUM (ADR-13);
  lange Report-Reads auf Alt-Segmenten blockieren keine Checkpoints der heißen Datei.
  (WAL macht Multi-File-Transaktionen nicht atomar — bewusst akzeptiert: Commit-Folge
  ist Event vor State, und `check_state` heilt spätestens im nächsten Check-Zyklus.)
- **PostgreSQL ≥ 15 — Server-Modus.** Treiber `jackc/pgx/v5` (ohne ORM); Aktivierung
  allein per `storage.dsn`; funktional identisch. Events als native Range-Partitionen
  (monatlich; Retention = `DROP PARTITION`); zusätzlich — nur im Multi-Node-Betrieb —
  `LISTEN/NOTIFY` für Event-Fanout an Follower (§7.8). DB-Betrieb (HA, PITR-Backup)
  liegt beim Betreiber: in Konzern-Umgebungen ein Feature, kein Mangel — die
  DB-Plattform existiert dort bereits samt DBA-Prozessen und Backup-Regimes.

| Profil | Empfohlenes Backend |
|---|---|
| Eval/POC, Einzelinstanz bis G1, Edge-/MSP-Box, einfachster Betrieb; RPO ≤ 5 min (WAL-Shipping) akzeptiert | **SQLite** (Default) |
| HA/Multi-Node (Pflicht), OpenShift-/Konzern-Profil mit DB-Plattform (A-15.18/22/24), anhaltende Ingress-Raten ⪆ 50 Events/s, Event-Retention ≫ 90 d, externe SQL-Lesezugriffe (BI) | **PostgreSQL** |

Der 50/s-Richtwert ist Betriebs-, nicht Engine-Ökonomie: 50 Events/s ≈ 4,3 Mio.
Zeilen/Tag ≈ O(100 GB) je 90-Tage-Retention — als Einzeldateien zunehmend unhandlich,
als PG-Partitionstabellen Routine. Die Check-Last selbst (G1) ist für beide trivial.
Was Monitoring-Systeme klassisch an ihrer Datenbank scheitern lässt, sind **Metriken
in der relationalen DB** (Zabbix-History-Problem) — das tut Northplane per Design nie
(NP-TSDB, unten).

- Dialekt-Disziplin: gemeinsames DML-Subset, generierte DDL je Backend (§6.5), keine
  Backend-Exklusiv-Pfade im Kern. Die reale „Doppel-Backend-Steuer" wird so klein
  gehalten und durch die CI-Matrix (§16) dauerhaft bezahlt statt aufgeschoben.
- Migrationen: nummerierte, eingebettete SQL-Dateien (je Dialekt aus einer Quelle
  generiert), vorwärts-only, mit Schema-Versionstabelle und Startup-Gate.

**Metriken — NP-TSDB (eigene Engine, pure Go):**

- Append-only **2-h-Chunks** pro Serie; Encoding: Delta-of-Delta-Timestamps +
  XOR-Float-Werte (Gorilla-Schema) → Ziel ≤ 2 Bytes/Sample real.
- Serien-Identität: `(object_id, metric, unit, labels-hash)`; Perfdata-Parser erzeugt
  Serien automatisch aus Plugin-Output (§8.3).
- **Downsampling-Tiers** (konfigurierbar): raw 30 d → 5-min-Aggregate
  (min/max/avg/sum/count) 400 d → 1-h-Aggregate 5 a. Kompaktierung als nächtlicher,
  drosselbarer Batch-Job (A-15.23: planbar, extern anstoß-/überwachbar).
- Query-Pfad: mmap-Reads, Zeitbereich + Aggregation + Group-by-Labels; API liefert
  bereits render-fertige Serien (Downsampling auf Pixelbreite serverseitig).
- **Kein PromQL** in v1 (bewusst, N5); dafür: OpenMetrics-Endpoint für
  Selbst-Monitoring und **Prometheus Remote-Write-Export** für Langzeit/Grafana.
- Crash-Sicherheit: Chunks werden via WAL-Datei (eigenes, simples Redo-Log) gehärtet;
  fsync-Batching 1 s.

**Speicherkalkulation (G1):** 100k Services × Ø 4 Serien × 1 Sample/60 s ≈ 6,7k
Samples/s ≈ < 14 KB/s komprimiert ≈ **~1,2 GB/Tag raw**; mit Tiers ≈ 45 GB für die
volle Retention — unkritisch für eine Single-Node.

### 7.4 Scheduler & Check-Execution

- **Zeitrad** (hierarchical timing wheel) mit deterministischem **Splay**: Start-Offsets
  werden aus `hash(object_id)` abgeleitet → gleichmäßige Lastverteilung, keine
  Check-Stürme nach Restart; Intervalle 1 s bis 24 h.
- **Worker-Pools** pro Check-Klasse:
  - `builtin` (in-process, Go-nativ: icmp, tcp, http(s), tls-cert, dns, snmp-get/walk,
    smtp, imap, ntp, ssh-banner, cert-Expiry, http-Multistep) — kein Fork, 10k+
    parallel problemlos;
  - `exec` (Nagios-Plugins): begrenzter Pool (Default `min(256, 32×vCPU)`), pro Check
    Timeout (Default 30 s, hart via `context` + Prozessgruppen-Kill), RSS-Limit,
    gemessene Latenz/Dauer als Meta-Metrik;
  - `agent`/`passive`: kein Scheduling, nur Freshness-Überwachung.
- Ergebnis-Pipeline: `Result → StateMachine → (state_change? → Event) → TSDB-Ingest →
  SSE-Fanout` — Batch-Commits alle 250 ms oder 500 Ergebnisse.
- **On-Demand-Recheck** (`POST …/check-now`) mit Priority-Lane.
- Satelliten-Checks: identische Semantik, Ausführung remote (§7.7).

### 7.5 Ingress-Adapter (externe Events — „Alarmserver-Erbe")

Einheitliches Adapter-Framework; jede Quelle wird zur `EventSource` mit eigener
Authentifizierung, Normalisierungs-Mapping (CEL-Ausdrücke, §9.2) und Rate-Limit:

| Adapter | Transport | Phase |
|---|---|---|
| **HTTP/Webhook** | `POST /api/v1/ingest/{source}` (HMAC/Token/Basic), beliebiges JSON; Mapping auf Normform | v1 |
| **Passive Checks** | `POST /api/v1/results` (Nagios-kompatible Felder) | v1 |
| **Agent-Events** | mTLS-Stream vom np-agent | v1 |
| **Prometheus Alertmanager** | kompatibler `/api/v2/alerts`-Receiver → Alerts aus bestehender Prom-Welt | v1 |
| **E-Mail** | IMAP-Poller (OAuth2/Graph & Basic); Parser-Regeln → Event | v2 |
| **SNMP-Traps** | Trap-Receiver (v2c/v3) mit MIB-freiem OID-Mapping + optionalen MIB-Paketen | v2 |
| **Heartbeat/Dead-Man** | `GET/POST /api/v1/heartbeats/{id}` — Ausbleiben erzeugt Event (F-02.02 „kein Event in X min") | v1 |
| **MQTT** | Subscriber-Bridge | v3 |
| **DB-Polling** (PostgreSQL/MSSQL/Oracle) | Query-Adapter mit Cursor-State | v3 |
| **SMS/Voice-Inbound** | über Gateway-Provider-Webhooks (kein eigener GSM/SIP-Stack) | v2 |

Alle Adapter erzeugen dieselbe **Normform**:

```json
{
  "source": "es_7f3a…", "receivedAt": "2026-06-07T03:12:09Z",
  "dedupKey": "switch-sw-core-02-linkdown",
  "severity": "critical",
  "summary": "Link down on sw-core-02 Gi1/0/48",
  "labels": { "site": "wien", "device": "sw-core-02" },
  "payload": { "...": "Originaldaten, unverändert archiviert" }
}
```

### 7.6 Realtime-Verteilung

- **SSE-Hub** (`GET /api/v1/stream?filter=…`): Server-Sent Events mit `Last-Event-ID`
  Resume, Heartbeat-Kommentaren (15 s), per-Subscription-Filter (Label-Selektor,
  Event-Typen, Tenant-Scope durch RBAC erzwungen). Begründung SSE statt WebSocket:
  unidirektional reicht (Mutationen laufen über REST), Proxy-/LB-freundlich,
  Auto-Reconnect im Browser eingebaut, triviale Implementierung ohne Dependency.
- Fanout-Ziel: 500 gleichzeitige UI-Clients, Drop-Policy: langsame Clients erhalten
  `event: resync`-Hinweis statt unbegrenzter Pufferung.

### 7.7 Satelliten (verteiltes Monitoring)

- `northplaned satellite --zone=wien-dc2 --server=https://…` — gleiches Binary.
- **Registrierung:** einmaliges Join-Token → CSR → Server-signiertes mTLS-Zertifikat
  (eigene interne CA, Rotation 90 d, automatisch).
- **Config-Push:** Satellit erhält den ihm zugeordneten Objekt-Teilbaum (Zone-Label)
  als versioniertes Snapshot-Bundle; Delta-Sync via Long-Poll/SSE.
- **Store-and-Forward:** Ergebnisse werden lokal gepuffert (Ring-Datei, Default 24 h)
  und nach Reconnect lückenlos nachgeliefert; Zeitstempel entstehen am Satellit.
- Zonen-Failover: zwei Satelliten je Zone möglich (aktiv/standby via Server-Lease).
- Sicherheits-Asymmetrie: Satellit authentisiert sich beim Server, nie umgekehrt;
  Server erreicht Satellit nicht aktiv (NAT-freundlich, nur ausgehende Verbindungen).

### 7.8 Hochverfügbarkeit & Skalierung

| Modus | Topologie | Eigenschaften |
|---|---|---|
| **Single** (Default) | 1 × `northplaned`, SQLite **oder** PostgreSQL + NP-TSDB lokal | RTO ≈ Restore-Zeit (< 15 min, §15.4), RPO ≤ 5 min via kontinuierlichem WAL-Backup (SQLite) bzw. DB-seitigem PITR (PostgreSQL); für die meisten Installationen die richtige Wahl |
| **HA** | 2+ × `northplaned` (stateless bzgl. Config/State) + PostgreSQL (extern, kundenseitig HA) + Shared-Nothing-TSDB mit Replikations-Stream zwischen Nodes | Leader-Election via DB-Lease (Scheduler/Notifier laufen nur auf Leader; API/UI/SSE auf allen Nodes — Follower beziehen Events für ihre SSE-Clients via `LISTEN/NOTIFY`, Fallback Cursor-Polling); Zero-Downtime-Upgrades durch rollierende Neustarts (A-15.24) |
| **Scale-out Checks** | beliebig viele Satelliten | 100k+ Checks durch Zonen-Sharding |

Vertikal: Worker-Pools, Batch-Größen, Cache-Limits konfigurierbar; horizontal:
Satelliten + HA-Read-Replicas für API-Last. Eine „Cluster-TSDB" wird bewusst nicht
gebaut (P5) — im HA-Modus schreibt der Leader die TSDB und streamt Chunks an Follower.

### 7.9 Dependency-Politik (Go)

**Regel: stdlib zuerst; jede Dependency braucht einen ADR-Eintrag mit Exit-Strategie.**

| Kategorie | Entscheidung |
|---|---|
| HTTP-Routing/Server | `net/http` (Go ≥ 1.22 ServeMux: Methoden + Wildcards) — **kein** gin/echo/chi |
| Logging | `log/slog` (JSON-Handler) |
| Metrics (self) | eigene Registry, OpenMetrics-Text-Export (~300 Zeilen) |
| SQLite | `modernc.org/sqlite` (pure Go, CGO-frei) — Begründung: statisches Cross-Compiling, keine libc-Kopplung |
| PostgreSQL | `jackc/pgx/v5` (nur stdlib-Adapter-Nutzung) |
| OIDC/OAuth2 | `golang.org/x/oauth2` + `coreos/go-oidc` (JOSE-Validierung selbst zu bauen wäre fahrlässig) |
| SAML (v2, falls gefordert) | `crewjam/saml`, hinter Build-Tag |
| SNMP | `gosnmp/gosnmp` |
| Krypto | stdlib + `golang.org/x/crypto` (argon2id, acme/autocert optional) |
| YAML (Bundles) | `goccy/go-yaml` oder `gopkg.in/yaml.v3` (eine, nicht beide) |
| CEL (Regel-Ausdrücke) | `google/cel-go` — ADR-08: mächtige, aber sandboxte Ausdruckssprache; Alternative (eigene Mini-DSL) verworfen wegen Fehleranfälligkeit |
| MCP | `modelcontextprotocol/go-sdk` (offizielles SDK) |
| **Verboten** | ORMs, DI-Frameworks, Web-Frameworks, Message-Broker im Kern, cgo-Pflicht-Pakete |

Frontend analog: shadcn/ui wird **vendored** (Code im Repo, kein npm-Paket) — Updates
sind bewusste Diffs; Runtime-Dependencies klein halten (§12.2).

---

## 8. Nagios-Kompatibilität im Detail

> Ziel: Ein Admin nimmt sein `check_…`-Verzeichnis und seine NRPE-Clients mit —
> ohne ein einziges Plugin anzufassen. (Monitoring-Plugins-Spezifikation als Norm.)

### 8.1 Plugin-Protokoll (Execution)

- Exit-Codes `0/1/2/3 → OK/WARNING/CRITICAL/UNKNOWN`; > 3 oder Signal/Timeout → UNKNOWN
  mit diagnostischem Output (Timeout wird als solcher ausgewiesen).
- stdout: erste Zeile = Status-Text; `|` trennt Perfdata; Folgezeilen = Long Output;
  Perfdata-Fortsetzung in Folgezeilen wird unterstützt (Multiline-Perfdata).
- stderr wird erfasst und im Check-Detail angezeigt (nicht Teil des Status-Texts).
- Max-Output 64 KB (konfigurierbar), UTF-8 mit Latin-1-Fallback-Erkennung.

### 8.2 Makros & Argumente

- Argument-Substitution `$ARG1$…$ARG32$` in CheckCommands; Standard-Makros:
  `$HOSTNAME$ $HOSTADDRESS$ $HOSTSTATE$ $SERVICEDESC$ $SERVICESTATE$ $SERVICEOUTPUT$
  $LONGSERVICEOUTPUT$ $SERVICEPERFDATA$ $TIMET$ $SHORTDATETIME$ …` (dokumentierte
  Teilmenge: alle host-/service-/notification-bezogenen; ausgenommen: summary-Makros
  der Nagios-Gesamtstatistik).
- Custom Variables: `vars.foo` → `$_HOSTFOO$` / `$_SERVICEFOO$`.
- Zusätzlich werden Makros als **Environment-Variablen** exportiert
  (`NAGIOS_HOSTADDRESS=…` und `NORTHPLANE_…`-Aliase) — schaltbar pro Command
  (Env-Injection kostet messbar bei hohen Raten).
- Secrets in Argumenten: `$SECRET:name$` löst aus dem verschlüsselten Secret-Store auf
  und wird in Logs/UI maskiert (Verbesserung gegenüber `resource.cfg`-Klartext).

### 8.3 Perfdata → Metriken

Vollständiger Parser der Perfdata-Grammatik:
`'label'=value[UOM];[warn];[crit];[min];[max]` mit UOM-Normalisierung
(`us|ms|s → s`, `B|KB|MB|GB|TB → bytes`, `%`, `c` = Counter mit Rate-Ableitung).
Warn/Crit-Ranges (`@`-Inversion, `start:end`) werden mitgeführt und in Charts als
Schwellenbänder gerendert. Fehlertolerant: kaputte Perfdata erzeugen Parse-Warnung
(Meta-Metrik), nie Check-Fehler.

### 8.4 Remote-Ausführung

- **NRPE-Client** eingebaut (`builtin:nrpe`): v2/v3/v4-Paketformat, TLS inkl.
  Zertifikatsprüfung (anders als klassisches check_nrpe-Default), Timeout-Mapping.
- `check_by_ssh`-Äquivalent (`builtin:ssh-exec`) mit Connection-Pooling/Multiplexing.
- **np-agent als moderner NRPE-Ersatz**: führt lokale Nagios-Plugins aus
  (`agent:exec:check_disk …`), pusht Ergebnisse über die bestehende mTLS-Verbindung —
  keine eingehenden Ports am Zielsystem, zentrale Plugin-Verteilung optional
  (signierte Plugin-Pakete).
- SNMP nativ (`builtin:snmp`), inkl. Tabellen-Walks und Threshold-Auswertung.

### 8.5 Passive Checks & externe Kommandos

- `POST /api/v1/results` akzeptiert Einzel- und Batch-Submission; Felder kompatibel zu
  NSCA-Semantik (`host`, `service`, `state`, `output`); Authentifizierung via
  API-Token; optionales HMAC für Submitter ohne TLS-Terminierungs-Vertrauen.
- **Kompatibilitäts-Shim** `np nsca-bridge`: lauscht NSCA-Protokoll (klassisch + ng),
  übersetzt auf die REST-API — für Bestandssender während der Migration.
- External-Command-Pipe wird **nicht** emuliert (Datei-Pipe = Sicherheits-Altlast);
  der Importer erkennt cmd-Pipe-Nutzer und schlägt API-Äquivalente vor
  (`PROCESS_SERVICE_CHECK_RESULT → POST /results`, `SCHEDULE_DOWNTIME → POST /downtimes` …).

### 8.6 E2E/Synthetic über das Plugin-Protokoll

Browser-Robotik (CMP-E2E-Äquivalent) wird nicht im Kern implementiert (N3), aber:
`exec`-Checks mit erhöhtem Timeout-Profil (bis 10 min) + Artefakt-Upload
(`NORTHPLANE_ARTIFACT_DIR`): Ein Playwright-Skript legt Screenshots/HAR dort ab,
Northplane versioniert sie am Check-Ergebnis (Retention konfigurierbar) — damit sind
Login-Flows etc. überwachbar, ohne dass der Kern einen Browser mitschleppt.
Native Multistep-HTTP-Checks (`builtin:http-flow`: Sequenz, Variablen-Extraktion,
Assertions, Timing pro Schritt) decken die DEM-Basisfälle ab.

### 8.7 Migrations-Importer

`northplaned import nagios --path /etc/icinga2|/etc/nagios` parst Objekt-Konfiguration
(nagios.cfg-Dialekt **und** Icinga2-DSL-Teilmenge), erzeugt ein Config-Bundle +
**Abweichungsbericht**: jede nicht mappbare Direktive wird gelistet (z. B.
`obsess_over_services`) mit Empfehlung. Ziel: 95 % typischer Setups automatisch,
Rest dokumentiert. Hostgruppen → statische Gruppen + Label-Vorschläge
(`hostgroup "linux-servers" → label os=linux`, heuristisch, als Review-Diff).

---

## 9. Alerting, Eskalation & On-Call

### 9.1 Pipeline

```
Events (Checks + Ingress) 
  → Routing/Klassifikation (CEL: Labels, Severity, Extraktion)      [F-02.01]
  → Alert-Rules (Zustand, Dedup, Pending, Autoclose, Heartbeat)     [F-02.02]
  → Gruppierung/Korrelation (Alert-Groups, Topologie, Zeitfenster)  [F-02.03]
  → Suppression (Downtimes, Silences, Flapping, Reachability)       [F-02.04]
  → Eskalations-Engine (Policies, Steps, Quittierungs-Routing)      [F-04.10/11]
  → Notifier (Kanäle, Templates, Retry/DLQ, Präferenzen)            [F-04.x]
```

### 9.2 Regeln (CEL-basiert)

```yaml
kind: AlertRule
metadata: { name: disk-critical-prod }
spec:
  match: event.type == "state_change" && event.labels.env == "prod"
         && event.state == "CRITICAL" && event.metric == "disk"
  pendingFor: 5m            # Alarm erst nach 5 min ununterbrochen kritisch
  dedupKey: '{{ .object.id }}/disk'
  severity: critical
  autoCloseAfter: 24h
  escalationPolicy: prod-infra
---
kind: AlertRule
metadata: { name: backup-heartbeat }
spec:
  heartbeat: { source: es_backupjob, expectEvery: 26h }   # „kein Event in X"
  severity: warning
```

- CEL ist sandboxed (keine I/O, Kostenlimit) und **testbar**: 
  `POST /api/v1/alert-rules/{id}:test` evaluiert gegen Demo-Events **oder einen
  historischen Zeitbereich** und liefert die hypothetischen Alarme + Eskalationsschritte
  (F-05.04 — im Referenzprodukt nur „zu schärfen", hier Kernfeature).
- Aggregation auf Gruppenebene: count/min/max/avg/median über Alarm-Werte (F-02.03).
- Suppression: Wartungsfenster (geplant, wiederkehrend), Ad-hoc-Silences mit
  Label-Selektor + Regex auf Event-Text, TTL-Pflicht (kein „für immer vergessen").

### 9.3 Korrelation & Incidents

Deterministische Stufen **vor** jeder AI:
1. **Topologie**: Parent-Down ⇒ Kind-Alarme unterdrückt (Reachability);
   BusinessService-Baum aggregiert Impact (§9.7).
2. **Sturm-Clusterung**: Alarme im 120-s-Fenster mit gemeinsamem dominanten Label
   (site/device/rule) werden zu einem Incident-Vorschlag gebündelt (Schwellwert
   konfigurierbar, Default ≥ 5).
3. **Flap-/Wiederkehr-Erkennung** auf Alert-Ebene (öffnet/schließt < 3×/h ⇒ Hinweis).

AI ergänzt (nie ersetzt) diese Stufen: Benennung, Ursachen-Hypothese, Zusammenfassung (§10).

### 9.4 Eskalationsketten

```yaml
kind: EscalationPolicy
metadata: { name: prod-infra }
spec:
  steps:
    - after: 0m
      notify: { schedule: netz-bereitschaft }        # wer gerade Dienst hat
      channels: [push, sms]                          # Override der Präferenz möglich
    - after: 10m
      unlessAcked: true
      notify: { schedule: netz-bereitschaft, escalateTo: backup }   # zweite Person
      channels: [voice]
    - after: 25m
      unlessAcked: true
      notify: { contactGroup: noc-leitung }
      repeatEvery: 30m
      maxRepeats: 4
    - after: 30m
      action: { servicenow: { assignmentGroup: NOC, autoClose: true } }  # F-04.05
```

- Quittierung über: UI, API, signierten Ack-Link (E-Mail/SMS/Teams — funktioniert ohne
  Login, einmalig, ablaufend), App-Push-Action, Voice-DTMF (providerabhängig).
- Ack stoppt die Kette; konfigurierbar: Ack-Routing („wenn nicht quittiert →
  nächste Person", F-04.11), Re-Eskalation bei `unack` oder Severity-Anstieg.
- Jeder Schritt erzeugt Events (auditierbare Kette: wer wurde wann worüber
  benachrichtigt, Zustellstatus, F-05.03/09).

### 9.5 On-Call (Bereitschaft)

- **Schedules** mit Layern: Rotationen (daily/weekly/custom Länge, Startanker,
  Teilnehmerreihenfolge = „Bereitschaftsrad", F-03.02), zeitliche Einschränkungen
  (nur 19:00–07:00 + Wochenende), **Overrides** (Urlaub/Tausch, F-03.03).
- **Ad-hoc-Übernahme**: per UI/API; per SMS/Anruf-Inbound (v2, Provider-Webhook) mit
  Stimm-/Absender-Verifikation gegen hinterlegte Nummern.
- Benachrichtigung bei Dienstbeginn/-ende über persönlichen Präferenzkanal (F-03.04);
  **ICS-Feed** pro Schedule und pro Person (Kalender-Abo); „Wer hat Dienst?"-Widget
  + API (`GET /oncall/now?schedule=…`).
- Bereitschafts-„Abo" (F-03.05): periodischer E-Mail-Export (HTML/CSV; PDF via
  Report-Renderer) mit Templates.
- Planungs-Statistik (F-03.08): Stunden je Person/Zeitraum, Wochenend-/Feiertagsanteile
  (Feiertagskalender je Region pflegbar), Überschreitungs-Warnungen.
  Regelbasierte Plangenerierung (F-03.06) und SAP-HCM-CATS-Export (F-03.07): v2-Backlog
  (Export als CSV/API ab v1 möglich).

### 9.6 Notification-Kanäle

| Kanal | Implementierung | Phase |
|---|---|---|
| E-Mail | nativer SMTP-Client (STARTTLS/implizit, DKIM-Signatur optional) | v1 |
| Webhook | Templates (Go-Template auf Normform), HMAC-SHA256-Signatur, Retry mit Exponential-Backoff + Jitter (bis 24 h), Dead-Letter-Queue mit UI/Alarm (F-04.04, A-15.24 Retry) | v1 |
| MS Teams | Adaptive Cards via Workflow/Incoming-Webhook; Ack-Buttons via signierte Links (F-04.01) | v1 |
| Slack | Block-Kit via Webhook/App | v1 |
| Push | **Web Push (VAPID)** an die als PWA installierte UI — dependency-less Mobil-Push ohne App-Store; native Companion-App als v3-Option | v1 |
| SMS | Provider-Abstraktion `SMSProvider` (HTTP): Twilio, websms, seven.io, generischer HTTP-Provider, **Hardware-GSM-Gateways** (SMSEagle/Teltonika-HTTP-API) für RZ ohne Internet (F-01.04-Pendant ausgehend) | v1 (ein Provider), weitere v2 |
| Voice | Provider-API (Twilio Voice / sipgate / 46elks): TTS-Ansage + DTMF-Ack; **kein eigener SIP-Stack** (N1; ADR-05) — On-Prem-Anbindung lokaler TK über SIP-HTTP-Gateways | v2 |
| ServiceNow | bidirektional: Incident-Create mit Feld-Mapping, Status-Sync per Webhook/Poll, Auto-Close bei Alarm-Ende (F-04.05) | v2 |
| ntfy / Gotify | simple HTTP | v1 (ntfy) |
| PagerDuty/Opsgenie-kompatibel | Events-API-v2-Format ausgehend (Migrations-/Koexistenz-Pfad) | v2 |

Persönliche **Kanal-Präferenzen** je Kontakt mit Zeitprofilen (tags: arbeitszeit/nacht),
von Eskalations-Steps überschreibbar (F-04.08). **Nachrichten-Templates** pro Kanal und
Regel mit Variablen aus Event/Alarm/Objekt + statischen Bausteinen (F-04.09);
Template-Preview mit echten historischen Events im UI.

### 9.7 Business Services (BPI)

- Baum/DAG aus `BusinessService`-Knoten; Blätter referenzieren Objekte oder
  Label-Selektoren; Knoten-Regeln: `worst | best | quorum(n%) | weighted`.
- Live-Impact: Statusänderung propagiert; UI zeigt „betroffene Geschäftsdienste" am
  Alarm und umgekehrt „verursachende Checks" am Service (CMP-BPI-Pendant).
- SLA-Definition je BusinessService (Ziel-%, Zeitfenster, geplante Downtimes
  ausgenommen) → Grundlage der Reports (§9.8); SLA-Verbrauch live einsehbar.

### 9.8 Reports

- Typen: Verfügbarkeit (Objekt/Gruppe/BusinessService, mit/ohne Downtimes),
  SLA-Erfüllung, Alarm-Statistik (MTTA/MTTR, Top-Verursacher), Bereitschafts-Report,
  Audit-/Berechtigungs-Report (Revision, A-15.07).
- **HTML-first** (im UI interaktiv, druckoptimiertes Stylesheet); **PDF** über
  optionalen Renderer-Container (headless Chromium als Sidecar, klar deklarierte
  optionale Abhängigkeit — ADR-11); Scheduling mit E-Mail-Versand + Archiv mit
  Retention (CMP-Reports-Pendant).
- Alle Reports sind API-Ressourcen (`POST /reports:render?format=html|pdf|csv`) —
  auch hier: AI und Automation nutzen denselben Weg.

---

## 10. AI-Subsystem

### 10.1 Prinzipien

1. **AI ist API-Client**: Der AI-Layer ruft ausschließlich die öffentliche REST-API mit
   einem Service-Token auf — gleiche RBAC, gleiches Audit (Actor-Typ `ai_agent`).
   Es gibt keinen privilegierten Innenpfad.
2. **Mensch entscheidet bei Mutationen**: mutierende AI-Aktionen laufen per Default im
   Modus `propose` (Diff/Aktion wird zur Bestätigung vorgelegt); Policies können
   einzelne Aktionen (z. B. `ack`, `downtime ≤ 2h`) auf `auto` stellen — pro Rolle,
   pro Ressource, pro Zeitfenster.
3. **Graceful ohne LLM**: Provider `none` ⇒ alle deterministischen Features
   (Korrelation Stufe 1–3, Anomalie-Statistik, Forecasts) bleiben aktiv; nur
   Sprach-Features (Summaries, NL-Query, Chat) deaktivieren sich sichtbar.
4. **Datenschutz**: konfigurierbare Redaction-Pipeline vor jedem LLM-Call
   (Hostnamen-Pseudonymisierung optional, Secrets/PII-Pattern immer), Datenklassen-
   Whitelist, EU-Endpoints, vollständiges Prompt/Response-Log im Audit (§13.6).

### 10.2 Provider-Abstraktion

```yaml
ai:
  provider: anthropic            # anthropic | azure-openai | openai-compat | none
  endpoint: https://api.anthropic.com   # bzw. EU-Endpoint/Gateway, Ollama-URL
  model: claude-sonnet-4-6       # Default-Arbeitsmodell (kostenoptimiert)
  modelDeep: claude-opus-4-8     # für RCA/Postmortems (optional)
  maxMonthlyTokens: 50_000_000   # Hard-Budget mit Alarm bei 80 %
  redaction: { hostnames: pseudonymize, customPatterns: [...] }
```

Ein dünner, eigener Client (Anthropic-Messages- + OpenAI-kompatibles Schema; Streaming;
Tool-Use) — keine LangChain-artigen Frameworks (P3/P5).

### 10.3 MCP-Server (eingebaut)

- Transporte: `northplaned mcp` (stdio, für lokale Agents/Claude Desktop) und
  `https://…/mcp` (Streamable HTTP) mit OAuth-Protected-Resource-Metadata; Tokens =
  normale Northplane-API-Tokens (Scopes!).
- **Tools (v1):**

| Tool | Zweck | Mutierend |
|---|---|---|
| `get_overview` | Zustandszusammenfassung (Problems, Incidents, On-Call) | – |
| `search_objects` | Objekte per Selektor/Volltext | – |
| `get_object` | Detail inkl. effektiver Config, Historie, Metrik-Links | – |
| `query_metrics` | Zeitreihenabfrage (aggregiert, downsampled) | – |
| `get_alerts` / `get_incidents` | Filterbare Listen + Timelines | – |
| `who_is_oncall` | aktuelle Bereitschaft je Schedule | – |
| `run_check_now` | Sofort-Recheck | ✓ (auto-fähig) |
| `acknowledge_alert` | Ack mit Kommentar | ✓ (auto-fähig) |
| `create_downtime` / `create_silence` | mit TTL-Limits aus Policy | ✓ |
| `propose_config_change` | erzeugt Bundle-Diff (Dry-Run-Validierung) | ✓ (immer propose) |
| `apply_config_change` | wendet vorher erzeugten, freigegebenen Diff an | ✓ (Approval-Gate) |
| `render_report` | Report on-demand | – |
| `explain_alert` | deterministischer Kontextabzug (Topologie, letzte Änderungen, ähnliche Vorfälle) als strukturierte Daten — Grundlage für LLM-Erklärungen | – |

- **Resources**: OpenAPI-Spec, Runbooks, Dashboards-Snapshots; **Prompts**: kuratierte
  Vorlagen („morning-briefing", „incident-triage", „config-review").

### 10.4 Assistent in der UI

- Seitenleiste (⌘K → „Ask"), kontextbewusst: aktuelle View (z. B. gefilterte
  Problemliste) wird als strukturierter Kontext mitgegeben.
- Antworten mit **Action-Cards**: vorgeschlagene Aktionen als Buttons mit Parametern
  („Downtime 2 h für 12 Services auf sw-core-02 anlegen") → ein Klick = API-Call mit
  Confirm; niemals unsichtbare Ausführung.
- NL-Query → übersetzt in Selektor/Filter-DSL, zeigt die generierte Query **sichtbar**
  an (Lerneffekt + Verifizierbarkeit).

### 10.5 AI-Features im Lebenszyklus

| Phase | Feature | Mechanik |
|---|---|---|
| Erkennen | **Anomalie-Hinweise** | Statistik-Engine (§10.6) erzeugt `anomaly`-Events; LLM fasst auf Wunsch zusammen, warum die Abweichung ungewöhnlich ist |
| Triagieren | **Incident-Benennung & Zusammenfassung** | bei Sturm-Cluster: LLM erhält Cluster-Events + Topologie-Auszug → Titel, Impact-Satz, Verdachtsursache mit Konfidenz |
| Diagnostizieren | **RCA-Hypothesen** | `explain_alert`-Kontext (letzte Config-Änderungen, Korrelation zu Deployments-Webhooks, ähnliche historische Incidents via Embedding-Suche*) → Hypothesenliste mit Belegen |
| Beheben | **Runbook-Vorschläge** | Runbooks als Markdown am Objekt/Template; LLM passt generisches Runbook auf konkreten Alarm an |
| Lernen | **Postmortem-Draft** | aus Incident-Timeline generiert; Mensch editiert |
| Verwalten | **NL-Konfiguration** | „Überwache 10.0.0.0/24 mit Linux-Standardprofil" → Discovery-Scan + Bundle-Vorschlag als Diff |
| Berichten | **Digest** | täglicher/wöchentlicher Management-Digest (neue Probleme, SLA-Stand, Auffälligkeiten) an Kanal |

\* Embedding-Suche: lokale Vektor-Ablage im relationalen Store (SQLite wie PostgreSQL,
bewusst ohne Vektor-Extension: Brute-Force über ≤ 100k Vektoren ist auf modernen CPUs
< 50 ms; kein Vektor-DB-Zukauf — P5). Embeddings via Provider oder lokal (Ollama);
Feature degradiert sauber ohne Provider.

### 10.6 Deterministische Statistik (kein LLM)

- **Baselines**: EWMA + Saisonalität (Stunde-des-Tages × Wochentag, 4 Wochen Fenster);
  Anomalie = |x − Baseline| > k × MAD (k konfigurierbar, Default 5) über
  Mindestdauer (Default 3 Intervalle) — bewusst konservativ gegen Alert-Fatigue.
- **Adaptive Thresholds** (`thresholdMode: adaptive`): Warn/Crit aus Baseline-Quantilen
  (P98/P99.5) mit Min/Max-Klemmen; täglich neu berechnet, Änderungen als Event
  auditierbar (nachvollziehbar, warum alarmiert wurde).
- **Forecasts**: lineare + saisonbereinigte Trends für Kapazität („Disk voll in
  ~9 Tagen") als `forecast`-Events; Konfidenzintervall im Chart.

---

## 11. API-Spezifikation

### 11.1 Konventionen

- Basis: `/api/v1`; Breaking Changes ⇒ `/api/v2` (Parallelbetrieb ≥ 12 Monate).
- **OpenAPI 3.1** wird beim Build aus den registrierten Routen + Go-Typen generiert
  (Single Source of Truth = Code) und unter `/api/openapi.json` + eingebauter
  Doc-UI (`/api/docs`, schlankes eigenes Rendering) ausgeliefert.
- HTTP-Semantik strikt (A-15.15): GET safe/cacheable (ETag), PUT idempotent, POST mit
  **Idempotency-Key**-Header für Notifications-kritische Endpunkte, DELETE idempotent,
  korrekte Codes (`409` bei Versionskonflikt, `422` Validierung, `429` mit
  `Retry-After`).
- Fehlerformat: **RFC 9457 Problem Details** (`application/problem+json`) mit
  maschinenlesbarem `code` (z. B. `np:validation/unknown-template`).
- Pagination: Cursor-basiert (`?limit=…&cursor=…`, `next_cursor` in Antwort);
  Filterung: einheitliche Selektor-Syntax `?selector=env=prod,role in (db,cache)` +
  Volltext `?q=`; Sortierung `?sort=-opened_at`.
- Konfig-Objekte tragen `version`; Mutationen verlangen `If-Match` (ETag) →
  Lost-Update-sicher (auch für die UI verpflichtend).
- Zeit: RFC 3339 UTC überall; Dauern als Go-Syntax (`90s`, `5m`).
- Bulk: `POST …:batch` mit atomarem (`all-or-nothing`) oder partiellem Modus +
  Einzelfehler-Bericht (Wizard-/Massenanlage, CMP-Wizard-Pendant).

### 11.2 Authentifizierung & Autorisierung

- Menschen: OIDC Authorization Code + PKCE (Entra ID, Keycloak getestet);
  Session-Cookie (HttpOnly, SameSite=Lax) **nur** für die eingebaute UI; SAML 2.0
  hinter Build-Tag (v2, A-15.08 nennt OIDC ausdrücklich als bevorzugt).
- Maschinen: **API-Tokens** (`np_` Prefix, Argon2id-Hash gespeichert), Scopes
  (`objects:read`, `alerts:ack`, `config:write`, `admin:*` …), optionale
  IP-Bindung, Ablauf + Rotations-Endpoint; **mTLS** für Agents/Satelliten.
- RBAC: Rollen = Bündel aus Permissions (`resource:action`) + **Scope**
  (Tenant + Folder-Teilbaum + optional Label-Selektor); Rollen verschachtelbar
  (Vererbung, A-15.07); IdP-Gruppen-Mapping (`Entra-Group-ID → Rolle`).
  Effektive-Rechte-Auskunft: `GET /api/v1/whoami` (auditierbar, Revisions-Anforderung).

### 11.3 Ressourcen-Katalog (Kurzreferenz)

```
/objects /hosts /services            CRUD, :batch, /effective-config, /check-now
/templates /check-commands /time-periods
/results                             passive Submission (Batch)
/metrics/query                       Zeitreihen (POST, JSON-Query)
/events                              Suche/Export (Filter, Zeitbereich)
/alerts                              Liste, /{id}, :ack, :resolve, :snooze
/incidents                           CRUD, Timeline, :merge, :summarize (AI)
/alert-rules /alert-groups           CRUD, :test (Demo-/Histo-Events)
/silences /downtimes                 CRUD (TTL-Pflicht bei Silences)
/escalation-policies                 CRUD, :simulate
/schedules /oncall                   CRUD, /now, /ics, Overrides
/contacts /contact-groups /channels  CRUD, :test-notification
/business-services                   CRUD, /impact, /sla
/dashboards /reports                 CRUD, :render
/event-sources /heartbeats           Ingress-Verwaltung
/ingest/{source}                     Event-Eingang (extern)
/stream                              SSE (Filter via Query)
/config/bundles                      :plan (Dry-Run-Diff), :apply, Export
/discovery/scans                     Netz-Scan-Jobs (ICMP/TCP/SNMP) → Vorschläge
/tenants /users /roles /api-tokens   Administration
/audit                               Suche, Export (NDJSON), Hash-Verifikation
/ai/conversations /ai/actions        Assistent, Approval-Queue
/system/health /system/info /metrics(OpenMetrics)  Selbst-Monitoring
```

### 11.4 Beispiele

**Host anlegen (idempotent über Bundle) — `POST /api/v1/config/bundles:apply`**

```http
POST /api/v1/config/bundles:apply?dryRun=true
Authorization: Bearer np_…
Content-Type: application/yaml

# (Bundle wie §6.2) → Antwort: Diff
```
```json
{ "plan": [
  { "action": "create", "kind": "Host", "name": "db-prod-01.example.net" },
  { "action": "update", "kind": "Service", "name": "postgres-connections",
    "diff": { "spec.interval": ["60s", "120s"] } }
], "warnings": [], "applyToken": "ap_9f2…" }
```

**Passives Ergebnis:**

```http
POST /api/v1/results
{ "results": [ { "host": "db-prod-01.example.net", "service": "backup-job",
  "state": 0, "output": "backup OK - 42GB in 13m | size=42GB;;;0; duration=780s;900;1200;0;" } ] }
```

**SSE-Stream:**

```
GET /api/v1/stream?types=state_change,alert&selector=env=prod
← event: state_change
  id: 01HZX…
  data: {"object":"svc_…","from":"OK","to":"CRITICAL","attempt":1,…}
```

**Downtime mit Idempotenz:**

```http
POST /api/v1/downtimes
Idempotency-Key: maint-2026-06-12-sw-core-02
{ "selector": "device=sw-core-02", "start": "2026-06-12T22:00:00Z",
  "end": "2026-06-13T02:00:00Z", "type": "fixed",
  "comment": "Firmware-Upgrade CHG0042" }
```

### 11.5 Webhooks (ausgehend) & Streams

- Subscription-Ressource: Ziel-URL, Event-Filter, Template, Secret (HMAC), Status;
  Zustellgarantie at-least-once mit Backoff/DLQ; Replays per API
  (`POST /webhooks/{id}:replay?from=…`).
- SSE für UI/leichte Integration; für hohe Volumina: NDJSON-Export-Endpunkte mit
  Cursor (kein Kafka im Kern, Export dorthin via kleinem Bridge-Beispiel dokumentiert).

### 11.6 Config-as-Code & Terraform

- Bundle-Format (YAML, mehrere Dokumente, `kind`-basiert) = kanonische Exportform
  (`np export --folder /prod > prod.yaml`); Round-Trip-stabil.
- `np apply -f …` mit `--prune --selector` (GitOps-tauglich); Server-seitiger
  **Plan/Apply-Zweischritt** mit Apply-Token (oben) — gleiche Mechanik nutzt der
  AI-Layer für `propose/apply`.
- **Terraform-Provider** (v2): generiert aus OpenAPI; Ressourcen für Rules, Policies,
  Schedules, Channels (F-02.01-Wunsch „Terraform" wird damit Standard statt „klären").

---

## 12. Frontend-Spezifikation

### 12.1 Grundsatzentscheidung: Architektur

**Entscheidung: SPA — React + TypeScript + Vite + Tailwind v4 + shadcn/ui, ausgeliefert
als statische Assets aus dem Go-Binary (`go:embed`), plus zwei server-gerenderte
Ausnahmen (Login, öffentliche Status-Page).**

Begründung gegen die Alternativen (Beratungsteil):

| Option | Bewertung |
|---|---|
| **React-SPA (gewählt)** | Monitoring-UI ist eine hochinteraktive, langlebige Arbeitsoberfläche (Live-Updates, Filter, Tabellen, Charts, Command-Palette) — exakt das SPA-Profil. Kein SEO-Bedarf, Nutzer sind eingeloggt. Größtes Ökosystem, einfachstes Hiring, shadcn/ui passt zur Vendoring-Philosophie (Code im Repo statt Dependency). React 19 ohne Framework-Overhead. |
| Next.js/SSR-Framework | Verworfen: SSR/Server-Components brauchen Node-Laufzeit ⇒ zerstört Single-Binary-Deployment (P3) und bringt für ein internes, login-pflichtiges Tool keinerlei Nutzen. |
| HTMX + Go-Templates | Ernsthaft erwogen (am wenigsten Dependencies). Verworfen für die Haupt-UI: Live-Wallboards, virtualisierte 50k-Zeilen-Tabellen, Drag-and-Drop-Dashboards und der AI-Chat werden mit Hypermedia-Roundtrips komplexer statt einfacher. **Aber:** Login + Status-Page sind bewusst server-gerendert (robust, JS-frei) — das Beste beider Welten. |
| SolidJS / Svelte 5 | Technisch attraktiv (kleiner/schneller), aber: shadcn-Ports hinken, Ökosystem-/Personal-Risiko höher; Performance-Vorteil ist bei dieser App nicht der Engpass (der Engpass sind Datenmengen → Virtualisierung, die es überall gibt). Verworfen — pragmatisch, nicht dogmatisch. |
| Desktop (Tauri/Electron) | Nicht erforderlich; PWA deckt Offline-Anzeige + Push ab. |

**Dependency-Disziplin Frontend:** Runtime-Deps limitiert auf: `react`, `react-dom`,
`@tanstack/react-query`, `@tanstack/react-router`, `@tanstack/react-virtual`,
Radix-Primitives (via shadcn), `uplot` (Zeitreihen-Charts, ~45 KB, vendored Wrapper),
`zod` (API-Schemas, aus OpenAPI generiert). **Kein** Redux/MobX (Server-State =
TanStack Query, UI-State = URL + lokale Hooks), kein moment/dayjs (`Intl` +
eigene 200-Zeilen-Helfer), kein i18next (eigener typisierter Katalog DE/EN),
kein Chart-Framework über uPlot hinaus.

### 12.2 Auslieferung & Performance

- Vite-Build → `go:embed`; Hashing + `Cache-Control: immutable`; Brotli vorkomprimiert.
- Budgets: Initial-Bundle **< 250 KB gz** (Code-Splitting je Route), TTI < 2 s auf
  Mittelklasse-Laptop, Interaktionen < 100 ms, Tabellen virtualisiert ab 200 Zeilen
  (A-15.21: < 1 s Antwortzeit; > 3 s ⇒ Progress-Indikator verpflichtend).
- Realtime: ein SSE-Multiplex-Stream pro Tab; TanStack-Query-Cache wird gezielt
  per Event invalidiert/gepatcht (kein Voll-Refetch); Offline/Reconnect-Banner mit
  `Last-Event-ID`-Resume; Tab-Sichtbarkeit drosselt Updates.

### 12.3 Informationsarchitektur (Views)

| View | Inhalt / Besonderheiten |
|---|---|
| **Overview/Wallboard** | Kennzahlen-Kacheln, Problem-Heatmap, Incident-Liste, On-Call-Widget; Wallboard-Modus (Fullscreen, Auto-Rotate, Read-only-Share-Link mit Token) |
| **Problems** | priorisierte Liste (Severity × Dauer × Impact), Gruppierung nach Incident/Host/Regel, Inline-Ack/Downtime/Silence (≤ 3 Klicks), gespeicherte Filter-Vorlagen global/persönlich (F-05.02) |
| **Objects** | Hosts/Services-Explorer: Folder-Baum + Label-Filterleiste + Volltext; virtualisierte Tabelle, Spalten konfigurierbar, Bulk-Aktionen; Detail: Status, Timeline, Metrik-Charts mit Schwellenbändern, effektive Config (Vererbungsanzeige!), Abhängigkeits-Mini-Graph, Runbook-Tab |
| **Topology/Impact** | Dependency-Graph (Canvas, Force/hierarchisch), BusinessService-Bäume mit Live-Status, „Was-wäre-wenn" (Knoten markieren ⇒ betroffene Services) |
| **Alerts & Incidents** | Alarm-Liste + Incident-Board; Incident-Detail: Timeline (Events, Notifications, Acks, AI-Annotationen), Impact, externes Ticket |
| **Events** | durchsuchbares Event-Log mit Filtervorlagen (F-05.01), NDJSON-Export |
| **On-Call** | Kalender (Monat/Jahr, F-05.06), Rotations-Editor (visuelles „Rad"), Override-Dialog, Übergabe-Flow, Statistik |
| **Dashboards** | Grid-Layout, Widgets (Chart, Status-Map nach Selektor, Liste, Text/Markdown, iFrame-frei!), personalisierbar (A-15.35), teilbar |
| **Reports** | Galerie, Scheduling, Archiv |
| **Admin** | Templates, Check-Commands (mit Test-Konsole!), Event-Quellen, Regeln (mit `:test`-Runner UI), Eskalations-Policies (Simulator), Kanäle (Test-Senden), Tenants/Rollen/Tokens, Audit-Browser, System-Health |
| **AI** | Chat-Sidebar (global), Approval-Queue für AI-Aktionen, Digest-Einstellungen |

### 12.4 UX-Prinzipien

- **Tastatur-first**: ⌘K-Command-Palette (Navigation, Aktionen, NL-Query), j/k-Listen,
  `a`=ack, `d`=downtime im Fokus-Kontext.
- Dichte umschaltbar (compact/comfortable); Dark-Mode default, Light vollwertig.
- Status niemals nur über Farbe (Icons/Pattern, WCAG 2.1 AA, A-15.29ff);
  deutsche und englische UI (Katalog, DE = Referenzsprache der Domäne).
- Destruktive/mutierende Aktionen: Confirm mit Konsequenz-Text („3 Eskalationen
  laufen — Ack stoppt sie"), Undo wo semantisch möglich (A-15.34: letzter Schritt).
- Leere Zustände erklären den nächsten Schritt (Onboarding: „Ersten Host anlegen →
  Wizard / Bundle / Discovery").
- **Status-Page** (`/status/{slug}`): server-gerendert, ohne JS funktionsfähig,
  read-only, optional öffentlich (Token/IP-Filter), Auto-Refresh via Meta/SSE-Enhancement.

---

## 13. Sicherheit & Compliance

### 13.1 Threat Model (Auszug, STRIDE-orientiert)

| Bedrohung | Gegenmaßnahme |
|---|---|
| Gestohlener API-Token | Scopes + Ablauf + IP-Bindung; Token-Hash (Argon2id); Audit-Anomalie-Hinweis (neue Quelle-IP) |
| Plugin-Ausbruch (exec) | dedizierter Ausführungs-User, leeres Env (Whitelist), keine Shell-Interpolation (argv-Array), Timeout + Prozessgruppen-Kill, optionale cgroup-Limits (CPU/RSS), Plugins-Verzeichnis read-only & allowlist-fähig |
| SSRF via Webhook/HTTP-Check | Ziel-Allow/Deny-Listen (CIDR), kein Redirect-Follow auf private Netze, DNS-Rebinding-Schutz (Pin nach Resolve), Link-Local/Metadata-IPs default geblockt |
| Injection in CEL/Templates | CEL sandboxed (keine I/O, Kosten-Budget); Go-Templates mit FuncMap-Whitelist, kein `os`-Zugriff |
| Event-Flood (Ingress) | per-Source Rate-Limits + Burst, 429, Drop-Statistik als Metrik + Alarm |
| Manipulation Audit | Hash-Kette (§13.5), Export an externes SIEM (write-only) |
| Supply Chain | `go.sum`-Verifikation, minimale Deps (§7.9), SBOM (CycloneDX) je Release, signierte Releases (cosign), reproduzierbare Builds angestrebt |
| AI-Prompt-Injection (Event-Texte!) | LLM-Kontext kennzeichnet Fremdtexte als Daten; Aktionen nur via Tool-Calls gegen RBAC; mutierend ⇒ Approval (§10.1); Event-Inhalte werden nie als Instruktionen interpretiert („tool results are data") |

### 13.2 Transport & Kryptographie

- TLS ≥ 1.2 (Default 1.3), kuratierte Cipher-Suites, kein Plaintext-Fallback
  (A-15.10); HSTS; optional ACME (intern: eigene CA für Agents/Satelliten).
- Secrets at rest: AES-256-GCM mit Master-Key aus Datei/KMS-Hook/Env;
  Schlüsselrotation mit Re-Encrypt-Job; Secrets nie in Bundles exportiert
  (Referenz `$SECRET:name$` bleibt symbolisch).
- Passwörter (lokale Notfall-Accounts): Argon2id; WebAuthn für lokale Accounts (v2).

### 13.3 Mandantenfähigkeit

- `tenant_id` auf jeder Zeile; Erzwingung im Storage-Layer (jede Query läuft durch
  Tenant-Scope-Builder — kein Handler kann ihn vergessen, Compile-Zeit-API).
- Getrennte Verschlüsselungs-Subkeys je Tenant; Export/Löschung je Tenant
  (DSGVO-Auftragsende); UI-Tenant-Switcher für berechtigte Operator (A-15.12:
  primär ein Mandant, Architektur erlaubt mehr).

### 13.4 DSGVO

- Personenbezug minimiert: Kontakte (Name, Kanäle, Dienstzeiten) sind die wesentliche
  PII-Klasse; Events können PII enthalten ⇒ Retention-Klassen je Event-Typ
  (umgesetzt als In-situ-Payload-Purge; das Zeilenskelett fällt mit dem
  Zeitsegment bzw. der Partition, ADR-13).
- **Beauskunftung**: `GET /api/v1/contacts/{id}:data-export` aggregiert alle
  personenbezogenen Daten (Profil, Benachrichtigungen, Bereitschaften, Audit-Einträge)
  als ZIP (JSON + HTML-Bericht) (A-15.04).
- **Löschkonzept**: Lösch-Workflow mit Anonymisierung in historischen Pflichtdaten
  (Audit bleibt integer: Actor wird pseudonymisiert, Kette bleibt gültig) (A-15.05).
- Datenhaltung: vollständig on-prem/EU; AI-Provider-Wahl inkl. EU-Endpoints,
  Redaction-Pipeline, Provider `none` (§10).

### 13.5 Audit

- Jede Mutation (UI/API/AI/System) → `audit_log` mit `before/after`, Actor, Quelle-IP,
  Request-ID; **Hash-Verkettung** (`hash = SHA256(prev_hash ‖ row)`) ⇒ Manipulation
  erkennbar (`np audit verify`); Export: Syslog (RFC 5424, TLS) / NDJSON / Webhook
  ans SIEM (A-15.20).
- Notifications-Protokoll: jeder Zustellversuch mit Provider-Antwort (F-05.09:
  „unveränderbare Historie").

### 13.6 AI-Governance

- AI-Aktionen-Log: Prompt-Hash, redigierter Prompt, Tool-Calls, Antwort, Token-Kosten,
  Genehmiger; monatliches Budget mit Hard-Stop; Modell-/Endpoint-Konfiguration nur
  durch `admin:ai`-Scope.

---

## 14. Nicht-funktionale Anforderungen

### 14.1 Performance-Ziele (verbindlich, CI-gemessen)

| Metrik | Ziel |
|---|---|
| Aktive Checks Single-Node (8 vCPU/16 GB) | 100.000 Services @ 60 s (≈ 1.700 Checks/s, Mix 70 % builtin / 30 % exec) |
| Check-Latenz (Soll-Zeit → Ausführung) | P99 < 2 s bei Volllast |
| API-Reads | P99 < 100 ms (Listen ≤ 100 Items), P99 < 300 ms (komplexe Filter) |
| API-Writes | P99 < 200 ms |
| State-Change → SSE beim Client | P95 < 1 s |
| State-Change → erste Notification versendet | P95 < 5 s |
| TSDB-Query (1 Serie, 24 h, downsampled) | P99 < 50 ms |
| UI Initial-Load (Cold) | < 2 s TTI; Folge-Navigation < 300 ms |
| Recovery nach Crash | Startbereit < 30 s inkl. WAL-Replay bei G1-Datenbestand |

### 14.2 Verfügbarkeit & Datensicherung

- Single-Modus: Ziel 99,5 %/Monat; **kontinuierliches Backup**: SQLite-WAL-Shipping +
  TSDB-Chunk-Sync auf S3-kompatibles Ziel/Verzeichnis (eingebaut, `northplaned backup`),
  RPO ≤ 5 min, dokumentierter Restore-Test (`np restore --verify` als Übung, A-15.25
  inkl. Self-Service). Im PostgreSQL-Modus liegt die relationale Sicherung beim
  DB-Betreiber (PITR/`pg_dump`); `northplaned backup` umfasst dann TSDB-Chunks,
  Artefakte und ein Konsistenz-Manifest inkl. DB-Schema-Version.
- HA-Modus: 99,9 %; Leader-Failover < 30 s (Lease-TTL 10 s); rollierende Upgrades.
- **Dead-Man-Switch**: konfigurierbarer ausgehender Heartbeat (healthchecks.io-API-
  kompatibel, selbst hostbar) + systemd-Watchdog (`WATCHDOG_USEC`) ⇒ Eigenausfall wird
  extern bemerkt (A-15.24 Fallback-Benachrichtigung; P7).
- Graceful Shutdown: laufende exec-Checks bis Timeout, Ergebnis-Flush, SSE-Drain.

### 14.3 Kapazitäts-/Mengengerüst

| Dimension | v1-Grenze (getestet) |
|---|---|
| Hosts / Services | 20k / 200k (Objekte), G1-Checkrate |
| Events | 500/s anhaltend (PostgreSQL-Modus; SQLite-Richtwert ~50/s, §7.3), 5.000/s Burst 60 s in beiden Modi (Backpressure dokumentiert) |
| Offene Alerts | 50k |
| Satelliten | 50 je Server |
| SSE-Clients | 500 |
| Mandanten | 100 |
| Benutzer | 5.000, 500 gleichzeitig |

### 14.4 Betriebssysteme & Plattformen

Server/Satellit: Linux amd64/arm64 (RHEL 9, Ubuntu LTS, Debian, Container);
Agent: + Windows Server 2019+, macOS 13+. Browser: Chrome/Edge/Firefox/Safari je
aktuelle u. Vorversion; iOS-Safari für Responsive-Pfade + PWA (A-15.17).

---

## 15. Deployment & Betrieb

### 15.1 Installationspfade

```bash
# 1) Bare Metal / VM (RHEL 9)
curl -LO https://…/northplaned_linux_amd64 && install -m755 northplaned /usr/local/bin/
northplaned init   # erzeugt /etc/northplane/config.yaml + systemd-Unit + Admin-Bootstrap-Token
systemctl enable --now northplaned

# 2) Container / OpenShift
podman run -v np-data:/var/lib/northplane -p 8443:8443 ghcr.io/…/northplane:1.0
# Image: distroless, non-root, arbitrary-UID-fähig, ro-rootfs, keine Capabilities
# (OpenShift restricted-v2-SCC-kompatibel, A-15.18); Helm-Chart ab v1.1
```

### 15.2 Konfigurationsschichten

1. `config.yaml` (bewusst minimal: Listen-Adresse, Storage-DSN/Pfad, OIDC, TLS,
   AI-Provider, Backup-Ziel) — alles, was **vor** API-Verfügbarkeit nötig ist;
2. alles andere: API/UI/Bundles (versioniert, auditiert);
3. `NORTHPLANE_*`-Env-Overrides für Container.

### 15.3 Upgrades

- SemVer; ein Binary ersetzen + Restart (Single: Downtime < 10 s, Checks holen via
  Splay auf); HA: rollierend = zero-downtime (A-15.24).
- Migrationen automatisch beim Start (vorwärts-only, Backup-Gate: weigert sich ohne
  frisches Backup-Manifest, überschreibbar per Flag).
- LTS-Zweig: jede .0 erhält 18 Monate Security-Fixes.

### 15.4 Observability des Systems selbst

- `/metrics` (OpenMetrics): Scheduler-Lag, Queue-Tiefen, Check-Raten, Notification-
  Erfolg/Fehler je Kanal, TSDB-Stats, SSE-Clients, AI-Tokens;
- strukturierte Logs (slog/JSON) mit Request-IDs; `np doctor` (Selbstdiagnose:
  fsync-Latenz, Zeit-Sync, Limits, Zertifikatslaufzeiten);
- eingebaute „Northplane überwacht Northplane"-Templates (Queue-Lag-Alarm etc.).

---

## 16. Teststrategie & Qualitätssicherung

| Ebene | Inhalt |
|---|---|
| Unit | Statemachine (Tabellen-getrieben: alle Nagios-Übergänge inkl. soft/hard/flap), Perfdata-Parser (Fuzzing!), CEL-Sandbox, Selektor-Parser, TSDB-Encoding (Property-based: encode→decode = id) |
| Integration | echte Nagios-Plugins (monitoring-plugins-Paket) im Container-Matrix-Test; NRPE gegen Referenz-Daemon; OIDC gegen Keycloak-Container; **Storage-Matrix: jede Suite läuft gegen SQLite und PostgreSQL** (ab M0, identische Testfälle) |
| Kompatibilität | **Golden-Corpus**: 25 reale Nagios/Icinga-Konfigurationen (anonymisiert) → Importer-Snapshot-Tests |
| Last | G1-Lastprofil als CI-Nightly (synthetischer Check-Mix), Pass/Fail gegen §14.1-Budgets; SSE-Fanout-Test |
| Sicherheit | SAST (gosec, CodeQL), Dependency-Audit (govulncheck), DAST gegen Test-Instanz, SSRF-Testsuite, jährlicher externer Pentest vor Major |
| E2E (UI) | Playwright: kritische Flows (Login/SSO, Ack ≤ 3 Klicks, Downtime, Regel-Test, Wallboard-Live-Update) |
| AI | deterministische Replay-Tests (aufgezeichnete Tool-Call-Sequenzen), Injection-Suite (bösartige Event-Texte dürfen nie Aktionen auslösen) |
| Chaos | Kill -9 unter Last (WAL-Replay-Korrektheit), Disk-Full, Clock-Skew, Satellit-Partition (Store-and-Forward-Lückenlosigkeit) |

Definition of Done je Feature: API + OpenAPI-Doku + UI + `np`-CLI + Audit-Events +
Metriken + Tests + Doku-Seite.

---

## 17. Roadmap & Phasenplan

| Phase | Inhalt (Auswahl) | Exit-Kriterium |
|---|---|---|
| **M0 — Kern** (≈ 3 Mon.) | Storage-Layer (SQLite **und** PostgreSQL hinter einer Schnittstelle, CI-Matrix ab Tag 1; NP-TSDB), Objektmodell + Templates, Scheduler/Executor (builtin + exec), Statemachine, Perfdata→TSDB, REST-Kern + OpenAPI, SSE, minimale UI (Problems, Objects, Detail), `np` CLI, passive Results, Heartbeats | 1.000 Hosts produktiv monitorbar; Nagios-Plugin-Suite grün; Integrations-Suite auf beiden Backends grün |
| **M1 — Alerting/On-Call** (≈ 2,5 Mon.) | Event-Ingress (HTTP, Alertmanager), Alert-Rules (CEL) + `:test`, Eskalations-Engine, Kanäle (E-Mail, Webhook, Teams, Slack, ntfy, Push, 1× SMS-Provider), Schedules/Rotationen/Overrides/ICS, Silences/Downtimes vollständig, Audit-Kette | Pilotteam ersetzt bestehende Alarmierung |
| **M2 — AI & Config-as-Code** (≈ 2 Mon.) | MCP-Server, Assistent + Action-Cards, Korrelation+Incidents, Anomalie/Adaptive/Forecast, NL-Query/-Config, Bundles `:plan/:apply`, Importer Nagios/Icinga, Discovery-Scan | „Alarm-Sturm-Szenario" (§5) demonstrierbar |
| **M3 — Enterprise** (≈ 2,5 Mon.) | RBAC-Vollausbau + verschachtelte Rollen + IdP-Gruppen, Mandanten-UI, Dashboards + Wallboard + Status-Page, Reports (HTML/PDF-Sidecar) + Scheduling, BusinessServices/SLA, SNMP-Traps, E-Mail-Ingress, Voice-Provider, ServiceNow | Ausschreibungs-MUSS-Profil (F/A-Mapping, Anhang B) erfüllt |
| **M4 — Scale/HA** (≈ 2 Mon.) | HA-Modus auf PostgreSQL (Leader-Election, Follower-Event-Fanout — das PG-Backend selbst ist seit M0 produktiv nutzbar), TSDB-Replikation, Satelliten GA, Prometheus Remote-Write, Terraform-Provider, Helm | G1×2 im HA-Modus; Zero-Downtime-Upgrade demonstriert |
| v2-Backlog | SAML, MQTT/DB-Ingress, SAP-CATS, regelbasierte Dienstplan-Optimierung, WebAuthn, native App, PromQL-Subset-Evaluation | — |

---

## 18. Entscheidungsregister (ADRs)

| # | Entscheidung | Kernbegründung | Verworfen |
|---|---|---|---|
| 01 | Go, Single-Binary, stdlib-first | statisches Deployment, Betriebssimplizität (P3/P5) | Rust (Team-Velocity), Java (Footprint) |
| 02 | Zwei gleichwertige SQL-Backends ab M0 — SQLite (Embedded-Default) und PostgreSQL (Server-Modus, empfohlen für Enterprise/HA/hohe Eventraten); Metriken immer in eigener TSDB | Zero-Dep-Einstieg bleibt (P3), Enterprise erhält echte DB-Plattform statt „SQLite in Produktion"-Debatte; der klassische Monitoring-DB-Schmerz (Metriken in SQL, Zabbix-History-Problem) ist per Design ausgeschlossen; Doppel-Backend-Steuer durch schmale Schnittstelle + CI-Matrix begrenzt (§7.3/§16); Grafana-erprobtes Modell | PostgreSQL-only (zerstört Single-Binary-Eval, Edge/MSP-Boxen, P3); SQLite-only mit Replikations-Layer à la rqlite/LiteFS für HA (P5: Konsens selbst betreiben); TimescaleDB (Extension-Zwang für alle); VictoriaMetrics embed (Dependency-Gewicht) |
| 03 | Nagios-Plugin-Protokoll vollständig, External-Command-Pipe nicht | Migrationsnutzen hoch / Pipe = Sicherheits-Altlast mit API-Ersatz | Vollemulation |
| 04 | SSE statt WebSocket | unidirektional genügt, Proxy-robust, eingebauter Reconnect | WS (Mehraufwand ohne Nutzen) |
| 05 | Voice/SMS via Provider-/Gateway-APIs, kein eigener SIP/GSM-Stack | Telephonie-Stack = eigenes Produkt; Gateways liefern On-Prem-Pfad | eigener SIP-Stack (N1) |
| 06 | CEL für Regeln | sandboxed, getestet, IaC-tauglich | eigene DSL (Aufwand/Risiko), Lua (Sandbox-Komplexität) |
| 07 | React + Tailwind + shadcn (vendored), Hybrid mit SSR-Login/Status | §12.1-Matrix | Next.js, HTMX-only, Solid/Svelte |
| 08 | AI strikt als API-Client mit Approval-Gates | Sicherheits-/Audit-Modell bleibt einheitlich (P2) | privilegierter AI-Pfad |
| 09 | UUIDv7 + Cursor-Pagination | zeitsortierbar, replikationsfreundlich | Auto-Increment (HA), Offset-Paging (Performanz) |
| 10 | OpenAPI aus Code generiert | eine Quelle der Wahrheit, Drift unmöglich | Spec-first (Doppelpflege) |
| 11 | PDF via optionalem Chromium-Sidecar | echtes HTML/CSS-Rendering; Kern bleibt schlank | pure-Go-PDF (Layout-Qualität), wkhtmltopdf (tot) |
| 12 | Web Push/PWA statt nativer App (v1) | dependency-less Mobil-Alarmierung inkl. Ack | App Stores (Pflegeaufwand) als v3-Option |
| 13 | Events zeitpartitioniert (SQLite: Segment-Dateien via ATTACH; PostgreSQL: Range-Partitionen); Retention = Segment-/Partitions-Drop | O(1)-Retention ohne DELETE-/VACUUM-Schmerz; Reports auf Alt-Segmenten stören heiße Schreibpfade nicht; PII-Feinsteuerung via Payload-Purge (§13.4) | zeilenweise Retention-Deletes (Bloat, WAL-/Lock-Druck), externes Event-Store (Dependency) |

---

## Anhang A: CMP → Northplane Capability-Mapping

| CMP-Produkt | Northplane-Modul | Phase | Anmerkung |
|---|---|---|---|
| CMP Monitoring | Core (Scheduler/Checks/States/TSDB) | M0 | Icinga-Erbe → eigenes Go-Core |
| CMP Monitoring Admin | Objects/Admin-UI + API | M0 | UI = API-Client (P1) |
| CMP Monitoring Wizard | Templates, `:batch`, Bundles, Discovery, Importer | M0–M2 | „Wizard" = Discovery + NL-Config |
| CMP Alarmserver | Ingress-Adapter + Alerting + Eskalation + Kanäle | M1–M3 | eine Pipeline statt Zweitsystem |
| CMP Alarmserver Webmin | Admin-Views (Regeln/Tests/Audit) | M1 | Regel-Test als Kernfeature |
| CMP Alarm App | PWA + Web Push + Ack-Links | M1 | native App v3-Option |
| CMP Agent (CMPA) | np-agent (Inventar + Metriken + lokale Checks) | M1 (Basis) | mTLS, Store-and-Forward |
| CMP End-to-End | exec-Checks mit Artefakten + builtin:http-flow | M0/M2 | N3: kein eigener Robot |
| CMP Visualization Dashboard | Dashboards + Wallboard + Status-Page | M3 | |
| CMP Reports | Reports-Modul | M3 | HTML-first, PDF-Sidecar |
| CMP BPI | BusinessServices + Impact + SLA | M3 | |
| CMP IPAM/MAC Finder | — (N2) | — | Discovery liefert Rohdaten; NetBox-Integration |
| CMP Consumption | Metrik-Reports/Forecast | M3 | über TSDB-Aggregation |
| DEM | builtin-Synthetics von Satelliten | M2 | Mehrstandort-Messung |

## Anhang B: Anforderungs-Traceability (Auszug)

| Anforderung | Erfüllung (Kapitel) |
|---|---|
| F-01.01 HTTP-Events | §7.5 Ingress `POST /ingest/{source}` |
| F-01.02 SNMP-Sammler | §7.5 Trap-Receiver (v2) + Satellit als „Event-Sammler" |
| F-01.03 E-Mail-Events | §7.5 IMAP/Graph (v2) |
| F-01.04/06 SMS/Telefon-Inbound | §7.5 via Gateway-Provider (v2) |
| F-01.07 MQTT | §7.5 (v3) |
| F-01.08 DB-Events | §7.5 (v3) |
| F-02.01 Klassifikation/Labels, IaC | §9.2 CEL + Labels; §11.6 Bundles/Terraform |
| F-02.02 Regeln inkl. Pending/Autoclose/Heartbeat | §9.2 |
| F-02.03 Alarmgruppen + Aggregation | §9.2/§9.3 |
| F-02.04 Suppression | §9.2; §6.3 Downtimes/Silences |
| F-03.01–.04 Bereitschaft/Dienstplan/Ad-hoc/Benachrichtigung | §9.5 |
| F-03.05 Abo | §9.5 |
| F-03.06–.08 Optimierung/SAP/Statistik | §9.5 (v2/Statistik v1) |
| F-04.01–.11 Kanäle/Eskalation/ServiceNow/Präferenzen/Templates | §9.4/§9.6 |
| F-05.01–.09 Ansichten/Filtervorlagen/Detail/Tests/Audit | §12.3; §9.2 `:test`; §13.5 |
| A-15.01/02 EU/DSGVO | §13.4 |
| A-15.07/08 RBAC/SSO | §11.2 |
| A-15.10 TLS | §13.2 |
| A-15.12 Mandanten | §13.3 |
| A-15.15 HTTP-Semantik | §11.1 |
| A-15.17/18 Clients/OpenShift/RHEL9 | §14.4/§15.1 |
| A-15.21 Antwortzeiten | §14.1/§12.2 |
| A-15.22/23 Skalierung/Batch | §7.8/§7.3 |
| A-15.24 Zero-Downtime/Fallback/Retry | §7.8/§14.2/§9.6 |
| A-15.25 Backup/Self-Service | §14.2 |
| A-15.26 Export | §11 (API/NDJSON/Bundles) |
| A-15.29–.36 UX-Prinzipien | §12.4 |

## Anhang C: Glossar

| Begriff | Bedeutung |
|---|---|
| **Bundle** | deklaratives YAML-Konfigurationspaket (`np apply`) |
| **builtin-Check** | in Go implementierter In-Process-Check |
| **CEL** | Common Expression Language — sandboxte Ausdruckssprache für Regeln |
| **Dead-Man-Switch** | ausgehender Heartbeat, dessen Ausbleiben extern alarmiert |
| **Ingress-Adapter** | Empfänger externer Events (Webhook, Mail, Trap, …) |
| **NP-TSDB** | eingebaute Zeitreihen-Engine (Gorilla-Encoding, Tiers) |
| **Normform** | kanonisches Event-Schema aller Quellen |
| **Satellit** | entfernter Check-Executor mit Store-and-Forward |
| **Splay** | deterministische Verteilung von Check-Startzeiten |
| **Wallboard** | Vollbild-Read-only-Dashboard für Leitstand-Monitore |

---

*Ende der Spezifikation. Änderungen an diesem Dokument durchlaufen Review; die
maschinenlesbaren Teile (Bundles, OpenAPI) sind der Implementierung nachgelagert
als Single Source of Truth zu pflegen.*
