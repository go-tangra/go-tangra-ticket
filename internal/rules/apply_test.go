package rules

// T053: triage decision and action application — enabled rules in sort order,
// tags accumulate across matching rules (missing tags created), the first
// matching rule wins for assign/status/priority, any drop discards, an erroring
// rule is skipped and counted, and history records actor "rule".

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-freya/freya/observe"
	"github.com/go-freya/freya/services/ticket/internal/agents"
	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/inbound"
	"github.com/go-freya/freya/services/ticket/internal/mailparse"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
	"github.com/go-freya/freya/services/ticket/internal/metrics"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

const (
	tenantA = "11111111-1111-7111-8111-111111111111"
	tenantB = "22222222-2222-7222-8222-222222222222"
	agentA  = "55555555-5555-7555-8555-555555555555"
	agentB  = "66666666-6666-7666-8666-666666666666"
)

type recorder struct{ events []audit.Event }

func (r *recorder) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.events = append(r.events, e)
	return nil
}

type triageEnv struct {
	st  *memstore.Mem
	tr  *Triage
	fm  *observe.Metrics
	rec *recorder
	ctx context.Context
}

func newTriageEnv(t *testing.T) *triageEnv {
	t.Helper()
	st := memstore.New()
	dir := agents.NewFake()
	dir.Add(tenantA, agents.User{ID: agentA, Name: "Ada"}, true)
	dir.Add(tenantA, agents.User{ID: agentB, Name: "Bob"}, true)
	fm, err := observe.NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	m, err := metrics.New(fm.Meter(metrics.Scope))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	svc := tickets.New(tickets.Deps{Store: st, Agents: dir, Metrics: m, Audit: rec})
	e := mustEngine(t)
	tr := NewTriage(TriageDeps{Engine: e, Store: st, Tickets: svc, Metrics: m, Audit: rec, Timeout: time.Second})
	return &triageEnv{st: st, tr: tr, fm: fm, rec: rec, ctx: context.Background()}
}

