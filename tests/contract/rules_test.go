package contract

// T054: the rules surface of contracts §A — CRUD shapes, validation refusals
// (invalid_rule with the reason, invalid_assignee), the /rules/test dry run,
// rules:manage required on every route and tenant isolation.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/events"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/httpapi"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/rules"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tags"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tickets"
)

const taggerA = "77777777-7777-7777-8777-777777777777"

// newTriageHarness mounts tickets, tags and rules. agent-a holds
// tickets:read+manage only, tagger-a tickets:read+tags:manage, viewer-a
// tickets:read, admin-a and agent-b everything (in their tenants).
func newTriageHarness(t *testing.T) *harness {
	t.Helper()
	rt := testrt.New(t, testutil.MustCA("example.org"), "ticket")
	v := verifier{
		"admin-a":  {UserID: adminA, TenantID: tenantA},
		"viewer-a": {UserID: viewerA, TenantID: tenantA},
		"agent-a":  {UserID: agentA, TenantID: tenantA},
		"tagger-a": {UserID: taggerA, TenantID: tenantA},
		"agent-b":  {UserID: agentB, TenantID: tenantB},
	}
	all := append([]string{}, authz.Permissions...)
	checker := authz.Static{adminA: all, agentA: {authz.TicketsRead, authz.TicketsManage}, taggerA: {authz.TicketsRead, authz.TagsManage},
		agentB: all, viewerA: {authz.TicketsRead}}
	s, err := httpapi.NewHandler(rt, httpapi.WithVerifier(v), httpapi.WithChecker(checker))
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := httpapi.LoadDocument()
	doc.Servers = nil
	rtr, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := agents.NewFake()
	dir.Add(tenantA, agents.User{ID: agentA, Name: "Ada Agent"}, true)
	dir.Add(tenantA, agents.User{ID: viewerA, Name: "Vic Viewer"}, false)
	dir.Add(tenantB, agents.User{ID: agentB, Name: "Bea"}, true)
	h := &harness{t: t, s: s, rtr: rtr, st: memstore.New(), pub: &events.Recorder{}}
	engine, err := rules.NewEngine(rules.Config{CostLimit: 100000, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s.Register(httpapi.Deps{
		Tickets: tickets.New(tickets.Deps{Store: h.st, Agents: dir, Events: h.pub}),
		Tags:    tags.New(tags.Deps{Store: h.st}),
		Rules:   rules.NewService(rules.Deps{Store: h.st, Engine: engine, Agents: dir, MaxRules: 5}),
	})
	return h
}

type ruleJSON struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Enabled    bool                `json:"enabled"`
	SortOrder  int                 `json:"sort_order"`
	Match      string              `json:"match"`
	Conditions []map[string]string `json:"conditions"`
	Expression string              `json:"expression"`
	Actions    []map[string]any    `json:"actions"`
	Version    int                 `json:"version"`
	TenantID   string              `json:"tenant_id"`
}

type refusal struct {
	Reason string         `json:"reason"`
	Detail map[string]any `json:"detail"`
}

const invoiceRule = `{"name":"Invoices","sort_order":10,"match":"all","conditions":[{"field":"subject","operator":"contains","value":"invoice"}],` +
	`"actions":[{"type":"tag","tag_kind":"category","tag_names":["Billing"]},{"type":"assign","assignee_id":"` + agentA + `"}]}`

func TestRulesCRUDShapes(t *testing.T) {
	h := newTriageHarness(t)
	w := h.do("POST", p+"/rules", "admin-a", invoiceRule)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	r := decode[ruleJSON](t, w)
	if r.ID == "" || r.Name != "Invoices" || !r.Enabled || r.Match != "all" || r.Version != 1 || len(r.Conditions) != 1 || len(r.Actions) != 2 {
		t.Fatalf("rule = %+v", r)
	}
	w = h.do("POST", p+"/rules", "admin-a", `{"name":"Spam","sort_order":1,"enabled":false,"expression":"spamScore > 5.0","actions":[{"type":"drop"}]}`)
	if w.Code != 201 {
		t.Fatalf("create expression rule = %d %s", w.Code, w.Body)
	}
	spam := decode[ruleJSON](t, w)
	if spam.Enabled || spam.Expression != "spamScore > 5.0" || spam.Match != "all" {
		t.Fatalf("spam rule = %+v", spam)
	}
	list := decode[struct{ Items []ruleJSON }](t, h.do("GET", p+"/rules", "admin-a", "")).Items
	if len(list) != 2 || list[0].ID != spam.ID || list[1].ID != r.ID {
		t.Fatalf("list not in sort order: %+v", list)
	}
	if got := decode[ruleJSON](t, h.do("GET", p+"/rules/"+r.ID, "admin-a", "")); got.ID != r.ID {
		t.Fatalf("get = %+v", got)
	}
	w = h.do("PUT", p+"/rules/"+r.ID, "admin-a", `{"name":"Invoices v2","enabled":true,"match":"any","conditions":[{"field":"fromDomain","operator":"equals","value":"billing.example"},{"field":"subject","operator":"contains","value":"invoice"}],"actions":[{"type":"priority","priority":"high"}]}`)
	if w.Code != 200 {
		t.Fatalf("update = %d %s", w.Code, w.Body)
	}
	up := decode[ruleJSON](t, w)
	if up.Version != 2 || up.Name != "Invoices v2" || up.Match != "any" || len(up.Actions) != 1 || up.SortOrder != 0 {
		t.Fatalf("updated = %+v", up)
	}
	if w := h.do("GET", p+"/rules/"+r.ID, "agent-b", ""); w.Code != 404 {
		t.Fatalf("cross-tenant get = %d", w.Code)
	}
	if w := h.do("PUT", p+"/rules/"+r.ID, "agent-b", invoiceRule); w.Code != 404 {
		t.Fatalf("cross-tenant update = %d %s", w.Code, w.Body)
	}
	if w := h.do("DELETE", p+"/rules/"+r.ID, "agent-b", ""); w.Code != 404 {
		t.Fatalf("cross-tenant delete = %d", w.Code)
	}
	if w := h.do("DELETE", p+"/rules/"+r.ID, "admin-a", ""); w.Code != 204 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", p+"/rules/"+r.ID, "admin-a", ""); w.Code != 404 {
		t.Fatalf("get deleted = %d", w.Code)
	}
}

