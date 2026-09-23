// Package grpcapi serves ticket.v1 for other platform services on the Freya
// SPIFFE mTLS channel (contracts §C): the caller is an authenticated service
// (the framework's policy allow-list admits it before any handler runs) acting
// for the tenant named in the request. Nothing here is proxied by the gateway.
// Responses never carry raw HTML bodies, attachment bytes or credentials.
package grpcapi

import (
	"context"
	"errors"
	"regexp"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go-freya/freya/authn"
	ticketv1 "github.com/go-freya/freya/services/ticket/api/proto/ticket/v1"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/comments"
	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// callerFunc resolves the SPIFFE identity of a call (overridable in tests).
var callerFunc = func(ctx context.Context) (string, bool) {
	p, ok := authn.FromContext(ctx)
	if !ok {
		return "", false
	}
	return p.ID.String(), true
}

// Caller returns the service subject for the tenant named in the request; the
// tenant must be a uuid and the peer must present a SPIFFE identity.
func Caller(ctx context.Context, tenantID string) (authz.Subjects, error) {
	id, ok := callerFunc(ctx)
	if !ok || id == "" {
		return authz.Subjects{}, status.Error(codes.Unauthenticated, "service identity required")
	}
	if !uuidRE.MatchString(tenantID) {
		return authz.Subjects{}, status.Error(codes.InvalidArgument, "tenant_id must be a uuid")
	}
	return authz.Service(tenantID, id), nil
}

// GRPCError maps a service/domain error to a gRPC status carrying only a
// stable reason (never detail).
func GRPCError(err error) error {
	if _, ok := status.FromError(err); ok && err != nil {
		return err
	}
	var ve tickets.ValidationError
	var cve comments.ValidationError
	switch {
	case errors.Is(err, comments.ErrCommentNotFound):
		return status.Error(codes.NotFound, "comment_not_found")
	case errors.Is(err, comments.ErrReplyUnavailable):
		return status.Error(codes.FailedPrecondition, "reply_unavailable")
	case errors.Is(err, comments.ErrDeliveryFailed):
		return status.Error(codes.Unavailable, "delivery_failed")
	case errors.Is(err, comments.ErrUnsafeHeader), errors.As(err, &cve):
		return status.Error(codes.InvalidArgument, "bad_request")
	case errors.Is(err, repo.ErrNotFound), errors.Is(err, tickets.ErrNotFound):
		return status.Error(codes.NotFound, "not_found")
	case errors.Is(err, authz.ErrForbidden):
		return status.Error(codes.PermissionDenied, "forbidden")
	case errors.Is(err, repo.ErrConflict), errors.Is(err, repo.ErrNotEmpty):
		return status.Error(codes.FailedPrecondition, "conflict")
	case errors.Is(err, tickets.ErrInvalidStatus):
		return status.Error(codes.InvalidArgument, "invalid_status")
	case errors.Is(err, tickets.ErrInvalidAssignee):
		return status.Error(codes.InvalidArgument, "invalid_assignee")
	case errors.As(err, &ve):
		return status.Error(codes.InvalidArgument, "bad_request")
	case errors.Is(err, tickets.ErrDirectoryUnavailable):
		return status.Error(codes.Unavailable, "directory_unavailable")
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}

// Deps carries the services the ticket.v1 server uses. A nil service leaves
// its methods Unimplemented (US1 Create/Get/List; US3 AddComment).
type Deps struct {
	Tickets  *tickets.Service
	Comments *comments.Service
}

// TicketsServer implements ticket.v1.Tickets. Until a story wires its method it
// answers Unimplemented.
type TicketsServer struct {
	ticketv1.UnimplementedTicketsServer
	d Deps
}

// Register registers the ticket.v1 mesh server on the gRPC server.
func Register(gs grpc.ServiceRegistrar, d Deps) {
	ticketv1.RegisterTicketsServer(gs, &TicketsServer{d: d})
}
