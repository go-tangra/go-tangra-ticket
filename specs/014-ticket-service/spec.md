# Feature Specification: Ticket Service

**Feature Branch**: `014-ticket-service`

**Created**: 2026-09-23

**Status**: Draft

**Input**: User description: "Create a new Freya module service called "ticket" (services/ticket) that replicates the functionality of the reference project /home/jadmin/projects/go-tangra/go-tangra-ticket, adapted to Freya platform conventions (same approach as prior replicas: deployer 008, paperless 009, inventory 010, ipam 011, asset 012)."

## Overview

The **ticket** service is a tenant-scoped **email-driven helpdesk** module. Support
mail sent to a tenant's support address arrives through an authenticated inbound
mail hook and becomes a **ticket** (subject, plain and HTML body, requester,
attachments); replies to the same conversation thread back onto that ticket as
comments and re-open it if it was resolved. Agents triage tickets through a
lifecycle (open → in progress → pending → resolved → closed) with priorities,
assignment to platform users, **tags** (grouped by kind, coloured), **internal
notes** and **public replies** that are emailed to the requester with threading
headers so the conversation stays on one ticket. An ordered set of **rules**
evaluates every inbound message (conditions on subject/body/sender/recipient/
domain/attachments/spam score, combined all/any, or an advanced expression) and
applies actions: tag, assign, set status, set priority, or drop the message
(spam). New tickets can receive an automatic acknowledgement, guarded against
mail loops. Tickets can also be opened manually in the UI.

Freya adaptations (vs the source): SPIFFE mTLS + gateway platform token replace
the mTLS-CN trust and LCM cert bootstrap; TimescaleDB + per-tenant row-level
security replace ent app-level tenant filtering; the inbound hook's shared token
is held in the platform secret store (warden), not an environment variable, and
inbound mail is routed to a tenant by its **support mailbox** (recipient address)
instead of a single process-wide default tenant; attachments live in the
platform object store; assignees are platform users resolved from the auth
directory; ticket, comment and assignment events are published to the platform
event bus for notification and live UI; every mutation is audited.

Enhancements over the reference (which lacks them or leaves them unfinished):
per-tenant mailbox routing, enforced API permissions, tenant-scoped attachment
downloads, a per-ticket change history, a tag filter on the list, agent
notifications via platform events, a statistics dashboard, clean removal of a
deleted ticket's attachments, and server-side assignee name resolution.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Work tickets through their lifecycle (Priority: P1)

An agent opens the ticket list, filters by status, priority, assignee or text,
opens a ticket in a side panel, reads the request, sets its priority, assigns it
to a colleague (or themselves), moves it to in-progress, and finally resolves and
closes it. Tickets can also be created manually for requests that arrive by phone
or chat.

**Why this priority**: This is the MVP — a tenant-isolated ticket queue with an
assignment and status lifecycle. Every other capability feeds or enriches it.

**Independent Test**: Create a ticket manually, list and filter it, assign it,
change its status and priority, and delete it — without any email involved.

**Acceptance Scenarios**:

1. **Given** an agent with manage rights, **When** they create a ticket with a subject (and optional description, priority, requester and assignee), **Then** it is stored as open with source "manual", and appears in the list.
2. **Given** tickets exist, **When** the list is filtered by status, priority, assignee, tag or a text query (subject, requester name or address), **Then** only matching tickets of the caller's tenant are returned, newest first, with a total count for paging.
3. **Given** a ticket, **When** it is assigned to a platform user, **Then** the assignee and their display name are recorded; assigning to "nobody" unassigns it; assigning to an unknown user is refused.
4. **Given** a ticket, **When** its status is changed to any lifecycle value, **Then** the change is saved, recorded in the ticket's history, and an unspecified status is refused.
5. **Given** a ticket, **When** its subject, description or priority is edited, **Then** only those fields change.

---

### User Story 2 - Turn inbound support email into tickets (Priority: P1)

A customer emails the tenant's support address. The message becomes a ticket with
the sender as requester, the decoded plain and HTML body, and any attachments;
inline images display inside the rendered message. When the customer replies to
the conversation, the reply is appended to the same ticket instead of opening a
new one, and a resolved or closed ticket re-opens.

**Why this priority**: Email is the reference system's primary intake channel;
without it the service is only a manual queue.

**Independent Test**: Post a raw email to the inbound hook for a configured
support mailbox, see a new ticket with body and attachments; post a reply to it
and see a public comment on the same ticket and the status re-opened.

**Acceptance Scenarios**:

