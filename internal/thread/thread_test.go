package thread

// T034: reference token, reply subject, reference line and the tenant-confined
// resolution order of research D4.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

const id = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func TestToken(t *testing.T) {
	if Token(id) != "[#"+id+"]" {
		t.Fatal(Token(id))
	}
	cases := map[string]string{
		"Re: Help [#" + id + "]":                    id,
		"Fwd: thing [#" + id + "] trailing":         id,
		"body\n\nTicket reference: [#" + id + "]\n": id,
		"no token here":                             "",
		"[#short]":                                  "",
		"[#zzzzzzzzzz]":                             "",
		"[#" + strings.Repeat("a", 100) + "]":       "",
		"[#" + id + "":                              "",
	}
	for in, want := range cases {
		if got := ParseToken(in); got != want {
			t.Errorf("ParseToken(%q) = %q want %q", in, got, want)
		}
	}
}

func TestReplySubject(t *testing.T) {
	if got := ReplySubject("Printer broken"); got != "Re: Printer broken" {
		t.Fatal(got)
	}
	for _, in := range []string{"Re: Printer broken", "RE: Printer broken", "re: Printer broken", "Re Printer broken"} {
		if got := ReplySubject(in); strings.Count(strings.ToLower(got), "re") != 1 || !strings.HasSuffix(got, "Printer broken") {
			t.Errorf("ReplySubject(%q) = %q", in, got)
		}
	}
	if ReplySubject("") != "Re: (no subject)" || ReplySubject("  \t") != "Re: (no subject)" {
		t.Fatal("empty subject")
	}
	if ParseToken(ReplySubject("x")) != "" {
		t.Fatal("subject must not carry a token")
	}
	if got := ReplySubject("a\r\nBcc: x@y"); strings.ContainsAny(got, "\r\n") {
		t.Fatalf("line break kept: %q", got)
	}
}

func TestAppendReference(t *testing.T) {
	out := AppendReference("We are on it.\n\n", id)
	if ParseToken(out) != id || !strings.Contains(out, ReferenceLine(id)) || strings.Count(out, id) != 1 {
		t.Fatalf("reference = %q", out)
	}
	if again := AppendReference(out, id); again != out {
		t.Fatalf("appended twice: %q", again)
	}
	if ReferenceLine(id) != "Ticket reference: [#"+id+"]" {
		t.Fatal(ReferenceLine(id))
	}
}

func TestMessageID(t *testing.T) {
	a, b := MessageID(id, "acme.example"), MessageID(id, "acme.example")
	if a == b || !strings.HasPrefix(a, "ticket."+id+".") || !strings.HasSuffix(a, "@acme.example") || strings.ContainsAny(a, "<> ") {
		t.Fatalf("message ids %q %q", a, b)
	}
	if !strings.HasSuffix(MessageID(id, ""), "@"+DefaultDomain) || !strings.HasSuffix(MessageID(id, "bad domain\r\n"), "@"+DefaultDomain) {
		t.Fatal("fallback domain")
	}
	if DomainOf("Support@Acme.Example") != "acme.example" || DomainOf("nope") != "" || DomainOf("a@") != "" {
		t.Fatal("DomainOf")
	}
}

const (
	tenantA = "11111111-1111-7111-8111-111111111111"
	tenantB = "22222222-2222-7222-8222-222222222222"
)

type fixtureStore struct {
	*memstore.Mem
	ticketA, ticketB store.Ticket
	commentMsg       string
}

