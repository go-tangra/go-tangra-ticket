// Package repodb binds repo.Store to TimescaleDB via *store.Store. Tenant-scoped
// calls run in a tenant transaction (RLS); the audit writer and tenant
// enumeration run under the system-scope pin; RouteMailbox calls the narrow
// SECURITY DEFINER routing function. Unique violations map to
// repo.ErrConflict, foreign-key violations on writes (a missing or cross-tenant
// parent — the schema's composite (tenant_id, id) keys) to repo.ErrNotFound,
// and malformed ids to repo.ErrNotFound.
package repodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// DB implements repo.Store over *store.Store.
type DB struct{ St *store.Store }

var _ repo.Store = (*DB)(nil)

// New wraps the store.
func New(st *store.Store) *DB { return &DB{St: st} }

// Close releases the underlying pool.
func (d *DB) Close() { d.St.Close() }

func (d *DB) tenant(ctx context.Context, tid string, fn func(tx pgx.Tx) error) error {
	return d.St.Tx(ctx, store.Scope{TenantID: tid}, fn)
}

func (d *DB) system(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return d.St.Tx(ctx, store.Scope{System: true}, fn)
}

type scanner interface{ Scan(dest ...any) error }

// mapErr maps store errors: no row / malformed id → ErrNotFound, unique
// violation → ErrConflict, foreign-key violation on delete → ErrNotEmpty.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return repo.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return repo.ErrConflict
		case "23503":
			return repo.ErrNotEmpty
		case "22P02": // invalid_text_representation (a non-uuid id)
			return repo.ErrNotFound
		}
	}
	return err
}

// mapWriteErr is mapErr for inserts/updates: a foreign-key violation means the
// referenced parent is missing or belongs to another tenant.
func mapWriteErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		return repo.ErrNotFound
	}
	return mapErr(err)
}

func affected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return repo.ErrNotFound
	}
	return nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRE.MatchString(s) }

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func now() time.Time { return time.Now().UTC() }

func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return now()
	}
	return t
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// likePattern escapes LIKE metacharacters so user input matches literally.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.ToLower(q)) + "%"
}

// ---------------------------------------------------------------- tickets

const ticketCols = `id, tenant_id, subject, description, body_html, status, priority, source,
 requester_email, requester_name, recipient, mailbox_id, external_id, assignee_id, assignee_name,
 comment_count, last_message_id, created_by, created_at, updated_at, resolved_at`

func scanTicket(sc scanner) (store.Ticket, error) {
	var t store.Ticket
	var mailbox, ext, assignee *string
	err := sc.Scan(&t.ID, &t.TenantID, &t.Subject, &t.Description, &t.BodyHTML, &t.Status, &t.Priority, &t.Source,
		&t.RequesterEmail, &t.RequesterName, &t.Recipient, &mailbox, &ext, &assignee, &t.AssigneeName,
		&t.CommentCount, &t.LastMessageID, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt)
	t.MailboxID, t.ExternalID, t.AssigneeID = deref(mailbox), deref(ext), deref(assignee)
	return t, err
}

// CreateTicket implements repo.Tickets.
func (d *DB) CreateTicket(ctx context.Context, t store.Ticket) error {
	created := orNow(t.CreatedAt)
	updated := t.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	if t.Status == "" {
		t.Status = store.StatusOpen
	}
	if t.Priority == "" {
		t.Priority = store.PriorityNormal
	}
	if t.Source == "" {
		t.Source = store.SourceManual
	}
	return d.tenant(ctx, t.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ticket_tickets (`+ticketCols+`)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
			t.ID, t.TenantID, t.Subject, t.Description, t.BodyHTML, t.Status, t.Priority, t.Source,
			t.RequesterEmail, t.RequesterName, t.Recipient, nullStr(t.MailboxID), nullStr(t.ExternalID), nullStr(t.AssigneeID), t.AssigneeName,
			t.CommentCount, t.LastMessageID, t.CreatedBy, created, updated, t.ResolvedAt)
		return mapWriteErr(err)
	})
}

func (d *DB) ticketWhere(ctx context.Context, tenantID, where string, arg any) (out store.Ticket, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = scanTicket(tx.QueryRow(ctx, `SELECT `+ticketCols+` FROM ticket_tickets WHERE `+where, arg))
		return mapErr(err)
	})
	return out, err
}

// GetTicket implements repo.Tickets.
func (d *DB) GetTicket(ctx context.Context, tenantID, id string) (store.Ticket, error) {
	return d.ticketWhere(ctx, tenantID, "id = $1::uuid", id)
}

