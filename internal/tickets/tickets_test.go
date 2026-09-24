package tickets

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/audit"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/blob"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/events"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

const (
	tenantA = "11111111-1111-7111-8111-111111111111"
	tenantB = "22222222-2222-7222-8222-222222222222"
	agentID = "55555555-5555-7555-8555-555555555555"
	ada     = "66666666-6666-7666-8666-666666666666"
	bob     = "77777777-7777-7777-8777-777777777777"
)

type recAudit struct {
	mu  sync.Mutex
	evs []audit.Event
}

func (r *recAudit) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
	return nil
}

func (r *recAudit) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, e := range r.evs {
		out = append(out, string(e.EventType))
	}
	return out
}

type fixture struct {
	svc   *Service
	st    *memstore.Mem
	dir   *agents.Fake
	pub   *events.Recorder
	aud   *recAudit
	blobs *blob.Fake
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{st: memstore.New(), dir: agents.NewFake(), pub: &events.Recorder{}, aud: &recAudit{}, blobs: blob.NewFake(),
		now: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	f.dir.Add(tenantA, agents.User{ID: ada, Name: "Ada Lovelace"}, true)
	f.dir.Add(tenantA, agents.User{ID: bob, Name: "Bob Viewer"}, false)
	f.svc = New(Deps{Store: f.st, Agents: f.dir, Events: f.pub, Audit: f.aud, Blobs: f.blobs})
	f.svc.SetClock(func() time.Time { return f.now })
	return f
}

func agentOf(tenant string) authz.Subjects {
	return authz.Subjects{TenantID: tenant, UserID: agentID, ActorKind: authz.ActorAgent}
}

func ptr(s string) *string { return &s }

func (f *fixture) eventTypes() []string {
	out := []string{}
	for _, e := range f.pub.Events {
		out = append(out, e.Type)
	}
	return out
}

func TestCreateDefaults(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.Create(context.Background(), agentOf(tenantA), CreateInput{Subject: "  Printer on fire  ", Description: "smoke"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != store.StatusOpen || v.Priority != store.PriorityNormal || v.Source != store.SourceManual {
		t.Fatalf("defaults: %+v", v.Ticket)
	}
	if v.Subject != "Printer on fire" || v.CreatedBy != agentID || v.TenantID != tenantA || v.ID == "" || v.Tags == nil {
		t.Fatalf("create = %+v", v)
	}
	if got := f.eventTypes(); len(got) != 1 || got[0] != events.TicketCreated {
		t.Fatalf("events = %v", got)
	}
	if got := f.aud.types(); len(got) != 1 || got[0] != string(audit.TicketCreate) {
		t.Fatalf("audit = %v", got)
	}
	// with priority, requester and assignee
	v, err = f.svc.Create(context.Background(), agentOf(tenantA), CreateInput{Subject: "VPN", Priority: store.PriorityHigh,
		RequesterEmail: "Carol@Example.org", RequesterName: "Carol", AssigneeID: ada})
	if err != nil {
		t.Fatal(err)
	}
	if v.Priority != store.PriorityHigh || v.AssigneeID != ada || v.AssigneeName != "Ada Lovelace" || v.RequesterEmail != "Carol@Example.org" {
		t.Fatalf("full create = %+v", v.Ticket)
	}
	// audit/event payloads never carry requester PII or bodies
	for _, e := range f.aud.evs {
		for k, val := range e.Details {
			if s, ok := val.(string); ok && (strings.Contains(s, "Carol") || strings.Contains(s, "smoke")) {
				t.Fatalf("audit detail %s leaks %q", k, s)
			}
		}
	}
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var ve ValidationError
	cases := []CreateInput{
		{Subject: "   "},
		{Subject: strings.Repeat("x", 999)},
		{Subject: "ok", Priority: "whenever"},
		{Subject: "ok", RequesterEmail: "not an address"},
		{Subject: "ok", RequesterEmail: "a@b.c\r\nBcc: x@y.z"},
		{Subject: "ok", RequesterName: "bad\x00name"},
	}
	for _, in := range cases {
		if _, err := f.svc.Create(ctx, agentOf(tenantA), in); !errors.As(err, &ve) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	if _, err := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "x", AssigneeID: bob}); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("non-agent assignee: %v", err)
	}
	if _, err := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "x", AssigneeID: "ghost"}); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("unknown assignee: %v", err)
	}
	if _, err := f.svc.Create(ctx, authz.Subjects{}, CreateInput{Subject: "x"}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("tenantless: %v", err)
	}
	// subject with line breaks is flattened
	v, err := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "a\r\nb"})
	if err != nil || v.Subject != "a  b" {
		t.Fatalf("flatten: %q %v", v.Subject, err)
	}
}

