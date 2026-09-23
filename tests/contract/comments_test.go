package contract

// T045: the conversation, message-body and attachment surface of contracts §A
// (plus mailboxes) over the full HTTP chain: shapes validated against the
// OpenAPI document, oldest-first order, note vs reply, reply_unavailable and
// delivery_failed, the body route never returning raw HTML, and attachment
// downloads scoped to the caller's tenant with safe headers.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/go-freya/freya/internal/testrt"
	"github.com/go-freya/freya/internal/testutil"

	"github.com/go-freya/freya/services/ticket/internal/agents"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/blob"
	"github.com/go-freya/freya/services/ticket/internal/comments"
	"github.com/go-freya/freya/services/ticket/internal/events"
	"github.com/go-freya/freya/services/ticket/internal/httpapi"
	"github.com/go-freya/freya/services/ticket/internal/mailboxes"
	"github.com/go-freya/freya/services/ticket/internal/mailer"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

type convHarness struct {
	*harness
	mail  *mailer.Fake
	blobs *blob.Fake
}

func newConvHarness(t *testing.T) *convHarness {
	t.Helper()
	rt := testrt.New(t, testutil.MustCA("example.org"), "ticket")
	v := verifier{
		"admin-a":  {UserID: adminA, TenantID: tenantA},
		"viewer-a": {UserID: viewerA, TenantID: tenantA},
		"agent-a":  {UserID: agentA, TenantID: tenantA},
		"agent-b":  {UserID: agentB, TenantID: tenantB},
	}
	all := append([]string{}, authz.Permissions...)
	checker := authz.Static{adminA: all, agentA: {authz.TicketsRead, authz.TicketsManage}, agentB: all, viewerA: {authz.TicketsRead}}
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
	dir.Add(tenantA, agents.User{ID: adminA, Name: "Alan Admin"}, true)
	dir.Add(tenantB, agents.User{ID: agentB, Name: "Bea"}, true)
	h := &convHarness{harness: &harness{t: t, s: s, rtr: rtr, st: memstore.New(), pub: &events.Recorder{}}, mail: &mailer.Fake{}, blobs: blob.NewFake()}
	tk := tickets.New(tickets.Deps{Store: h.st, Agents: dir, Events: h.pub, Blobs: h.blobs})
	s.Register(httpapi.Deps{Tickets: tk,
		Comments:  comments.New(comments.Deps{Store: h.st, Mailer: h.mail, Tickets: tk, Agents: dir, Events: h.pub, Blobs: h.blobs}),
		Mailboxes: mailboxes.New(mailboxes.Deps{Store: h.st})})
	return h
}

// seed stores an email ticket of tenant with an HTML body, a PDF attachment and
// an inline image, as the inbound edge would.
func (h *convHarness) seed(tenant string) (store.Ticket, store.Attachment, store.Attachment) {
	h.t.Helper()
	ctx := context.Background()
	mb := store.Mailbox{ID: store.NewID(), TenantID: tenant, Address: "support-" + tenant[:4] + "@acme.example", DisplayName: "Support", Active: true}
	if err := h.st.CreateMailbox(ctx, mb); err != nil {
		h.t.Fatal(err)
	}
	tk := store.Ticket{ID: store.NewID(), TenantID: tenant, Subject: "Screenshot", Description: "See the screenshot below.",
		BodyHTML: `<p>See <img src="cid:shot1@x"></p><script>alert(1)</script><img src="https://tracker.example/p.gif">`,
		Status:   store.StatusOpen, Priority: store.PriorityNormal, Source: store.SourceEmail, RequesterEmail: "jane@customer.example",
		RequesterName: "Jane", Recipient: mb.Address, MailboxID: mb.ID, ExternalID: "root-" + tenant[:4] + "@customer.example"}
	if err := h.st.CreateTicket(ctx, tk); err != nil {
		h.t.Fatal(err)
	}
	mk := func(name, ct, cid, data string, inline bool) store.Attachment {
		a := store.Attachment{ID: store.NewID(), TenantID: tenant, TicketID: tk.ID, Filename: name, ContentType: ct, Size: int64(len(data)),
			ContentID: cid, Inline: inline}
		a.StorageKey = store.AttachmentKey(tenant, tk.ID, a.ID)
		if _, err := h.blobs.Put(ctx, a.StorageKey, strings.NewReader(data), a.Size, ct); err != nil {
			h.t.Fatal(err)
		}
		if err := h.st.CreateAttachment(ctx, a); err != nil {
			h.t.Fatal(err)
		}
		return a
	}
	pdf := mk("Rechnung März.pdf", "application/pdf", "", "%PDF-1.4 x", false)
	png := mk("shot.png", "image/png", "shot1@x", "\x89PNG....", true)
	return tk, pdf, png
}

