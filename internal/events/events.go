// Package events publishes ticket lifecycle events to the shared platform
// event bus (platform:events:<tenant>, research D10): the notification module
// subscribes for agent alerts and the module's SSE stream refreshes open UIs.
// Payloads carry ids and metadata only — never message bodies, attachment data
// or requester addresses (contracts §D).
package events

import (
	"context"

	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/stream"
)

// Event types published to platform:events:<tenant>.
const (
	TicketCreated          = "ticket.created"
	TicketAssigned         = "ticket.assigned"
	TicketStatusChanged    = "ticket.status_changed"
	TicketCommented        = "ticket.commented"
	TicketRequesterReplied = "ticket.requester_replied"
)

// Types lists every event type the module publishes.
var Types = []string{TicketCreated, TicketAssigned, TicketStatusChanged, TicketCommented, TicketRequesterReplied}

// Payload is the content-safe event body.
type Payload struct {
	TicketID   string `json:"ticket_id"`
	Subject    string `json:"subject"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	AssigneeID string `json:"assignee_id,omitempty"`
	Source     string `json:"source"`
	ActorKind  string `json:"actor_kind"`
}

// PayloadOf builds the payload of t. The subject is truncated to 200 bytes;
// description, body, requester and recipient fields are never copied.
func PayloadOf(t store.Ticket, actorKind string) Payload {
	subj := t.Subject
	if len(subj) > 200 {
		subj = subj[:200]
	}
	return Payload{TicketID: t.ID, Subject: subj, Status: t.Status, Priority: t.Priority,
		AssigneeID: t.AssigneeID, Source: t.Source, ActorKind: actorKind}
}

// Publisher emits a realtime event to all of a tenant's subscribers.
type Publisher interface {
	Publish(ctx context.Context, tenantID, eventType string, p Payload)
}

// HubPublisher publishes through the stream hub (nil hub is a no-op).
type HubPublisher struct{ Hub *stream.Hub }

// Publish broadcasts eventType to every subscriber of tenantID; publishing is
// best effort and never fails the caller's operation.
func (p HubPublisher) Publish(ctx context.Context, tenantID, eventType string, pl Payload) {
	if p.Hub == nil {
		return
	}
	_, _ = p.Hub.PublishID(ctx, tenantID, nil, true, eventType, pl, true)
}

// Emit publishes through pub when it is non-nil.
func Emit(ctx context.Context, pub Publisher, tenantID, eventType string, t store.Ticket, actorKind string) {
	if pub == nil {
		return
	}
	pub.Publish(ctx, tenantID, eventType, PayloadOf(t, actorKind))
}

// Recorded is one captured event (Recorder).
type Recorded struct {
	TenantID string
	Type     string
	Payload  Payload
}

// Recorder is an in-memory Publisher for tests.
type Recorder struct{ Events []Recorded }

// Publish implements Publisher.
func (r *Recorder) Publish(_ context.Context, tenantID, eventType string, p Payload) {
	r.Events = append(r.Events, Recorded{TenantID: tenantID, Type: eventType, Payload: p})
}

var (
	_ Publisher = HubPublisher{}
	_ Publisher = (*Recorder)(nil)
)
