// Package repo defines the storage contract of the ticket (helpdesk) service.
// Every tenant-scoped method runs under the tenant's RLS scope; the only
// cross-tenant reads are RouteMailbox (the narrow inbound routing lookup),
// TenantIDs (platform-admin backup) and audit appends.
package repo

import (
	"context"
	"errors"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/store"
)

// Sentinel errors every implementation maps its failures to.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrNotEmpty = errors.New("not empty")
)

// Tickets is the ticket persistence surface.
type Tickets interface {
	// CreateTicket inserts t; ErrConflict on a duplicate (tenant, external_id).
	CreateTicket(ctx context.Context, t store.Ticket) error
	GetTicket(ctx context.Context, tenantID, id string) (store.Ticket, error)
	// FindTicketByExternalID resolves an inbound root message id.
	FindTicketByExternalID(ctx context.Context, tenantID, externalID string) (store.Ticket, error)
	// ListTickets returns one page (newest first) plus the total match count.
	ListTickets(ctx context.Context, tenantID string, f store.TicketFilter) ([]store.Ticket, int64, error)
	// UpdateTicket applies a partial update of subject/description/priority.
	UpdateTicket(ctx context.Context, tenantID, id string, p store.TicketPatch, at time.Time) (store.Ticket, error)
	// SetAssignee sets (or with "" clears) the assignee and its denormalised name.
	SetAssignee(ctx context.Context, tenantID, id, assigneeID, assigneeName string, at time.Time) (store.Ticket, error)
	// SetStatus sets the status; entering resolved sets resolved_at, any other
	// status clears it.
	SetStatus(ctx context.Context, tenantID, id, status string, at time.Time) (store.Ticket, error)
	// DeleteTicket removes the ticket (comments, links, history and attachment
	// rows cascade) and returns the object keys of its attachments.
	DeleteTicket(ctx context.Context, tenantID, id string) ([]string, error)
}

// Comments is the conversation persistence surface.
type Comments interface {
	// CreateComment inserts c, bumps the ticket's comment_count and updated_at
	// and, for a comment carrying a message id, its last_message_id. ErrConflict
	// on a duplicate (tenant, message_id); ErrNotFound when the ticket is gone.
	CreateComment(ctx context.Context, c store.Comment) error
	GetComment(ctx context.Context, tenantID, id string) (store.Comment, error)
	// ListComments returns a ticket's comments oldest first.
	ListComments(ctx context.Context, tenantID, ticketID string) ([]store.Comment, error)
	// SetCommentDelivery records the outcome of an outbound reply/ack.
	SetCommentDelivery(ctx context.Context, tenantID, id, delivery string) error
	// DeleteComment removes a comment (its attachments cascade), decrements the
	// ticket's comment_count and returns the removed attachment object keys.
	DeleteComment(ctx context.Context, tenantID, id string) ([]string, error)
	// FindCommentByMessageID resolves a recorded message id (dedup + threading).
	FindCommentByMessageID(ctx context.Context, tenantID, messageID string) (store.Comment, error)
	// LastMessageID is the ticket's most recent message id ("" when none).
	LastMessageID(ctx context.Context, tenantID, ticketID string) (string, error)
}

// Attachments is the attachment-metadata surface (bytes live in object storage).
type Attachments interface {
	// CreateAttachment inserts a; ErrConflict on a duplicate storage key,
	// ErrNotFound when the ticket/comment is gone.
	CreateAttachment(ctx context.Context, a store.Attachment) error
	ListAttachments(ctx context.Context, tenantID, ticketID string) ([]store.Attachment, error)
	// GetAttachment returns the attachment only when it belongs to ticketID.
	GetAttachment(ctx context.Context, tenantID, ticketID, id string) (store.Attachment, error)
	// DeleteAttachmentsByTicket removes a ticket's attachment rows and returns
	// their object keys.
	DeleteAttachmentsByTicket(ctx context.Context, tenantID, ticketID string) ([]string, error)
}

