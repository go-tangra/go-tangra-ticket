package inbound

// The mail handler of the edge (research D2–D4, D8; contracts §B):
//
//  1. the relay token (Authorization: Bearer or X-Ticket-Token — never a query
//     parameter) is compared in constant time against the warden-held value;
//  2. the raw message is read within the body cap and parsed (mailparse);
//  3. the recipient (X-Iris-Recipient | X-Ticket-Recipient, else the message's
//     Delivered-To/To addresses) is routed to a mailbox → tenant through the
//     narrow system lookup; every later step runs as authz.SystemFor(tenant);
//  4. a message id already recorded (comment or ticket root) is a duplicate;
//  5. a reply to a ticket of THAT tenant (thread.Resolve) becomes a public
//     requester comment with its attachments and re-opens a resolved/closed
//     ticket; rules never run on replies;
//  6. otherwise the Triage hook (US4 rules) may drop the message, a new email
//     ticket is created with its attachments, and the hook's actions applied;
//  7. the new ticket is acknowledged to the requester when the mailbox asks for
//     it and the mail is not automated (ack.go, US6).
//
// Refusals (token, recipient, size) all carry the same generic body; storage
// failures answer 503 so the relay retries — objects are uploaded before rows
// are written and removed again when the rows cannot be, so a retry never
// meets half-stored state. Logs, events and audit never carry addresses,
// subjects or bodies.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/blob"
	"github.com/go-freya/freya/services/ticket/internal/events"
	"github.com/go-freya/freya/services/ticket/internal/history"
	"github.com/go-freya/freya/services/ticket/internal/mailer"
	"github.com/go-freya/freya/services/ticket/internal/mailparse"
	"github.com/go-freya/freya/services/ticket/internal/metrics"
	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/secrets"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/thread"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

// Relay request headers.
const (
	HeaderToken         = "X-Ticket-Token"
	HeaderIrisRecipient = "X-Iris-Recipient"
	HeaderRecipient     = "X-Ticket-Recipient"
	HeaderIrisMessageID = "X-Iris-Message-Id"
)

// Outcomes of an accepted delivery (202).
const (
	OutcomeCreated   = "created"
	OutcomeThreaded  = "threaded"
	OutcomeDuplicate = "duplicate"
	OutcomeDropped   = "dropped"
)

// maxRecipientLookups bounds the fallback routing over the message's addresses.
const maxRecipientLookups = 10

// TriageInput is what the Triage hook sees of a message that would open a
// new ticket.
type TriageInput struct {
	Message   *mailparse.Message
	Recipient string // the routed mailbox address
	MailboxID string
}

// Plan is the Triage decision: Drop discards the message (no ticket); Apply
// (optional) runs the matched actions on the created ticket.
type Plan struct {
	Drop  bool
	Apply func(ctx context.Context, ticketID string) error
}

// Triage is the rules hook (US4). It runs only for messages that would open a
// new ticket — never for replies — under the routed tenant's inbound subject.
// An Evaluate error is logged and ingestion continues without rules.
type Triage interface {
	Evaluate(ctx context.Context, subj authz.Subjects, in TriageInput) (Plan, error)
}

// Deps wire the handler. Store, Blobs and Secrets are required; Tickets is
// used to re-open threaded tickets (history, event, audit); the rest are
// optional.
type Deps struct {
	Store   repo.Store
	Blobs   blob.Store
	Secrets secrets.Source
	Tickets *tickets.Service
	Audit   audit.Recorder
	Events  events.Publisher
	Metrics *metrics.Metrics
	Log     *slog.Logger
	Limits  mailparse.Limits
	Triage  Triage
	// Mailer sends acknowledgements (US6); nil or disabled = none are sent.
	Mailer mailer.Sender
	// MailDomain is the Message-ID domain fallback (config smtp.mail_domain).
	MailDomain string
}

// Handler serves POST /inbound/mail.
type Handler struct {
	d Deps
}

// NewHandler builds the mail handler.
func NewHandler(d Deps) *Handler {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Limits.MaxTextBytes <= 0 || d.Limits.MaxTextBytes > tickets.MaxDescription {
		d.Limits.MaxTextBytes = tickets.MaxDescription
	}
	return &Handler{d: d}
}

