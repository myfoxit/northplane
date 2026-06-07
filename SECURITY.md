# Security Policy

## Reporting a vulnerability

Please report security issues privately — **do not open a public issue** for an
unpatched vulnerability.

Email: **info@myfoxit.com** (subject prefix `[northplane-security]`), or use
GitHub's *Report a vulnerability* (Security → Advisories) if the repository has
private advisories enabled.

Include: affected version/commit, a description, and a reproduction if possible.
We aim to acknowledge within 3 business days and to ship a fix or mitigation for
confirmed high-severity issues promptly.

## Supported versions

Until a `1.0.0` tag is cut, only the latest `main`/`master` commit is supported.

## Security posture

Northplane is built to be operated safely by default:

- **No plaintext in production.** The server refuses to serve plaintext HTTP on
  a non-loopback listener unless `tls.insecure` is explicitly set. Behind a
  TLS-terminating reverse proxy, set `trustProxy: true` so `Secure` cookies and
  HSTS are emitted from the forwarded scheme.
- **Secrets at rest** are encrypted with AES-256-GCM (fresh nonce per record)
  under a master key file (`0600`). API tokens are stored as argon2id hashes;
  the plaintext token is shown once.
- **AuthN/Z.** Bearer API tokens or OIDC (Authorization Code + PKCE) sessions;
  RBAC permissions are enforced centrally on every route, and the AI/MCP tool
  surface enforces the *same* permissions as the equivalent REST routes.
- **CSRF.** Session-cookie requests the browser marks cross-site are rejected;
  API-token clients carry no ambient credential and are unaffected.
- **Untrusted input.** Request bodies are size-capped; the perfdata/NRPE/SNMP
  parsers and the TSDB file readers are bounds-checked and fuzz-tested where
  applicable. Plugin execution is argv-only (no shell) with process-group kill.
- **Network discovery** refuses loopback/link-local/multicast targets (blocks
  SSRF toward cloud metadata endpoints).
- **AI** runs as a privilege-scoped, audited API client: monitoring data is
  redacted before any LLM call, mutating tools require human approval, and all
  tool invocations are written to the audit log.

If you find a gap in any of the above, we want to hear about it.