1. **Given** a support mailbox mapped to a tenant, **When** a raw email addressed to it is delivered to the inbound hook with the valid hook token, **Then** a ticket is created in that tenant with subject, sender name/address as requester, recipient, plain and HTML body, and every attachment (inline or not) stored and downloadable.
2. **Given** the same message is delivered twice, **When** the second delivery arrives, **Then** it is recognised by its message id and nothing is duplicated.
3. **Given** an existing ticket, **When** a reply arrives that references it (by the message threading chain, or by the ticket reference in its body or subject), **Then** it is recorded as a public comment from the requester, its attachments are stored on the ticket, and a resolved/closed ticket returns to open.
4. **Given** a request without a valid hook token, or for a recipient that is not a configured mailbox, **When** it reaches the hook, **Then** it is refused and nothing is stored.
5. **Given** mail with many character sets and encodings (base64, quoted-printable, legacy code pages), **When** it is ingested, **Then** subject, names and bodies are decoded to readable text; an oversized message or attachment is refused or skipped with a clear log entry.

---

### User Story 3 - Converse with the requester: public replies and internal notes (Priority: P1)

On a ticket, an agent writes an internal note visible only to agents, or a public
reply that is emailed to the requester. The requester's email client threads the
reply with the original conversation, and their answer comes back onto the same
ticket.

**Why this priority**: Two-way communication is what makes the queue a helpdesk;
it completes the email loop started in User Story 2.

**Independent Test**: Add an internal note and a public reply to a ticket; see
both in the conversation, confirm only the reply was sent by email with threading
information and a ticket reference, and that a reply to that email threads back.

**Acceptance Scenarios**:

1. **Given** a ticket, **When** an agent adds an internal note, **Then** it is stored as internal, shown only to agents, and never emailed.
2. **Given** a ticket with a requester address and outbound email configured, **When** an agent sends a public reply, **Then** an email goes to the requester with a single "Re:" subject, a ticket reference line in the body and threading headers linking it to the conversation, and the reply is stored as a public comment carrying its message id; a resolved or closed ticket returns to open.
3. **Given** outbound email is not configured or fails, **When** a public reply is sent, **Then** the agent is told clearly that it was not delivered (it is not silently lost).
4. **Given** comments on a ticket, **When** the conversation is read, **Then** comments are listed oldest first with author (agent, requester or system), time and internal/public marking; an agent can delete a comment they are allowed to manage.

---

### User Story 4 - Automate triage with rules (Priority: P2)

An administrator defines ordered rules that run on every inbound message: for
example "subject contains 'invoice' → tag billing and assign to Maria", "sender
domain equals example.net → priority high", or "spam score > 5 → drop". Rules can
be built from conditions (all/any) or, for power users, written as an advanced
expression.

**Why this priority**: Automation saves agent time and filters spam, but the
service is fully usable without it.

**Independent Test**: Create a tag rule, an assign rule and a drop rule, deliver
matching and non-matching emails, and confirm the resulting tags/assignee, and
that the dropped message produced no ticket.

**Acceptance Scenarios**:

1. **Given** enabled rules in sort order, **When** an inbound message is evaluated, **Then** every matching rule's actions are applied to the new ticket (tag with named tags of a kind — creating missing tags — assign, set status, set priority).
2. **Given** a matching rule with a drop action, **When** the message is evaluated, **Then** no ticket is created and the drop is recorded in the audit trail and metrics.
3. **Given** conditions on subject, body, sender, sender name, recipient, sender domain (text operators: contains, not contains, equals, not equals, starts with, ends with, matches pattern), has-attachments, and spam score (numeric comparisons), combined with ALL or ANY, **When** a rule is saved, **Then** it is validated; an invalid condition or advanced expression is refused with the reason.
4. **Given** a disabled rule, or a rule that errors during evaluation, **When** mail arrives, **Then** that rule is skipped and ingestion still succeeds.

---

### User Story 5 - Organise with tags (Priority: P2)

An agent maintains a tag vocabulary of two kinds — free-form tags and categories —
each with a colour and description, and sets a ticket's tags from it; the list can
be filtered by tag.

**Why this priority**: Tags give structure for reporting and rules but are not
required to work a ticket.

**Independent Test**: Create tags of two kinds, set tags on a ticket, filter the
list by a tag, rename a tag and see the ticket reflect it, delete a tag.

**Acceptance Scenarios**:

1. **Given** the tag vocabulary, **When** a tag is created with kind tag or category (default tag), **Then** its name is unique per tenant and kind, a duplicate is refused, and its kind cannot be changed later; a tag without a colour gets a stable automatic one.
2. **Given** a ticket, **When** its tags are set to a list, **Then** the ticket carries exactly that list (replacing the previous set) and unknown tag ids are refused.
3. **Given** a tag in use, **When** it is deleted, **Then** it is removed from all tickets that carried it.

