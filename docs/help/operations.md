---
title: Operate Tickets
summary: Configuration keys and defaults, the inbound mail edge and relay setup, outbound SMTP, secret references, service policies and troubleshooting.
kind: reference
order: 90
tags: [ticket, operations, configuration, email, policy, troubleshooting]
audience: operator
---
Tickets runs as `ticketsvc` (image `ghcr.io/go-tangra/go-tangra-ticket`) with
TimescaleDB, the platform event bus, an S3-compatible object store for
attachments and, for replies, an SMTP relay.

```sh
ticketsvc -config /app/deploy/container.yaml             # serve; applies migrations
ticketsvc bootstrap -config /app/deploy/container.yaml   # apply migrations and exit
ticketsvc version
```

Listeners: mesh gRPC (`:9955`) and HTTP (`:9956`), admin (`:9840`) and the
**inbound mail edge** (`inbound.addr`, `:9957`), the only listener outside
the mesh. At start the service creates its object-store bucket when missing
and registers with the gateway and auth.

## Configuration

The file is YAML. Unknown keys are refused, so a typo stops the service at
start with `config: <file>: ...`. Besides the framework keys (`service_name`,
`trust_domain`, `env`, `identity`, `authz`, `server`, `admin`, `discovery`,
`limits`) Tickets reads:

| Key | Default | Notes |
|---|---|---|
| `db.dsn`, `db.migrate_dsn`, `db.max_conns` | —, `db.dsn`, 16 | `db.dsn` required; in production `sslmode=verify-full` (or `verify-ca`) |
| `valkey.*` | — | `addresses` (required), `username` (`ticket`), `password`, `ca_file`, `allow_plaintext` (refused in production) |
| `kek.source` | `file` | `file` (`kek.path`) or `env` (`kek.env`) |
| `object_store.endpoint`, `object_store.bucket` | — | required |
| `object_store.region`, `use_ssl`, `access_key`, `secret_key` | `us-east-1`, false, —, — | `use_ssl` required in production |
| `inbound.addr` | — | required, `host:port` |
| `inbound.tls_cert_file`, `inbound.tls_key_file` | — | required unless `inbound.insecure_dev` (refused in production) |
| `inbound.relay_token_ref` | — | required; secret reference of the relay token |
| `inbound.max_body_bytes` | 10 MiB | 1 KiB–64 MiB per message |
| `inbound.max_attachment_bytes` | 25 MiB | 1 KiB–64 MiB per attachment |
| `inbound.max_parts`, `inbound.max_depth` | 100, 10 | 1–1000 MIME parts, 1–50 nesting levels |
| `smtp.host` | empty | empty: replies are refused (`reply_unavailable`) and acknowledgements skipped |
| `smtp.port` | 587 | |
| `smtp.tls` | `starttls` | `implicit`, `starttls` or `none` (needs `smtp.allow_plaintext`; refused in production) |
| `smtp.username`, `smtp.password_ref` | — | `password_ref` required with a username |
| `smtp.mail_domain` | — | Message-ID domain fallback |
| `smtp.timeout_seconds` | 30 | 1–300 |
| `rules.cost_limit` | 100000 | 100–10000000 per rule evaluation |
| `rules.eval_timeout_ms` | 50 | 1–5000 per message |
| `rules.max_rules` | 500 | 1–10000 rules per tenant |
| `secrets.warden_service`, `token_file`, `refresh_seconds` | `warden`, —, 300 | resolution of `warden:` references; values are cached and re-read after `refresh_seconds` |
| `events.enabled` | true | publish ticket events |
| `gateway.service` | `gateway` | mesh name of the gateway; the only peer allowed to fetch the help pages |
| `gateway.issuer` | — | required, an `https://` origin |
| `mesh_enroll.*` | — | SVID enrollment with lcm |

These keys are accepted and range-checked but currently have no effect:
`object_store.presign_ttl_seconds`, `limits_ticket.max_request_bytes`,
`limits_ticket.max_backup_bytes` and `limits_ticket.max_page_size`.

## Inbound mail edge

The mail relay posts each accepted message, as raw RFC 822 bytes, to the edge:

```http
POST /inbound/mail HTTP/1.1
Host: ticket.example.org:9957
Authorization: Bearer <relay token>
Content-Type: message/rfc822
X-Iris-Recipient: support@example.org
X-Iris-Message-Id: <id@relay>

<raw message>
```

- The token may also come as `X-Ticket-Token`, never in the query string. The
  recipient may come as `X-Ticket-Recipient`; without either header the `To`
  and `Delivered-To` headers are used.
- The answer is `202` with `outcome` `created`, `threaded`, `duplicate` (a
  redelivery, safe to retry) or `dropped` (a triage rule) and the
  `ticket_id`.
- A wrong token, an unknown or inactive mailbox and an oversized message all
  get the same refusal, so mailboxes cannot be probed. `503` means storage is
  unavailable: the relay should retry.
- `GET /healthz` on the edge is the relay's liveness probe.

One relay token serves every tenant: routing is by the recipient address of
an active mailbox.

## Secret references

The relay token and the SMTP password are references, never values:
`warden:<secret id>` (or a bare id), fetched with
`/warden.v1.Secrets/GetPassword` using the platform token in
`secrets.token_file`, or `file:<path>` for development. `file:` references
are refused when `env` is `production`. Warden checks the token's principal
for each secret, so give the service a dedicated principal that may view
exactly these two secrets, and refresh its token file before it expires.

## Service policies

Tickets' own policy admits the gateway with `operations: ["*"]`, which covers
the help pages (`GET /docs/v1/bundle`), and lets any module call
`ticket.v1.Tickets/Create`, `/Get`, `/List` and `/AddComment`.

Other modules must admit `spiffe://<trust domain>/svc/ticket`:

| Module | Operations | Why |
|---|---|---|
| auth | `/auth.v1.Profiles/Lookup`, `/auth.v1.Profiles/ListMembers` | assignable users (rule `ticket-directory`) |
| warden | `/warden.v1.Secrets/GetPassword` | `warden:` references (rule `ticket-secrets`) |

## Troubleshooting

**The service refuses to start with `config: ...`.** The message names the
key, for example `inbound.relay_token_ref is required` or `smtp.tls none is
not permitted in production`.

**The service refuses to start with `help docs: ...`.** The help pages built
into the binary are invalid. `tangra-docs lint` in CI catches this; rebuild
from a commit that passed CI.

**Replies answer `reply_unavailable`.** `smtp.host` is empty, the ticket has
no requester address, or the tenant has no active mailbox to send from.

**Replies are marked Delivery failed.** The relay refused or did not answer:
check `smtp.*`, the TLS mode and the password reference. Failed replies are
not retried.

**The relay gets refusals for every message.** Check the relay token, that
the recipient address is an active mailbox, and the message size against
`inbound.max_body_bytes`.

**The Assignee list is empty or assigning answers `directory_unavailable`.**
auth is unreachable or its policy lacks the `ticket-directory` rule.

**No acknowledgements are sent.** The mailbox must have **Send
acknowledgements** on and `smtp.host` must be set; automatic mail,
no-reply senders and support mailboxes are never acknowledged (audited as
`ack.skipped` with the reason).
