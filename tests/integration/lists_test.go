//go:build integration

package integration

// Feature 032 against TimescaleDB under the RLS app role: the ticket queue
// pages every ticket exactly once in every sort field × direction (incl. the
// priority rank and the assignee with unassigned last), a request without a
// sort keeps the previous newest-first order, the 0004 updated_at index
// exists, and totals count only the caller's tenant.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tickets"
)

const otherTenant = "33333333-3333-7333-8333-333333333333"

func TestTicketListsPaging(t *testing.T) {
	ctx := context.Background()
	in := &infra{}
	setupDB(t, ctx, in)
	if err := store.Migrate(ctx, in.adminDSN); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := store.Open(ctx, in.appDSN, 4)
	if err != nil {
		t.Fatal(err)
	}
	db := repodb.New(st)
	t.Cleanup(db.Close)

	admin, err := pgx.Connect(ctx, in.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	var idx bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'ticket_tickets_updated')`).Scan(&idx); err != nil || !idx {
		t.Fatalf("updated_at index: %v %v", idx, err)
	}
	_ = admin.Close(ctx)

	priorities := []string{store.PriorityLow, store.PriorityNormal, store.PriorityHigh, store.PriorityUrgent}
	names := []string{"", "Ada", "bob", "", "Carol"}
	now := time.Now().UTC().Truncate(time.Second)
	const n = 23
	for i := range n {
		tk := repotest.NewTicket(tenant, fmt.Sprintf("subject %02d", (i*7)%n), now.Add(time.Duration(i/4)*time.Second))
		tk.Priority = priorities[i%len(priorities)]
		if name := names[i%len(names)]; name != "" {
			tk.AssigneeID, tk.AssigneeName = "agent-"+name, name
		}
		tk.UpdatedAt = now.Add(time.Duration(i%6) * time.Minute)
		if err := db.CreateTicket(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 4 {
		if err := db.CreateTicket(ctx, repotest.NewTicket(otherTenant, fmt.Sprintf("other %d", i), now)); err != nil {
			t.Fatal(err)
		}
	}

	svc := tickets.New(tickets.Deps{Store: db, Agents: agents.NewFake()})
	subj := authz.Subjects{TenantID: tenant, UserID: "viewer", ActorKind: authz.ActorAgent}
	list := func(req listquery.Request) ([]tickets.View, int, listquery.Request) {
		t.Helper()
		items, total, applied, err := svc.List(ctx, subj, store.TicketFilter{}, req)
		if err != nil {
			t.Fatal(err)
		}
		return items, total, applied
	}

	for field := range store.TicketList.Fields {
		for _, dir := range []listquery.Dir{listquery.Asc, listquery.Desc} {
			seen := map[string]bool{}
			var prev *tickets.View
			for page := 1; page <= (n+4)/5; page++ {
				req, err := listquery.New(page, 5, field, dir, store.TicketList)
				if err != nil {
					t.Fatal(err)
				}
				items, total, applied := list(req)
				if total != n || applied.Page != page {
					t.Fatalf("%s %s page %d: total %d applied %+v", field, dir, page, total, applied)
				}
				for i := range items {
					v := items[i]
					if seen[v.ID] {
						t.Fatalf("%s %s: %s twice", field, dir, v.ID)
					}
					seen[v.ID] = true
					if prev != nil && !inOrder(*prev, v, field, dir) {
						t.Fatalf("%s %s: %v before %v", field, dir, store.TicketSortKey(prev.Ticket, field), store.TicketSortKey(v.Ticket, field))
					}
					prev = &items[i]
				}
			}
			if len(seen) != n {
				t.Fatalf("%s %s: %d of %d tickets", field, dir, len(seen), n)
			}
		}
	}

	// No sort = the previous order (created_at DESC, id DESC).
	def, err := listquery.New(1, 200, "", "", store.TicketList)
	if err != nil {
		t.Fatal(err)
	}
	items, _, applied := list(def)
	if applied.Sort != "created_at" || applied.Order != listquery.Desc {
		t.Fatalf("default = %+v", applied)
	}
	all, err := db.AllTickets(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	newest := all[0]
	for _, tk := range all {
		if tk.CreatedAt.After(newest.CreatedAt) || (tk.CreatedAt.Equal(newest.CreatedAt) && tk.ID > newest.ID) {
			newest = tk
		}
	}
	if items[0].ID != newest.ID {
		t.Fatalf("default first = %s, want newest %s", items[0].ID, newest.ID)
	}

	other := authz.Subjects{TenantID: otherTenant, UserID: "viewer", ActorKind: authz.ActorAgent}
	if _, total, _, err := svc.List(ctx, other, store.TicketFilter{}, def); err != nil || total != 4 {
		t.Fatalf("other tenant total = %d %v", total, err)
	}
	unassigned, err := listquery.New(1, 3, "assignee", "", store.TicketList)
	if err != nil {
		t.Fatal(err)
	}
	if items, total, _, err := svc.List(ctx, subj, store.TicketFilter{AssigneeID: store.AssigneeNone}, unassigned); err != nil || total != 9 || len(items) != 3 {
		t.Fatalf("unassigned filter = %d/%d %v", len(items), total, err)
	}
}

// inOrder reports whether b may follow a in field/dir order (nil last, id
// tie-breaker in the same direction).
func inOrder(a, b tickets.View, field string, dir listquery.Dir) bool {
	ka, kb := store.TicketSortKey(a.Ticket, field), store.TicketSortKey(b.Ticket, field)
	c := 0
	switch {
	case ka == nil && kb == nil:
	case ka == nil:
		return false
	case kb == nil:
		return true
	default:
		c = compare(ka, kb)
	}
	if c == 0 {
		c = compare(a.ID, b.ID)
	}
	if dir == listquery.Desc {
		c = -c
	}
	return c < 0
}

func compare(a, b any) int {
	switch x := a.(type) {
	case string:
		y := b.(string)
		switch {
		case lower(x) < lower(y):
			return -1
		case lower(x) > lower(y):
			return 1
		}
		return 0
	case int64:
		y := b.(int64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case time.Time:
		return x.Compare(b.(time.Time))
	}
	panic(fmt.Sprintf("key %T", a))
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
