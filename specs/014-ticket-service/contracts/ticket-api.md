# Phase 1 Contracts: Ticket Service

Surfaces: (A) browser HTTP API via the gateway under `/api/ticket`; (B) the
off-mesh inbound mail edge; (C) module-to-module gRPC (`ticket.v1`, SPIFFE mTLS);
(D) events; (E) internal client interfaces (blob, mailer, user directory, secret
store, rules).

## A. Browser HTTP API — prefix `/api/ticket/v1` (gateway-proxied)

Every route declares `x-freya-permission` (or `x-freya-public`); mutating routes
carry the platform CSRF header. Errors use the platform envelope with reasons
`bad_request`, `invalid_status`, `invalid_assignee`, `invalid_rule`,
`ticket_not_found`, `comment_not_found`, `conflict`, `reply_unavailable`,
`directory_unavailable`.

Tickets
- `GET /tickets` (tickets:read) — query `status`, `priority`, `assignee_id`
  (`none` = unassigned), `tag_id`, `query`, `page`, `page_size` (≤ 100) →
  `{items: Ticket[], total}` (newest first; items include tags and comment_count).
- `POST /tickets` (tickets:manage) `{subject, description?, priority?,
  requester_email?, requester_name?, assignee_id?}` → Ticket (source manual).
- `GET /tickets/{id}` (tickets:read) → Ticket with tags, attachments, comment_count.
- `PUT /tickets/{id}` (tickets:manage) `{subject?, description?, priority?}` → Ticket.
- `DELETE /tickets/{id}` (tickets:delete) → 204 (comments, links, history,
  attachments and their objects removed).
- `POST /tickets/{id}/assign` (tickets:manage) `{assignee_id | null}` → Ticket.
- `POST /tickets/{id}/status` (tickets:manage) `{status}` → Ticket.
- `POST /tickets/{id}/tags` (tickets:manage) `{tag_ids: string[]}` → Ticket (replaces the set).
- `GET /tickets/{id}/history` (tickets:read) → `{items: HistoryEntry[]}`.
- `GET /tickets/{id}/body` (tickets:read) → `{html_sanitized, text}` (research D7;
  `cid:` images rewritten to attachment URLs; raw HTML is never returned).
- `GET /tickets/{id}/attachments/{attId}` (tickets:read) → file stream
  (`Content-Disposition: attachment`; allow-listed images may be `inline`).

Comments
- `GET /tickets/{id}/comments` (tickets:read) → `{items: Comment[]}` oldest first.
- `POST /tickets/{id}/comments` (tickets:manage) `{body, internal: true}` → Comment
  (internal note; never emailed).
- `POST /tickets/{id}/reply` (tickets:manage) `{body}` → Comment (emailed to the
  requester; 409 `reply_unavailable` when no requester address or no relay;
  delivery failure → 502 with the comment recorded as `delivery=failed`).
- `DELETE /comments/{id}` (tickets:manage) → 204.

Tags (tags:manage for writes, tickets:read for list)
- `GET /tags?kind=tag|category`, `POST /tags` `{name, kind?, color?, description?}`,
  `PUT /tags/{id}` `{name?, color?, description?}` (kind immutable), `DELETE /tags/{id}`.

Rules (rules:manage)
- `GET /rules` (sort order), `GET /rules/{id}`, `POST /rules` RuleInput,
  `PUT /rules/{id}` RuleInput, `DELETE /rules/{id}`.
- `POST /rules/test` RuleInput + sample `{subject, body, from, from_name,
  recipient, has_attachments, spam_score}` → `{matched, actions}` (dry-run helper
  for the builder).
- RuleInput: `{name, enabled, sort_order, match: all|any, conditions: [{field,
  operator, value}], expression?, actions: [{type: tag|assign|status|priority|drop,
  tag_kind?, tag_names?, assignee_id?, status?, priority?}]}`.

Mailboxes (mailboxes:manage)
- `GET /mailboxes`, `POST /mailboxes` `{address, display_name?, active?, auto_ack?,
  auto_ack_template?}`, `PUT /mailboxes/{id}`, `DELETE /mailboxes/{id}` (refused
  while tickets reference it unless `?force=true`, which detaches them).

Users, stats, system
- `GET /assignable-users` (tickets:read) → `{items: [{id, name, email}]}`.
- `GET /stats?days=30` (stats:read) → `{by_status, by_priority, by_assignee,
  unassigned_open, created_per_day[], resolved_per_day[]}`.
- `GET /stream` (tickets:read) — SSE of this tenant's ticket events.
- `POST /backup/export`, `POST /backup/import` (backup:manage; full/cross-tenant
  restore → platform-admin).
- `GET /health` (public).

No response includes relay/hook secrets, raw HTML bodies, or object-store
credentials.

## B. Inbound mail edge — separate listener (not gateway-proxied)

- `POST /inbound/mail`
  - Auth: `Authorization: Bearer <relay token>` or `X-Ticket-Token` (warden-held,
    constant-time compare). Query-string tokens are not accepted.
  - Body: raw RFC 822 (`Content-Type: message/rfc822`), ≤ 10 MiB.
  - Headers: `X-Iris-Recipient` | `X-Ticket-Recipient` (recipient mailbox),
    optional `X-Iris-Message-Id`.
  - Responses: `202 {outcome: created|threaded|duplicate|dropped, ticket_id?}`;
    `401/404/413` collapse to a generic refusal body (no mailbox enumeration);
    `503` when storage is unavailable (relay retries).
- `GET /healthz` (liveness for the relay).

## C. Module-to-module gRPC — `ticket.v1` (SPIFFE mTLS)

- `Tickets.Create {tenant, subject, description, priority, requester_email,
  requester_name, source_module}` → Ticket (lets other modules open tickets, e.g.
  monitoring or asset alerts).
- `Tickets.Get {tenant, id}` → Ticket; `Tickets.List {tenant, filter}` → page.
- `Tickets.AddComment {tenant, ticket_id, body, internal}` → Comment.
- Caller identity from the SPIFFE ID, checked against the policy allow-list;
  tenant from the verified platform context.

## D. Events — `platform:events:<tenant>`

`ticket.created`, `ticket.assigned`, `ticket.status_changed`, `ticket.commented`,
`ticket.requester_replied`. Payload: `{ticket_id, subject, status, priority,
assignee_id, source, actor_kind}` — never bodies, attachment data or requester
addresses.

## E. Internal interfaces (each with a fake for offline tests)

- `blob.Store`: `Put(ctx, key, r, size, contentType) (checksum)`, `Get`, `Delete`,
  `EnsureBucket` (reused from paperless/asset).
- `mailer.Sender`: `Send(ctx, OutMail{From, FromName, To, Subject, Text,
  MessageID, InReplyTo, References[], AutoReply bool}) error`; `Enabled() bool`.
- `agents.Directory`: `Assignable(ctx, tenant) ([]User, error)`,
  `Get(ctx, tenant, userID) (User, error)`.
- `secrets.Source`: `RelayToken(ctx)`, `SMTPPassword(ctx)` (warden references).
- `rules.Engine`: `Compile(RuleInput) (Program, error)`,
  `Evaluate(ctx, Program, Email) (bool, error)` with cost limit + deadline.
