// Package tickets is the ticket lifecycle service (US1): create, read, list
// with filters and paging, partial update, delete (rows and attachment
// objects), assignment validated against the agent directory (research D9) and
// status changes (resolved_at maintained by the store). Every status, priority
// and assignee change writes a history row; create/assign/status publish a
// content-safe event (contracts §D); every mutation is audited without bodies
// or requester PII (research D11).
//
// Route-level permissions are enforced by the callers (httpapi per route,
// grpcapi per method); the service confines every operation to the caller's
// tenant and the store enforces per-tenant RLS. Store sentinels are masked into
// this package's errors.
package tickets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/audit"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/blob"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/events"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/history"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/metrics"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/sanitize"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// Errors.
var (
	ErrNotFound             = errors.New("tickets: not found")
	ErrInvalidStatus        = errors.New("tickets: invalid status")
	ErrInvalidAssignee      = errors.New("tickets: invalid assignee")
	ErrDirectoryUnavailable = errors.New("tickets: user directory unavailable")
	ErrAttachmentNotFound   = errors.New("tickets: attachment not found")
)

// ValidationError reports a bad input field.
type ValidationError struct{ Field, Msg string }

func (e ValidationError) Error() string { return "tickets: " + e.Field + ": " + e.Msg }

// Limits (data-model.md validation rules).
const (
	MaxSubject     = 998 // RFC 5322 line limit
	MaxDescription = 1 << 20
	MaxName        = 200
	MaxEmail       = 320
	MaxPageSize    = 100
)

// Deps wire the service. Only Store is required; the others degrade to no-ops
// (Agents: assignment answers ErrDirectoryUnavailable).
type Deps struct {
	Store   repo.Store
	Agents  agents.Directory
	Events  events.Publisher
	Audit   audit.Recorder
	Metrics *metrics.Metrics
	Blobs   blob.Store
	Log     *slog.Logger
}

// Service manages tickets.
type Service struct {
	d    Deps
	hist *history.Log
	now  func() time.Time
}

// New builds the service.
func New(d Deps) *Service {
	s := &Service{d: d, hist: history.New(d.Store), now: func() time.Time { return time.Now().UTC() }}
	return s
}

// SetClock injects the clock (tests).
func (s *Service) SetClock(now func() time.Time) {
	s.now = now
	s.hist.SetClock(now)
}

// View is the API projection of a ticket: never the raw HTML body (only
// has_html), always a tag list, attachments on single reads.
type View struct {
	store.Ticket
	HasHTML     bool               `json:"has_html"`
	Tags        []store.Tag        `json:"tags"`
	Attachments []store.Attachment `json:"attachments,omitempty"`
}

// CreateInput is the manual create body.
type CreateInput struct {
	Subject        string `json:"subject"`
	Description    string `json:"description"`
	Priority       string `json:"priority"`
	RequesterEmail string `json:"requester_email"`
	RequesterName  string `json:"requester_name"`
	AssigneeID     string `json:"assignee_id"`
}

// UpdateInput is the partial update body; nil leaves a field unchanged.
type UpdateInput struct {
	Subject     *string `json:"subject"`
	Description *string `json:"description"`
	Priority    *string `json:"priority"`
}

