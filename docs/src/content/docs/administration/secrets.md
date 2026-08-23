---
title: Secrets
description: "The encrypted secret store — AES-256-GCM SecretBox, the secret.key master key and its provisioning, the $SECRET:name$ reference syntax and where it is accepted, the secrets API and Admin tab, backup and rotation advice, and what happens when the key is lost."
sidebar:
  order: 6
---

Northplane keeps credentials that its own configuration needs — SMTP passwords, Twilio auth tokens, ticket-system API keys, webhook HMAC secrets, agent bearer tokens — in a **write-only secret store**. Values are encrypted at rest with a master key, are never returned by the API, and are referenced from configuration by name. Do not confuse this with [API tokens](/docs/administration/api-tokens/), which are credentials *for* Northplane; secrets are credentials Northplane uses *towards* other systems.

## How the store works

| Property | Value |
|---|---|
| Cipher | **AES-256-GCM**; each value is sealed as `nonce ‖ ciphertext` with a fresh random nonce |
| Master key | 32 bytes, stored **hex-encoded (64 characters)** in a key file with mode `0600` |
| Storage | Table `secrets(tenant_id, name, ciphertext, updated_by, updated_at)`, primary key `(tenant_id, name)` — secrets are **tenant-scoped** by name |
| Visibility | Write-only: `GET /api/v1/secrets` returns names only; audit entries for `secret.put` carry no value; the agent check pull does not expand references |
| Consumers | Notification channels, event sources (ingest auth, IMAP and MQTT passwords), telephony, outgoing webhooks, check-command macros, AI provider connections (see below) |

A wrong master key makes decryption fail with `secret decryption failed (wrong master key?)` — the value is then treated as missing.

## The master key (`secret.key`)

The key file location is `secretKeyFile` in `config.yaml` (env `NORTHPLANE_SECRET_KEY_FILE`). `northplaned init` writes `<configDir>/secret.key` (as root: `/etc/northplane/secret.key`) with mode `0600` and references it from the generated `config.yaml`; its summary prints "secret key: … (0600 — back this up!)".

At start the server resolves the key (**self-provisioning**):

1. If `secretKeyFile` is set and the file does **not exist**, a fresh key is generated there (log `server: generated secret-store master key`); then the file is loaded.
2. If the configured path is **unusable** — read-only mount, a directory left behind by a missing Docker bind-mount source, bad contents — the server logs `server: configured secretKeyFile unusable — falling back to the data directory` and uses `<dataDir>/secret.key` (generated if missing). A file with garbage content is never overwritten.
3. If no path is configured, `<dataDir>/secret.key` is used directly (generated if missing).
4. If even that fails: `server: secret store disabled (no usable master key)` — the server still starts, but `PUT /api/v1/secrets/{name}` answers `503 np:secrets/nokey` and everything that needs a sealed value (secrets, AI provider keys, MQTT credentials) is unavailable.

The file must hold exactly 64 hex characters (plus optional whitespace); anything else is `secret key file must hold 64 hex chars (32 bytes)`.

:::caution[The fallback is loud for a reason]
If a deployment silently falls back to `<dataDir>/secret.key`, values sealed afterwards are bound to *that* key. Later "fixing" the bind mount swaps the key and makes those values unreadable. Watch the start-up log for the fallback warning and keep exactly one key per data set.
:::

**Where the key lives per deployment variant:**

| Variant | Key file |
|---|---|
| Binary + `northplaned init` | `/etc/northplane/secret.key` (root) or `<configDir>/secret.key`; referenced by `secretKeyFile` |
| Root `docker-compose.yml` (Caddy bundle) | No explicit key: self-provisioned at `/var/lib/northplane/secret.key` inside the `northplane-data` volume |
| `deploy/docker-compose.yml` and `deploy/docker-compose.vm.yml` | `./secret.key` on the host, bind-mounted read-only at `/etc/northplane/secret.key` and set via `NORTHPLANE_SECRET_KEY_FILE`. The provisioning script creates it with `openssl rand -hex 32`, owned by uid/gid **65532** (the container user), mode `0600`. It survives image swaps and demo/real switches. |

