package store

import (
	"testing"
	"time"
)

func TestDaySeries(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	s := DaySeries(now.Add(-24*time.Hour), now)
	if len(s) != 2 || s[0].Day != "2026-09-22" {
		t.Fatalf("series = %+v", s)
	}
	if s := DaySeries(now.AddDate(-50, 0, 0), now); len(s) > 3661 {
		t.Fatalf("unbounded series: %d", len(s))
	}
	if s := DaySeries(now.Add(48*time.Hour), now); len(s) != 0 {
		t.Fatalf("future since = %d", len(s))
	}
	BumpDay(s, now)
	BumpDay(s, now, 5)
	BumpDay(s, now.AddDate(0, 0, -9)) // outside: ignored
	series := DaySeries(now, now)
	BumpDay(series, now, 3)
	if series[0].Count != 3 {
		t.Fatal("bump")
	}
}

func TestEnumsAndHelpers(t *testing.T) {
	for _, s := range Statuses {
		if !ValidStatus(s) {
			t.Fatal(s)
		}
	}
	if ValidStatus("unspecified") || ValidStatus("") || !ValidPriority("urgent") || ValidPriority("p0") {
		t.Fatal("enum validation")
	}
	if !ValidKind(KindCategory) || ValidKind("label") {
		t.Fatal("kind")
	}
	if !Active(StatusPending) || Active(StatusResolved) || Active(StatusClosed) {
		t.Fatal("active")
	}
	if AttachmentKey("t", "k", "a") != "tenants/t/tickets/k/a" {
		t.Fatal("key")
	}
	if !(Ticket{BodyHTML: "<p>"}).HasHTML() || (Ticket{}).HasHTML() {
		t.Fatal("has html")
	}
	f := TicketFilter{PageSize: 500}.Normalized(100)
	if f.Page != 1 || f.PageSize != 100 || f.Offset() != 0 {
		t.Fatalf("normalized = %+v", f)
	}
	f = TicketFilter{Page: 3}.Normalized(0)
	if f.PageSize != 25 || f.Offset() != 50 {
		t.Fatalf("defaults = %+v", f)
	}
}
