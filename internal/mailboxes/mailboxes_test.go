package mailboxes

// T036: mailbox CRUD, address normalisation, global uniqueness, guarded delete.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

const (
	tenantA = "11111111-1111-7111-8111-111111111111"
	tenantB = "22222222-2222-7222-8222-222222222222"
)

type recorder struct{ events []audit.Event }

func (r *recorder) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.events = append(r.events, e)
	return nil
}

func ptr[T any](v T) *T { return &v }

func agent(tenant string) authz.Subjects {
	return authz.Subjects{TenantID: tenant, UserID: "u-" + tenant[:4], ActorKind: authz.ActorAgent}
}

func newSvc() (*Service, *memstore.Mem, *recorder) {
	st := memstore.New()
	rec := &recorder{}
	return New(Deps{Store: st, Audit: rec}), st, rec
}

func TestCreateNormalisesAndDefaults(t *testing.T) {
	svc, st, rec := newSvc()
	ctx := context.Background()
	mb, err := svc.Create(ctx, agent(tenantA), Input{Address: ptr("  Support@ACME.Example ")})
	if err != nil {
		t.Fatal(err)
	}
	if mb.Address != "support@acme.example" || mb.DisplayName != DefaultDisplayName || !mb.Active || mb.AutoAck || mb.TenantID != tenantA || mb.ID == "" {
		t.Fatalf("mailbox = %+v", mb)
	}
	r, err := st.RouteMailbox(ctx, "SUPPORT@acme.example")
	if err != nil || r.TenantID != tenantA || r.MailboxID != mb.ID {
		t.Fatalf("route = %+v %v", r, err)
	}
	if len(rec.events) != 1 || rec.events[0].EventType != audit.MailboxCreate || rec.events[0].SubjectID != mb.ID {
		t.Fatalf("audit = %+v", rec.events)
	}
	list, err := svc.List(ctx, agent(tenantA))
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if list, _ := svc.List(ctx, agent(tenantB)); len(list) != 0 {
		t.Fatal("tenant B sees tenant A's mailbox")
	}
}

func TestValidation(t *testing.T) {
	svc, _, _ := newSvc()
	ctx := context.Background()
	bad := []Input{
		{},
		{Address: ptr("")},
		{Address: ptr("not-an-address")},
		{Address: ptr("Support <support@acme.example>")},
		{Address: ptr("a@b.example, c@d.example")},
		{Address: ptr("support@acme.example\r\nBcc: x@y")},
		{Address: ptr(strings.Repeat("a", 320) + "@x.example")},
		{Address: ptr("ok@acme.example"), DisplayName: ptr("Support\r\nBcc: x@y")},
		{Address: ptr("ok@acme.example"), DisplayName: ptr(strings.Repeat("n", 201))},
		{Address: ptr("ok@acme.example"), AutoAckTemplate: ptr(strings.Repeat("t", 8193))},
		{Address: ptr("ok@acme.example"), AutoAckTemplate: ptr("bad\x00byte")},
	}
	for i, in := range bad {
		var ve ValidationError
		if _, err := svc.Create(ctx, agent(tenantA), in); !errors.As(err, &ve) {
			t.Errorf("case %d accepted: %v", i, err)
		}
	}
	if _, err := svc.Create(ctx, authz.Subjects{}, Input{Address: ptr("x@y.example")}); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("no tenant: %v", err)
	}
	mb, err := svc.Create(ctx, agent(tenantA), Input{Address: ptr("ok@acme.example"), DisplayName: ptr("  Acme Help  "),
		AutoAck: ptr(true), AutoAckTemplate: ptr("Hi {{name}},\nthanks."), Active: ptr(false)})
	if err != nil || mb.DisplayName != "Acme Help" || !mb.AutoAck || mb.AutoAckTemplate != "Hi {{name}},\nthanks." || mb.Active {
		t.Fatalf("full create = %+v %v", mb, err)
	}
}

