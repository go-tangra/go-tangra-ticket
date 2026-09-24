// Package store holds the ticket (helpdesk) domain types, the embedded
// migrations and the pgx pool with tenant-scoped transactions.
package store

import "time"

// Ticket statuses (data-model.md).
const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusPending    = "pending"
	StatusResolved   = "resolved"
	StatusClosed     = "closed"
)

// Ticket priorities.
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

// Ticket sources.
const (
	SourceManual = "manual"
	SourceEmail  = "email"
)

// Comment author kinds and delivery states.
const (
	AuthorAgent     = "agent"
	AuthorRequester = "requester"
	AuthorSystem    = "system"

	DeliveryNone   = "none"
	DeliverySent   = "sent"
	DeliveryFailed = "failed"
)

// Tag kinds.
const (
	KindTag      = "tag"
	KindCategory = "category"
)

// Rule match modes and action types.
const (
	MatchAll = "all"
	MatchAny = "any"

	ActionTag      = "tag"
	ActionAssign   = "assign"
	ActionStatus   = "status"
	ActionPriority = "priority"
	ActionDrop     = "drop"
)

// History fields and actor kinds.
const (
	FieldStatus   = "status"
	FieldPriority = "priority"
	FieldAssignee = "assignee"

	ActorAgent   = "agent"
	ActorRule    = "rule"
	ActorInbound = "inbound"
	ActorSystem  = "system"
)

// CreatedByInbound is the created_by value of tickets opened from email.
const CreatedByInbound = "inbound-mail"

// Statuses lists every valid status (ordered as the lifecycle reads).
var Statuses = []string{StatusOpen, StatusInProgress, StatusPending, StatusResolved, StatusClosed}

// Priorities lists every valid priority (ascending).
var Priorities = []string{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent}

// ValidStatus reports whether s is a known status.
func ValidStatus(s string) bool { return contains(Statuses, s) }

// ValidPriority reports whether p is a known priority.
func ValidPriority(p string) bool { return contains(Priorities, p) }

// ValidKind reports whether k is a known tag kind.
func ValidKind(k string) bool { return k == KindTag || k == KindCategory }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Ticket is a support request. Unique (tenant_id, external_id) when
// external_id is set (inbound dedup). BodyHTML is stored as received and only
// ever served sanitised.
type Ticket struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	Subject        string     `json:"subject"`
	Description    string     `json:"description,omitempty"`
	BodyHTML       string     `json:"-"`
	Status         string     `json:"status"`
	Priority       string     `json:"priority"`
	Source         string     `json:"source"`
	RequesterEmail string     `json:"requester_email,omitempty"`
	RequesterName  string     `json:"requester_name,omitempty"`
	Recipient      string     `json:"recipient,omitempty"`
	MailboxID      string     `json:"mailbox_id,omitempty"`
	ExternalID     string     `json:"external_id,omitempty"`
	AssigneeID     string     `json:"assignee_id,omitempty"`
	AssigneeName   string     `json:"assignee_name,omitempty"`
	CommentCount   int        `json:"comment_count"`
	LastMessageID  string     `json:"last_message_id,omitempty"`
	CreatedBy      string     `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
}

// HasHTML reports whether the ticket carries an HTML body.
func (t Ticket) HasHTML() bool { return t.BodyHTML != "" }

// Comment is one timeline entry: an internal note, a public agent reply, a
// requester reply or a system entry (acknowledgement). Unique (tenant_id,
// message_id) when message_id is set.
type Comment struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TicketID    string    `json:"ticket_id"`
	Body        string    `json:"body"`
	Internal    bool      `json:"internal"`
	AuthorKind  string    `json:"author_kind"`
	AuthorID    string    `json:"author_id,omitempty"`
	AuthorName  string    `json:"author_name,omitempty"`
	AuthorEmail string    `json:"author_email,omitempty"`
	MessageID   string    `json:"message_id,omitempty"`
	Delivery    string    `json:"delivery"`
	CreatedAt   time.Time `json:"created_at"`
}

// Attachment is file metadata; bytes live in object storage under StorageKey
// (unique). CommentID is empty for attachments of the ticket's first message.
type Attachment struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TicketID    string    `json:"ticket_id"`
	CommentID   string    `json:"comment_id,omitempty"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	ContentID   string    `json:"content_id,omitempty"`
	Inline      bool      `json:"inline"`
	StorageKey  string    `json:"-"`
	Checksum    string    `json:"checksum,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// AttachmentKey is the object key of an attachment: tenant-prefixed, never
// derived from the file name.
func AttachmentKey(tenantID, ticketID, attachmentID string) string {
	return "tenants/" + tenantID + "/tickets/" + ticketID + "/" + attachmentID
}

// Tag is a label or category. Unique (tenant_id, kind, lower(name)); the kind
// is immutable after create. An empty Color means "automatic" (UI-derived).
type Tag struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Color       string    `json:"color,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// TagLink attaches a tag to a ticket (backup iteration).
type TagLink struct {
	TenantID string `json:"tenant_id"`
	TicketID string `json:"ticket_id"`
	TagID    string `json:"tag_id"`
}