// errRetry marks a failure the relay should retry (503).
type errRetry struct{ err error }

func (e errRetry) Error() string { return "inbound: " + e.err.Error() }
func (e errRetry) Unwrap() error { return e.err }

func retry(err error) error { return errRetry{err} }

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if code, reason := h.authenticate(ctx, r); code != 0 {
		h.refuse(ctx, w, code, reason)
		return
	}
	limit := h.d.Limits.MaxBodyBytes
	if limit <= 0 {
		limit = mailparse.DefaultLimits().MaxBodyBytes
	}
	if r.ContentLength > limit {
		h.refuse(ctx, w, http.StatusRequestEntityTooLarge, "size")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		var mbe *http.MaxBytesError
		if err == nil || errors.As(err, &mbe) {
			h.refuse(ctx, w, http.StatusRequestEntityTooLarge, "size")
			return
		}
		h.refuse(ctx, w, http.StatusBadRequest, "read")
		return
	}
	msg, err := mailparse.Parse(raw, h.d.Limits)
	if err != nil {
		h.refuse(ctx, w, http.StatusRequestEntityTooLarge, "size")
		return
	}
	route, recipient, err := h.route(ctx, r, &msg)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			h.refuse(ctx, w, http.StatusNotFound, "mailbox")
			return
		}
		h.fail(ctx, w, "", err)
		return
	}
	if ids := mailparse.ParseMessageIDs(r.Header.Get(HeaderIrisMessageID)); len(ids) > 0 {
		msg.MessageID = ids[0]
	}
	subj := authz.SystemFor(route.TenantID)
	outcome, ticketID, err := h.ingest(ctx, subj, route, recipient, &msg)
	if err != nil {
		h.fail(ctx, w, route.TenantID, err)
		return
	}
	h.d.Metrics.Inbound(outcome)
	h.d.Log.InfoContext(ctx, "inbound mail", "outcome", outcome, "tenant_id", route.TenantID, "ticket_id", ticketID)
	body := map[string]string{"outcome": outcome}
	if ticketID != "" {
		body["ticket_id"] = ticketID
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(body)
}

// authenticate checks the relay token; 0 when accepted, else the status
// (401 refused, 503 when the expected value cannot be read) and an audit reason.
func (h *Handler) authenticate(ctx context.Context, r *http.Request) (int, string) {
	got := r.Header.Get(HeaderToken)
	if got == "" {
		if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "Bearer ") {
			got = strings.TrimSpace(a[7:])
		}
	}
	if h.d.Secrets == nil {
		return http.StatusServiceUnavailable, "token_unavailable"
	}
	want, err := h.d.Secrets.RelayToken(ctx)
	if err != nil || want == "" {
		return http.StatusServiceUnavailable, "token_unavailable"
	}
	// Hash both sides so the comparison is constant-time in the length too.
	gs, ws := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	if got == "" || subtle.ConstantTimeCompare(gs[:], ws[:]) != 1 {
		return http.StatusUnauthorized, "token"
	}
	return 0, ""
}

// route resolves the recipient mailbox. A relay-supplied recipient is
// authoritative; without one the message's Delivered-To, then To addresses
// are tried. repo.ErrNotFound for no active mailbox.
func (h *Handler) route(ctx context.Context, r *http.Request, msg *mailparse.Message) (store.MailboxRoute, string, error) {
	var candidates []string
	if v := r.Header.Get(HeaderIrisRecipient); v != "" {
		candidates = []string{v}
	} else if v := r.Header.Get(HeaderRecipient); v != "" {
		candidates = []string{v}
	} else {
		candidates = append(append(candidates, msg.DeliveredTo...), msg.To...)
	}
	for i, c := range candidates {
		if i == maxRecipientLookups {
			break
		}
		addr := mailparse.NormalizeRecipient(c)
		if addr == "" {
			continue
		}
		rt, err := h.d.Store.RouteMailbox(ctx, addr)
		if errors.Is(err, repo.ErrNotFound) {
			continue
		}
		if err != nil {
			return store.MailboxRoute{}, "", retry(err)
		}
		if rt.Active {
			return rt, addr, nil
		}
	}
	return store.MailboxRoute{}, "", repo.ErrNotFound
}

