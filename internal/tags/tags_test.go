package tags

// T059: tag vocabulary — kind default and immutability, per-kind
// case-insensitive uniqueness, colour validation, set-ticket-tags replacing the
// set with unknown ids refused, delete removing links, audit.

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

func (r *recorder) has(t audit.EventType, id, outcome string) bool {
	for _, e := range r.events {
		if e.EventType == t && e.SubjectID == id && e.Outcome == outcome {
			return true
		}
	}
	return false
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

var ctx = context.Background()

func TestCreateDefaultsAndUniqueness(t *testing.T) {
	svc, _, rec := newSvc()
	tg, err := svc.Create(ctx, agent(tenantA), Input{Name: ptr("  Billing "), Description: ptr("money")})
	if err != nil {
		t.Fatal(err)
	}
	if tg.Kind != store.KindTag || tg.Name != "Billing" || tg.Color != "" || tg.TenantID != tenantA || tg.ID == "" {
		t.Fatalf("tag = %+v", tg)
	}
	if !rec.has(audit.TagCreate, tg.ID, audit.OutcomeOK) {
		t.Fatalf("audit = %+v", rec.events)
	}
	if _, err := svc.Create(ctx, agent(tenantA), Input{Name: ptr("BILLING")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := svc.Create(ctx, agent(tenantA), Input{Name: ptr("billing"), Kind: ptr(store.KindCategory)}); err != nil {
		t.Fatalf("same name of the other kind: %v", err)
	}
	if _, err := svc.Create(ctx, agent(tenantB), Input{Name: ptr("Billing")}); err != nil {
		t.Fatalf("same name in another tenant: %v", err)
	}
	for _, c := range Colors {
		if _, err := svc.Create(ctx, agent(tenantA), Input{Name: ptr("c-" + c), Color: ptr(c)}); err != nil {
			t.Fatalf("palette colour %q: %v", c, err)
		}
	}
	if _, err := svc.Create(ctx, agent(tenantA), Input{Name: ptr("hex"), Color: ptr("#A1b2C3")}); err != nil {
		t.Fatalf("hex colour: %v", err)
	}
}

func TestValidation(t *testing.T) {
	svc, _, _ := newSvc()
	bad := []Input{
		{},
		{Name: ptr("")},
		{Name: ptr("   ")},
		{Name: ptr(strings.Repeat("x", MaxName+1))},
		{Name: ptr("tab\there")},
		{Name: ptr("bad\xffutf8")},
		{Name: ptr("x"), Kind: ptr("label")},
		{Name: ptr("x"), Color: ptr("red; background:url(x)")},
		{Name: ptr("x"), Color: ptr("#12345")},
		{Name: ptr("x"), Description: ptr(strings.Repeat("d", MaxDescription+1))},
	}
	for _, in := range bad {
		var ve ValidationError
		if _, err := svc.Create(ctx, agent(tenantA), in); !errors.As(err, &ve) {
			t.Fatalf("Create(%+v) = %v, want a ValidationError", in, err)
		}
	}
	if _, err := svc.Create(ctx, authz.Subjects{}, Input{Name: ptr("x")}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("no tenant = %v", err)
	}
}

func TestUpdateKeepsKind(t *testing.T) {
	svc, _, rec := newSvc()
	tg, _ := svc.Create(ctx, agent(tenantA), Input{Name: ptr("Alpha"), Kind: ptr(store.KindCategory), Color: ptr("info")})
	up, err := svc.Update(ctx, agent(tenantA), tg.ID, Input{Name: ptr("Alpha 2"), Kind: ptr(store.KindCategory)})
	if err != nil || up.Name != "Alpha 2" || up.Kind != store.KindCategory || up.Color != "info" {
		t.Fatalf("update = %+v %v", up, err)
	}
	var ve ValidationError
	if _, err := svc.Update(ctx, agent(tenantA), tg.ID, Input{Kind: ptr(store.KindTag)}); !errors.As(err, &ve) || ve.Field != "kind" {
		t.Fatalf("kind change = %v", err)
	}
	if up, err := svc.Update(ctx, agent(tenantA), tg.ID, Input{Color: ptr("")}); err != nil || up.Color != "" {
		t.Fatalf("reset colour = %+v %v", up, err)
	}
	if _, err := svc.Update(ctx, agent(tenantB), tg.ID, Input{Name: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant = %v", err)
	}
	if _, err := svc.Update(ctx, agent(tenantA), tg.ID, Input{Name: ptr("")}); !errors.As(err, &ve) {
		t.Fatalf("blank rename = %v", err)
	}
	other, _ := svc.Create(ctx, agent(tenantA), Input{Name: ptr("Beta"), Kind: ptr(store.KindCategory)})
	if _, err := svc.Update(ctx, agent(tenantA), other.ID, Input{Name: ptr("ALPHA 2")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename conflict = %v", err)
	}
	if !rec.has(audit.TagUpdate, tg.ID, audit.OutcomeOK) {
		t.Fatal("update not audited")
	}
}

func seedTicket(t *testing.T, st *memstore.Mem, tenant string) store.Ticket {
	t.Helper()
	tk := store.Ticket{ID: store.NewID(), TenantID: tenant, Subject: "s", Status: store.StatusOpen, Priority: store.PriorityNormal, Source: store.SourceManual}
	if err := st.CreateTicket(ctx, tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestSetTicketTagsReplacesAndRefusesUnknown(t *testing.T) {
	svc, st, rec := newSvc()
	a, _ := svc.Create(ctx, agent(tenantA), Input{Name: ptr("A")})
	b, _ := svc.Create(ctx, agent(tenantA), Input{Name: ptr("B")})
	foreign, _ := svc.Create(ctx, agent(tenantB), Input{Name: ptr("F")})
	tk := seedTicket(t, st, tenantA)

	if err := svc.SetTicketTags(ctx, agent(tenantA), tk.ID, []string{a.ID, b.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	links, _ := st.TagsForTickets(ctx, tenantA, []string{tk.ID})
	if len(links[tk.ID]) != 2 {
		t.Fatalf("links = %+v", links)
	}
	if err := svc.SetTicketTags(ctx, agent(tenantA), tk.ID, []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	links, _ = st.TagsForTickets(ctx, tenantA, []string{tk.ID})
	if len(links[tk.ID]) != 1 || links[tk.ID][0].ID != b.ID {
		t.Fatalf("not replaced: %+v", links)
	}
	for _, id := range []string{"missing", foreign.ID} {
		if err := svc.SetTicketTags(ctx, agent(tenantA), tk.ID, []string{a.ID, id}); !errors.Is(err, ErrUnknownTag) {
			t.Fatalf("unknown %s = %v", id, err)
		}
	}
	links, _ = st.TagsForTickets(ctx, tenantA, []string{tk.ID})
	if len(links[tk.ID]) != 1 {
		t.Fatal("refused set changed links")
	}
	if err := svc.SetTicketTags(ctx, agent(tenantA), "missing", nil); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("missing ticket = %v", err)
	}
	if err := svc.SetTicketTags(ctx, agent(tenantB), tk.ID, nil); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("cross-tenant ticket = %v", err)
	}
	many := make([]string, MaxTicketTags+1)
	for i := range many {
		many[i] = store.NewID()
	}
	var ve ValidationError
	if err := svc.SetTicketTags(ctx, agent(tenantA), tk.ID, many); !errors.As(err, &ve) {
		t.Fatalf("too many = %v", err)
	}
	if !rec.has(audit.TicketTags, tk.ID, audit.OutcomeOK) || !rec.has(audit.TicketTags, tk.ID, audit.OutcomeRefused) {
		t.Fatalf("set tags audit = %+v", rec.events)
	}
	if err := svc.SetTicketTags(ctx, agent(tenantA), tk.ID, []string{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
}

func TestDeleteRemovesLinks(t *testing.T) {
	svc, st, rec := newSvc()
	a, _ := svc.Create(ctx, agent(tenantA), Input{Name: ptr("A")})
	tk := seedTicket(t, st, tenantA)
	_ = svc.SetTicketTags(ctx, agent(tenantA), tk.ID, []string{a.ID})
	if err := svc.Delete(ctx, agent(tenantB), a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant delete = %v", err)
	}
	if err := svc.Delete(ctx, agent(tenantA), a.ID); err != nil {
		t.Fatal(err)
	}
	links, _ := st.TagsForTickets(ctx, tenantA, []string{tk.ID})
	if len(links[tk.ID]) != 0 {
		t.Fatal("links not removed")
	}
	if !rec.has(audit.TagDelete, a.ID, audit.OutcomeOK) {
		t.Fatal("delete not audited")
	}
	if err := svc.Delete(ctx, agent(tenantA), a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete again = %v", err)
	}
}

func TestListByKind(t *testing.T) {
	svc, st, _ := newSvc()
	_, _ = svc.Create(ctx, agent(tenantA), Input{Name: ptr("t1")})
	_, _ = svc.Create(ctx, agent(tenantA), Input{Name: ptr("c1"), Kind: ptr(store.KindCategory)})
	all, err := svc.List(ctx, agent(tenantA), "")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %+v %v", all, err)
	}
	cats, _ := svc.List(ctx, agent(tenantA), store.KindCategory)
	if len(cats) != 1 || cats[0].Name != "c1" {
		t.Fatalf("categories = %+v", cats)
	}
	var ve ValidationError
	if _, err := svc.List(ctx, agent(tenantA), "label"); !errors.As(err, &ve) {
		t.Fatalf("bad kind = %v", err)
	}
	if b, _ := svc.List(ctx, agent(tenantB), ""); len(b) != 0 {
		t.Fatal("tenant leak")
	}
	st.FailNext("ListTags")
	if _, err := svc.List(ctx, agent(tenantA), ""); err == nil {
		t.Fatal("store failure hidden")
	}
}
