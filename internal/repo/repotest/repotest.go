// Package repotest is the behavioural conformance suite of repo.Store. The
// in-memory store runs it in the unit suite; the TimescaleDB store runs it in
// the tagged integration suite, so both implementations provably agree on
// tenant isolation, the unique/partial-unique guards, cascades, the
// denormalised counters and the routing lookup.
package repotest

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// Fixed tenants of the suite.
const (
	TenantA = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	TenantB = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

// Factory returns a fresh, empty store for one subtest.
type Factory func(t *testing.T) repo.Store

// Run executes every conformance case against stores built by newStore.
func Run(t *testing.T, newStore Factory) {
	cases := map[string]func(*testing.T, repo.Store){
		"tickets":     testTickets,
		"filters":     testFilters,
		"comments":    testComments,
		"attachments": testAttachments,
		"tags":        testTags,
		"rules":       testRules,
		"mailboxes":   testMailboxes,
		"history":     testHistory,
		"stats":       testStats,
		"backup":      testBackup,
		"isolation":   testIsolation,
		"cascade":     testCascade,
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fn := cases[n]
		t.Run(n, func(t *testing.T) { fn(t, newStore(t)) })
	}
}

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// NewTicket builds a valid ticket row.
func NewTicket(tenant, subject string, at time.Time) store.Ticket {
	return store.Ticket{ID: store.NewID(), TenantID: tenant, Subject: subject, Status: store.StatusOpen,
		Priority: store.PriorityNormal, Source: store.SourceManual, CreatedBy: "u1", CreatedAt: at, UpdatedAt: at}
}

func base() time.Time { return time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond) }

func testTickets(t *testing.T, s repo.Store) {
	now := base()
	a := NewTicket(TenantA, "Printer on fire", now)
	a.ExternalID = "<root-1@example.org>"
	a.Source = store.SourceEmail
	a.RequesterEmail = "c@customer.example"
	a.BodyHTML = "<p>hi</p>"
	must(t, s.CreateTicket(ctx, a))
	dup := NewTicket(TenantA, "dup", now)
	dup.ExternalID = a.ExternalID
	wantErr(t, s.CreateTicket(ctx, dup), repo.ErrConflict)
	// the same external id in another tenant is fine; manual tickets have none
	other := NewTicket(TenantB, "other", now)
	other.ExternalID = a.ExternalID
	must(t, s.CreateTicket(ctx, other))
	must(t, s.CreateTicket(ctx, NewTicket(TenantA, "manual 1", now)))
	must(t, s.CreateTicket(ctx, NewTicket(TenantA, "manual 2", now)))

	got, err := s.GetTicket(ctx, TenantA, a.ID)
	must(t, err)
	if got.Subject != a.Subject || got.BodyHTML != a.BodyHTML || got.ExternalID != a.ExternalID || got.Source != store.SourceEmail {
		t.Fatalf("get = %+v", got)
	}
	found, err := s.FindTicketByExternalID(ctx, TenantA, a.ExternalID)
	must(t, err)
	if found.ID != a.ID {
		t.Fatal("find by external id")
	}
	_, err = s.FindTicketByExternalID(ctx, TenantA, "")
	wantErr(t, err, repo.ErrNotFound)
	_, err = s.GetTicket(ctx, TenantA, store.NewID())
	wantErr(t, err, repo.ErrNotFound)

	subj, desc, prio := "Printer on FIRE", "smoke", store.PriorityUrgent
	up, err := s.UpdateTicket(ctx, TenantA, a.ID, store.TicketPatch{Subject: &subj, Description: &desc, Priority: &prio}, now.Add(time.Minute))
	must(t, err)
	if up.Subject != subj || up.Description != desc || up.Priority != prio || up.ExternalID != a.ExternalID {
		t.Fatalf("update = %+v", up)
	}
	up, err = s.UpdateTicket(ctx, TenantA, a.ID, store.TicketPatch{}, now.Add(2*time.Minute))
	must(t, err)
	if up.Subject != subj {
		t.Fatal("empty patch changed fields")
	}
	_, err = s.UpdateTicket(ctx, TenantB, a.ID, store.TicketPatch{Subject: &subj}, now)
	wantErr(t, err, repo.ErrNotFound)

	as, err := s.SetAssignee(ctx, TenantA, a.ID, "agent-1", "Ada", now)
	must(t, err)
	if as.AssigneeID != "agent-1" || as.AssigneeName != "Ada" {
		t.Fatalf("assign = %+v", as)
	}
	as, err = s.SetAssignee(ctx, TenantA, a.ID, "", "stale", now)
	must(t, err)
	if as.AssigneeID != "" || as.AssigneeName != "" {
		t.Fatalf("unassign = %+v", as)
	}
	_, err = s.SetAssignee(ctx, TenantB, a.ID, "x", "", now)
	wantErr(t, err, repo.ErrNotFound)

	res, err := s.SetStatus(ctx, TenantA, a.ID, store.StatusResolved, now.Add(3*time.Minute))
	must(t, err)
	if res.Status != store.StatusResolved || res.ResolvedAt == nil {
		t.Fatalf("resolve = %+v", res)
	}
	reo, err := s.SetStatus(ctx, TenantA, a.ID, store.StatusOpen, now.Add(4*time.Minute))
	must(t, err)
	if reo.Status != store.StatusOpen || reo.ResolvedAt != nil {
		t.Fatalf("reopen = %+v", reo)
	}
	_, err = s.SetStatus(ctx, TenantB, a.ID, store.StatusClosed, now)
	wantErr(t, err, repo.ErrNotFound)

	keys, err := s.DeleteTicket(ctx, TenantA, a.ID)
	must(t, err)
	if len(keys) != 0 {
		t.Fatalf("keys = %v", keys)
	}
	_, err = s.DeleteTicket(ctx, TenantA, a.ID)
	wantErr(t, err, repo.ErrNotFound)
	// the external id is free again after delete
	again := NewTicket(TenantA, "again", now)
	again.ExternalID = a.ExternalID
	must(t, s.CreateTicket(ctx, again))
}

