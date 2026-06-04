package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/protobuf/types/known/emptypb"

	ticketpb "github.com/go-tangra/go-tangra-ticket/gen/go/ticket/service/v1"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
	"github.com/go-tangra/go-tangra-ticket/internal/metrics"
)

type TicketService struct {
	ticketpb.UnimplementedTicketServiceServer

	log     *log.Helper
	repo    *data.TicketRepo
	metrics *metrics.Collector
}

func NewTicketService(ctx *bootstrap.Context, repo *data.TicketRepo, m *metrics.Collector) *TicketService {
	return &TicketService{
		log:     ctx.NewLoggerHelper("ticket/service/ticket"),
		repo:    repo,
		metrics: m,
	}
}

func (s *TicketService) CreateTicket(ctx context.Context, req *ticketpb.CreateTicketRequest) (*ticketpb.CreateTicketResponse, error) {
	if req.Subject == "" {
		return nil, ticketpb.ErrorBadRequest("subject is required")
	}
	tenantID := getTenantID(ctx)
	e, err := s.repo.Create(ctx, data.NewTicket{
		TenantID:       tenantID,
		CreateBy:       getUserID(ctx),
		Source:         "manual",
		Subject:        req.Subject,
		Description:    req.Description,
		Priority:       priorityName(req.Priority),
		RequesterEmail: req.RequesterEmail,
		RequesterName:  req.RequesterName,
		AssigneeID:     req.AssigneeId,
	})
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to create ticket")
	}
	if s.metrics != nil {
		s.metrics.TicketCreated(e.Status)
	}
	return &ticketpb.CreateTicketResponse{Ticket: ticketToProto(e, 0, "")}, nil
}

func (s *TicketService) GetTicket(ctx context.Context, req *ticketpb.GetTicketRequest) (*ticketpb.GetTicketResponse, error) {
	tenantID := getTenantID(ctx)
	e, err := s.repo.Get(ctx, tenantID, req.Id)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to get ticket")
	}
	if e == nil {
		return nil, ticketpb.ErrorTicketNotFound("ticket not found")
	}
	cc := int32(s.repo.CountComments(ctx, e.ID))
	return &ticketpb.GetTicketResponse{Ticket: ticketToProto(e, cc, "")}, nil
}

func (s *TicketService) ListTickets(ctx context.Context, req *ticketpb.ListTicketsRequest) (*ticketpb.ListTicketsResponse, error) {
	tenantID := getTenantID(ctx)

	var page, pageSize int32 = 1, 20
	if req.Page != nil {
		page = *req.Page
	}
	if req.PageSize != nil {
		pageSize = *req.PageSize
	}

	f := data.TicketFilter{}
	if req.Status != nil && *req.Status != ticketpb.TicketStatus_TICKET_STATUS_UNSPECIFIED {
		f.Status = req.Status.String()
	}
	if req.Priority != nil && *req.Priority != ticketpb.TicketPriority_TICKET_PRIORITY_UNSPECIFIED {
		f.Priority = req.Priority.String()
	}
	if req.AssigneeId != nil {
		f.ByAssignee = true
		f.AssigneeID = *req.AssigneeId
	}
	if req.Query != nil {
		f.Query = *req.Query
	}

	rows, total, err := s.repo.List(ctx, tenantID, page, pageSize, f)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to list tickets")
	}
	out := &ticketpb.ListTicketsResponse{Total: int32(total), Tickets: make([]*ticketpb.Ticket, 0, len(rows))}
	for _, e := range rows {
		out.Tickets = append(out.Tickets, ticketToProto(e, 0, ""))
	}
	return out, nil
}

func (s *TicketService) UpdateTicket(ctx context.Context, req *ticketpb.UpdateTicketRequest) (*ticketpb.UpdateTicketResponse, error) {
	tenantID := getTenantID(ctx)
	var priority *string
	if req.Priority != nil && *req.Priority != ticketpb.TicketPriority_TICKET_PRIORITY_UNSPECIFIED {
		p := req.Priority.String()
		priority = &p
	}
	e, err := s.repo.Update(ctx, tenantID, req.Id, req.Subject, req.Description, priority)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to update ticket")
	}
	if e == nil {
		return nil, ticketpb.ErrorTicketNotFound("ticket not found")
	}
	return &ticketpb.UpdateTicketResponse{Ticket: ticketToProto(e, 0, "")}, nil
}

func (s *TicketService) DeleteTicket(ctx context.Context, req *ticketpb.DeleteTicketRequest) (*emptypb.Empty, error) {
	tenantID := getTenantID(ctx)
	if err := s.repo.Delete(ctx, tenantID, req.Id); err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to delete ticket")
	}
	return &emptypb.Empty{}, nil
}

func (s *TicketService) AssignTicket(ctx context.Context, req *ticketpb.AssignTicketRequest) (*ticketpb.AssignTicketResponse, error) {
	tenantID := getTenantID(ctx)
	e, err := s.repo.Assign(ctx, tenantID, req.Id, req.AssigneeId)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to assign ticket")
	}
	if e == nil {
		return nil, ticketpb.ErrorTicketNotFound("ticket not found")
	}
	s.log.Infof("ticket %s assigned to user %d by %s", req.Id, req.AssigneeId, getUsername(ctx))
	return &ticketpb.AssignTicketResponse{Ticket: ticketToProto(e, 0, "")}, nil
}

func (s *TicketService) UpdateTicketStatus(ctx context.Context, req *ticketpb.UpdateTicketStatusRequest) (*ticketpb.UpdateTicketStatusResponse, error) {
	if req.Status == ticketpb.TicketStatus_TICKET_STATUS_UNSPECIFIED {
		return nil, ticketpb.ErrorInvalidStatus("status is required")
	}
	tenantID := getTenantID(ctx)

	old, _ := s.repo.Get(ctx, tenantID, req.Id)
	oldStatus := ""
	if old != nil {
		oldStatus = old.Status
	}

	e, err := s.repo.UpdateStatus(ctx, tenantID, req.Id, req.Status.String())
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to update status")
	}
	if e == nil {
		return nil, ticketpb.ErrorTicketNotFound("ticket not found")
	}
	if s.metrics != nil {
		s.metrics.TicketStatusChanged(oldStatus, e.Status)
	}
	return &ticketpb.UpdateTicketStatusResponse{Ticket: ticketToProto(e, 0, "")}, nil
}