// Tags is the tag vocabulary surface.
type Tags interface {
	// CreateTag inserts t; ErrConflict on (tenant, kind, lower(name)).
	CreateTag(ctx context.Context, t store.Tag) error
	GetTag(ctx context.Context, tenantID, id string) (store.Tag, error)
	// ListTags lists the tenant's tags by name ("" kind = every kind).
	ListTags(ctx context.Context, tenantID, kind string) ([]store.Tag, error)
	// UpdateTag writes name/color/description (the kind is immutable).
	UpdateTag(ctx context.Context, t store.Tag) error
	DeleteTag(ctx context.Context, tenantID, id string) error
	// EnsureTagByName returns the tag of that kind and name (case-insensitive),
	// creating it when missing (rules' tag action).
	EnsureTagByName(ctx context.Context, tenantID, kind, name string) (store.Tag, error)
	// SetTicketTags replaces the ticket's tag set; ErrNotFound when the ticket
	// or any tag id does not exist in the tenant (nothing is changed then).
	SetTicketTags(ctx context.Context, tenantID, ticketID string, tagIDs []string) error
	// AddTicketTags adds tags to the ticket's set (idempotent).
	AddTicketTags(ctx context.Context, tenantID, ticketID string, tagIDs []string) error
	// TagsForTickets returns the tags of each ticket (sorted by kind, name).
	TagsForTickets(ctx context.Context, tenantID string, ticketIDs []string) (map[string][]store.Tag, error)
}

// Rules is the triage-rule surface.
type Rules interface {
	CreateRule(ctx context.Context, r store.Rule) error
	GetRule(ctx context.Context, tenantID, id string) (store.Rule, error)
	// ListRules lists every rule by sort order, then name.
	ListRules(ctx context.Context, tenantID string) ([]store.Rule, error)
	// ListEnabledRules lists the enabled rules in evaluation order.
	ListEnabledRules(ctx context.Context, tenantID string) ([]store.Rule, error)
	// UpdateRule writes the rule and bumps its version; the stored rule is returned.
	UpdateRule(ctx context.Context, r store.Rule) (store.Rule, error)
	DeleteRule(ctx context.Context, tenantID, id string) error
}

// Mailboxes is the inbound-routing surface.
type Mailboxes interface {
	// CreateMailbox inserts m; ErrConflict when the address is used by ANY tenant.
	CreateMailbox(ctx context.Context, m store.Mailbox) error
	GetMailbox(ctx context.Context, tenantID, id string) (store.Mailbox, error)
	ListMailboxes(ctx context.Context, tenantID string) ([]store.Mailbox, error)
	UpdateMailbox(ctx context.Context, m store.Mailbox) error
	// DeleteMailbox removes the mailbox; while tickets reference it the delete
	// is refused with ErrNotEmpty unless force (which detaches them).
	DeleteMailbox(ctx context.Context, tenantID, id string, force bool) error
	// RouteMailbox is the SYSTEM-scoped routing lookup of the inbound edge:
	// address (case-insensitive) → tenant + mailbox routing fields only.
	RouteMailbox(ctx context.Context, address string) (store.MailboxRoute, error)
}

// History is the change-history surface.
type History interface {
	AppendHistory(ctx context.Context, h store.History) error
	// ListHistory returns a ticket's history oldest first.
	ListHistory(ctx context.Context, tenantID, ticketID string) ([]store.History, error)
}

// Stats is the dashboard aggregate surface.
type Stats interface {
	// TicketStats aggregates the tenant's tickets; the per-day series cover
	// [since, now] (created from tickets, resolved from history).
	TicketStats(ctx context.Context, tenantID string, since time.Time) (store.Stats, error)
}

// Backup is the export iteration surface (FK order: tags, mailboxes, rules,
// tickets, tag links, comments, history, attachments).
type Backup interface {
	AllTickets(ctx context.Context, tenantID string) ([]store.Ticket, error)
	AllComments(ctx context.Context, tenantID string) ([]store.Comment, error)
	AllAttachments(ctx context.Context, tenantID string) ([]store.Attachment, error)
	AllTagLinks(ctx context.Context, tenantID string) ([]store.TagLink, error)
	AllHistory(ctx context.Context, tenantID string) ([]store.History, error)
	// TenantIDs enumerates tenants holding ticket data (system scope; platform-admin backup).
	TenantIDs(ctx context.Context) ([]string, error)
}

// Store is the full ticket persistence contract.
type Store interface {
	Tickets
	Comments
	Attachments
	Tags
	Rules
	Mailboxes
	History
	Stats
	Backup
	// AppendAudit persists an audit row (system scope).
	AppendAudit(ctx context.Context, row store.AuditRow) error
}