func testFilters(t *testing.T, s repo.Store) {
	now := base()
	var ids []string
	for i := 0; i < 5; i++ {
		tk := NewTicket(TenantA, "ticket", now.Add(time.Duration(i)*time.Second))
		switch i {
		case 0:
			tk.Status, tk.Priority = store.StatusPending, store.PriorityHigh
		case 1:
			tk.AssigneeID, tk.AssigneeName = "agent-1", "Ada"
		case 2:
			tk.Subject, tk.RequesterEmail, tk.RequesterName = "VPN broken", "bob@corp.example", "Bob Builder"
		}
		must(t, s.CreateTicket(ctx, tk))
		ids = append(ids, tk.ID)
	}
	must(t, s.CreateTicket(ctx, NewTicket(TenantB, "vpn elsewhere", now)))
	tag := store.Tag{ID: store.NewID(), TenantID: TenantA, Name: "network", Kind: store.KindTag}
	must(t, s.CreateTag(ctx, tag))
	must(t, s.SetTicketTags(ctx, TenantA, ids[2], []string{tag.ID}))

	list := func(f store.TicketFilter) ([]store.Ticket, int64) {
		t.Helper()
		items, total, err := s.ListTickets(ctx, TenantA, f)
		must(t, err)
		return items, total
	}
	items, total := list(store.TicketFilter{})
	if total != 5 || len(items) != 5 || items[0].ID != ids[4] || items[4].ID != ids[0] {
		t.Fatalf("all: total=%d len=%d", total, len(items))
	}
	if _, n := list(store.TicketFilter{Status: store.StatusPending}); n != 1 {
		t.Fatalf("status filter = %d", n)
	}
	if _, n := list(store.TicketFilter{Priority: store.PriorityHigh}); n != 1 {
		t.Fatalf("priority filter = %d", n)
	}
	if got, n := list(store.TicketFilter{AssigneeID: "agent-1"}); n != 1 || got[0].ID != ids[1] {
		t.Fatalf("assignee filter = %d", n)
	}
	if _, n := list(store.TicketFilter{AssigneeID: store.AssigneeNone}); n != 4 {
		t.Fatalf("unassigned filter = %d", n)
	}
	if got, n := list(store.TicketFilter{TagID: tag.ID}); n != 1 || got[0].ID != ids[2] {
		t.Fatalf("tag filter = %d", n)
	}
	for _, q := range []string{"vpn", "BOB@corp", "builder"} {
		if got, n := list(store.TicketFilter{Query: q}); n != 1 || got[0].ID != ids[2] {
			t.Fatalf("query %q = %d", q, n)
		}
	}
	if _, n := list(store.TicketFilter{Query: "%"}); n != 0 {
		t.Fatalf("wildcard query must be literal, got %d", n)
	}
	page, n := list(store.TicketFilter{Page: 2, PageSize: 2})
	if n != 5 || len(page) != 2 || page[0].ID != ids[2] {
		t.Fatalf("page 2 = %d/%d", len(page), n)
	}
	page, n = list(store.TicketFilter{Page: 9, PageSize: 2})
	if n != 5 || len(page) != 0 {
		t.Fatalf("page 9 = %d/%d", len(page), n)
	}
}

