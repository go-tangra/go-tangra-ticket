package store

import (
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"
)

func TestListSpecsValid(t *testing.T) {
	for name, s := range map[string]listquery.Spec{"tickets": TicketList, "mailboxes": MailboxList, "rules": RuleList, "tags": TagList} {
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	r, err := listquery.New(0, 0, "", "", TicketList)
	if err != nil || r.OrderBy(TicketList) != "created_at DESC, id DESC" || r.PageSize != 25 {
		t.Fatalf("ticket default = %+v %v %q", r, err, r.OrderBy(TicketList))
	}
	if _, err := listquery.New(1, 200, "", "", TicketList); err != nil {
		t.Fatalf("max size: %v", err)
	}
}

func TestRanksAndSortKey(t *testing.T) {
	for i, s := range []string{StatusOpen, StatusInProgress, StatusPending, StatusResolved, StatusClosed} {
		if StatusRank(s) != int64(i+1) {
			t.Fatalf("status rank %s", s)
		}
	}
	for i, p := range []string{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent} {
		if PriorityRank(p) != int64(i+1) {
			t.Fatalf("priority rank %s", p)
		}
	}
	if StatusRank("x") != 0 || PriorityRank("x") != 0 {
		t.Fatal("unknown rank")
	}
	now := time.Now()
	tk := Ticket{Subject: "S", Status: StatusPending, Priority: PriorityHigh, AssigneeName: "Ann", CreatedAt: now, UpdatedAt: now.Add(time.Hour)}
	want := map[string]any{"subject": "S", "status": int64(3), "priority": int64(3), "assignee": "Ann", "created_at": now, "updated_at": now.Add(time.Hour)}
	for f, v := range want {
		if got := TicketSortKey(tk, f); got != v {
			t.Fatalf("%s = %v", f, got)
		}
	}
	if TicketSortKey(Ticket{}, "assignee") != nil {
		t.Fatal("unassigned must be nil")
	}
}
