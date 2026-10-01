package contract

// Feature 032: the list contract of the ticket tables — page/page_size/sort/
// order answer {items,total,page,page_size,sort,order}; invalid values are 422
// validation_failed naming only the parameter; mailboxes, rules and tags page
// like tickets.

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

type listPage struct {
	Items []struct {
		ID           string `json:"id"`
		Subject      string `json:"subject"`
		Priority     string `json:"priority"`
		AssigneeName string `json:"assignee_name"`
		Name         string `json:"name"`
		Address      string `json:"address"`
		SortOrder    int    `json:"sort_order"`
	} `json:"items"`
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Sort     string `json:"sort"`
	Order    string `json:"order"`
}

func (h *harness) listPage(path, tok string, q url.Values) listPage {
	h.t.Helper()
	w := h.do("GET", path+"?"+q.Encode(), tok, "")
	if w.Code != 200 {
		h.t.Fatalf("GET %s?%s = %d %s", path, q.Encode(), w.Code, w.Body)
	}
	return decode[listPage](h.t, w)
}

func TestTicketListSortAndPageShape(t *testing.T) {
	h := newHarness(t)
	h.create("agent-a", `{"subject":"beta","priority":"low"}`)
	h.create("agent-a", `{"subject":"Alpha","priority":"urgent","assignee_id":"`+agentA+`"}`)
	h.create("agent-a", `{"subject":"gamma","priority":"high"}`)
	h.create("agent-b", `{"subject":"other tenant"}`)

	def := h.listPage(p+"/tickets", "viewer-a", url.Values{})
	if def.Total != 3 || def.Page != 1 || def.PageSize != 25 || def.Sort != "created_at" || def.Order != "desc" || def.Items[0].Subject != "gamma" {
		t.Fatalf("default page = %+v", def)
	}
	cases := []struct {
		q    url.Values
		want string
	}{
		{url.Values{"sort": {"subject"}}, "Alpha,beta,gamma"},
		{url.Values{"sort": {"subject"}, "order": {"desc"}}, "gamma,beta,Alpha"},
		{url.Values{"sort": {"priority"}}, "Alpha,gamma,beta"}, // urgent first by default
		{url.Values{"sort": {"priority"}, "order": {"asc"}}, "beta,gamma,Alpha"},
		{url.Values{"sort": {"assignee"}}, "Alpha"}, // unassigned last (ties by id)
		{url.Values{"sort": {"created_at"}, "order": {"asc"}}, "beta,Alpha,gamma"},
	}
	for _, c := range cases {
		pg := h.listPage(p+"/tickets", "viewer-a", c.q)
		got := []string{}
		for _, it := range pg.Items {
			got = append(got, it.Subject)
		}
		if !strings.HasPrefix(strings.Join(got, ","), c.want) || pg.Sort != c.q.Get("sort") {
			t.Errorf("%s = %v (%s %s)", c.q.Encode(), got, pg.Sort, pg.Order)
		}
	}
	// Exactly once over pages of 2 with a filter applied.
	seen := map[string]bool{}
	for page := 1; page <= 2; page++ {
		pg := h.listPage(p+"/tickets", "viewer-a", url.Values{"sort": {"subject"}, "page": {fmt.Sprint(page)}, "page_size": {"2"}})
		if pg.Total != 3 || pg.Page != page {
			t.Fatalf("page %d = %+v", page, pg)
		}
		for _, it := range pg.Items {
			if seen[it.ID] {
				t.Fatalf("duplicate %s", it.ID)
			}
			seen[it.ID] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("paged %d of 3", len(seen))
	}
}

func TestListParamRefusals(t *testing.T) {
	h := newTriageHarness(t)
	for _, path := range []string{"/tickets", "/tags", "/rules"} {
		for _, c := range []struct{ q, param string }{
			{"page=0", "page"}, {"page=-1", "page"}, {"page=abc", "page"},
			{"page_size=0", "page_size"}, {"page_size=201", "page_size"}, {"page_size=x", "page_size"},
			{"sort=password", "sort"}, {"sort=id", "sort"}, {"order=up", "order"},
		} {
			w := h.do("GET", p+path+"?"+c.q, "admin-a", "")
			if w.Code != 422 {
				t.Errorf("%s?%s = %d", path, c.q, w.Code)
				continue
			}
			r := decode[refusal](t, w)
			if r.Reason != "validation_failed" || r.Detail["param"] != c.param || len(r.Detail) != 1 {
				t.Errorf("%s?%s = %+v", path, c.q, r)
			}
			if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "abc") {
				t.Errorf("%s?%s echoes the value: %s", path, c.q, w.Body)
			}
		}
	}
	// The ticket list keeps its own sort enum: rule fields are not accepted.
	if w := h.do("GET", p+"/tickets?sort=sort_order", "admin-a", ""); w.Code != 422 {
		t.Fatalf("foreign sort field = %d", w.Code)
	}
}