func testComments(t *testing.T, s repo.Store) {
	now := base()
	tk := NewTicket(TenantA, "thread", now)
	must(t, s.CreateTicket(ctx, tk))
	note := store.Comment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Body: "internal", Internal: true,
		AuthorKind: store.AuthorAgent, AuthorID: "agent-1", CreatedAt: now.Add(time.Second)}
	must(t, s.CreateComment(ctx, note))
	reply := store.Comment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Body: "hello", AuthorKind: store.AuthorAgent,
		AuthorID: "agent-1", MessageID: "<ticket.x.1@acme.example>", Delivery: store.DeliverySent, CreatedAt: now.Add(2 * time.Second)}
	must(t, s.CreateComment(ctx, reply))
	dup := reply
	dup.ID = store.NewID()
	wantErr(t, s.CreateComment(ctx, dup), repo.ErrConflict)
	orphan := note
	orphan.ID, orphan.TicketID = store.NewID(), store.NewID()
	wantErr(t, s.CreateComment(ctx, orphan), repo.ErrNotFound)

	got, err := s.GetTicket(ctx, TenantA, tk.ID)
	must(t, err)
	if got.CommentCount != 2 || got.LastMessageID != reply.MessageID {
		t.Fatalf("counters = %d %q", got.CommentCount, got.LastMessageID)
	}
	last, err := s.LastMessageID(ctx, TenantA, tk.ID)
	must(t, err)
	if last != reply.MessageID {
		t.Fatalf("last = %q", last)
	}
	_, err = s.LastMessageID(ctx, TenantB, tk.ID)
	wantErr(t, err, repo.ErrNotFound)

	list, err := s.ListComments(ctx, TenantA, tk.ID)
	must(t, err)
	if len(list) != 2 || list[0].ID != note.ID || list[1].ID != reply.ID || list[0].Delivery != store.DeliveryNone {
		t.Fatalf("list = %+v", list)
	}
	c, err := s.FindCommentByMessageID(ctx, TenantA, reply.MessageID)
	must(t, err)
	if c.ID != reply.ID {
		t.Fatal("find by message id")
	}
	_, err = s.FindCommentByMessageID(ctx, TenantB, reply.MessageID)
	wantErr(t, err, repo.ErrNotFound)
	_, err = s.FindCommentByMessageID(ctx, TenantA, "")
	wantErr(t, err, repo.ErrNotFound)

	must(t, s.SetCommentDelivery(ctx, TenantA, reply.ID, store.DeliveryFailed))
	c, err = s.GetComment(ctx, TenantA, reply.ID)
	must(t, err)
	if c.Delivery != store.DeliveryFailed {
		t.Fatal("delivery not recorded")
	}
	wantErr(t, s.SetCommentDelivery(ctx, TenantB, reply.ID, store.DeliverySent), repo.ErrNotFound)
	_, err = s.GetComment(ctx, TenantB, reply.ID)
	wantErr(t, err, repo.ErrNotFound)

	att := store.Attachment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, CommentID: reply.ID, Filename: "a.txt",
		ContentType: "text/plain", Size: 3, StorageKey: store.AttachmentKey(TenantA, tk.ID, "c1")}
	must(t, s.CreateAttachment(ctx, att))
	keys, err := s.DeleteComment(ctx, TenantA, reply.ID)
	must(t, err)
	if !reflect.DeepEqual(keys, []string{att.StorageKey}) {
		t.Fatalf("comment delete keys = %v", keys)
	}
	_, err = s.DeleteComment(ctx, TenantA, reply.ID)
	wantErr(t, err, repo.ErrNotFound)
	got, err = s.GetTicket(ctx, TenantA, tk.ID)
	must(t, err)
	if got.CommentCount != 1 {
		t.Fatalf("count after delete = %d", got.CommentCount)
	}
	as, err := s.ListAttachments(ctx, TenantA, tk.ID)
	must(t, err)
	if len(as) != 0 {
		t.Fatal("comment attachments not cascaded")
	}
}

func testAttachments(t *testing.T, s repo.Store) {
	now := base()
	tk := NewTicket(TenantA, "files", now)
	must(t, s.CreateTicket(ctx, tk))
	a1 := store.Attachment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Filename: "a.pdf", ContentType: "application/pdf",
		Size: 10, StorageKey: store.AttachmentKey(TenantA, tk.ID, "1"), Checksum: "abc", CreatedAt: now}
	a2 := store.Attachment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Filename: "logo.png", ContentType: "image/png",
		Size: 5, ContentID: "logo@x", Inline: true, StorageKey: store.AttachmentKey(TenantA, tk.ID, "2"), CreatedAt: now.Add(time.Second)}
	must(t, s.CreateAttachment(ctx, a1))
	must(t, s.CreateAttachment(ctx, a2))
	dupKey := a1
	dupKey.ID = store.NewID()
	wantErr(t, s.CreateAttachment(ctx, dupKey), repo.ErrConflict)
	orphan := a1
	orphan.ID, orphan.TicketID, orphan.StorageKey = store.NewID(), store.NewID(), "k3"
	wantErr(t, s.CreateAttachment(ctx, orphan), repo.ErrNotFound)
	badComment := a1
	badComment.ID, badComment.CommentID, badComment.StorageKey = store.NewID(), store.NewID(), "k4"
	wantErr(t, s.CreateAttachment(ctx, badComment), repo.ErrNotFound)

	list, err := s.ListAttachments(ctx, TenantA, tk.ID)
	must(t, err)
	if len(list) != 2 || list[0].ID != a1.ID || !list[1].Inline || list[1].ContentID != "logo@x" {
		t.Fatalf("list = %+v", list)
	}
	got, err := s.GetAttachment(ctx, TenantA, tk.ID, a2.ID)
	must(t, err)
	if got.StorageKey != a2.StorageKey || got.Size != 5 {
		t.Fatalf("get = %+v", got)
	}
	_, err = s.GetAttachment(ctx, TenantB, tk.ID, a2.ID)
	wantErr(t, err, repo.ErrNotFound)
	_, err = s.GetAttachment(ctx, TenantA, store.NewID(), a2.ID)
	wantErr(t, err, repo.ErrNotFound)
	keys, err := s.DeleteAttachmentsByTicket(ctx, TenantA, tk.ID)
	must(t, err)
	if len(keys) != 2 {
		t.Fatalf("keys = %v", keys)
	}
}