// ingest runs dedup → threading → triage → creation for a routed message.
func (h *Handler) ingest(ctx context.Context, subj authz.Subjects, route store.MailboxRoute, recipient string, msg *mailparse.Message) (string, string, error) {
	tenantID := route.TenantID
	if id, dup, err := h.duplicate(ctx, tenantID, msg.MessageID); err != nil {
		return "", "", err
	} else if dup {
		h.audit(ctx, subj, audit.InboundDuplicate, audit.SubjectTicket, id, nil)
		return OutcomeDuplicate, id, nil
	}
	match, err := thread.Resolve(ctx, h.d.Store, tenantID, thread.Input{MessageIDs: msg.ThreadIDs(), Text: msg.Text, Subject: msg.Subject})
	if err != nil {
		return "", "", retry(err)
	}
	if match.TicketID != "" {
		outcome, id, err := h.reply(ctx, subj, match, msg)
		if !errors.Is(err, errTicketGone) {
			return outcome, id, err
		}
		// the ticket was deleted meanwhile: the reply opens a new ticket
	}
	var plan Plan
	if h.d.Triage != nil {
		p, err := h.d.Triage.Evaluate(ctx, subj, TriageInput{Message: msg, Recipient: recipient, MailboxID: route.MailboxID})
		if err != nil {
			h.d.Log.WarnContext(ctx, "inbound mail: rules skipped", "tenant_id", tenantID, "err", err)
		} else {
			plan = p
		}
	}
	if plan.Drop {
		h.audit(ctx, subj, audit.InboundDropped, audit.SubjectMessage, "", map[string]any{"spam_score": msg.SpamScore})
		return OutcomeDropped, "", nil
	}
	return h.create(ctx, subj, route, recipient, msg, plan)
}

// duplicate reports whether msgID is already recorded in the tenant.
func (h *Handler) duplicate(ctx context.Context, tenantID, msgID string) (string, bool, error) {
	if msgID == "" {
		return "", false, nil
	}
	c, err := h.d.Store.FindCommentByMessageID(ctx, tenantID, msgID)
	if err == nil {
		return c.TicketID, true, nil
	}
	if !errors.Is(err, repo.ErrNotFound) {
		return "", false, retry(err)
	}
	t, err := h.d.Store.FindTicketByExternalID(ctx, tenantID, msgID)
	if err == nil {
		return t.ID, true, nil
	}
	if !errors.Is(err, repo.ErrNotFound) {
		return "", false, retry(err)
	}
	return "", false, nil
}

var errTicketGone = errors.New("inbound: ticket gone")

// staged is an attachment whose object is uploaded but whose row is not yet
// written.
type staged struct {
	att store.Attachment
}

// upload stores every attachment object of msg under the ticket; on failure
// the objects already stored are removed and a retryable error returned.
func (h *Handler) upload(ctx context.Context, tenantID, ticketID string, msg *mailparse.Message) ([]staged, error) {
	out := make([]staged, 0, len(msg.Attachments))
	for _, a := range msg.Attachments {
		id := store.NewID()
		key := store.AttachmentKey(tenantID, ticketID, id)
		sum, err := h.d.Blobs.Put(ctx, key, bytes.NewReader(a.Data), int64(len(a.Data)), a.ContentType)
		if err != nil {
			h.discard(ctx, out)
			return nil, retry(fmt.Errorf("attachment upload: %w", err))
		}
		out = append(out, staged{att: store.Attachment{ID: id, TenantID: tenantID, TicketID: ticketID, Filename: a.Filename,
			ContentType: a.ContentType, Size: int64(len(a.Data)), ContentID: a.ContentID, Inline: a.Inline, StorageKey: key, Checksum: sum}})
	}
	return out, nil
}

// discard removes staged objects (best effort).
func (h *Handler) discard(ctx context.Context, st []staged) {
	for _, s := range st {
		if err := h.d.Blobs.Delete(ctx, s.att.StorageKey); err != nil {
			h.d.Log.WarnContext(ctx, "inbound mail: orphan attachment object", "ticket_id", s.att.TicketID, "err", err)
		}
	}
}

