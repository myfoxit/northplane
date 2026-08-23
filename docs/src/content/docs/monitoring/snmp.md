---
title: SNMP
description: Poll devices with the builtin snmp and snmp-walk checks (v1/v2c/v3), receive SNMP traps through the snmp-trap event source, and turn Cisco counters and traps into metrics and alerts.
sidebar:
  order: 5
---

Northplane covers SNMP in two directions: **polling** with the builtin `snmp` / `snmp-walk`
checks (GET of one OID, or the row count of a walk), and **traps** received by the `snmp-trap`
event source, which become events and — through alert rules — alerts. There is no MIB compiler:
OIDs are numeric everywhere, in checks, in trap events and in labels.

## Polling with builtin:snmp

One service = one OID. The value is graded with Nagios ranges and stored as a metric.

```yaml
kind: Host
metadata: {name: r1, labels: {role: router, vendor: cisco}}
spec:
  address: 172.30.0.31
  checkCommand: builtin:icmp
---
kind: Service
metadata: {name: uptime, host: r1}
spec:
  checkCommand: builtin:snmp
  args: ["-v", "2c", "-C", "$SECRET:snmp-ro$", "-o", "1.3.6.1.2.1.1.3.0", "-l", "sysUpTime"]
```

The flag reference (all aliases and defaults) is on the
[builtin checks page](/docs/monitoring/builtin-checks/#snmp); the essentials:

| Flag | Meaning |
|---|---|
| `-H host[:port]` | agent address; defaults to the object address. A Nagios-style `host:161` address works — the embedded port is used unless `-p` is given |
| `-o OID` | the OID to GET (required) |
| `-v 1` / `-v 2c` / `-v 3` | protocol; default `2c` |
| `-C community` | v1/v2c community (default `public`); use `$SECRET:name$` |
| `-U user --seclevel authPriv --authproto SHA256 -A pass --privproto AES -X pass` | SNMPv3 USM; `--seclevel` is `authPriv`, `authNoPriv` or (anything else) noAuthNoPriv; auth protocols `MD5`, `SHA`, `SHA256`, `SHA512`; priv `DES`, `AES`, `AES256` |
| `-l label`, `--unit %` | perfdata label (default `value`) and unit suffix |
| `-w`, `-c` | ranges on numeric values |
| `-e text` | expected substring for string (OctetString) values |
| `-t 10s`, `--retries 1` | timeout and retries |

How values are interpreted:

- Integer, Gauge, Counter, TimeTicks, Counter64 and numeric strings → graded, output
  `<LABEL> OK - <oid> = <value><unit> | <label>=<value><unit>;<w>;<c>;;`.
- OctetString → `SNMP OK - <oid> = "text"` without perfdata; with `-e`, a missing substring is CRITICAL.
- `NoSuchObject` / `NoSuchInstance` → CRITICAL (`SNMP: no such OID`). This is useful: an OSPF
  neighbour entry that disappears is a CRITICAL by itself.
- Other types (IP addresses, OIDs) → UNKNOWN `non-numeric type`.
- Transport errors (timeout, connection) → CRITICAL.

Counters (`ifInOctets` and friends) are stored as absolute values; charts and dashboards derive rates
when the perfdata unit is `c` — which `builtin:snmp` does not set, so for throughput graphs prefer
rates the device already computes, or accept absolute counters. See
[Metrics and NP-TSDB](/docs/monitoring/metrics-and-tsdb/).

## Row counts with builtin:snmp-walk

`snmp-walk` BulkWalks a subtree and grades the **number of rows** — "how many interfaces", "how
many BGP peers", "how many entries in the ARP table". Values are not inspected.

```yaml
kind: Service
metadata: {name: interfaces, host: r1}
spec:
  checkCommand: builtin:snmp-walk
  args: ["-C", "$SECRET:snmp-ro$", "-o", "1.3.6.1.2.1.2.2.1.8", "-c", "4:"]
  interval: 5m
```

Output `ROWS OK - walk 1.3.6.1.2.1.2.2.1.8 returned 12 rows | rows=12;;;;`; default timeout 20 s
(walks of large tables need a larger `-t` and object `timeout`).

## Cisco examples

These services come from the lab routers behind the doktrace.com instance (Cisco IOSv): CPU and
memory from the Cisco enterprise MIBs, interface state from IF-MIB, OSPF adjacency from OSPF-MIB.
Replace community, interface indices and the neighbour address for your devices.

```yaml
kind: Template
metadata: {name: cisco-snmp}
spec:
  kind: service
  checkCommand: builtin:snmp
  interval: 60s
  timeout: 15s
---
kind: Service
metadata: {name: cpu-1min, host: r1}
spec:
  templates: [cisco-snmp]
  args: ["-C", "$SECRET:snmp-ro$", "-o", "1.3.6.1.4.1.9.2.1.57.0", "-l", "cpu1m", "--unit", "%", "-w", "70", "-c", "90"]
---
kind: Service
metadata: {name: cpu-5min, host: r1}
spec:
  templates: [cisco-snmp]
  args: ["-C", "$SECRET:snmp-ro$", "-o", "1.3.6.1.4.1.9.2.1.58.0", "-l", "cpu5m", "--unit", "%", "-w", "60", "-c", "80"]
---
kind: Service
metadata: {name: memory-free, host: r1}
spec:
  templates: [cisco-snmp]
  # ciscoMemoryPoolFree of pool 1 (processor), bytes; alert when it drops BELOW the bound
  args: ["-C", "$SECRET:snmp-ro$", "-o", "1.3.6.1.4.1.9.9.48.1.1.1.6.1", "-l", "memfree", "--unit", "B", "-w", "52428800:", "-c", "20971520:"]
---
kind: Service
metadata: {name: if-gi0-1-link, host: r1, labels: {link: r1-r2}}
spec:
  templates: [cisco-snmp]
  # ifOperStatus.<ifIndex>: 1 = up; anything else is CRITICAL
  args: ["-C", "$SECRET:snmp-ro$", "-o", "1.3.6.1.2.1.2.2.1.8.2", "-l", "ifOperStatus", "-c", "1:1"]
---
kind: Service
metadata: {name: ospf-neighbor-r2, host: r1, labels: {link: r1-r2}}
spec:
  templates: [cisco-snmp]
  # ospfNbrState.<neighbour-ip>.0: 8 = full. The row disappears when the
  # adjacency drops -> "no such OID" -> CRITICAL, which is the desired result.
  args: ["-C", "$SECRET:snmp-ro$", "-o", "1.3.6.1.2.1.14.10.1.6.10.0.12.2.0", "-l", "ospfNbrState", "-c", "8:8"]
```

Notes on the thresholds: `-c 1:1` means "critical unless exactly 1"; `52428800:` means "warning
below 50 MiB" (the range grammar alerts *outside* the range; a lower bound with an open end). Write
byte thresholds as plain numbers — a unit suffix in a range (`50MB:`) is stripped, not scaled.
During a link-flap drill in the lab both the polls (ifOperStatus, OSPF) and the traps (below) caught
the outage; the polls need up to one `interval` to notice, traps arrive within a second.

## Receiving SNMP traps

Traps are configured as an **event source** of type `snmp-trap` (**Admin → Event sources**
(Event-Quellen), `/api/v1/event-sources`, bundle kind `EventSource`). Northplane opens one UDP
listener per distinct `listen` address across all tenants and sources; several sources may share an
address and are told apart by community (v1/v2c) or USM user (v3). The reconcile loop picks up
changes every 30 s; a failing bind is logged (`listener start failed`), not fatal.

```yaml
kind: EventSource
metadata: {name: cisco-traps}
spec:
  type: snmp-trap
  enabled: true
  config:
    listen: "udp://:9162"
    community: public
    severity: warning
  labels: {src: cisco-traps, team: netops}
```

| Config key | Default | Meaning |
|---|---|---|
| `listen` | `udp://:9162` | gosnmp listen URL (`udp://<ip>:<port>`); the classic port 162 needs root or `CAP_NET_BIND_SERVICE` |
| `community` | `public` | community accepted for v1/v2c |
| `severity` | `warning` | severity for enterprise/unknown traps (`critical`, `warning`, `info`, `ok`) |
| `v3User` | — | USM user; setting it switches the listener into v3-capable mode (v1/v2c are still received) |
| `v3SecLevel` | inferred | `noAuthNoPriv`, `authNoPriv`, `authPriv`; when empty, inferred from which passphrases are set |
| `v3AuthProto` | `SHA` | `MD5`, `SHA`, `SHA224`, `SHA256`, `SHA384`, `SHA512` |
| `v3AuthPass` | — | auth passphrase (inline) |
| `v3PrivProto` | `AES` | `DES`, `AES`, `AES192`, `AES256` |
| `v3PrivPass` | — | privacy passphrase (inline) |

Event-source fields that apply as well: `enabled` (must be true), `labels` (merged into every
event), `rateLimit`/`burst` (token bucket, default 50 events/s with burst 200; excess traps are
dropped silently with a debug log). `authMode`, `secretRef` and `mapping` are not used by traps.

:::caution[SNMPv3 passphrases are inline]
The listener reads `v3AuthPass`/`v3PrivPass` from the source config; it does not resolve secret
references. The Admin dialog writes `v3AuthSecretRef`/`v3PrivSecretRef`, which the listener ignores
— enter `v3AuthPass`/`v3PrivPass` under "Further settings" (Weitere Einstellungen) or set them
via the API/bundle.
:::

:::caution[Docker: publish the UDP port]
The shipped compose files publish only `8443/tcp` (or 80/443 through Caddy). Add
`- "9162:9162/udp"` to the `northplane` service's `ports` (or `162:9162/udp`), and point your devices
at the host. A trap source that is `enabled` but whose port is not reachable shows `received: 0`
forever — check with `tcpdump -ni any udp port 9162` on the host.
:::

### From trap to event

1. The packet must match a source on that listener: community equal to `community` (v1/v2c),
   or USM user equal to `v3User` (v3; gosnmp has already authenticated/decrypted). Otherwise the
   trap is dropped (debug log).
2. The trap OID is taken from `snmpTrapOID.0` (v2c/v3) or synthesised from the v1 generic/specific
   fields (`enterprise.0.specific` for generic 6).
3. Classification: `coldStart`, `warmStart`, `linkUp` → `ok`; `linkDown` → `critical`;
   `authenticationFailure`, `egpNeighborLoss` → `warning`; everything else → the source's `severity`.
4. An event of type `ingress` is stored and published to the alert rules:

| Event field | Value |
|---|---|
| `summary` | `SNMP linkDown from 172.30.0.31` (well-known traps) or `SNMP trap 1.3.6.1.4.1.9.9.43.2.0.1 from 172.30.0.31` |
| `severity` | from the classification above |
| `dedupKey` | `<source name>/<agent IP>/<trap OID>` |
| `labels` | source labels + `source` (source name), `agent` (IP), `trapOid`, and up to 20 varbinds keyed by OID (values truncated to 200 chars); trap labels win over source labels on collision |
| `payload` | `{"version","trapOid","community","varbinds":[{"oid","type","value"}]}`, capped at about 16 KiB |

To verify reception, open **Events** and filter for type `ingress` (or
`GET /api/v1/events?types=ingress&sourceId=<source id>`); every accepted trap shows up there
within a second. The listener keeps counters (listeners, received, dropped, emitted) internally,
but they are not exposed through the API.

### Alerting on traps

Traps only become alerts through [alert rules](/docs/alarming/alert-rules/). Because the default
dedup key contains the trap OID, `linkDown` and `linkUp` from the same device would produce
*different* keys and the `linkUp` (severity `ok`) would not close the `linkDown` alert. Give the
rule a dedup key that identifies the device (or the link) instead:

```yaml
kind: AlertRule
metadata: {name: cisco-linkdown}
spec:
  match: 'event.type == "ingress" && event.labels.source == "cisco-traps" && event.labels.trapOid == "1.3.6.1.6.3.1.1.5.3"'
  severity: critical
  title: '{{ .event.summary }}'            # "SNMP linkDown from 172.30.0.31"
  dedupKey: 'link/{{ .event.labels.agent }}'
  escalationPolicy: netops
---
kind: AlertRule
metadata: {name: cisco-other-traps}
spec:
  match: 'event.type == "ingress" && event.labels.source == "cisco-traps" && !(event.labels.trapOid in ["1.3.6.1.6.3.1.1.5.3", "1.3.6.1.6.3.1.1.5.4"])'
  severity: warning
  title: '{{ .event.summary }}'
```

The `linkUp` trap is classified `ok`; for every rule it does *not* match, the engine re-renders
that rule's dedup key with the event and resolves the alert carrying it (resolve-on-OK, on by
default). With the key above the `linkUp` from the same agent closes the `linkDown` alert — and so
does any other `ok` trap from that agent (`coldStart` after a reboot), which is usually what you
want; links that stay down after the reboot re-open the alert with the next `linkDown`.