func TestVocabularyListsPage(t *testing.T) {
	h := newTriageHarness(t)
	for _, n := range []string{"network", "Access", "billing"} {
		h.tag("admin-a", `{"name":"`+n+`"}`)
	}
	h.tag("agent-b", `{"name":"elsewhere"}`)
	pg := h.listPage(p+"/tags", "viewer-a", url.Values{"page_size": {"2"}})
	if pg.Total != 3 || len(pg.Items) != 2 || pg.Items[0].Name != "Access" || pg.Sort != "name" || pg.Order != "asc" {
		t.Fatalf("tags page 1 = %+v", pg)
	}
	pg = h.listPage(p+"/tags", "viewer-a", url.Values{"page": {"5"}, "page_size": {"2"}, "order": {"desc"}})
	if pg.Total != 3 || pg.Page != 2 || len(pg.Items) != 1 || pg.Items[0].Name != "Access" {
		t.Fatalf("tags clamped desc = %+v", pg)
	}

	for i, n := range []string{"zeta", "alpha", "mid"} {
		w := h.do("POST", p+"/rules", "admin-a", fmt.Sprintf(`{"name":%q,"sort_order":%d,"expression":"spamScore > 5.0","actions":[{"type":"drop"}]}`, n, 30-10*i))
		if w.Code != 201 {
			t.Fatalf("rule %s = %d %s", n, w.Code, w.Body)
		}
	}
	pg = h.listPage(p+"/rules", "admin-a", url.Values{})
	if pg.Total != 3 || pg.Sort != "sort_order" || pg.Items[0].Name != "mid" || pg.Items[2].Name != "zeta" {
		t.Fatalf("rules default = %+v", pg)
	}
	pg = h.listPage(p+"/rules", "admin-a", url.Values{"sort": {"name"}, "page_size": {"1"}, "page": {"2"}})
	if pg.Total != 3 || len(pg.Items) != 1 || pg.Items[0].Name != "mid" {
		t.Fatalf("rules by name page 2 = %+v", pg)
	}
}

func TestMailboxListPages(t *testing.T) {
	h := newConvHarness(t)
	for _, b := range []string{`{"address":"c@acme.example","display_name":"Alpha"}`, `{"address":"a@acme.example","display_name":"Zulu"}`, `{"address":"b@acme.example","display_name":"Mike"}`} {
		if w := h.do("POST", p+"/mailboxes", "admin-a", b); w.Code != 201 {
			t.Fatalf("create = %d %s", w.Code, w.Body)
		}
	}
	pg := h.listPage(p+"/mailboxes", "admin-a", url.Values{"page_size": {"2"}})
	if pg.Total != 3 || len(pg.Items) != 2 || pg.Items[0].Address != "a@acme.example" || pg.Sort != "address" {
		t.Fatalf("mailboxes default = %+v", pg)
	}
	pg = h.listPage(p+"/mailboxes", "admin-a", url.Values{"sort": {"name"}, "order": {"desc"}})
	if pg.Items[0].Address != "a@acme.example" || pg.Items[2].Address != "c@acme.example" {
		t.Fatalf("mailboxes by name desc = %+v", pg)
	}
	if pg := h.listPage(p+"/mailboxes", "agent-b", url.Values{}); pg.Total != 0 || pg.Page != 1 || pg.Items == nil {
		t.Fatalf("other tenant = %+v", pg)
	}
	if w := h.do("GET", p+"/mailboxes?sort=display_name", "admin-a", ""); w.Code != 422 {
		t.Fatalf("column name as sort = %d", w.Code)
	}
}
