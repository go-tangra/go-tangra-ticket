// Package comments is the ticket conversation (US3, FR-008/FR-009): internal
// notes (agents only, never emailed) and public replies emailed to the
// requester through the relay with a single "Re:" subject, the reference line
// appended once and the threading headers (Message-ID, In-Reply-To = the
// ticket's last message id else its root, References = root + last) so the
// requester's answer threads back (research D4/D5).
//
// A reply is recorded before it is sent (so it is never silently lost) and its
// delivery state is then set to sent or failed; a failure is reported to the
// caller (ErrDeliveryFailed) with the recorded comment. A delivered reply
// re-opens a resolved/closed ticket. Replies are refused up front
// (ErrReplyUnavailable) without a requester address, a relay or a mailbox to
// send from, and (ErrUnsafeHeader) when any header would carry a line break.
// Audit and events never carry bodies or addresses.
package comments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/audit"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/blob"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/events"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/history"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailer"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/metrics"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/thread"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tickets"
)

// Errors (ticket lookups return tickets.ErrNotFound).
var (
	ErrCommentNotFound  = errors.New("comments: not found")
	ErrReplyUnavailable = errors.New("comments: reply unavailable")
	ErrDeliveryFailed   = errors.New("comments: reply recorded but not delivered")
	ErrUnsafeHeader     = errors.New("comments: reply refused: unsafe header value")
)

// ValidationError reports a bad input field.
type ValidationError struct{ Field, Msg string }

func (e ValidationError) Error() string { return "comments: " + e.Field + ": " + e.Msg }

// MaxBody bounds a comment body.
const MaxBody = 1 << 20

// Deps wire the service. Store is required; without Mailer public replies are
// refused; Tickets re-opens tickets (history/event/audit); Agents resolves
// author names; the rest are optional.
type Deps struct {
	Store      repo.Store
	Mailer     mailer.Sender
	Tickets    *tickets.Service
	Agents     agents.Directory
	Events     events.Publisher
	Audit      audit.Recorder
	Metrics    *metrics.Metrics
	Blobs      blob.Store
	Log        *slog.Logger
	MailDomain string // Message-ID domain fallback (config smtp.mail_domain)
}

// Service manages comments.
type Service struct{ d Deps }

// New builds the service.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d}
}

func tenantOf(subj authz.Subjects) (string, error) {
	if subj.TenantID == "" {
		return "", fmt.Errorf("%w: tenant required", authz.ErrForbidden)
	}
	return subj.TenantID, nil
}

func cleanBody(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ValidationError{"body", "required"}
	}
	if len(v) > MaxBody || !utf8.ValidString(v) {
		return "", ValidationError{"body", "too long or not UTF-8"}
	}
	for _, r := range v {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", ValidationError{"body", "control characters"}
		}
	}
	return v, nil
}

func (s *Service) ticket(ctx context.Context, tenantID, id string) (store.Ticket, error) {
	t, err := s.d.Store.GetTicket(ctx, tenantID, id)
	if errors.Is(err, repo.ErrNotFound) {
		return store.Ticket{}, tickets.ErrNotFound
	}
	return t, err
}

// author fills the author fields from the caller: signed-in agents by their
// directory name (falling back to the id), everything else as "system".
func (s *Service) author(ctx context.Context, subj authz.Subjects, c *store.Comment) {
	c.AuthorID = subj.ActorID()
	if !subj.IsHuman() {
		c.AuthorKind = store.AuthorSystem
		return
	}
	c.AuthorKind = store.AuthorAgent
	c.AuthorName = subj.UserID
	if s.d.Agents != nil {
		if u, err := s.d.Agents.Get(ctx, subj.TenantID, subj.UserID); err == nil {
			c.AuthorName = u.DisplayName()
		}
	}
}

func (s *Service) audit(ctx context.Context, subj authz.Subjects, t audit.EventType, id, outcome string, details map[string]any) {
	audit.Emit(ctx, s.d.Audit, audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind),
		ActorID: subj.ActorID(), SubjectKind: audit.SubjectComment, SubjectID: id, Outcome: outcome, Details: details})
}