func testTags(t *testing.T, s repo.Store) {
	now := base()
	tk := NewTicket(TenantA, "tagged", now)
	must(t, s.CreateTicket(ctx, tk))
	bug := store.Tag{ID: store.NewID(), TenantID: TenantA, Name: "Bug", Kind: store.KindTag, Color: "#ff0000"}
	must(t, s.CreateTag(ctx, bug))
	wantErr(t, s.CreateTag(ctx, store.Tag{ID: store.NewID(), TenantID: TenantA, Name: "bug", Kind: store.KindTag}), repo.ErrConflict)
	cat := store.Tag{ID: store.NewID(), TenantID: TenantA, Name: "bug", Kind: store.KindCategory}
	must(t, s.CreateTag(ctx, cat)) // same name, other kind
	must(t, s.CreateTag(ctx, store.Tag{ID: store.NewID(), TenantID: TenantB, Name: "Bug", Kind: store.KindTag}))

	all, err := s.ListTags(ctx, TenantA, "")
	must(t, err)
	if len(all) != 2 || all[0].Kind != store.KindCategory {
		t.Fatalf("list = %+v", all)
	}
	only, err := s.ListTags(ctx, TenantA, store.KindTag)
	must(t, err)
	if len(only) != 1 || only[0].Color != "#ff0000" {
		t.Fatalf("kind list = %+v", only)
	}
	g, err := s.GetTag(ctx, TenantA, bug.ID)
	must(t, err)
	if g.Name != "Bug" {
		t.Fatal("get tag")
	}
	_, err = s.GetTag(ctx, TenantB, bug.ID)
	wantErr(t, err, repo.ErrNotFound)

	ren := bug
	ren.Name, ren.Kind, ren.Color, ren.Description = "Defect", store.KindCategory, "", "software defects"
	must(t, s.UpdateTag(ctx, ren))
	g, err = s.GetTag(ctx, TenantA, bug.ID)
	must(t, err)
	if g.Name != "Defect" || g.Kind != store.KindTag || g.Color != "" || g.Description != "software defects" {
		t.Fatalf("update = %+v (kind must stay)", g)
	}
	clash := g
	clash.Kind = store.KindTag
	must(t, s.CreateTag(ctx, store.Tag{ID: store.NewID(), TenantID: TenantA, Name: "Other", Kind: store.KindTag}))
	clash.Name = "OTHER"
	wantErr(t, s.UpdateTag(ctx, clash), repo.ErrConflict)
	missing := g
	missing.ID = store.NewID()
	wantErr(t, s.UpdateTag(ctx, missing), repo.ErrNotFound)

	e1, err := s.EnsureTagByName(ctx, TenantA, store.KindTag, "defect")
	must(t, err)
	if e1.ID != bug.ID {
		t.Fatal("ensure must find case-insensitively")
	}
	e2, err := s.EnsureTagByName(ctx, TenantA, "", "vip")
	must(t, err)
	if e2.ID == "" || e2.Kind != store.KindTag || e2.Name != "vip" {
		t.Fatalf("ensure create = %+v", e2)
	}
	e3, err := s.EnsureTagByName(ctx, TenantA, store.KindTag, "VIP")
	must(t, err)
	if e3.ID != e2.ID {
		t.Fatal("ensure not idempotent")
	}

	must(t, s.SetTicketTags(ctx, TenantA, tk.ID, []string{bug.ID, cat.ID}))
	wantErr(t, s.SetTicketTags(ctx, TenantA, tk.ID, []string{bug.ID, store.NewID()}), repo.ErrNotFound)
	wantErr(t, s.SetTicketTags(ctx, TenantB, tk.ID, nil), repo.ErrNotFound)
	m, err := s.TagsForTickets(ctx, TenantA, []string{tk.ID})
	must(t, err)
	if len(m[tk.ID]) != 2 {
		t.Fatalf("failed set must not change the set: %+v", m)
	}
	must(t, s.AddTicketTags(ctx, TenantA, tk.ID, []string{e2.ID, bug.ID}))
	m, err = s.TagsForTickets(ctx, TenantA, []string{tk.ID, store.NewID()})
	must(t, err)
	if len(m[tk.ID]) != 3 || len(m) != 1 {
		t.Fatalf("after add = %+v", m)
	}
	wantErr(t, s.AddTicketTags(ctx, TenantA, tk.ID, []string{store.NewID()}), repo.ErrNotFound)
	m, err = s.TagsForTickets(ctx, TenantB, []string{tk.ID})
	must(t, err)
	if len(m) != 0 {
		t.Fatal("cross-tenant tag read")
	}
	must(t, s.SetTicketTags(ctx, TenantA, tk.ID, []string{cat.ID}))
	m, err = s.TagsForTickets(ctx, TenantA, []string{tk.ID})
	must(t, err)
	if len(m[tk.ID]) != 1 || m[tk.ID][0].ID != cat.ID {
		t.Fatalf("replace = %+v", m)
	}
	must(t, s.DeleteTag(ctx, TenantA, cat.ID))
	wantErr(t, s.DeleteTag(ctx, TenantA, cat.ID), repo.ErrNotFound)
	m, err = s.TagsForTickets(ctx, TenantA, []string{tk.ID})
	must(t, err)
	if len(m[tk.ID]) != 0 {
		t.Fatal("tag delete did not remove links")
	}
}

