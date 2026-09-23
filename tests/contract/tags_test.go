package contract

// T060: the tags surface of contracts §A — CRUD shapes, ?kind= filtering,
// per-kind case-insensitive uniqueness, immutable kind, the set-tags route on
// tickets, the list tag filter, and the tags:manage vs tickets:manage split.

import (
	"net/http"
	"testing"
)

type tagJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

func (h *harness) tag(tok, body string) tagJSON {
	h.t.Helper()
	w := h.do("POST", p+"/tags", tok, body)
	if w.Code != http.StatusCreated {
		h.t.Fatalf("create tag %s = %d %s", body, w.Code, w.Body)
	}
	return decode[tagJSON](h.t, w)
}

func TestTagsCRUDShapes(t *testing.T) {
	h := newTriageHarness(t)
	billing := h.tag("tagger-a", `{"name":"Billing","color":"warning","description":"Money matters"}`)
	if billing.ID == "" || billing.Kind != "tag" || billing.Color != "warning" || billing.Description != "Money matters" {
		t.Fatalf("tag = %+v", billing)
	}
	cat := h.tag("admin-a", `{"name":"billing","kind":"category"}`)
	if cat.Kind != "category" || cat.Color != "" {
		t.Fatalf("category = %+v (same name, other kind is allowed; no colour = automatic)", cat)
	}
	if w := h.do("POST", p+"/tags", "admin-a", `{"name":"BILLING"}`); w.Code != 409 || reasonOf(t, w) != "conflict" {
		t.Fatalf("duplicate = %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"name":""}`, `{"name":"  "}`, `{"name":"x","color":"#zzz"}`, `{"name":"x","kind":"label"}`, `{"kind":"tag"}`} {
		if w := h.do("POST", p+"/tags", "admin-a", body); w.Code != 400 && w.Code != 422 {
			t.Fatalf("%s = %d %s", body, w.Code, w.Body)
		}
	}
	list := decode[struct{ Items []tagJSON }](t, h.do("GET", p+"/tags", "viewer-a", "")).Items
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	cats := decode[struct{ Items []tagJSON }](t, h.do("GET", p+"/tags?kind=category", "viewer-a", "")).Items
	if len(cats) != 1 || cats[0].ID != cat.ID {
		t.Fatalf("?kind=category = %+v", cats)
	}
	if w := h.do("GET", p+"/tags?kind=label", "viewer-a", ""); w.Code != 400 && w.Code != 422 {
		t.Fatalf("bad kind = %d", w.Code)
	}
	if other := decode[struct{ Items []tagJSON }](t, h.do("GET", p+"/tags", "agent-b", "")).Items; len(other) != 0 {
		t.Fatalf("tenant B sees tenant A's tags: %+v", other)
	}

	w := h.do("PUT", p+"/tags/"+billing.ID, "tagger-a", `{"name":"Invoices","color":"#1e88e5"}`)
	if w.Code != 200 {
		t.Fatalf("rename = %d %s", w.Code, w.Body)
	}
	if up := decode[tagJSON](t, w); up.Name != "Invoices" || up.Kind != "tag" || up.Color != "#1e88e5" || up.Description != "Money matters" {
		t.Fatalf("renamed = %+v", up)
	}
	if w := h.do("PUT", p+"/tags/"+billing.ID, "admin-a", `{"kind":"category"}`); w.Code != 422 {
		t.Fatalf("kind change = %d %s", w.Code, w.Body)
	}
	if w := h.do("PUT", p+"/tags/"+billing.ID, "admin-a", `{"kind":"tag","description":""}`); w.Code != 200 || decode[tagJSON](t, w).Description != "" {
		t.Fatalf("same kind is accepted = %d %s", w.Code, w.Body)
	}
	h.tag("admin-a", `{"name":"Other"}`)
	if w := h.do("PUT", p+"/tags/"+billing.ID, "admin-a", `{"name":"other"}`); w.Code != 409 {
		t.Fatalf("rename onto an existing name = %d", w.Code)
	}
	if w := h.do("PUT", p+"/tags/"+billing.ID, "agent-b", `{"name":"x"}`); w.Code != 404 {
		t.Fatalf("cross-tenant update = %d", w.Code)
	}
	if w := h.do("DELETE", p+"/tags/"+billing.ID, "agent-b", ""); w.Code != 404 {
		t.Fatalf("cross-tenant delete = %d", w.Code)
	}
	if w := h.do("DELETE", p+"/tags/"+billing.ID, "tagger-a", ""); w.Code != 204 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if w := h.do("DELETE", p+"/tags/"+billing.ID, "tagger-a", ""); w.Code != 404 {
		t.Fatalf("delete again = %d", w.Code)
	}
}

func TestTicketTagsAndFilter(t *testing.T) {
	h := newTriageHarness(t)
	a := h.tag("admin-a", `{"name":"Alpha"}`)
	b := h.tag("admin-a", `{"name":"Beta","kind":"category"}`)
	foreign := h.tag("agent-b", `{"name":"Foreign"}`)
	t1 := h.create("agent-a", `{"subject":"one"}`)
	t2 := h.create("agent-a", `{"subject":"two"}`)

	w := h.do("POST", p+"/tickets/"+t1.ID+"/tags", "agent-a", `{"tag_ids":["`+a.ID+`","`+b.ID+`","`+a.ID+`"]}`)
	if w.Code != 200 {
		t.Fatalf("set tags = %d %s", w.Code, w.Body)
	}
	if got := decode[ticketJSON](t, w); len(got.Tags) != 2 {
		t.Fatalf("ticket tags = %+v", got.Tags)
	}
	// Replacing the set.
	w = h.do("POST", p+"/tickets/"+t1.ID+"/tags", "agent-a", `{"tag_ids":["`+b.ID+`"]}`)
	if got := decode[ticketJSON](t, w); w.Code != 200 || len(got.Tags) != 1 || got.Tags[0]["id"] != b.ID {
		t.Fatalf("replace = %d %+v", w.Code, got.Tags)
	}
	h.do("POST", p+"/tickets/"+t2.ID+"/tags", "agent-a", `{"tag_ids":["`+a.ID+`"]}`)

	// Unknown and foreign tag ids are refused and change nothing.
	for _, id := range []string{"no-such-tag", foreign.ID} {
		w := h.do("POST", p+"/tickets/"+t1.ID+"/tags", "agent-a", `{"tag_ids":["`+a.ID+`","`+id+`"]}`)
		if w.Code != 422 {
			t.Fatalf("unknown tag %s = %d %s", id, w.Code, w.Body)
		}
	}
	if got := decode[ticketJSON](t, h.do("GET", p+"/tickets/"+t1.ID, "agent-a", "")); len(got.Tags) != 1 || got.Tags[0]["id"] != b.ID {
		t.Fatalf("refused set changed the tags: %+v", got.Tags)
	}
	if w := h.do("POST", p+"/tickets/no-such-ticket/tags", "agent-a", `{"tag_ids":[]}`); w.Code != 404 || reasonOf(t, w) != "ticket_not_found" {
		t.Fatalf("unknown ticket = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/tickets/"+t1.ID+"/tags", "agent-b", `{"tag_ids":[]}`); w.Code != 404 {
		t.Fatalf("cross-tenant set = %d", w.Code)
	}

	// The list filter.
	page := decode[pageJSON](t, h.do("GET", p+"/tickets?tag_id="+a.ID, "viewer-a", ""))
	if *page.Total != 1 || page.Items[0].ID != t2.ID {
		t.Fatalf("tag filter = %+v", page)
	}

	// Deleting a tag removes it from every ticket.
	if w := h.do("DELETE", p+"/tags/"+b.ID, "admin-a", ""); w.Code != 204 {
		t.Fatalf("delete = %d", w.Code)
	}
	if got := decode[ticketJSON](t, h.do("GET", p+"/tickets/"+t1.ID, "agent-a", "")); len(got.Tags) != 0 {
		t.Fatalf("deleted tag still linked: %+v", got.Tags)
	}
	// Clearing the set.
	if w := h.do("POST", p+"/tickets/"+t2.ID+"/tags", "agent-a", `{"tag_ids":[]}`); w.Code != 200 || len(decode[ticketJSON](t, w).Tags) != 0 {
		t.Fatalf("clear = %d %s", w.Code, w.Body)
	}
}

func TestTagPermissionSplit(t *testing.T) {
	h := newTriageHarness(t)
	tg := h.tag("admin-a", `{"name":"Alpha"}`)
	tk := h.create("agent-a", `{"subject":"one"}`)
	// tickets:manage without tags:manage: may set a ticket's tags, not edit the vocabulary.
	if w := h.do("POST", p+"/tickets/"+tk.ID+"/tags", "agent-a", `{"tag_ids":["`+tg.ID+`"]}`); w.Code != 200 {
		t.Fatalf("agent set tags = %d", w.Code)
	}
	for _, c := range []struct{ m, path, body string }{
		{"POST", p + "/tags", `{"name":"New"}`},
		{"PUT", p + "/tags/" + tg.ID, `{"name":"Renamed"}`},
		{"DELETE", p + "/tags/" + tg.ID, ""},
	} {
		for _, tok := range []string{"agent-a", "viewer-a"} {
			if w := h.do(c.m, c.path, tok, c.body); w.Code != 403 {
				t.Fatalf("%s %s %s = %d", tok, c.m, c.path, w.Code)
			}
		}
	}
	// tags:manage without tickets:manage: edits the vocabulary, not a ticket's tags.
	if w := h.do("POST", p+"/tickets/"+tk.ID+"/tags", "tagger-a", `{"tag_ids":[]}`); w.Code != 403 {
		t.Fatalf("tagger set tags = %d", w.Code)
	}
	if w := h.do("POST", p+"/tickets/"+tk.ID+"/tags", "viewer-a", `{"tag_ids":[]}`); w.Code != 403 {
		t.Fatalf("viewer set tags = %d", w.Code)
	}
	// tickets:read lists the vocabulary.
	if w := h.do("GET", p+"/tags", "viewer-a", ""); w.Code != 200 {
		t.Fatalf("viewer list = %d", w.Code)
	}
}
