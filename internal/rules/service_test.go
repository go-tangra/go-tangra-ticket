package rules

// Rules administration: validation on save, versioning and cache eviction,
// assignee checks, per-tenant limits, audit, and the dry run.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/audit"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

func admin(tenant string) authz.Subjects {
	return authz.Subjects{TenantID: tenant, UserID: "admin-" + tenant[:4], ActorKind: authz.ActorAgent}
}

func ptrTo[T any](v T) *T { return &v }

type svcEnv struct {
	svc *Service
	st  *memstore.Mem
	dir *agents.Fake
	eng *Engine
	rec *recorder
}

func newSvcEnv(t *testing.T) *svcEnv {
	t.Helper()
	st := memstore.New()
	dir := agents.NewFake()
	dir.Add(tenantA, agents.User{ID: agentA, Name: "Ada"}, true)
	dir.Add(tenantA, agents.User{ID: agentB, Name: "Bob"}, false)
	eng := mustEngine(t)
	rec := &recorder{}
	return &svcEnv{svc: NewService(Deps{Store: st, Engine: eng, Agents: dir, Audit: rec, MaxRules: 3}), st: st, dir: dir, eng: eng, rec: rec}
}

func validInput() Input {
	return Input{Name: " Invoices ", SortOrder: 5, Match: "ANY",
		Conditions: []store.Condition{{Field: " subject ", Operator: "CONTAINS", Value: "invoice"}},
		Actions: []store.Action{
			{Type: store.ActionTag, TagNames: []string{" Billing ", "billing", "", "Late"}, AssigneeID: "ignored"},
			{Type: store.ActionAssign, AssigneeID: agentA},
			{Type: store.ActionStatus, Status: store.StatusPending},
			{Type: store.ActionPriority, Priority: store.PriorityHigh},
			{Type: store.ActionDrop, Status: "ignored"},
		}}
}

func (e *svcEnv) audited(t audit.EventType, outcome string) bool {
	for _, ev := range e.rec.events {
		if ev.EventType == t && ev.Outcome == outcome {
			return true
		}
	}
	return false
}

func TestServiceCreateNormalises(t *testing.T) {
	e := newSvcEnv(t)
	ctx := context.Background()
	r, err := e.svc.Create(ctx, admin(tenantA), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "Invoices" || !r.Enabled || r.Match != store.MatchAny || r.Version != 1 || r.TenantID != tenantA || r.SortOrder != 5 {
		t.Fatalf("rule = %+v", r)
	}
	if c := r.Conditions[0]; c.Field != "subject" || c.Operator != "contains" {
		t.Fatalf("condition = %+v", c)
	}
	tag := r.Actions[0]
	if tag.TagKind != store.KindTag || strings.Join(tag.TagNames, ",") != "Billing,Late" || tag.AssigneeID != "" {
		t.Fatalf("tag action = %+v", tag)
	}
	if r.Actions[4].Status != "" {
		t.Fatalf("drop action kept foreign fields: %+v", r.Actions[4])
	}
	if !e.audited(audit.RuleCreate, audit.OutcomeOK) {
		t.Fatal("create not audited")
	}
	in := validInput()
	in.Enabled = ptrTo(false)
	if r2, err := e.svc.Create(ctx, admin(tenantA), in); err != nil || r2.Enabled {
		t.Fatalf("disabled create = %+v %v", r2, err)
	}
}

func TestServiceValidation(t *testing.T) {
	e := newSvcEnv(t)
	ctx := context.Background()
	mutate := []func(*Input){
		func(in *Input) { in.Name = "" },
		func(in *Input) { in.Name = strings.Repeat("n", MaxName+1) },
		func(in *Input) { in.Name = "a\x00b" },
		func(in *Input) { in.SortOrder = MaxSort + 1 },
		func(in *Input) { in.Match = "some" },
		func(in *Input) { in.Actions = nil },
		func(in *Input) { in.Actions = make([]store.Action, MaxActions+1) },
		func(in *Input) { in.Actions = []store.Action{{Type: "explode"}} },
		func(in *Input) {
			in.Actions = []store.Action{{Type: store.ActionTag, TagKind: "label", TagNames: []string{"x"}}}
		},
		func(in *Input) {
			in.Actions = []store.Action{{Type: store.ActionTag, TagNames: []string{strings.Repeat("t", MaxTagName+1)}}}
		},
		func(in *Input) {
			names := make([]string, MaxTagNames+1)
			for i := range names {
				names[i] = strings.Repeat("t", i+1)
			}
			in.Actions = []store.Action{{Type: store.ActionTag, TagNames: names}}
		},
		func(in *Input) { in.Actions = []store.Action{{Type: store.ActionAssign, AssigneeID: " "}} },
		func(in *Input) {
			in.Conditions = []store.Condition{{Field: "subject", Operator: "matches", Value: "["}}
		},
		func(in *Input) { in.Expression = "subject" },
		// Conditions kept beside an overriding expression must still be valid.
		func(in *Input) {
			in.Expression = "true"
			in.Conditions = []store.Condition{{Field: "nope", Operator: "contains", Value: "x"}}
		},
	}
	for i, m := range mutate {
		in := validInput()
		m(&in)
		var ie *InvalidError
		if _, err := e.svc.Create(ctx, admin(tenantA), in); !errors.As(err, &ie) {
			t.Fatalf("case %d: %v, want InvalidError", i, err)
		}
	}
	if !e.audited(audit.RuleCreate, audit.OutcomeRefused) {
		t.Fatal("refusal not audited")
	}
	in := validInput()
	in.Actions = []store.Action{{Type: store.ActionAssign, AssigneeID: agentB}}
	if _, err := e.svc.Create(ctx, admin(tenantA), in); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("not assignable = %v", err)
	}
	in.Actions = []store.Action{{Type: store.ActionAssign, AssigneeID: "ghost"}}
	if _, err := e.svc.Create(ctx, admin(tenantA), in); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("unknown = %v", err)
	}
	e.dir.Err = errors.New("auth down")
	if _, err := e.svc.Create(ctx, admin(tenantA), validInput()); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("directory down = %v", err)
	}
	noDir := NewService(Deps{Store: e.st, Engine: e.eng})
	if _, err := noDir.Create(ctx, admin(tenantA), validInput()); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("no directory = %v", err)
	}
	if _, err := e.svc.Create(ctx, authz.Subjects{}, validInput()); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("no tenant = %v", err)
	}
}