func testRules(t *testing.T, s repo.Store) {
	r1 := store.Rule{ID: store.NewID(), TenantID: TenantA, Name: "spam", Enabled: true, SortOrder: 10, Match: store.MatchAny,
		Conditions: []store.Condition{{Field: "spamScore", Operator: "gt", Value: "5"}},
		Actions:    []store.Action{{Type: store.ActionDrop}}}
	r2 := store.Rule{ID: store.NewID(), TenantID: TenantA, Name: "vip", Enabled: false, SortOrder: 1,
		Expression: `from.endsWith("@vip.example")`,
		Actions:    []store.Action{{Type: store.ActionTag, TagKind: store.KindTag, TagNames: []string{"vip", "fast"}}, {Type: store.ActionPriority, Priority: store.PriorityHigh}}}
	must(t, s.CreateRule(ctx, r1))
	must(t, s.CreateRule(ctx, r2))
	must(t, s.CreateRule(ctx, store.Rule{ID: store.NewID(), TenantID: TenantB, Name: "b", Enabled: true, Actions: []store.Action{{Type: store.ActionDrop}}}))
	wantErr(t, s.CreateRule(ctx, r1), repo.ErrConflict)

	all, err := s.ListRules(ctx, TenantA)
	must(t, err)
	if len(all) != 2 || all[0].ID != r2.ID || all[1].ID != r1.ID {
		t.Fatalf("order = %+v", all)
	}
	if all[0].Version != 1 || all[0].Match != store.MatchAll || !reflect.DeepEqual(all[0].Actions, r2.Actions) {
		t.Fatalf("stored = %+v", all[0])
	}
	en, err := s.ListEnabledRules(ctx, TenantA)
	must(t, err)
	if len(en) != 1 || en[0].ID != r1.ID || !reflect.DeepEqual(en[0].Conditions, r1.Conditions) {
		t.Fatalf("enabled = %+v", en)
	}
	r2.Enabled, r2.SortOrder = true, 20
	up, err := s.UpdateRule(ctx, r2)
	must(t, err)
	if up.Version != 2 || !up.Enabled {
		t.Fatalf("update = %+v", up)
	}
	got, err := s.GetRule(ctx, TenantA, r2.ID)
	must(t, err)
	if got.Version != 2 || got.SortOrder != 20 {
		t.Fatalf("get = %+v", got)
	}
	_, err = s.GetRule(ctx, TenantB, r2.ID)
	wantErr(t, err, repo.ErrNotFound)
	bad := r2
	bad.TenantID = TenantB
	_, err = s.UpdateRule(ctx, bad)
	wantErr(t, err, repo.ErrNotFound)
	must(t, s.DeleteRule(ctx, TenantA, r1.ID))
	wantErr(t, s.DeleteRule(ctx, TenantA, r1.ID), repo.ErrNotFound)
}

