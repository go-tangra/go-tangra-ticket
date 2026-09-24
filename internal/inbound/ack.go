package inbound

// Loop-safe acknowledgement of a new email ticket (FR-013, research D5,
// RFC 3834). It runs after the ticket and its rule actions are stored, so it
// sees the final ticket. The acknowledgement:
//
//   - is sent only when the routed mailbox has auto_ack on and a relay is
//     configured;
//   - is never sent to automated mail (Auto-Submitted other than "no";
//     Precedence bulk/list/junk/auto_reply), to daemon/no-reply senders or to an
//     address that is itself a support mailbox (self-addressed / mailbox loop);
//   - renders the mailbox template ({{name}} → requester name; a built-in
//     greeting when empty) and always carries this ticket's reference line once;
//   - is recorded FIRST as a public system comment with its Message-ID (so the
//     requester's answer threads back), then sent with Auto-Submitted:
//     auto-replied + Precedence: auto_reply (mailer AutoReply), and the comment's
//     delivery set to sent/failed.
//
// Every decision is audited (ack.sent ok|error, ack.skipped with a reason) and
// counted (ticket_acks_total{result}); neither carries addresses or bodies.

import (
	"context"
	"errors"
	"strings"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/audit"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailer"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailparse"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/metrics"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/thread"
)

// Reasons an acknowledgement is skipped (audit ack.skipped).
const (
	AckOff           = "auto_ack_off"
	AckNoRelay       = "no_relay"
	AckAutoSubmitted = "auto_submitted"
	AckBulk          = "bulk_precedence"
	AckDaemon        = "daemon_sender"
	AckSelf          = "self_addressed"
	AckUnsafe        = "unsafe_header"
	AckRecordFailed  = "record_failed"
)

// AckAuthorName is the author name of the recorded acknowledgement comment.
const AckAuthorName = "Auto-acknowledgement"

// NamePlaceholder is substituted with the requester's name in a template.
const NamePlaceholder = "{{name}}"

// defaultAck is used when the mailbox has no template.
const defaultAck = "Hello{{greeting}},\n\n" +
	"Thank you for contacting support. We have received your request and " +
	"our team will get back to you as soon as possible.\n\n" +
	"You can simply reply to this email to add more information."

// AckBody renders the acknowledgement text: the template with {{name}}
// substituted (the built-in greeting when the template is empty) and the
// ticket's reference line appended exactly once.
func AckBody(template, name, ticketID string) string {
	name = strings.TrimSpace(name)
	var body string
	if strings.TrimSpace(template) == "" {
		greeting := ""
		if name != "" {
			greeting = " " + name
		}
		body = strings.ReplaceAll(defaultAck, "{{greeting}}", greeting)
	} else {
		body = strings.ReplaceAll(template, NamePlaceholder, name)
	}
	ref := thread.ReferenceLine(ticketID)
	if strings.Contains(body, ref) {
		return body
	}
	return strings.TrimRight(body, "\r\n") + "\n\n--\n" + ref + "\n"
}

// ackSuppressed reports why no acknowledgement may be sent ("" when it may).
func (h *Handler) ackSuppressed(ctx context.Context, route store.MailboxRoute, recipient string, msg *mailparse.Message, t store.Ticket) string {
	if !route.AutoAck {
		return AckOff
	}
	if h.d.Mailer == nil || !h.d.Mailer.Enabled() {
		return AckNoRelay
	}
	if msg.AutoSubmitted != "" && msg.AutoSubmitted != "no" {
		return AckAutoSubmitted
	}
	if msg.IsAuto() {
		return AckBulk
	}
	to := strings.ToLower(strings.TrimSpace(t.RequesterEmail))
	if mailparse.IsDaemonSender(to) || !strings.Contains(to, "@") {
		return AckDaemon
	}
	if strings.EqualFold(to, recipient) {
		return AckSelf
	}
	// any support mailbox (of any tenant) answering another is a mail loop
	if _, err := h.d.Store.RouteMailbox(ctx, to); err == nil {
		return AckSelf
	}
	return ""
}

