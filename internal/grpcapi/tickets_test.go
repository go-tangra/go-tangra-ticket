package grpcapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ticketv1 "github.com/go-tangra/go-tangra-ticket/v4/api/proto/ticket/v1"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tickets"
)

const (
	otherTenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	monitor     = "spiffe://example.org/svc/monitor"
)

func newTicketsServer(t *testing.T) (*TicketsServer, *memstore.Mem) {
	t.Helper()
	st := memstore.New()
	dir := agents.NewFake()
	svc := tickets.New(tickets.Deps{Store: st, Agents: dir})
	return &TicketsServer{d: Deps{Tickets: svc}}, st
}

func TestTicketsCreateGetList(t *testing.T) {
	s, st := newTicketsServer(t)
	ctx := context.Background()
	withCaller(t, monitor, true)

	created, err := s.Create(ctx, &ticketv1.CreateTicketRequest{TenantId: tn, Subject: "Disk full on db-1", Description: "95%",
		Priority: "high", RequesterEmail: "ops@example.org", SourceModule: "monitor"})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetId() == "" || created.GetStatus() != "open" || created.GetPriority() != "high" || created.GetSource() != "manual" ||
		created.GetCreatedAt().GetUnix() == 0 || created.GetResolvedAt() != nil {
		t.Fatalf("created = %+v", created)
	}
	stored, _ := st.GetTicket(ctx, tn, created.GetId())
	if stored.CreatedBy != monitor {
		t.Fatalf("created_by = %q", stored.CreatedBy)
	}
	tag := store.Tag{ID: store.NewID(), TenantID: tn, Name: "infra", Kind: store.KindTag}
	_ = st.CreateTag(ctx, tag)
	_ = st.SetTicketTags(ctx, tn, created.GetId(), []string{tag.ID})
	if _, err := st.SetStatus(ctx, tn, created.GetId(), store.StatusResolved, stored.CreatedAt); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, &ticketv1.GetTicketRequest{TenantId: tn, Id: created.GetId()})
	if err != nil || got.GetSubject() != "Disk full on db-1" || len(got.GetTagIds()) != 1 || got.GetResolvedAt() == nil {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := s.Get(ctx, &ticketv1.GetTicketRequest{TenantId: otherTenant, Id: created.GetId()}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant get: %v", err)
	}

	_, _ = s.Create(ctx, &ticketv1.CreateTicketRequest{TenantId: tn, Subject: "Second"})
	page, err := s.List(ctx, &ticketv1.ListTicketsRequest{TenantId: tn, Filter: &ticketv1.TicketFilter{PageSize: 1}})
	if err != nil || page.GetTotal() != 2 || len(page.GetItems()) != 1 || page.GetItems()[0].GetSubject() != "Second" {
		t.Fatalf("list = %+v %v", page, err)
	}
	page, err = s.List(ctx, &ticketv1.ListTicketsRequest{TenantId: tn, Filter: &ticketv1.TicketFilter{Status: "resolved", AssigneeId: "none"}})
	if err != nil || page.GetTotal() != 1 {
		t.Fatalf("filtered list = %+v %v", page, err)
	}
	if page, err := s.List(ctx, &ticketv1.ListTicketsRequest{TenantId: tn}); err != nil || page.GetTotal() != 2 {
		t.Fatalf("nil filter = %+v %v", page, err)
	}
	if page, err := s.List(ctx, &ticketv1.ListTicketsRequest{TenantId: otherTenant}); err != nil || page.GetTotal() != 0 {
		t.Fatalf("other tenant list = %+v %v", page, err)
	}
}

func TestTicketsRefusals(t *testing.T) {
	s, _ := newTicketsServer(t)
	ctx := context.Background()

	// no SPIFFE identity
	withCaller(t, "", false)
	if _, err := s.Create(ctx, &ticketv1.CreateTicketRequest{TenantId: tn, Subject: "x"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous create: %v", err)
	}
	if _, err := s.Get(ctx, &ticketv1.GetTicketRequest{TenantId: tn, Id: "x"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous get: %v", err)
	}
	if _, err := s.List(ctx, &ticketv1.ListTicketsRequest{TenantId: tn}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous list: %v", err)
	}

	withCaller(t, monitor, true)
	cases := []struct {
		name string
		call func() error
		want codes.Code
	}{
		{"bad tenant", func() error {
			_, err := s.Create(ctx, &ticketv1.CreateTicketRequest{TenantId: "nope", Subject: "x"})
			return err
		}, codes.InvalidArgument},
		{"empty subject", func() error {
			_, err := s.Create(ctx, &ticketv1.CreateTicketRequest{TenantId: tn, Subject: " "})
			return err
		}, codes.InvalidArgument},
		{"bad priority", func() error {
			_, err := s.Create(ctx, &ticketv1.CreateTicketRequest{TenantId: tn, Subject: "x", Priority: "asap"})
			return err
		}, codes.InvalidArgument},
		{"missing ticket", func() error {
			_, err := s.Get(ctx, &ticketv1.GetTicketRequest{TenantId: tn, Id: "missing"})
			return err
		}, codes.NotFound},
		{"bad status filter", func() error {
			_, err := s.List(ctx, &ticketv1.ListTicketsRequest{TenantId: tn, Filter: &ticketv1.TicketFilter{Status: "unspecified"}})
			return err
		}, codes.InvalidArgument},
		{"page size", func() error {
			_, err := s.List(ctx, &ticketv1.ListTicketsRequest{TenantId: tn, Filter: &ticketv1.TicketFilter{PageSize: 101}})
			return err
		}, codes.InvalidArgument},
		{"query length", func() error {
			_, err := s.List(ctx, &ticketv1.ListTicketsRequest{TenantId: tn, Filter: &ticketv1.TicketFilter{Query: strings.Repeat("q", 201)}})
			return err
		}, codes.InvalidArgument},
	}
	for _, c := range cases {
		if got := status.Code(c.call()); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestTicketsUnwired(t *testing.T) {
	s := &TicketsServer{}
	ctx := context.Background()
	if _, err := s.Get(ctx, &ticketv1.GetTicketRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("get = %v", err)
	}
	if _, err := s.List(ctx, &ticketv1.ListTicketsRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("list = %v", err)
	}
}

func TestTicketErrorMapping(t *testing.T) {
	cases := map[error]codes.Code{
		tickets.ErrNotFound:                 codes.NotFound,
		tickets.ErrInvalidStatus:            codes.InvalidArgument,
		tickets.ErrInvalidAssignee:          codes.InvalidArgument,
		tickets.ValidationError{Field: "x"}: codes.InvalidArgument,
		tickets.ErrDirectoryUnavailable:     codes.Unavailable,
		errors.Join(authz.ErrForbidden):     codes.PermissionDenied,
	}
	for err, want := range cases {
		if got := status.Code(GRPCError(err)); got != want {
			t.Errorf("%v => %s, want %s", err, got, want)
		}
	}
	// a non-service subject never passes the mesh allow-list for an unknown permission
	withCaller(t, monitor, true)
	if _, err := authorize(context.Background(), tn, authz.BackupManage); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("service backup: %v", err)
	}
}
