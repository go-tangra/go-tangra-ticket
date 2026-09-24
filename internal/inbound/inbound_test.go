package inbound

// T035: the inbound mail handler behind the edge listener — authentication,
// generic refusals, mailbox routing, dedup, threading, attachments, storage
// failures, audit, metrics and PII-free logs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-tangra/go-tangra/v4/observe"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/audit"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/blob"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/events"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailparse"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/metrics"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/secrets"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tickets"
)

const (
	tenantA   = "11111111-1111-7111-8111-111111111111"
	tenantB   = "22222222-2222-7222-8222-222222222222"
	relay     = "relay-secret-token-value"
	support   = "support@acme.example"
	maxBody   = 64 << 10
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

func (r *auditRec) of(t audit.EventType) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, e := range r.events {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

type env struct {
	t      *testing.T
	srv    http.Handler
	st     *memstore.Mem
	blobs  *blob.Fake
	sec    *secrets.Fake
	audit  *auditRec
	pub    *events.Recorder
	fm     *observe.Metrics
	tk     *tickets.Service
	logs   *bytes.Buffer
	triage *fakeTriage
}

type fakeTriage struct {
	drop     bool
	err      error
	calls    int
	applied  []string
	applyErr error
}

func (f *fakeTriage) Evaluate(_ context.Context, subj authz.Subjects, in TriageInput) (Plan, error) {
	f.calls++
	if subj.ActorKind != authz.ActorInbound || in.Message == nil || in.Recipient == "" {
		return Plan{}, errors.New("bad triage input")
	}
	if f.err != nil {
		return Plan{}, f.err
	}
	return Plan{Drop: f.drop, Apply: func(_ context.Context, ticketID string) error {
		f.applied = append(f.applied, ticketID)
		return f.applyErr
	}}, nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, st: memstore.New(), blobs: blob.NewFake(), sec: &secrets.Fake{Relay: relay}, audit: &auditRec{},
		pub: &events.Recorder{}, logs: &bytes.Buffer{}}
	fm, err := observe.NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	e.fm = fm
	m, err := metrics.New(fm.Meter(metrics.Scope))
	if err != nil {
		t.Fatal(err)
	}
	for _, mb := range []store.Mailbox{
		{ID: store.NewID(), TenantID: tenantA, Address: support, DisplayName: "Acme Support", Active: true},
		{ID: store.NewID(), TenantID: tenantA, Address: "old@acme.example", DisplayName: "Old", Active: false},
		{ID: store.NewID(), TenantID: tenantB, Address: "help@beta.example", DisplayName: "Beta", Active: true},
	} {
		if err := e.st.CreateMailbox(ctx, mb); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.tk = tickets.New(tickets.Deps{Store: e.st, Events: e.pub, Audit: e.audit, Metrics: m, Blobs: e.blobs, Log: log})
	lim := mailparse.DefaultLimits()
	lim.MaxBodyBytes = maxBody
	h := NewHandler(Deps{Store: e.st, Blobs: e.blobs, Secrets: e.sec, Tickets: e.tk, Audit: e.audit, Events: e.pub,
		Metrics: m, Log: log, Limits: lim})
	e.srv = NewServer(ServerConfig{MaxBodyBytes: maxBody}, h, log).Handler()
	return e
}

func (e *env) withTriage(f *fakeTriage) *env {
	e.t.Helper()
	m, _ := metrics.New(e.fm.Meter(metrics.Scope))
	lim := mailparse.DefaultLimits()
	lim.MaxBodyBytes = maxBody
	log := slog.New(slog.NewJSONHandler(e.logs, nil))
	h := NewHandler(Deps{Store: e.st, Blobs: e.blobs, Secrets: e.sec, Tickets: e.tk, Audit: e.audit, Events: e.pub,
		Metrics: m, Log: log, Limits: lim, Triage: f})
	e.srv = NewServer(ServerConfig{MaxBodyBytes: maxBody}, h, log).Handler()
	e.triage = f
	return e
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mail", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type result struct {
	Outcome  string `json:"outcome"`
	TicketID string `json:"ticket_id"`
}

func (e *env) post(raw []byte, hdr map[string]string, target ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	path := PathMail
	if len(target) > 0 {
		path = target[0]
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "message/rfc822")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	return w
}

func auth(extra ...string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + relay, "X-Iris-Recipient": support}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "" {
			delete(h, extra[i])
			continue
		}
		h[extra[i]] = extra[i+1]
	}
	return h
}