---

### User Story 6 - Auto-acknowledge, notify, and report (Priority: P3)

When a new ticket arrives by email, the requester receives an acknowledgement
that contains their ticket reference (unless the mail looks automated). Agents
are notified of new tickets and of tickets assigned to them, and a dashboard
shows open work by status, priority and assignee and intake volume over time.

**Why this priority**: These improve responsiveness and visibility but are
additive to the core loop.

**Independent Test**: Deliver a new email and see the acknowledgement sent and
recorded; deliver a bulk/auto-submitted email and see no acknowledgement; assign
a ticket and see an assignment event; read dashboard figures.

**Acceptance Scenarios**:

1. **Given** auto-acknowledgement is enabled for the mailbox, **When** a new ticket is created from email, **Then** the requester is emailed an acknowledgement (from a per-mailbox template with the requester's name substituted) containing the ticket reference, and it is recorded as a public system comment so a reply to it threads back.
2. **Given** the inbound mail is auto-submitted, bulk/list mail, from a no-reply/daemon address, or from the support mailbox itself, **When** it is ingested, **Then** no acknowledgement is sent (mail-loop protection).
3. **Given** tickets are created, assigned, commented on or change status, **When** those happen, **Then** corresponding events are published for notification and live UI refresh, without exposing message bodies or attachments in the event payload.
4. **Given** tickets exist, **When** the dashboard is read, **Then** counts by status, by priority and by assignee, unassigned open tickets, and tickets created/resolved per day are shown for the tenant.

### Edge Cases

- A reply references a ticket that was deleted: it opens a new ticket instead of failing.
- A reply's threading chain points at a ticket in another tenant: it is never attached across tenants; the mailbox's tenant decides.
- An email with no subject gets a placeholder subject; one with no plain-text body falls back to text derived from the HTML body.
- HTML bodies are shown safely: scripts, event handlers and remote content never execute in the agent's browser; inline images resolve to the ticket's own attachments only.
- Attachment names with path characters are sanitised; an attachment over the size cap is skipped and noted on the ticket; the whole message over the body cap is refused.
- Two rules set conflicting values (e.g. two assignees or statuses): the first matching rule in sort order wins; tags from all matching rules accumulate.
- A drop rule matches a reply to an existing ticket: replies to existing tickets are never dropped by rules (rules apply to new tickets only).
- Deleting a ticket removes its comments, tag links and attachments (including stored files).
- Assigning to a user who later leaves the tenant: the ticket keeps the id and last known name, shown as an inactive assignee.
- Outbound email, object storage or the user directory being unavailable degrades gracefully: ingestion stores what it can and reports, replies fail visibly, names fall back to ids.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST let agents create, read, list, update (subject, description, priority), and delete tickets; manually created tickets have source "manual", email tickets source "email".
- **FR-002**: System MUST support ticket statuses open, in progress, pending, resolved and closed, and priorities low, normal, high and urgent; any status may be set explicitly, an unspecified value is refused, and every status/priority/assignment change is recorded in the ticket's history with actor and time.
- **FR-003**: System MUST list tickets per tenant with paging and a total, newest first, filterable by status, priority, assignee (including unassigned), tag, and a case-insensitive text query over subject, requester name and requester address.
- **FR-004**: System MUST let agents assign a ticket to an assignable platform user of the same tenant or unassign it, recording the assignee's display name resolved from the platform user directory, and MUST expose the list of assignable users.
- **FR-005**: System MUST accept inbound email through an authenticated inbound hook: raw RFC 822 messages, authenticated by a hook token held in the platform secret store and compared in constant time, routed to a tenant by the recipient's configured support mailbox.
- **FR-006**: System MUST parse inbound mail: decode MIME multipart structure, transfer encodings and common legacy character sets; extract subject, sender name/address, recipient, plain and HTML bodies, message id and threading references, spam score, and auto-submitted/bulk indicators; and store every attachment (with content type, size, content id and inline flag) in object storage, enforcing size caps per message and per attachment.
- **FR-007**: System MUST de-duplicate inbound deliveries by message id, and MUST thread replies onto existing tickets of the same tenant — by message-id chain (ticket root or recorded comment), then by a ticket reference token in the body or subject — appending them as public requester comments and re-opening resolved/closed tickets.
- **FR-008**: System MUST support ticket comments: internal notes (never emailed, agents only) and public replies; list them oldest first with author kind (agent, requester, system), author name/address and message id; and allow deletion by authorised agents.
- **FR-009**: System MUST send public replies to the requester by email with a single "Re:" subject, a ticket reference line appended to the body once, and threading headers (message id, in-reply-to, references) linking to the conversation, and MUST report delivery failure to the agent.
- **FR-010**: System MUST let agents CRUD tags (kind tag or category, fixed after creation; name unique per tenant+kind; colour; description), list them (optionally by kind), and set a ticket's tag list; deleting a tag removes it from tickets.
- **FR-011**: System MUST let administrators CRUD ordered rules (name, enabled, sort order, match ALL/ANY, conditions, optional advanced expression overriding the conditions, actions), validating conditions and expressions on save.
- **FR-012**: System MUST evaluate enabled rules in sort order against each inbound message that would create a new ticket, applying actions tag (named tags of a kind, created if missing; tags accumulate across matching rules), assign, set status, set priority (for these three the first matching rule wins), and drop (discard the message without creating a ticket); rules never run on manual tickets or on replies to existing tickets; a rule that fails to evaluate is skipped and logged.
- **FR-013**: System MUST optionally send an acknowledgement for new email tickets per mailbox (template with the requester name substituted and the ticket reference always present), mark it as an automatic reply (auto-submitted headers) so other systems do not answer it, record it as a public system comment, and suppress it for auto-submitted, bulk, daemon/no-reply senders and self-addressed mail.
- **FR-014**: System MUST let administrators manage support mailboxes (address, tenant, display name, auto-acknowledgement on/off and template, active flag); an address maps to exactly one tenant.
- **FR-015**: System MUST render a ticket's HTML body for agents in a sanitised, script-free form with inline images resolved to the ticket's attachments, and provide attachment downloads via short-lived links.
- **FR-016**: System MUST publish events for ticket created, assigned, status changed, commented and requester-replied to the platform event bus for notification and live UI, carrying identifiers and metadata only.
- **FR-017**: System MUST provide dashboard statistics per tenant: counts by status, by priority and by assignee, unassigned open tickets, and created/resolved per day over a selectable window.
- **FR-018**: System MUST register its routes, API permissions, UI abilities and navigation with the application gateway, expose service-to-service APIs for other modules (e.g. to open a ticket programmatically), and ship its own UI module (ticket list with filters and detail drawer, rules builder, tags, mailboxes, dashboard).
- **FR-019**: System MUST enforce API permissions (view tickets; manage tickets — edit, assign, status, comment, reply, tags; delete tickets; manage tags; manage rules; manage mailboxes; view statistics), seeded as the roles ticket admin (all), agent (view and manage) and viewer (view only), and record an append-only audit entry for every mutation, ingestion (accepted, threaded, duplicate, dropped, refused) and outbound email.
- **FR-020**: System MUST expose operational metrics: tickets ingested, threaded, duplicated, dropped by rule, refused; replies sent/failed; and status transitions.
- **FR-021**: System MUST support per-tenant export/import of all ticket data (tickets, comments, tags and links, rules, mailboxes, attachment metadata by reference) with skip/overwrite handling; full or cross-tenant restore MUST be restricted to platform administrators and secrets are never exported.

### Security Requirements *(mandatory — Constitution: Development Workflow)*

- **Trust boundaries crossed**: public inbound mail hook (from the mail relay); browser API via the application gateway; service-to-service mesh (auth user directory, warden, notification); object storage; outbound mail relay; the shared event bus.
- **Data classification**: ticket content, bodies and attachments (tenant-confidential, may contain personal data); requester names and email addresses (PII); the inbound hook token and mail-relay credentials (secrets); audit records (tamper-evident).
- **Authentication/Authorization**: browser callers use the gateway platform token and API permissions; module callers use SPIFFE mTLS; the inbound hook uses a secret token plus mailbox routing; RLS isolates tenants.
- **Threat scenarios**: forged inbound mail injected without the token; a crafted reply threading into another tenant's ticket; stored XSS through HTML email bodies; malicious attachments (path traversal names, oversized payloads, zip bombs); mail loops/amplification via auto-replies; header injection in outbound replies; leakage of requester PII or message bodies into logs, events or audit; rule expressions used for denial of service.
- **SR-001**: All ticket, comment, tag, rule, attachment and mailbox data MUST be isolated per tenant by row-level security; threading lookups MUST be confined to the tenant resolved from the mailbox.
- **SR-002**: The inbound hook MUST reject requests without the valid token (constant-time comparison), MUST bound request and attachment sizes, MUST not reveal whether a mailbox exists, and the token and mail-relay credentials MUST come from the platform secret store and never appear in any response, log, audit entry or event.
- **SR-003**: HTML bodies MUST be sanitised before display (no scripts, event handlers, forms or remote resource loads); attachments MUST be served as downloads with safe content types and sanitised file names, never rendered inline as active content.
- **SR-004**: Outbound email MUST prevent header injection (addresses and subjects validated), MUST only be sent to the ticket's requester address, and auto-acknowledgements MUST follow mail-loop protections (RFC 3834 auto-submitted handling, daemon/self sender suppression).
- **SR-005**: Rule expressions MUST be evaluated in a sandbox with bounded cost and time; an expression cannot access anything beyond the message fields.
- **SR-006**: Requester PII and message bodies MUST be excluded from logs and event payloads and redacted in audit detail; every operation MUST be audited with actor (or "inbound mail"), tenant, subject and outcome.

### Key Entities *(include if feature involves data)*

- **Ticket**: a support request in a tenant; subject, plain and HTML body, status, priority, source (manual/email), requester name and address, recipient mailbox, external message id, assignee (platform user id + name), comment count, created/updated.
- **Comment**: an entry in a ticket's conversation; body, internal/public, author kind (agent/requester/system), author id/name/address, message id for threading.
- **Attachment**: a file belonging to a ticket, received with inbound mail; name, content type, size, content id, inline flag, stored object.
- **Tag**: a coloured label of kind tag or category, unique per tenant+kind; many-to-many with tickets.
- **Rule**: an ordered, enabled/disabled automation; match mode, conditions, optional advanced expression, actions.
- **Support mailbox**: an inbound address routed to a tenant, with auto-acknowledgement settings.
- **Ticket history entry**: a recorded change of status, priority or assignee with actor and time.
- **Platform user**: an assignee or agent, resolved from the platform user directory (no local table).

## Success Criteria *(mandatory)*

- **SC-001**: An agent can find, assign and change the status of a ticket from the list in under 30 seconds.
- **SC-002**: 100% of inbound emails addressed to a configured mailbox with a valid token result in exactly one ticket or one threaded comment; repeated deliveries of the same message never create duplicates.
- **SC-003**: Replies are threaded onto the correct ticket in at least 99% of cases where the requester replies to an agent's or acknowledgement email, and never onto another tenant's ticket.
- **SC-004**: A new email ticket appears in the agent's list within 5 seconds of delivery to the inbound hook.
- **SC-005**: A public reply reaches the requester's mailbox and displays in the same conversation thread in standard mail clients; delivery failures are shown to the agent 100% of the time.
- **SC-006**: Rules apply the configured tags/assignee/status/priority to 100% of matching messages in test cases, and drop rules prevent ticket creation for 100% of matching spam; a broken rule never blocks ingestion.
- **SC-007**: Zero script execution or remote content loading from HTML email bodies in the agent UI (verified with hostile samples), and no requester PII, message body, hook token or relay credential appears in logs, events or audit detail.
- **SC-008**: No acknowledgement is ever sent in response to auto-submitted, bulk, daemon or self-addressed mail (0 mail loops in loop-test scenarios).
- **SC-009**: The service manages at least 100,000 tickets per tenant with list, search and dashboard queries returning in under 3 seconds.

## Assumptions

- A mail relay (as with the reference system's iris/KumoMTA) receives mail for support addresses and forwards each raw message to the inbound hook with the recipient identified; operating that relay is outside this feature.
- Outbound replies go through a configured mail relay; when none is configured, public replies are refused with a clear message rather than queued indefinitely.
- Agents are platform users of the tenant with the relevant permissions; requesters are external email addresses and have no login (no customer portal).
- Agents cannot attach files to replies (outbound replies are plain text, as in the reference); attachments arrive only with inbound mail.
- Status changes are unrestricted between lifecycle values (as in the reference); reports treat resolved and closed as "done".
- The ticket reference shown to requesters is derived from the ticket id; message and attachment size caps default to the reference's limits (10 MiB per message body, 25 MiB per attachment) and are configurable.
- The platform provides tenant identity, the gateway, the event bus, the audit trail, identity issuance, the secret store, object storage, notification and the auth user directory; this feature consumes them.

## Out of Scope

- Service-level agreements, due dates, escalation timers and business-hours calendars (the reference has none).
- A customer self-service portal, customer accounts, or satisfaction surveys.
- Channels other than email and manual entry (chat, phone integration, social media).
- Ticket merging/splitting, parent/child tickets, and knowledge-base articles.
- Operating the inbound/outbound mail infrastructure itself (DNS, MX, relay, spam scoring — the spam score is read from the relay's header).