func testMailboxes(t *testing.T, s repo.Store) {
	mb := store.Mailbox{ID: store.NewID(), TenantID: TenantA, Address: " Support@Acme.Example ", DisplayName: "Acme Support",
		Active: true, AutoAck: true, AutoAckTemplate: "Hi {{name}}"}
	must(t, s.CreateMailbox(ctx, mb))
	wantErr(t, s.CreateMailbox(ctx, store.Mailbox{ID: store.NewID(), TenantID: TenantB, Address: "support@acme.example", DisplayName: "x"}), repo.ErrConflict)
	mbB := store.Mailbox{ID: store.NewID(), TenantID: TenantB, Address: "help@beta.example", DisplayName: "Beta", Active: false}
	must(t, s.CreateMailbox(ctx, mbB))

	got, err := s.GetMailbox(ctx, TenantA, mb.ID)
	must(t, err)
	if got.Address != "support@acme.example" || !got.AutoAck {
		t.Fatalf("get = %+v", got)
	}
	_, err = s.GetMailbox(ctx, TenantB, mb.ID)
	wantErr(t, err, repo.ErrNotFound)
	list, err := s.ListMailboxes(ctx, TenantA)
	must(t, err)
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}

	r, err := s.RouteMailbox(ctx, "SUPPORT@acme.example")
	must(t, err)
	if r.TenantID != TenantA || r.MailboxID != mb.ID || !r.Active || !r.AutoAck || r.AutoAckTemplate != "Hi {{name}}" || r.DisplayName != "Acme Support" {
		t.Fatalf("route = %+v", r)
	}
	r, err = s.RouteMailbox(ctx, "help@beta.example")
	must(t, err)
	if r.TenantID != TenantB || r.Active {
		t.Fatalf("route B = %+v", r)
	}
	_, err = s.RouteMailbox(ctx, "nobody@acme.example")
	wantErr(t, err, repo.ErrNotFound)
	_, err = s.RouteMailbox(ctx, "")
	wantErr(t, err, repo.ErrNotFound)

	got.DisplayName, got.Active, got.Address = "Acme Help", false, "HELP@acme.example"
	must(t, s.UpdateMailbox(ctx, got))
	got2, err := s.GetMailbox(ctx, TenantA, mb.ID)
	must(t, err)
	if got2.Address != "help@acme.example" || got2.Active || got2.DisplayName != "Acme Help" {
		t.Fatalf("update = %+v", got2)
	}
	clash := got2
	clash.Address = "help@beta.example"
	wantErr(t, s.UpdateMailbox(ctx, clash), repo.ErrConflict)
	foreign := got2
	foreign.TenantID = TenantB
	wantErr(t, s.UpdateMailbox(ctx, foreign), repo.ErrNotFound)

	tk := NewTicket(TenantA, "via mail", base())
	tk.MailboxID, tk.Source = mb.ID, store.SourceEmail
	must(t, s.CreateTicket(ctx, tk))
	wrongTenant := NewTicket(TenantB, "cross", base())
	wrongTenant.MailboxID = mb.ID
	wantErr(t, s.CreateTicket(ctx, wrongTenant), repo.ErrNotFound)
	wantErr(t, s.DeleteMailbox(ctx, TenantA, mb.ID, false), repo.ErrNotEmpty)
	wantErr(t, s.DeleteMailbox(ctx, TenantB, mb.ID, true), repo.ErrNotFound)
	must(t, s.DeleteMailbox(ctx, TenantA, mb.ID, true))
	after, err := s.GetTicket(ctx, TenantA, tk.ID)
	must(t, err)
	if after.MailboxID != "" {
		t.Fatal("force delete must detach tickets")
	}
	must(t, s.DeleteMailbox(ctx, TenantB, mbB.ID, false))
}

func testHistory(t *testing.T, s repo.Store) {
	now := base()
	tk := NewTicket(TenantA, "hist", now)
	must(t, s.CreateTicket(ctx, tk))
	h1 := store.History{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Field: store.FieldStatus, OldValue: "open", NewValue: "pending",
		ActorKind: store.ActorAgent, ActorID: "agent-1", CreatedAt: now.Add(time.Second)}
	h2 := store.History{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Field: store.FieldAssignee, OldValue: "", NewValue: "agent-1",
		ActorKind: store.ActorRule, CreatedAt: now.Add(2 * time.Second)}
	must(t, s.AppendHistory(ctx, h2))
	must(t, s.AppendHistory(ctx, h1))
	orphan := h1
	orphan.ID, orphan.TicketID = store.NewID(), store.NewID()
	wantErr(t, s.AppendHistory(ctx, orphan), repo.ErrNotFound)
	list, err := s.ListHistory(ctx, TenantA, tk.ID)
	must(t, err)
	if len(list) != 2 || list[0].ID != h1.ID || list[1].ActorKind != store.ActorRule {
		t.Fatalf("history = %+v", list)
	}
	other, err := s.ListHistory(ctx, TenantB, tk.ID)
	must(t, err)
	if len(other) != 0 {
		t.Fatal("cross-tenant history")
	}
}

