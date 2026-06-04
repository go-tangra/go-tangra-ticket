package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/protobuf/types/known/emptypb"

	ticketpb "github.com/go-tangra/go-tangra-ticket/gen/go/ticket/service/v1"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
)

type CommentService struct {
	ticketpb.UnimplementedTicketCommentServiceServer

	log        *log.Helper
	repo       *data.CommentRepo
	ticketRepo *data.TicketRepo
}

func NewCommentService(ctx *bootstrap.Context, repo *data.CommentRepo, ticketRepo *data.TicketRepo) *CommentService {
	return &CommentService{
		log:        ctx.NewLoggerHelper("ticket/service/comment"),
		repo:       repo,
		ticketRepo: ticketRepo,
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
