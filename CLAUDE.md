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

> v1 limitation: MIME multipart bodies are stored as the raw body in
> `description`; a follow-up can walk parts to extract `text/plain`.

## Configuration / env

- `ADMIN_GRPC_ENDPOINT`, `GRPC_ADVERTISE_ADDR`, `HTTP_ADVERTISE_ADDR`, `FRONTEND_ENTRY_URL`, `CERTS_DIR` — platform registration/mTLS.
- `HTTP_PUBLIC_BIND` (default `:10801`), `METRICS_ADDR` (default `:10810`).
- `TICKET_WEBHOOK_TOKEN` — shared secret for the iris webhook.
- `TICKET_DEFAULT_TENANT` — tenant id assigned to inbound-email tickets (default `0`).

## Codegen

```bash
make api      # buf dep update + generate + descriptor
make ent      # ent generate (temporarily pins tablewriter to v0.0.5 — the
              # ent v0.14.5 CLI is incompatible with newer tablewriter)
make wire     # wire DI
make generate # all of the above
```

The frontend uses a hand-written fetch client (`frontend/src/api/`), so there
is **no `protoc-gen-typescript-http` / ts-codegen stage** (avoids the
@latest-generator drift that bit hr/notification).

## Conventions

- All queries scoped by `tenant_id` (ent `mixin.TenantID`).
- Caller identity from gRPC metadata: `x-md-global-{tenantid,userid,username,roles}`.
- UUID string IDs; status/priority stored as proto enum-name strings.
- Reference module: `go-tangra-sharing`. Uses published `go-tangra-common v1.17.1`.