// record writes the attachment rows (the ticket/comment exists now); a row
// that cannot be written takes its object with it.
func (h *Handler) record(ctx context.Context, st []staged, commentID string) int {
	n := 0
	for _, s := range st {
		a := s.att
		a.CommentID = commentID
		if err := h.d.Store.CreateAttachment(ctx, a); err != nil {
			h.d.Log.WarnContext(ctx, "inbound mail: attachment not recorded", "ticket_id", a.TicketID, "err", err)
			h.discard(ctx, []staged{s})
			continue
		}
		n++
	}
	return n
}

// reply appends a threaded requester reply.
func (h *Handler) reply(ctx context.Context, subj authz.Subjects, match thread.Match, msg *mailparse.Message) (string, string, error) {
	tenantID := subj.TenantID
	st, err := h.upload(ctx, tenantID, match.TicketID, msg)
	if err != nil {
		return "", "", err
	}
	body := msg.Text
	if body == "" {
		body = "(empty message)"
	}
	c := store.Comment{ID: store.NewID(), TenantID: tenantID, TicketID: match.TicketID, Body: body, AuthorKind: store.AuthorRequester,
		AuthorName: msg.FromName, AuthorEmail: msg.FromEmail, MessageID: msg.MessageID, Delivery: store.DeliveryNone}
	if err := h.d.Store.CreateComment(ctx, c); err != nil {
		h.discard(ctx, st)
		switch {
		case errors.Is(err, repo.ErrConflict): // concurrent duplicate delivery
			h.audit(ctx, subj, audit.InboundDuplicate, audit.SubjectTicket, match.TicketID, nil)
			return OutcomeDuplicate, match.TicketID, nil
		case errors.Is(err, repo.ErrNotFound):
			return "", "", errTicketGone
		}
		return "", "", retry(err)
	}
	stored := h.record(ctx, st, c.ID)
	h.note(ctx, tenantID, match.TicketID, msg)
	reopened := false
	t, err := h.d.Store.GetTicket(ctx, tenantID, match.TicketID)
	if err == nil && (t.Status == store.StatusResolved || t.Status == store.StatusClosed) && h.d.Tickets != nil {
		if v, rerr := h.d.Tickets.SetStatusAs(ctx, subj, t.ID, store.StatusOpen, store.ActorInbound); rerr == nil {
			t, reopened = v.Ticket, true
		} else {
			h.d.Log.WarnContext(ctx, "inbound mail: re-open failed", "ticket_id", t.ID, "err", rerr)
		}
	}
	if err == nil {
		events.Emit(ctx, h.d.Events, tenantID, events.TicketRequesterReplied, t, history.ActorKind(subj))
	}
	h.audit(ctx, subj, audit.InboundThreaded, audit.SubjectTicket, match.TicketID, map[string]any{
		"by": match.By, "comment_id": c.ID, "attachments": stored, "skipped": len(msg.Skipped), "reopened": reopened})
	return OutcomeThreaded, match.TicketID, nil
}

// create opens a new email ticket.
func (h *Handler) create(ctx context.Context, subj authz.Subjects, route store.MailboxRoute, recipient string, msg *mailparse.Message, plan Plan) (string, string, error) {
	tenantID := subj.TenantID
	t := store.Ticket{ID: store.NewID(), TenantID: tenantID, Subject: msg.Subject, Description: msg.Text, BodyHTML: msg.HTML,
		Status: store.StatusOpen, Priority: store.PriorityNormal, Source: store.SourceEmail, RequesterEmail: msg.FromEmail,
		RequesterName: msg.FromName, Recipient: recipient, MailboxID: route.MailboxID, ExternalID: msg.MessageID,
		CreatedBy: store.CreatedByInbound}
	if t.Subject == "" {
		t.Subject = mailparse.NoSubject
	}
	st, err := h.upload(ctx, tenantID, t.ID, msg)
	if err != nil {
		return "", "", err
	}
	if err := h.d.Store.CreateTicket(ctx, t); err != nil {
		h.discard(ctx, st)
		if errors.Is(err, repo.ErrConflict) && t.ExternalID != "" { // concurrent duplicate delivery
			if ex, ferr := h.d.Store.FindTicketByExternalID(ctx, tenantID, t.ExternalID); ferr == nil {
				h.audit(ctx, subj, audit.InboundDuplicate, audit.SubjectTicket, ex.ID, nil)
				return OutcomeDuplicate, ex.ID, nil
			}
		}
		return "", "", retry(err)
	}
	stored := h.record(ctx, st, "")
	h.note(ctx, tenantID, t.ID, msg)
	if plan.Apply != nil {
		if err := plan.Apply(ctx, t.ID); err != nil {
			h.d.Log.WarnContext(ctx, "inbound mail: rule actions failed", "ticket_id", t.ID, "err", err)
		}
	}
	created, err := h.d.Store.GetTicket(ctx, tenantID, t.ID)
	if err != nil {
		created = t
	}
	events.Emit(ctx, h.d.Events, tenantID, events.TicketCreated, created, history.ActorKind(subj))
	h.acknowledge(ctx, subj, route, recipient, msg, created)
	h.audit(ctx, subj, audit.InboundCreated, audit.SubjectTicket, t.ID, map[string]any{
		"mailbox_id": route.MailboxID, "attachments": stored, "skipped": len(msg.Skipped), "has_html": msg.HTML != "",
		"auto": msg.IsAuto(), "spam_score": msg.SpamScore})
	return OutcomeCreated, t.ID, nil
}

