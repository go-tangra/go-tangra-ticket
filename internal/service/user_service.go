package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	ticketpb "github.com/go-tangra/go-tangra-ticket/gen/go/ticket/service/v1"
	"github.com/go-tangra/go-tangra-ticket/internal/client"
)

// UserService exposes the set of users a ticket can be assigned to,
// proxied from admin-service.
type UserService struct {
	ticketpb.UnimplementedTicketUserServiceServer

	log         *log.Helper
	adminClient *client.AdminClient
}

func NewUserService(ctx *bootstrap.Context, adminClient *client.AdminClient) *UserService {
	return &UserService{
		log:         ctx.NewLoggerHelper("ticket/service/user"),
		adminClient: adminClient,
	}
}

func (s *UserService) ListAssignableUsers(ctx context.Context, _ *ticketpb.ListAssignableUsersRequest) (*ticketpb.ListAssignableUsersResponse, error) {
	if s.adminClient == nil {
		return &ticketpb.ListAssignableUsersResponse{}, nil
	}
	resp, err := s.adminClient.ListUsers(ctx)
	if err != nil {
		return nil, ticketpb.ErrorAdminUnavailable("failed to fetch users from admin-service")
	}
	out := &ticketpb.ListAssignableUsersResponse{Users: make([]*ticketpb.AssignableUser, 0, len(resp.Items))}
	for _, u := range resp.Items {
		out.Users = append(out.Users, &ticketpb.AssignableUser{
			Id:       u.Id,
			Username: u.Username,
			Name:     u.Realname,
			Email:    u.Email,
		})
	}
	return out, nil
}
