# Quickstart: Ticket Service

Validation scenarios proving the feature end to end. Assumes the `deploy/stack`
platform is up (TimescaleDB, Valkey, RustFS, warden, gateway, auth, notification)
with the ticket service registered, and **Mailpit** (already in the stack) as the
outbound relay so sent replies can be inspected. Contracts: [ticket-api.md](./contracts/ticket-api.md);
data: [data-model.md](./data-model.md).

## Prerequisites
- ticket service running: gateway `/api/ticket`, inbound edge listener reachable
  from the host; relay token and SMTP password stored in warden and referenced by
  the ticket config.
- An operator signed in with the ticket admin role; a second user with the agent
  role (assignee); a viewer-role user for negative checks.
- A mailbox `support@acme.test` created for the operator's tenant, auto-ack on.
- Sample `.eml` fixtures under `services/ticket/testdata/mail/` (plain, HTML with
  inline image, multipart with attachments, legacy charset, auto-submitted, spam).

## Scenario 1 — Manual ticket lifecycle (US1)
1. New ticket (subject only) → open, normal, source manual; appears in the list.
2. Filter by status/priority/assignee/tag/text → only matching tickets; paging total correct.
3. Assign to the agent → assignee name shown; assign to a non-agent user → refused (`invalid_assignee`); unassign.
4. Status open → in progress → resolved → closed; history lists each change with actor; setting an empty status is refused.
5. As viewer: list/read works; any change is refused (403).

## Scenario 2 — Inbound email (US2)
1. `POST` a fixture to the inbound edge with the token and `X-Iris-Recipient: support@acme.test` → `202 created`; the ticket shows requester, recipient, subject, text + sanitised HTML with its inline image, and downloadable attachments.
2. Re-post the same message → `202 duplicate`; nothing new stored.
3. Post a reply fixture (In-Reply-To = original Message-Id) → `202 threaded`; a requester comment appears; resolve the ticket first and repeat → it returns to open.
4. Wrong/missing token, unknown mailbox, oversized body → identical generic refusal; nothing stored; audit records the refusal.
5. Legacy-charset and quoted-printable fixtures render readable subject/body.

## Scenario 3 — Replies and notes (US3)
1. Add an internal note → shown with "internal" marking; Mailpit receives nothing.
2. Send a public reply → Mailpit shows one message to the requester, subject `Re: …`, body ending with `Ticket reference: [#<id>]`, `In-Reply-To`/`References` set, From = mailbox address; the comment carries its message id.
3. Reply to that message (post it to the inbound edge) → threads onto the same ticket.
4. Stop Mailpit → a public reply returns an error and the comment shows `delivery=failed`.

## Scenario 4 — Rules (US4)
1. Rule "subject contains invoice → tag billing (category), assign agent"; post a matching fixture → ticket tagged + assigned; non-matching fixture → untouched.
2. Rule "spam score gt 5 → drop"; post the spam fixture → `202 dropped`, no ticket; audit + metric recorded.
3. Save a rule with an invalid expression → refused with the compiler message; `POST /rules/test` shows match/no-match for a sample.
4. Two rules with different assignees → the first in sort order wins; tags from both accumulate.
5. Reply fixture matching the drop rule → still threads (rules skip replies).

## Scenario 5 — Tags (US5)
1. Create tag + category with the same name → allowed (different kinds); duplicate in the same kind → refused.
2. Set a ticket's tags; filter the list by one; rename it → ticket shows the new name; delete it → removed from the ticket.

## Scenario 6 — Acknowledgement, events, dashboard (US6)
1. New email ticket → Mailpit shows an acknowledgement with the requester's name and the reference, headers `Auto-Submitted: auto-replied`; it appears as a system comment.
2. Auto-submitted / bulk / no-reply / self-addressed fixtures → ticket created, no acknowledgement.
3. Assign a ticket → a `ticket.assigned` event on the tenant stream (no body/PII in the payload); an open UI refreshes.
4. Dashboard → counts by status/priority/assignee, unassigned open, created/resolved per day.

## Security checks (cross-cutting)
- A hostile HTML fixture (script, onerror, remote img, form, CSS url()) renders with nothing executing and no network request to the remote host.
- A reply fixture whose headers reference another tenant's ticket id never attaches to it.
- Attachment download of another tenant's attachment id → 404.
- Logs, events and audit contain no requester addresses, bodies, relay token or SMTP password.
- A header-injection attempt in a requester address or subject (CR/LF) → reply refused.
- Export/import round-trips a tenant; non-admin import lands only in the caller's tenant.