// note records an internal system comment listing attachments that were not
// stored (size cap) and parts that were not processed (part/depth cap).
func (h *Handler) note(ctx context.Context, tenantID, ticketID string, msg *mailparse.Message) {
	if len(msg.Skipped) == 0 && !msg.Truncated {
		return
	}
	var b strings.Builder
	for _, s := range msg.Skipped {
		fmt.Fprintf(&b, "Attachment %q (%d bytes) was not stored: it exceeds the size limit.\n", s.Filename, s.Size)
	}
	if msg.Truncated {
		b.WriteString("Some parts of this message were not processed: it exceeds the MIME part or nesting limit.\n")
	}
	c := store.Comment{ID: store.NewID(), TenantID: tenantID, TicketID: ticketID, Body: strings.TrimSpace(b.String()), Internal: true,
		AuthorKind: store.AuthorSystem, AuthorName: "Inbound mail", Delivery: store.DeliveryNone}
	if err := h.d.Store.CreateComment(ctx, c); err != nil {
		h.d.Log.WarnContext(ctx, "inbound mail: skip note not recorded", "ticket_id", ticketID, "err", err)
	}
}

func (h *Handler) audit(ctx context.Context, subj authz.Subjects, t audit.EventType, kind, id string, details map[string]any) {
	audit.Emit(ctx, h.d.Audit, audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorInbound, ActorID: subj.ActorID(),
		SubjectKind: kind, SubjectID: id, Outcome: audit.OutcomeOK, Details: details})
}

// refuse answers the generic refusal and records it under the nil tenant (the
// delivery was not, or could not be, routed).
func (h *Handler) refuse(ctx context.Context, w http.ResponseWriter, code int, reason string) {
	audit.Emit(ctx, h.d.Audit, audit.Event{TenantID: audit.NilTenant, EventType: audit.InboundRefused, ActorKind: audit.ActorInbound,
		ActorID: authz.InboundActorID, SubjectKind: audit.SubjectMessage, Outcome: audit.OutcomeRefused, Reason: reason})
	if code == http.StatusServiceUnavailable {
		h.d.Metrics.Inbound(metrics.OutcomeError)
	} else {
		h.d.Metrics.Inbound(metrics.OutcomeRefused)
	}
	h.d.Log.WarnContext(ctx, "inbound mail refused", "status", code, "reason", reason)
	Refusal(w, code)
}

// fail answers 503 so the relay retries.
func (h *Handler) fail(ctx context.Context, w http.ResponseWriter, tenantID string, err error) {
	h.d.Metrics.Inbound(metrics.OutcomeError)
	h.d.Log.ErrorContext(ctx, "inbound mail failed; relay will retry", "tenant_id", tenantID, "err", err)
	if tenantID != "" {
		audit.Emit(ctx, h.d.Audit, audit.Event{TenantID: tenantID, EventType: audit.InboundRefused, ActorKind: audit.ActorInbound,
			ActorID: authz.InboundActorID, SubjectKind: audit.SubjectMessage, Outcome: audit.OutcomeError, Reason: "storage_unavailable"})
	}
	Refusal(w, http.StatusServiceUnavailable)
}
