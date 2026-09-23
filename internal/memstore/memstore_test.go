package memstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/repo/repotest"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

func TestConformance(t *testing.T) {
	repotest.Run(t, func(*testing.T) repo.Store { return New() })
}

func TestFailNextEveryMethod(t *testing.T) {
	ctx := context.Background()
	m := New()
	tn := repotest.TenantA
	tk := repotest.NewTicket(tn, "x", time.Now())
	calls := map[string]func() error{
		"CreateTicket":              func() error { return m.CreateTicket(ctx, tk) },
		"GetTicket":                 func() error { _, err := m.GetTicket(ctx, tn, "x"); return err },
		"FindTicketByExternalID":    func() error { _, err := m.FindTicketByExternalID(ctx, tn, "x"); return err },
		"ListTickets":               func() error { _, _, err := m.ListTickets(ctx, tn, store.TicketFilter{}); return err },
		"UpdateTicket":              func() error { _, err := m.UpdateTicket(ctx, tn, "x", store.TicketPatch{}, time.Time{}); return err },
		"SetAssignee":               func() error { _, err := m.SetAssignee(ctx, tn, "x", "", "", time.Time{}); return err },
		"SetStatus":                 func() error { _, err := m.SetStatus(ctx, tn, "x", "open", time.Time{}); return err },
		"DeleteTicket":              func() error { _, err := m.DeleteTicket(ctx, tn, "x"); return err },
		"CreateComment":             func() error { return m.CreateComment(ctx, store.Comment{}) },
		"GetComment":                func() error { _, err := m.GetComment(ctx, tn, "x"); return err },
		"ListComments":              func() error { _, err := m.ListComments(ctx, tn, "x"); return err },
		"SetCommentDelivery":        func() error { return m.SetCommentDelivery(ctx, tn, "x", "sent") },
		"DeleteComment":             func() error { _, err := m.DeleteComment(ctx, tn, "x"); return err },
		"FindCommentByMessageID":    func() error { _, err := m.FindCommentByMessageID(ctx, tn, "x"); return err },
		"LastMessageID":             func() error { _, err := m.LastMessageID(ctx, tn, "x"); return err },
		"CreateAttachment":          func() error { return m.CreateAttachment(ctx, store.Attachment{}) },
		"ListAttachments":           func() error { _, err := m.ListAttachments(ctx, tn, "x"); return err },
		"GetAttachment":             func() error { _, err := m.GetAttachment(ctx, tn, "x", "y"); return err },
		"DeleteAttachmentsByTicket": func() error { _, err := m.DeleteAttachmentsByTicket(ctx, tn, "x"); return err },
		"CreateTag":                 func() error { return m.CreateTag(ctx, store.Tag{}) },
		"GetTag":                    func() error { _, err := m.GetTag(ctx, tn, "x"); return err },
		"ListTags":                  func() error { _, err := m.ListTags(ctx, tn, ""); return err },
		"UpdateTag":                 func() error { return m.UpdateTag(ctx, store.Tag{}) },
		"DeleteTag":                 func() error { return m.DeleteTag(ctx, tn, "x") },
		"EnsureTagByName":           func() error { _, err := m.EnsureTagByName(ctx, tn, "", "x"); return err },
		"SetTicketTags":             func() error { return m.SetTicketTags(ctx, tn, "x", nil) },
		"AddTicketTags":             func() error { return m.AddTicketTags(ctx, tn, "x", nil) },
		"TagsForTickets":            func() error { _, err := m.TagsForTickets(ctx, tn, nil); return err },
		"CreateRule":                func() error { return m.CreateRule(ctx, store.Rule{}) },
		"GetRule":                   func() error { _, err := m.GetRule(ctx, tn, "x"); return err },
		"ListRules":                 func() error { _, err := m.ListRules(ctx, tn); return err },
		"ListEnabledRules":          func() error { _, err := m.ListEnabledRules(ctx, tn); return err },
		"UpdateRule":                func() error { _, err := m.UpdateRule(ctx, store.Rule{}); return err },
		"DeleteRule":                func() error { return m.DeleteRule(ctx, tn, "x") },
		"CreateMailbox":             func() error { return m.CreateMailbox(ctx, store.Mailbox{}) },
		"GetMailbox":                func() error { _, err := m.GetMailbox(ctx, tn, "x"); return err },
		"ListMailboxes":             func() error { _, err := m.ListMailboxes(ctx, tn); return err },
		"UpdateMailbox":             func() error { return m.UpdateMailbox(ctx, store.Mailbox{}) },
		"DeleteMailbox":             func() error { return m.DeleteMailbox(ctx, tn, "x", false) },
		"RouteMailbox":              func() error { _, err := m.RouteMailbox(ctx, "x"); return err },
		"AppendHistory":             func() error { return m.AppendHistory(ctx, store.History{}) },
		"ListHistory":               func() error { _, err := m.ListHistory(ctx, tn, "x"); return err },
		"TicketStats":               func() error { _, err := m.TicketStats(ctx, tn, time.Now()); return err },
		"AllTickets":                func() error { _, err := m.AllTickets(ctx, tn); return err },
		"AllComments":               func() error { _, err := m.AllComments(ctx, tn); return err },
		"AllAttachments":            func() error { _, err := m.AllAttachments(ctx, tn); return err },
		"AllTagLinks":               func() error { _, err := m.AllTagLinks(ctx, tn); return err },
		"AllHistory":                func() error { _, err := m.AllHistory(ctx, tn); return err },
		"TenantIDs":                 func() error { _, err := m.TenantIDs(ctx); return err },
		"AppendAudit":               func() error { return m.AppendAudit(ctx, store.AuditRow{}) },
	}
	for name, call := range calls {
		m.FailNext(name)
		err := call()
		var ie injectedErr
		if !errors.As(err, &ie) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want injected", name, err)
		}
	}
	m.Close()
}

