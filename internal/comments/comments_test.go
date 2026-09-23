package comments

// T043: internal notes are never sent; public replies need a requester and a
// relay, carry the threading headers and the reference line once, record the
// message id and delivery state, re-open resolved/closed tickets and publish
// events; delete; nothing sensitive in audit.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/go-freya/freya/services/ticket/internal/agents"
	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/blob"
	"github.com/go-freya/freya/services/ticket/internal/events"
	"github.com/go-freya/freya/services/ticket/internal/mailer"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/thread"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

const (
	tenantA   = "11111111-1111-7111-8111-111111111111"
	tenantB   = "22222222-2222-7222-8222-222222222222"
	agentA    = "55555555-5555-7555-8555-555555555555"
	requester = "jane@customer.example"
)

type auditRec struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *auditRec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *auditRec) last(t audit.EventType) (audit.Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].EventType == t {
			return r.events[i], true
		}
	}
	return audit.Event{}, false
}

type env struct {
	svc   *Service
	st    *memstore.Mem
	mail  *mailer.Fake
	pub   *events.Recorder
	audit *auditRec
	blobs *blob.Fake
	tk    *tickets.Service
	mb    store.Mailbox
}

func agent() authz.Subjects {
	return authz.Subjects{TenantID: tenantA, UserID: agentA, ActorKind: authz.ActorAgent}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{st: memstore.New(), mail: &mailer.Fake{}, pub: &events.Recorder{}, audit: &auditRec{}, blobs: blob.NewFake()}
	dir := agents.NewFake()
	dir.Add(tenantA, agents.User{ID: agentA, Name: "Ada Agent"}, true)
	e.tk = tickets.New(tickets.Deps{Store: e.st, Agents: dir, Events: e.pub, Audit: e.audit, Blobs: e.blobs})
	e.svc = New(Deps{Store: e.st, Mailer: e.mail, Tickets: e.tk, Agents: dir, Events: e.pub, Audit: e.audit, Blobs: e.blobs, MailDomain: "tickets.acme.example"})
	e.mb = store.Mailbox{ID: store.NewID(), TenantID: tenantA, Address: "support@acme.example", DisplayName: "Acme Support", Active: true}
	if err := e.st.CreateMailbox(context.Background(), e.mb); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) emailTicket(t *testing.T, status string) store.Ticket {
	t.Helper()
	tk := store.Ticket{ID: store.NewID(), TenantID: tenantA, Subject: "Printer on floor 3 is jammed", Status: status, Priority: store.PriorityNormal,
		Source: store.SourceEmail, RequesterEmail: requester, RequesterName: "Jane Customer", Recipient: e.mb.Address, MailboxID: e.mb.ID,
		ExternalID: "plain-0001@mail.customer.example", CreatedBy: store.CreatedByInbound}
	if err := e.st.CreateTicket(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestInternalNoteNeverSent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.emailTicket(t, store.StatusResolved)
	c, err := e.svc.AddNote(ctx, agent(), tk.ID, "  Called the customer back.  ")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Internal || c.AuthorKind != store.AuthorAgent || c.AuthorID != agentA || c.AuthorName != "Ada Agent" || c.MessageID != "" ||
		c.Delivery != store.DeliveryNone || c.Body != "Called the customer back." {
		t.Fatalf("note = %+v", c)
	}
	if len(e.mail.Messages()) != 0 {
		t.Fatal("internal note was emailed")
	}
	got, _ := e.st.GetTicket(ctx, tenantA, tk.ID)
	if got.CommentCount != 1 || got.Status != store.StatusResolved || got.LastMessageID != "" {
		t.Fatalf("ticket after note = %+v", got)
	}
	if len(e.pub.Events) != 1 || e.pub.Events[0].Type != events.TicketCommented {
		t.Fatalf("events = %+v", e.pub.Events)
	}
	ev, ok := e.audit.last(audit.CommentCreate)
	if !ok || ev.Details["internal"] != true || ev.SubjectKind != audit.SubjectComment {
		t.Fatalf("audit = %+v", ev)
	}
	list, err := e.svc.List(ctx, agent(), tk.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
}

func TestReplyThreadingAndDelivery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.emailTicket(t, store.StatusOpen)
	c, err := e.svc.Reply(ctx, agent(), tk.ID, "We replaced the roller.")
	if err != nil {
		t.Fatal(err)
	}
	msgs := e.mail.Messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d", len(msgs))
	}
	m := msgs[0]
	if m.From != "support@acme.example" || m.FromName != "Acme Support" || m.To != requester || m.ToName != "Jane Customer" ||
		m.Subject != "Re: Printer on floor 3 is jammed" || m.AutoReply {
		t.Fatalf("mail = %+v", m)
	}
	if strings.Count(m.Text, thread.ReferenceLine(tk.ID)) != 1 || !strings.HasPrefix(m.Text, "We replaced the roller.") {
		t.Fatalf("text = %q", m.Text)
	}
	if !strings.HasPrefix(m.MessageID, "ticket."+tk.ID+".") || !strings.HasSuffix(m.MessageID, "@acme.example") ||
		m.InReplyTo != tk.ExternalID || len(m.References) != 1 || m.References[0] != tk.ExternalID {
		t.Fatalf("threading = %+v", m)
	}
	if c.Internal || c.MessageID != m.MessageID || c.Delivery != store.DeliverySent || c.AuthorKind != store.AuthorAgent || c.Body != "We replaced the roller." {
		t.Fatalf("comment = %+v", c)
	}
	got, _ := e.st.GetTicket(ctx, tenantA, tk.ID)
	if got.LastMessageID != m.MessageID || got.CommentCount != 1 {
		t.Fatalf("ticket = %+v", got)
	}
	ev, ok := e.audit.last(audit.ReplySent)
	if !ok || ev.SubjectID != c.ID {
		t.Fatalf("audit = %+v", ev)
	}
	b, _ := json.Marshal(ev.Details)
	for _, pii := range []string{requester, "Jane", "roller"} {
		if strings.Contains(string(b), pii) {
			t.Fatalf("audit leaks %q: %s", pii, b)
		}
	}

	// a second reply threads onto the first
	if _, err := e.svc.Reply(ctx, agent(), tk.ID, "Anything else?"); err != nil {
		t.Fatal(err)
	}
	m2 := e.mail.Messages()[1]
	if m2.InReplyTo != m.MessageID || len(m2.References) != 2 || m2.References[0] != tk.ExternalID || m2.References[1] != m.MessageID {
		t.Fatalf("second reply threading = %+v", m2)
	}
}