// FindTicketByExternalID implements repo.Tickets.
func (d *DB) FindTicketByExternalID(ctx context.Context, tenantID, externalID string) (store.Ticket, error) {
	if externalID == "" {
		return store.Ticket{}, repo.ErrNotFound
	}
	return d.ticketWhere(ctx, tenantID, "external_id = $1", externalID)
}

// ListTickets implements repo.Tickets.
func (d *DB) ListTickets(ctx context.Context, tenantID string, f store.TicketFilter) (items []store.Ticket, total int64, err error) {
	f = f.Normalized(100)
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Priority != "" {
		add("priority = $%d", f.Priority)
	}
	switch f.AssigneeID {
	case "":
	case store.AssigneeNone:
		where = append(where, "assignee_id IS NULL")
	default:
		add("assignee_id = $%d", f.AssigneeID)
	}
	if f.TagID != "" {
		if !isUUID(f.TagID) {
			return []store.Ticket{}, 0, nil
		}
		add("EXISTS (SELECT 1 FROM ticket_tag_links l WHERE l.ticket_id = ticket_tickets.id AND l.tag_id = $%d::uuid)", f.TagID)
	}
	if f.Query != "" {
		args = append(args, likePattern(f.Query))
		n := len(args)
		where = append(where, fmt.Sprintf(`(lower(subject) LIKE $%[1]d ESCAPE '\' OR lower(requester_email) LIKE $%[1]d ESCAPE '\' OR lower(requester_name) LIKE $%[1]d ESCAPE '\')`, n))
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_tickets`+cond, args...).Scan(&total); err != nil {
			return err
		}
		q := fmt.Sprintf(`SELECT %s FROM ticket_tickets%s ORDER BY created_at DESC, id DESC LIMIT %d OFFSET %d`, ticketCols, cond, f.PageSize, f.Offset())
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		items = []store.Ticket{}
		for rows.Next() {
			t, err := scanTicket(rows)
			if err != nil {
				return err
			}
			items = append(items, t)
		}
		return rows.Err()
	})
	return items, total, mapErr(err)
}

func (d *DB) updateTicket(ctx context.Context, tenantID, id, set string, args ...any) (out store.Ticket, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		all := append([]any{id}, args...)
		out, err = scanTicket(tx.QueryRow(ctx, `UPDATE ticket_tickets SET `+set+` WHERE id = $1::uuid RETURNING `+ticketCols, all...))
		return mapWriteErr(err)
	})
	return out, err
}

// UpdateTicket implements repo.Tickets.
func (d *DB) UpdateTicket(ctx context.Context, tenantID, id string, p store.TicketPatch, at time.Time) (store.Ticket, error) {
	return d.updateTicket(ctx, tenantID, id,
		`subject = COALESCE($2, subject), description = COALESCE($3, description), priority = COALESCE($4, priority), updated_at = $5`,
		p.Subject, p.Description, p.Priority, orNow(at))
}

// SetAssignee implements repo.Tickets.
func (d *DB) SetAssignee(ctx context.Context, tenantID, id, assigneeID, assigneeName string, at time.Time) (store.Ticket, error) {
	if assigneeID == "" {
		assigneeName = ""
	}
	return d.updateTicket(ctx, tenantID, id, `assignee_id = $2, assignee_name = $3, updated_at = $4`,
		nullStr(assigneeID), assigneeName, orNow(at))
}

// SetStatus implements repo.Tickets.
func (d *DB) SetStatus(ctx context.Context, tenantID, id, status string, at time.Time) (store.Ticket, error) {
	return d.updateTicket(ctx, tenantID, id, `resolved_at = CASE WHEN $2 = 'resolved'
   THEN COALESCE(CASE WHEN status = 'resolved' THEN resolved_at END, $3) ELSE NULL END,
 status = $2, updated_at = $3`, status, orNow(at))
}

func collectKeys(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// DeleteTicket implements repo.Tickets.
func (d *DB) DeleteTicket(ctx context.Context, tenantID, id string) (keys []string, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		if keys, err = collectKeys(ctx, tx, `SELECT storage_key FROM ticket_attachments WHERE ticket_id = $1::uuid ORDER BY storage_key`, id); err != nil {
			return mapErr(err)
		}
		return affected(tx.Exec(ctx, `DELETE FROM ticket_tickets WHERE id = $1::uuid`, id))
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// --------------------------------------------------------------- comments

const commentCols = `id, tenant_id, ticket_id, body, internal, author_kind, author_id, author_name, author_email, message_id, delivery, created_at`

func scanComment(sc scanner) (store.Comment, error) {
	var c store.Comment
	var author, msg *string
	err := sc.Scan(&c.ID, &c.TenantID, &c.TicketID, &c.Body, &c.Internal, &c.AuthorKind, &author, &c.AuthorName, &c.AuthorEmail, &msg, &c.Delivery, &c.CreatedAt)
	c.AuthorID, c.MessageID = deref(author), deref(msg)
	return c, err
}

// CreateComment implements repo.Comments.
func (d *DB) CreateComment(ctx context.Context, c store.Comment) error {
	at := orNow(c.CreatedAt)
	if c.Delivery == "" {
		c.Delivery = store.DeliveryNone
	}
	return d.tenant(ctx, c.TenantID, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `UPDATE ticket_tickets SET comment_count = comment_count + 1, updated_at = $2,
 last_message_id = CASE WHEN $3 <> '' AND NOT $4 THEN $3 ELSE last_message_id END WHERE id = $1::uuid`,
			c.TicketID, at, c.MessageID, c.Internal)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO ticket_comments (`+commentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			c.ID, c.TenantID, c.TicketID, c.Body, c.Internal, c.AuthorKind, nullStr(c.AuthorID), c.AuthorName, c.AuthorEmail, nullStr(c.MessageID), c.Delivery, at)
		return mapWriteErr(err)
	})
}