func mapErr(err error) error {
	if errors.Is(err, repo.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func tenantOf(subj authz.Subjects) (string, error) {
	if subj.TenantID == "" {
		return "", fmt.Errorf("%w: tenant required", authz.ErrForbidden)
	}
	return subj.TenantID, nil
}

// cleanSubject trims and flattens line breaks (the subject later becomes a
// mail header) and enforces the length bound.
func cleanSubject(v string) (string, error) {
	v = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(v))
	if v == "" {
		return "", ValidationError{"subject", "required"}
	}
	if utf8.RuneCountInString(v) > MaxSubject || !utf8.ValidString(v) {
		return "", ValidationError{"subject", "too long or not UTF-8"}
	}
	if hasControl(v, false) {
		return "", ValidationError{"subject", "control characters"}
	}
	return v, nil
}

func hasControl(v string, allowNewlines bool) bool {
	for _, r := range v {
		if allowNewlines && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func cleanDescription(v string) (string, error) {
	if len(v) > MaxDescription || !utf8.ValidString(v) || hasControl(v, true) {
		return "", ValidationError{"description", "too long or invalid characters"}
	}
	return v, nil
}

func cleanRequester(email, name string) (string, string, error) {
	email, name = strings.TrimSpace(email), strings.TrimSpace(name)
	if email != "" {
		if len(email) > MaxEmail || hasControl(email, false) {
			return "", "", ValidationError{"requester_email", "invalid address"}
		}
		a, err := mail.ParseAddress(email)
		if err != nil || a.Address != email {
			return "", "", ValidationError{"requester_email", "invalid address"}
		}
	}
	if utf8.RuneCountInString(name) > MaxName || hasControl(name, false) || !utf8.ValidString(name) {
		return "", "", ValidationError{"requester_name", "too long or invalid characters"}
	}
	return email, name, nil
}

// resolveAssignee validates an assignee through the directory.
func (s *Service) resolveAssignee(ctx context.Context, tenantID, userID string) (agents.User, error) {
	if s.d.Agents == nil {
		return agents.User{}, ErrDirectoryUnavailable
	}
	u, err := s.d.Agents.Get(ctx, tenantID, userID)
	switch {
	case err == nil:
		return u, nil
	case errors.Is(err, agents.ErrUnknownUser), errors.Is(err, agents.ErrNotAssignable):
		return agents.User{}, ErrInvalidAssignee
	}
	return agents.User{}, ErrDirectoryUnavailable
}

func (s *Service) audit(ctx context.Context, subj authz.Subjects, t audit.EventType, id, outcome string, details map[string]any) {
	audit.Emit(ctx, s.d.Audit, audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectTicket, SubjectID: id, Outcome: outcome, Details: details})
}

// view decorates t with its tags (and attachments when full).
func (s *Service) view(ctx context.Context, t store.Ticket, full bool) (View, error) {
	v := View{Ticket: t, HasHTML: t.HasHTML(), Tags: []store.Tag{}}
	tags, err := s.d.Store.TagsForTickets(ctx, t.TenantID, []string{t.ID})
	if err != nil {
		return View{}, err
	}
	if tt := tags[t.ID]; tt != nil {
		v.Tags = tt
	}
	if full {
		atts, err := s.d.Store.ListAttachments(ctx, t.TenantID, t.ID)
		if err != nil {
			return View{}, err
		}
		v.Attachments = atts
	}
	return v, nil
}

// Create opens a manual ticket (open/normal/manual unless given).
func (s *Service) Create(ctx context.Context, subj authz.Subjects, in CreateInput) (View, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return View{}, err
	}
	subject, err := cleanSubject(in.Subject)
	if err != nil {
		return View{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return View{}, err
	}
	email, name, err := cleanRequester(in.RequesterEmail, in.RequesterName)
	if err != nil {
		return View{}, err
	}
	prio := in.Priority
	if prio == "" {
		prio = store.PriorityNormal
	}
	if !store.ValidPriority(prio) {
		return View{}, ValidationError{"priority", "unknown priority"}
	}
	now := s.now()
	t := store.Ticket{ID: store.NewID(), TenantID: tenantID, Subject: subject, Description: desc, Status: store.StatusOpen,
		Priority: prio, Source: store.SourceManual, RequesterEmail: email, RequesterName: name,
		CreatedBy: subj.ActorID(), CreatedAt: now, UpdatedAt: now}
	if in.AssigneeID != "" {
		u, err := s.resolveAssignee(ctx, tenantID, in.AssigneeID)
		if err != nil {
			return View{}, err
		}
		t.AssigneeID, t.AssigneeName = u.ID, u.DisplayName()
	}
	if err := s.d.Store.CreateTicket(ctx, t); err != nil {
		return View{}, err
	}
	stored, err := s.d.Store.GetTicket(ctx, tenantID, t.ID)
	if err != nil {
		return View{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.TicketCreate, t.ID, audit.OutcomeOK, map[string]any{
		"status": stored.Status, "priority": stored.Priority, "source": stored.Source, "assigned": stored.AssigneeID != ""})
	events.Emit(ctx, s.d.Events, tenantID, events.TicketCreated, stored, history.ActorKind(subj))
	return s.view(ctx, stored, false)
}

// Get returns one ticket of the caller's tenant with tags and attachments.
func (s *Service) Get(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return View{}, err
	}
	t, err := s.d.Store.GetTicket(ctx, tenantID, id)
	if err != nil {
		return View{}, mapErr(err)
	}
	return s.view(ctx, t, true)
}

// List returns one page of the caller's tickets (newest first) and the total.
func (s *Service) List(ctx context.Context, subj authz.Subjects, f store.TicketFilter) ([]View, int64, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return nil, 0, err
	}
	if f.Status != "" && !store.ValidStatus(f.Status) {
		return nil, 0, ErrInvalidStatus
	}
	if f.Priority != "" && !store.ValidPriority(f.Priority) {
		return nil, 0, ValidationError{"priority", "unknown priority"}
	}
	f.Query = strings.TrimSpace(f.Query)
	f = f.Normalized(MaxPageSize)
	items, total, err := s.d.Store.ListTickets(ctx, tenantID, f)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, len(items))
	for i, t := range items {
		ids[i] = t.ID
	}
	tags := map[string][]store.Tag{}
	if len(ids) > 0 {
		if tags, err = s.d.Store.TagsForTickets(ctx, tenantID, ids); err != nil {
			return nil, 0, err
		}
	}
	out := make([]View, len(items))
	for i, t := range items {
		v := View{Ticket: t, HasHTML: t.HasHTML(), Tags: tags[t.ID]}
		if v.Tags == nil {
			v.Tags = []store.Tag{}
		}
		out[i] = v
	}
	return out, total, nil
}