func testStats(t *testing.T, s repo.Store) {
	now := time.Now().UTC()
	since := now.Add(-48 * time.Hour)
	mk := func(status, prio, assignee string, at time.Time) store.Ticket {
		tk := NewTicket(TenantA, "s", at)
		tk.Status, tk.Priority, tk.AssigneeID, tk.AssigneeName = status, prio, assignee, map[string]string{"a1": "Ada", "a2": "Bob"}[assignee]
		must(t, s.CreateTicket(ctx, tk))
		return tk
	}
	mk(store.StatusOpen, store.PriorityHigh, "", now.Add(-time.Hour))
	mk(store.StatusOpen, store.PriorityNormal, "a1", now.Add(-time.Hour))
	mk(store.StatusPending, store.PriorityNormal, "a1", now.Add(-2*time.Hour))
	mk(store.StatusInProgress, store.PriorityLow, "a2", now.Add(-72*time.Hour)) // before the window
	res := mk(store.StatusResolved, store.PriorityUrgent, "", now.Add(-time.Hour))
	must(t, s.AppendHistory(ctx, store.History{ID: store.NewID(), TenantID: TenantA, TicketID: res.ID, Field: store.FieldStatus,
		OldValue: "open", NewValue: store.StatusResolved, ActorKind: store.ActorAgent, CreatedAt: now.Add(-30 * time.Minute)}))
	must(t, s.CreateTicket(ctx, NewTicket(TenantB, "other", now)))

	st, err := s.TicketStats(ctx, TenantA, since)
	must(t, err)
	if st.Total != 5 || st.ByStatus[store.StatusOpen] != 2 || st.ByStatus[store.StatusResolved] != 1 || st.ByStatus[store.StatusClosed] != 0 {
		t.Fatalf("by status = %+v", st.ByStatus)
	}
	if st.ByPriority[store.PriorityNormal] != 2 || st.ByPriority[store.PriorityUrgent] != 1 {
		t.Fatalf("by priority = %+v", st.ByPriority)
	}
	if st.UnassignedOpen != 1 {
		t.Fatalf("unassigned open = %d", st.UnassignedOpen)
	}
	if len(st.ByAssignee) != 2 || st.ByAssignee[0].AssigneeID != "a1" || st.ByAssignee[0].Count != 2 || st.ByAssignee[0].AssigneeName != "Ada" {
		t.Fatalf("by assignee = %+v", st.ByAssignee)
	}
	if len(st.CreatedPerDay) != 3 || len(st.ResolvedPerDay) != 3 {
		t.Fatalf("series lengths = %d %d", len(st.CreatedPerDay), len(st.ResolvedPerDay))
	}
	var created, resolved int64
	for i := range st.CreatedPerDay {
		created += st.CreatedPerDay[i].Count
		resolved += st.ResolvedPerDay[i].Count
	}
	if created != 4 || resolved != 1 {
		t.Fatalf("series sums = %d %d", created, resolved)
	}
	if st.CreatedPerDay[2].Day != now.Format("2006-01-02") {
		t.Fatalf("last day = %s", st.CreatedPerDay[2].Day)
	}
	empty, err := s.TicketStats(ctx, store.NewID(), since)
	must(t, err)
	if empty.Total != 0 || empty.ByAssignee == nil || empty.ByStatus[store.StatusOpen] != 0 {
		t.Fatalf("empty stats = %+v", empty)
	}
}

func testBackup(t *testing.T, s repo.Store) {
	now := base()
	tk := NewTicket(TenantA, "b", now)
	must(t, s.CreateTicket(ctx, tk))
	must(t, s.CreateComment(ctx, store.Comment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Body: "x", AuthorKind: store.AuthorSystem}))
	must(t, s.CreateAttachment(ctx, store.Attachment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Filename: "f", ContentType: "text/plain", StorageKey: "k-b"}))
	tag := store.Tag{ID: store.NewID(), TenantID: TenantA, Name: "t", Kind: store.KindTag}
	must(t, s.CreateTag(ctx, tag))
	must(t, s.SetTicketTags(ctx, TenantA, tk.ID, []string{tag.ID}))
	must(t, s.AppendHistory(ctx, store.History{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Field: store.FieldPriority, OldValue: "normal", NewValue: "high", ActorKind: store.ActorAgent}))
	must(t, s.CreateMailbox(ctx, store.Mailbox{ID: store.NewID(), TenantID: TenantB, Address: "b@b.example", DisplayName: "B"}))

	ts, err := s.AllTickets(ctx, TenantA)
	must(t, err)
	cs, err := s.AllComments(ctx, TenantA)
	must(t, err)
	as, err := s.AllAttachments(ctx, TenantA)
	must(t, err)
	ls, err := s.AllTagLinks(ctx, TenantA)
	must(t, err)
	hs, err := s.AllHistory(ctx, TenantA)
	must(t, err)
	if len(ts) != 1 || len(cs) != 1 || len(as) != 1 || len(ls) != 1 || len(hs) != 1 || ls[0].TagID != tag.ID {
		t.Fatalf("backup = %d %d %d %d %d", len(ts), len(cs), len(as), len(ls), len(hs))
	}
	ids, err := s.TenantIDs(ctx)
	must(t, err)
	if !reflect.DeepEqual(ids, []string{TenantA, TenantB}) {
		t.Fatalf("tenants = %v", ids)
	}
	for _, fn := range []func() (int, error){
		func() (int, error) { x, err := s.AllTickets(ctx, TenantB); return len(x), err },
		func() (int, error) { x, err := s.AllComments(ctx, TenantB); return len(x), err },
		func() (int, error) { x, err := s.AllAttachments(ctx, TenantB); return len(x), err },
		func() (int, error) { x, err := s.AllTagLinks(ctx, TenantB); return len(x), err },
		func() (int, error) { x, err := s.AllHistory(ctx, TenantB); return len(x), err },
	} {
		n, err := fn()
		must(t, err)
		if n != 0 {
			t.Fatal("cross-tenant backup rows")
		}
	}
	must(t, s.AppendAudit(ctx, store.AuditRow{ID: store.NewID(), TenantID: TenantA, At: time.Now(), ActorKind: "user", Action: "ticket.create", SubjectKind: "ticket", Outcome: "ok", Detail: map[string]any{"k": "v"}}))
}

