package metrics

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/go-freya/freya/observe"
)

func TestCountersRenderOnAdminHandler(t *testing.T) {
	fm, err := observe.NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(fm.Meter(Scope))
	if err != nil {
		t.Fatal(err)
	}
	m.Inbound(OutcomeCreated)
	m.Inbound(OutcomeCreated)
	m.Inbound(OutcomeRefused)
	m.RuleError()
	m.Reply(ResultSent)
	m.Ack(ResultSkipped)
	m.Transition("open", "resolved")
	rec := httptest.NewRecorder()
	fm.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`ticket_inbound_total{outcome="created"} 2`,
		`ticket_inbound_total{outcome="refused"} 1`,
		`ticket_rules_errors_total 1`,
		`ticket_replies_total{result="sent"} 1`,
		`ticket_acks_total{result="skipped"} 1`,
		`ticket_status_transitions_total{from="open",to="resolved"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in:\n%s", want, body)
		}
	}
}

func TestNilSafe(t *testing.T) {
	var m *Metrics
	m.Inbound(OutcomeError)
	m.RuleError()
	m.Reply(ResultFailed)
	m.Ack(ResultFailed)
	m.Transition("a", "b")
	if _, err := New(noop.NewMeterProvider().Meter("x")); err != nil {
		t.Fatal(err)
	}
}

type failingMeter struct {
	noop.Meter
	failAt int
	n      *int
}

func (f failingMeter) Int64Counter(name string, _ ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	*f.n++
	if *f.n == f.failAt {
		return nil, errors.New("boom " + name)
	}
	return noop.Int64Counter{}, nil
}

func TestNewPropagatesErrors(t *testing.T) {
	for i := 1; i <= 5; i++ {
		n := 0
		if _, err := New(failingMeter{failAt: i, n: &n}); err == nil {
			t.Errorf("counter %d error swallowed", i)
		}
	}
}