func (d *DB) commentWhere(ctx context.Context, tenantID, where string, arg any) (out store.Comment, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = scanComment(tx.QueryRow(ctx, `SELECT `+commentCols+` FROM ticket_comments WHERE `+where, arg))
		return mapErr(err)
	})
	return out, err
}

// GetComment implements repo.Comments.
func (d *DB) GetComment(ctx context.Context, tenantID, id string) (store.Comment, error) {
	return d.commentWhere(ctx, tenantID, "id = $1::uuid", id)
}

// FindCommentByMessageID implements repo.Comments.
func (d *DB) FindCommentByMessageID(ctx context.Context, tenantID, messageID string) (store.Comment, error) {
	if messageID == "" {
		return store.Comment{}, repo.ErrNotFound
	}
	return d.commentWhere(ctx, tenantID, "message_id = $1", messageID)
}

func listRows[T any](ctx context.Context, tx pgx.Tx, scan func(scanner) (T, error), q string, args ...any) ([]T, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListComments implements repo.Comments.
func (d *DB) ListComments(ctx context.Context, tenantID, ticketID string) (out []store.Comment, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanComment, `SELECT `+commentCols+` FROM ticket_comments WHERE ticket_id = $1::uuid ORDER BY created_at, id`, ticketID)
		return mapErr(err)
	})
	return out, err
}

// SetCommentDelivery implements repo.Comments.
func (d *DB) SetCommentDelivery(ctx context.Context, tenantID, id, delivery string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		return affected(tx.Exec(ctx, `UPDATE ticket_comments SET delivery = $2 WHERE id = $1::uuid`, id, delivery))
	})
}

