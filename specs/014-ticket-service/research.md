# Phase 0 Research: Ticket Service

Decisions resolving the Technical Context. Scope: a replica of go-tangra-ticket
(email-driven helpdesk: inbound mail → tickets, threading, replies, CEL rules,
tags, auto-acknowledgement) adapted to Freya, plus the enhancements listed in the
spec Overview (mailbox routing, enforced permissions, tenant-scoped downloads,
history, tag filter, events, dashboard, delete cleanup, export/import).

## D1. Storage & conflict detection (TimescaleDB + RLS)
**Decision**: TimescaleDB with per-tenant RLS on every `ticket_*` table
(tickets, comments, attachments, tags, tag links, rules, mailboxes, history);
`ticket_audit_events` is a hypertable. Unique constraints are the conflict and
idempotency layer: `(tenant_id, external_id)` partial (non-null — manual tickets
have none), `(tenant_id, message_id)` partial on comments (inbound dedup +
threading), `(tenant_id, kind, name)` on tags, `(ticket_id, tag_id)` on links,
`address` on mailboxes (**global**, lower-cased — one address routes to exactly
one tenant).
**Rationale**: the source relies on ent tenant filtering, a system-viewer bypass
for the webhook, and misses tenant scoping in two places (attachment download,
comment count). RLS closes those by construction; the partial unique indexes make
duplicate deliveries a database-level no-op.
**Alternatives**: app-level filtering (source) — rejected, bypass risk.

## D2. Inbound mail edge (off-mesh listener, relay-compatible)
**Decision**: A distinct **inbound listener** (like inventory's ingest edge, spec
010 D7) accepts `POST /inbound/mail` with a raw RFC 822 body
(`Content-Type: message/rfc822`, ≤ 10 MiB, configurable). Authentication is a
platform relay token held in warden (config holds a warden secret reference; the
value is fetched at start-up and on rotation), presented as
`Authorization: Bearer` or `X-Ticket-Token` and compared in constant time; the
source's `?token=` query form is **not** supported (tokens in URLs leak into
logs). The recipient is taken from `X-Iris-Recipient` (drop-in compatible with the
existing iris/KumoMTA relay) or `X-Ticket-Recipient`, falling back to the first
`To`/`Delivered-To` address; the message id from `X-Iris-Message-Id` or
`Message-Id`. The recipient is resolved to a **mailbox → tenant** through a narrow
system-scoped lookup (returns tenant + mailbox settings only); an unknown or
inactive mailbox, a missing/invalid token or an oversized body are refused with
the same generic 4xx (no mailbox enumeration). All subsequent writes run under a
scoped system subject pinned to that tenant.
**Rationale**: the relay is a platform-level component serving all tenants, so one
relay credential + per-address routing replaces the source's single
`TICKET_DEFAULT_TENANT`. A separate listener keeps an unauthenticated-by-mTLS
surface off the mesh and off the gateway's browser edge.
**Alternatives**: a gateway `x-freya-public` route (mixes relay traffic with the
browser edge and its CSRF/session model — rejected); per-mailbox tokens (one relay
would need every tenant's token — rejected, operationally worse, no security gain
since routing is by address).

## D3. Mail parsing (port the reference parser; no new MIME dependency)
**Decision**: Port the source's `mailparse` onto the standard library
(`net/mail`, `mime`, `mime/multipart`, `mime/quotedprintable`, `encoding/base64`)
plus `golang.org/x/text` charsets (already an indirect dependency): RFC 2047
headers, multipart walk, base64/QP, CJK/Windows/ISO code pages, HTML→text
fallback, attachments with content id + inline flag (≤ 25 MiB each, count-capped),
`In-Reply-To`/`References`, `Auto-Submitted`/`Precedence`, `X-Spam-Score`. Part
depth and part count are bounded (zip-bomb / pathological MIME guard). File names
are reduced to a safe base name.
**Rationale**: proven against real mail in the source; avoids a new dependency
(enmime) for equivalent behaviour. Fuzzed.

## D4. Threading & ticket reference (port `thread`)
**Decision**: Resolve an inbound message to a ticket, within the mailbox's tenant
only, by (1) a recorded comment `message_id` in `In-Reply-To`/`References`, (2) a
ticket `external_id` in those headers, (3) a `[#<ticket-id>]` token in the body,
then the subject. Replies become public comments (`author_kind=requester`),
attachments attach to the ticket, and resolved/closed tickets re-open. Outbound
replies use subject `Re: <original>` (single prefix), append the reference line
`Ticket reference: [#<id>]` once, and set `Message-ID` `ticket.<id>.<uuid>@<mailbox
domain>`, `In-Reply-To` (last message id, else external id) and `References`.
Rules never run on replies. A reply whose ticket was deleted opens a new ticket.
**Rationale**: identical behaviour to the source keeps existing customer threads
working; tenant confinement comes from D2 routing + RLS.

