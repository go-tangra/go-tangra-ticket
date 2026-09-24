package stats

// T064: dashboard statistics (FR-017, research D12) — counts by status,
// priority and assignee, unassigned open tickets, created per day and resolved
// per day (from history transitions into resolved) over a bounded window.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

const (
	tenantA = "11111111-1111-7111-8111-111111111111"
	tenantB = "22222222-2222-7222-8222-222222222222"
)

var now = time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)

func day(n int) time.Time { return now.AddDate(0, 0, -n) }

func seed(t *testing.T) *memstore.Mem {
	t.Helper()
	ctx := context.Background()
	m := memstore.New()
	m.Now = func() time.Time { return now }
	mk := func(tenant, status, prio, assignee, name string, created time.Time) string {
		tk := store.Ticket{ID: store.NewID(), TenantID: tenant, Subject: "s", Status: status, Priority: prio, Source: store.SourceManual,
			AssigneeID: assignee, AssigneeName: name, CreatedBy: "u", CreatedAt: created}
		if err := m.CreateTicket(ctx, tk); err != nil {
			t.Fatal(err)
		}
		return tk.ID
	}
	mk(tenantA, store.StatusOpen, store.PriorityHigh, "", "", day(0))
	mk(tenantA, store.StatusOpen, store.PriorityNormal, "", "", day(1))
	mk(tenantA, store.StatusInProgress, store.PriorityUrgent, "u1", "Ann", day(1))
	mk(tenantA, store.StatusPending, store.PriorityNormal, "u1", "Ann", day(5))
	mk(tenantA, store.StatusOpen, store.PriorityLow, "u2", "Bob", day(40)) // outside the default window
	r1 := mk(tenantA, store.StatusResolved, store.PriorityNormal, "u2", "Bob", day(3))
	c1 := mk(tenantA, store.StatusClosed, store.PriorityNormal, "", "", day(2))
	mk(tenantB, store.StatusOpen, store.PriorityHigh, "", "", day(0)) // another tenant
	hist := func(ticket, field, newV string, at time.Time) {
		if err := m.AppendHistory(ctx, store.History{ID: store.NewID(), TenantID: tenantA, TicketID: ticket, Field: field, OldValue: "open",
			NewValue: newV, ActorKind: store.ActorAgent, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	hist(r1, store.FieldStatus, store.StatusResolved, day(1))
	hist(c1, store.FieldStatus, store.StatusResolved, day(1))
	hist(c1, store.FieldStatus, store.StatusClosed, day(0))     // not a resolve
	hist(r1, store.FieldPriority, store.StatusResolved, day(0)) // wrong field
	hist(r1, store.FieldStatus, store.StatusResolved, day(50))  // outside the window
	return m
}

func agent(tenant string) authz.Subjects {
	return authz.Subjects{TenantID: tenant, UserID: "u1", ActorKind: authz.ActorAgent}
}

func count(series []store.DayCount, at time.Time) int64 {
	d := at.UTC().Format(store.DayFormat)
	for _, c := range series {
		if c.Day == d {
			return c.Count
		}
	}
	return -1
}

func TestStatsAggregates(t *testing.T) {
	s := New(seed(t))
	s.SetClock(func() time.Time { return now })
	st, err := s.Get(context.Background(), agent(tenantA), 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 7 {
		t.Fatalf("total = %d", st.Total)
	}
	wantStatus := map[string]int64{store.StatusOpen: 3, store.StatusInProgress: 1, store.StatusPending: 1, store.StatusResolved: 1, store.StatusClosed: 1}
	for k, v := range wantStatus {
		if st.ByStatus[k] != v {
			t.Errorf("by_status[%s] = %d want %d", k, st.ByStatus[k], v)
		}
	}
	if st.ByPriority[store.PriorityNormal] != 4 || st.ByPriority[store.PriorityUrgent] != 1 || st.ByPriority[store.PriorityLow] != 1 {
		t.Errorf("by_priority = %v", st.ByPriority)
	}
	// open work only: Ann has 2 active, Bob 1 active (his resolved ticket does not count)
	if len(st.ByAssignee) != 2 || st.ByAssignee[0].AssigneeID != "u1" || st.ByAssignee[0].Count != 2 || st.ByAssignee[0].AssigneeName != "Ann" ||
		st.ByAssignee[1].AssigneeID != "u2" || st.ByAssignee[1].Count != 1 {
		t.Errorf("by_assignee = %+v", st.ByAssignee)
	}
	if st.UnassignedOpen != 2 {
		t.Errorf("unassigned_open = %d", st.UnassignedOpen)
	}
	// default window: 30 days ending today, zero-filled
	if len(st.CreatedPerDay) != DefaultDays || len(st.ResolvedPerDay) != DefaultDays {
		t.Fatalf("series lengths = %d/%d", len(st.CreatedPerDay), len(st.ResolvedPerDay))
	}
	if st.CreatedPerDay[DefaultDays-1].Day != now.Format(store.DayFormat) || st.CreatedPerDay[0].Day != day(DefaultDays-1).Format(store.DayFormat) {
		t.Fatalf("window = %s..%s", st.CreatedPerDay[0].Day, st.CreatedPerDay[DefaultDays-1].Day)
	}
	if count(st.CreatedPerDay, day(0)) != 1 || count(st.CreatedPerDay, day(1)) != 2 || count(st.CreatedPerDay, day(5)) != 1 || count(st.CreatedPerDay, day(4)) != 0 {
		t.Errorf("created_per_day = %+v", st.CreatedPerDay)
	}
	var created int64
	for _, c := range st.CreatedPerDay {
		created += c.Count
	}
	if created != 6 { // the 40-day-old ticket is outside
		t.Errorf("created in window = %d", created)
	}
	if count(st.ResolvedPerDay, day(1)) != 2 || count(st.ResolvedPerDay, day(0)) != 0 {
		t.Errorf("resolved_per_day = %+v", st.ResolvedPerDay)
	}
}

func TestStatsWindow(t *testing.T) {
	s := New(seed(t))
	s.SetClock(func() time.Time { return now })
	ctx := context.Background()
	for _, c := range []struct{ in, want int }{{-5, DefaultDays}, {0, DefaultDays}, {1, 1}, {7, 7}, {MaxDays, MaxDays}, {MaxDays + 100, MaxDays}} {
		st, err := s.Get(ctx, agent(tenantA), c.in)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.CreatedPerDay) != c.want || len(st.ResolvedPerDay) != c.want {
			t.Errorf("days=%d: series = %d want %d", c.in, len(st.CreatedPerDay), c.want)
		}
	}
	// a 60-day window includes the old ticket and the old resolve
	st, _ := s.Get(ctx, agent(tenantA), 60)
	if count(st.CreatedPerDay, day(40)) != 1 || count(st.ResolvedPerDay, day(50)) != 1 {
		t.Errorf("60-day window = %+v / %+v", st.CreatedPerDay, st.ResolvedPerDay)
	}
	if got := Days("abc"); got != DefaultDays {
		t.Errorf("Days(abc) = %d", got)
	}
	if got := Days("12"); got != 12 {
		t.Errorf("Days(12) = %d", got)
	}
	if got := Days(""); got != DefaultDays {
		t.Errorf("Days() = %d", got)
	}
}

func TestStatsTenantIsolationAndErrors(t *testing.T) {
	m := seed(t)
	s := New(m)
	ctx := context.Background()
	st, err := s.Get(ctx, agent(tenantB), 0)
	if err != nil || st.Total != 1 || st.UnassignedOpen != 1 {
		t.Fatalf("tenant B = %+v %v", st, err)
	}
	empty, err := s.Get(ctx, agent("33333333-3333-7333-8333-333333333333"), 0)
	if err != nil || empty.Total != 0 || empty.ByAssignee == nil || len(empty.ByStatus) != len(store.Statuses) {
		t.Fatalf("empty tenant = %+v %v", empty, err)
	}
	if _, err := s.Get(ctx, authz.Subjects{UserID: "u"}, 0); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("no tenant = %v", err)
	}
	m.FailNext("TicketStats")
	if _, err := s.Get(ctx, agent(tenantA), 0); err == nil {
		t.Fatal("store error swallowed")
	}
}