func (s *Service) commented(ctx context.Context, subj authz.Subjects, tenantID, ticketID string) {
	if t, err := s.d.Store.GetTicket(ctx, tenantID, ticketID); err == nil {
		events.Emit(ctx, s.d.Events, tenantID, events.TicketCommented, t, history.ActorKind(subj))
	}
}

// List returns a ticket's comments, oldest first.
func (s *Service) List(ctx context.Context, subj authz.Subjects, ticketID string) ([]store.Comment, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return nil, err
	}
	if _, err := s.ticket(ctx, tenantID, ticketID); err != nil {
		return nil, err
	}
	out, err := s.d.Store.ListComments(ctx, tenantID, ticketID)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []store.Comment{}
	}
	return out, nil
}

// Add is the module-to-module entry point: an internal note, or a public
// reply emailed to the requester.
func (s *Service) Add(ctx context.Context, subj authz.Subjects, ticketID, body string, internal bool) (store.Comment, error) {
	if internal {
		return s.AddNote(ctx, subj, ticketID, body)
	}
	return s.Reply(ctx, subj, ticketID, body)
}

// AddNote records an internal note (never emailed).
func (s *Service) AddNote(ctx context.Context, subj authz.Subjects, ticketID, body string) (store.Comment, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Comment{}, err
	}
	body, err = cleanBody(body)
	if err != nil {
		return store.Comment{}, err
	}
	if _, err := s.ticket(ctx, tenantID, ticketID); err != nil {
		return store.Comment{}, err
	}
	c := store.Comment{ID: store.NewID(), TenantID: tenantID, TicketID: ticketID, Body: body, Internal: true, Delivery: store.DeliveryNone}
	s.author(ctx, subj, &c)
	if err := s.d.Store.CreateComment(ctx, c); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return store.Comment{}, tickets.ErrNotFound
		}
		return store.Comment{}, err
	}
	stored, err := s.d.Store.GetComment(ctx, tenantID, c.ID)
	if err != nil {
		stored = c
	}
	s.audit(ctx, subj, audit.CommentCreate, c.ID, audit.OutcomeOK, map[string]any{"ticket_id": ticketID, "internal": true})
	s.commented(ctx, subj, tenantID, ticketID)
	return stored, nil
}

// sender picks the mailbox a reply is sent from: the ticket's own mailbox,
// else the tenant's first active mailbox.
func (s *Service) sender(ctx context.Context, t store.Ticket) (store.Mailbox, error) {
	if t.MailboxID != "" {
		mb, err := s.d.Store.GetMailbox(ctx, t.TenantID, t.MailboxID)
		if err == nil {
			return mb, nil
		}
		if !errors.Is(err, repo.ErrNotFound) {
			return store.Mailbox{}, err
		}
	}
	list, err := s.d.Store.ListMailboxes(ctx, t.TenantID)
	if err != nil {
		return store.Mailbox{}, err
	}
	for _, mb := range list {
		if mb.Active {
			return mb, nil
		}
	}
	return store.Mailbox{}, fmt.Errorf("%w: no mailbox to send from", ErrReplyUnavailable)
}

