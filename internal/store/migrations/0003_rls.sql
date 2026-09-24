-- +goose Up
-- Per-tenant row-level security on every ticket_* table. ticket_app is
-- NOBYPASSRLS; every statement runs with app.tenant_id set to the caller's
-- tenant. Trusted system paths (audit writer, tenant enumeration) set
-- app.system='on' (with app.tenant_id pinned to the nil uuid so the uuid cast
-- stays valid) so the policy admits their cross-tenant access.
--
-- The inbound mail edge does NOT use the system scope for routing: it resolves a
-- recipient address to its mailbox through ticket_route_mailbox(), a narrow
-- SECURITY DEFINER function that returns routing fields only, and then runs every
-- write under a tenant scope pinned to the routed tenant.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'ticket_mailboxes','ticket_tickets','ticket_comments','ticket_attachments',
    'ticket_tags','ticket_tag_links','ticket_rules','ticket_history','ticket_audit_events'
  ]
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on') WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')$p$, t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO ticket_app', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- Routing lookup for the inbound edge: address -> tenant + mailbox settings.
-- Runs as the owner with the system scope set for its own duration only (FORCE
-- ROW LEVEL SECURITY applies to a non-superuser owner too); returns routing
-- fields only, at most one row.
-- +goose StatementBegin
CREATE FUNCTION ticket_route_mailbox(p_address text)
RETURNS TABLE (tenant_id uuid, mailbox_id uuid, display_name text, active boolean, auto_ack boolean, auto_ack_template text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp
SET app.system = 'on'
SET app.tenant_id = '00000000-0000-0000-0000-000000000000'
AS $f$
  SELECT m.tenant_id, m.id, m.display_name, m.active, m.auto_ack, m.auto_ack_template
  FROM ticket_mailboxes m
  WHERE m.address = lower(btrim(p_address))
  LIMIT 1
$f$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION ticket_route_mailbox(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ticket_route_mailbox(text) TO ticket_app;

-- +goose Down
DROP FUNCTION IF EXISTS ticket_route_mailbox(text);
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'ticket_mailboxes','ticket_tickets','ticket_comments','ticket_attachments',
    'ticket_tags','ticket_tag_links','ticket_rules','ticket_history','ticket_audit_events'
  ]
  LOOP
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
  END LOOP;
END $$;
-- +goose StatementEnd
