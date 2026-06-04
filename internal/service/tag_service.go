package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/protobuf/types/known/emptypb"

	ticketpb "github.com/go-tangra/go-tangra-ticket/gen/go/ticket/service/v1"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
)

type TagService struct {
	ticketpb.UnimplementedTicketTagServiceServer

	log        *log.Helper
	repo       *data.TagRepo
	ticketRepo *data.TicketRepo
}

func NewTagService(ctx *bootstrap.Context, repo *data.TagRepo, ticketRepo *data.TicketRepo) *TagService {
	return &TagService{
		log:        ctx.NewLoggerHelper("ticket/service/tag"),
		repo:       repo,
		ticketRepo: ticketRepo,
	}
}

func (s *TagService) ListTags(ctx context.Context, req *ticketpb.ListTagsRequest) (*ticketpb.ListTagsResponse, error) {
	kind := ""
	if req.Kind != nil {
		kind = *req.Kind
	}
	rows, err := s.repo.List(ctx, getTenantID(ctx), kind)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to list tags")
	}
	out := &ticketpb.ListTagsResponse{Tags: make([]*ticketpb.TicketTag, 0, len(rows))}
	for _, e := range rows {
		out.Tags = append(out.Tags, tagToProto(e))
	}
	return out, nil
}

func (s *TagService) CreateTag(ctx context.Context, req *ticketpb.CreateTagRequest) (*ticketpb.CreateTagResponse, error) {
	if req.Name == "" {
		return nil, ticketpb.ErrorBadRequest("name is required")
	}
	e, err := s.repo.Create(ctx, data.NewTag{
		TenantID:    getTenantID(ctx),
		Name:        req.Name,
		Kind:        req.Kind,
		Color:       req.Color,
		Description: req.Description,
	})
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to create tag")
	}
	return &ticketpb.CreateTagResponse{Tag: tagToProto(e)}, nil
}

func (s *TagService) UpdateTag(ctx context.Context, req *ticketpb.UpdateTagRequest) (*ticketpb.UpdateTagResponse, error) {
	e, err := s.repo.Update(ctx, getTenantID(ctx), req.Id, req.Name, req.Color, req.Description)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to update tag")
	}
	if e == nil {
		return nil, ticketpb.ErrorNotFound("tag not found")
	}
	return &ticketpb.UpdateTagResponse{Tag: tagToProto(e)}, nil
}

func (s *TagService) DeleteTag(ctx context.Context, req *ticketpb.DeleteTagRequest) (*emptypb.Empty, error) {
	if err := s.repo.Delete(ctx, getTenantID(ctx), req.Id); err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to delete tag")
	}
	return &emptypb.Empty{}, nil
}

func (s *TagService) SetTicketTags(ctx context.Context, req *ticketpb.SetTicketTagsRequest) (*ticketpb.SetTicketTagsResponse, error) {
	tenantID := getTenantID(ctx)
	if err := s.repo.SetTicketTags(ctx, tenantID, req.TicketId, req.TagIds); err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to set ticket tags")
	}
	e, err := s.ticketRepo.Get(ctx, tenantID, req.TicketId)
	if err != nil || e == nil {
		return nil, ticketpb.ErrorTicketNotFound("ticket not found")
	}
	cc := int32(s.ticketRepo.CountComments(ctx, e.ID))
	return &ticketpb.SetTicketTagsResponse{Ticket: ticketToProto(e, cc, "")}, nil
}
