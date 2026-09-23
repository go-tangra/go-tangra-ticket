# Phase 1 Data Model: Ticket Service

All tables carry `tenant_id uuid NOT NULL` with per-tenant RLS (research D1).
IDs are application-generated strings (UUIDv7). Timestamps `timestamptz`. Enums
are stored as short lower-case text. Message bodies and requester PII are never
logged; audit detail redacts them. Attachment bytes live only in object storage
(keys tenant-prefixed). Every unique constraint below is a conflict/idempotency
guard and MUST be preserved.

## Entities

### ticket_tickets  (RLS)
- `id` (PK), `tenant_id`, `subject` (NOT NULL; "(no subject)" when empty),
  `description` (plain text body), `body_html` (raw HTML as received; only served
  sanitised), `status` (open|in_progress|pending|resolved|closed, default open),
  `priority` (low|normal|high|urgent, default normal), `source` (manual|email),
  `requester_email`, `requester_name`, `recipient` (mailbox address),
  `mailbox_id` (FK, null for manual), `external_id` (root message id, null for
  manual), `assignee_id` (platform user id, null = unassigned), `assignee_name`
  (denormalised), `comment_count` (maintained), `last_message_id` (for
  In-Reply-To), `created_by` (user id | "inbound-mail"), `created_at`,
  `updated_at`, `resolved_at` (set on transition into resolved, cleared on
  re-open).
- **Unique**: `(tenant_id, external_id) WHERE external_id IS NOT NULL`.
- Indexes: `(tenant_id, status, created_at DESC)`, `(tenant_id, assignee_id)`,
  `(tenant_id, priority)`, `(tenant_id, created_at DESC)`, trigram or lower()
  indexes on subject / requester_email / requester_name for the text query.

### ticket_comments  (RLS)
- `id` (PK), `tenant_id`, `ticket_id` (FK ON DELETE CASCADE), `body` (NOT NULL),
  `internal` (bool), `author_kind` (agent|requester|system), `author_id` (user id,
  null for requester/system), `author_name`, `author_email`, `message_id` (null for
  internal notes), `delivery` (none|sent|failed — for public agent replies and
  acknowledgements), `created_at`.
- **Unique**: `(tenant_id, message_id) WHERE message_id IS NOT NULL` (inbound
  dedup + threading lookup).
- Indexes: `(ticket_id, created_at)`.

### ticket_attachments  (RLS)
- `id` (PK), `tenant_id`, `ticket_id` (FK ON DELETE CASCADE), `comment_id` (FK,
  null when it came with the ticket's first message), `filename` (safe base name),
  `content_type`, `size`, `content_id` (MIME Content-ID, for `cid:` images),
  `inline` (bool), `storage_key` (object key), `checksum` (sha256), `created_at`.
- **Unique**: `storage_key`. Indexes: `(ticket_id)`, `(ticket_id, content_id)`.
- Object deletion accompanies row deletion (ticket delete removes both).

### ticket_tags  (RLS)
- `id` (PK), `tenant_id`, `name`, `kind` (tag|category, immutable after create),
  `color` (null → deterministic automatic colour in the UI), `description`,
  `created_at`.
- **Unique**: `(tenant_id, kind, lower(name))`.

### ticket_tag_links  (RLS)
- `tenant_id`, `ticket_id` (FK ON DELETE CASCADE), `tag_id` (FK ON DELETE CASCADE).
- **PK/unique**: `(ticket_id, tag_id)`. Index `(tenant_id, tag_id)` for the tag filter.

### ticket_rules  (RLS)
- `id` (PK), `tenant_id`, `name` (NOT NULL), `enabled` (default true),
  `sort_order` (int; lower runs first), `match` (all|any), `conditions` (JSONB array
  of {field, operator, value}), `expression` (raw CEL, overrides conditions when
  non-empty), `actions` (JSONB array of {type, tag_kind, tag_names[], assignee_id,
  status, priority}), `version` (int, bumps on update — program cache key),
  `created_at`, `updated_at`.
- Validation on save: every condition field/operator known and value typed;
  effective expression compiles and type-checks to bool; at least one action;
  referenced assignee assignable; status/priority valid.
- Indexes: `(tenant_id, enabled, sort_order)`.

### ticket_mailboxes  (RLS for management; routing via a system-scoped lookup)
- `id` (PK), `tenant_id`, `address` (lower-cased email address), `display_name`
  (From name for replies, default "Support"), `active` (bool), `auto_ack` (bool),
  `auto_ack_template` (text with `{{name}}`; the reference line is always appended),
  `created_at`, `updated_at`.
- **Unique**: `address` (global — one address → one tenant).
- The inbound edge resolves `address → (tenant_id, mailbox)` through a narrow
  security-definer function that returns only routing fields.

### ticket_history  (RLS)
- `id` (PK), `tenant_id`, `ticket_id` (FK ON DELETE CASCADE), `field`
  (status|priority|assignee), `old_value`, `new_value`, `actor_kind`
  (agent|rule|inbound|system), `actor_id`, `created_at`.
- Indexes: `(ticket_id, created_at)`, `(tenant_id, field, new_value, created_at)`
  (resolved-per-day statistics).

### ticket_audit_events  (hypertable, append-only)
- Framework audit vocabulary: actor (user id | "inbound-mail" | "system"),
  tenant, action, subject, outcome, redacted detail, time.

## Relationships
- Ticket 1–N Comments, Attachments, History; Ticket N–M Tags (via links);
  Mailbox 1–N Tickets (email source); Attachment optionally belongs to a Comment.
- Platform user (auth directory) is referenced by id from assignee/author fields;
  no local user table.

## State transitions (ticket status)
- Any status may be set explicitly by an agent (tickets:manage) or a rule action
  (spec FR-002); `unspecified` is refused.
- Automatic: a public agent reply or an inbound requester reply on a
  `resolved`/`closed` ticket sets it to `open` (history actor agent/inbound).
- Entering `resolved` sets `resolved_at`; leaving it clears it.
- Every change writes a `ticket_history` row and publishes `ticket.status_changed`.

## Validation rules (from requirements)
- Subject 1–998 chars (RFC line limit); description/body bounded by the inbound caps.
- Requester email, mailbox address: valid addr-spec, no control characters.
- Assignee: a user of the same tenant holding tickets:manage (FR-004).
- Tag set on a ticket: every id exists in the tenant (FR-010).
- Inbound message ≤ 10 MiB, attachment ≤ 25 MiB, ≤ 100 parts, MIME depth ≤ 10
  (configurable; research D3).