Varbinds are labels keyed by their **instance OID**, e.g. `1.3.6.1.2.1.2.2.1.1.2` = `2` for
`ifIndex.2` in a `linkDown`. For one rule per link, match on that key —
`match: '… && "1.3.6.1.2.1.2.2.1.1.2" in event.labels'` with `dedupKey: 'link/{{ .event.labels.agent }}/2'`
— and read values in templates with `index`: `{{ index .event.labels "1.3.6.1.2.1.2.2.1.8.2" }}`
(ifOperStatus.2). `event.labels.source` is the source **name**, `event.source` the source id.
Every configuration write on a Cisco device (`write memory`) sends a `ciscoConfigMan` trap, which
lands in the "other traps" rule — expected, and a nice audit trail.

### Sending traps from Cisco IOS

```text
snmp-server community <ro-community> RO
snmp-server enable traps snmp linkdown linkup coldstart warmstart authentication
snmp-server host 10.10.10.11 version 2c public udp-port 9162
```

The `udp-port` keyword must follow the community string. If the management interface lives in a
VRF, tell IOS (`snmp-server host … vrf <name> …` where supported); on the lab's IOSv images that
option was silently ignored and traps only flowed once the management interface left the VRF —
when traps stay at zero while polling works, capture on the receiver before suspecting Northplane.

## SNMP gotchas

- `-C` is the community for `snmp`; for the `nrpe` check `-C` is the command — do not mix up
  the conventions when copying args between services.
- `-v 3` without `--seclevel authPriv`/`authNoPriv` silently sends noAuthNoPriv; an
  `authpass` is ignored in that case.
- A numeric string value (e.g. a temperature sensor exporting `"42"`) is graded like a number;
  a value of type OctetString containing text needs `-e`.
- Ranges: `-c 1:1` (exactly 1), `-c 8:8` (exactly 8), `-w 52428800:` (not below) — unit suffixes
  are stripped.
- Large walks: raise `-t` **and** the object `timeout` (default 30 s), otherwise the object timeout
  wins and the result is UNKNOWN.
- The trap listener does not resolve MIBs; v1 traps and v2c/v3 notifications are normalised to
  the same event shape (v1 generic traps are mapped to the equivalent v2c trap OIDs).
- Two instances listening on the same host and port cannot share the UDP socket — run the trap
  receiver on one instance (or bind distinct `listen` addresses).