// Update applies a partial update of subject/description/priority; a priority
// change is recorded in the history.
func (s *Service) Update(ctx context.Context, subj authz.Subjects, id string, in UpdateInput) (View, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return View{}, err
	}
	var p store.TicketPatch
	changed := []any{}
	if in.Subject != nil {
		v, err := cleanSubject(*in.Subject)
		if err != nil {
			return View{}, err
		}
		p.Subject = &v
		changed = append(changed, "subject")
	}
	if in.Description != nil {
		v, err := cleanDescription(*in.Description)
		if err != nil {
			return View{}, err
		}
		p.Description = &v
		changed = append(changed, "description")
	}
	if in.Priority != nil {
		if !store.ValidPriority(*in.Priority) {
			return View{}, ValidationError{"priority", "unknown priority"}
		}
		p.Priority = in.Priority
		changed = append(changed, "priority")
	}
	before, err := s.d.Store.GetTicket(ctx, tenantID, id)
	if err != nil {
		return View{}, mapErr(err)
	}
	after, err := s.d.Store.UpdateTicket(ctx, tenantID, id, p, s.now())
	if err != nil {
		return View{}, mapErr(err)
	}
	if err := s.hist.Append(ctx, tenantID, id, store.FieldPriority, before.Priority, after.Priority, history.ActorKind(subj), subj.ActorID()); err != nil {
		return View{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.TicketUpdate, id, audit.OutcomeOK, map[string]any{"fields": changed, "priority": after.Priority})
	return s.view(ctx, after, true)
}

// Delete removes the ticket with its comments, links, history and attachment
// rows, then (best effort) the attachment objects.
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) error {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return err
	}
	keys, err := s.d.Store.DeleteTicket(ctx, tenantID, id)
	if err != nil {
		return mapErr(err)
	}
	failed := 0
	if s.d.Blobs != nil {
		for _, k := range keys {
			if err := s.d.Blobs.Delete(ctx, k); err != nil {
				failed++
				if s.d.Log != nil {
					s.d.Log.WarnContext(ctx, "ticket delete: attachment object not removed", "ticket_id", id, "err", err)
				}
			}
		}
	}
	s.audit(ctx, subj, audit.TicketDelete, id, audit.OutcomeOK, map[string]any{"attachments": len(keys), "objects_failed": failed})
	return nil
}