func TestRuleValidationRefusals(t *testing.T) {
	h := newTriageHarness(t)
	cases := []struct {
		name, body, reason, field string
	}{
		{"unknown field", `{"name":"x","conditions":[{"field":"bogus","operator":"contains","value":"x"}],"actions":[{"type":"drop"}]}`, "invalid_rule", "conditions[0].field"},
		{"bad operator", `{"name":"x","conditions":[{"field":"spamScore","operator":"contains","value":"5"}],"actions":[{"type":"drop"}]}`, "invalid_rule", "conditions[0].operator"},
		{"bad regex", `{"name":"x","conditions":[{"field":"subject","operator":"matches","value":"("}],"actions":[{"type":"drop"}]}`, "invalid_rule", "conditions[0].value"},
		{"bad expression", `{"name":"x","expression":"subject +","actions":[{"type":"drop"}]}`, "invalid_rule", "expression"},
		{"non-boolean expression", `{"name":"x","expression":"subject","actions":[{"type":"drop"}]}`, "invalid_rule", "expression"},
		{"no condition", `{"name":"x","actions":[{"type":"drop"}]}`, "invalid_rule", "conditions"},
		{"blank name", `{"name":"  ","expression":"true","actions":[{"type":"drop"}]}`, "invalid_rule", "name"},
		{"bad status", `{"name":"x","expression":"true","actions":[{"type":"status","status":"weird"}]}`, "invalid_rule", "actions[0].status"},
		{"bad priority", `{"name":"x","expression":"true","actions":[{"type":"priority"}]}`, "invalid_rule", "actions[0].priority"},
		{"tag without names", `{"name":"x","expression":"true","actions":[{"type":"tag","tag_names":[" "]}]}`, "invalid_rule", "actions[0].tag_names"},
		{"assign without id", `{"name":"x","expression":"true","actions":[{"type":"assign"}]}`, "invalid_rule", "actions[0].assignee_id"},
		{"unassignable", `{"name":"x","expression":"true","actions":[{"type":"assign","assignee_id":"` + viewerA + `"}]}`, "invalid_assignee", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do("POST", p+"/rules", "admin-a", tc.body)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("= %d %s", w.Code, w.Body)
			}
			ref := decode[refusal](t, w)
			if ref.Reason != tc.reason {
				t.Fatalf("reason = %q", ref.Reason)
			}
			if tc.field != "" && (ref.Detail["field"] != tc.field || ref.Detail["message"] == "") {
				t.Fatalf("detail = %+v", ref.Detail)
			}
		})
	}
	// Schema violations are refused before the service.
	for _, body := range []string{`{"name":"x"}`, `{"name":"x","actions":[]}`, `{"name":"x","actions":[{"type":"explode"}]}`, `{"name":"x","actions":[{"type":"drop"}],"extra":1}`} {
		if w := h.do("POST", p+"/rules", "admin-a", body); w.Code != 400 && w.Code != 422 {
			t.Fatalf("%s = %d", body, w.Code)
		}
	}
	// Nothing was stored.
	if list := decode[struct{ Items []ruleJSON }](t, h.do("GET", p+"/rules", "admin-a", "")).Items; len(list) != 0 {
		t.Fatalf("refused rules stored: %+v", list)
	}
	// The per-tenant rule limit (5 in the harness).
	for i := 0; i < 5; i++ {
		if w := h.do("POST", p+"/rules", "admin-a", `{"name":"r","expression":"true","actions":[{"type":"drop"}]}`); w.Code != 201 {
			t.Fatalf("create %d = %d", i, w.Code)
		}
	}
	w := h.do("POST", p+"/rules", "admin-a", `{"name":"r","expression":"true","actions":[{"type":"drop"}]}`)
	if w.Code != 422 || reasonOf(t, w) != "invalid_rule" {
		t.Fatalf("limit = %d %s", w.Code, w.Body)
	}
}

