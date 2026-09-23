package app

// US4+US5 wiring: rules and tags created through the browser API drive the
// running inbound edge — a matching drop rule discards spam (no ticket), a tag
// and assign rule shapes the new ticket (history actor "rule"), and the ticket
// list filters by the rule-created tag.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/inbound"
	"github.com/go-freya/freya/services/ticket/internal/rules"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

func TestRulesDriveInboundWiring(t *testing.T) {
	o := options()
	o.Checker = authz.Static{"u1": append([]string{}, authz.Permissions...)}
	a, err := Build(context.Background(), testConfig(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, ok := a.Triage.(*rules.Triage); !ok || a.Rules == nil || a.Tags == nil || a.Engine == nil {
		t.Fatalf("US4/US5 services not wired: %T", a.Triage)
	}
	api := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://localhost/api/ticket/v1"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer agent")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if method != "GET" {
			r.Header.Set("X-CSRF-Token", "csrf")
		}
		w := httptest.NewRecorder()
		a.HTTP.Handler().ServeHTTP(w, r)
		return w
	}
	deliver := func(fixture string) (string, string) {
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mail", fixture))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", inbound.PathMail, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer relay-token")
		r.Header.Set(inbound.HeaderIrisRecipient, "support@acme.example")
		w := httptest.NewRecorder()
		a.Inbound.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusAccepted {
			t.Fatalf("inbound %s = %d %s", fixture, w.Code, w.Body)
		}
		var out struct {
			Outcome  string `json:"outcome"`
			TicketID string `json:"ticket_id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.Outcome, out.TicketID
	}
	if w := api("POST", "/mailboxes", `{"address":"support@acme.example"}`); w.Code != 201 {
		t.Fatalf("mailbox = %d %s", w.Code, w.Body)
	}
	if w := api("POST", "/rules", `{"name":"Spam","sort_order":1,"conditions":[{"field":"spamScore","operator":"gt","value":"5"}],"actions":[{"type":"drop"}]}`); w.Code != 201 {
		t.Fatalf("drop rule = %d %s", w.Code, w.Body)
	}
	if w := api("POST", "/rules", `{"name":"Printers","sort_order":2,"match":"any","conditions":[{"field":"subject","operator":"contains","value":"PRINTER"},{"field":"body","operator":"contains","value":"toner"}],`+
		`"actions":[{"type":"tag","tag_kind":"category","tag_names":["Hardware"]},{"type":"assign","assignee_id":"u1"},{"type":"priority","priority":"high"}]}`); w.Code != 201 {
		t.Fatalf("tag rule = %d %s", w.Code, w.Body)
	}

	if outcome, id := deliver("spam-high-score.eml"); outcome != inbound.OutcomeDropped || id != "" {
		t.Fatalf("spam = %s %s", outcome, id)
	}
	outcome, id := deliver("plain.eml")
	if outcome != inbound.OutcomeCreated {
		t.Fatalf("plain = %s", outcome)
	}
	tk, err := a.Repo.GetTicket(context.Background(), appTenant, id)
	if err != nil || tk.AssigneeID != "u1" || tk.Priority != store.PriorityHigh {
		t.Fatalf("ticket = %+v %v", tk, err)
	}
	hist, _ := a.Repo.ListHistory(context.Background(), appTenant, id)
	if len(hist) != 2 || hist[0].ActorKind != store.ActorRule || hist[1].ActorKind != store.ActorRule {
		t.Fatalf("history = %+v", hist)
	}
	var tags struct{ Items []store.Tag }
	_ = json.Unmarshal(api("GET", "/tags?kind=category", "").Body.Bytes(), &tags)
	if len(tags.Items) != 1 || tags.Items[0].Name != "Hardware" {
		t.Fatalf("rule-created tag = %+v", tags.Items)
	}
	var page struct {
		Items []struct{ ID string }
		Total int
	}
	_ = json.Unmarshal(api("GET", "/tickets?tag_id="+tags.Items[0].ID, "").Body.Bytes(), &page)
	if page.Total != 1 || page.Items[0].ID != id {
		t.Fatalf("tag filter = %+v", page)
	}
}
