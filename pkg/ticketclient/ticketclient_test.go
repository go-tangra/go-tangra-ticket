package ticketclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	ticketv1 "github.com/go-freya/freya/services/ticket/api/proto/ticket/v1"
	"github.com/go-freya/freya/services/ticket/pkg/ticketclient"
)

type stub struct {
	ticketv1.UnimplementedTicketsServer
	last any
}

func (s *stub) Create(_ context.Context, req *ticketv1.CreateTicketRequest) (*ticketv1.Ticket, error) {
	s.last = req
	return &ticketv1.Ticket{Id: "t1", Subject: req.GetSubject(), Priority: req.GetPriority(), Status: "open", Source: "manual",
		TagIds: []string{"g1"}, CommentCount: 2, CreatedAt: &ticketv1.Timestamp{Unix: 100}}, nil
}

func (s *stub) Get(_ context.Context, req *ticketv1.GetTicketRequest) (*ticketv1.Ticket, error) {
	if req.GetId() == "missing" {
		return nil, status.Error(codes.NotFound, "not_found")
	}
	return &ticketv1.Ticket{Id: req.GetId(), ResolvedAt: &ticketv1.Timestamp{Unix: 50}}, nil
}

func (s *stub) List(_ context.Context, req *ticketv1.ListTicketsRequest) (*ticketv1.ListTicketsResponse, error) {
	s.last = req
	return &ticketv1.ListTicketsResponse{Items: []*ticketv1.Ticket{{Id: "a"}, {Id: "b"}}, Total: 7}, nil
}

func (s *stub) AddComment(_ context.Context, req *ticketv1.AddCommentRequest) (*ticketv1.Comment, error) {
	if req.GetBody() == "" {
		return nil, status.Error(codes.InvalidArgument, "validation_failed")
	}
	return &ticketv1.Comment{Id: "c1", TicketId: req.GetTicketId(), Body: req.GetBody(), Internal: req.GetInternal(),
		AuthorKind: "system", Delivery: "none", CreatedAt: &ticketv1.Timestamp{Unix: 200}}, nil
}

func dial(t *testing.T) (*ticketclient.Client, *stub) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	s := &stub{}
	ticketv1.RegisterTicketsServer(srv, s)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///buf",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return ticketclient.New(conn), s
}

func TestClient(t *testing.T) {
	c, s := dial(t)
	ctx := context.Background()

	tk, err := c.Create(ctx, "ten", ticketclient.NewTicket{Subject: "Disk full", Priority: "high", SourceModule: "monitor"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID != "t1" || tk.Subject != "Disk full" || tk.CommentCount != 2 || len(tk.TagIDs) != 1 ||
		!tk.CreatedAt.Equal(time.Unix(100, 0)) || !tk.UpdatedAt.IsZero() {
		t.Fatalf("create: %+v", tk)
	}
	if req := s.last.(*ticketv1.CreateTicketRequest); req.GetTenantId() != "ten" || req.GetSourceModule() != "monitor" {
		t.Fatalf("create req: %+v", req)
	}

	got, err := c.Get(ctx, "ten", "x")
	if err != nil || got.ID != "x" || got.ResolvedAt.Unix() != 50 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := c.Get(ctx, "ten", "missing"); status.Code(err) != codes.NotFound {
		t.Fatalf("get missing: %v", err)
	}

	page, err := c.List(ctx, "ten", ticketclient.Filter{Status: "open", AssigneeID: "none", Page: 2, PageSize: 25})
	if err != nil || len(page.Items) != 2 || page.Total != 7 {
		t.Fatalf("list: %+v %v", page, err)
	}
	if f := s.last.(*ticketv1.ListTicketsRequest).GetFilter(); f.GetStatus() != "open" || f.GetAssigneeId() != "none" || f.GetPage() != 2 || f.GetPageSize() != 25 {
		t.Fatalf("list filter: %+v", f)
	}
	if _, err := c.List(ctx, "ten", ticketclient.Filter{Page: -5, PageSize: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	if f := s.last.(*ticketv1.ListTicketsRequest).GetFilter(); f.GetPage() != 0 || f.GetPageSize() != 1<<30 {
		t.Fatalf("clamp: %+v", f)
	}

	cm, err := c.AddComment(ctx, "ten", "t1", "note", true)
	if err != nil || cm.ID != "c1" || !cm.Internal || cm.TicketID != "t1" || cm.CreatedAt.Unix() != 200 {
		t.Fatalf("comment: %+v %v", cm, err)
	}
	if _, err := c.AddComment(ctx, "ten", "t1", "", false); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("comment invalid: %v", err)
	}
	if _, err := c.Create(context.Background(), "ten", ticketclient.NewTicket{}); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsPropagate(t *testing.T) {
	c, _ := dial(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Create(ctx, "t", ticketclient.NewTicket{}); err == nil {
		t.Fatal("create: want error")
	}
	if _, err := c.List(ctx, "t", ticketclient.Filter{}); err == nil {
		t.Fatal("list: want error")
	}
}