func TestGlobalUniqueness(t *testing.T) {
	svc, _, rec := newSvc()
	ctx := context.Background()
	if _, err := svc.Create(ctx, agent(tenantA), Input{Address: ptr("help@shared.example")}); err != nil {
		t.Fatal(err)
	}
	// the same address in ANOTHER tenant conflicts (one address routes to one tenant)
	if _, err := svc.Create(ctx, agent(tenantB), Input{Address: ptr("HELP@shared.example")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-tenant duplicate: %v", err)
	}
	b, err := svc.Create(ctx, agent(tenantB), Input{Address: ptr("b@shared.example")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, agent(tenantB), b.ID, Input{Address: ptr("help@shared.example")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("update onto a taken address: %v", err)
	}
	last := rec.events[len(rec.events)-1]
	if last.Outcome != audit.OutcomeRefused {
		t.Fatalf("refusal not audited: %+v", last)
	}
}

func TestUpdatePartial(t *testing.T) {
	svc, _, _ := newSvc()
	ctx := context.Background()
	mb, _ := svc.Create(ctx, agent(tenantA), Input{Address: ptr("a@acme.example"), DisplayName: ptr("A"), AutoAckTemplate: ptr("t")})
	up, err := svc.Update(ctx, agent(tenantA), mb.ID, Input{AutoAck: ptr(true)})
	if err != nil || !up.AutoAck || up.DisplayName != "A" || up.Address != "a@acme.example" || up.AutoAckTemplate != "t" || !up.Active {
		t.Fatalf("partial = %+v %v", up, err)
	}
	up, err = svc.Update(ctx, agent(tenantA), mb.ID, Input{Address: ptr("B@Acme.example"), DisplayName: ptr(""), AutoAckTemplate: ptr("")})
	if err != nil || up.Address != "b@acme.example" || up.DisplayName != DefaultDisplayName || up.AutoAckTemplate != "" {
		t.Fatalf("clear = %+v %v", up, err)
	}
	if _, err := svc.Update(ctx, agent(tenantB), mb.ID, Input{Active: ptr(false)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant update: %v", err)
	}
	var ve ValidationError
	if _, err := svc.Update(ctx, agent(tenantA), mb.ID, Input{Address: ptr("nope")}); !errors.As(err, &ve) {
		t.Fatalf("invalid update: %v", err)
	}
}

func TestDeleteGuarded(t *testing.T) {
	svc, st, rec := newSvc()
	ctx := context.Background()
	mb, _ := svc.Create(ctx, agent(tenantA), Input{Address: ptr("a@acme.example")})
	tk := store.Ticket{ID: store.NewID(), TenantID: tenantA, Subject: "s", Status: store.StatusOpen, Priority: store.PriorityNormal,
		Source: store.SourceEmail, MailboxID: mb.ID}
	if err := st.CreateTicket(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, agent(tenantA), mb.ID, false); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete in use: %v", err)
	}
	if err := svc.Delete(ctx, agent(tenantB), mb.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant delete: %v", err)
	}
	if err := svc.Delete(ctx, agent(tenantA), mb.ID, true); err != nil {
		t.Fatalf("force delete: %v", err)
	}
	if got, _ := st.GetTicket(ctx, tenantA, tk.ID); got.MailboxID != "" {
		t.Fatal("ticket not detached")
	}
	if err := svc.Delete(ctx, agent(tenantA), mb.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	last := rec.events[len(rec.events)-1]
	if last.EventType != audit.MailboxDelete || last.Details["force"] != true {
		t.Fatalf("delete audit = %+v", last)
	}
}

func TestStoreFailures(t *testing.T) {
	svc, st, _ := newSvc()
	ctx := context.Background()
	st.FailNext("ListMailboxes")
	if _, err := svc.List(ctx, agent(tenantA)); err == nil {
		t.Fatal("list failure swallowed")
	}
	st.FailNext("CreateMailbox")
	if _, err := svc.Create(ctx, agent(tenantA), Input{Address: ptr("x@acme.example")}); err == nil {
		t.Fatal("create failure swallowed")
	}
	mb, _ := svc.Create(ctx, agent(tenantA), Input{Address: ptr("x@acme.example")})
	st.FailNext("UpdateMailbox")
	if _, err := svc.Update(ctx, agent(tenantA), mb.ID, Input{Active: ptr(false)}); err == nil {
		t.Fatal("update failure swallowed")
	}
	st.FailNext("DeleteMailbox")
	if err := svc.Delete(ctx, agent(tenantA), mb.ID, false); err == nil {
		t.Fatal("delete failure swallowed")
	}
	if _, err := svc.Get(ctx, agent(tenantA), mb.ID); err != nil {
		t.Fatal(err)
	}
}