// Condition is one rule condition (field/operator/value).
type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// Action is one rule action.
type Action struct {
	Type       string   `json:"type"`
	TagKind    string   `json:"tag_kind,omitempty"`
	TagNames   []string `json:"tag_names,omitempty"`
	AssigneeID string   `json:"assignee_id,omitempty"`
	Status     string   `json:"status,omitempty"`
	Priority   string   `json:"priority,omitempty"`
}

// Rule is an ordered CEL triage rule. Version bumps on every update (the
// compiled-program cache key).
type Rule struct {
	ID         string      `json:"id"`
	TenantID   string      `json:"tenant_id"`
	Name       string      `json:"name"`
	Enabled    bool        `json:"enabled"`
	SortOrder  int         `json:"sort_order"`
	Match      string      `json:"match"`
	Conditions []Condition `json:"conditions"`
	Expression string      `json:"expression,omitempty"`
	Actions    []Action    `json:"actions"`
	Version    int         `json:"version"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

// Mailbox routes an inbound address to a tenant and carries the reply
// identity and acknowledgement settings. Address is globally unique, lower-case.
type Mailbox struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenant_id"`
	Address         string    `json:"address"`
	DisplayName     string    `json:"display_name"`
	Active          bool      `json:"active"`
	AutoAck         bool      `json:"auto_ack"`
	AutoAckTemplate string    `json:"auto_ack_template,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// MailboxRoute is the narrow result of the system-scoped routing lookup: the
// routed tenant and the mailbox settings the inbound edge needs, nothing else.
type MailboxRoute struct {
	TenantID        string
	MailboxID       string
	DisplayName     string
	Active          bool
	AutoAck         bool
	AutoAckTemplate string
}

// History records a status/priority/assignee change.
type History struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	TicketID  string    `json:"ticket_id"`
	Field     string    `json:"field"`
	OldValue  string    `json:"old_value"`
	NewValue  string    `json:"new_value"`
	ActorKind string    `json:"actor_kind"`
	ActorID   string    `json:"actor_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditRow is an append-only audit event.
type AuditRow struct {
	ID          string
	TenantID    string
	At          time.Time
	ActorKind   string
	ActorID     string
	Action      string
	SubjectKind string
	SubjectID   string
	Outcome     string
	Reason      string
	Detail      map[string]any
}

// AssigneeNone is the filter value selecting unassigned tickets.
const AssigneeNone = "none"

// TicketFilter selects a page of tickets (newest first). AssigneeID "none"
// selects unassigned tickets; Query matches subject/requester (case-insensitive).
type TicketFilter struct {
	Status     string
	Priority   string
	AssigneeID string
	TagID      string
	Query      string
	Page       int // 1-based; <=0 means 1
	PageSize   int // <=0 means 25
}

// Normalized returns the filter with paging defaults applied and the page size
// capped at max.
func (f TicketFilter) Normalized(max int) TicketFilter {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = 25
	}
	if max > 0 && f.PageSize > max {
		f.PageSize = max
	}
	return f
}

// Offset is the row offset of the page.
func (f TicketFilter) Offset() int { return (f.Page - 1) * f.PageSize }

// TicketPatch carries the partial update of a ticket's editable fields; nil
// leaves a field unchanged.
type TicketPatch struct {
	Subject     *string
	Description *string
	Priority    *string
}

// DayCount is one bucket of a per-day series (UTC dates, YYYY-MM-DD).
type DayCount struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

// AssigneeCount is the open-ish ticket count of one assignee.
type AssigneeCount struct {
	AssigneeID   string `json:"assignee_id"`
	AssigneeName string `json:"assignee_name,omitempty"`
	Count        int64  `json:"count"`
}

// Stats is the per-tenant dashboard aggregate.
type Stats struct {
	Total          int64            `json:"total"`
	ByStatus       map[string]int64 `json:"by_status"`
	ByPriority     map[string]int64 `json:"by_priority"`
	ByAssignee     []AssigneeCount  `json:"by_assignee"`
	UnassignedOpen int64            `json:"unassigned_open"`
	CreatedPerDay  []DayCount       `json:"created_per_day"`
	ResolvedPerDay []DayCount       `json:"resolved_per_day"`
}

// Active reports whether status counts as "open work" (not resolved/closed).
func Active(status string) bool { return status != StatusResolved && status != StatusClosed }

// DayFormat is the layout of DayCount.Day.
const DayFormat = "2006-01-02"

// DaySeries returns zero-filled UTC day buckets from since through now
// (bounded to ten years).
func DaySeries(since, now time.Time) []DayCount {
	start := since.UTC().Truncate(24 * time.Hour)
	end := now.UTC().Truncate(24 * time.Hour)
	out := []DayCount{}
	for d := start; !d.After(end) && len(out) <= 3660; d = d.Add(24 * time.Hour) {
		out = append(out, DayCount{Day: d.Format(DayFormat)})
	}
	return out
}

// BumpDay adds n to the bucket of at (ignored outside the series).
func BumpDay(series []DayCount, at time.Time, n ...int64) {
	add := int64(1)
	if len(n) > 0 {
		add = n[0]
	}
	day := at.UTC().Format(DayFormat)
	for i := range series {
		if series[i].Day == day {
			series[i].Count += add
			return
		}
	}
}
