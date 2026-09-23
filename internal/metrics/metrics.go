// Package metrics holds the ticket module's Prometheus counters (research D12).
// They are created on the framework's meter (freya App.Metrics().Meter), so the
// admin listener's /metrics renders them next to the framework instruments:
//
//	ticket_inbound_total{outcome}            created|threaded|duplicate|dropped|refused|error
//	ticket_rules_errors_total                 rules skipped because evaluation failed
//	ticket_replies_total{result}              sent|failed
//	ticket_acks_total{result}                 sent|skipped|failed
//	ticket_status_transitions_total{from,to}
//
// Labels carry closed vocabularies only (never tenants, addresses or ids). Every
// method is nil-safe so services can run without metrics in tests.
package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Scope is the instrumentation scope name.
const Scope = "github.com/go-freya/freya/services/ticket"

// Inbound outcomes.
const (
	OutcomeCreated   = "created"
	OutcomeThreaded  = "threaded"
	OutcomeDuplicate = "duplicate"
	OutcomeDropped   = "dropped"
	OutcomeRefused   = "refused"
	OutcomeError     = "error"
)

// Delivery results.
const (
	ResultSent    = "sent"
	ResultFailed  = "failed"
	ResultSkipped = "skipped"
)

// Metrics are the module counters.
type Metrics struct {
	inbound     metric.Int64Counter
	rulesErrors metric.Int64Counter
	replies     metric.Int64Counter
	acks        metric.Int64Counter
	transitions metric.Int64Counter
}

// New creates the counters on meter.
func New(meter metric.Meter) (*Metrics, error) {
	m := &Metrics{}
	var err error
	if m.inbound, err = meter.Int64Counter("ticket.inbound", metric.WithDescription("Inbound mail deliveries by outcome")); err != nil {
		return nil, err
	}
	if m.rulesErrors, err = meter.Int64Counter("ticket.rules.errors", metric.WithDescription("Rules skipped because evaluation failed")); err != nil {
		return nil, err
	}
	if m.replies, err = meter.Int64Counter("ticket.replies", metric.WithDescription("Public replies emailed by result")); err != nil {
		return nil, err
	}
	if m.acks, err = meter.Int64Counter("ticket.acks", metric.WithDescription("Acknowledgements by result")); err != nil {
		return nil, err
	}
	if m.transitions, err = meter.Int64Counter("ticket.status.transitions", metric.WithDescription("Ticket status transitions")); err != nil {
		return nil, err
	}
	return m, nil
}

func one(c metric.Int64Counter, attrs ...attribute.KeyValue) {
	c.Add(context.Background(), 1, metric.WithAttributes(attrs...))
}

// Inbound counts one inbound delivery outcome.
func (m *Metrics) Inbound(outcome string) {
	if m != nil {
		one(m.inbound, attribute.String("outcome", outcome))
	}
}

// RuleError counts a rule skipped because it failed to evaluate.
func (m *Metrics) RuleError() {
	if m != nil {
		one(m.rulesErrors)
	}
}

// Reply counts one outbound public reply.
func (m *Metrics) Reply(result string) {
	if m != nil {
		one(m.replies, attribute.String("result", result))
	}
}

// Ack counts one acknowledgement decision.
func (m *Metrics) Ack(result string) {
	if m != nil {
		one(m.acks, attribute.String("result", result))
	}
}

// Transition counts a status change.
func (m *Metrics) Transition(from, to string) {
	if m != nil {
		one(m.transitions, attribute.String("from", from), attribute.String("to", to))
	}
}