// DeleteComment implements repo.Comments.
func (d *DB) DeleteComment(ctx context.Context, tenantID, id string) (keys []string, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var ticketID string
		if err := tx.QueryRow(ctx, `SELECT ticket_id FROM ticket_comments WHERE id = $1::uuid`, id).Scan(&ticketID); err != nil {
			return mapErr(err)
		}
		if keys, err = collectKeys(ctx, tx, `SELECT storage_key FROM ticket_attachments WHERE comment_id = $1::uuid ORDER BY storage_key`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ticket_comments WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE ticket_tickets SET comment_count = GREATEST(comment_count - 1, 0) WHERE id = $1`, ticketID)
		return err
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return keys, nil
}

// LastMessageID implements repo.Comments.
func (d *DB) LastMessageID(ctx context.Context, tenantID, ticketID string) (id string, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		return mapErr(tx.QueryRow(ctx, `SELECT last_message_id FROM ticket_tickets WHERE id = $1::uuid`, ticketID).Scan(&id))
	})
	return id, err
}

// ------------------------------------------------------------ attachments

const attachmentCols = `id, tenant_id, ticket_id, comment_id, filename, content_type, size, content_id, inline, storage_key, checksum, created_at`

func scanAttachment(sc scanner) (store.Attachment, error) {
	var a store.Attachment
	var comment *string
	err := sc.Scan(&a.ID, &a.TenantID, &a.TicketID, &comment, &a.Filename, &a.ContentType, &a.Size, &a.ContentID, &a.Inline, &a.StorageKey, &a.Checksum, &a.CreatedAt)
	a.CommentID = deref(comment)
	return a, err
}

// CreateAttachment implements repo.Attachments.
func (d *DB) CreateAttachment(ctx context.Context, a store.Attachment) error {
	if a.ContentType == "" {
		a.ContentType = "application/octet-stream"
	}
	return d.tenant(ctx, a.TenantID, func(tx pgx.Tx) error {
		if a.CommentID != "" {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_comments WHERE id = $1::uuid AND ticket_id = $2::uuid`, a.CommentID, a.TicketID).Scan(&n); err != nil {
				return mapErr(err)
			}
			if n == 0 {
				return repo.ErrNotFound
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO ticket_attachments (`+attachmentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			a.ID, a.TenantID, a.TicketID, nullStr(a.CommentID), a.Filename, a.ContentType, a.Size, a.ContentID, a.Inline, a.StorageKey, a.Checksum, orNow(a.CreatedAt))
		return mapWriteErr(err)
	})
}

// ListAttachments implements repo.Attachments.
func (d *DB) ListAttachments(ctx context.Context, tenantID, ticketID string) (out []store.Attachment, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanAttachment, `SELECT `+attachmentCols+` FROM ticket_attachments WHERE ticket_id = $1::uuid ORDER BY created_at, id`, ticketID)
		return mapErr(err)
	})
	return out, err
}

// GetAttachment implements repo.Attachments.
func (d *DB) GetAttachment(ctx context.Context, tenantID, ticketID, id string) (out store.Attachment, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = scanAttachment(tx.QueryRow(ctx, `SELECT `+attachmentCols+` FROM ticket_attachments WHERE id = $1::uuid AND ticket_id = $2::uuid`, id, ticketID))
		return mapErr(err)
	})
	return out, err
}

// DeleteAttachmentsByTicket implements repo.Attachments.
func (d *DB) DeleteAttachmentsByTicket(ctx context.Context, tenantID, ticketID string) (keys []string, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		keys, err = collectKeys(ctx, tx, `DELETE FROM ticket_attachments WHERE ticket_id = $1::uuid RETURNING storage_key`, ticketID)
		return mapErr(err)
	})
	return keys, err
}

// ------------------------------------------------------------------- tags

const tagCols = `id, tenant_id, name, kind, color, description, created_at`

func scanTag(sc scanner) (store.Tag, error) {
	var t store.Tag
	var color *string
	err := sc.Scan(&t.ID, &t.TenantID, &t.Name, &t.Kind, &color, &t.Description, &t.CreatedAt)
	t.Color = deref(color)
	return t, err
}

func insertTag(ctx context.Context, tx pgx.Tx, t store.Tag) error {
	if t.Kind == "" {
		t.Kind = store.KindTag
	}
	_, err := tx.Exec(ctx, `INSERT INTO ticket_tags (`+tagCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t.ID, t.TenantID, t.Name, t.Kind, nullStr(t.Color), t.Description, orNow(t.CreatedAt))
	return mapWriteErr(err)
}

// CreateTag implements repo.Tags.
func (d *DB) CreateTag(ctx context.Context, t store.Tag) error {
	return d.tenant(ctx, t.TenantID, func(tx pgx.Tx) error { return insertTag(ctx, tx, t) })
}

// GetTag implements repo.Tags.
func (d *DB) GetTag(ctx context.Context, tenantID, id string) (out store.Tag, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = scanTag(tx.QueryRow(ctx, `SELECT `+tagCols+` FROM ticket_tags WHERE id = $1::uuid`, id))
		return mapErr(err)
	})
	return out, err
}

// ListTags implements repo.Tags.
func (d *DB) ListTags(ctx context.Context, tenantID, kind string) (out []store.Tag, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanTag, `SELECT `+tagCols+` FROM ticket_tags WHERE ($1 = '' OR kind = $1) ORDER BY kind, lower(name), id`, kind)
		return mapErr(err)
	})
	return out, err
}

// UpdateTag implements repo.Tags (the kind is never written).
func (d *DB) UpdateTag(ctx context.Context, t store.Tag) error {
	return d.tenant(ctx, t.TenantID, func(tx pgx.Tx) error {
		return affected(tx.Exec(ctx, `UPDATE ticket_tags SET name = $2, color = $3, description = $4 WHERE id = $1::uuid`,
			t.ID, t.Name, nullStr(t.Color), t.Description))
	})
}

// DeleteTag implements repo.Tags (links cascade).
func (d *DB) DeleteTag(ctx context.Context, tenantID, id string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		return affected(tx.Exec(ctx, `DELETE FROM ticket_tags WHERE id = $1::uuid`, id))
	})
}

// EnsureTagByName implements repo.Tags.
func (d *DB) EnsureTagByName(ctx context.Context, tenantID, kind, name string) (out store.Tag, err error) {
	if kind == "" {
		kind = store.KindTag
	}
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_tags (id, tenant_id, name, kind, created_at) VALUES ($1,$2,$3,$4,$5)
 ON CONFLICT (tenant_id, kind, lower(name)) DO NOTHING`, store.NewID(), tenantID, name, kind, now()); err != nil {
			return mapWriteErr(err)
		}
		out, err = scanTag(tx.QueryRow(ctx, `SELECT `+tagCols+` FROM ticket_tags WHERE kind = $1 AND lower(name) = lower($2)`, kind, name))
		return mapErr(err)
	})
	return out, err
}