type commentJSON struct {
	ID         string `json:"id"`
	Body       string `json:"body"`
	Internal   bool   `json:"internal"`
	AuthorKind string `json:"author_kind"`
	AuthorName string `json:"author_name"`
	MessageID  string `json:"message_id"`
	Delivery   string `json:"delivery"`
}

func TestConversationShapesAndOrder(t *testing.T) {
	h := newConvHarness(t)
	tk, _, _ := h.seed(tenantA)
	base := p + "/tickets/" + tk.ID

	if w := h.do("GET", base+"/comments", "viewer-a", ""); w.Code != 200 || len(decode[struct{ Items []commentJSON }](t, w).Items) != 0 {
		t.Fatalf("empty list = %d %s", w.Code, w.Body)
	}
	w := h.do("POST", base+"/comments", "agent-a", `{"body":"Called back.","internal":true}`)
	if w.Code != 201 {
		t.Fatalf("note = %d %s", w.Code, w.Body)
	}
	note := decode[commentJSON](t, w)
	if !note.Internal || note.AuthorKind != "agent" || note.AuthorName != "Ada Agent" || note.Delivery != "none" || note.MessageID != "" {
		t.Fatalf("note shape = %+v", note)
	}
	if len(h.mail.Messages()) != 0 {
		t.Fatal("note emailed")
	}
	// only internal notes go through /comments
	if w := h.do("POST", base+"/comments", "agent-a", `{"body":"x","internal":false}`); w.Code != 422 && w.Code != 400 {
		t.Fatalf("public via /comments = %d", w.Code)
	}
	w = h.do("POST", base+"/reply", "agent-a", `{"body":"We are on it."}`)
	if w.Code != 201 {
		t.Fatalf("reply = %d %s", w.Code, w.Body)
	}
	rep := decode[commentJSON](t, w)
	if rep.Internal || rep.Delivery != "sent" || rep.MessageID == "" || len(h.mail.Messages()) != 1 {
		t.Fatalf("reply shape = %+v", rep)
	}
	list := decode[struct{ Items []commentJSON }](t, h.do("GET", base+"/comments", "viewer-a", "")).Items
	if len(list) != 2 || list[0].ID != note.ID || list[1].ID != rep.ID {
		t.Fatalf("order = %+v", list)
	}
	// permissions
	for _, c := range []struct{ m, path, body string }{
		{"POST", base + "/comments", `{"body":"x","internal":true}`},
		{"POST", base + "/reply", `{"body":"x"}`},
		{"DELETE", p + "/comments/" + note.ID, ""},
	} {
		if w := h.do(c.m, c.path, "viewer-a", c.body); w.Code != 403 {
			t.Errorf("viewer %s %s = %d", c.m, c.path, w.Code)
		}
	}
	// tenant isolation
	if w := h.do("GET", base+"/comments", "agent-b", ""); w.Code != 404 || reasonOf(t, w) != "ticket_not_found" {
		t.Fatalf("cross-tenant list = %d %s", w.Code, w.Body)
	}
	if w := h.do("DELETE", p+"/comments/"+note.ID, "agent-b", ""); w.Code != 404 || reasonOf(t, w) != "comment_not_found" {
		t.Fatalf("cross-tenant delete = %d %s", w.Code, w.Body)
	}
	if w := h.do("DELETE", p+"/comments/"+note.ID, "agent-a", ""); w.Code != 204 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if w := h.do("DELETE", p+"/comments/"+note.ID, "agent-a", ""); w.Code != 404 {
		t.Fatalf("second delete = %d", w.Code)
	}
}

