package grpcapi

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ticketv1 "github.com/go-freya/freya/services/ticket/api/proto/ticket/v1"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/comments"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

// authorize resolves the SPIFFE caller for the request's tenant and checks the
// service allow-list for perm (mesh peers may read and manage tickets only).
func authorize(ctx context.Context, tenantID, perm string) (authz.Subjects, error) {
	subj, err := Caller(ctx, tenantID)
	if err != nil {
		return authz.Subjects{}, err
	}
	if err := authz.Require(ctx, nil, subj, perm); err != nil {
		return authz.Subjects{}, GRPCError(err)
	}
	return subj, nil
}

func ts(t time.Time) *ticketv1.Timestamp {
	if t.IsZero() {
		return nil
	}
	return &ticketv1.Timestamp{Unix: t.Unix()}
}

// ticketToPB maps a ticket view (never the raw HTML body) to the wire type.
func ticketToPB(v tickets.View) *ticketv1.Ticket {
	out := &ticketv1.Ticket{
		Id: v.ID, Subject: v.Subject, Description: v.Description, Status: v.Status, Priority: v.Priority, Source: v.Source,
		RequesterEmail: v.RequesterEmail, RequesterName: v.RequesterName, AssigneeId: v.AssigneeID, AssigneeName: v.AssigneeName,
		CommentCount: int32(min(v.CommentCount, 1<<31-1)), // #nosec G115 -- clamped above
		CreatedAt:    ts(v.CreatedAt), UpdatedAt: ts(v.UpdatedAt),
	}
	if v.ResolvedAt != nil {
		out.ResolvedAt = ts(*v.ResolvedAt)
	}
	for _, t := range v.Tags {
		out.TagIds = append(out.TagIds, t.ID)
	}
	return out
}

// Create opens a ticket on behalf of another module (e.g. a monitoring alert).
// The ticket is recorded as created by the calling service's SPIFFE id;
// source_module is informational only.
func (s *TicketsServer) Create(ctx context.Context, req *ticketv1.CreateTicketRequest) (*ticketv1.Ticket, error) {
	if s.d.Tickets == nil {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	subj, err := authorize(ctx, req.GetTenantId(), authz.TicketsManage)
	if err != nil {
		return nil, err
	}
	v, err := s.d.Tickets.Create(ctx, subj, tickets.CreateInput{
		Subject: req.GetSubject(), Description: req.GetDescription(), Priority: req.GetPriority(),
		RequesterEmail: req.GetRequesterEmail(), RequesterName: req.GetRequesterName(),
	})
	if err != nil {
		return nil, GRPCError(err)
	}
	return ticketToPB(v), nil
}

// Get returns one ticket of the request's tenant.
func (s *TicketsServer) Get(ctx context.Context, req *ticketv1.GetTicketRequest) (*ticketv1.Ticket, error) {
	if s.d.Tickets == nil {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	subj, err := authorize(ctx, req.GetTenantId(), authz.TicketsRead)
	if err != nil {
		return nil, err
	}
	v, err := s.d.Tickets.Get(ctx, subj, req.GetId())
	if err != nil {
		return nil, GRPCError(err)
	}
	return ticketToPB(v), nil
}

// List returns one page of the request's tenant's tickets (newest first).
func (s *TicketsServer) List(ctx context.Context, req *ticketv1.ListTicketsRequest) (*ticketv1.ListTicketsResponse, error) {
	if s.d.Tickets == nil {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	subj, err := authorize(ctx, req.GetTenantId(), authz.TicketsRead)
	if err != nil {
		return nil, err
	}
	f := req.GetFilter()
	if f.GetPageSize() > tickets.MaxPageSize || f.GetPageSize() < 0 || f.GetPage() < 0 || len(f.GetQuery()) > 200 {
		return nil, status.Error(codes.InvalidArgument, "bad_request")
	}
	items, total, err := s.d.Tickets.List(ctx, subj, store.TicketFilter{
		Status: f.GetStatus(), Priority: f.GetPriority(), AssigneeID: f.GetAssigneeId(), TagID: f.GetTagId(),
		Query: f.GetQuery(), Page: int(f.GetPage()), PageSize: int(f.GetPageSize()),
	})
	if err != nil {
		return nil, GRPCError(err)
	}
	out := &ticketv1.ListTicketsResponse{Items: make([]*ticketv1.Ticket, 0, len(items)), Total: total}
	for _, v := range items {
		out.Items = append(out.Items, ticketToPB(v))
	}
	return out, nil
}

// commentToPB maps a comment to the wire type (never the author's address).
func commentToPB(c store.Comment) *ticketv1.Comment {
	return &ticketv1.Comment{Id: c.ID, TicketId: c.TicketID, Body: c.Body, Internal: c.Internal, AuthorKind: c.AuthorKind,
		AuthorId: c.AuthorID, AuthorName: c.AuthorName, Delivery: c.Delivery, CreatedAt: ts(c.CreatedAt)}
}

// AddComment adds an internal note, or a public reply emailed to the
// requester, on behalf of another module (recorded as a system author).
func (s *TicketsServer) AddComment(ctx context.Context, req *ticketv1.AddCommentRequest) (*ticketv1.Comment, error) {
	if s.d.Comments == nil {
		return nil, status.Error(codes.Unimplemented, "not implemented")
	}
	subj, err := authorize(ctx, req.GetTenantId(), authz.TicketsManage)
	if err != nil {
		return nil, err
	}
	if len(req.GetBody()) > comments.MaxBody {
		return nil, status.Error(codes.InvalidArgument, "bad_request")
	}
	c, err := s.d.Comments.Add(ctx, subj, req.GetTicketId(), req.GetBody(), req.GetInternal())
	if err != nil {
		return nil, GRPCError(err)
	}
	return commentToPB(c), nil
}
