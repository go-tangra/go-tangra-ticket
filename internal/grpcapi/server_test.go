package grpcapi

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ticketv1 "github.com/go-freya/freya/services/ticket/api/proto/ticket/v1"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/repo"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func withCaller(t *testing.T, id string, ok bool) {
	t.Helper()
	prev := callerFunc
	callerFunc = func(context.Context) (string, bool) { return id, ok }
	t.Cleanup(func() { callerFunc = prev })
}

func TestCaller(t *testing.T) {
	if _, err := Caller(context.Background(), tn); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no peer: %v", err)
	}
	withCaller(t, "spiffe://example.org/svc/monitor", true)
	s, err := Caller(context.Background(), tn)
	if err != nil || s.ActorKind != authz.ActorService || s.TenantID != tn || s.UserID != "spiffe://example.org/svc/monitor" {
		t.Fatalf("caller = %+v %v", s, err)
	}
	if _, err := Caller(context.Background(), "not-a-uuid"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad tenant: %v", err)
	}
	withCaller(t, "", true)
	if _, err := Caller(context.Background(), tn); status.Code(err) != codes.Unauthenticated {
		t.Fatal("empty identity")
	}
}

func TestGRPCError(t *testing.T) {
	cases := map[error]codes.Code{
		repo.ErrNotFound:                    codes.NotFound,
		authz.ErrForbidden:                  codes.PermissionDenied,
		repo.ErrConflict:                    codes.FailedPrecondition,
		repo.ErrNotEmpty:                    codes.FailedPrecondition,
		errors.New("x"):                     codes.Unavailable,
		status.Error(codes.Aborted, "keep"): codes.Aborted,
	}
	for err, want := range cases {
		if got := status.Code(GRPCError(err)); got != want {
			t.Errorf("%v => %s, want %s", err, got, want)
		}
	}
}

type registrar struct{ names []string }

func (r *registrar) RegisterService(d *grpc.ServiceDesc, _ any) {
	r.names = append(r.names, d.ServiceName)
}

func TestRegisterAndUnimplemented(t *testing.T) {
	r := &registrar{}
	Register(r, Deps{})
	if len(r.names) != 1 || r.names[0] != "ticket.v1.Tickets" {
		t.Fatalf("registered = %v", r.names)
	}
	s := &TicketsServer{}
	if _, err := s.Create(context.Background(), &ticketv1.CreateTicketRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("create = %v", err)
	}
}
