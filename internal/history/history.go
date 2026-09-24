// Package history records a ticket's status/priority/assignee changes
// (research D12): who changed which field from what to what, and when. Rows
// are tenant-scoped and cascade with the ticket; resolved-per-day statistics
// read the transitions into "resolved" from here.
package history

import (
	"context"
	"time"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// Log appends and lists history entries.
type Log struct {
	st  repo.History
	now func() time.Time
}

// New builds the log over st.
func New(st repo.History) *Log {
	return &Log{st: st, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock injects the clock (tests).
func (l *Log) SetClock(now func() time.Time) { l.now = now }

// Append records one change of field on the ticket. Unchanged values are not
// recorded (nil error).
func (l *Log) Append(ctx context.Context, tenantID, ticketID, field, oldValue, newValue, actorKind, actorID string) error {
	if oldValue == newValue {
		return nil
	}
	return l.st.AppendHistory(ctx, store.History{
		ID: store.NewID(), TenantID: tenantID, TicketID: ticketID, Field: field,
		OldValue: oldValue, NewValue: newValue, ActorKind: actorKind, ActorID: actorID, CreatedAt: l.now(),
	})
}

// List returns a ticket's history oldest first (never nil).
func (l *Log) List(ctx context.Context, tenantID, ticketID string) ([]store.History, error) {
	out, err := l.st.ListHistory(ctx, tenantID, ticketID)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []store.History{}
	}
	return out, nil
}

// ActorKind maps an authenticated caller to the history actor vocabulary:
// signed-in agents are "agent", the inbound edge "inbound", and mesh services
// and internal maintenance "system". Rule actions record store.ActorRule
// explicitly.
func ActorKind(s authz.Subjects) string {
	switch s.ActorKind {
	case authz.ActorAgent:
		return store.ActorAgent
	case authz.ActorInbound:
		return store.ActorInbound
	}
	return store.ActorSystem
}
