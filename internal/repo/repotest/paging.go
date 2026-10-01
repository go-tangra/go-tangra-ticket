package repotest

import (
	"cmp"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// Req builds a validated list request (zero values take spec defaults).
func Req(t *testing.T, spec listquery.Spec, page, size int, sort string, order listquery.Dir) listquery.Request {
	t.Helper()
	r, err := listquery.New(page, size, sort, order, spec)
	if err != nil {
		t.Fatalf("request %d/%d %s %s: %v", page, size, sort, order, err)
	}
	return r
}

// pageFn reads one page of a list.
type pageFn[T any] func(req listquery.Request) ([]T, int, listquery.Request, error)

// walk pages through a list of want records with page size 3 in every sort
// field × direction of spec and checks each record comes exactly once, the
// total on every page, and the order (key ascending/descending, nil last,
// id tie-breaker in the same direction).
func walk[T any](t *testing.T, spec listquery.Spec, want int, page pageFn[T], key func(T, string) any, id func(T) string) {
	t.Helper()
	for field := range spec.Fields {
		for _, dir := range []listquery.Dir{listquery.Asc, listquery.Desc} {
			var got []T
			pages := (want + 2) / 3
			for p := 1; p <= pages; p++ {
				items, total, applied, err := page(Req(t, spec, p, 3, field, dir))
				must(t, err)
				if total != want || applied.Page != p || applied.Sort != field || applied.Order != dir {
					t.Fatalf("%s %s page %d: total=%d applied=%+v", field, dir, p, total, applied)
				}
				got = append(got, items...)
			}
			seen := map[string]bool{}
			for _, it := range got {
				if seen[id(it)] {
					t.Fatalf("%s %s: %s returned twice", field, dir, id(it))
				}
				seen[id(it)] = true
			}
			if len(got) != want {
				t.Fatalf("%s %s: %d of %d records", field, dir, len(got), want)
			}
			for i := 1; i < len(got); i++ {
				if c := orderCmp(key(got[i-1], field), key(got[i], field), id(got[i-1]), id(got[i]), dir); c > 0 {
					t.Fatalf("%s %s: out of order at %d: %v (%s) before %v (%s)", field, dir, i,
						key(got[i-1], field), id(got[i-1]), key(got[i], field), id(got[i]))
				}
			}
		}
	}
}

// orderCmp compares two adjacent rows the way listquery orders them.
func orderCmp(a, b any, ida, idb string, dir listquery.Dir) int {
	switch {
	case a == nil && b == nil:
	case a == nil:
		return 1
	case b == nil:
		return -1
	default:
		if c := keyCmp(a, b); c != 0 {
			if dir == listquery.Desc {
				return -c
			}
			return c
		}
	}
	c := cmp.Compare(ida, idb)
	if dir == listquery.Desc {
		return -c
	}
	return c
}

func keyCmp(a, b any) int {
	switch x := a.(type) {
	case string:
		return cmp.Compare(strings.ToLower(x), strings.ToLower(b.(string)))
	case int64:
		return cmp.Compare(x, b.(int64))
	case time.Time:
		return x.Compare(b.(time.Time))
	}
	panic(fmt.Sprintf("key type %T", a))
}

// testPaging: every list pages each record exactly once in every sort field ×
// direction (incl. status/priority ranks and the assignee with unassigned
// last), totals count only the caller's tenant, and the vocabulary lists page
// the same way.
func testPaging(t *testing.T, s repo.Store) {
	now := base().Truncate(time.Second)
	subjects := []string{"alpha", "Alpha", "beta", "gamma", "delta", "Beta", "epsilon"}
	statuses := []string{store.StatusOpen, store.StatusInProgress, store.StatusPending, store.StatusResolved, store.StatusClosed}
	priorities := []string{store.PriorityLow, store.PriorityNormal, store.PriorityHigh, store.PriorityUrgent}
	assignees := []string{"", "Ada", "bob", "", "Carol", "ada"}
	const n = 14
	for i := range n {
		tk := NewTicket(TenantA, subjects[i%len(subjects)], now.Add(time.Duration(i/3)*time.Second)) // equal created_at in threes
		tk.Status, tk.Priority = statuses[i%len(statuses)], priorities[i%len(priorities)]
		if name := assignees[i%len(assignees)]; name != "" {
			tk.AssigneeID, tk.AssigneeName = "agent-"+strings.ToLower(name), name
		}
		tk.UpdatedAt = now.Add(time.Duration(n-i%5) * time.Minute)
		must(t, s.CreateTicket(ctx, tk))
	}
	for i := range 2 {
		must(t, s.CreateTicket(ctx, NewTicket(TenantB, fmt.Sprintf("other%d", i), now)))
	}
	walk(t, store.TicketList, n, func(r listquery.Request) ([]store.Ticket, int, listquery.Request, error) {
		return s.ListTickets(ctx, TenantA, store.TicketFilter{}, r)
	}, store.TicketSortKey, func(tk store.Ticket) string { return tk.ID })
	// A filtered list pages within the filter; the other tenant counts its own.
	walk(t, store.TicketList, 5, func(r listquery.Request) ([]store.Ticket, int, listquery.Request, error) {
		return s.ListTickets(ctx, TenantA, store.TicketFilter{AssigneeID: store.AssigneeNone}, r)
	}, store.TicketSortKey, func(tk store.Ticket) string { return tk.ID })
	if _, total, _, err := s.ListTickets(ctx, TenantB, store.TicketFilter{}, Req(t, store.TicketList, 0, 0, "", "")); err != nil || total != 2 {
		t.Fatalf("tenant B total = %d %v", total, err)
	}

	names := []string{"Support", "billing", "Support", "sales", "Billing"}
	for i, name := range names {
		must(t, s.CreateMailbox(ctx, store.Mailbox{ID: store.NewID(), TenantID: TenantA, Address: fmt.Sprintf("%c@paging.example", 'e'-rune(i)), DisplayName: name, Active: true}))
	}
	must(t, s.CreateMailbox(ctx, store.Mailbox{ID: store.NewID(), TenantID: TenantB, Address: "z@paging.example", DisplayName: "B"}))
	walk(t, store.MailboxList, len(names), func(r listquery.Request) ([]store.Mailbox, int, listquery.Request, error) {
		return s.PageMailboxes(ctx, TenantA, r)
	}, func(m store.Mailbox, f string) any {
		if f == "name" {
			return m.DisplayName
		}
		return m.Address
	}, func(m store.Mailbox) string { return m.ID })

	for i, name := range []string{"spam", "Vip", "alpha", "vip2", "Beta"} {
		must(t, s.CreateRule(ctx, store.Rule{ID: store.NewID(), TenantID: TenantA, Name: name, Enabled: i%2 == 0, SortOrder: 10 * (i % 3),
			Match: store.MatchAll, Actions: []store.Action{{Type: store.ActionDrop}}}))
	}
	must(t, s.CreateRule(ctx, store.Rule{ID: store.NewID(), TenantID: TenantB, Name: "b", Actions: []store.Action{{Type: store.ActionDrop}}}))
	walk(t, store.RuleList, 5, func(r listquery.Request) ([]store.Rule, int, listquery.Request, error) {
		return s.PageRules(ctx, TenantA, r)
	}, func(r store.Rule, f string) any {
		if f == "name" {
			return r.Name
		}
		return int64(r.SortOrder)
	}, func(r store.Rule) string { return r.ID })

	for i, name := range []string{"network", "Hardware", "access", "Zeta", "billing"} {
		kind := store.KindTag
		if i%2 == 1 {
			kind = store.KindCategory
		}
		must(t, s.CreateTag(ctx, store.Tag{ID: store.NewID(), TenantID: TenantA, Name: name, Kind: kind}))
	}
	must(t, s.CreateTag(ctx, store.Tag{ID: store.NewID(), TenantID: TenantB, Name: "b", Kind: store.KindTag}))
	tagKey := func(tg store.Tag, _ string) any { return tg.Name }
	tagID := func(tg store.Tag) string { return tg.ID }
	walk(t, store.TagList, 5, func(r listquery.Request) ([]store.Tag, int, listquery.Request, error) {
		return s.PageTags(ctx, TenantA, "", r)
	}, tagKey, tagID)
	walk(t, store.TagList, 2, func(r listquery.Request) ([]store.Tag, int, listquery.Request, error) {
		return s.PageTags(ctx, TenantA, store.KindCategory, r)
	}, tagKey, tagID)

	// Beyond the last page clamps to it; an empty list answers page 1.
	items, total, applied, err := s.PageTags(ctx, TenantA, "", Req(t, store.TagList, 7, 2, "", ""))
	must(t, err)
	if total != 5 || applied.Page != 3 || len(items) != 1 {
		t.Fatalf("clamp = %d/%d page %d", len(items), total, applied.Page)
	}
	other := "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c77"
	mbs, total, applied, err := s.PageMailboxes(ctx, other, Req(t, store.MailboxList, 4, 2, "", ""))
	must(t, err)
	if total != 0 || applied.Page != 1 || mbs == nil || len(mbs) != 0 {
		t.Fatalf("empty = %v/%d page %d", mbs, total, applied.Page)
	}
}
