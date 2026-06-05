// Package thread holds the email-threading helpers shared by the outbound
// reply path (service) and the inbound webhook: the subject token that carries
// the ticket id and the logic to parse it back out of a reply subject.
package thread

import (
	"regexp"
	"strings"
)

// tokenRe matches the ticket id embedded in a subject as "[#<id>]". Ticket ids
// are UUIDs, but we accept any hex/dash run of reasonable length.
var tokenRe = regexp.MustCompile(`\[#([0-9a-fA-F-]{8,})\]`)

// Token renders the reference token for a ticket id. It is embedded in the
// outbound message BODY (a reference line) so the subject stays clean.
func Token(ticketID string) string {
	return "[#" + ticketID + "]"
}

// ParseToken extracts the ticket id from any text (subject or body), or "" if
// none is present.
func ParseToken(s string) string {
	m := tokenRe.FindStringSubmatch(s)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// ReplySubject builds the outbound reply subject: a single "Re:" prefix, no
// token (the reference lives in the body now).
func ReplySubject(original string) string {
	s := strings.TrimSpace(original)
	if s == "" {
		s = "(no subject)"
	}
	if !hasRePrefix(s) {
		s = "Re: " + s
	}
	return s
}

// ReferenceLine is the human-readable body line carrying the ticket reference.
func ReferenceLine(ticketID string) string {
	return "Ticket reference: " + Token(ticketID)
}

// AppendReference adds the reference line to an outbound body (once). If the
// body already contains the token it is returned unchanged.
func AppendReference(body, ticketID string) string {
	if ParseToken(body) != "" {
		return body
	}
	b := strings.TrimRight(body, "\n")
	return b + "\n\n--\n" + ReferenceLine(ticketID) + "\n"
}

func hasRePrefix(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(low, "re:") || strings.HasPrefix(low, "re ")
}