func TestReplyReopensResolvedAndClosed(t *testing.T) {
	for _, status := range []string{store.StatusResolved, store.StatusClosed} {
		e := newEnv(t)
		ctx := context.Background()
		tk := e.emailTicket(t, status)
		if _, err := e.svc.Reply(ctx, agent(), tk.ID, "Reopening."); err != nil {
			t.Fatal(err)
		}
		got, _ := e.st.GetTicket(ctx, tenantA, tk.ID)
		if got.Status != store.StatusOpen {
			t.Fatalf("%s not re-opened: %s", status, got.Status)
		}
		hist, _ := e.st.ListHistory(ctx, tenantA, tk.ID)
		if len(hist) != 1 || hist[0].ActorKind != store.ActorAgent || hist[0].NewValue != store.StatusOpen {
			t.Fatalf("history = %+v", hist)
		}
		var commented, status bool
		for _, ev := range e.pub.Events {
			commented = commented || ev.Type == events.TicketCommented
			status = status || ev.Type == events.TicketStatusChanged
		}
		if !commented || !status {
			t.Fatalf("events = %+v", e.pub.Events)
		}
	}
}

func TestReplyUnavailable(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	// no requester address
	manual := store.Ticket{ID: store.NewID(), TenantID: tenantA, Subject: "manual", Status: store.StatusOpen, Priority: store.PriorityNormal, Source: store.SourceManual}
	_ = e.st.CreateTicket(ctx, manual)
	if _, err := e.svc.Reply(ctx, agent(), manual.ID, "hi"); !errors.Is(err, ErrReplyUnavailable) {
		t.Fatalf("no requester: %v", err)
	}
	// relay disabled / not wired
	tk := e.emailTicket(t, store.StatusOpen)
	e.mail.Disabled = true
	if _, err := e.svc.Reply(ctx, agent(), tk.ID, "hi"); !errors.Is(err, ErrReplyUnavailable) {
		t.Fatalf("relay disabled: %v", err)
	}
	e.mail.Disabled = false
	noMail := New(Deps{Store: e.st, Tickets: e.tk})
	if _, err := noMail.Reply(ctx, agent(), tk.ID, "hi"); !errors.Is(err, ErrReplyUnavailable) {
		t.Fatalf("no mailer: %v", err)
	}
	// no mailbox to send from
	e2 := newEnv(t)
	_ = e2.st.DeleteMailbox(ctx, tenantA, e2.mb.ID, true)
	tk2 := store.Ticket{ID: store.NewID(), TenantID: tenantA, Subject: "x", Status: store.StatusOpen, Priority: store.PriorityNormal,
		Source: store.SourceManual, RequesterEmail: requester}
	_ = e2.st.CreateTicket(ctx, tk2)
	if _, err := e2.svc.Reply(ctx, agent(), tk2.ID, "hi"); !errors.Is(err, ErrReplyUnavailable) {
		t.Fatalf("no mailbox: %v", err)
	}
	if cs, _ := e.st.ListComments(ctx, tenantA, tk.ID); len(cs) != 0 {
		t.Fatal("refused reply stored a comment")
	}
	if len(e.mail.Messages()) != 0 {
		t.Fatal("refused reply sent mail")
	}
}

