package contract

// T024: the tickets surface of contracts §A over the full HTTP chain (OpenAPI
// validation, platform token, per-route permission, handlers, service,
// in-memory store): shapes, filters and paging, lifecycle reasons, tenant
// isolation and viewer refusal on every mutation.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/go-freya/freya/internal/testrt"
	"github.com/go-freya/freya/internal/testutil"
	"github.com/go-freya/freya/services/auth/pkg/authclient"

	"github.com/go-freya/freya/services/ticket/internal/agents"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/blob"
	"github.com/go-freya/freya/services/ticket/internal/events"
	"github.com/go-freya/freya/services/ticket/internal/httpapi"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

const (
	tenantA = "11111111-1111-7111-8111-111111111111"
	tenantB = "22222222-2222-7222-8222-222222222222"
	adminA  = "33333333-3333-7333-8333-333333333333"
	viewerA = "44444444-4444-7444-8444-444444444444"
	agentA  = "55555555-5555-7555-8555-555555555555"
	agentB  = "66666666-6666-7666-8666-666666666666"
	p       = "/api/ticket/v1"
)

type verifier map[string]authclient.Identity

func (v verifier) Verify(_ context.Context, tok string) (authclient.Identity, error) {
	if id, ok := v[tok]; ok {
		return id, nil
	}
	return authclient.Identity{}, errors.New("unauthenticated")
}

type harness struct {
	t   *testing.T
	s   *httpapi.Server
	rtr routers.Router
	st  *memstore.Mem
	pub *events.Recorder
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	rt := testrt.New(t, testutil.MustCA("example.org"), "ticket")
	v := verifier{
		"admin-a":  {UserID: adminA, TenantID: tenantA},
		"viewer-a": {UserID: viewerA, TenantID: tenantA},
		"agent-a":  {UserID: agentA, TenantID: tenantA},
		"agent-b":  {UserID: agentB, TenantID: tenantB},
	}
	all := []string{authz.TicketsRead, authz.TicketsManage, authz.TicketsDelete}
	checker := authz.Static{adminA: all, agentA: {authz.TicketsRead, authz.TicketsManage}, agentB: all, viewerA: {authz.TicketsRead}}
	s, err := httpapi.NewHandler(rt, httpapi.WithVerifier(v), httpapi.WithChecker(checker))
	if err != nil {
		t.Fatal(err)
	}
	dir := agents.NewFake()
	dir.Add(tenantA, agents.User{ID: agentA, Name: "Ada Agent"}, true)
	dir.Add(tenantA, agents.User{ID: adminA, Name: "Alan Admin"}, true)
	dir.Add(tenantA, agents.User{ID: viewerA, Name: "Vic Viewer"}, false)
	dir.Add(tenantB, agents.User{ID: agentB, Name: "Bea"}, true)
	doc, err := httpapi.LoadDocument()
	if err != nil {
		t.Fatal(err)
	}
	doc.Servers = nil
	rtr, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, s: s, rtr: rtr, st: memstore.New(), pub: &events.Recorder{}}
	svc := tickets.New(tickets.Deps{Store: h.st, Agents: dir, Events: h.pub, Blobs: blob.NewFake()})
	s.Register(httpapi.Deps{Tickets: svc})
	return h
}

func (h *harness) do(method, path, tok, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if method != http.MethodGet {
		r.Header.Set("X-CSRF-Token", "csrf")
	}
	w := httptest.NewRecorder()
	h.s.Handler().ServeHTTP(w, r)
	h.checkResponse(method, path, tok, w)
	return w
}

// checkResponse validates a successful response body against the OpenAPI
// document so the handlers and the contract cannot drift.
func (h *harness) checkResponse(method, path, tok string, w *httptest.ResponseRecorder) {
	h.t.Helper()
	if w.Code >= 300 {
		return
	}
	r := httptest.NewRequest(method, path, nil)
	route, params, err := h.rtr.FindRoute(r)
	if err != nil {
		h.t.Fatalf("%s %s: no route: %v", method, path, err)
	}
	in := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	res := &openapi3filter.ResponseValidationInput{RequestValidationInput: in, Status: w.Code, Header: w.Header(),
		Body: io.NopCloser(bytes.NewReader(w.Body.Bytes())), Options: &openapi3filter.Options{IncludeResponseStatus: true}}
	if err := openapi3filter.ValidateResponse(context.Background(), res); err != nil {
		h.t.Errorf("%s %s (%s) response violates the contract: %v\n%s", method, path, tok, err, w.Body)
	}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}