func TestServiceUpdateBumpsVersionAndEvicts(t *testing.T) {
	e := newSvcEnv(t)
	ctx := context.Background()
	r, _ := e.svc.Create(ctx, admin(tenantA), validInput())
	stored, _ := e.st.GetRule(ctx, tenantA, r.ID)
	if _, err := e.eng.Program(stored); err != nil || e.eng.CacheLen() != 1 {
		t.Fatal("program not cached")
	}
	in := validInput()
	in.Expression = `spamScore > 3.0`
	up, err := e.svc.Update(ctx, admin(tenantA), r.ID, in)
	if err != nil || up.Version != 2 || up.Expression != "spamScore > 3.0" || !up.CreatedAt.Equal(r.CreatedAt) {
		t.Fatalf("update = %+v %v", up, err)
	}
	p, _ := e.eng.Program(up)
	if p.Expr != "spamScore > 3.0" {
		t.Fatalf("stale cached program %q", p.Expr)
	}
	if _, err := e.svc.Update(ctx, admin(tenantB), r.ID, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant update = %v", err)
	}
	bad := validInput()
	bad.Name = ""
	if _, err := e.svc.Update(ctx, admin(tenantA), r.ID, bad); err == nil {
		t.Fatal("invalid update accepted")
	}
	if got, _ := e.svc.Get(ctx, admin(tenantA), r.ID); got.Version != 2 {
		t.Fatalf("refused update changed the rule: %+v", got)
	}
	if err := e.svc.Delete(ctx, admin(tenantB), r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant delete = %v", err)
	}
	if err := e.svc.Delete(ctx, admin(tenantA), r.ID); err != nil || e.eng.CacheLen() != 0 {
		t.Fatalf("delete = %v cache %d", err, e.eng.CacheLen())
	}
	if _, err := e.svc.Get(ctx, admin(tenantA), r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
	if !e.audited(audit.RuleUpdate, audit.OutcomeOK) || !e.audited(audit.RuleDelete, audit.OutcomeOK) {
		t.Fatal("update/delete not audited")
	}
}

func TestServiceListAndLimit(t *testing.T) {
	e := newSvcEnv(t)
	ctx := context.Background()
	if l, err := e.svc.List(ctx, admin(tenantA)); err != nil || l == nil || len(l) != 0 {
		t.Fatalf("empty list = %#v %v", l, err)
	}
	for _, sort := range []int{30, 10, 20} {
		in := validInput()
		in.SortOrder = sort
		if _, err := e.svc.Create(ctx, admin(tenantA), in); err != nil {
			t.Fatal(err)
		}
	}
	l, _ := e.svc.List(ctx, admin(tenantA))
	if len(l) != 3 || l[0].SortOrder != 10 || l[2].SortOrder != 30 {
		t.Fatalf("order = %+v", l)
	}
	var ie *InvalidError
	if _, err := e.svc.Create(ctx, admin(tenantA), validInput()); !errors.As(err, &ie) {
		t.Fatalf("limit = %v", err)
	}
	if l, _ := e.svc.List(ctx, admin(tenantB)); len(l) != 0 {
		t.Fatal("tenant leak")
	}
	e.st.FailNext("ListRules")
	if _, err := e.svc.List(ctx, admin(tenantA)); err == nil {
		t.Fatal("store failure hidden")
	}
}

func TestServiceDryRun(t *testing.T) {
	e := newSvcEnv(t)
	ctx := context.Background()
	in := validInput()
	res, err := e.svc.Test(ctx, admin(tenantA), in, Sample{Subject: "Invoice 7"})
	if err != nil || !res.Matched || len(res.Actions) != 5 {
		t.Fatalf("match = %+v %v", res, err)
	}
	res, err = e.svc.Test(ctx, admin(tenantA), in, Sample{Subject: "hello"})
	if err != nil || res.Matched || res.Actions == nil || len(res.Actions) != 0 {
		t.Fatalf("no match = %+v %v", res, err)
	}
	// Assignees are not looked up for a dry run.
	in.Actions = []store.Action{{Type: store.ActionAssign, AssigneeID: "ghost"}}
	if _, err := e.svc.Test(ctx, admin(tenantA), in, Sample{}); err != nil {
		t.Fatalf("dry run looked up the assignee: %v", err)
	}
	in.Expression = `int(subject) > 0`
	var ie *InvalidError
	if _, err := e.svc.Test(ctx, admin(tenantA), in, Sample{Subject: "x"}); !errors.As(err, &ie) {
		t.Fatalf("runtime error = %v", err)
	}
	if _, err := e.svc.Test(ctx, authz.Subjects{}, in, Sample{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("no tenant = %v", err)
	}
	if l, _ := e.svc.List(ctx, admin(tenantA)); len(l) != 0 {
		t.Fatal("dry run stored a rule")
	}
	s := Sample{From: "a@B.Example", Body: strings.Repeat("x", MaxBodyEval+10)}.Email()
	if s.FromDomain != "b.example" || len(s.Body) != MaxBodyEval {
		t.Fatalf("sample email = %q %d", s.FromDomain, len(s.Body))
	}
}