func TestReplyRefusals(t *testing.T) {
	h := newConvHarness(t)
	tk, _, _ := h.seed(tenantA)
	manual := h.create("agent-a", `{"subject":"no requester"}`)
	if w := h.do("POST", p+"/tickets/"+manual.ID+"/reply", "agent-a", `{"body":"x"}`); w.Code != 409 || reasonOf(t, w) != "reply_unavailable" {
		t.Fatalf("no requester = %d %s", w.Code, w.Body)
	}
	h.mail.Err = errors.New("relay down")
	w := h.do("POST", p+"/tickets/"+tk.ID+"/reply", "agent-a", `{"body":"hello?"}`)
	if w.Code != 502 || reasonOf(t, w) != "delivery_failed" {
		t.Fatalf("delivery failure = %d %s", w.Code, w.Body)
	}
	list := decode[struct{ Items []commentJSON }](t, h.do("GET", p+"/tickets/"+tk.ID+"/comments", "agent-a", "")).Items
	if len(list) != 1 || list[0].Delivery != "failed" {
		t.Fatalf("failed reply not recorded: %+v", list)
	}
	h.mail.Err, h.mail.Disabled = nil, true
	if w := h.do("POST", p+"/tickets/"+tk.ID+"/reply", "agent-a", `{"body":"x"}`); w.Code != 409 || reasonOf(t, w) != "reply_unavailable" {
		t.Fatalf("relay disabled = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/tickets/"+store.NewID()+"/reply", "agent-a", `{"body":"x"}`); w.Code != 404 || reasonOf(t, w) != "ticket_not_found" {
		t.Fatalf("unknown ticket = %d", w.Code)
	}
	if w := h.do("POST", p+"/tickets/"+tk.ID+"/reply", "agent-a", `{"body":""}`); w.Code != 422 && w.Code != 400 {
		t.Fatalf("empty body = %d", w.Code)
	}
}

func TestBodyNeverRaw(t *testing.T) {
	h := newConvHarness(t)
	tk, _, png := h.seed(tenantA)
	w := h.do("GET", p+"/tickets/"+tk.ID+"/body", "viewer-a", "")
	if w.Code != 200 {
		t.Fatalf("body = %d %s", w.Code, w.Body)
	}
	b := decode[map[string]string](t, w)
	if strings.Contains(w.Body.String(), "body_html") || strings.Contains(b["html_sanitized"], "<script") ||
		strings.Contains(b["html_sanitized"], "tracker.example") || strings.Contains(b["html_sanitized"], "cid:") {
		t.Fatalf("unsafe body: %s", w.Body)
	}
	if !strings.Contains(b["html_sanitized"], `src="/api/ticket/v1/tickets/`+tk.ID+`/attachments/`+png.ID+`"`) || b["text"] != "See the screenshot below." {
		t.Fatalf("body = %+v", b)
	}
	if w := h.do("GET", p+"/tickets/"+tk.ID+"/body", "agent-b", ""); w.Code != 404 {
		t.Fatalf("cross-tenant body = %d", w.Code)
	}
	// the ticket itself never carries the raw HTML
	if w := h.do("GET", p+"/tickets/"+tk.ID, "viewer-a", ""); strings.Contains(w.Body.String(), "<script") || !strings.Contains(w.Body.String(), `"has_html":true`) {
		t.Fatalf("ticket = %s", w.Body)
	}
}

func TestAttachmentDownload(t *testing.T) {
	h := newConvHarness(t)
	tk, pdf, png := h.seed(tenantA)
	tkB, _, pngB := h.seed(tenantB)

	w := h.do("GET", p+"/tickets/"+tk.ID+"/attachments/"+pdf.ID, "viewer-a", "")
	if w.Code != 200 || w.Body.String() != "%PDF-1.4 x" {
		t.Fatalf("pdf = %d %q", w.Code, w.Body)
	}
	hd := w.Header()
	if hd.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(hd.Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(hd.Get("Content-Disposition"), "filename*=utf-8''Rechnung%20M%C3%A4rz.pdf") || hd.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(hd.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("pdf headers = %v", hd)
	}
	w = h.do("GET", p+"/tickets/"+tk.ID+"/attachments/"+png.ID, "viewer-a", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline;") ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("inline image = %d %v", w.Code, w.Header())
	}
	// another tenant's attachment is not found, however it is addressed
	for _, path := range []string{
		p + "/tickets/" + tkB.ID + "/attachments/" + pngB.ID,
		p + "/tickets/" + tk.ID + "/attachments/" + pngB.ID,
	} {
		if w := h.do("GET", path, "agent-a", ""); w.Code != 404 {
			t.Errorf("cross-tenant %s = %d", path, w.Code)
		}
	}
	if w := h.do("GET", p+"/tickets/"+tk.ID+"/attachments/"+pdf.ID, "", ""); w.Code != 401 {
		t.Fatalf("anonymous = %d", w.Code)
	}
	_ = h.blobs.Delete(context.Background(), pdf.StorageKey)
	if w := h.do("GET", p+"/tickets/"+tk.ID+"/attachments/"+pdf.ID, "agent-a", ""); w.Code != 503 {
		t.Fatalf("object store miss = %d", w.Code)
	}
	// delete ticket removes attachment objects
	if w := h.do("DELETE", p+"/tickets/"+tk.ID, "admin-a", ""); w.Code != 204 {
		t.Fatalf("delete = %d", w.Code)
	}
	if h.blobs.Len() != 2 { // only tenant B's objects remain
		t.Fatalf("objects after delete = %d", h.blobs.Len())
	}
}

type mailboxJSON struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	Active      bool   `json:"active"`
	AutoAck     bool   `json:"auto_ack"`
}

func TestMailboxesCRUD(t *testing.T) {
	h := newConvHarness(t)
	w := h.do("POST", p+"/mailboxes", "admin-a", `{"address":"Help@Acme.Example","display_name":"Acme Help","auto_ack":true,"auto_ack_template":"Hi {{name}}"}`)
	if w.Code != 201 {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	mb := decode[mailboxJSON](t, w)
	if mb.Address != "help@acme.example" || !mb.Active || !mb.AutoAck || mb.DisplayName != "Acme Help" {
		t.Fatalf("mailbox = %+v", mb)
	}
	if w := h.do("POST", p+"/mailboxes", "agent-b", `{"address":"help@acme.example"}`); w.Code != 409 || reasonOf(t, w) != "conflict" {
		t.Fatalf("global uniqueness = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/mailboxes", "admin-a", `{"address":"not an address"}`); w.Code != 422 {
		t.Fatalf("invalid = %d", w.Code)
	}
	if w := h.do("GET", p+"/mailboxes", "agent-a", ""); w.Code != 403 {
		t.Fatalf("agent without mailboxes:manage = %d", w.Code)
	}
	w = h.do("PUT", p+"/mailboxes/"+mb.ID, "admin-a", `{"active":false}`)
	if w.Code != 200 || decode[mailboxJSON](t, w).Active || decode[mailboxJSON](t, w).Address != "help@acme.example" {
		t.Fatalf("update = %d %s", w.Code, w.Body)
	}
	if w := h.do("PUT", p+"/mailboxes/"+mb.ID, "agent-b", `{"active":true}`); w.Code != 404 {
		t.Fatalf("cross-tenant update = %d", w.Code)
	}
	list := decode[struct{ Items []mailboxJSON }](t, h.do("GET", p+"/mailboxes", "admin-a", "")).Items
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	_ = h.st.CreateTicket(context.Background(), store.Ticket{ID: store.NewID(), TenantID: tenantA, Subject: "s", Status: store.StatusOpen,
		Priority: store.PriorityNormal, Source: store.SourceEmail, MailboxID: mb.ID})
	if w := h.do("DELETE", p+"/mailboxes/"+mb.ID, "admin-a", ""); w.Code != 409 {
		t.Fatalf("delete in use = %d", w.Code)
	}
	if w := h.do("DELETE", p+"/mailboxes/"+mb.ID+"?force=true", "admin-a", ""); w.Code != 204 {
		t.Fatalf("force delete = %d %s", w.Code, w.Body)
	}
}