func TestManualTicketRepliesFromTenantMailbox(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := store.Ticket{ID: store.NewID(), TenantID: tenantA, Subject: "Phone call", Status: store.StatusOpen, Priority: store.PriorityNormal,
		Source: store.SourceManual, RequesterEmail: requester}
	_ = e.st.CreateTicket(ctx, tk)
	if _, err := e.svc.Reply(ctx, agent(), tk.ID, "Following up by email."); err != nil {
		t.Fatal(err)
	}
	m := e.mail.Messages()[0]
	if m.From != e.mb.Address || m.InReplyTo != "" || len(m.References) != 0 {
		t.Fatalf("manual reply = %+v", m)
	}
}

func TestDeliveryFailureRecorded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.emailTicket(t, store.StatusResolved)
	e.mail.Err = errors.New("relay down")
	c, err := e.svc.Reply(ctx, agent(), tk.ID, "Hello?")
	if !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("err = %v", err)
	}
	if c.ID == "" || c.Delivery != store.DeliveryFailed {
		t.Fatalf("comment = %+v", c)
	}
	stored, _ := e.st.GetComment(ctx, tenantA, c.ID)
	if stored.Delivery != store.DeliveryFailed {
		t.Fatalf("stored = %+v", stored)
	}
	if got, _ := e.st.GetTicket(ctx, tenantA, tk.ID); got.Status != store.StatusResolved {
		t.Fatal("undelivered reply re-opened the ticket")
	}
	if _, ok := e.audit.last(audit.ReplyFailed); !ok {
		t.Fatal("failure not audited")
	}
}

func TestHeaderInjectionRefusesReply(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.emailTicket(t, store.StatusOpen)
	bad := tk
	bad.ID, bad.ExternalID, bad.RequesterName = store.NewID(), "", "Eve\r\nBcc: attacker@evil.example"
	_ = e.st.CreateTicket(ctx, bad)
	if _, err := e.svc.Reply(ctx, agent(), bad.ID, "hi"); !errors.Is(err, ErrUnsafeHeader) {
		t.Fatalf("injection: %v", err)
	}
	if cs, _ := e.st.ListComments(ctx, tenantA, bad.ID); len(cs) != 0 || len(e.mail.Messages()) != 0 {
		t.Fatal("unsafe reply stored or sent")
	}
}