func uniq(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// checkTicketAndTags verifies the ticket and every tag exist in the tenant.
func checkTicketAndTags(ctx context.Context, tx pgx.Tx, ticketID string, tagIDs []string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_tickets WHERE id = $1::uuid`, ticketID).Scan(&n); err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return repo.ErrNotFound
	}
	if len(tagIDs) == 0 {
		return nil
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_tags WHERE id = ANY($1::uuid[])`, tagIDs).Scan(&n); err != nil {
		return mapErr(err)
	}
	if n != len(tagIDs) {
		return repo.ErrNotFound
	}
	return nil
}

func (d *DB) linkTags(ctx context.Context, tenantID, ticketID string, tagIDs []string, replace bool) error {
	tagIDs = uniq(tagIDs)
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		if err := checkTicketAndTags(ctx, tx, ticketID, tagIDs); err != nil {
			return err
		}
		if replace {
			if _, err := tx.Exec(ctx, `DELETE FROM ticket_tag_links WHERE ticket_id = $1::uuid`, ticketID); err != nil {
				return err
			}
		}
		if len(tagIDs) == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO ticket_tag_links (tenant_id, ticket_id, tag_id)
 SELECT $1, $2::uuid, t::uuid FROM unnest($3::text[]) AS t ON CONFLICT DO NOTHING`, tenantID, ticketID, tagIDs)
		return mapWriteErr(err)
	})
}

// SetTicketTags implements repo.Tags.
func (d *DB) SetTicketTags(ctx context.Context, tenantID, ticketID string, tagIDs []string) error {
	return d.linkTags(ctx, tenantID, ticketID, tagIDs, true)
}

// AddTicketTags implements repo.Tags.
func (d *DB) AddTicketTags(ctx context.Context, tenantID, ticketID string, tagIDs []string) error {
	return d.linkTags(ctx, tenantID, ticketID, tagIDs, false)
}

// TagsForTickets implements repo.Tags.
func (d *DB) TagsForTickets(ctx context.Context, tenantID string, ticketIDs []string) (map[string][]store.Tag, error) {
	out := map[string][]store.Tag{}
	valid := ticketIDs[:0:0]
	for _, id := range ticketIDs {
		if isUUID(id) {
			valid = append(valid, id)
		}
	}
	ticketIDs = valid
	if len(ticketIDs) == 0 {
		return out, nil
	}
	err := d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT l.ticket_id, t.id, t.tenant_id, t.name, t.kind, t.color, t.description, t.created_at
 FROM ticket_tag_links l JOIN ticket_tags t ON t.id = l.tag_id
 WHERE l.ticket_id = ANY($1::uuid[]) ORDER BY t.kind, lower(t.name), t.id`, ticketIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var tid string
			var t store.Tag
			var color *string
			if err := rows.Scan(&tid, &t.ID, &t.TenantID, &t.Name, &t.Kind, &color, &t.Description, &t.CreatedAt); err != nil {
				return err
			}
			t.Color = deref(color)
			out[tid] = append(out[tid], t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// ------------------------------------------------------------------ rules

const ruleCols = `id, tenant_id, name, enabled, sort_order, match, conditions, expression, actions, version, created_at, updated_at`

func scanRule(sc scanner) (store.Rule, error) {
	var r store.Rule
	var conds, acts []byte
	if err := sc.Scan(&r.ID, &r.TenantID, &r.Name, &r.Enabled, &r.SortOrder, &r.Match, &conds, &r.Expression, &acts, &r.Version, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return r, err
	}
	if err := json.Unmarshal(conds, &r.Conditions); err != nil {
		return r, fmt.Errorf("repodb: rule conditions: %w", err)
	}
	if err := json.Unmarshal(acts, &r.Actions); err != nil {
		return r, fmt.Errorf("repodb: rule actions: %w", err)
	}
	if r.Conditions == nil {
		r.Conditions = []store.Condition{}
	}
	if r.Actions == nil {
		r.Actions = []store.Action{}
	}
	return r, nil
}

func ruleJSON(r store.Rule) (conds, acts []byte) {
	if r.Conditions == nil {
		r.Conditions = []store.Condition{}
	}
	if r.Actions == nil {
		r.Actions = []store.Action{}
	}
	return mustJSON(r.Conditions), mustJSON(r.Actions)
}

// CreateRule implements repo.Rules.
func (d *DB) CreateRule(ctx context.Context, r store.Rule) error {
	if r.Match == "" {
		r.Match = store.MatchAll
	}
	if r.Version <= 0 {
		r.Version = 1
	}
	created := orNow(r.CreatedAt)
	updated := r.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	conds, acts := ruleJSON(r)
	return d.tenant(ctx, r.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ticket_rules (`+ruleCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			r.ID, r.TenantID, r.Name, r.Enabled, r.SortOrder, r.Match, conds, r.Expression, acts, r.Version, created, updated)
		return mapWriteErr(err)
	})
}

// GetRule implements repo.Rules.
func (d *DB) GetRule(ctx context.Context, tenantID, id string) (out store.Rule, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = scanRule(tx.QueryRow(ctx, `SELECT `+ruleCols+` FROM ticket_rules WHERE id = $1::uuid`, id))
		return mapErr(err)
	})
	return out, err
}

func (d *DB) listRules(ctx context.Context, tenantID, where string) (out []store.Rule, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanRule, `SELECT `+ruleCols+` FROM ticket_rules`+where+` ORDER BY sort_order, name, id`)
		return mapErr(err)
	})
	return out, err
}

// ListRules implements repo.Rules.
func (d *DB) ListRules(ctx context.Context, tenantID string) ([]store.Rule, error) {
	return d.listRules(ctx, tenantID, "")
}

// ListEnabledRules implements repo.Rules.
func (d *DB) ListEnabledRules(ctx context.Context, tenantID string) ([]store.Rule, error) {
	return d.listRules(ctx, tenantID, " WHERE enabled")
}

// UpdateRule implements repo.Rules (bumps the version).
func (d *DB) UpdateRule(ctx context.Context, r store.Rule) (out store.Rule, err error) {
	if r.Match == "" {
		r.Match = store.MatchAll
	}
	conds, acts := ruleJSON(r)
	err = d.tenant(ctx, r.TenantID, func(tx pgx.Tx) error {
		out, err = scanRule(tx.QueryRow(ctx, `UPDATE ticket_rules SET name = $2, enabled = $3, sort_order = $4, match = $5,
 conditions = $6, expression = $7, actions = $8, version = version + 1, updated_at = $9 WHERE id = $1::uuid RETURNING `+ruleCols,
			r.ID, r.Name, r.Enabled, r.SortOrder, r.Match, conds, r.Expression, acts, now()))
		return mapWriteErr(err)
	})
	return out, err
}

// DeleteRule implements repo.Rules.
func (d *DB) DeleteRule(ctx context.Context, tenantID, id string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		return affected(tx.Exec(ctx, `DELETE FROM ticket_rules WHERE id = $1::uuid`, id))
	})
}

// -------------------------------------------------------------- mailboxes

const mailboxCols = `id, tenant_id, address, display_name, active, auto_ack, auto_ack_template, created_at, updated_at`

func scanMailbox(sc scanner) (store.Mailbox, error) {
	var m store.Mailbox
	err := sc.Scan(&m.ID, &m.TenantID, &m.Address, &m.DisplayName, &m.Active, &m.AutoAck, &m.AutoAckTemplate, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func normAddr(a string) string { return strings.ToLower(strings.TrimSpace(a)) }

// CreateMailbox implements repo.Mailboxes (the address unique index is global,
// so an address held by another tenant conflicts even though RLS hides it).
func (d *DB) CreateMailbox(ctx context.Context, m store.Mailbox) error {
	created := orNow(m.CreatedAt)
	updated := m.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	return d.tenant(ctx, m.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ticket_mailboxes (`+mailboxCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			m.ID, m.TenantID, normAddr(m.Address), m.DisplayName, m.Active, m.AutoAck, m.AutoAckTemplate, created, updated)
		return mapWriteErr(err)
	})
}

// GetMailbox implements repo.Mailboxes.
func (d *DB) GetMailbox(ctx context.Context, tenantID, id string) (out store.Mailbox, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = scanMailbox(tx.QueryRow(ctx, `SELECT `+mailboxCols+` FROM ticket_mailboxes WHERE id = $1::uuid`, id))
		return mapErr(err)
	})
	return out, err
}

