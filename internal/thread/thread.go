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

// Token renders the subject token for a ticket id.
func Token(ticketID string) string {
	return "[#" + ticketID + "]"
}

// ParseToken extracts the ticket id from a subject, or "" if none is present.
func ParseToken(subject string) string {
	m := tokenRe.FindStringSubmatch(subject)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// ReplySubject builds the outbound reply subject: a single "Re:" prefix and the
// ticket token appended exactly once.
func ReplySubject(original, ticketID string) string {
	s := strings.TrimSpace(original)
	if s == "" {
		s = "(no subject)"
	}
	if !hasRePrefix(s) {
		s = "Re: " + s
	}
	if ParseToken(s) == "" {
		s = s + " " + Token(ticketID)
	}
	return s
}

func hasRePrefix(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(low, "re:") || strings.HasPrefix(low, "re ")
}