func (e *triageEnv) addRule(t *testing.T, r store.Rule) store.Rule {
	t.Helper()
	if r.ID == "" {
		r.ID = store.NewID()
	}
	if r.TenantID == "" {
		r.TenantID = tenantA
	}
	if r.Match == "" {
		r.Match = store.MatchAll
	}
	if err := e.st.CreateRule(e.ctx, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *triageEnv) newTicket(t *testing.T, tenant string) store.Ticket {
	t.Helper()
	tk := store.Ticket{ID: store.NewID(), TenantID: tenant, Subject: "Invoice overdue", Status: store.StatusOpen, Priority: store.PriorityNormal,
		Source: store.SourceEmail, CreatedBy: store.CreatedByInbound}
	if err := e.st.CreateTicket(e.ctx, tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func (e *triageEnv) scrape() string {
	rec := httptest.NewRecorder()
	e.fm.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

var invoiceMail = Email{Subject: "Invoice overdue", Body: "please pay", From: "ann@customer.example", FromDomain: "customer.example", Recipient: "support@acme.example"}

func TestDecideOrderAccumulationAndFirstWins(t *testing.T) {
	e := newTriageEnv(t)
	subjectInvoice := []store.Condition{cond("subject", "contains", "invoice")}
	e.addRule(t, store.Rule{Name: "late", SortOrder: 30, Enabled: true, Conditions: subjectInvoice, Actions: []store.Action{
		{Type: store.ActionTag, TagKind: store.KindTag, TagNames: []string{"billing", "late"}},
		{Type: store.ActionAssign, AssigneeID: agentB},
		{Type: store.ActionStatus, Status: store.StatusClosed},
		{Type: store.ActionPriority, Priority: store.PriorityLow},
	}})
	e.addRule(t, store.Rule{Name: "first", SortOrder: 10, Enabled: true, Conditions: subjectInvoice, Actions: []store.Action{
		{Type: store.ActionTag, TagNames: []string{"Billing"}},
		{Type: store.ActionAssign, AssigneeID: agentA},
	}})
	e.addRule(t, store.Rule{Name: "second", SortOrder: 20, Enabled: true, Conditions: []store.Condition{cond("fromDomain", "equals", "customer.example")}, Actions: []store.Action{
		{Type: store.ActionTag, TagKind: store.KindCategory, TagNames: []string{"Finance"}},
		{Type: store.ActionStatus, Status: store.StatusPending},
		{Type: store.ActionPriority, Priority: store.PriorityHigh},
	}})
	e.addRule(t, store.Rule{Name: "no match", SortOrder: 0, Enabled: true, Conditions: []store.Condition{cond("subject", "contains", "outage")}, Actions: []store.Action{
		{Type: store.ActionAssign, AssigneeID: agentB}, {Type: store.ActionTag, TagNames: []string{"outage"}},
	}})
	e.addRule(t, store.Rule{Name: "disabled", SortOrder: 1, Enabled: false, Conditions: subjectInvoice, Actions: []store.Action{{Type: store.ActionDrop}}})

	d, err := e.tr.Decide(e.ctx, tenantA, invoiceMail)
	if err != nil {
		t.Fatal(err)
	}
	if d.Drop || d.AssigneeID != agentA || d.Status != store.StatusPending || d.Priority != store.PriorityHigh || d.Errors != 0 {
		t.Fatalf("decision = %+v", d)
	}
	if got := strings.Join(d.MatchedNames, ","); got != "first,second,late" {
		t.Fatalf("matched in order = %q", got)
	}
	var tags []string
	for _, tg := range d.Tags {
		tags = append(tags, tg.Kind+":"+tg.Name)
	}
	if got := strings.Join(tags, ","); got != "tag:Billing,category:Finance,tag:late" {
		t.Fatalf("tags = %q (case-insensitive dedupe, accumulation)", got)
	}

	tk := e.newTicket(t, tenantA)
	// "Billing" already exists with another case: reused, not duplicated.
	existing := store.Tag{ID: store.NewID(), TenantID: tenantA, Name: "BILLING", Kind: store.KindTag}
	if err := e.st.CreateTag(e.ctx, existing); err != nil {
		t.Fatal(err)
	}
	if err := e.tr.Apply(e.ctx, authz.SystemFor(tenantA), tk.ID, d); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetTicket(e.ctx, tenantA, tk.ID)
	if got.AssigneeID != agentA || got.Status != store.StatusPending || got.Priority != store.PriorityHigh {
		t.Fatalf("ticket = %+v", got)
	}
	links, _ := e.st.TagsForTickets(e.ctx, tenantA, []string{tk.ID})
	if len(links[tk.ID]) != 3 {
		t.Fatalf("ticket tags = %+v", links[tk.ID])
	}
	all, _ := e.st.ListTags(e.ctx, tenantA, "")
	if len(all) != 3 {
		t.Fatalf("vocabulary = %+v (missing tags created, existing reused)", all)
	}
	hist, _ := e.st.ListHistory(e.ctx, tenantA, tk.ID)
	if len(hist) != 3 {
		t.Fatalf("history = %+v", hist)
	}
	for _, h := range hist {
		if h.ActorKind != store.ActorRule {
			t.Fatalf("history actor = %q, want rule", h.ActorKind)
		}
	}
	found := false
	for _, ev := range e.rec.events {
		if ev.EventType == audit.TicketTags && ev.SubjectID == tk.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("tag application not audited: %+v", e.rec.events)
	}
}

func TestAnyDropDiscards(t *testing.T) {
	e := newTriageEnv(t)
	e.addRule(t, store.Rule{Name: "tagger", SortOrder: 1, Enabled: true, Conditions: []store.Condition{cond("subject", "contains", "invoice")},
		Actions: []store.Action{{Type: store.ActionTag, TagNames: []string{"billing"}}}})
	e.addRule(t, store.Rule{Name: "spam", SortOrder: 2, Enabled: true, Conditions: []store.Condition{cond("spamScore", "gt", "5")},
		Actions: []store.Action{{Type: store.ActionDrop}}})
	m := invoiceMail
	m.SpamScore = 9
	d, err := e.tr.Decide(e.ctx, tenantA, m)
	if err != nil || !d.Drop {
		t.Fatalf("decision = %+v %v", d, err)
	}
	m.SpamScore = 1
	if d, _ := e.tr.Decide(e.ctx, tenantA, m); d.Drop {
		t.Fatal("drop without a match")
	}
}

func TestErroringRuleSkippedAndCounted(t *testing.T) {
	e := newTriageEnv(t)
	bad := e.addRule(t, store.Rule{Name: "broken", SortOrder: 1, Enabled: true, Expression: `int(subject) > 0`, Actions: []store.Action{{Type: store.ActionDrop}}})
	e.addRule(t, store.Rule{Name: "invalid stored", SortOrder: 2, Enabled: true, Expression: `nope(`, Actions: []store.Action{{Type: store.ActionDrop}}})
	e.addRule(t, store.Rule{Name: "good", SortOrder: 3, Enabled: true, Conditions: []store.Condition{cond("subject", "contains", "invoice")},
		Actions: []store.Action{{Type: store.ActionPriority, Priority: store.PriorityUrgent}}})
	d, err := e.tr.Decide(e.ctx, tenantA, invoiceMail)
	if err != nil {
		t.Fatal(err)
	}
	if d.Drop || d.Priority != store.PriorityUrgent || d.Errors != 2 {
		t.Fatalf("decision = %+v", d)
	}
	if body := e.scrape(); !strings.Contains(body, "ticket_rules_errors_total 2") {
		t.Fatalf("rule errors not counted:\n%s", body)
	}
	found := false
	for _, ev := range e.rec.events {
		if ev.EventType == audit.RuleError && ev.SubjectID == bad.ID && ev.Outcome == audit.OutcomeError {
			found = true
		}
	}
	if !found {
		t.Fatalf("rule error not audited: %+v", e.rec.events)
	}
}

func TestEvaluatePlanForInbound(t *testing.T) {
	e := newTriageEnv(t)
	e.addRule(t, store.Rule{Name: "assign", SortOrder: 1, Enabled: true, Conditions: []store.Condition{cond("recipient", "equals", "support@acme.example")},
		Actions: []store.Action{{Type: store.ActionAssign, AssigneeID: agentA}, {Type: store.ActionTag, TagNames: []string{"inbound"}}}})
	msg := &mailparse.Message{Subject: "Hello", Text: "hi", FromEmail: "ann@customer.example"}
	plan, err := e.tr.Evaluate(e.ctx, authz.SystemFor(tenantA), inbound.TriageInput{Message: msg, Recipient: "support@acme.example"})
	if err != nil || plan.Drop || plan.Apply == nil {
		t.Fatalf("plan = %+v %v", plan, err)
	}
	tk := e.newTicket(t, tenantA)
	if err := plan.Apply(e.ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetTicket(e.ctx, tenantA, tk.ID)
	if got.AssigneeID != agentA {
		t.Fatalf("assignee = %q", got.AssigneeID)
	}
	// No rule matches: no Apply.
	plan, err = e.tr.Evaluate(e.ctx, authz.SystemFor(tenantA), inbound.TriageInput{Message: msg, Recipient: "other@acme.example"})
	if err != nil || plan.Drop || plan.Apply != nil {
		t.Fatalf("plan without matches = %+v %v", plan, err)
	}
	// Another tenant's rules never apply.
	plan, err = e.tr.Evaluate(e.ctx, authz.SystemFor(tenantB), inbound.TriageInput{Message: msg, Recipient: "support@acme.example"})
	if err != nil || plan.Apply != nil {
		t.Fatalf("cross-tenant plan = %+v %v", plan, err)
	}
	if _, err := e.tr.Evaluate(e.ctx, authz.Subjects{}, inbound.TriageInput{Message: msg}); err == nil {
		t.Fatal("evaluate without a tenant accepted")
	}
}

func TestStoreFailureIsAnError(t *testing.T) {
	e := newTriageEnv(t)
	e.st.FailNext("ListEnabledRules")
	if _, err := e.tr.Decide(e.ctx, tenantA, invoiceMail); err == nil {
		t.Fatal("list failure must surface (ingestion logs it and continues)")
	}
}

func TestApplyBestEffort(t *testing.T) {
	e := newTriageEnv(t)
	tk := e.newTicket(t, tenantA)
	d := Decision{AssigneeID: "unknown-user", Status: store.StatusResolved, Tags: []TagRef{{Kind: store.KindTag, Name: "x"}}}
	err := e.tr.Apply(e.ctx, authz.SystemFor(tenantA), tk.ID, d)
	if err == nil {
		t.Fatal("an unassignable assignee must be reported")
	}
	got, _ := e.st.GetTicket(e.ctx, tenantA, tk.ID)
	if got.Status != store.StatusResolved || got.AssigneeID != "" {
		t.Fatalf("other actions must still apply: %+v", got)
	}
	links, _ := e.st.TagsForTickets(e.ctx, tenantA, []string{tk.ID})
	if len(links[tk.ID]) != 1 {
		t.Fatal("tags not applied")
	}
	if err := e.tr.Apply(e.ctx, authz.SystemFor(tenantA), tk.ID, Decision{}); err != nil {
		t.Fatalf("empty decision: %v", err)
	}
}

func TestMaxRulesBoundsEvaluation(t *testing.T) {
	e := newTriageEnv(t)
	e.tr.d.MaxRules = 1
	e.addRule(t, store.Rule{Name: "a", SortOrder: 1, Enabled: true, Conditions: []store.Condition{cond("subject", "contains", "invoice")},
		Actions: []store.Action{{Type: store.ActionPriority, Priority: store.PriorityHigh}}})
	e.addRule(t, store.Rule{Name: "b", SortOrder: 2, Enabled: true, Conditions: []store.Condition{cond("subject", "contains", "invoice")},
		Actions: []store.Action{{Type: store.ActionDrop}}})
	d, err := e.tr.Decide(e.ctx, tenantA, invoiceMail)
	if err != nil || d.Drop || len(d.MatchedNames) != 1 {
		t.Fatalf("decision = %+v %v", d, err)
	}
}