// ListMailboxes implements repo.Mailboxes.
func (d *DB) ListMailboxes(ctx context.Context, tenantID string) (out []store.Mailbox, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanMailbox, `SELECT `+mailboxCols+` FROM ticket_mailboxes ORDER BY address`)
		return mapErr(err)
	})
	return out, err
}

// UpdateMailbox implements repo.Mailboxes.
func (d *DB) UpdateMailbox(ctx context.Context, m store.Mailbox) error {
	return d.tenant(ctx, m.TenantID, func(tx pgx.Tx) error {
		return affected(tx.Exec(ctx, `UPDATE ticket_mailboxes SET address = $2, display_name = $3, active = $4, auto_ack = $5,
 auto_ack_template = $6, updated_at = $7 WHERE id = $1::uuid`,
			m.ID, normAddr(m.Address), m.DisplayName, m.Active, m.AutoAck, m.AutoAckTemplate, now()))
	})
}

// DeleteMailbox implements repo.Mailboxes.
func (d *DB) DeleteMailbox(ctx context.Context, tenantID, id string, force bool) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var exists, refs int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_mailboxes WHERE id = $1::uuid`, id).Scan(&exists); err != nil {
			return mapErr(err)
		}
		if exists == 0 {
			return repo.ErrNotFound
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_tickets WHERE mailbox_id = $1::uuid`, id).Scan(&refs); err != nil {
			return err
		}
		if refs > 0 && !force {
			return repo.ErrNotEmpty
		}
		// ON DELETE SET NULL (mailbox_id) detaches the referencing tickets.
		return affected(tx.Exec(ctx, `DELETE FROM ticket_mailboxes WHERE id = $1::uuid`, id))
	})
}