type ticketJSON struct {
	ID             string           `json:"id"`
	Subject        string           `json:"subject"`
	Description    string           `json:"description"`
	Status         string           `json:"status"`
	Priority       string           `json:"priority"`
	Source         string           `json:"source"`
	RequesterEmail string           `json:"requester_email"`
	AssigneeID     string           `json:"assignee_id"`
	AssigneeName   string           `json:"assignee_name"`
	CommentCount   *int             `json:"comment_count"`
	Tags           []map[string]any `json:"tags"`
	ResolvedAt     *string          `json:"resolved_at"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      string           `json:"updated_at"`
}

type pageJSON struct {
	Items []ticketJSON `json:"items"`
	Total *int         `json:"total"`
}

func reasonOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	return decode[map[string]any](t, w)["reason"].(string)
}

func (h *harness) create(tok, body string) ticketJSON {
	h.t.Helper()
	w := h.do("POST", p+"/tickets", tok, body)
	if w.Code != http.StatusCreated {
		h.t.Fatalf("create %s = %d %s", body, w.Code, w.Body)
	}
	return decode[ticketJSON](h.t, w)
}

func TestTicketLifecycleShapes(t *testing.T) {
	h := newHarness(t)
	tk := h.create("agent-a", `{"subject":"Printer on fire","description":"smoke","requester_email":"carol@example.org","requester_name":"Carol"}`)
	if tk.ID == "" || tk.Status != "open" || tk.Priority != "normal" || tk.Source != "manual" || tk.CommentCount == nil ||
		tk.Tags == nil || tk.CreatedAt == "" || tk.UpdatedAt == "" || tk.ResolvedAt != nil {
		t.Fatalf("create shape = %+v", tk)
	}
	if strings.Contains(h.do("GET", p+"/tickets/"+tk.ID, "agent-a", "").Body.String(), "body_html") {
		t.Fatal("raw html field exposed")
	}

	w := h.do("GET", p+"/tickets/"+tk.ID, "viewer-a", "")
	if w.Code != 200 || decode[ticketJSON](t, w).Subject != "Printer on fire" {
		t.Fatalf("get = %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", p+"/tickets/"+store.NewID(), "agent-a", ""); w.Code != 404 || reasonOf(t, w) != "ticket_not_found" {
		t.Fatalf("missing = %d %s", w.Code, w.Body)
	}

	// partial update
	w = h.do("PUT", p+"/tickets/"+tk.ID, "agent-a", `{"priority":"high"}`)
	up := decode[ticketJSON](t, w)
	if w.Code != 200 || up.Priority != "high" || up.Subject != "Printer on fire" || up.Description != "smoke" {
		t.Fatalf("update = %d %s", w.Code, w.Body)
	}
	if w := h.do("PUT", p+"/tickets/"+tk.ID, "agent-a", `{"subject":""}`); w.Code != 422 {
		t.Fatalf("empty subject = %d", w.Code)
	}

	// assign / unassign
	w = h.do("POST", p+"/tickets/"+tk.ID+"/assign", "agent-a", `{"assignee_id":"`+agentA+`"}`)
	as := decode[ticketJSON](t, w)
	if w.Code != 200 || as.AssigneeID != agentA || as.AssigneeName != "Ada Agent" {
		t.Fatalf("assign = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/tickets/"+tk.ID+"/assign", "agent-a", `{"assignee_id":"`+viewerA+`"}`); w.Code != 422 || reasonOf(t, w) != "invalid_assignee" {
		t.Fatalf("non-agent assignee = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/tickets/"+tk.ID+"/assign", "agent-a", `{"assignee_id":"nobody-known"}`); w.Code != 422 || reasonOf(t, w) != "invalid_assignee" {
		t.Fatalf("unknown assignee = %d %s", w.Code, w.Body)
	}
	w = h.do("POST", p+"/tickets/"+tk.ID+"/assign", "agent-a", `{"assignee_id":null}`)
	if un := decode[ticketJSON](t, w); w.Code != 200 || un.AssigneeID != "" || un.AssigneeName != "" {
		t.Fatalf("unassign = %d %s", w.Code, w.Body)
	}

	// status
	w = h.do("POST", p+"/tickets/"+tk.ID+"/status", "agent-a", `{"status":"resolved"}`)
	if st := decode[ticketJSON](t, w); w.Code != 200 || st.Status != "resolved" || st.ResolvedAt == nil {
		t.Fatalf("resolve = %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"unspecified", "", "done"} {
		if w := h.do("POST", p+"/tickets/"+tk.ID+"/status", "agent-a", `{"status":"`+bad+`"}`); w.Code != 422 || reasonOf(t, w) != "invalid_status" {
			t.Errorf("status %q = %d %s", bad, w.Code, w.Body)
		}
	}
	w = h.do("POST", p+"/tickets/"+tk.ID+"/status", "agent-a", `{"status":"in_progress"}`)
	if st := decode[ticketJSON](t, w); st.ResolvedAt != nil || st.Status != "in_progress" {
		t.Fatalf("reopen = %s", w.Body)
	}

	// history
	w = h.do("GET", p+"/tickets/"+tk.ID+"/history", "viewer-a", "")
	hist := decode[struct {
		Items []map[string]any `json:"items"`
	}](t, w)
	if w.Code != 200 || len(hist.Items) != 5 {
		t.Fatalf("history = %d %s", w.Code, w.Body)
	}
	first := hist.Items[0]
	for _, k := range []string{"id", "field", "old_value", "new_value", "actor_kind", "created_at"} {
		if _, ok := first[k]; !ok {
			t.Errorf("history entry lacks %s: %v", k, first)
		}
	}
	if first["field"] != "priority" || hist.Items[4]["new_value"] != "in_progress" {
		t.Fatalf("history order = %v", hist.Items)
	}

	// assignable users
	w = h.do("GET", p+"/assignable-users", "viewer-a", "")
	users := decode[struct {
		Items []map[string]any `json:"items"`
	}](t, w)
	if w.Code != 200 || len(users.Items) != 2 || users.Items[0]["name"] != "Ada Agent" {
		t.Fatalf("assignable = %d %s", w.Code, w.Body)
	}

	// delete needs tickets:delete
	if w := h.do("DELETE", p+"/tickets/"+tk.ID, "agent-a", ""); w.Code != 403 {
		t.Fatalf("agent delete = %d", w.Code)
	}
	if w := h.do("DELETE", p+"/tickets/"+tk.ID, "admin-a", ""); w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", p+"/tickets/"+tk.ID, "admin-a", ""); w.Code != 404 {
		t.Fatalf("after delete = %d", w.Code)
	}
	if w := h.do("DELETE", p+"/tickets/"+tk.ID, "admin-a", ""); w.Code != 404 {
		t.Fatalf("double delete = %d", w.Code)
	}

	// events never carry bodies or requester addresses
	raw, _ := json.Marshal(h.pub.Events)
	for _, leak := range []string{"smoke", "carol@example.org", "Carol"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("event payload leaks %q: %s", leak, raw)
		}
	}
}

func TestTicketListFiltersAndPaging(t *testing.T) {
	h := newHarness(t)
	a := h.create("agent-a", `{"subject":"Printer jam","requester_name":"Carol","requester_email":"carol@example.org"}`)
	b := h.create("agent-a", `{"subject":"VPN down","priority":"urgent","assignee_id":"`+agentA+`"}`)
	c := h.create("agent-a", `{"subject":"Email quota"}`)
	h.create("agent-b", `{"subject":"Printer in tenant B"}`)
	if w := h.do("POST", p+"/tickets/"+c.ID+"/status", "agent-a", `{"status":"pending"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	tag := store.Tag{ID: store.NewID(), TenantID: tenantA, Name: "hardware", Kind: store.KindTag}
	if err := h.st.CreateTag(context.Background(), tag); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetTicketTags(context.Background(), tenantA, a.ID, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}

	list := func(q url.Values) pageJSON {
		t.Helper()
		w := h.do("GET", p+"/tickets?"+q.Encode(), "viewer-a", "")
		if w.Code != 200 {
			t.Fatalf("list %s = %d %s", q.Encode(), w.Code, w.Body)
		}
		pg := decode[pageJSON](t, w)
		if pg.Total == nil || pg.Items == nil {
			t.Fatalf("page shape: %s", w.Body)
		}
		return pg
	}
	ids := func(pg pageJSON) string {
		out := []string{}
		for _, it := range pg.Items {
			out = append(out, it.ID)
		}
		return strings.Join(out, ",")
	}
	if pg := list(url.Values{}); *pg.Total != 3 || ids(pg) != strings.Join([]string{c.ID, b.ID, a.ID}, ",") {
		t.Fatalf("all (newest first) = %s", ids(pg))
	}
	cases := []struct {
		q    url.Values
		want string
	}{
		{url.Values{"status": {"pending"}}, c.ID},
		{url.Values{"priority": {"urgent"}}, b.ID},
		{url.Values{"assignee_id": {agentA}}, b.ID},
		{url.Values{"assignee_id": {"none"}}, c.ID + "," + a.ID},
		{url.Values{"tag_id": {tag.ID}}, a.ID},
		{url.Values{"query": {"printer"}}, a.ID},
		{url.Values{"query": {"CAROL@example"}}, a.ID},
		{url.Values{"status": {"open"}, "assignee_id": {"none"}}, a.ID},
	}
	for _, cs := range cases {
		if got := ids(list(cs.q)); got != cs.want {
			t.Errorf("%s = %s, want %s", cs.q.Encode(), got, cs.want)
		}
	}
	pg := list(url.Values{"page": {"2"}, "page_size": {"2"}})
	if *pg.Total != 3 || ids(pg) != a.ID {
		t.Fatalf("page 2 = %d %s", *pg.Total, ids(pg))
	}
	if pg := list(url.Values{"page": {"9"}}); *pg.Total != 3 || len(pg.Items) != 0 {
		t.Fatalf("past the end = %+v", pg)
	}
	if pg := list(url.Values{}); len(pg.Items[2].Tags) != 1 || pg.Items[2].Tags[0]["name"] != "hardware" {
		t.Fatalf("items carry tags: %+v", pg.Items[2])
	}
	if w := h.do("GET", p+"/tickets?status=unspecified", "viewer-a", ""); w.Code != 422 || reasonOf(t, w) != "invalid_status" {
		t.Fatalf("bad status filter = %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", p+"/tickets?priority=meh", "viewer-a", ""); w.Code != 422 {
		t.Fatalf("bad priority filter = %d", w.Code)
	}
	if w := h.do("GET", p+"/tickets?page_size=101", "viewer-a", ""); w.Code != 422 {
		t.Fatalf("page_size cap = %d", w.Code)
	}
}

func TestTicketTenantIsolation(t *testing.T) {
	h := newHarness(t)
	a := h.create("agent-a", `{"subject":"Tenant A secret"}`)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/tickets/" + a.ID, ""},
		{"GET", "/tickets/" + a.ID + "/history", ""},
		{"PUT", "/tickets/" + a.ID, `{"subject":"pwned"}`},
		{"POST", "/tickets/" + a.ID + "/assign", `{"assignee_id":"` + agentB + `"}`},
		{"POST", "/tickets/" + a.ID + "/status", `{"status":"closed"}`},
		{"DELETE", "/tickets/" + a.ID, ""},
	} {
		if w := h.do(c.method, p+c.path, "agent-b", c.body); w.Code != 404 {
			t.Errorf("tenant B %s %s = %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	w := h.do("GET", p+"/tickets", "agent-b", "")
	if pg := decode[pageJSON](t, w); *pg.Total != 0 {
		t.Fatalf("tenant B sees %d tickets", *pg.Total)
	}
	if got := decode[ticketJSON](t, h.do("GET", p+"/tickets/"+a.ID, "agent-a", "")); got.Subject != "Tenant A secret" || got.Status != "open" {
		t.Fatalf("tenant A ticket changed: %+v", got)
	}
}

func TestViewerIsRefusedEveryMutation(t *testing.T) {
	h := newHarness(t)
	a := h.create("agent-a", `{"subject":"Read only"}`)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/tickets", `{"subject":"x"}`},
		{"PUT", "/tickets/" + a.ID, `{"subject":"x"}`},
		{"DELETE", "/tickets/" + a.ID, ""},
		{"POST", "/tickets/" + a.ID + "/assign", `{"assignee_id":null}`},
		{"POST", "/tickets/" + a.ID + "/status", `{"status":"closed"}`},
	} {
		if w := h.do(c.method, p+c.path, "viewer-a", c.body); w.Code != 403 || reasonOf(t, w) != "forbidden" {
			t.Errorf("viewer %s %s = %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if w := h.do("GET", p+"/tickets", "", ""); w.Code != 401 {
		t.Fatalf("anonymous = %d", w.Code)
	}
}
