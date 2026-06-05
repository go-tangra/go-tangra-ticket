package service

import (
	"context"
	"strings"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/protobuf/types/known/emptypb"

	ticketpb "github.com/go-tangra/go-tangra-ticket/gen/go/ticket/service/v1"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
	"github.com/go-tangra/go-tangra-ticket/internal/mailer"
	"github.com/go-tangra/go-tangra-ticket/internal/thread"
)

type CommentService struct {
	ticketpb.UnimplementedTicketCommentServiceServer

	log        *log.Helper
	repo       *data.CommentRepo
	ticketRepo *data.TicketRepo
	mail       *mailer.Mailer
}

func NewCommentService(ctx *bootstrap.Context, repo *data.CommentRepo, ticketRepo *data.TicketRepo, mail *mailer.Mailer) *CommentService {
	return &CommentService{
		log:        ctx.NewLoggerHelper("ticket/service/comment"),
		repo:       repo,
		ticketRepo: ticketRepo,
		mail:       mail,
	}
}

func (s *CommentService) CreateComment(ctx context.Context, req *ticketpb.CreateCommentRequest) (*ticketpb.CreateCommentResponse, error) {
	if req.Body == "" {
		return nil, ticketpb.ErrorBadRequest("body is required")
	}
	tenantID := getTenantID(ctx)

	// Ensure the ticket exists (and belongs to this tenant).
	tk, err := s.ticketRepo.Get(ctx, tenantID, req.TicketId)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to load ticket")
	}
	if tk == nil {
		return nil, ticketpb.ErrorTicketNotFound("ticket not found")
	}

	e, err := s.repo.Create(ctx, data.NewComment{
		TenantID: tenantID,
		TicketID: req.TicketId,
		Body:     req.Body,
		Internal: req.Internal,
		AuthorID: getUserID(ctx),
	})
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to create comment")
	}
	return &ticketpb.CreateCommentResponse{Comment: commentToProto(e, getUsername(ctx))}, nil
}

func (s *CommentService) ListComments(ctx context.Context, req *ticketpb.ListCommentsRequest) (*ticketpb.ListCommentsResponse, error) {
	tenantID := getTenantID(ctx)
	rows, err := s.repo.ListByTicket(ctx, tenantID, req.TicketId)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to list comments")
	}
	out := &ticketpb.ListCommentsResponse{Total: int32(len(rows)), Comments: make([]*ticketpb.TicketComment, 0, len(rows))}
	for _, e := range rows {
		out.Comments = append(out.Comments, commentToProto(e, ""))
	}
	return out, nil
}

func (s *CommentService) DeleteComment(ctx context.Context, req *ticketpb.DeleteCommentRequest) (*emptypb.Empty, error) {
	tenantID := getTenantID(ctx)
	if err := s.repo.Delete(ctx, tenantID, req.Id); err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to delete comment")
	}
	return &emptypb.Empty{}, nil
}

// ReplyTicket emails a public reply to the requester and records it as a
// public comment, stamping threading headers so the reply chains back.
func (s *CommentService) ReplyTicket(ctx context.Context, req *ticketpb.ReplyTicketRequest) (*ticketpb.ReplyTicketResponse, error) {
	if strings.TrimSpace(req.Body) == "" {
		return nil, ticketpb.ErrorBadRequest("body is required")
	}
	if s.mail == nil || !s.mail.Enabled() {
		return nil, ticketpb.ErrorBadRequest("outbound email is not configured")
	}
	tenantID := getTenantID(ctx)

	tk, err := s.ticketRepo.Get(ctx, tenantID, req.TicketId)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to load ticket")
	}
	if tk == nil {
		return nil, ticketpb.ErrorTicketNotFound("ticket not found")
	}
	if tk.RequesterEmail == "" {
		return nil, ticketpb.ErrorBadRequest("ticket has no requester email to reply to")
	}

	from := tk.Recipient
	if from == "" {
		from = s.mail.DefaultFrom()
	}
	msgID := s.mail.GenMessageID(tk.ID, from)

	// Build the References chain: thread root + the most recent message we have.
	var refs []string
	if tk.ExternalID != "" {
		refs = append(refs, tk.ExternalID)
	}
	inReplyTo := tk.ExternalID
	if last, _ := s.repo.LastMessageID(ctx, tenantID, tk.ID); last != "" {
		inReplyTo = last
		if last != tk.ExternalID {
			refs = append(refs, last)
		}
	}

	out := mailer.OutMail{
		From:       from,
		To:         tk.RequesterEmail,
		ToName:     tk.RequesterName,
		Subject:    thread.ReplySubject(tk.Subject),
		Text:       thread.AppendReference(req.Body, tk.ID),
		MessageID:  msgID,
		InReplyTo:  inReplyTo,
		References: refs,
	}
	if err := s.mail.Send(ctx, out); err != nil {
		s.log.Errorf("reply email to %s failed: %v", tk.RequesterEmail, err)
		return nil, ticketpb.ErrorBadRequest("failed to send reply email: %s", err.Error())
	}

	e, err := s.repo.Create(ctx, data.NewComment{
		TenantID:  tenantID,
		TicketID:  tk.ID,
		Body:      req.Body,
		Internal:  false,
		AuthorID:  getUserID(ctx),
		MessageID: msgID,
	})
	if err != nil {
		// The mail went out; surface the failure but don't pretend it didn't send.
		return nil, ticketpb.ErrorDatabaseError("reply sent but failed to record comment")
	}

	// A reply re-opens a resolved/closed ticket.
	if tk.Status == "TICKET_STATUS_RESOLVED" || tk.Status == "TICKET_STATUS_CLOSED" {
		if _, err := s.ticketRepo.UpdateStatus(ctx, tenantID, tk.ID, "TICKET_STATUS_OPEN"); err != nil {
			s.log.Warnf("reopen ticket %s failed: %v", tk.ID, err)
		}
	}

	return &ticketpb.ReplyTicketResponse{Comment: commentToProto(e, getUsername(ctx))}, nil
}
