package audit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/store"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

type memStore struct {
	mu   sync.Mutex
	rows []store.AuditRow
	err  error
}

func (m *memStore) AppendAudit(_ context.Context, r store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.rows = append(m.rows, r)
	return nil
}

func (m *memStore) all() []store.AuditRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.AuditRow(nil), m.rows...)
}

func ok(t EventType) Event {
	return Event{TenantID: tn, EventType: t, ActorKind: ActorAgent, ActorID: "u1", SubjectKind: SubjectTicket, SubjectID: "t1", Outcome: OutcomeOK}
}

func TestVocabulary(t *testing.T) {
	for _, want := range []string{"ticket.create", "ticket.update", "ticket.delete", "ticket.assign", "ticket.status", "ticket.tags",
		"comment.create", "comment.delete", "reply.sent", "reply.failed", "ack.sent", "ack.skipped",
		"inbound.created", "inbound.threaded", "inbound.duplicate", "inbound.dropped", "inbound.refused",
		"rule.create", "rule.update", "rule.delete", "tag.create", "tag.update", "tag.delete",
		"mailbox.create", "mailbox.update", "mailbox.delete", "backup.export", "backup.import"} {
		if !Known(want) {
			t.Errorf("vocabulary lacks %s", want)
		}
	}
	if Known("ticket.explode") {
		t.Fatal("unknown event accepted")
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(ok(TicketCreate)); err != nil {
		t.Fatal(err)
	}
	bad := []func(*Event){
		func(e *Event) { e.EventType = "nope" },
		func(e *Event) { e.TenantID = "" },
		func(e *Event) { e.ActorKind = "robot" },
		func(e *Event) { e.SubjectKind = "planet" },
		func(e *Event) { e.Outcome = "maybe" },
	}
	for i, mut := range bad {
		e := ok(TicketCreate)
		mut(&e)
		if Validate(e) == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	for _, k := range []string{ActorInbound, ActorService, ActorSystem, ActorRule} {
		e := ok(InboundCreated)
		e.ActorKind, e.SubjectKind = k, SubjectMessage
		if err := Validate(e); err != nil {
			t.Errorf("actor %s: %v", k, err)
		}
	}
}

func TestRedaction(t *testing.T) {
	d := Redact(map[string]any{
		"body": "secret text", "Description": "x", "body_html": "<p>", "requester_email": "a@b", "requester_name": "Ann",
		"author_email": "c@d", "author_name": "Bob", "relay_token": "tok", "smtp_password": "pw", "client_secret": "s",
		"status": "open", "long": strings.Repeat("x", 400),
		"nested": map[string]any{"token": "t", "keep": "v", "list": []any{map[string]any{"password": "p", "n": 1}, "s"}},
	})
	for _, k := range []string{"body", "Description", "body_html", "requester_email", "requester_name", "author_email", "author_name", "relay_token", "smtp_password", "client_secret"} {
		if _, found := d[k]; found {
			t.Errorf("%s leaked", k)
		}
	}
	if d["status"] != "open" || len(d["long"].(string)) != 256 {
		t.Fatalf("kept values: %+v", d)
	}
	nested := d["nested"].(map[string]any)
	if _, found := nested["token"]; found || nested["keep"] != "v" {
		t.Fatalf("nested: %+v", nested)
	}
	item := nested["list"].([]any)[0].(map[string]any)
	if _, found := item["password"]; found || item["n"] != 1 {
		t.Fatalf("list item: %+v", item)
	}
}

func TestWriterRecordsAndFlushes(t *testing.T) {
	st := &memStore{}
	w := NewWriter(st, nil)
	e := ok(TicketStatus)
	e.Reason = strings.Repeat("r", 300)
	e.Details = map[string]any{"from": "open", "to": "resolved", "body": "never"}
	if err := w.Record(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := w.Record(context.Background(), Event{EventType: "bogus"}); err == nil {
		t.Fatal("invalid event queued")
	}
	w.Flush(context.Background())
	rows := st.all()
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.Action != "ticket.status" || r.ID == "" || r.At.IsZero() || len(r.Reason) != 256 || r.Detail["to"] != "resolved" {
		t.Fatalf("row = %+v", r)
	}
	if _, found := r.Detail["body"]; found {
		t.Fatal("body in audit detail")
	}
	w.Close()
	w.Close() // idempotent
	if err := w.Record(context.Background(), ok(TicketCreate)); err == nil {
		t.Fatal("record after close")
	}
	w.Flush(context.Background()) // no-op after close
	if w.Dropped() != 1 {
		t.Fatalf("dropped = %d", w.Dropped())
	}
}

func TestWriterErrorsAndBackpressure(t *testing.T) {
	st := &memStore{err: errors.New("db down")}
	var mu sync.Mutex
	var errs []error
	w := newWriter(st, func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }, 1)
	// not started: the second record overflows the queue of 1
	_ = w.Record(context.Background(), ok(TicketCreate))
	_ = w.Record(context.Background(), ok(TicketCreate))
	if w.Dropped() != 1 {
		t.Fatalf("dropped = %d", w.Dropped())
	}
	w.tick = time.Millisecond
	w.start()
	w.Flush(context.Background())
	time.Sleep(5 * time.Millisecond)
	w.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(errs) < 2 {
		t.Fatalf("errors = %v", errs)
	}
	nw := newWriter(st, nil, 1)
	nw.onError(errors.New("ignored")) // default handler is a no-op
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nw.Flush(ctx) // not started + cancelled: returns
}

type recorder struct{ got []Event }

func (r *recorder) Record(_ context.Context, e Event) error { r.got = append(r.got, e); return nil }

func TestEmitAndActorOf(t *testing.T) {
	Emit(context.Background(), nil, ok(TicketCreate)) // nil recorder: no-op
	r := &recorder{}
	Emit(context.Background(), r, ok(TicketCreate))
	if len(r.got) != 1 {
		t.Fatal("emit")
	}
	for in, want := range map[string]string{"agent": ActorAgent, "inbound": ActorInbound, "service": ActorService, "system": ActorSystem, "rule": ActorRule, "": ActorAgent} {
		if ActorOf(in) != want {
			t.Errorf("ActorOf(%q) = %q", in, ActorOf(in))
		}
	}
}
