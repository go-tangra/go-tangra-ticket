# Ticket service — deployment notes

The **ticket** service is the tenant-scoped **helpdesk** module of the Freya
platform (go-tangra-ticket replica): tickets with status/priority/assignee, a
conversation timeline (internal notes and emailed public replies), **inbound
email** turned into tickets or threaded back into existing ones, loop-safe
auto-acknowledgements, sandboxed **CEL triage rules**, tags, mailboxes, history,
statistics, live updates and tenant backup.

Module id `ticket`; browser API under `/api/ticket/v1` (gateway-proxied,
platform token); module gRPC surface `ticket.v1` on the SPIFFE mTLS mesh (not
proxied, see `pkg/ticketclient`); and one **off-mesh** listener, the inbound
mail edge.

## Server operations

| Item | Value |
|---|---|
| Binary | `ticketsvc -config deploy/container.yaml` (applies migrations, then serves) |
| Listeners | gRPC `server.grpc_addr` (:9955), HTTP `server.http_addr` (:9956) — mesh, mTLS; admin `admin.addr` (:9840, `/healthz`, `/readyz`); inbound edge `inbound.addr` (:9957) |
| Store | TimescaleDB, database `ticket`, app role `ticket_app` (NOBYPASSRLS); `db.migrate_dsn` for the migration role (creates `pg_trgm`) |
| Event bus | Valkey user `ticket` (Streams `platform:events:<tenant>`) |
| KEK | 32-byte key (`kek.source: file|env`, `deploy/kek.dev` for development only) |
| Object store | S3-compatible (`object_store`; RustFS in the stack), bucket self-provisioned at start |
| Mesh identity | enrolls with lcm through the gateway edge (`mesh_enroll`), stores its SVID in `/state` |
| Gateway | registers its manifest (routes, permissions, abilities, nav) on a lease; seeds the roles `ticket admin` / `ticket agent` / `ticket viewer` into auth |

Every `ticket_*` table carries `tenant_id` under **row-level security**. The
only cross-tenant read is `ticket_route_mailbox(address)` (SECURITY DEFINER,
returns tenant + mailbox settings only) used by the inbound edge to route a
recipient address to its tenant; all subsequent writes run under a scoped
system subject pinned to that tenant.

Policy (`deploy/policy.yaml`): the gateway forwards everything; any platform
module may call `ticket.v1.Tickets/{Create,Get,List,AddComment}`. ticket itself
needs `auth` to allow `svc/ticket` on `auth.v1.Profiles/{Lookup,ListMembers}`
(assignable agents — rule `ticket-directory` in `services/auth/deploy/policy.yaml`)
and, when relay/SMTP secrets are warden references, `warden` to allow
`warden.v1.Secrets/GetPassword` (rule `ticket-secrets`).

## Inbound mail edge (relay setup)

A mail relay (iris/KumoMTA or any MTA hook) delivers each accepted message by
posting the raw RFC 822 bytes:

```
POST https://<ticket-host>:9957/inbound/mail
Authorization: Bearer <relay token>          (or X-Ticket-Token: <relay token>)
Content-Type: message/rfc822
X-Iris-Recipient: support@example.org         (or X-Ticket-Recipient; falls back to To/Delivered-To)
X-Iris-Message-Id: <id@relay>                 (optional; else the Message-Id header)

<raw message>
```

- `202 {"outcome":"created"|"threaded"|"duplicate"|"dropped","ticket_id":…}`.
  `duplicate` is a redelivery of an already-ingested Message-Id (safe to retry).
- A missing/wrong token, an unknown or inactive mailbox and an oversized body all
  get the **same generic refusal** (no mailbox enumeration). `503` means storage
  is unavailable — the relay should retry.
- Tokens in the query string are **not** accepted (they leak into logs).
- `GET /healthz` is the relay's liveness probe.
- TLS is required (`inbound.tls_cert_file`/`tls_key_file`); `inbound.insecure_dev`
  is a named development opt-out refused in production.
- Bounds: `inbound.max_body_bytes` (10 MiB), `max_attachment_bytes` (25 MiB),
  `max_parts` (100), `max_depth` (10). Oversized attachments are skipped and
  noted, never truncated silently.

**iris / KumoMTA**: the existing iris hook already sends `X-Iris-Recipient` and
`X-Iris-Message-Id`; point its webhook URL at `/inbound/mail` and give it the
relay token as a Bearer header. One relay credential serves every tenant —
routing is by the recipient address of an **active mailbox**.