A key you create yourself is just `openssl rand -hex 32 > secret.key && chmod 600 secret.key` (owned by the user the server runs as — uid 65532 in the container images).

## Store a secret

**UI:** **Admin → Secrets** shows Name and Referenz (`$SECRET:name$`) for every secret of the active tenant, with a delete action per row. **Anlegen / Create** asks for Name (e.g. `smtp-password`) and Wert (Value); the dialog warns "Wird nie wieder angezeigt / Never shown again". There is no edit — to change a value, create it again under the same name (upsert).

**API** (all `admin:secrets`, tenant = the request's tenant):

| Endpoint | Behaviour |
|---|---|
| [`PUT /api/v1/secrets/{name}`](/docs/reference/api/operations/put_secrets_name/) | Body `{"value":"…"}` → `204` (create or overwrite). `503 np:secrets/nokey` without a master key. Audit `secret.put` (no value). |
| [`GET /api/v1/secrets`](/docs/reference/api/operations/get_secrets/) | `["smtp-password","twilio-auth"]` — a plain JSON array of **names** |
| [`DELETE /api/v1/secrets/{name}`](/docs/reference/api/operations/delete_secrets_name/) | `204`. Audit `secret.delete`. |

```bash
curl -s -X PUT https://monitoring.example.net/api/v1/secrets/smtp-password \
  -H "Authorization: Bearer np_<48 hex>" -H "Content-Type: application/json" \
  -d '{"value":"<secret>"}'
```

Names are free-form strings; keep them URL-safe (they are path segments) and descriptive. A name is unique per tenant — two tenants may both have `smtp-password` with different values.

## Reference a secret: `$SECRET:name$`

Wherever a configuration field would otherwise hold a credential, write `$SECRET:name$` instead. The value is resolved at use time, in the **tenant of the document** that holds the reference, and never written back into the document or returned by the API.

| Where | How the reference is resolved |
|---|---|
| **Notification channel** config fields (`password`, `apiKey`, `secretAccessKey`, `sessionToken`, `authToken`, `apiKeySecret`, `token`, `secret`, `apiToken`, `fcmServiceAccount`, `apnsKey`, Asterisk AMI `secret`, MQTT `password`, …) | **Whole-value**: the field must be exactly `$SECRET:name$`. An unresolvable reference becomes the empty string (the delivery then fails with an auth error). The Channels dialog shows the hint "Value or `$SECRET:name$` reference". See [Channels](/docs/alarming/channels/). |
| **Check commands** (named check commands, `exec:` plugins, `agent:exec:` commands, builtin args) | As a **macro** like `$ARG1$`: `$SECRET:name$` may appear anywhere inside an argument, e.g. `--token $SECRET:agent-token$` for the builtin `agent` check. Resolved by the executor at run time in the object's tenant; an unresolvable reference is left **verbatim** in the argument and reported as an unknown macro by `POST /api/v1/check-commands:test`. **Not** expanded in `GET /api/v1/agent/checks` (the pulled args keep `$SECRET:…$` literally). See [Plugins and Nagios](/docs/monitoring/plugins-and-nagios/). |
| **Telephony event sources** (`twilioAuthToken` and similar config keys of `voice-inbound` / `sms-inbound`) | Whole-value, placeholder `$SECRET:twilio-auth$` in the dialog. See [Voice and IVR](/docs/alarming/voice-and-ivr/). |
| **Outgoing webhook subscriptions** (`secret`) | `$SECRET:name$` **or** a literal; resolved at delivery to compute `X-Northplane-Signature: sha256=<hmac>`. See [Outgoing webhooks](/docs/alarming/webhooks-out/). |

Some resources reference secrets **by name without the `$SECRET:` wrapper** (the field is a secret *name*):

| Field | Resource |
|---|---|
| `secretRef` | Event sources — the token / HMAC key / basic-auth password for `authMode: token`, `hmac`, `basic` on `POST /api/v1/ingest/{source}` |
| `passwordSecretRef` | IMAP (`email`/`imap`) event sources and MQTT event sources — re-read from the store on every (re)connect |
| AI provider connection keys | Sealed with the same box when you save a connection in **Admin → AI providers** ([Agent chat](/docs/ai/agent-chat/)) |

The Event sources dialog has a "secret ref" field next to the auth mode — see [Event sources](/docs/alarming/event-sources/).

:::caution[SNMPv3 trap passphrases are not secret references]
The Event sources dialog writes `v3AuthSecretRef` / `v3PrivSecretRef` for `snmp-trap` sources, but the trap listener only reads the inline keys `v3AuthPass` / `v3PrivPass` and never consults the secret store. Provide SNMPv3 passphrases inline (API, or the "Weitere Einstellungen" key/value editor) — see [SNMP](/docs/monitoring/snmp/).
:::

```yaml title="bundle excerpt — channel and event source using secrets"
kind: Channel
metadata: {name: ops-mail}
spec:
  type: email
  config:
    provider: smtp
    host: smtp.example.net
    port: "587"
    username: northplane@example.net
    password: $SECRET:smtp-password$
---
kind: EventSource
metadata: {name: grafana}
spec:
  type: webhook
  authMode: token
  secretRef: grafana-ingest        # name of a secret, no $SECRET:…$ wrapper
```

Secrets themselves are **not** a bundle kind: create them on every instance (main and each edge) with the API or the UI before applying a bundle that references them. A federation edge resolves references against its **own** store.

## Tenancy

Secrets are stored per tenant. A channel in tenant *A* can only resolve `$SECRET:x$` from tenant *A*'s store; a central admin creates a customer's secrets by sending `X-Northplane-Tenant: <tenant-id>` with the `PUT` (see [Tenants and sites](/docs/administration/tenants-and-sites/)). The master key, however, is one per instance.

## Backup and rotation

- **Back up `secret.key`** together with the database. `northplaned backup` copies `core.db`, the event segments and the TSDB — **not** the key file, even when it lives in the data directory — yet the key is what makes the `secrets` table readable. The deployment scripts keep it outside the data volume (`/opt/northplane/secret.key`) precisely so that it is backed up as a separate file; see [Storage](/docs/administration/storage/) and [Operations](/docs/deployment/operations/).
- File permissions: `0600`, owned by the service user (uid 65532 in containers). Never commit it, never put it in an image.
- **There is no key-rotation command.** Rotating the master key means: generate a new key file, point `secretKeyFile` at it (or replace the file), restart, and **re-enter every secret** (`PUT` each name again) plus re-save AI provider connections. Until you do, the old values cannot be decrypted. Keep an inventory of secret names (`GET /api/v1/secrets` per tenant) before you start.
- Rotating an individual *secret value* is just another `PUT` under the same name; the next delivery picks it up.

## If the key is lost

With the key gone (or replaced by a different one) every sealed value is unrecoverable: `secret decryption failed (wrong master key?)`. Symptoms are notification failures (empty password / token → provider rejects), inbound webhooks rejected (`401`, because the source's `secretRef` resolves to nothing), agent checks with `$SECRET:…$` macros failing, and AI connections that no longer authenticate. Recovery is the rotation procedure above: provide a key, restart, re-create every secret and AI connection. Names survive (they are stored in clear), so `GET /api/v1/secrets` gives you the checklist.

## Errors

| Code | HTTP | When |
|---|---|---|
| `np:secrets/nokey` | 503 | `PUT /api/v1/secrets/{name}` while the store has no usable master key |
| `np:auth/forbidden` | 403 | Caller lacks `admin:secrets` |

The hardening checklist in [Security](/docs/administration/security/) covers key handling as well.
