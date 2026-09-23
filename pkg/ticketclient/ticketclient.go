// Package ticketclient is a thin, typed Go client for the ticket.v1
// service-to-service gRPC API (contracts §C), for other Freya modules that need
// to open helpdesk tickets (monitoring or asset alerts), read them and add
// comments. It wraps the generated gRPC stubs so callers deal in ordinary Go
// values. The caller supplies a connected, SPIFFE-mTLS gRPC connection (e.g.
// from freya.App.Client(ctx, "ticket")); this package does not dial or manage
// the connection. No response carries a raw HTML body, attachment bytes or
// relay/SMTP credentials.
package ticketclient

import (
	"context"
	"time"

	"google.golang.org/grpc"

	ticketv1 "github.com/go-freya/freya/services/ticket/api/proto/ticket/v1"
)

// Client calls the ticket.v1 API over a caller-provided gRPC connection.
type Client struct {
	tickets ticketv1.TicketsClient
}

// New builds a client from a connected (SPIFFE-mTLS) gRPC connection to the
// ticket service.
func New(conn grpc.ClientConnInterface) *Client {
	return &Client{tickets: ticketv1.NewTicketsClient(conn)}
}

// Ticket is a helpdesk ticket as the module reports it.
type Ticket struct {
	ID, Subject, Description         string
	Status, Priority, Source         string
	RequesterEmail, RequesterName    string
	AssigneeID, AssigneeName         string
	CommentCount                     int
	TagIDs                           []string
	CreatedAt, UpdatedAt, ResolvedAt time.Time // zero = unset
}

// Comment is one conversation entry.
type Comment struct {
	ID, TicketID, Body               string
	Internal                         bool
	AuthorKind, AuthorID, AuthorName string
	Delivery                         string
	CreatedAt                        time.Time
}

// NewTicket is the Create body. SourceModule is informational (the calling
// module's name) and is recorded in the ticket's audit trail.
type NewTicket struct {
	Subject, Description, Priority string
	RequesterEmail, RequesterName  string
	SourceModule                   string
}

// Filter constrains List. Empty fields match all; AssigneeID "none" selects
// unassigned tickets. PageSize is capped at 100 by the service.
type Filter struct {
	Status, Priority, AssigneeID, TagID, Query string
	Page, PageSize                             int
}

// Page is one page of List results.
type Page struct {
	Items []Ticket
	Total int64
}

// Create opens a ticket in tenantID.
func (c *Client) Create(ctx context.Context, tenantID string, in NewTicket) (Ticket, error) {
	t, err := c.tickets.Create(ctx, &ticketv1.CreateTicketRequest{
		TenantId: tenantID, Subject: in.Subject, Description: in.Description, Priority: in.Priority,
		RequesterEmail: in.RequesterEmail, RequesterName: in.RequesterName, SourceModule: in.SourceModule,
	})
	if err != nil {
		return Ticket{}, err
	}
	return toTicket(t), nil
}

// Get reads one ticket of tenantID.
func (c *Client) Get(ctx context.Context, tenantID, id string) (Ticket, error) {
	t, err := c.tickets.Get(ctx, &ticketv1.GetTicketRequest{TenantId: tenantID, Id: id})
	if err != nil {
		return Ticket{}, err
	}
	return toTicket(t), nil
}

// List returns one filtered page of tenantID's tickets.
func (c *Client) List(ctx context.Context, tenantID string, f Filter) (Page, error) {
	res, err := c.tickets.List(ctx, &ticketv1.ListTicketsRequest{TenantId: tenantID, Filter: &ticketv1.TicketFilter{
		Status: f.Status, Priority: f.Priority, AssigneeId: f.AssigneeID, TagId: f.TagID, Query: f.Query,
		Page: clamp32(f.Page), PageSize: clamp32(f.PageSize),
	}})
	if err != nil {
		return Page{}, err
	}
	out := Page{Total: res.GetTotal(), Items: make([]Ticket, 0, len(res.GetItems()))}
	for _, t := range res.GetItems() {
		out.Items = append(out.Items, toTicket(t))
	}
	return out, nil
}

// AddComment appends a comment (internal notes are never emailed).
func (c *Client) AddComment(ctx context.Context, tenantID, ticketID, body string, internal bool) (Comment, error) {
	cm, err := c.tickets.AddComment(ctx, &ticketv1.AddCommentRequest{TenantId: tenantID, TicketId: ticketID, Body: body, Internal: internal})
	if err != nil {
		return Comment{}, err
	}
	return Comment{
		ID: cm.GetId(), TicketID: cm.GetTicketId(), Body: cm.GetBody(), Internal: cm.GetInternal(),
		AuthorKind: cm.GetAuthorKind(), AuthorID: cm.GetAuthorId(), AuthorName: cm.GetAuthorName(),
		Delivery: cm.GetDelivery(), CreatedAt: ts(cm.GetCreatedAt()),
	}, nil
}

func toTicket(t *ticketv1.Ticket) Ticket {
	return Ticket{
		ID: t.GetId(), Subject: t.GetSubject(), Description: t.GetDescription(),
		Status: t.GetStatus(), Priority: t.GetPriority(), Source: t.GetSource(),
		RequesterEmail: t.GetRequesterEmail(), RequesterName: t.GetRequesterName(),
		AssigneeID: t.GetAssigneeId(), AssigneeName: t.GetAssigneeName(),
		CommentCount: int(t.GetCommentCount()), TagIDs: append([]string(nil), t.GetTagIds()...),
		CreatedAt: ts(t.GetCreatedAt()), UpdatedAt: ts(t.GetUpdatedAt()), ResolvedAt: ts(t.GetResolvedAt()),
	}
}

func ts(t *ticketv1.Timestamp) time.Time {
	if t.GetUnix() == 0 {
		return time.Time{}
	}
	return time.Unix(t.GetUnix(), 0).UTC()
}

func clamp32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > 1<<30:
		return 1 << 30
	}
	return int32(n) // #nosec G115 -- bounded above
}
