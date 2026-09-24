package httpapi

import (
	"net/http"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/backup"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/comments"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailboxes"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/rules"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/stats"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/stream"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tags"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tickets"
)

// Prefix of the browser API.
const Prefix = "/api/ticket/v1"

// Deps wire the HTTP handlers. Every field is optional: a route whose service
// is not wired answers 501 not_implemented. The domain services of the user
// stories (tickets, comments, tags, rules, mailboxes, stats, backup) are added
// here as they land.
type Deps struct {
	Hub       *stream.Hub
	Health    func() map[string]string // component status for /health
	Tickets   *tickets.Service         // US1: tickets CRUD, assign, status, history, assignable users (+ US3 body, attachments)
	Comments  *comments.Service        // US3: conversation (notes, replies, delete)
	Mailboxes *mailboxes.Service       // US2: support mailboxes
	Rules     *rules.Service           // US4: triage rules + dry run
	Tags      *tags.Service            // US5: tag vocabulary (+ a ticket's tag set, with Tickets)
	Stats     *stats.Service           // US6: dashboard aggregates
	Backup    *backup.Service          // US6: tenant export/import
}

// Register mounts the handlers of every wired dependency.
func (s *Server) Register(d Deps) {
	s.MustHandle("GET", Prefix+"/health", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{"status": "ok"}
		if d.Health != nil {
			comps := d.Health()
			for _, v := range comps {
				if v != "ok" {
					out["status"] = "degraded"
				}
			}
			out["components"] = comps
		}
		WriteJSON(w, http.StatusOK, out)
	})
	if d.Hub != nil {
		s.RegisterStream(d.Hub)
	}
	if d.Tickets != nil {
		s.registerTickets(d.Tickets)
		s.registerMessage(d.Tickets)
	}
	if d.Comments != nil {
		s.registerComments(d.Comments)
	}
	if d.Mailboxes != nil {
		s.registerMailboxes(d.Mailboxes)
	}
	if d.Rules != nil {
		s.registerRules(d.Rules)
	}
	if d.Tags != nil {
		s.registerTags(d.Tags, d.Tickets)
	}
	if d.Stats != nil {
		s.registerStats(d.Stats)
	}
	if d.Backup != nil {
		s.registerBackup(d.Backup)
	}
}