// RouteMailbox implements repo.Mailboxes through ticket_route_mailbox(), which
// returns routing fields only (no RLS scope is set by the caller).
func (d *DB) RouteMailbox(ctx context.Context, address string) (out store.MailboxRoute, err error) {
	a := normAddr(address)
	if a == "" {
		return out, repo.ErrNotFound
	}
	err = d.St.Tx(ctx, store.Scope{}, func(tx pgx.Tx) error {
		return mapErr(tx.QueryRow(ctx, `SELECT tenant_id, mailbox_id, display_name, active, auto_ack, auto_ack_template FROM ticket_route_mailbox($1)`, a).
			Scan(&out.TenantID, &out.MailboxID, &out.DisplayName, &out.Active, &out.AutoAck, &out.AutoAckTemplate))
	})
	return out, err
}

// ---------------------------------------------------------------- history

const historyCols = `id, tenant_id, ticket_id, field, old_value, new_value, actor_kind, actor_id, created_at`

func scanHistory(sc scanner) (store.History, error) {
	var h store.History
	err := sc.Scan(&h.ID, &h.TenantID, &h.TicketID, &h.Field, &h.OldValue, &h.NewValue, &h.ActorKind, &h.ActorID, &h.CreatedAt)
	return h, err
}

// AppendHistory implements repo.History.
func (d *DB) AppendHistory(ctx context.Context, h store.History) error {
	return d.tenant(ctx, h.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ticket_history (`+historyCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			h.ID, h.TenantID, h.TicketID, h.Field, h.OldValue, h.NewValue, h.ActorKind, h.ActorID, orNow(h.CreatedAt))
		return mapWriteErr(err)
	})
}

// ListHistory implements repo.History.
func (d *DB) ListHistory(ctx context.Context, tenantID, ticketID string) (out []store.History, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanHistory, `SELECT `+historyCols+` FROM ticket_history WHERE ticket_id = $1::uuid ORDER BY created_at, id`, ticketID)
		return mapErr(err)
	})
	return out, err
}

// ------------------------------------------------------------------ stats

func groupCounts(ctx context.Context, tx pgx.Tx, q string, into map[string]int64, args ...any) error {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return err
		}
		into[k] = n
	}
	return rows.Err()
}

// TicketStats implements repo.Stats.
func (d *DB) TicketStats(ctx context.Context, tenantID string, since time.Time) (store.Stats, error) {
	at := now()
	st := store.Stats{ByStatus: map[string]int64{}, ByPriority: map[string]int64{}, ByAssignee: []store.AssigneeCount{},
		CreatedPerDay: store.DaySeries(since, at), ResolvedPerDay: store.DaySeries(since, at)}
	for _, s := range store.Statuses {
		st.ByStatus[s] = 0
	}
	for _, p := range store.Priorities {
		st.ByPriority[p] = 0
	}
	err := d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		if err := groupCounts(ctx, tx, `SELECT status, count(*) FROM ticket_tickets GROUP BY status`, st.ByStatus); err != nil {
			return err
		}
		if err := groupCounts(ctx, tx, `SELECT priority, count(*) FROM ticket_tickets GROUP BY priority`, st.ByPriority); err != nil {
			return err
		}
		for _, n := range st.ByStatus {
			st.Total += n
		}
		const active = `status NOT IN ('resolved','closed')`
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ticket_tickets WHERE assignee_id IS NULL AND `+active).Scan(&st.UnassignedOpen); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT assignee_id, max(assignee_name), count(*) FROM ticket_tickets
 WHERE assignee_id IS NOT NULL AND `+active+` GROUP BY assignee_id ORDER BY count(*) DESC, assignee_id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ac store.AssigneeCount
			if err := rows.Scan(&ac.AssigneeID, &ac.AssigneeName, &ac.Count); err != nil {
				rows.Close()
				return err
			}
			st.ByAssignee = append(st.ByAssignee, ac)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		days := map[string]int64{}
		if err := groupCounts(ctx, tx, `SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), count(*) FROM ticket_tickets
 WHERE created_at >= $1 GROUP BY 1`, days, since); err != nil {
			return err
		}
		fill(st.CreatedPerDay, days)
		days = map[string]int64{}
		if err := groupCounts(ctx, tx, `SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), count(*) FROM ticket_history
 WHERE field = 'status' AND new_value = 'resolved' AND created_at >= $1 GROUP BY 1`, days, since); err != nil {
			return err
		}
		fill(st.ResolvedPerDay, days)
		return nil
	})
	return st, mapErr(err)
}

