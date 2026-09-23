// Package thread holds the email-threading rules shared by the inbound edge
// and the outbound reply path (research D4), ported from go-tangra-ticket
// internal/thread: the "[#<ticket-id>]" reference token and the reference line
// appended once to outbound bodies, the single-"Re:" reply subject, outbound
// Message-IDs, and Resolve — mapping an inbound message to an existing ticket
// of ONE tenant (the tenant its mailbox routed to) by, in order, a recorded
// comment message id in the threading chain, a ticket root message id in the
// chain, a token in the body, then a token in the subject. Every lookup is
// tenant-scoped (and RLS-confined in the store), so a chain or token pointing
// at another tenant's ticket never matches.
package thread

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

// ErrTenant is returned by Resolve without a tenant.
var ErrTenant = errors.New("thread: tenant required")

// DefaultDomain is the Message-ID domain when none is usable.
const DefaultDomain = "tickets.local"

// tokenRe matches "[#<id>]"; ticket ids are UUIDs, any 8–64 hex/dash run is
// accepted (legacy references).
var tokenRe = regexp.MustCompile(`\[#([0-9a-fA-F-]{8,64})\]`)

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// Token renders the reference token of a ticket id.
func Token(ticketID string) string { return "[#" + ticketID + "]" }

// ParseToken extracts the first ticket id token from s ("" when none).
func ParseToken(s string) string {
	if m := tokenRe.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return ""
}

// ReferenceLine is the human-readable body line carrying the reference.
func ReferenceLine(ticketID string) string { return "Ticket reference: " + Token(ticketID) }

// AppendReference adds the reference line to an outbound body once; a body
// that already carries a token is returned unchanged.
func AppendReference(body, ticketID string) string {
	if ParseToken(body) != "" || strings.Contains(body, Token(ticketID)) {
		return body
	}
	return strings.TrimRight(body, "\r\n") + "\n\n--\n" + ReferenceLine(ticketID) + "\n"
}

// ReplySubject is the outbound subject: one line, a single "Re:" prefix and no
// token (the reference lives in the body).
func ReplySubject(original string) string {
	s := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, original)), " ")
	if s == "" {
		s = "(no subject)"
	}
	if !hasRePrefix(s) {
		s = "Re: " + s
	}
	return s
}

func hasRePrefix(s string) bool {
	low := strings.ToLower(s)
	return strings.HasPrefix(low, "re:") || strings.HasPrefix(low, "re ")
}

// DomainOf returns the lower-cased domain of an address ("" when none).
func DomainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[i+1:]))
}

// MessageID mints an outbound message id "ticket.<id>.<uuid>@<domain>" (no
// angle brackets); an unusable domain falls back to DefaultDomain.
func MessageID(ticketID, domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !domainRe.MatchString(domain) {
		domain = DefaultDomain
	}
	return "ticket." + ticketID + "." + store.NewID() + "@" + domain
}

// Store is the lookup surface Resolve needs (repo.Store satisfies it).
type Store interface {
	FindCommentByMessageID(ctx context.Context, tenantID, messageID string) (store.Comment, error)
	FindTicketByExternalID(ctx context.Context, tenantID, externalID string) (store.Ticket, error)
	GetTicket(ctx context.Context, tenantID, id string) (store.Ticket, error)
}

// How a message was matched.
const (
	ByComment      = "comment"
	ByExternalID   = "external_id"
	ByBodyToken    = "body_token"
	BySubjectToken = "subject_token"
)

// Input is what Resolve looks at.
type Input struct {
	MessageIDs []string // In-Reply-To then References (mailparse.Message.ThreadIDs)
	Text       string   // plain-text body
	Subject    string
}

// Match is the resolved ticket ("" when the message starts a new thread).
type Match struct {
	TicketID string
	By       string
}

// Resolve maps an inbound message to a ticket of tenantID (research D4). A
// store failure other than "not found" is returned so the edge can ask the
// relay to retry instead of opening a duplicate ticket.
func Resolve(ctx context.Context, st Store, tenantID string, in Input) (Match, error) {
	if tenantID == "" {
		return Match{}, ErrTenant
	}
	for _, id := range in.MessageIDs {
		c, err := st.FindCommentByMessageID(ctx, tenantID, id)
		if err == nil {
			return Match{TicketID: c.TicketID, By: ByComment}, nil
		}
		if !errors.Is(err, repo.ErrNotFound) {
			return Match{}, err
		}
	}
	for _, id := range in.MessageIDs {
		t, err := st.FindTicketByExternalID(ctx, tenantID, id)
		if err == nil {
			return Match{TicketID: t.ID, By: ByExternalID}, nil
		}
		if !errors.Is(err, repo.ErrNotFound) {
			return Match{}, err
		}
	}
	for _, c := range []struct{ text, by string }{{in.Text, ByBodyToken}, {in.Subject, BySubjectToken}} {
		tok := ParseToken(c.text)
		if tok == "" {
			continue
		}
		t, err := st.GetTicket(ctx, tenantID, tok)
		if err == nil {
			return Match{TicketID: t.ID, By: c.by}, nil
		}
		if !errors.Is(err, repo.ErrNotFound) {
			return Match{}, err
		}
	}
	return Match{}, nil
}
