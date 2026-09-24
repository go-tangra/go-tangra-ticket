-- +goose Up
-- Core ticket (helpdesk) tables (data-model.md). Every table carries tenant_id
-- and is RLS-protected (policies + grants in 0003_rls.sql); the append-only audit
-- hypertable lives in 0002_audit.sql. Every UNIQUE constraint / partial unique
-- index below is a conflict or idempotency guard and MUST be preserved:
--   * (tenant_id, external_id) on tickets and (tenant_id, message_id) on comments
--     make a duplicate inbound delivery a database-level no-op;
--   * mailbox address is GLOBAL: one address routes to exactly one tenant.
-- Attachment bytes live only in object storage (tenant-prefixed keys).
-- Child rows reference parents through (tenant_id, id) composite foreign keys,
-- so the database itself refuses a row that links across tenants (foreign-key
-- checks bypass RLS; the composite key closes that gap).
-- Referenced-parent tables are created first so the foreign keys resolve.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Mailboxes: inbound routing address -> tenant, reply From identity, auto-ack.
CREATE TABLE ticket_mailboxes (
  id                uuid PRIMARY KEY,
  tenant_id         uuid NOT NULL,
  address           text NOT NULL CHECK (address = lower(address) AND address <> ''),
  display_name      text NOT NULL DEFAULT 'Support',
  active            boolean NOT NULL DEFAULT true,
  auto_ack          boolean NOT NULL DEFAULT false,
  auto_ack_template text NOT NULL DEFAULT '',
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (address),
  UNIQUE (tenant_id, id)
);
CREATE INDEX ticket_mailboxes_tenant ON ticket_mailboxes (tenant_id);