func fill(series []store.DayCount, days map[string]int64) {
	for i := range series {
		series[i].Count = days[series[i].Day]
	}
}

// ----------------------------------------------------------------- backup

// AllTickets implements repo.Backup.
func (d *DB) AllTickets(ctx context.Context, tenantID string) (out []store.Ticket, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanTicket, `SELECT `+ticketCols+` FROM ticket_tickets ORDER BY id`)
		return mapErr(err)
	})
	return out, err
}

// AllComments implements repo.Backup.
func (d *DB) AllComments(ctx context.Context, tenantID string) (out []store.Comment, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanComment, `SELECT `+commentCols+` FROM ticket_comments ORDER BY created_at, id`)
		return mapErr(err)
	})
	return out, err
}

// AllAttachments implements repo.Backup.
func (d *DB) AllAttachments(ctx context.Context, tenantID string) (out []store.Attachment, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanAttachment, `SELECT `+attachmentCols+` FROM ticket_attachments ORDER BY id`)
		return mapErr(err)
	})
	return out, err
}

// AllTagLinks implements repo.Backup.
func (d *DB) AllTagLinks(ctx context.Context, tenantID string) (out []store.TagLink, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, func(sc scanner) (store.TagLink, error) {
			var l store.TagLink
			return l, sc.Scan(&l.TenantID, &l.TicketID, &l.TagID)
		}, `SELECT tenant_id, ticket_id, tag_id FROM ticket_tag_links ORDER BY ticket_id, tag_id`)
		return mapErr(err)
	})
	return out, err
}

// AllHistory implements repo.Backup.
func (d *DB) AllHistory(ctx context.Context, tenantID string) (out []store.History, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, scanHistory, `SELECT `+historyCols+` FROM ticket_history ORDER BY created_at, id`)
		return mapErr(err)
	})
	return out, err
}

// TenantIDs implements repo.Backup (system scope).
func (d *DB) TenantIDs(ctx context.Context) (out []string, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		out, err = listRows(ctx, tx, func(sc scanner) (string, error) {
			var s string
			return s, sc.Scan(&s)
		}, `SELECT tenant_id::text FROM ticket_tickets UNION SELECT tenant_id::text FROM ticket_tags
 UNION SELECT tenant_id::text FROM ticket_rules UNION SELECT tenant_id::text FROM ticket_mailboxes ORDER BY 1`)
		return mapErr(err)
	})
	return out, err
}

// ------------------------------------------------------------------ audit

// AppendAudit implements repo.Store (system scope: refusals of the inbound
// edge are recorded under the nil tenant).
func (d *DB) AppendAudit(ctx context.Context, row store.AuditRow) error {
	var detail []byte
	if row.Detail != nil {
		detail = mustJSON(row.Detail)
	}
	return d.system(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ticket_audit_events (id, tenant_id, at, actor_kind, actor_id, action, subject_kind, subject_id, outcome, reason, detail)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			row.ID, row.TenantID, orNow(row.At), row.ActorKind, row.ActorID, row.Action, row.SubjectKind, row.SubjectID, row.Outcome, row.Reason, detail)
		return mapErr(err)
	})
}