// Assign sets (or with "" clears) the assignee; the assignee must be an agent
// of the tenant (holding tickets:manage).
func (s *Service) Assign(ctx context.Context, subj authz.Subjects, id, assigneeID string) (View, error) {
	return s.AssignAs(ctx, subj, id, assigneeID, history.ActorKind(subj))
}

// AssignAs is Assign recording actorKind in the history (rules record
// store.ActorRule).
func (s *Service) AssignAs(ctx context.Context, subj authz.Subjects, id, assigneeID, actorKind string) (View, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return View{}, err
	}
	before, err := s.d.Store.GetTicket(ctx, tenantID, id)
	if err != nil {
		return View{}, mapErr(err)
	}
	var u agents.User
	if assigneeID != "" {
		if u, err = s.resolveAssignee(ctx, tenantID, assigneeID); err != nil {
			s.audit(ctx, subj, audit.TicketAssign, id, audit.OutcomeRefused, map[string]any{"reason": reasonOf(err)})
			return View{}, err
		}
	}
	if before.AssigneeID == assigneeID && (assigneeID == "" || before.AssigneeName == u.DisplayName()) {
		return s.view(ctx, before, true)
	}
	name := ""
	if assigneeID != "" {
		name = u.DisplayName()
	}
	after, err := s.d.Store.SetAssignee(ctx, tenantID, id, assigneeID, name, s.now())
	if err != nil {
		return View{}, mapErr(err)
	}
	if err := s.hist.Append(ctx, tenantID, id, store.FieldAssignee, before.AssigneeID, after.AssigneeID, actorKind, subj.ActorID()); err != nil {
		return View{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.TicketAssign, id, audit.OutcomeOK, map[string]any{"assignee_id": after.AssigneeID, "previous_assignee_id": before.AssigneeID})
	events.Emit(ctx, s.d.Events, tenantID, events.TicketAssigned, after, actorKind)
	return s.view(ctx, after, true)
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, ErrInvalidAssignee):
		return "invalid_assignee"
	case errors.Is(err, ErrDirectoryUnavailable):
		return "directory_unavailable"
	}
	return "error"
}

// SetStatus moves the ticket to status (any lifecycle value; "unspecified" or
// unknown values are refused with ErrInvalidStatus).
func (s *Service) SetStatus(ctx context.Context, subj authz.Subjects, id, status string) (View, error) {
	return s.SetStatusAs(ctx, subj, id, status, history.ActorKind(subj))
}

// SetStatusAs is SetStatus recording actorKind in the history (rules record
// store.ActorRule, inbound re-opens store.ActorInbound).
func (s *Service) SetStatusAs(ctx context.Context, subj authz.Subjects, id, status, actorKind string) (View, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return View{}, err
	}
	if !store.ValidStatus(status) {
		return View{}, ErrInvalidStatus
	}
	before, err := s.d.Store.GetTicket(ctx, tenantID, id)
	if err != nil {
		return View{}, mapErr(err)
	}
	if before.Status == status {
		return s.view(ctx, before, true)
	}
	after, err := s.d.Store.SetStatus(ctx, tenantID, id, status, s.now())
	if err != nil {
		return View{}, mapErr(err)
	}
	if err := s.hist.Append(ctx, tenantID, id, store.FieldStatus, before.Status, after.Status, actorKind, subj.ActorID()); err != nil {
		return View{}, mapErr(err)
	}
	s.d.Metrics.Transition(before.Status, after.Status)
	s.audit(ctx, subj, audit.TicketStatus, id, audit.OutcomeOK, map[string]any{"from": before.Status, "to": after.Status})
	events.Emit(ctx, s.d.Events, tenantID, events.TicketStatusChanged, after, actorKind)
	return s.view(ctx, after, true)
}

