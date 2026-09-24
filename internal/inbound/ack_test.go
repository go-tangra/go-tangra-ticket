package inbound

// T063: loop-safe acknowledgement of new email tickets (FR-013, research D5):
// sent from the mailbox template with {{name}} substituted and the reference
// line appended once, marked Auto-Submitted/Precedence (AutoReply), recorded as
// a public system comment carrying its Message-ID so a reply threads back, and
// suppressed for auto-submitted, bulk/list/junk, daemon/no-reply, self-addressed
// mail, a mailbox with auto_ack off and a missing relay.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/audit"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailer"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailparse"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/metrics"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/thread"
)

// withAck rebuilds the handler with a mailer and sets the support mailbox's
// auto-acknowledgement.
func (e *env) withAck(fm *mailer.Fake, autoAck bool, template string) *env {
	e.t.Helper()
	ctx := context.Background()
	mbs, err := e.st.ListMailboxes(ctx, tenantA)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, mb := range mbs {
		if mb.Address == support {
			mb.AutoAck, mb.AutoAckTemplate = autoAck, template
			if err := e.st.UpdateMailbox(ctx, mb); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	m, _ := metrics.New(e.fm.Meter(metrics.Scope))
	lim := mailparse.DefaultLimits()
	lim.MaxBodyBytes = maxBody
	log := slog.New(slog.NewJSONHandler(e.logs, nil))
	var sender mailer.Sender
	if fm != nil {
		sender = fm
	}
	h := NewHandler(Deps{Store: e.st, Blobs: e.blobs, Secrets: e.sec, Tickets: e.tk, Audit: e.audit, Events: e.pub,
		Metrics: m, Log: log, Limits: lim, Mailer: sender, MailDomain: "fallback.example"})
	e.srv = NewServer(ServerConfig{MaxBodyBytes: maxBody}, h, log).Handler()
	return e
}

func (e *env) comments(ticketID string) []store.Comment {
	e.t.Helper()
	cs, err := e.st.ListComments(context.Background(), tenantA, ticketID)
	if err != nil {
		e.t.Fatal(err)
	}
	return cs
}

func TestAckSentFromTemplateAndRecorded(t *testing.T) {
	fm := &mailer.Fake{}
	e := newEnv(t).withAck(fm, true, "Dear {{name}},\nwe got it. Thanks {{name}}!")
	res := e.accept(fixture(t, "plain.eml"), auth(), "created")
	sent := fm.Messages()
	if len(sent) != 1 {
		t.Fatalf("sent = %d", len(sent))
	}
	m := sent[0]
	if !m.AutoReply || m.From != support || m.FromName != "Acme Support" || m.To != requester || m.ToName != "Jane Customer" {
		t.Fatalf("ack envelope = %+v", m)
	}
	if m.Subject != "Re: Printer on floor 3 is jammed" || m.InReplyTo != "plain-0001@mail.customer.example" ||
		len(m.References) != 1 || m.References[0] != "plain-0001@mail.customer.example" {
		t.Fatalf("ack threading = %+v", m)
	}
	if !strings.HasPrefix(m.Text, "Dear Jane Customer,\nwe got it. Thanks Jane Customer!") ||
		strings.Count(m.Text, thread.ReferenceLine(res.TicketID)) != 1 || strings.Contains(m.Text, "{{name}}") {
		t.Fatalf("ack body = %q", m.Text)
	}
	if !strings.HasPrefix(m.MessageID, "ticket."+res.TicketID+".") || !strings.HasSuffix(m.MessageID, "@acme.example") {
		t.Fatalf("message id = %q", m.MessageID)
	}
	cs := e.comments(res.TicketID)
	if len(cs) != 1 {
		t.Fatalf("comments = %+v", cs)
	}
	c := cs[0]
	if c.Internal || c.AuthorKind != store.AuthorSystem || c.MessageID != m.MessageID || c.Delivery != store.DeliverySent || c.Body != m.Text {
		t.Fatalf("ack comment = %+v", c)
	}
	if acks := e.audit.of(audit.AckSent); len(acks) != 1 || acks[0].Outcome != audit.OutcomeOK || acks[0].SubjectID != res.TicketID {
		t.Fatalf("ack audit = %+v", acks)
	}
	for _, ev := range e.audit.events {
		for k, v := range ev.Details {
			if s, ok := v.(string); ok && (strings.Contains(s, requester) || strings.Contains(s, "Jane")) {
				t.Fatalf("audit %s leaks PII in %s", ev.EventType, k)
			}
		}
	}
	if !e.metric(`ticket_acks_total{result="sent"} 1`) {
		t.Fatal("ack metric")
	}
	// the requester's answer to the acknowledgement threads back
	reply := "From: Jane Customer <jane@customer.example>\r\nTo: support@acme.example\r\nSubject: Re: Printer\r\n" +
		"Message-ID: <ans-1@mail.customer.example>\r\nIn-Reply-To: <" + m.MessageID + ">\r\n\r\nmore info"
	th := e.accept([]byte(reply), auth(), "threaded")
	if th.TicketID != res.TicketID || len(fm.Messages()) != 1 {
		t.Fatalf("reply to ack: %+v, sent=%d", th, len(fm.Messages()))
	}
}

func TestAckDefaultTemplateAndDomainFallback(t *testing.T) {
	fm := &mailer.Fake{}
	e := newEnv(t).withAck(fm, true, "")
	res := e.accept([]byte("From: Sam <sam@customer.example>\r\nSubject: hi\r\nMessage-ID: <d-1@x>\r\n\r\nplease help"), auth(), "created")
	sent := fm.Messages()
	if len(sent) != 1 || !strings.HasPrefix(sent[0].Text, "Hello Sam,") || !strings.Contains(sent[0].Text, thread.Token(res.TicketID)) {
		t.Fatalf("default ack = %+v", sent)
	}
	// nameless requester: the greeting has no dangling name
	e.accept([]byte("From: pat@customer.example\r\nSubject: hi\r\nMessage-ID: <d-2@x>\r\n\r\nhelp"), auth(), "created")
	if got := fm.Messages()[1].Text; !strings.HasPrefix(got, "Hello,") {
		t.Fatalf("nameless ack = %q", got)
	}
	// a template carrying a foreign token still gets this ticket's reference
	e2 := newEnv(t).withAck(fm, true, "See [#aaaaaaaa-bbbb] for {{name}}")
	r2 := e2.accept(fixture(t, "plain.eml"), auth(), "created")
	if got := fm.Messages()[2].Text; strings.Count(got, thread.ReferenceLine(r2.TicketID)) != 1 {
		t.Fatalf("foreign-token template = %q", got)
	}
	// the mailbox address without a usable domain falls back to MailDomain
	if got := domainFor("nodomain", "fallback.example"); got != "fallback.example" {
		t.Fatalf("domain fallback = %q", got)
	}
}

func TestAckSuppressed(t *testing.T) {
	cases := []struct {
		name    string
		raw     func(t *testing.T) []byte
		autoAck bool
		off     bool // mailer disabled
		nilMail bool
		reason  string
	}{
		{"auto-submitted", func(t *testing.T) []byte { return fixture(t, "auto-submitted.eml") }, true, false, false, AckAutoSubmitted},
		{"bulk precedence", func(t *testing.T) []byte { return fixture(t, "bulk-precedence.eml") }, true, false, false, AckBulk},
		{"list precedence", precedence("list"), true, false, false, AckBulk},
		{"junk precedence", precedence("junk"), true, false, false, AckBulk},
		{"auto_reply precedence", precedence("auto_reply"), true, false, false, AckBulk},
		{"no-reply sender", func(t *testing.T) []byte { return fixture(t, "noreply-sender.eml") }, true, false, false, AckDaemon},
		{"mailer-daemon", sender("MAILER-DAEMON@mx.customer.example"), true, false, false, AckDaemon},
		{"no sender address", func(*testing.T) []byte { return []byte("Subject: x\r\nMessage-ID: <n@x>\r\n\r\nbody") }, true, false, false, AckDaemon},
		{"self-addressed", sender("Support@Acme.example"), true, false, false, AckSelf},
		{"another support mailbox", sender("help@beta.example"), true, false, false, AckSelf},
		{"auto_ack off", func(t *testing.T) []byte { return fixture(t, "plain.eml") }, false, false, false, AckOff},
		{"relay disabled", func(t *testing.T) []byte { return fixture(t, "plain.eml") }, true, true, false, AckNoRelay},
		{"no mailer", func(t *testing.T) []byte { return fixture(t, "plain.eml") }, true, false, true, AckNoRelay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fm := &mailer.Fake{Disabled: c.off}
			if c.nilMail {
				fm = nil
			}
			e := newEnv(t).withAck(fm, c.autoAck, "")
			res := e.accept(c.raw(t), auth(), "created")
			if fm != nil && len(fm.Messages()) != 0 {
				t.Fatalf("ack sent: %+v", fm.Messages())
			}
			for _, cm := range e.comments(res.TicketID) {
				if cm.AuthorKind == store.AuthorSystem && !cm.Internal {
					t.Fatalf("ack comment recorded: %+v", cm)
				}
			}
			if len(e.audit.of(audit.AckSent)) != 0 {
				t.Fatal("ack.sent audited")
			}
			sk := e.audit.of(audit.AckSkipped)
			if len(sk) != 1 || sk[0].Reason != c.reason || sk[0].SubjectID != res.TicketID {
				t.Fatalf("ack.skipped = %+v", sk)
			}
			if !e.metric(`ticket_acks_total{result="skipped"} 1`) {
				t.Fatal("skipped metric")
			}
		})
	}
}

func precedence(v string) func(*testing.T) []byte {
	return func(*testing.T) []byte {
		return []byte("From: Jane <jane@customer.example>\r\nSubject: news\r\nPrecedence: " + v + "\r\nMessage-ID: <p-" + v + "@x>\r\n\r\nbody")
	}
}

func sender(addr string) func(*testing.T) []byte {
	return func(*testing.T) []byte {
		return []byte("From: <" + addr + ">\r\nSubject: note\r\nMessage-ID: <s-1@x>\r\n\r\nbody")
	}
}

func TestAckNotSentForRepliesDuplicatesOrDrops(t *testing.T) {
	fm := &mailer.Fake{}
	e := newEnv(t).withAck(fm, true, "")
	e.accept(fixture(t, "plain.eml"), auth(), "created")
	e.accept(fixture(t, "plain.eml"), auth(), "duplicate")
	e.accept(fixture(t, "reply-in-reply-to.eml"), auth(), "threaded")
	if n := len(fm.Messages()); n != 1 {
		t.Fatalf("acks = %d", n)
	}
}

func TestAckSeesRuleOutcome(t *testing.T) {
	fm := &mailer.Fake{}
	e := newEnv(t).withAck(fm, true, "")
	m, _ := metrics.New(e.fm.Meter(metrics.Scope))
	lim := mailparse.DefaultLimits()
	lim.MaxBodyBytes = maxBody
	sentAtApply := -1
	tri := &applyTriage{fn: func() { sentAtApply = len(fm.Messages()) }}
	h := NewHandler(Deps{Store: e.st, Blobs: e.blobs, Secrets: e.sec, Tickets: e.tk, Audit: e.audit, Metrics: m, Limits: lim, Mailer: fm, Triage: tri})
	e.srv = NewServer(ServerConfig{MaxBodyBytes: maxBody}, h, nil).Handler()
	e.accept(fixture(t, "plain.eml"), auth(), "created")
	if sentAtApply != 0 || len(fm.Messages()) != 1 {
		t.Fatalf("ack must follow the rule actions: at apply=%d, sent=%d", sentAtApply, len(fm.Messages()))
	}
}

type applyTriage struct{ fn func() }

func (a *applyTriage) Evaluate(context.Context, authz.Subjects, TriageInput) (Plan, error) {
	return Plan{Apply: func(context.Context, string) error { a.fn(); return nil }}, nil
}

func TestAckUnsafeHeadersAndOddThreading(t *testing.T) {
	fm := &mailer.Fake{}
	e := newEnv(t).withAck(fm, true, "")
	h := NewHandler(Deps{Store: e.st, Audit: e.audit, Mailer: fm})
	ctx := context.Background()
	route := store.MailboxRoute{TenantID: tenantA, MailboxID: "mb", DisplayName: "Acme Support", AutoAck: true}
	mk := func(name, ext string) store.Ticket {
		tk := store.Ticket{ID: store.NewID(), TenantID: tenantA, Subject: "s", Status: store.StatusOpen, Priority: store.PriorityNormal,
			Source: store.SourceEmail, RequesterEmail: requester, RequesterName: name, ExternalID: ext, CreatedBy: store.CreatedByInbound}
		if err := e.st.CreateTicket(ctx, tk); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	// a requester name that would inject a header: refused, nothing recorded
	bad := mk("Eve\r\nBcc: x@evil.example", "")
	h.acknowledge(ctx, authz.SystemFor(tenantA), route, support, &mailparse.Message{FromEmail: requester}, bad)
	if len(fm.Messages()) != 0 || len(e.comments(bad.ID)) != 0 {
		t.Fatal("unsafe ack sent")
	}
	if sk := e.audit.of(audit.AckSkipped); len(sk) != 1 || sk[0].Reason != AckUnsafe {
		t.Fatalf("unsafe audit = %+v", sk)
	}
	// an unusable root message id only drops the threading headers
	odd := mk("Jane", "not a <valid> id")
	h.acknowledge(ctx, authz.SystemFor(tenantA), route, support, &mailparse.Message{FromEmail: requester}, odd)
	if s := fm.Messages(); len(s) != 1 || s[0].InReplyTo != "" || len(s[0].References) != 0 {
		t.Fatalf("odd threading ack = %+v", s)
	}
}

func TestAckDeliveryAndRecordFailures(t *testing.T) {
	// relay failure: the comment stays (delivery=failed), audited as an error
	fm := &mailer.Fake{Err: errors.New("relay down")}
	e := newEnv(t).withAck(fm, true, "")
	res := e.accept(fixture(t, "plain.eml"), auth(), "created")
	cs := e.comments(res.TicketID)
	if len(cs) != 1 || cs[0].Delivery != store.DeliveryFailed {
		t.Fatalf("failed ack comment = %+v", cs)
	}
	if a := e.audit.of(audit.AckSent); len(a) != 1 || a[0].Outcome != audit.OutcomeError {
		t.Fatalf("failed ack audit = %+v", a)
	}
	if !e.metric(`ticket_acks_total{result="failed"} 1`) {
		t.Fatal("failed metric")
	}
	// the comment cannot be recorded: nothing is sent (no un-threadable ack)
	fm2 := &mailer.Fake{}
	e2 := newEnv(t).withAck(fm2, true, "")
	e2.st.FailNext("CreateComment")
	e2.accept(fixture(t, "plain.eml"), auth(), "created")
	if len(fm2.Messages()) != 0 {
		t.Fatal("ack sent without a recorded comment")
	}
	if sk := e2.audit.of(audit.AckSkipped); len(sk) != 1 || sk[0].Reason != AckRecordFailed {
		t.Fatalf("record failure audit = %+v", sk)
	}
}