// domainFor is the Message-ID domain: the mailbox's, else the fallback.
func domainFor(mailbox, fallback string) string {
	if d := thread.DomainOf(mailbox); d != "" {
		return d
	}
	return fallback
}

// acknowledge sends (or skips) the acknowledgement of the new ticket t.
func (h *Handler) acknowledge(ctx context.Context, subj authz.Subjects, route store.MailboxRoute, recipient string, msg *mailparse.Message, t store.Ticket) {
	if reason := h.ackSuppressed(ctx, route, recipient, msg, t); reason != "" {
		h.ackSkipped(ctx, subj, t.ID, reason)
		return
	}
	out := mailer.OutMail{From: recipient, FromName: route.DisplayName, To: strings.TrimSpace(t.RequesterEmail), ToName: t.RequesterName,
		Subject: thread.ReplySubject(t.Subject), Text: AckBody(route.AutoAckTemplate, t.RequesterName, t.ID),
		MessageID: thread.MessageID(t.ID, domainFor(recipient, h.d.MailDomain)), AutoReply: true}
	if t.ExternalID != "" {
		out.InReplyTo, out.References = t.ExternalID, []string{t.ExternalID}
	}
	if mailer.Validate(out) != nil && out.InReplyTo != "" {
		// an unusable root message id only costs the threading headers
		out.InReplyTo, out.References = "", nil
	}
	if mailer.Validate(out) != nil {
		h.ackSkipped(ctx, subj, t.ID, AckUnsafe)
		return
	}
	c := store.Comment{ID: store.NewID(), TenantID: t.TenantID, TicketID: t.ID, Body: out.Text, AuthorKind: store.AuthorSystem,
		AuthorID: subj.ActorID(), AuthorName: AckAuthorName, MessageID: out.MessageID, Delivery: store.DeliveryNone}
	if err := h.d.Store.CreateComment(ctx, c); err != nil {
		h.d.Log.WarnContext(ctx, "inbound mail: acknowledgement not recorded", "ticket_id", t.ID, "err", err)
		h.ackSkipped(ctx, subj, t.ID, AckRecordFailed)
		return
	}
	sendErr := h.d.Mailer.Send(ctx, out)
	delivery, outcome, result := store.DeliverySent, audit.OutcomeOK, metrics.ResultSent
	if sendErr != nil {
		delivery, outcome, result = store.DeliveryFailed, audit.OutcomeError, metrics.ResultFailed
		h.d.Log.WarnContext(ctx, "inbound mail: acknowledgement not delivered", "ticket_id", t.ID, "err", sendErr)
	}
	if err := h.d.Store.SetCommentDelivery(ctx, t.TenantID, c.ID, delivery); err != nil && !errors.Is(err, context.Canceled) {
		h.d.Log.WarnContext(ctx, "inbound mail: acknowledgement delivery state not recorded", "ticket_id", t.ID, "err", err)
	}
	h.d.Metrics.Ack(result)
	audit.Emit(ctx, h.d.Audit, audit.Event{TenantID: subj.TenantID, EventType: audit.AckSent, ActorKind: audit.ActorInbound, ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectTicket, SubjectID: t.ID, Outcome: outcome, Details: map[string]any{"comment_id": c.ID, "mailbox_id": route.MailboxID}})
}

func (h *Handler) ackSkipped(ctx context.Context, subj authz.Subjects, ticketID, reason string) {
	h.d.Metrics.Ack(metrics.ResultSkipped)
	audit.Emit(ctx, h.d.Audit, audit.Event{TenantID: subj.TenantID, EventType: audit.AckSkipped, ActorKind: audit.ActorInbound, ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectTicket, SubjectID: ticketID, Outcome: audit.OutcomeOK, Reason: reason})
}