// Reply records a public reply and emails it to the requester. On a relay
// failure the recorded comment (delivery=failed) is returned with
// ErrDeliveryFailed.
func (s *Service) Reply(ctx context.Context, subj authz.Subjects, ticketID, body string) (store.Comment, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Comment{}, err
	}
	body, err = cleanBody(body)
	if err != nil {
		return store.Comment{}, err
	}
	t, err := s.ticket(ctx, tenantID, ticketID)
	if err != nil {
		return store.Comment{}, err
	}
	if t.RequesterEmail == "" {
		return store.Comment{}, fmt.Errorf("%w: the ticket has no requester address", ErrReplyUnavailable)
	}
	if s.d.Mailer == nil || !s.d.Mailer.Enabled() {
		return store.Comment{}, fmt.Errorf("%w: outbound mail is not configured", ErrReplyUnavailable)
	}
	mb, err := s.sender(ctx, t)
	if err != nil {
		return store.Comment{}, err
	}
	domain := thread.DomainOf(mb.Address)
	if domain == "" {
		domain = s.d.MailDomain
	}
	out := mailer.OutMail{From: mb.Address, FromName: mb.DisplayName, To: t.RequesterEmail, ToName: t.RequesterName,
		Subject: thread.ReplySubject(t.Subject), Text: thread.AppendReference(body, t.ID), MessageID: thread.MessageID(t.ID, domain)}
	if t.ExternalID != "" {
		out.InReplyTo = t.ExternalID
		out.References = []string{t.ExternalID}
	}
	if t.LastMessageID != "" {
		out.InReplyTo = t.LastMessageID
		if t.LastMessageID != t.ExternalID {
			out.References = append(out.References, t.LastMessageID)
		}
	}
	if err := mailer.Validate(out); err != nil {
		s.audit(ctx, subj, audit.ReplyFailed, "", audit.OutcomeRefused, map[string]any{"ticket_id": t.ID, "reason": "unsafe_header"})
		return store.Comment{}, ErrUnsafeHeader
	}

	c := store.Comment{ID: store.NewID(), TenantID: tenantID, TicketID: t.ID, Body: body, MessageID: out.MessageID, Delivery: store.DeliveryNone}
	s.author(ctx, subj, &c)
	if err := s.d.Store.CreateComment(ctx, c); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return store.Comment{}, tickets.ErrNotFound
		}
		return store.Comment{}, err
	}
	sendErr := s.d.Mailer.Send(ctx, out)
	c.Delivery = store.DeliverySent
	if sendErr != nil {
		c.Delivery = store.DeliveryFailed
	}
	if err := s.d.Store.SetCommentDelivery(ctx, tenantID, c.ID, c.Delivery); err != nil {
		s.d.Log.WarnContext(ctx, "reply delivery state not recorded", "comment_id", c.ID, "err", err)
	}
	if stored, err := s.d.Store.GetComment(ctx, tenantID, c.ID); err == nil {
		c = stored
	}
	if sendErr != nil {
		s.d.Metrics.Reply(metrics.ResultFailed)
		s.d.Log.WarnContext(ctx, "reply not delivered", "ticket_id", t.ID, "comment_id", c.ID, "err", sendErr)
		s.audit(ctx, subj, audit.ReplyFailed, c.ID, audit.OutcomeError, map[string]any{"ticket_id": t.ID})
		s.commented(ctx, subj, tenantID, t.ID)
		return c, ErrDeliveryFailed
	}
	s.d.Metrics.Reply(metrics.ResultSent)
	reopened := false
	if (t.Status == store.StatusResolved || t.Status == store.StatusClosed) && s.d.Tickets != nil {
		if _, err := s.d.Tickets.SetStatusAs(ctx, subj, t.ID, store.StatusOpen, history.ActorKind(subj)); err != nil {
			s.d.Log.WarnContext(ctx, "reply: re-open failed", "ticket_id", t.ID, "err", err)
		} else {
			reopened = true
		}
	}
	s.audit(ctx, subj, audit.ReplySent, c.ID, audit.OutcomeOK, map[string]any{"ticket_id": t.ID, "mailbox_id": mb.ID, "reopened": reopened})
	s.commented(ctx, subj, tenantID, t.ID)
	return c, nil
}

// Delete removes a comment (and its attachments with their objects).
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) error {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return err
	}
	c, err := s.d.Store.GetComment(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrCommentNotFound
		}
		return err
	}
	keys, err := s.d.Store.DeleteComment(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrCommentNotFound
		}
		return err
	}
	failed := 0
	if s.d.Blobs != nil {
		for _, k := range keys {
			if err := s.d.Blobs.Delete(ctx, k); err != nil {
				failed++
				s.d.Log.WarnContext(ctx, "comment delete: attachment object not removed", "comment_id", id, "err", err)
			}
		}
	}
	s.audit(ctx, subj, audit.CommentDelete, id, audit.OutcomeOK, map[string]any{"ticket_id": c.TicketID, "attachments": len(keys), "objects_failed": failed})
	return nil
}
