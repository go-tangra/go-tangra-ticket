package app

// US2+US3 wiring: the built app's inbound edge ingests a fixture into the
// routed tenant, the browser API serves the sanitised body and the attachment,
// and a public reply goes out through the (fake) mailer threaded to the
// original message.

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

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/inbound"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailer"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

func TestInboundToReplyWiring(t *testing.T) {
	o := options()
	o.Checker = authz.Static{"u1": {authz.TicketsRead, authz.TicketsManage, authz.MailboxesManage}}
	fake := &mailer.Fake{}
	o.Mailer = fake
	a, err := Build(context.Background(), testConfig(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Comments == nil || a.Mailbox == nil || a.Mail == nil || a.Mailer != fake {
		t.Fatal("US2/US3 services not wired")
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
	if w := api("POST", "/mailboxes", `{"address":"support@acme.example","display_name":"Acme Support"}`); w.Code != 201 {
		t.Fatalf("mailbox = %d %s", w.Code, w.Body)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mail", "html-inline-image.eml"))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", inbound.PathMail, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer relay-token")
	r.Header.Set(inbound.HeaderIrisRecipient, "support@acme.example")
	w := httptest.NewRecorder()
	a.Inbound.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("inbound = %d %s", w.Code, w.Body)
	}
	var out struct {
		Outcome  string `json:"outcome"`
		TicketID string `json:"ticket_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Outcome != "created" || out.TicketID == "" {
		t.Fatalf("outcome = %s", w.Body)
	}
	if w := api("GET", "/tickets/"+out.TicketID+"/body", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "/attachments/") {
		t.Fatalf("body = %d %s", w.Code, w.Body)
	}
	tk, _ := a.Repo.GetTicket(context.Background(), appTenant, out.TicketID)
	atts, _ := a.Repo.ListAttachments(context.Background(), appTenant, tk.ID)
	if len(atts) != 1 {
		t.Fatalf("attachments = %+v", atts)
	}
	if w := api("GET", "/tickets/"+tk.ID+"/attachments/"+atts[0].ID, ""); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("attachment = %d %v", w.Code, w.Header())
	}
	if w := api("POST", "/tickets/"+tk.ID+"/reply", `{"body":"Thanks, looking into it."}`); w.Code != 201 {
		t.Fatalf("reply = %d %s", w.Code, w.Body)
	}
	sent := fake.Messages()
	if len(sent) != 1 || sent[0].From != "support@acme.example" || sent[0].InReplyTo != "inline-0001@mail.customer.example" {
		t.Fatalf("sent = %+v", sent)
	}
	if tk.Source != store.SourceEmail || tk.RequesterEmail != "jane@customer.example" {
		t.Fatalf("ticket = %+v", tk)
	}
}

func TestDefaultMailerFromConfig(t *testing.T) {
	c := testConfig()
	c.SMTP.Host, c.SMTP.Port, c.SMTP.TLS = "smtp.example.org", 587, "starttls"
	a, err := Build(context.Background(), c, options())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.Mailer.Enabled() {
		t.Fatal("configured relay not enabled")
	}
	b, err := Build(context.Background(), testConfig(), options())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Mailer.Enabled() {
		t.Fatal("empty smtp.host must disable outbound mail")
	}
}

// US6 wiring: a mailbox with auto-acknowledgement answers a new email ticket
// through the wired mailer (auto-reply headers), and /stats and /backup are
// served (no longer 501).
func TestAckStatsBackupWiring(t *testing.T) {
	o := options()
	o.Checker = authz.Static{"u1": {authz.TicketsRead, authz.MailboxesManage, authz.StatsRead, authz.BackupManage}}
	fake := &mailer.Fake{}
	o.Mailer = fake
	a, err := Build(context.Background(), testConfig(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
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
	if w := api("POST", "/mailboxes", `{"address":"support@acme.example","auto_ack":true,"auto_ack_template":"Hi {{name}}"}`); w.Code != 201 {
		t.Fatalf("mailbox = %d %s", w.Code, w.Body)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mail", "plain.eml"))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", inbound.PathMail, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer relay-token")
	r.Header.Set(inbound.HeaderIrisRecipient, "support@acme.example")
	w := httptest.NewRecorder()
	a.Inbound.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("inbound = %d %s", w.Code, w.Body)
	}
	sent := fake.Messages()
	if len(sent) != 1 || !sent[0].AutoReply || !strings.HasPrefix(sent[0].Text, "Hi Jane Customer") {
		t.Fatalf("ack = %+v", sent)
	}
	if w := api("GET", "/stats?days=7", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatalf("stats = %d %s", w.Code, w.Body)
	}
	if w := api("POST", "/backup/export", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"schema_version":1`) {
		t.Fatalf("export = %d", w.Code)
	}
}
