-- +goose Up
-- Server-side sorting of the ticket queue (feature 032): "recently updated"
-- pages read the index in order, with the id tie-breaker of the list contract.
CREATE INDEX IF NOT EXISTS ticket_tickets_updated ON ticket_tickets (tenant_id, updated_at DESC, id);

-- +goose Down
DROP INDEX IF EXISTS ticket_tickets_updated;