func TestPartialUpdate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v, _ := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "Old", Description: "keep me"})
	f.now = f.now.Add(time.Hour)
	u, err := f.svc.Update(ctx, agentOf(tenantA), v.ID, UpdateInput{Subject: ptr("New")})
	if err != nil {
		t.Fatal(err)
	}
	if u.Subject != "New" || u.Description != "keep me" || u.Priority != store.PriorityNormal || !u.UpdatedAt.Equal(f.now) {
		t.Fatalf("partial = %+v", u.Ticket)
	}
	u, err = f.svc.Update(ctx, agentOf(tenantA), v.ID, UpdateInput{Priority: ptr(store.PriorityUrgent)})
	if err != nil || u.Priority != store.PriorityUrgent || u.Subject != "New" {
		t.Fatalf("priority = %+v %v", u.Ticket, err)
	}
	h, _ := f.svc.History(ctx, agentOf(tenantA), v.ID)
	if len(h) != 1 || h[0].Field != store.FieldPriority || h[0].OldValue != "normal" || h[0].NewValue != "urgent" || h[0].ActorKind != store.ActorAgent || h[0].ActorID != agentID {
		t.Fatalf("priority history = %+v", h)
	}
	// unchanged priority writes no history
	if _, err := f.svc.Update(ctx, agentOf(tenantA), v.ID, UpdateInput{Priority: ptr(store.PriorityUrgent)}); err != nil {
		t.Fatal(err)
	}
	if h, _ := f.svc.History(ctx, agentOf(tenantA), v.ID); len(h) != 1 {
		t.Fatalf("no-op priority wrote history: %+v", h)
	}
	var ve ValidationError
	if _, err := f.svc.Update(ctx, agentOf(tenantA), v.ID, UpdateInput{Subject: ptr("")}); !errors.As(err, &ve) {
		t.Fatalf("empty subject: %v", err)
	}
	if _, err := f.svc.Update(ctx, agentOf(tenantA), v.ID, UpdateInput{Priority: ptr("bogus")}); !errors.As(err, &ve) {
		t.Fatalf("bad priority: %v", err)
	}
	if _, err := f.svc.Update(ctx, agentOf(tenantB), v.ID, UpdateInput{Subject: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant update: %v", err)
	}
}

func TestAssignAndUnassign(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v, _ := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "Assign me"})
	a, err := f.svc.Assign(ctx, agentOf(tenantA), v.ID, ada)
	if err != nil {
		t.Fatal(err)
	}
	if a.AssigneeID != ada || a.AssigneeName != "Ada Lovelace" {
		t.Fatalf("assigned = %+v", a.Ticket)
	}
	last := f.pub.Events[len(f.pub.Events)-1]
	if last.Type != events.TicketAssigned || last.Payload.AssigneeID != ada || last.Payload.ActorKind != store.ActorAgent {
		t.Fatalf("assign event = %+v", last)
	}
	u, err := f.svc.Assign(ctx, agentOf(tenantA), v.ID, "")
	if err != nil || u.AssigneeID != "" || u.AssigneeName != "" {
		t.Fatalf("unassigned = %+v %v", u.Ticket, err)
	}
	h, _ := f.svc.History(ctx, agentOf(tenantA), v.ID)
	if len(h) != 2 || h[0].Field != store.FieldAssignee || h[0].OldValue != "" || h[0].NewValue != ada || h[1].OldValue != ada || h[1].NewValue != "" {
		t.Fatalf("assign history = %+v", h)
	}
	if n := len(f.pub.Events); f.pub.Events[n-1].Type != events.TicketAssigned {
		t.Fatalf("unassign event = %v", f.eventTypes())
	}
	// re-unassigning is a no-op
	before := len(f.pub.Events)
	if _, err := f.svc.Assign(ctx, agentOf(tenantA), v.ID, ""); err != nil || len(f.pub.Events) != before {
		t.Fatalf("no-op unassign: %v events=%d", err, len(f.pub.Events)-before)
	}
	if _, err := f.svc.Assign(ctx, agentOf(tenantA), v.ID, bob); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("viewer assignee: %v", err)
	}
	if _, err := f.svc.Assign(ctx, agentOf(tenantA), "missing", ada); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ticket: %v", err)
	}
	f.dir.Err = agents.ErrUnavailable
	if _, err := f.svc.Assign(ctx, agentOf(tenantA), v.ID, ada); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("directory down: %v", err)
	}
	if _, err := f.svc.AssignableUsers(ctx, agentOf(tenantA)); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("assignable down: %v", err)
	}
	f.dir.Err = nil
	users, err := f.svc.AssignableUsers(ctx, agentOf(tenantA))
	if err != nil || len(users) != 1 || users[0].ID != ada {
		t.Fatalf("assignable = %+v %v", users, err)
	}
	if got := f.aud.types(); got[len(got)-1] != string(audit.TicketAssign) {
		t.Fatalf("audit = %v", got)
	}
}

func TestSetStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v, _ := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "Status"})
	f.now = f.now.Add(time.Hour)
	r, err := f.svc.SetStatus(ctx, agentOf(tenantA), v.ID, store.StatusResolved)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != store.StatusResolved || r.ResolvedAt == nil || !r.ResolvedAt.Equal(f.now) {
		t.Fatalf("resolved = %+v", r.Ticket)
	}
	last := f.pub.Events[len(f.pub.Events)-1]
	if last.Type != events.TicketStatusChanged || last.Payload.Status != store.StatusResolved {
		t.Fatalf("status event = %+v", last)
	}
	o, err := f.svc.SetStatus(ctx, agentOf(tenantA), v.ID, store.StatusOpen)
	if err != nil || o.ResolvedAt != nil || o.Status != store.StatusOpen {
		t.Fatalf("reopened = %+v %v", o.Ticket, err)
	}
	h, _ := f.svc.History(ctx, agentOf(tenantA), v.ID)
	if len(h) != 2 || h[0].Field != store.FieldStatus || h[0].OldValue != "open" || h[0].NewValue != "resolved" || h[1].NewValue != "open" {
		t.Fatalf("status history = %+v", h)
	}
	// same status: no history, no event
	before := len(f.pub.Events)
	if _, err := f.svc.SetStatus(ctx, agentOf(tenantA), v.ID, store.StatusOpen); err != nil || len(f.pub.Events) != before {
		t.Fatalf("no-op status: %v", err)
	}
	for _, bad := range []string{"", "unspecified", "done", "OPEN"} {
		if _, err := f.svc.SetStatus(ctx, agentOf(tenantA), v.ID, bad); !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("status %q: %v", bad, err)
		}
	}
	if _, err := f.svc.SetStatus(ctx, agentOf(tenantB), v.ID, store.StatusClosed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant: %v", err)
	}
	// an inbound actor records "inbound", a service actor "system"
	if _, err := f.svc.SetStatus(ctx, authz.SystemFor(tenantA), v.ID, store.StatusPending); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetStatus(ctx, authz.Service(tenantA, "spiffe://example.org/monitor"), v.ID, store.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	h, _ = f.svc.History(ctx, agentOf(tenantA), v.ID)
	if h[2].ActorKind != store.ActorInbound || h[3].ActorKind != store.ActorSystem {
		t.Fatalf("actor kinds = %+v", h[2:])
	}
}