func seed(t *testing.T) *fixtureStore {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()
	mk := func(tenant, ext string) store.Ticket {
		tk := store.Ticket{ID: store.NewID(), TenantID: tenant, Subject: "s", Status: store.StatusOpen, Priority: store.PriorityNormal,
			Source: store.SourceEmail, ExternalID: ext}
		if err := st.CreateTicket(ctx, tk); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	fs := &fixtureStore{Mem: st}
	fs.ticketA = mk(tenantA, "root-a@customer.example")
	fs.ticketB = mk(tenantB, "root-b@customer.example")
	fs.commentMsg = "ticket." + fs.ticketA.ID + ".x@acme.example"
	if err := st.CreateComment(ctx, store.Comment{ID: store.NewID(), TenantID: tenantA, TicketID: fs.ticketA.ID, Body: "b",
		AuthorKind: store.AuthorAgent, MessageID: fs.commentMsg}); err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestResolveOrder(t *testing.T) {
	ctx := context.Background()
	fs := seed(t)
	other := fs.ticketB.ID
	cases := []struct {
		name string
		in   Input
		want string
		by   string
	}{
		{"comment message id", Input{MessageIDs: []string{"unknown@x", fs.commentMsg}}, fs.ticketA.ID, ByComment},
		{"ticket root", Input{MessageIDs: []string{"root-a@customer.example"}}, fs.ticketA.ID, ByExternalID},
		{"comment beats root", Input{MessageIDs: []string{"root-a@customer.example", fs.commentMsg}}, fs.ticketA.ID, ByComment},
		{"body token", Input{Text: "see " + Token(fs.ticketA.ID)}, fs.ticketA.ID, ByBodyToken},
		{"subject token", Input{Subject: "Re: x " + Token(fs.ticketA.ID)}, fs.ticketA.ID, BySubjectToken},
		{"body token beats subject", Input{Text: Token(fs.ticketA.ID), Subject: Token(other)}, fs.ticketA.ID, ByBodyToken},
		{"headers beat tokens", Input{MessageIDs: []string{"root-a@customer.example"}, Text: Token(other)}, fs.ticketA.ID, ByExternalID},
		{"nothing", Input{Subject: "new request", Text: "hello"}, "", ""},
	}
	for _, c := range cases {
		m, err := Resolve(ctx, fs, tenantA, c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if m.TicketID != c.want || m.By != c.by {
			t.Errorf("%s: got %+v want %s by %s", c.name, m, c.want, c.by)
		}
	}
}

func TestResolveTenantConfined(t *testing.T) {
	ctx := context.Background()
	fs := seed(t)
	// Tenant B's ticket root, comment id and token never resolve for tenant A.
	for _, in := range []Input{
		{MessageIDs: []string{"root-b@customer.example"}},
		{Text: Token(fs.ticketB.ID)},
		{Subject: Token(fs.ticketB.ID)},
	} {
		if m, err := Resolve(ctx, fs, tenantA, in); err != nil || m.TicketID != "" {
			t.Errorf("cross-tenant %+v resolved to %+v (%v)", in, m, err)
		}
	}
	if m, _ := Resolve(ctx, fs, tenantB, Input{MessageIDs: []string{fs.commentMsg}}); m.TicketID != "" {
		t.Error("tenant A's comment resolved for tenant B")
	}
	// A reply to a deleted ticket resolves to nothing (a new ticket is opened).
	if _, err := fs.DeleteTicket(ctx, tenantA, fs.ticketA.ID); err != nil {
		t.Fatal(err)
	}
	if m, err := Resolve(ctx, fs, tenantA, Input{MessageIDs: []string{fs.commentMsg, "root-a@customer.example"}, Text: Token(fs.ticketA.ID)}); err != nil || m.TicketID != "" {
		t.Errorf("deleted ticket resolved: %+v %v", m, err)
	}
	if _, err := Resolve(ctx, fs, "", Input{}); !errors.Is(err, ErrTenant) {
		t.Errorf("empty tenant: %v", err)
	}
}

func TestResolveStoreErrors(t *testing.T) {
	ctx := context.Background()
	for _, method := range []string{"FindCommentByMessageID", "FindTicketByExternalID", "GetTicket"} {
		fs := seed(t)
		fs.FailNext(method)
		in := Input{MessageIDs: []string{"x@y"}, Text: Token(fs.ticketA.ID)}
		if _, err := Resolve(ctx, fs, tenantA, in); err == nil {
			t.Errorf("%s failure swallowed", method)
		}
	}
}

func FuzzThread(f *testing.F) {
	for _, s := range []string{"Printer broken", "Re: Re: x", "[#" + id + "]", "", "re", "RE:", "Ticket reference: [#" + id + "]\n", "\r\n[#", "[#-------]"} {
		f.Add(s, id)
	}
	f.Fuzz(func(t *testing.T, s, ticketID string) {
		tok := ParseToken(s)
		if tok != "" && !strings.Contains(s, Token(tok)) {
			t.Fatalf("token %q not in input", tok)
		}
		subj := ReplySubject(s)
		if ReplySubject(subj) != subj {
			t.Fatalf("ReplySubject not idempotent: %q -> %q", subj, ReplySubject(subj))
		}
		if strings.ContainsAny(subj, "\r\n") {
			t.Fatalf("subject line break: %q", subj)
		}
		body := AppendReference(s, ticketID)
		if AppendReference(body, ticketID) != body {
			t.Fatal("AppendReference not idempotent")
		}
	})
}