## D5. Outbound email (in-service mailer, relay settings + warden secret)
**Decision**: An in-service `mailer` package adapted from the notification
module's email channel (`net/smtp`; implicit TLS on 465, STARTTLS otherwise,
plaintext only when explicitly allowed for development; PLAIN auth only over TLS;
every header checked for CR/LF/control characters). The relay host/port/TLS mode
and username are config; the password is a warden secret reference. The From
address is the ticket's mailbox address (display name per mailbox). Auto-
acknowledgements carry `Auto-Submitted: auto-replied` and `Precedence:
auto_reply`. When no relay is configured, public replies and acknowledgements
are refused/skipped with a clear error (never silently queued).
**Rationale**: replies need custom threading headers and a per-ticket From, which
the notification module's generic channel API does not expose; the notification
module still receives the agent-facing events (D10).
**Alternatives**: send through the notification module (no header control —
rejected); a third-party mail library (unnecessary; stdlib suffices).

## D6. Rules engine (CEL, sandboxed)
**Decision**: Port the source's engine on `github.com/google/cel-go`: conditions
(field/operator/value; fields subject, body, from, fromName, recipient,
fromDomain, hasAttachments, spamScore; text operators contains, not_contains,
equals, not_equals, starts_with, ends_with, matches (RE2); numeric gt/gte/lt/lte/
eq/neq) compile to a CEL expression combined with `&&` (ALL) or `||` (ANY); a raw
expression overrides the conditions. Values are emitted as properly quoted CEL
literals (no expression injection from condition values). Programs are compiled
and type-checked on save (invalid → refused with the reason) and cached per rule
version; evaluation uses a **cost limit** and a per-message deadline, with only the
email fields declared (no functions beyond the standard library + strings ext).
Actions: tag (kind + names, tags auto-created), assign, status, priority, drop.
All enabled rules run in sort order; tags accumulate; for assign/status/priority
the first matching rule wins; any drop discards the message; an erroring rule is
skipped and counted.
**Rationale**: parity with the source; CEL is non-Turing-complete and supports
cost limits, satisfying SR-005.
**Alternatives**: a hand-written matcher (loses the raw-expression feature);
expr-lang (no built-in cost limit).

