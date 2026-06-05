# Go-Tangra Ticket

Support ticket system for the go-tangra platform. Tickets are created from
**inbound email forwarded by iris/KumoMTA** (email-to-ticket) and can be
**assigned to users**, prioritized, commented on, and moved through a status
lifecycle. Admin REST API is exposed via the admin gateway (gRPC transcoding);
the iris webhook and the embedded frontend are served by the module's own
HTTP server.

## Ports

- **10800** — gRPC (admin gateway transcoding, mTLS). Ticket/Comment/User APIs.
- **10801** — public HTTP. Hosts `POST /webhooks/iris` (inbound email) and the
  embedded module-federation frontend.
- **10810** — Prometheus metrics (`/metrics`).

## Entities

- **Ticket** (`ticket_tickets`) — subject, description, status, priority,
  source (`iris`/`manual`), requester (email/name), recipient, `assignee_id`,
  `external_id` (RFC822/iris Message-Id, unique per tenant for dedup).
- **TicketComment** (`ticket_comments`) — reply or internal note, linked to a
  ticket; author is an internal user or an external email.

## Services (protos/ticket/service/v1)

- `TicketService` — Create/Get/List/Update/Delete + `AssignTicket` + `UpdateTicketStatus`.
- `TicketCommentService` — Create/List/Delete comments.
- `TicketUserService` — `ListAssignableUsers` (proxied from admin-service for the assignee dropdown).

## Status lifecycle

`OPEN → IN_PROGRESS → PENDING → RESOLVED → CLOSED` (stored as the proto enum
name string, e.g. `TICKET_STATUS_OPEN`).

## iris webhook (email-to-ticket)

`POST /webhooks/iris` accepts the raw RFC822 message iris/KumoMTA forwards
(`Content-Type: message/rfc822`). It parses `From` → requester, `Subject` →
title, body → description, and uses `X-Iris-Message-Id` (or the email
`Message-Id`) as `external_id` for idempotent dedup. `X-Iris-Recipient` becomes
the ticket's `recipient`.

**Auth:** a shared token (`TICKET_WEBHOOK_TOKEN`) checked (constant-time) from
the `X-Ticket-Token` header, `?token=` query param, or `Bearer` Authorization.
If the env var is unset the endpoint is open (logged loudly at startup) — set
it in production.

## Email threading (reply chaining)

Operators reply from the ticket drawer (Reply vs Internal note). A **Reply**
(`TicketCommentService.ReplyTicket`) emails the requester over the SMTP relay
and records a public comment; an **Internal note** stays private.

When a **new** ticket is created from inbound mail, an **auto-reply**
acknowledgement is emailed to the requester (`internal/webhook` →
`sendAutoReply`), recorded as a public comment. Loop protection (RFC 3834):
skipped for auto/bulk mail (`Auto-Submitted`/`Precedence`), daemon/no-reply
senders, and when the requester address equals the support address. Disable
with `TICKET_AUTOREPLY=off`; customise text with `TICKET_AUTOREPLY_BODY`.

Outbound mail is stamped so the requester's response threads back:
- **Message-ID** — `ticket.<id>.<uuid>@<from-domain>`, stored on the comment.
- **In-Reply-To / References** — the thread root (`external_id`) + the most
  recent message-id in the thread.
- **Body reference** — a `Ticket reference: [#<id>]` line appended to the body
  (the subject stays clean — no token). Auto-replies set `Auto-Submitted`.
- **From** — the ticket's `recipient` (the support address that received the
  original), falling back to `TICKET_SMTP_FROM`.

Inbound matching (`internal/webhook/iris.go` → `findThreadTicket`): ① comment
`message_id` ∈ References/In-Reply-To, ② ticket `external_id` ∈ those,
③ `[#<id>]` token in the body (preferred) or subject (legacy). A match appends
a public comment (and re-opens a resolved/closed ticket) instead of creating a
new ticket. Threading helpers live in `internal/thread`; SMTP sending in
`internal/mailer` (wneessen/go-mail).

## Configuration / env

- `ADMIN_GRPC_ENDPOINT`, `GRPC_ADVERTISE_ADDR`, `HTTP_ADVERTISE_ADDR`, `FRONTEND_ENTRY_URL`, `CERTS_DIR` — platform registration/mTLS.
- `HTTP_PUBLIC_BIND` (default `:10801`), `METRICS_ADDR` (default `:10810`).
- `TICKET_WEBHOOK_TOKEN` — shared secret for the iris webhook.
- `TICKET_DEFAULT_TENANT` — tenant id assigned to inbound-email tickets (default `0`).
- **SMTP relay (outbound replies):** `TICKET_SMTP_HOST` (enables sending; unset = replies disabled), `TICKET_SMTP_PORT` (default `587`), `TICKET_SMTP_USERNAME`/`TICKET_SMTP_PASSWORD` (optional), `TICKET_SMTP_FROM` (fallback From), `TICKET_SMTP_FROM_NAME` (default `Support`), `TICKET_SMTP_TLS` (`starttls`|`ssl`|`none`, default `starttls`), `TICKET_SMTP_INSECURE` (`1` to skip cert verify), `TICKET_MAIL_DOMAIN` (Message-ID/token host; defaults to the From domain).
- **Auto-reply:** `TICKET_AUTOREPLY` (`off` to disable; default on when SMTP is configured), `TICKET_AUTOREPLY_BODY` (custom acknowledgement text; `{{name}}` substituted — the reference line is always appended).

## Codegen

```bash
make api      # buf dep update + generate + descriptor
make ent      # ent generate (temporarily pins tablewriter to v0.0.5 — the
              # ent v0.14.5 CLI is incompatible with newer tablewriter)
make wire     # wire DI
make generate # all of the above
```

The frontend uses a **generated** TypeScript client (`frontend/src/generated/`,
from `buf.typescript.gen.yaml`) wrapped by a fetch `handler` in
`frontend/src/api/client.ts`. The generator (`protoc-gen-typescript-http`) is
**pinned** to `v0.0.0-20260525125049-694cf6cd0529` in both CI and the Dockerfile
`ts-codegen` stage — do NOT use `@latest`: it drifted to a
`ClientTransport`/`.unary` API that breaks the fetch handler (the bug that hit
hr/notification). Regenerate with `make ts-client`.

## Conventions

- All queries scoped by `tenant_id` (ent `mixin.TenantID`).
- Caller identity from gRPC metadata: `x-md-global-{tenantid,userid,username,roles}`.
- UUID string IDs; status/priority stored as proto enum-name strings.
- Reference module: `go-tangra-sharing`. Uses published `go-tangra-common v1.17.1`.
