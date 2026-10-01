package store

import "github.com/go-tangra/go-tangra/v4/listquery"

// List definitions of the ticket tables (specs/032-server-side-tables in
// go-tangra). Sort names map to constant SQL expressions only; the unique id
// breaks ties so paging a static list returns every record once.
var (
	// TicketList pages the ticket queue (newest first by default). Status and
	// priority sort by their workflow/urgency rank, not alphabetically; the
	// assignee sorts by its denormalised name with unassigned tickets last.
	TicketList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"subject":    {Expr: "subject", Text: true, DefaultDir: listquery.Asc},
			"status":     {Expr: StatusRankExpr, DefaultDir: listquery.Asc},
			"priority":   {Expr: PriorityRankExpr, DefaultDir: listquery.Desc},
			"assignee":   {Expr: "NULLIF(assignee_name, '')", Text: true, DefaultDir: listquery.Asc},
			"created_at": {Expr: "created_at", DefaultDir: listquery.Desc},
			"updated_at": {Expr: "updated_at", DefaultDir: listquery.Desc},
		},
		Default: "created_at", TieBreak: "id",
	}
	// MailboxList pages the support mailboxes (by address, as before).
	MailboxList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"address": {Expr: "address", Text: true, DefaultDir: listquery.Asc},
			"name":    {Expr: "display_name", Text: true, DefaultDir: listquery.Asc},
		},
		Default: "address", TieBreak: "id",
	}
	// RuleList pages the triage rules (evaluation order by default).
	RuleList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"sort_order": {Expr: "sort_order", DefaultDir: listquery.Asc},
			"name":       {Expr: "name", Text: true, DefaultDir: listquery.Asc},
		},
		Default: "sort_order", TieBreak: "id",
	}
	// TagList pages the tag vocabulary.
	TagList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"name": {Expr: "name", Text: true, DefaultDir: listquery.Asc},
		},
		Default: "name", TieBreak: "id",
	}
)

// Rank expressions of the enumerated ticket columns (constants, never request
// data). StatusRank/PriorityRank are their in-memory equivalents.
const (
	StatusRankExpr   = "CASE status WHEN 'open' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'pending' THEN 3 WHEN 'resolved' THEN 4 WHEN 'closed' THEN 5 END"
	PriorityRankExpr = "CASE priority WHEN 'low' THEN 1 WHEN 'normal' THEN 2 WHEN 'high' THEN 3 WHEN 'urgent' THEN 4 END"
)

// StatusRank is the workflow position of a status (0 when unknown).
func StatusRank(s string) int64 {
	switch s {
	case StatusOpen:
		return 1
	case StatusInProgress:
		return 2
	case StatusPending:
		return 3
	case StatusResolved:
		return 4
	case StatusClosed:
		return 5
	}
	return 0
}

// PriorityRank is the urgency of a priority (0 when unknown).
func PriorityRank(p string) int64 {
	switch p {
	case PriorityLow:
		return 1
	case PriorityNormal:
		return 2
	case PriorityHigh:
		return 3
	case PriorityUrgent:
		return 4
	}
	return 0
}

// TicketSortKey is the in-memory value of a TicketList sort field for t
// (mirrors the SQL expressions; nil sorts last like NULL).
func TicketSortKey(t Ticket, field string) any {
	switch field {
	case "subject":
		return t.Subject
	case "status":
		return StatusRank(t.Status)
	case "priority":
		return PriorityRank(t.Priority)
	case "assignee":
		if t.AssigneeName == "" {
			return nil
		}
		return t.AssigneeName
	case "updated_at":
		return t.UpdatedAt
	}
	return t.CreatedAt
}