func (e *env) accept(raw []byte, hdr map[string]string, want string) result {
	e.t.Helper()
	w := e.post(raw, hdr)
	if w.Code != http.StatusAccepted {
		e.t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	var res result
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		e.t.Fatal(err)
	}
	if res.Outcome != want {
		e.t.Fatalf("outcome = %q want %q (%s)", res.Outcome, want, w.Body)
	}
	return res
}

func (e *env) tickets(tenant string) []store.Ticket {
	e.t.Helper()
	all, err := e.st.AllTickets(context.Background(), tenant)
	if err != nil {
		e.t.Fatal(err)
	}
	return all
}

func (e *env) metric(line string) bool {
	rec := httptest.NewRecorder()
	e.fm.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return strings.Contains(rec.Body.String(), line)
}

func TestRefusalsAreGenericAndStoreNothing(t *testing.T) {
	e := newEnv(t)
	raw := fixture(t, "plain.eml")
	cases := []struct {
		name string
		hdr  map[string]string
		path string
		code int
	}{
		{"missing token", auth("Authorization", ""), "", 401},
		{"wrong token", auth("Authorization", "Bearer wrong"), "", 401},
		{"wrong token same length", auth("Authorization", "Bearer "+strings.Repeat("x", len(relay))), "", 401},
		{"basic scheme", auth("Authorization", "Basic "+relay), "", 401},
		{"query token ignored", auth("Authorization", ""), PathMail + "?token=" + relay, 401},
		{"unknown mailbox", auth("X-Iris-Recipient", "nobody@acme.example"), "", 404},
		{"inactive mailbox", auth("X-Iris-Recipient", "old@acme.example"), "", 404},
		{"garbage recipient", auth("X-Iris-Recipient", "not an address\r\n"), "", 404},
	}
	for _, c := range cases {
		var w *httptest.ResponseRecorder
		if c.path != "" {
			w = e.post(raw, c.hdr, c.path)
		} else {
			w = e.post(raw, c.hdr)
		}
		if w.Code != c.code || strings.TrimSpace(w.Body.String()) != `{"outcome":"refused"}` {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	// oversized body (declared and streamed)
	big := append(append([]byte{}, raw...), bytes.Repeat([]byte("x"), maxBody)...)
	if w := e.post(big, auth()); w.Code != 413 || strings.TrimSpace(w.Body.String()) != `{"outcome":"refused"}` {
		t.Errorf("oversized: %d %s", w.Code, w.Body)
	}
	if n := len(e.tickets(tenantA)) + len(e.tickets(tenantB)); n != 0 || e.blobs.Len() != 0 {
		t.Fatalf("stored %d tickets / %d objects on refusal", n, e.blobs.Len())
	}
	refused := e.audit.of(audit.InboundRefused)
	if len(refused) < len(cases) {
		t.Fatalf("refusals audited = %d", len(refused))
	}
	for _, ev := range refused {
		if ev.TenantID != audit.NilTenant || ev.Outcome != audit.OutcomeRefused {
			t.Errorf("refusal audit = %+v", ev)
		}
	}
	if !e.metric(`ticket_inbound_total{outcome="refused"}`) {
		t.Error("refusal metric missing")
	}
	// the relay token is never logged
	if strings.Contains(e.logs.String(), relay) {
		t.Fatal("relay token in logs")
	}
}

func TestTokenUnavailableAsksRelayToRetry(t *testing.T) {
	e := newEnv(t)
	e.sec.Err = secrets.ErrUnavailable
	if w := e.post(fixture(t, "plain.eml"), auth()); w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"outcome":"refused"}` {
		t.Fatalf("secret store down: %d %s", w.Code, w.Body)
	}
	e.sec.Err = nil
	e.sec.SetRelay("")
	if w := e.post(fixture(t, "plain.eml"), auth()); w.Code != 503 {
		t.Fatalf("unconfigured token: %d", w.Code)
	}
}

func TestCreatedWithAttachments(t *testing.T) {
	e := newEnv(t)
	res := e.accept(fixture(t, "attachments.eml"), auth("Authorization", "", "X-Ticket-Token", relay), "created")
	list := e.tickets(tenantA)
	if len(list) != 1 || list[0].ID != res.TicketID {
		t.Fatalf("tickets = %+v", list)
	}
	tk := list[0]
	if tk.Subject != "Invoice and photo attached" || tk.RequesterEmail != requester || tk.RequesterName != "Jane Customer" ||
		tk.Recipient != support || tk.Source != store.SourceEmail || tk.ExternalID != "attach-0001@mail.customer.example" ||
		tk.MailboxID == "" || tk.CreatedBy != store.CreatedByInbound || tk.Status != store.StatusOpen ||
		!strings.HasPrefix(tk.Description, "Please find the invoice") {
		t.Fatalf("ticket = %+v", tk)
	}
	atts, _ := e.st.ListAttachments(context.Background(), tenantA, tk.ID)
	if len(atts) != 2 || e.blobs.Len() != 2 {
		t.Fatalf("attachments = %+v objects=%d", atts, e.blobs.Len())
	}
	names := map[string]bool{}
	for _, a := range atts {
		names[a.Filename] = true
		if a.StorageKey != store.AttachmentKey(tenantA, tk.ID, a.ID) || a.Checksum == "" || a.Size == 0 || a.CommentID != "" {
			t.Errorf("attachment = %+v", a)
		}
		rc, err := e.blobs.Get(context.Background(), a.StorageKey)
		if err != nil {
			t.Fatal(err)
		}
		_ = rc.Close()
	}
	if !names["invoice.pdf"] || !names["device photo.png"] {
		t.Fatalf("names = %v", names)
	}
	if len(e.pub.Events) != 1 || e.pub.Events[0].Type != events.TicketCreated || e.pub.Events[0].Payload.ActorKind != store.ActorInbound {
		t.Fatalf("events = %+v", e.pub.Events)
	}
	created := e.audit.of(audit.InboundCreated)
	if len(created) != 1 || created[0].TenantID != tenantA || created[0].ActorKind != audit.ActorInbound || created[0].SubjectID != tk.ID {
		t.Fatalf("audit = %+v", created)
	}
	if !e.metric(`ticket_inbound_total{outcome="created"} 1`) {
		t.Error("created metric")
	}
	for _, pii := range []string{requester, "Jane Customer", "Please find the invoice", "Invoice and photo"} {
		if strings.Contains(e.logs.String(), pii) {
			t.Errorf("log carries %q", pii)
		}
		if b, _ := json.Marshal(created[0].Details); strings.Contains(string(b), pii) {
			t.Errorf("audit carries %q", pii)
		}
	}
}

func TestInlineHTMLAndRecipientFallbacks(t *testing.T) {
	e := newEnv(t)
	// recipient from X-Ticket-Recipient
	e.accept(fixture(t, "html-inline-image.eml"), auth("X-Iris-Recipient", "", "X-Ticket-Recipient", "Support@Acme.Example"), "created")
	tk := e.tickets(tenantA)[0]
	if !tk.HasHTML() || !strings.Contains(tk.BodyHTML, "cid:shot1@customer.example") {
		t.Fatalf("html = %q", tk.BodyHTML)
	}
	atts, _ := e.st.ListAttachments(context.Background(), tenantA, tk.ID)
	if len(atts) != 1 || !atts[0].Inline || atts[0].ContentID != "shot1@customer.example" || atts[0].ContentType != "image/png" {
		t.Fatalf("inline = %+v", atts)
	}
	// recipient from the To header when the relay sends none
	e.accept(fixture(t, "plain.eml"), auth("X-Iris-Recipient", ""), "created")
	// X-Iris-Message-Id overrides the header
	res := e.accept(fixture(t, "quoted-printable.eml"), auth("X-Iris-Message-Id", "<relay-id@kumo>"), "created")
	got, _ := e.st.GetTicket(context.Background(), tenantA, res.TicketID)
	if got.ExternalID != "relay-id@kumo" {
		t.Fatalf("external id = %q", got.ExternalID)
	}
}

func TestDuplicateDelivery(t *testing.T) {
	e := newEnv(t)
	first := e.accept(fixture(t, "plain.eml"), auth(), "created")
	second := e.accept(fixture(t, "plain.eml"), auth(), "duplicate")
	if second.TicketID != first.TicketID || len(e.tickets(tenantA)) != 1 {
		t.Fatalf("duplicate stored: %+v", second)
	}
	if len(e.audit.of(audit.InboundDuplicate)) != 1 || !e.metric(`ticket_inbound_total{outcome="duplicate"} 1`) {
		t.Fatal("duplicate not audited/counted")
	}
	// the same message id delivered to another tenant's mailbox is a separate ticket there
	e.accept(fixture(t, "plain.eml"), auth("X-Iris-Recipient", "help@beta.example"), "created")
	if len(e.tickets(tenantB)) != 1 {
		t.Fatal("tenant B ticket missing")
	}
}

func TestThreadedReplyReopens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	orig := e.accept(fixture(t, "plain.eml"), auth(), "created")
	if _, err := e.tk.SetStatus(ctx, authz.Internal(tenantA), orig.TicketID, store.StatusResolved); err != nil {
		t.Fatal(err)
	}
	e.pub.Events = nil
	res := e.accept(fixture(t, "reply-in-reply-to.eml"), auth(), "threaded")
	if res.TicketID != orig.TicketID || len(e.tickets(tenantA)) != 1 {
		t.Fatalf("threaded onto %s", res.TicketID)
	}
	cs, _ := e.st.ListComments(ctx, tenantA, orig.TicketID)
	if len(cs) != 1 {
		t.Fatalf("comments = %+v", cs)
	}
	c := cs[0]
	if c.AuthorKind != store.AuthorRequester || c.Internal || c.AuthorEmail != requester || c.AuthorName != "Jane Customer" ||
		c.MessageID != "reply-0001@mail.customer.example" || !strings.HasPrefix(c.Body, "It is still jamming") {
		t.Fatalf("comment = %+v", c)
	}
	tk, _ := e.st.GetTicket(ctx, tenantA, orig.TicketID)
	if tk.Status != store.StatusOpen || tk.ResolvedAt != nil || tk.CommentCount != 1 {
		t.Fatalf("not re-opened: %+v", tk)
	}
	hist, _ := e.st.ListHistory(ctx, tenantA, orig.TicketID)
	last := hist[len(hist)-1]
	if last.Field != store.FieldStatus || last.NewValue != store.StatusOpen || last.ActorKind != store.ActorInbound {
		t.Fatalf("history = %+v", last)
	}
	var replied bool
	for _, ev := range e.pub.Events {
		if ev.Type == events.TicketRequesterReplied && ev.Payload.TicketID == orig.TicketID {
			replied = true
		}
	}
	if !replied {
		t.Fatalf("events = %+v", e.pub.Events)
	}
	if len(e.audit.of(audit.InboundThreaded)) != 1 || !e.metric(`ticket_inbound_total{outcome="threaded"} 1`) {
		t.Fatal("threaded not audited/counted")
	}
	// the same reply again is a duplicate
	e.accept(fixture(t, "reply-in-reply-to.eml"), auth(), "duplicate")
}

func tokenReply(ticketID string) []byte {
	return []byte(strings.ReplaceAll(string(fixtureRaw("reply-token-only.eml")), "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55", ticketID))
}

func fixtureRaw(name string) []byte {
	raw, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "mail", name))
	return raw
}

func TestTokenReplyTenantConfined(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.accept(fixture(t, "plain.eml"), auth(), "created")
	e.accept(tokenReply(a.TicketID), auth(), "threaded")

	// tenant B's ticket referenced from mail to tenant A's mailbox opens a new
	// ticket in tenant A and never touches tenant B.
	b := e.accept(fixture(t, "attachments.eml"), auth("X-Iris-Recipient", "help@beta.example"), "created")
	raw := []byte(strings.ReplaceAll(string(tokenReply(b.TicketID)), "token-0001@", "token-0002@"))
	res := e.accept(raw, auth(), "created")
	if res.TicketID == b.TicketID {
		t.Fatal("cross-tenant thread")
	}
	if tb, _ := e.st.GetTicket(ctx, tenantB, b.TicketID); tb.CommentCount != 0 {
		t.Fatal("tenant B ticket was touched")
	}
	if got, err := e.st.GetTicket(ctx, tenantA, res.TicketID); err != nil || got.TenantID != tenantA {
		t.Fatalf("new ticket not in routed tenant: %+v %v", got, err)
	}
}

func TestReplyToDeletedTicketOpensNew(t *testing.T) {
	e := newEnv(t)
	orig := e.accept(fixture(t, "plain.eml"), auth(), "created")
	if err := e.tk.Delete(context.Background(), authz.Internal(tenantA), orig.TicketID); err != nil {
		t.Fatal(err)
	}
	res := e.accept(fixture(t, "reply-in-reply-to.eml"), auth(), "created")
	if res.TicketID == orig.TicketID || len(e.tickets(tenantA)) != 1 {
		t.Fatal("reply to deleted ticket not opened as new")
	}
}

func TestStorageFailuresAskRelayToRetry(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(e *env)
	}{
		{"routing store down", func(e *env) { e.st.FailNext("RouteMailbox") }},
		{"dedup store down", func(e *env) { e.st.FailNext("FindCommentByMessageID") }},
		{"object store down", func(e *env) { e.blobs.FailPut(errors.New("s3 down")) }},
		{"ticket insert fails", func(e *env) { e.st.FailNext("CreateTicket") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e)
			if w := e.post(fixture(t, "attachments.eml"), auth()); w.Code != 503 {
				t.Fatalf("status = %d %s", w.Code, w.Body)
			}
			if len(e.tickets(tenantA)) != 0 || e.blobs.Len() != 0 {
				t.Fatalf("partial state: %d tickets %d objects", len(e.tickets(tenantA)), e.blobs.Len())
			}
			if !e.metric(`ticket_inbound_total{outcome="error"}`) {
				t.Error("error metric")
			}
			// the retry succeeds
			e.accept(fixture(t, "attachments.eml"), auth(), "created")
		})
	}
	// a reply whose comment insert fails is retried too (no partial comment)
	e := newEnv(t)
	e.accept(fixture(t, "plain.eml"), auth(), "created")
	e.st.FailNext("CreateComment")
	if w := e.post(fixture(t, "reply-in-reply-to.eml"), auth()); w.Code != 503 {
		t.Fatalf("reply failure = %d", w.Code)
	}
	e.accept(fixture(t, "reply-in-reply-to.eml"), auth(), "threaded")
}

func TestOversizedAttachmentSkippedAndNoted(t *testing.T) {
	e := newEnv(t)
	m, _ := metrics.New(e.fm.Meter(metrics.Scope))
	lim := mailparse.DefaultLimits()
	lim.MaxBodyBytes = maxBody
	lim.MaxAttachmentBytes = 64
	h := NewHandler(Deps{Store: e.st, Blobs: e.blobs, Secrets: e.sec, Tickets: e.tk, Audit: e.audit, Metrics: m, Limits: lim})
	e.srv = NewServer(ServerConfig{MaxBodyBytes: maxBody}, h, nil).Handler()
	raw := "From: jane@customer.example\r\nTo: support@acme.example\r\nSubject: big\r\nMessage-ID: <big@x>\r\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nsee file\r\n" +
		"--B\r\nContent-Type: application/zip\r\nContent-Disposition: attachment; filename=\"big.zip\"\r\n\r\n" +
		strings.Repeat("Z", 200) + "\r\n--B--\r\n"
	res := e.accept([]byte(raw), auth(), "created")
	atts, _ := e.st.ListAttachments(context.Background(), tenantA, res.TicketID)
	cs, _ := e.st.ListComments(context.Background(), tenantA, res.TicketID)
	if len(atts) != 0 || len(cs) != 1 || !cs[0].Internal || cs[0].AuthorKind != store.AuthorSystem || !strings.Contains(cs[0].Body, "big.zip") {
		t.Fatalf("skip note: atts=%+v comments=%+v", atts, cs)
	}
}

func TestTriageHook(t *testing.T) {
	e := newEnv(t).withTriage(&fakeTriage{})
	created := e.accept(fixture(t, "plain.eml"), auth(), "created")
	if e.triage.calls != 1 || len(e.triage.applied) != 1 || e.triage.applied[0] != created.TicketID {
		t.Fatalf("triage = %+v", e.triage)
	}
	// replies never run rules
	e.accept(fixture(t, "reply-in-reply-to.eml"), auth(), "threaded")
	if e.triage.calls != 1 {
		t.Fatal("rules ran on a reply")
	}
	// drop discards the message
	e.triage.drop = true
	e.accept(fixture(t, "spam-high-score.eml"), auth(), "dropped")
	if len(e.tickets(tenantA)) != 1 || len(e.audit.of(audit.InboundDropped)) != 1 || !e.metric(`ticket_inbound_total{outcome="dropped"} 1`) {
		t.Fatal("drop not honoured / audited")
	}
	// an erroring triage never blocks ingestion; a failing apply neither
	e.triage.drop, e.triage.err = false, errors.New("cel broke")
	e.accept(fixture(t, "bulk-precedence.eml"), auth(), "created")
	e.triage.err, e.triage.applyErr = nil, errors.New("apply broke")
	e.accept(fixture(t, "noreply-sender.eml"), auth(), "created")
}

func TestUnparseableAndHeaderlessMail(t *testing.T) {
	e := newEnv(t)
	res := e.accept([]byte("no headers at all, just words"), auth(), "created")
	tk, _ := e.st.GetTicket(context.Background(), tenantA, res.TicketID)
	if tk.Subject != mailparse.NoSubject || tk.ExternalID != "" || tk.RequesterEmail != "" {
		t.Fatalf("ticket = %+v", tk)
	}
	// mail without a message id cannot be de-duplicated but is accepted
	e.accept([]byte("From: a@b.example\r\nSubject: x\r\n\r\nbody"), auth(), "created")
	e.accept([]byte("From: a@b.example\r\nSubject: x\r\n\r\nbody"), auth(), "created")
	if n := len(e.tickets(tenantA)); n != 3 {
		t.Fatalf("tickets = %d", n)
	}
}