func TestGetAndList(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, _ := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "Printer jam", RequesterName: "Carol"})
	f.now = f.now.Add(time.Minute)
	b, _ := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "VPN down", Priority: store.PriorityUrgent, AssigneeID: ada})
	f.now = f.now.Add(time.Minute)
	_, _ = f.svc.Create(ctx, agentOf(tenantB), CreateInput{Subject: "Other tenant printer"})
	tag := store.Tag{ID: store.NewID(), TenantID: tenantA, Name: "hardware", Kind: store.KindTag}
	if err := f.st.CreateTag(ctx, tag); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetTicketTags(ctx, tenantA, a.ID, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	att := store.Attachment{ID: store.NewID(), TenantID: tenantA, TicketID: a.ID, Filename: "log.txt", ContentType: "text/plain", Size: 3,
		StorageKey: store.AttachmentKey(tenantA, a.ID, "x")}
	if err := f.st.CreateAttachment(ctx, att); err != nil {
		t.Fatal(err)
	}

	g, err := f.svc.Get(ctx, agentOf(tenantA), a.ID)
	if err != nil || len(g.Tags) != 1 || g.Tags[0].Name != "hardware" || len(g.Attachments) != 1 {
		t.Fatalf("get = %+v %v", g, err)
	}
	if _, err := f.svc.Get(ctx, agentOf(tenantB), a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant get: %v", err)
	}

	list := func(fl store.TicketFilter) ([]View, int64) {
		t.Helper()
		items, total, err := f.svc.List(ctx, agentOf(tenantA), fl)
		if err != nil {
			t.Fatal(err)
		}
		return items, total
	}
	items, total := list(store.TicketFilter{})
	if total != 2 || len(items) != 2 || items[0].ID != b.ID || items[1].Tags[0].ID != tag.ID || items[0].Tags == nil {
		t.Fatalf("list = %d %+v", total, items)
	}
	if items, _ := list(store.TicketFilter{Priority: store.PriorityUrgent}); len(items) != 1 || items[0].ID != b.ID {
		t.Fatal("priority filter")
	}
	if items, _ := list(store.TicketFilter{AssigneeID: store.AssigneeNone}); len(items) != 1 || items[0].ID != a.ID {
		t.Fatal("unassigned filter")
	}
	if items, _ := list(store.TicketFilter{AssigneeID: ada}); len(items) != 1 || items[0].ID != b.ID {
		t.Fatal("assignee filter")
	}
	if items, _ := list(store.TicketFilter{TagID: tag.ID}); len(items) != 1 || items[0].ID != a.ID {
		t.Fatal("tag filter")
	}
	if items, _ := list(store.TicketFilter{Query: "carol"}); len(items) != 1 || items[0].ID != a.ID {
		t.Fatal("query filter")
	}
	if items, _ := list(store.TicketFilter{Status: store.StatusOpen}); len(items) != 2 {
		t.Fatal("status filter")
	}
	if items, total := list(store.TicketFilter{Page: 2, PageSize: 1}); total != 2 || len(items) != 1 || items[0].ID != a.ID {
		t.Fatal("paging")
	}
	if _, _, err := f.svc.List(ctx, agentOf(tenantA), store.TicketFilter{Status: "unspecified"}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("bad status filter: %v", err)
	}
	var ve ValidationError
	if _, _, err := f.svc.List(ctx, agentOf(tenantA), store.TicketFilter{Priority: "meh"}); !errors.As(err, &ve) {
		t.Fatalf("bad priority filter: %v", err)
	}
	f.st.FailNext("ListTickets")
	if _, _, err := f.svc.List(ctx, agentOf(tenantA), store.TicketFilter{}); err == nil {
		t.Fatal("store failure swallowed")
	}
}

func TestDeleteRemovesObjects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v, _ := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "With files"})
	keep, _ := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "Other"})
	for i, tk := range []View{v, v, keep} {
		id := store.NewID()
		key := store.AttachmentKey(tenantA, tk.ID, id)
		if _, err := f.blobs.Put(ctx, key, strings.NewReader("data"), 4, "text/plain"); err != nil {
			t.Fatal(err)
		}
		if err := f.st.CreateAttachment(ctx, store.Attachment{ID: id, TenantID: tenantA, TicketID: tk.ID, Filename: "f", ContentType: "text/plain", Size: int64(i), StorageKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.Delete(ctx, agentOf(tenantB), v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant delete: %v", err)
	}
	if err := f.svc.Delete(ctx, agentOf(tenantA), v.ID); err != nil {
		t.Fatal(err)
	}
	if f.blobs.Len() != 1 {
		t.Fatalf("objects left = %d", f.blobs.Len())
	}
	if _, err := f.svc.Get(ctx, agentOf(tenantA), v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted ticket readable: %v", err)
	}
	if got := f.aud.types(); got[len(got)-1] != string(audit.TicketDelete) {
		t.Fatalf("audit = %v", got)
	}
	// an object-store failure does not resurrect the ticket
	id := store.NewID()
	key := store.AttachmentKey(tenantA, keep.ID, id)
	_ = f.st.CreateAttachment(ctx, store.Attachment{ID: id, TenantID: tenantA, TicketID: keep.ID, Filename: "g", ContentType: "text/plain", StorageKey: key})
	f.blobs.FailDelete(errors.New("s3 down"))
	if err := f.svc.Delete(ctx, agentOf(tenantA), keep.ID); err != nil {
		t.Fatalf("blob failure surfaced: %v", err)
	}
	if err := f.svc.Delete(ctx, agentOf(tenantA), keep.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
}

func TestHistoryOfMissingTicket(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.History(context.Background(), agentOf(tenantA), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("history: %v", err)
	}
}

func TestNilDepsAreSafe(t *testing.T) {
	st := memstore.New()
	svc := New(Deps{Store: st})
	v, err := svc.Create(context.Background(), agentOf(tenantA), CreateInput{Subject: "bare"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetStatus(context.Background(), agentOf(tenantA), v.ID, store.StatusClosed); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Assign(context.Background(), agentOf(tenantA), v.ID, ada); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("assign without directory: %v", err)
	}
	if err := svc.Delete(context.Background(), agentOf(tenantA), v.ID); err != nil {
		t.Fatal(err)
	}
}
