-- +goose Up
-- Index-backed sorting (go-tangra 032 perf): NOT NULL sort fields order without
-- NULLS LAST, so a plain (tenant_id, col, id) btree serves "col DESC, id DESC"
-- by a backward scan. The 0004 index stored updated_at DESC with id ascending,
-- which matches neither direction of the list's tie-breaker.
DROP INDEX IF EXISTS ticket_tickets_updated;
CREATE INDEX ticket_tickets_updated ON ticket_tickets (tenant_id, updated_at, id);

-- +goose Down
DROP INDEX IF EXISTS ticket_tickets_updated;
CREATE INDEX ticket_tickets_updated ON ticket_tickets (tenant_id, updated_at DESC, id);
