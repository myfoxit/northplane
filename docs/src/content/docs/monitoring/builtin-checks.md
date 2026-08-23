---
title: Builtin checks
description: Reference of the 17 builtin in-process checks — flags, defaults, threshold ranges, output and perfdata formats, examples and gotchas.
sidebar:
  order: 2
---

Builtin checks run inside `northplaned` — no fork, no plugin binary, thousands in parallel.
You reference them as `checkCommand: builtin:<name>` and pass monitoring-plugins-style flags
in the object's `args` array. This page is the complete reference; how objects, templates and
scheduling fit together is described in [Object model](/docs/concepts/object-model/) and
[Checks and states](/docs/concepts/checks-and-states/).

## Available checks

| Name | Aliases | Purpose |
|---|---|---|
| [icmp](#icmp-and-ping) | `ping` | ICMP echo round-trip time |
| [tcp](#tcp) | | TCP connect, optional TLS, send/expect |
| [ssh-banner](#ssh-banner) | | SSH identification string |
| [smtp](#smtp) | | SMTP greeting + EHLO, optional STARTTLS |
| [imap](#imap) | | IMAP greeting, optional implicit TLS |
| [ntp](#ntp) | | SNTP clock offset |
| [dns](#dns) | | resolve a name, compare the answer |
| [http](#http-and-https) | `https` | HTTP(S) request: status, body, response time, cert days |
| [tls-cert](#tls-cert-and-cert) | `cert` | certificate expiry in days |
| [http-flow](#http-flow) | | multi-step HTTP transaction with variable extraction |
| [nrpe](#nrpe) | | query a remote NRPE daemon |
| [snmp](#snmp) | | SNMP GET of one OID, graded |
| [snmp-walk](#snmp-walk) | | SNMP BulkWalk, graded on the row count |
| [agent](#agent) | | query an np-agent active listener |

That is 17 registered names (`agent, cert, dns, http, http-flow, https, icmp, imap, nrpe, ntp, ping, smtp, snmp, snmp-walk, ssh-banner, tcp, tls-cert`). `GET /api/v1/check-commands:builtins` returns the same list
([API reference](/docs/reference/api/operations/get_check_commands_builtins/)).

## How a builtin check is invoked

- `checkCommand: builtin:<name>`; `args` is a list of tokens, **one flag or value per element**
  (`["-p", "443"]`, never `["-p 443"]`). The token `-p 443` as a single element is parsed as
  flag `p` with the value ` 443`, which is not a number.
- The target is the object's effective `address`. For a **service** the target is the host's
  address (falling back to the host name). `-H` / `--hostname` / `--host` overrides it.
- Every check is bounded by the object's effective `timeout` (default 30 s) in addition to its own
  `-t`. A timed-out builtin returns UNKNOWN.
- Macros such as `$HOSTADDRESS$`, `$ARGn$`, `$_HOSTFOO$` and `$SECRET:name$` are expanded in
  `args` before parsing (see [Plugins and Nagios compatibility](/docs/monitoring/plugins-and-nagios/#macros)).
- A named `CheckCommand` of type `builtin` works too: `line: ["http", "-S", "-u", "/health"]`
  (`line[0]` is the check name, the rest are flags; the object's `args` are then only available as `$ARGn$`).

### Flag syntax

The parser follows monitoring-plugins conventions:

| Form | Parsed as |
|---|---|
| `--name=value` | `name` = `value` |
| `--name value` | `name` = `value`, unless `value` starts with `-` (then `name` = `true` and the next token is parsed on its own) |
| `--name` | `name` = `true` (boolean flag) |
| `-x value` | `x` = `value`; a value starting with `-` is accepted only when it is a number (`-w -5`) |
| `-x` | `x` = `true` |
| `-p80` | `p` = `80` (glued short flag) |
| anything else | positional, ignored by all current checks |

Boolean flags accept `true`, `1`, `yes`. Durations accept `5s`, `500ms` or bare seconds (`2.5`).
When a flag is repeated the last occurrence wins. Each check reads specific aliases (listed in the
tables below as **Flag** and **Alias**); an alias that is not listed is not recognised — for example
`ssh-banner` understands `-w` but not `--warning`.

### Thresholds: the Nagios range grammar

`-w` and `-c` are Nagios ranges. A range **alerts when the value lies outside** `[start, end]`;
the `@` prefix inverts that.

| Range | Alerts when | Note |
|---|---|---|
| `10` | value < 0 **or** value > 10 | start defaults to 0 |
| `10:` | value < 10 | end = +∞ |
| `~:10` | value > 10 | `~` = −∞ as start |
| `5:8` | value outside 5…8 | |
| `@5:8` | value inside 5…8 | inverted |
| `80%`, `5948MB` | like `80`, `5948` | a unit suffix is **stripped, not scaled** — `50MB:` means `50:` |
| `8:5` | never — error | `start > end` → `UNKNOWN - bad threshold` |

The critical range is checked first, then warning. Checks with built-in defaults (`icmp`, `ntp`,
`tls-cert`) fill in the default for each of `-w`/`-c` you omit; the others treat a missing
`-w`/`-c` as "no threshold" and only report the value.

### Output and perfdata

Most checks produce `<NAME> <STATE> - <text> | <label>=<value><unit>;<warn>;<crit>;;`, for
example `TIME OK - connected to 10.0.0.5:5432 in 0.003s | time=0.003123s;;;;`. Labels containing
spaces are single-quoted (`'disk /'=42%;85;95;0;100`). Every perfdata token becomes a series in
[NP-TSDB](/docs/monitoring/metrics-and-tsdb/) and the warn/crit fields are used for threshold
bands in charts and meters. Several checks write only the short `-w`/`-c` spellings into the
perfdata fields (noted per check) — use those if you want the thresholds to show up in charts.

### Testing a check

`POST /api/v1/check-commands:test` (permission `checks:run`) runs a builtin once against an
address without creating an object
([API reference](/docs/reference/api/operations/post_check_commands_test/)):

```bash
curl -sS -X POST "$NP_SERVER/api/v1/check-commands:test" \
  -H "Authorization: Bearer $NP_TOKEN" -H "Content-Type: application/json" \
  -d '{"builtin":"http","address":"10.0.0.5","args":["-S","-u","/health","-e","200"]}'
# {"state":0,"label":"OK","output":"HTTP OK - 200 OK https://10.0.0.5/health in 0.012s, 15 bytes, cert expires in 80d","perfdata":"time=0.012000s;;;0; size=15B;;;0; cert_days=80;;;;","tookMs":13}
```

The test endpoint does **not** expand macros and passes no object `vars` (so `http-flow` needs
`--flow`). There is no UI for it; on a running object use **Check now** (Jetzt prüfen) in the
object detail page or `np check-now <object-id>`.

## icmp and ping

Native ICMP echo, one request. Uses an unprivileged datagram ICMP socket (`udp4`) where the
platform allows it and falls back to a raw socket (root or `cap_net_raw`). IPv4 only.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | target (resolved with `ip4`) |
| `-t` | `--timeout` | `5s` | reply timeout |
| `-w` | `--warning` | `200` | RTT range in **milliseconds** |
| `-c` | `--critical` | `1000` | RTT range in **milliseconds** |

Output: `PING OK - 10.0.0.1 rta 1.23ms | rta=1.234567ms;200;1000;;`.
No reply → `CRITICAL - no reply from <host> within 5s`; name resolution failure → CRITICAL;
no usable socket → `UNKNOWN - icmp socket: … (unprivileged ICMP unavailable — run as root, grant cap_net_raw, or use builtin:tcp)`.

```yaml
spec:
  checkCommand: builtin:icmp
  args: ["-w", "100", "-c", "500", "-t", "2s"]
```

:::caution[Containers and unprivileged users]
The Docker image runs `northplaned` as uid 65532. On Linux, unprivileged ICMP datagram sockets
work only when the kernel setting `net.ipv4.ping_group_range` covers the process group; if the check
reports `icmp socket`, widen that sysctl, grant `CAP_NET_RAW`, or monitor the host with
`builtin:tcp` instead.
:::

## tcp

TCP connect with optional TLS, a send string and an expected substring.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | target |
| `-p` | `--port` | — (**required**) | port; missing → `UNKNOWN - tcp: -p port required` |
| `-t` | `--timeout` | `10s` | dial, read and write timeout |
| `-S` | `--ssl` | off | TLS connect (SNI = host) |
| `--insecure` | | off | skip certificate verification |
| `-s` | `--send` | — | string sent after connect, terminated with CRLF |
| `-e` | `--expect` | — | substring that must appear in the first 4096 bytes read |
| `-w` | `--warning` | — | connect-time range in seconds |
| `-c` | `--critical` | — | connect-time range in seconds |

Output: `TIME OK - connected to 10.0.0.5:5432 in 0.003s | time=0.003123s;;;;`.
Connect failure → CRITICAL; `-e` mismatch → `CRITICAL - unexpected response from host:port: "…"`.

```yaml
spec:
  checkCommand: builtin:tcp
  args: ["-p", "6379", "-s", "PING", "-e", "+PONG", "-w", "0.2", "-c", "1"]
```

## ssh-banner

Connects and reads one line, which must start with `SSH-`.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | target |
| `-p` | `--port` | `22` | port |
| `-t` | `--timeout` | `10s` | connect/read timeout |
| `-w` | — | — | time range in seconds |
| `-c` | — | — | time range in seconds |

Output: `TIME OK - SSH-2.0-OpenSSH_9.6 (0.012s) | time=0.012345s;;;;`.
Non-SSH answer → `CRITICAL - not an SSH service at host:22: "…"`.

```yaml
spec:
  checkCommand: builtin:ssh-banner
  args: ["-p", "2222"]
```

## smtp

Expects a `220` greeting, sends `EHLO northplane.monitor`, expects `250` (multi-line replies are
handled), optionally upgrades with STARTTLS, then sends `QUIT`.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | target |
| `-p` | `--port` | `25` | port |
| `-t` | `--timeout` | `15s` | session timeout |
| `-S` | `--starttls` | off | issue STARTTLS after EHLO (**not** implicit TLS on 465) |
| `--insecure` | | off | skip certificate verification after STARTTLS |
| `-w` | — | — | time range in seconds |
| `-c` | — | — | time range in seconds |

Output: `TIME OK - SMTP mail.example.org:25 responsive (0.120s) | time=0.12s;;;;`.
Bad greeting, rejected EHLO, rejected STARTTLS or a failed handshake → CRITICAL.

```yaml
spec:
  checkCommand: builtin:smtp
  args: ["-p", "587", "-S", "-w", "2", "-c", "5"]
```

:::note
There is no implicit-TLS mode for port 465. Use `builtin:tcp -p 465 -S -e 220` for an SMTPS port.
:::

## imap

Expects a greeting starting with `* OK`.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | target |
| `-S` | `--ssl` | off | implicit TLS |
| `-p` | `--port` | `143`; **`993` when `-S` is set** | port |
| `--insecure` | | off | skip certificate verification |
| `-t` | `--timeout` | `15s` | connect/read timeout |
| `-w` | — | — | time range in seconds |
| `-c` | — | — | time range in seconds |

Output: `TIME OK - IMAP mail.example.org:993 responsive (0.080s) | time=0.08s;;;;`.

```yaml
spec:
  checkCommand: builtin:imap
  args: ["-S"]
```

:::caution[Port switch with -S]
The port switch to 993 applies whenever the port *is* 143 — also when you passed `-p 143`
explicitly. To test STARTTLS-less plaintext on 143 simply omit `-S`; to use TLS on a custom
port pass `-S -p <port>`.
:::

## ntp

One SNTP v4 exchange on UDP port 123 (fixed, no `-p`). Offset = ((t2−t1)+(t3−t4))/2, graded
on its **magnitude**.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | NTP server |
| `-t` | `--timeout` | `10s` | reply timeout |
| `-w` | `--warning` | `0.5` | \|offset\| range in seconds |
| `-c` | `--critical` | `2` | \|offset\| range in seconds |

Output: `OFFSET OK - clock offset 0.0012s against pool.ntp.org | offset=0.0012s;0.5;2;;`
(the text shows the signed offset, perfdata the magnitude). No response → CRITICAL.

```yaml
spec:
  checkCommand: builtin:ntp
  args: ["-w", "0.1", "-c", "1"]
```

## dns

Resolves a name and optionally compares the answer.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | **the name to resolve** |
| `--server` | `-s` | system resolver | DNS server to query, `host` or `host:port` (`:53` is appended) |
| `--type` | `-q` | `A` | `A`, `AAAA`, `CNAME`, `MX`, `TXT`; anything else → UNKNOWN |
| `-a` | `--expect` | — | expected value; exact match against any returned value |
| `-w` | — | — | lookup-time range in seconds |
| `-c` | — | — | lookup-time range in seconds |

Output: `TIME OK - A example.org → 93.184.216.34 (0.020s) | time=0.02s;;;;` (values sorted).
Lookup error, no records or `-a` mismatch → CRITICAL.

```yaml
# "Does our resolver 10.0.0.2 answer correctly for www.example.org?"
spec:
  checkCommand: builtin:dns
  args: ["-H", "www.example.org", "-s", "10.0.0.2", "-a", "203.0.113.10", "-w", "0.5", "-c", "2"]
```

:::caution[The address is the name, not the server]
Without `-H` the object's address is used as the *name to resolve*. To monitor a DNS server,
put the server in `-s` and the name in `-H` (or make the object address the name).
:::

## http and https

`https` is the **same function** as `http`. TLS is used only when `-S`/`--ssl` is given or `-u`
is a full `https://…` URL — `builtin:https -u /` against a host checks plain `http://host/`.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | host (required even with a full URL: `UNKNOWN - http: no host`) |
| `-u` | `--uri`, `--url` | `/` | path, or a full `http://`/`https://` URL (used verbatim, host ignored) |
| `-p` | `--port` | scheme default | port appended to the host |
| `-S` | `--ssl` | off | use `https://` |
| `-t` | `--timeout` | `15s` | whole request |
| `--insecure` | `--k-insecure` | off | skip certificate verification |
| `--max-redirects` | | `5` | redirects to follow; more → `CRITICAL - … stopped after N redirects` |
| `--method` | `-j` | `GET` (`POST` when `--post` is set) | HTTP method |
| `--post` | `-P` | — | request body |
| `-a` | `--authorization` | — | `user:pass` basic auth |
| `--bearer` | | — | `Authorization: Bearer <token>` |
| `--header` | `-k` | — | one `Name: value` header (the last `--header` wins) |
| `-e` | `--expect` | — | expected status; substring or prefix of e.g. `200 OK` |
| `-r` | `--regex` | — | body must match this Go (RE2) regex |
| `-s` | `--string` | — | body must contain this substring |
| `-w` | `--warning` | — | response-time range in seconds |
| `-c` | `--critical` | — | response-time range in seconds |

State logic: without `-e`, status ≥ 400 → CRITICAL and ≥ 300 → WARNING (only reached when a
redirect is not followed); with `-e`, a mismatch → CRITICAL. `-r`, `-s` and the time thresholds are
evaluated only while the result is still OK. The body is read up to 1 MiB; keep-alives are off;
User-Agent `Northplane/1.0 (check_http-compatible)`.

Output: `HTTP OK - 200 OK https://example.org/ in 0.123s, 1234 bytes, cert expires in 80d | time=0.123000s;;;0; size=1234B;;;0; cert_days=80;;;;`
— the `cert expires` part and `cert_days` only on TLS connections; the perfdata warn/crit fields are
filled from `-w`/`-c` (short forms only).

```yaml
spec:
  checkCommand: builtin:http
  args: ["-S", "-u", "/api/health", "-e", "200", "-r", "\"status\":\"ok\"", "-w", "1", "-c", "3"]
```

```yaml
# full URL, bearer token, POST
spec:
  checkCommand: builtin:http
  args: ["-u", "https://api.example.org/graphql", "--method", "POST", "--post", "{\"query\":\"{ ping }\"}",
         "--header", "Content-Type: application/json", "--bearer", "$SECRET:api-probe$", "-e", "200"]
```

:::caution[SSRF guard]
Connections to link-local addresses (`169.254.0.0/16` including the cloud metadata endpoint,
`fe80::/10`) are refused — also for redirect targets and after DNS resolution. RFC 1918 and
loopback targets are allowed.
:::

## tls-cert and cert

Connects with TLS and reports the **leaf certificate's** remaining days. The chain is not
validated (`InsecureSkipVerify` is always on) — this check is about expiry, use `http -S` for
validity.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | target |
| `-p` | `--port` | `443` | port |
| `-t` | `--timeout` | `10s` | connect timeout |
| `--servername` | | host | SNI name |
| `-w` | `--warning` | `30` | **plain integer** days; `days <= w` → WARNING |
| `-c` | `--critical` | `7` | **plain integer** days; `days <= c` → CRITICAL |

Output: `CERT OK - example.org expires 2026-12-01 (99 days), issuer "R3" | cert_days=99;30;7;;`.
Connect failure or no certificate → CRITICAL. The thresholds are not ranges; a value such as
`30:` silently falls back to the default.

```yaml
spec:
  checkCommand: builtin:tls-cert
  args: ["-p", "8443", "--servername", "vpn.example.org", "-w", "21", "-c", "7"]
```

## http-flow

A multi-step HTTP transaction (login → fetch → logout). The steps come from the service var
`flow` (a JSON array) or from `--flow <json>` (the flag wins). Missing steps → UNKNOWN.
A cookie jar is shared across steps per host; extracted variables are interpolated as `${name}`
in later URLs, headers and bodies. The object address is not used — step URLs are absolute.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `--flow` | | var `flow` | JSON array of steps |
| `-t` | `--timeout` | `60s` | timeout for the whole flow |
| `--insecure` | | off | skip certificate verification |
| `-w` | — | — | **total** time range in seconds |
| `-c` | — | — | **total** time range in seconds |

Step fields:

| Field | Default | Meaning |
|---|---|---|
| `name` | `stepN` | label in text and perfdata |
| `method` | `GET` | HTTP method |
| `url` | required | absolute URL, may contain `${var}` |
| `headers` | — | map; values are interpolated |
| `body` | — | request body, interpolated |
| `expectStatus` | `200` | exact status code |
| `expectRegex` | — | body regex (RE2) |
| `maxSeconds` | — | per-step limit; exceeding it → CRITICAL |
| `extract` | — | `{"var": "<regex with one capture group>"}` or `{"var": "json:path.to.field"}` (dot path, numeric array indices) |

Output: `FLOW OK - 3 steps in 0.912s (login 0.301s, me 0.210s, logout 0.401s) | login=0.301000s;;;0; me=0.210000s;;;0; logout=0.401000s;;;0; total=0.912000s;2;5;0;`.
Any failed assertion → `CRITICAL - step "login": status 500 (expected 200)`; an invalid regex → UNKNOWN.

```yaml
spec:
  checkCommand: builtin:http-flow
  args: ["-w", "2", "-c", "5"]
  vars:
    flow: '[{"name":"login","method":"POST","url":"https://app.example.org/login","body":"u=probe&p=secret","extract":{"token":"json:data.token"}},{"name":"me","url":"https://app.example.org/me","headers":{"Authorization":"Bearer ${token}"},"expectRegex":"\"id\""}]'
```

## nrpe

Queries a remote NRPE daemon (v2 fixed-size packets or v3/v4 variable-length) and passes the
remote plugin's state and output through. Northplane is an NRPE **client** only.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | NRPE host |
| `-C` | `--command` | `_NRPE_CHECK` | remote command name |
| `-a` | `--args` | — | comma-separated arguments, joined with `!` for the daemon |
| `-p` | `--port` | `5666` | port |
| `-V` | `--nrpe-version` | `2` | `2` (1036-byte packets) or `3`/`4` |
| `-t` | `--timeout` | `10s` | query timeout |
| `-n` | `--no-ssl` | off (TLS on) | plaintext TCP |
| `--insecure` | | off | accept any certificate in TLS mode |

Transport errors (connection refused, CRC, handshake) → `UNKNOWN - nrpe: …`; otherwise the
state is the remote plugin's exit code and the output is parsed like any plugin output
(perfdata included).

```yaml
spec:
  checkCommand: builtin:nrpe
  args: ["-C", "check_disk", "--args=-w 20%,-c 10%,-p /", "-n", "-t", "15"]
```

:::caution[-C, not -c — and no anonymous DH]
The command flag is **`-C`** (`-c` would be the critical threshold elsewhere and is ignored here).
TLS mode verifies certificates: Go refuses the anonymous-DH ciphers `check_nrpe` uses by default,
so either run the daemon with real certificates or use `-n` and configure the daemon for
plaintext (`nrpe -n`, `allow_v2_packets=1` for the default `-V 2`).
Remote arguments usually start with `-`; pass them as `--args=-w 20%,-c 10%` — with the
two-token form `-a -w 20%` the parser treats `-a` as a boolean and drops the value.
:::

## snmp

GET of one OID with SNMP v1, v2c or v3 and grading of the value. Details, Cisco examples and traps
are on the [SNMP page](/docs/monitoring/snmp/).

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | agent; `host:port` is accepted (the embedded port is used unless `-p` is given) |
| `-o` | `--oid` | — (**required**) | OID to get; missing → `UNKNOWN - snmp: -o OID required` |
| `-p` | `--port` | `161` | UDP port |
| `-C` | `--community` | `public` | v1/v2c community |
| `-v` | `--protocol` | `2c` | `1`, `2c`, `3` (any other value behaves as `2c`) |
| `--retries` | | `1` | retries |
| `-t` | `--timeout` | `10s` | per-request timeout |
| `--seclevel` | | noAuthNoPriv | v3: `authPriv`, `authNoPriv`; anything else = noAuthNoPriv |
| `--user` | `-U` | — | v3 user name |
| `--authproto` | | `SHA` | `MD5`, `SHA256`, `SHA512`, else `SHA` |
| `--authpass` | `-A` | — | v3 auth passphrase |
| `--privproto` | | `AES` | `DES`, `AES256`, else `AES` |
| `--privpass` | `-X` | — | v3 priv passphrase |
| `-l` | `--label` | `value` | perfdata label (and text prefix) |
| `--unit` | | — | unit suffix for text and perfdata |
| `-w` | `--warning` | — | range on numeric values |
| `-c` | `--critical` | — | range on numeric values |
| `-e` | `--expect`, `-s` | — | substring expected in an OctetString value |

Result mapping: a numeric value (Integer, Counter, Gauge, TimeTicks, Counter64, numeric strings) is
graded and reported as `<LABEL> OK - <oid> = <value><unit> | <label>=<value><unit>;<w>;<c>;;`;
an OctetString yields `SNMP OK - <oid> = "text"` without perfdata (CRITICAL when `-e` does not
match); `NoSuchObject`/`NoSuchInstance` → `CRITICAL - SNMP: no such OID`; other types → UNKNOWN;
connect/get errors → CRITICAL.

```yaml
# Cisco CPU, 1-minute average, in percent
spec:
  checkCommand: builtin:snmp
  args: ["-v", "2c", "-C", "$SECRET:snmp-ro$", "-o", "1.3.6.1.4.1.9.2.1.57.0",
         "-l", "cpu1m", "--unit", "%", "-w", "70", "-c", "90"]
```

```yaml
# SNMPv3 authPriv
spec:
  checkCommand: builtin:snmp
  args: ["-v", "3", "--seclevel", "authPriv", "-U", "monitor", "--authproto", "SHA256",
         "-A", "$SECRET:snmp-auth$", "--privproto", "AES", "-X", "$SECRET:snmp-priv$",
         "-o", "1.3.6.1.2.1.1.3.0", "-l", "uptime"]
```

## snmp-walk

BulkWalk of a subtree; the **number of rows** is graded. Same client flags as `snmp`
(`-H`, `-p`, `-C`, `-v`, `--retries`, v3 flags), except:

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-o` | `--oid` | — (**required**) | base OID |
| `-t` | `--timeout` | `20s` | per-request timeout |
| `-w` | `--warning` | — | range on the row count |
| `-c` | `--critical` | — | range on the row count |

Output: `ROWS OK - walk 1.3.6.1.2.1.2.2.1.8 returned 12 rows | rows=12;;;;`. Walk errors →
CRITICAL. Values are not inspected — for "is interface X up" use `snmp` on the specific instance.

```yaml
# at least 4 interfaces present (ifOperStatus column)
spec:
  checkCommand: builtin:snmp-walk
  args: ["-o", "1.3.6.1.2.1.2.2.1.8", "-C", "public", "-c", "4:"]
```

## agent

Queries the HTTPS listener of [np-agent](/docs/monitoring/agent/#active-listener-mode)
(NCPA-style, "server pulls"). Three modes: summary (default), one graded metric, or a remote
plugin defined in the agent's `agent.yaml`.

| Flag | Alias | Default | Meaning |
|---|---|---|---|
| `-H` | `--hostname`, `--host` | object address | agent host (`UNKNOWN - agent: no host` when empty) |
| `--token` | | — (**required**) | bearer = the agent's `listenToken`; use `$SECRET:…$` |
| `-p` | `--port` | `5693` | listener port |
| `-t` | `--timeout` | `10s` | HTTP timeout |
| `--insecure` | | off | accept the agent's self-signed certificate |
| `--check` | | — | run `GET /v1/run/<service>` on the agent and pass its state/output through |
| `--metric` | | — | `load1`, `cpu`, `memory`, `disk:<mount>`, `processes`, `net:<iface>` graded with `-w`/`-c` |
| `-w` | `--warning` | — | range for `--metric` |
| `-c` | `--critical` | — | range for `--metric` |

Summary mode applies built-in thresholds — load1 WARNING > 2×CPUs / CRITICAL > 4×CPUs,
cpu % 85/95, memory % 90/95, each disk 85/95, processes informational — and emits the worst state:
`AGENT OK - web-01 v1.0.0 up 2h0m0s: load 0.50, mem 40.0%, disk / 42.0%, 120 procs | load1=0.50;8;16;0; memory=40.0%;90;95;0;100 'disk /'=42.0%;85;95;0;100 processes=120;;;0;`.
Single-metric output looks like `MEMORY WARNING - web-01 RAM 91.0% used, 1.4 GB available | memory=91%;90;95;;`;
`net:<iface>` grades the **rx** rate (`NET OK - web-01 eth0 rx 1523 B/s tx 884 B/s | rx_eth0=1523B/s;;;0; tx_eth0=884B/s;;;0;`).
HTTP and transport failures, including `401` for a wrong token → CRITICAL; a metric the agent does
not report (e.g. `load1` on Windows, `cpu` on Linux) → UNKNOWN. The transport is always HTTPS.

```yaml
spec:
  checkCommand: builtin:agent
  args: ["--token", "$SECRET:agent-web01$", "--insecure", "--metric", "disk:/", "-w", "80", "-c", "90"]
```

## Gotchas at a glance

- `builtin:https` does not imply TLS — add `-S` (or a full `https://` URL).
- `dns`: the address/`-H` is the **name**, `-s` is the server.
- `nrpe`: `-C` selects the command; `-n` disables TLS; TLS mode needs real certificates on the daemon.
- `imap -S` turns port 143 into 993, even when 143 was given explicitly.
- `smtp -S` is STARTTLS, not implicit TLS.
- `tls-cert -w/-c` are integers (days), not ranges; the chain is not validated.
- `snmp` text values are compared with `-e`; numeric values need `-w`/`-c`; unit suffixes in ranges are stripped, not scaled.
- `agent` needs `--token`; a wrong token is CRITICAL, not UNKNOWN.
- One token per `args` element; the check's own `-t` never extends the object's `timeout`.
- The http check refuses link-local/metadata destinations by design.