**Threading**: a message threads into an existing ticket of the same tenant when
its `In-Reply-To`/`References` match a message the ticket sent or received, or
when the subject carries the ticket's reference token (`[#<ticket-id>]`). Otherwise a
new ticket (source `email`) is created and the triage rules run.

**Loop safety** (RFC 3834): no auto-acknowledgement is sent for messages carrying
`Auto-Submitted` (other than `no`) or `Precedence: bulk|list|junk|auto_reply`,
from daemon/no-reply senders, or from an address that is itself a support mailbox;
acknowledgements carry `Auto-Submitted: auto-replied` and `Precedence: auto_reply`.
Every skip is audited (`ack.skipped` with the reason).

## Outbound SMTP relay

Public replies and acknowledgements are sent by the module itself (`smtp`):
`tls: implicit` (465) or `starttls`; `tls: none` needs `smtp.allow_plaintext`
and is refused in production. PLAIN auth only over TLS. `smtp.username` +
`smtp.password_ref` (a secret reference) when the relay requires auth.
`smtp.mail_domain` is the Message-ID domain fallback. The From address is the
ticket's mailbox (display name per mailbox). Without `smtp.host`, public replies
are refused (`reply_unavailable`) and acknowledgements skipped — never queued
silently. Delivery state (`sent`/`failed`) is recorded on the comment and audited.

## Secrets (relay token, SMTP password)

Config holds **references**, never values; they are resolved at use time,
cached for `secrets.refresh_seconds` and re-read after that (rotation without a
restart). Values never appear in logs, responses, audit rows, events or backups.

| Form | Meaning |
|---|---|
| `warden:<secret-id>` (or bare `<secret-id>`) | a warden secret, fetched over the mesh with `warden.v1.Secrets/GetPassword`, authorised by the platform token in `secrets.token_file` |
| `file:<path>` | a local file — **development only** (warned at start, refused in production) |

Warden authorises `GetPassword` per user (the platform token names the
principal whose grant on the secret is checked), so production deployments give
ticket a token for a dedicated principal that holds `view` on exactly the two
secrets, and refresh that file before it expires.

The containerized dev stack uses `file:` references: `ticket-secrets-init`
generates a random relay token once into the `ticket-secrets` volume
(`file:/secrets/relay.token`), and Mailpit needs no SMTP credentials. Read the
dev relay token with
`docker compose -p freya-stack exec ticket cat /secrets/relay.token`.

## Mailboxes and rules

- **Mailboxes** (`mailboxes:manage`) map an address to the tenant; an address is
  globally unique. `active: false` stops routing (generic refusal).
  `auto_ack` + `auto_ack_template` (`{{name}}` → requester name; a built-in
  greeting when empty; the ticket's reference line is always appended) enable
  the acknowledgement.
- **Rules** (`rules:manage`) run on every new inbound ticket in sort order:
  conditions (ALL/ANY) or a raw CEL expression over `subject, body, from,
  fromName, recipient, fromDomain, hasAttachments, spamScore`; actions tag /
  assign / status / priority / drop. Rules are compiled and type-checked on
  save (invalid → `invalid_rule` with the reason), evaluated under a cost limit
  (`rules.cost_limit`) and a per-message deadline (`rules.eval_timeout_ms`); an
  erroring rule is skipped and counted, never blocks ingestion.

## Security summary

- Tenant isolation: RLS on every table + routing-pinned system subject for inbound
  writes; unique constraints on mailbox address, message ids and tag names.
- HTML email bodies are stored raw but only ever **returned sanitised**
  (bluemonday; `cid:` images rewritten to tenant-scoped attachment URLs, remote
  images dropped) and shown in a sandboxed `srcdoc` iframe without
  `allow-scripts`/`allow-same-origin`. The only raw-HTML carrier is
  `POST /backup/export` (`backup:manage`), for lossless restore.
- Attachments stream through the module with `Content-Disposition: attachment`
  and `X-Content-Type-Options: nosniff` after an RLS check; keys are
  tenant-prefixed.
- Outbound headers are checked for CR/LF/control characters (no header injection).
- Audit: every mutation, ingestion outcome and outbound mail, with requester PII
  and bodies redacted. Events carry ids/status only — never bodies or addresses.
- Full / cross-tenant backup restore is platform-admin only.

## Metrics

OpenTelemetry counters exported on the admin listener: `ticket.inbound` (by
outcome), `ticket.replies` (sent/failed), `ticket.acks` (by result),
`ticket.status.transitions`, `ticket.rules.errors`.