// SetPriorityAs sets the priority recording actorKind in the history (rules
// record store.ActorRule); an unchanged priority is a no-op.
func (s *Service) SetPriorityAs(ctx context.Context, subj authz.Subjects, id, priority, actorKind string) (View, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return View{}, err
	}
	if !store.ValidPriority(priority) {
		return View{}, ValidationError{"priority", "unknown priority"}
	}
	before, err := s.d.Store.GetTicket(ctx, tenantID, id)
	if err != nil {
		return View{}, mapErr(err)
	}
	if before.Priority == priority {
		return s.view(ctx, before, true)
	}
	after, err := s.d.Store.UpdateTicket(ctx, tenantID, id, store.TicketPatch{Priority: &priority}, s.now())
	if err != nil {
		return View{}, mapErr(err)
	}
	if err := s.hist.Append(ctx, tenantID, id, store.FieldPriority, before.Priority, after.Priority, actorKind, subj.ActorID()); err != nil {
		return View{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.TicketUpdate, id, audit.OutcomeOK, map[string]any{"fields": []any{"priority"}, "priority": after.Priority})
	return s.view(ctx, after, true)
}

// History returns the ticket's change history, oldest first.
func (s *Service) History(ctx context.Context, subj authz.Subjects, id string) ([]store.History, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return nil, err
	}
	if _, err := s.d.Store.GetTicket(ctx, tenantID, id); err != nil {
		return nil, mapErr(err)
	}
	return s.hist.List(ctx, tenantID, id)
}

// AssignableUsers lists the tenant's agents (users holding tickets:manage).
func (s *Service) AssignableUsers(ctx context.Context, subj authz.Subjects) ([]agents.User, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return nil, err
	}
	if s.d.Agents == nil {
		return nil, ErrDirectoryUnavailable
	}
	users, err := s.d.Agents.Assignable(ctx, tenantID)
	if err != nil {
		return nil, ErrDirectoryUnavailable
	}
	if users == nil {
		users = []agents.User{}
	}
	return users, nil
}

// Body returns the ticket's message for display: the HTML sanitised (inline
// images bound to the ticket's own attachments, research D7) and the plain
// text. The raw HTML never leaves the service.
func (s *Service) Body(ctx context.Context, subj authz.Subjects, id string) (sanitize.Body, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return sanitize.Body{}, err
	}
	t, err := s.d.Store.GetTicket(ctx, tenantID, id)
	if err != nil {
		return sanitize.Body{}, mapErr(err)
	}
	atts, err := s.d.Store.ListAttachments(ctx, tenantID, id)
	if err != nil {
		return sanitize.Body{}, err
	}
	return sanitize.Message(t, atts), nil
}

// OpenAttachment returns an attachment of the ticket with a reader over its
// bytes, only when both are found in the caller's tenant (RLS; research D8).
// The caller closes the reader.
func (s *Service) OpenAttachment(ctx context.Context, subj authz.Subjects, ticketID, attID string) (store.Attachment, io.ReadCloser, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Attachment{}, nil, err
	}
	if _, err := s.d.Store.GetTicket(ctx, tenantID, ticketID); err != nil {
		return store.Attachment{}, nil, mapErr(err)
	}
	a, err := s.d.Store.GetAttachment(ctx, tenantID, ticketID, attID)
	if errors.Is(err, repo.ErrNotFound) {
		return store.Attachment{}, nil, ErrAttachmentNotFound
	}
	if err != nil {
		return store.Attachment{}, nil, err
	}
	if s.d.Blobs == nil {
		return store.Attachment{}, nil, errors.New("tickets: object store not configured")
	}
	rc, err := s.d.Blobs.Get(ctx, a.StorageKey)
	if err != nil {
		return store.Attachment{}, nil, fmt.Errorf("tickets: attachment object: %w", err)
	}
	return a, rc, nil
}