func testIsolation(t *testing.T, s repo.Store) {
	now := base()
	tk := NewTicket(TenantA, "secret", now)
	must(t, s.CreateTicket(ctx, tk))
	items, total, err := s.ListTickets(ctx, TenantB, store.TicketFilter{})
	must(t, err)
	if total != 0 || len(items) != 0 {
		t.Fatal("tenant B sees tenant A tickets")
	}
	_, err = s.GetTicket(ctx, TenantB, tk.ID)
	wantErr(t, err, repo.ErrNotFound)
	_, err = s.DeleteTicket(ctx, TenantB, tk.ID)
	wantErr(t, err, repo.ErrNotFound)
	c := store.Comment{ID: store.NewID(), TenantID: TenantB, TicketID: tk.ID, Body: "x", AuthorKind: store.AuthorAgent}
	wantErr(t, s.CreateComment(ctx, c), repo.ErrNotFound)
	a := store.Attachment{ID: store.NewID(), TenantID: TenantB, TicketID: tk.ID, Filename: "f", ContentType: "text/plain", StorageKey: "iso"}
	wantErr(t, s.CreateAttachment(ctx, a), repo.ErrNotFound)
	list, err := s.ListComments(ctx, TenantB, tk.ID)
	must(t, err)
	if len(list) != 0 {
		t.Fatal("cross-tenant comments")
	}
	al, err := s.ListAttachments(ctx, TenantB, tk.ID)
	must(t, err)
	if len(al) != 0 {
		t.Fatal("cross-tenant attachments")
	}
	keys, err := s.DeleteAttachmentsByTicket(ctx, TenantB, tk.ID)
	must(t, err)
	if len(keys) != 0 {
		t.Fatal("cross-tenant attachment delete")
	}
	rules, err := s.ListRules(ctx, TenantB)
	must(t, err)
	tags, err := s.ListTags(ctx, TenantB, "")
	must(t, err)
	mbs, err := s.ListMailboxes(ctx, TenantB)
	must(t, err)
	if len(rules)+len(tags)+len(mbs) != 0 {
		t.Fatal("cross-tenant vocabulary")
	}
	wantErr(t, s.DeleteTag(ctx, TenantB, store.NewID()), repo.ErrNotFound)
	_, err = s.DeleteComment(ctx, TenantB, store.NewID())
	wantErr(t, err, repo.ErrNotFound)
}

func testCascade(t *testing.T, s repo.Store) {
	now := base()
	tk := NewTicket(TenantA, "cascade", now)
	must(t, s.CreateTicket(ctx, tk))
	c := store.Comment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Body: "reply", AuthorKind: store.AuthorRequester, MessageID: "<m1@x>"}
	must(t, s.CreateComment(ctx, c))
	k1 := store.AttachmentKey(TenantA, tk.ID, "a1")
	k2 := store.AttachmentKey(TenantA, tk.ID, "a2")
	must(t, s.CreateAttachment(ctx, store.Attachment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Filename: "a", ContentType: "text/plain", StorageKey: k1}))
	must(t, s.CreateAttachment(ctx, store.Attachment{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, CommentID: c.ID, Filename: "b", ContentType: "text/plain", StorageKey: k2}))
	tag := store.Tag{ID: store.NewID(), TenantID: TenantA, Name: "x", Kind: store.KindTag}
	must(t, s.CreateTag(ctx, tag))
	must(t, s.SetTicketTags(ctx, TenantA, tk.ID, []string{tag.ID}))
	must(t, s.AppendHistory(ctx, store.History{ID: store.NewID(), TenantID: TenantA, TicketID: tk.ID, Field: store.FieldStatus, NewValue: "pending", ActorKind: store.ActorSystem}))

	keys, err := s.DeleteTicket(ctx, TenantA, tk.ID)
	must(t, err)
	sort.Strings(keys)
	want := []string{k1, k2}
	sort.Strings(want)
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v want %v", keys, want)
	}
	_, err = s.FindCommentByMessageID(ctx, TenantA, "<m1@x>")
	wantErr(t, err, repo.ErrNotFound)
	hs, err := s.ListHistory(ctx, TenantA, tk.ID)
	must(t, err)
	ls, err := s.AllTagLinks(ctx, TenantA)
	must(t, err)
	as, err := s.AllAttachments(ctx, TenantA)
	must(t, err)
	if len(hs)+len(ls)+len(as) != 0 {
		t.Fatal("ticket delete did not cascade")
	}
	// the tag itself survives; the message id is reusable
	if _, err := s.GetTag(ctx, TenantA, tag.ID); err != nil {
		t.Fatal("tag removed with ticket")
	}
}