func TestRuleDryRun(t *testing.T) {
	h := newTriageHarness(t)
	body := `{"rule":` + invoiceRule + `,"sample":{"subject":"Your INVOICE 42","from":"ann@billing.example","spam_score":1.5}}`
	w := h.do("POST", p+"/rules/test", "admin-a", body)
	if w.Code != 200 {
		t.Fatalf("test = %d %s", w.Code, w.Body)
	}
	res := decode[struct {
		Matched bool             `json:"matched"`
		Actions []map[string]any `json:"actions"`
	}](t, w)
	if !res.Matched || len(res.Actions) != 2 {
		t.Fatalf("result = %+v", res)
	}
	w = h.do("POST", p+"/rules/test", "admin-a", `{"rule":`+invoiceRule+`,"sample":{"subject":"hello"}}`)
	res = decode[struct {
		Matched bool             `json:"matched"`
		Actions []map[string]any `json:"actions"`
	}](t, w)
	if w.Code != 200 || res.Matched || res.Actions == nil || len(res.Actions) != 0 {
		t.Fatalf("no match = %d %s", w.Code, w.Body)
	}
	// fromDomain is derived from the sample sender.
	dom := `{"rule":{"name":"d","conditions":[{"field":"fromDomain","operator":"equals","value":"BILLING.example"}],"actions":[{"type":"drop"}]},"sample":{"from":"x@billing.example"}}`
	if !decode[struct{ Matched bool }](t, h.do("POST", p+"/rules/test", "admin-a", dom)).Matched {
		t.Fatal("fromDomain not derived")
	}
	w = h.do("POST", p+"/rules/test", "admin-a", `{"rule":{"name":"x","expression":"nope(","actions":[{"type":"drop"}]},"sample":{}}`)
	if w.Code != 422 || reasonOf(t, w) != "invalid_rule" {
		t.Fatalf("invalid dry run = %d %s", w.Code, w.Body)
	}
	// The dry run stores nothing.
	if list := decode[struct{ Items []ruleJSON }](t, h.do("GET", p+"/rules", "admin-a", "")).Items; len(list) != 0 {
		t.Fatalf("dry run stored a rule: %+v", list)
	}
}

func TestRulesRequireRulesManage(t *testing.T) {
	h := newTriageHarness(t)
	r := decode[ruleJSON](t, h.do("POST", p+"/rules", "admin-a", invoiceRule))
	for _, tok := range []string{"agent-a", "viewer-a", "tagger-a"} {
		for _, c := range []struct{ m, path, body string }{
			{"GET", p + "/rules", ""},
			{"GET", p + "/rules/" + r.ID, ""},
			{"POST", p + "/rules", invoiceRule},
			{"PUT", p + "/rules/" + r.ID, invoiceRule},
			{"DELETE", p + "/rules/" + r.ID, ""},
			{"POST", p + "/rules/test", `{"rule":` + invoiceRule + `,"sample":{}}`},
		} {
			if w := h.do(c.m, c.path, tok, c.body); w.Code != 403 {
				t.Fatalf("%s %s %s = %d", tok, c.m, c.path, w.Code)
			}
		}
	}
	if w := h.do("GET", p+"/rules", "", ""); w.Code != 401 {
		t.Fatalf("anonymous = %d", w.Code)
	}
	if !strings.Contains(h.do("GET", p+"/rules", "admin-a", "").Body.String(), r.ID) {
		t.Fatal("admin cannot list")
	}
}