func TestValidationAndScope(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.emailTicket(t, store.StatusOpen)
	var ve ValidationError
	for _, body := range []string{"", "   \n ", "nul\x00", strings.Repeat("x", MaxBody+1)} {
		if _, err := e.svc.AddNote(ctx, agent(), tk.ID, body); !errors.As(err, &ve) {
			t.Errorf("note %q: %v", body[:min(len(body), 10)], err)
		}
		if _, err := e.svc.Reply(ctx, agent(), tk.ID, body); !errors.As(err, &ve) {
			t.Errorf("reply %q: %v", body[:min(len(body), 10)], err)
		}
	}
	other := authz.Subjects{TenantID: tenantB, UserID: "u", ActorKind: authz.ActorAgent}
	if _, err := e.svc.AddNote(ctx, other, tk.ID, "x"); !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("cross-tenant note: %v", err)
	}
	if _, err := e.svc.List(ctx, other, tk.ID); !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("cross-tenant list: %v", err)
	}
	if _, err := e.svc.Reply(ctx, other, tk.ID, "x"); !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("cross-tenant reply: %v", err)
	}
	if _, err := e.svc.List(ctx, authz.Subjects{}, tk.ID); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("no tenant: %v", err)
	}
}

func TestDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.emailTicket(t, store.StatusOpen)
	c, _ := e.svc.AddNote(ctx, agent(), tk.ID, "temp")
	att := store.Attachment{ID: store.NewID(), TenantID: tenantA, TicketID: tk.ID, CommentID: c.ID, Filename: "a.txt",
		ContentType: "text/plain", Size: 1, StorageKey: store.AttachmentKey(tenantA, tk.ID, "x")}
	_, _ = e.blobs.Put(ctx, att.StorageKey, strings.NewReader("x"), 1, "text/plain")
	_ = e.st.CreateAttachment(ctx, att)
	other := authz.Subjects{TenantID: tenantB, UserID: "u", ActorKind: authz.ActorAgent}
	if err := e.svc.Delete(ctx, other, c.ID); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("cross-tenant delete: %v", err)
	}
	if err := e.svc.Delete(ctx, agent(), c.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.GetTicket(ctx, tenantA, tk.ID); got.CommentCount != 0 || e.blobs.Len() != 0 {
		t.Fatalf("after delete: count=%d objects=%d", got.CommentCount, e.blobs.Len())
	}
	if err := e.svc.Delete(ctx, agent(), c.ID); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, ok := e.audit.last(audit.CommentDelete); !ok {
		t.Fatal("delete not audited")
	}
}

func TestAddFromService(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.emailTicket(t, store.StatusOpen)
	svc := authz.Service(tenantA, "spiffe://example.org/svc/monitor")
	c, err := e.svc.Add(ctx, svc, tk.ID, "Disk alert cleared.", true)
	if err != nil || !c.Internal || c.AuthorKind != store.AuthorSystem || c.AuthorID != "spiffe://example.org/svc/monitor" {
		t.Fatalf("service note = %+v %v", c, err)
	}
	c, err = e.svc.Add(ctx, svc, tk.ID, "Your issue is fixed.", false)
	if err != nil || c.Internal || c.Delivery != store.DeliverySent || len(e.mail.Messages()) != 1 {
		t.Fatalf("service reply = %+v %v", c, err)
	}
}

func TestStoreFailures(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.emailTicket(t, store.StatusOpen)
	e.st.FailNext("CreateComment")
	if _, err := e.svc.AddNote(ctx, agent(), tk.ID, "x"); err == nil {
		t.Fatal("create failure swallowed")
	}
	e.st.FailNext("CreateComment")
	if _, err := e.svc.Reply(ctx, agent(), tk.ID, "x"); err == nil || len(e.mail.Messages()) != 0 {
		t.Fatal("reply sent although its comment could not be recorded")
	}
	e.st.FailNext("ListComments")
	if _, err := e.svc.List(ctx, agent(), tk.ID); err == nil {
		t.Fatal("list failure swallowed")
	}
	e.st.FailNext("DeleteComment")
	c, _ := e.svc.AddNote(ctx, agent(), tk.ID, "y")
	if err := e.svc.Delete(ctx, agent(), c.ID); err == nil {
		t.Fatal("delete failure swallowed")
	}
}