## D7. Safe display of HTML email bodies
**Decision**: The raw HTML is stored as received but **never returned raw**. A
sanitised rendition is produced server-side with
`github.com/microcosm-cc/bluemonday` (UGC-like policy: no scripts, event
handlers, forms, iframes, objects, `style` elements, external URLs in `src`;
links get `rel=noopener noreferrer` and `target=_blank`); `cid:` image sources are
rewritten to the ticket's own tenant-scoped attachment URLs and any other image
source is dropped. The UI shows it in a sandboxed `srcdoc` iframe **without**
`allow-scripts` or `allow-same-origin`; the gateway's strict CSP (inherited by
`srcdoc`) is a second layer. A "plain text" toggle shows the text body.
**Rationale**: the gateway replaces module CSP headers and sets
`X-Frame-Options: DENY` (transport/edge), so a separately served, differently
policed HTML document cannot be framed; `srcdoc` inherits the parent policy
instead. Consequence (accepted): inline `style` attributes are blocked by the
edge CSP, so rich layouts render simplified but always readable and safe.
**Alternatives**: raw HTML in a sandboxed iframe (the source — relies on the
sandbox alone, and remote images leak read receipts); a relaxed CSP route
(blocked by the edge's `X-Frame-Options: DENY`).

## D8. Attachments (object storage, tenant-scoped)
**Decision**: Reuse the paperless/asset `blob` package (minio-go, RustFS in the
stack, self-provisioned bucket). Keys `tenants/<tenant>/tickets/<ticket>/<attachment>`
(the file name is metadata, not part of the key). Downloads go through the module
(`GET /tickets/{id}/attachments/{attId}`), which checks the ticket under RLS and
streams with `Content-Disposition: attachment`, a safe content type and
`X-Content-Type-Options: nosniff`; inline images for the HTML view are served by
the same route with an image allow-list. Deleting a ticket deletes its attachment
rows and objects (the source leaked both).
**Rationale**: proven code; fixes the source's unscoped download route.

## D9. Agents, assignees and the user directory
**Decision**: An `agents` resolver over `authclient` (interface + fake) lists the
tenant's users who hold `tickets:manage` as assignable users and resolves display
names; assignment to a user outside that set is refused (`invalid_assignee`).
Names are stored denormalised on the ticket (refreshed on assignment) so the list
never needs the directory; if the directory is down, assignment of a cached user
still works and names fall back to ids.
**Rationale**: the source returned every admin-service user and never populated
`assignee_name`.

## D10. Events, live UI and notifications
**Decision**: Publish `ticket.created`, `ticket.assigned`, `ticket.status_changed`,
`ticket.commented`, `ticket.requester_replied` to `platform:events:<tenant>`
(ids, subject, status, priority, assignee, source — never bodies, attachments or
requester addresses). The notification module subscribes for agent alerts; the
module's SSE stream refreshes open UIs.

## D11. Permissions, roles and audit
**Decision**: API permissions `tickets:read`, `tickets:manage` (edit, assign,
status, comment, reply, set tags), `tickets:delete`, `tags:manage`, `rules:manage`,
`mailboxes:manage`, `stats:read`, `backup:manage`, declared per route
(`x-freya-permission`) and enforced in the module; seeded roles `ticket admin`
(all), `ticket agent` (read + manage + tags), `ticket viewer` (read). Every
mutation, ingestion outcome (accepted, threaded, duplicate, dropped, refused) and
outbound email is audited with requester PII and bodies redacted.

## D12. History and statistics
**Decision**: `ticket_history` records status/priority/assignee changes (actor or
"rule"/"inbound mail", old, new, time). Statistics are SQL aggregates: counts by
status/priority/assignee, unassigned open, created per day, resolved per day
(from history transitions into resolved) over a selectable window (default 30
days). Prometheus metrics (framework `observe`) on the admin listener: ingested,
threaded, duplicate, dropped, refused, replies sent/failed, status transitions.

## D13. Backup
**Decision**: Export/import in FK order (tags → mailboxes → rules → tickets →
tag links → comments → history → attachments by reference), schema-versioned,
skip/overwrite; full/cross-tenant restore platform-admin only; relay/hook
secrets and object bytes are never exported.

## D14. UI
**Decision**: A Module-Federation remote on the shared `@freya/ui` kit (FlyonUI,
drawer pattern, `breakpointSpecificity` build plugin): ticket list (filters incl.
tag, paging) with a detail drawer (tags, status/priority/assignee, sanitised
message view, attachments, conversation timeline, reply/internal-note composer,
history), manual "New ticket" drawer, rules builder (conditions/actions rows,
advanced expression field, enable toggle, sort order), tags, mailboxes, dashboard.

## Supply-chain note (Constitution VI)
New third-party dependencies: `github.com/google/cel-go` (rule engine — Google-
maintained, used by Kubernetes; needed for parity + cost limits) and
`github.com/microcosm-cc/bluemonday` (HTML sanitisation — widely used, OWASP-style
policies). Reused: `minio/minio-go/v7`, `golang.org/x/text`. Intra-repo: authclient,
warden client. Pinned in `go.sum`; `govulncheck` in CI.

## STRIDE summary
- **Spoofing**: forged inbound mail → relay token (warden, constant-time) + mailbox
  routing; forged agents → gateway platform token; mesh peers SPIFFE-identified.
- **Tampering**: cross-tenant threading/edits → routing-pinned tenant + RLS +
  unique constraints; header injection in replies → header validation (SR-004).
- **Repudiation**: append-only audit of every mutation, ingestion outcome and
  outbound mail (SR-006).
- **Information disclosure**: requester PII/bodies in logs/events → excluded/
  redacted; cross-tenant attachments → RLS-checked streaming; stored XSS / tracking
  pixels → server-side sanitisation + sandboxed srcdoc + edge CSP (SR-003).
- **Denial of service**: oversized or pathological MIME → body/attachment/part
  caps; runaway rules → CEL cost limit + deadline (SR-005); mail loops → RFC 3834
  guards (SR-004).
- **Elevation of privilege**: rules/mailboxes/backup restricted to their
  permissions; full restore platform-admin only; inbound writes use a scoped system
  subject pinned to the routed tenant, never a bypass.
