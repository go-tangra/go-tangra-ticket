package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/stream"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func sample() store.Ticket {
	return store.Ticket{ID: "t1", TenantID: tn, Subject: strings.Repeat("s", 300), Description: "SECRET-BODY", BodyHTML: "<p>SECRET-HTML</p>",
		Status: store.StatusOpen, Priority: store.PriorityHigh, Source: store.SourceEmail, RequesterEmail: "req@customer.example",
		RequesterName: "Req Name", Recipient: "support@acme.example", AssigneeID: "agent-1", AssigneeName: "Ada", ExternalID: "<m@x>"}
}

// The event payload must never carry bodies, requester fields or addresses.
func TestPayloadIsContentSafe(t *testing.T) {
	p := PayloadOf(sample(), "agent")
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, leak := range []string{"SECRET-BODY", "SECRET-HTML", "req@customer.example", "Req Name", "support@acme.example", "description", "body", "requester", "recipient", "<m@x>"} {
		if strings.Contains(s, leak) {
			t.Errorf("payload leaks %q: %s", leak, s)
		}
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"ticket_id", "subject", "status", "priority", "assignee_id", "source", "actor_kind"} {
		if _, ok := m[k]; !ok {
			t.Errorf("payload lacks %s", k)
		}
	}
	if len(p.Subject) != 200 {
		t.Fatalf("subject not truncated: %d", len(p.Subject))
	}
}

func TestEmitAndRecorder(t *testing.T) {
	Emit(context.Background(), nil, tn, TicketCreated, sample(), "agent") // nil publisher: no-op
	r := &Recorder{}
	Emit(context.Background(), r, tn, TicketAssigned, sample(), "rule")
	if len(r.Events) != 1 || r.Events[0].Type != TicketAssigned || r.Events[0].Payload.ActorKind != "rule" {
		t.Fatalf("recorded = %+v", r.Events)
	}
	for _, typ := range Types {
		if err := stream.ValidateType(typ, true); err != nil {
			t.Errorf("event type %s invalid for the hub: %v", typ, err)
		}
	}
}

func TestHubPublisherRelaysToSubscribers(t *testing.T) {
	HubPublisher{}.Publish(context.Background(), tn, TicketCreated, Payload{}) // nil hub: no-op
	hub := stream.NewHub(stream.NewMemory(), stream.Config{}, nil)
	defer hub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := hub.Subscribe(ctx, tn, "agent-1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	time.Sleep(50 * time.Millisecond)
	HubPublisher{Hub: hub}.Publish(ctx, tn, TicketStatusChanged, PayloadOf(sample(), "agent"))
	select {
	case ev := <-sub.Events():
		if ev.Type != TicketStatusChanged || strings.Contains(ev.Data, "SECRET") || strings.Contains(ev.Data, "req@customer.example") {
			t.Fatalf("event = %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("no event relayed")
	}
}