func TestDefaultsAndAudit(t *testing.T) {
	ctx := context.Background()
	m := New()
	tk := store.Ticket{ID: store.NewID(), TenantID: repotest.TenantA, Subject: "s"}
	if err := m.CreateTicket(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateTicket(ctx, tk); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("duplicate id: %v", err)
	}
	got, _ := m.GetTicket(ctx, repotest.TenantA, tk.ID)
	if got.Status != store.StatusOpen || got.Priority != store.PriorityNormal || got.Source != store.SourceManual || got.CreatedAt.IsZero() {
		t.Fatalf("defaults = %+v", got)
	}
	c := store.Comment{ID: store.NewID(), TenantID: repotest.TenantA, TicketID: tk.ID, Body: "b", AuthorKind: store.AuthorAgent}
	if err := m.CreateComment(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateComment(ctx, c); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("duplicate comment id: %v", err)
	}
	a := store.Attachment{ID: store.NewID(), TenantID: repotest.TenantA, TicketID: tk.ID, StorageKey: "k"}
	if err := m.CreateAttachment(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.StorageKey = "k2"
	if err := m.CreateAttachment(ctx, a); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("duplicate attachment id: %v", err)
	}
	mb := store.Mailbox{ID: store.NewID(), TenantID: repotest.TenantA, Address: "a@b"}
	_ = m.CreateMailbox(ctx, mb)
	mb.Address = "c@d"
	if err := m.CreateMailbox(ctx, mb); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("duplicate mailbox id: %v", err)
	}
	tag := store.Tag{ID: store.NewID(), TenantID: repotest.TenantA, Name: "n"}
	if err := m.CreateTag(ctx, tag); err != nil {
		t.Fatal(err)
	}
	if g, _ := m.GetTag(ctx, repotest.TenantA, tag.ID); g.Kind != store.KindTag {
		t.Fatal("kind default")
	}
	if _, err := m.EnsureTagByName(ctx, repotest.TenantA, store.KindTag, "n"); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendAudit(ctx, store.AuditRow{Action: "x"}); err != nil || len(m.Audit()) != 1 {
		t.Fatal("audit")
	}
	if _, err := m.UpdateRule(ctx, store.Rule{ID: "missing"}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatal("update missing rule")
	}
	if err := m.DeleteMailbox(ctx, repotest.TenantB, mb.ID, false); !errors.Is(err, repo.ErrNotFound) {
		t.Fatal("cross-tenant mailbox delete")
	}
}