-- Tickets.
CREATE TABLE ticket_tickets (
  id              uuid PRIMARY KEY,
  tenant_id       uuid NOT NULL,
  subject         text NOT NULL CHECK (subject <> '' AND char_length(subject) <= 998),
  description     text NOT NULL DEFAULT '',
  body_html       text NOT NULL DEFAULT '',
  status          text NOT NULL DEFAULT 'open'
                  CHECK (status IN ('open','in_progress','pending','resolved','closed')),
  priority        text NOT NULL DEFAULT 'normal'
                  CHECK (priority IN ('low','normal','high','urgent')),
  source          text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','email')),
  requester_email text NOT NULL DEFAULT '',
  requester_name  text NOT NULL DEFAULT '',
  recipient       text NOT NULL DEFAULT '',
  mailbox_id      uuid,
  external_id     text,
  assignee_id     text,
  assignee_name   text NOT NULL DEFAULT '',
  comment_count   integer NOT NULL DEFAULT 0,
  last_message_id text NOT NULL DEFAULT '',
  created_by      text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  resolved_at     timestamptz,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, mailbox_id) REFERENCES ticket_mailboxes (tenant_id, id) ON DELETE SET NULL (mailbox_id)
);
CREATE UNIQUE INDEX ticket_tickets_external_id ON ticket_tickets (tenant_id, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX ticket_tickets_status_created ON ticket_tickets (tenant_id, status, created_at DESC);
CREATE INDEX ticket_tickets_assignee ON ticket_tickets (tenant_id, assignee_id);
CREATE INDEX ticket_tickets_priority ON ticket_tickets (tenant_id, priority);
CREATE INDEX ticket_tickets_created ON ticket_tickets (tenant_id, created_at DESC);
CREATE INDEX ticket_tickets_mailbox ON ticket_tickets (tenant_id, mailbox_id) WHERE mailbox_id IS NOT NULL;
CREATE INDEX ticket_tickets_subject_trgm ON ticket_tickets USING gin (lower(subject) gin_trgm_ops);
CREATE INDEX ticket_tickets_requester_email_trgm ON ticket_tickets USING gin (lower(requester_email) gin_trgm_ops);
CREATE INDEX ticket_tickets_requester_name_trgm ON ticket_tickets USING gin (lower(requester_name) gin_trgm_ops);

-- Comments (conversation timeline).
CREATE TABLE ticket_comments (
  id           uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  ticket_id    uuid NOT NULL,
  body         text NOT NULL,
  internal     boolean NOT NULL DEFAULT false,
  author_kind  text NOT NULL CHECK (author_kind IN ('agent','requester','system')),
  author_id    text,
  author_name  text NOT NULL DEFAULT '',
  author_email text NOT NULL DEFAULT '',
  message_id   text,
  delivery     text NOT NULL DEFAULT 'none' CHECK (delivery IN ('none','sent','failed')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, ticket_id) REFERENCES ticket_tickets (tenant_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX ticket_comments_message_id ON ticket_comments (tenant_id, message_id) WHERE message_id IS NOT NULL;
CREATE INDEX ticket_comments_ticket ON ticket_comments (ticket_id, created_at);

-- Attachments (bytes in object storage).
CREATE TABLE ticket_attachments (
  id           uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  ticket_id    uuid NOT NULL,
  comment_id   uuid,
  filename     text NOT NULL,
  content_type text NOT NULL DEFAULT 'application/octet-stream',
  size         bigint NOT NULL DEFAULT 0 CHECK (size >= 0),
  content_id   text NOT NULL DEFAULT '',
  inline       boolean NOT NULL DEFAULT false,
  storage_key  text NOT NULL,
  checksum     text NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (storage_key),
  FOREIGN KEY (tenant_id, ticket_id) REFERENCES ticket_tickets (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, comment_id) REFERENCES ticket_comments (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX ticket_attachments_ticket ON ticket_attachments (ticket_id);
CREATE INDEX ticket_attachments_cid ON ticket_attachments (ticket_id, content_id);
CREATE INDEX ticket_attachments_comment ON ticket_attachments (tenant_id, comment_id) WHERE comment_id IS NOT NULL;

-- Tags (kind tag|category, immutable after create).
CREATE TABLE ticket_tags (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  name        text NOT NULL CHECK (name <> ''),
  kind        text NOT NULL DEFAULT 'tag' CHECK (kind IN ('tag','category')),
  color       text,
  description text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);
CREATE UNIQUE INDEX ticket_tags_kind_name ON ticket_tags (tenant_id, kind, lower(name));

CREATE TABLE ticket_tag_links (
  tenant_id uuid NOT NULL,
  ticket_id uuid NOT NULL,
  tag_id    uuid NOT NULL,
  PRIMARY KEY (ticket_id, tag_id),
  FOREIGN KEY (tenant_id, ticket_id) REFERENCES ticket_tickets (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, tag_id) REFERENCES ticket_tags (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX ticket_tag_links_tag ON ticket_tag_links (tenant_id, tag_id);

-- Rules (ordered CEL triage rules).
CREATE TABLE ticket_rules (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  name        text NOT NULL CHECK (name <> ''),
  enabled     boolean NOT NULL DEFAULT true,
  sort_order  integer NOT NULL DEFAULT 0,
  match       text NOT NULL DEFAULT 'all' CHECK (match IN ('all','any')),
  conditions  jsonb NOT NULL DEFAULT '[]'::jsonb,
  expression  text NOT NULL DEFAULT '',
  actions     jsonb NOT NULL DEFAULT '[]'::jsonb,
  version     integer NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_rules_enabled_order ON ticket_rules (tenant_id, enabled, sort_order);

-- History of status/priority/assignee changes.
CREATE TABLE ticket_history (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  ticket_id  uuid NOT NULL,
  field      text NOT NULL CHECK (field IN ('status','priority','assignee')),
  old_value  text NOT NULL DEFAULT '',
  new_value  text NOT NULL DEFAULT '',
  actor_kind text NOT NULL CHECK (actor_kind IN ('agent','rule','inbound','system')),
  actor_id   text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, ticket_id) REFERENCES ticket_tickets (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX ticket_history_ticket ON ticket_history (ticket_id, created_at);
CREATE INDEX ticket_history_stats ON ticket_history (tenant_id, field, new_value, created_at);

-- +goose Down
DROP TABLE IF EXISTS ticket_history;
DROP TABLE IF EXISTS ticket_rules;
DROP TABLE IF EXISTS ticket_tag_links;
DROP TABLE IF EXISTS ticket_tags;
DROP TABLE IF EXISTS ticket_attachments;
DROP TABLE IF EXISTS ticket_comments;
DROP TABLE IF EXISTS ticket_tickets;
DROP TABLE IF EXISTS ticket_mailboxes;
