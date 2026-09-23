package grpcapi

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ticketv1 "github.com/go-freya/freya/services/ticket/api/proto/ticket/v1"
	"github.com/go-freya/freya/services/ticket/internal/comments"
	"github.com/go-freya/freya/services/ticket/internal/mailer"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

func TestAddComment(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	mail := &mailer.Fake{}
	tk := tickets.New(tickets.Deps{Store: st})
	s := &TicketsServer{d: Deps{Tickets: tk, Comments: comments.New(comments.Deps{Store: st, Mailer: mail, Tickets: tk})}}
	withCaller(t, monitor, true)
	created, err := s.Create(ctx, &ticketv1.CreateTicketRequest{TenantId: tn, Subject: "Disk full", RequesterEmail: "ops@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.AddComment(ctx, &ticketv1.AddCommentRequest{TenantId: tn, TicketId: created.GetId(), Body: "cleared", Internal: true})
	if err != nil || !c.GetInternal() || c.GetAuthorKind() != "system" || c.GetAuthorId() != monitor || c.GetDelivery() != "none" ||
		c.GetBody() != "cleared" || c.GetCreatedAt().GetUnix() == 0 || c.GetTicketId() != created.GetId() {
		t.Fatalf("note = %+v %v", c, err)
	}
	// a public comment is a reply: without a mailbox it is unavailable
	if _, err := s.AddComment(ctx, &ticketv1.AddCommentRequest{TenantId: tn, TicketId: created.GetId(), Body: "fixed"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("reply without mailbox: %v", err)
	}
	_ = st.CreateMailbox(ctx, store.Mailbox{ID: store.NewID(), TenantID: tn, Address: "support@acme.example", DisplayName: "S", Active: true})
	if c, err := s.AddComment(ctx, &ticketv1.AddCommentRequest{TenantId: tn, TicketId: created.GetId(), Body: "fixed"}); err != nil || c.GetDelivery() != "sent" {
		t.Fatalf("reply = %+v %v", c, err)
	}
	mail.Err = context.DeadlineExceeded
	if _, err := s.AddComment(ctx, &ticketv1.AddCommentRequest{TenantId: tn, TicketId: created.GetId(), Body: "again"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("delivery failure: %v", err)
	}
	for _, req := range []*ticketv1.AddCommentRequest{
		{TenantId: tn, TicketId: created.GetId(), Body: "", Internal: true},
		{TenantId: "not-a-uuid", TicketId: created.GetId(), Body: "x", Internal: true},
	} {
		if _, err := s.AddComment(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%+v: %v", req, err)
		}
	}
	if _, err := s.AddComment(ctx, &ticketv1.AddCommentRequest{TenantId: otherTenant, TicketId: created.GetId(), Body: "x", Internal: true}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant: %v", err)
	}
	if _, err := (&TicketsServer{}).AddComment(ctx, &ticketv1.AddCommentRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unwired: %v", err)
	}
	withCaller(t, "", false)
	if _, err := s.AddComment(ctx, &ticketv1.AddCommentRequest{TenantId: tn, TicketId: created.GetId(), Body: "x", Internal: true}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no caller: %v", err)
	}
}
