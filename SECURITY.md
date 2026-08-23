# Security policy

Northplane is a monitoring and alarm server; it holds credentials for the systems it watches and
the channels it alarms through. We take reports seriously.

## Reporting a vulnerability

Please **do not** open a public issue. Use GitHub's private reporting:
**[Report a vulnerability](https://github.com/myfoxit/northplane/security/advisories/new)**.
Include the version (`northplaned version` or the image tag), a description, and steps to
reproduce. You will get an acknowledgement within a few days and a fix or mitigation plan as soon
as we have one; credit goes to the reporter in the release notes unless you prefer otherwise.

## Supported versions

The latest release and the `main` branch (what `ghcr.io/myfoxit/northplane:latest` and the public
instance run) receive fixes.

## Hardening

The manual has a [security hardening checklist](https://doktrace.com/docs/administration/security/)
(TLS or trusted proxy, token scopes and expiry, ingest authentication, signup, secrets at rest,
audit verification) and lists the unauthenticated endpoints.
