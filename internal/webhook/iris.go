// Package webhook implements the inbound iris/KumoMTA email webhook that
// turns forwarded support mail into tickets.
package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-common/viewer"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent"
	"github.com/go-tangra/go-tangra-ticket/internal/mailer"
	"github.com/go-tangra/go-tangra-ticket/internal/metrics"
	"github.com/go-tangra/go-tangra-ticket/internal/rules"
	"github.com/go-tangra/go-tangra-ticket/internal/thread"
)

const maxBodyBytes = 10 << 20 // 10 MiB

// IrisHandler ingests inbound emails forwarded by iris/KumoMTA. iris POSTs
// the raw RFC822 message (Content-Type: message/rfc822) with X-Iris-Recipient
// and X-Iris-Message-Id headers. The endpoint is protected by a shared token.
type IrisHandler struct {
	log      *log.Helper
	repo     *data.TicketRepo
	comments *data.CommentRepo
	attach   *data.AttachmentRepo
	storage  *data.StorageClient
	tags      *data.TagRepo
	ruleRepo  *data.RuleRepo
	engine    *rules.Engine
	mail      *mailer.Mailer
	metrics   *metrics.Collector
	token     string
	tenantID  uint32
	autoReply bool
}

func NewIrisHandler(ctx *bootstrap.Context, repo *data.TicketRepo, comments *data.CommentRepo, attach *data.AttachmentRepo, storage *data.StorageClient, tags *data.TagRepo, ruleRepo *data.RuleRepo, engine *rules.Engine, mail *mailer.Mailer, m *metrics.Collector) *IrisHandler {
	tid := uint32(0)
	if v := os.Getenv("TICKET_DEFAULT_TENANT"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			tid = uint32(n)
		}
	}
	h := &IrisHandler{
		log:       ctx.NewLoggerHelper("ticket/webhook/iris"),
		repo:      repo,
		comments:  comments,
		attach:    attach,
		storage:   storage,
		tags:      tags,
		ruleRepo:  ruleRepo,
		engine:    engine,
		mail:      mail,
		metrics:   m,
		token:     os.Getenv("TICKET_WEBHOOK_TOKEN"),
		tenantID:  tid,
		autoReply: os.Getenv("TICKET_AUTOREPLY") != "off",
	}
	if h.token == "" {
		h.log.Warn("TICKET_WEBHOOK_TOKEN is not set; the iris webhook is UNAUTHENTICATED — set it in production")
	}
	return h
}

// authorized validates the shared token from X-Ticket-Token, ?token=, or a
// Bearer Authorization header, in constant time.
func (h *IrisHandler) authorized(r *http.Request) bool {
	if h.token == "" {
		return true
	}
	got := r.Header.Get("X-Ticket-Token")
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	if got == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			got = strings.TrimPrefix(a, "Bearer ")
		}
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

// Handle is the HTTP handler for POST /webhooks/iris.
func (h *IrisHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorized(r) {
		h.log.Warn("iris webhook: invalid or missing token")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	pm := parseMail(body)
	subject, fromName, fromEmail, text := pm.subject, pm.fromName, pm.fromEmail, pm.text
	recipient := r.Header.Get("X-Iris-Recipient")
	externalID := r.Header.Get("X-Iris-Message-Id")
	if externalID == "" {
		externalID = pm.messageID
	}
	if subject == "" {
		subject = "(no subject)"
	}

	// This raw HTTP handler bypasses the gRPC middleware, so inject the
	// system viewer that ent's tenant/privacy layer requires for writes.
	ctx := viewer.NewSystemViewerContext(r.Context())

	// Idempotency: a repeated delivery of the same message is a no-op (the
	// message-id may already be a ticket root or a recorded comment).
	if pm.messageID != "" {
		if cid, _ := h.comments.FindTicketIDByMessageIDs(ctx, h.tenantID, []string{pm.messageID}); cid != "" {
			h.log.Infof("iris webhook: duplicate reply %s -> ticket %s, ignored", pm.messageID, cid)
			writeOK(w)
			return
		}
	}
	if externalID != "" {
		if existing, _ := h.repo.FindByExternalID(ctx, h.tenantID, externalID); existing != nil {
			h.log.Infof("iris webhook: duplicate message %s -> ticket %s, ignored", externalID, existing.ID)
			writeOK(w)
			return
		}
	}

	// Threading: if this mail is a reply to an existing ticket, append it as a
	// public comment instead of opening a new ticket.
	if ticketID := h.findThreadTicket(ctx, pm, subject); ticketID != "" {
		h.appendReply(ctx, ticketID, pm, fromEmail, fromName)
		h.log.Infof("iris webhook: threaded reply from %q onto ticket %s", fromEmail, ticketID)
		writeOK(w)
		return
	}

	// Evaluate auto-tag rules BEFORE creating the ticket so a matching "drop"
	// rule (spam, unwanted sender domain, high spam score) can discard the
	// message without ever opening a ticket.
	email := h.ruleEmail(pm, recipient)
	drop, matched := h.evaluateRules(ctx, email)
	if drop {
		h.log.Infof("iris webhook: dropped message from %q (subject=%q, spam=%.2f) by rule", fromEmail, subject, pm.spamScore)
		writeOK(w)
		return
	}

	e, err := h.repo.Create(ctx, data.NewTicket{
		TenantID:       h.tenantID,
		ExternalID:     externalID,
		Subject:        subject,
		Description:    text,
		BodyHTML:       pm.html,
		Source:         "iris",
		RequesterEmail: fromEmail,
		RequesterName:  fromName,
		Recipient:      recipient,
	})
	if err != nil {
		h.log.Errorf("iris webhook: create ticket failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if h.metrics != nil {
		h.metrics.TicketCreated(e.Status)
	}

	h.storeAttachments(ctx, e.ID, pm.attachments)
	h.applyMatchedRules(ctx, e.ID, matched)
	h.sendAutoReply(ctx, e, pm)

	h.log.Infof("iris webhook: created ticket %s from %q (subject=%q, attachments=%d)", e.ID, fromEmail, subject, len(pm.attachments))
	writeOK(w)
}

// ruleEmail builds the rule-evaluation input from a parsed mail.
func (h *IrisHandler) ruleEmail(pm parsedMail, recipient string) rules.Email {
	return rules.Email{
		Subject:        pm.subject,
		Body:           pm.text,
		From:           pm.fromEmail,
		FromName:       pm.fromName,
		Recipient:      recipient,
		FromDomain:     emailDomain(pm.fromEmail),
		HasAttachments: len(pm.attachments) > 0,
		SpamScore:      pm.spamScore,
	}
}

// emailDomain extracts the domain part of an address (lowercased).
func emailDomain(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return ""
}

// sendAutoReply emails the requester an acknowledgement when a new ticket is
// created, embedding the request reference in the BODY. It records the message
// as a public comment so a reply to it threads back. Heavily guarded against
// mail loops (RFC 3834): never replies to auto/bulk mail or daemon senders.
func (h *IrisHandler) sendAutoReply(ctx context.Context, tk *ent.Ticket, pm parsedMail) {
	if !h.autoReply || h.mail == nil || !h.mail.Enabled() || h.comments == nil {
		return
	}
	if tk.RequesterEmail == "" || isDaemonSender(tk.RequesterEmail) {
		return
	}
	if pm.isAuto() {
		h.log.Infof("iris webhook: skip auto-reply for ticket %s (incoming is auto/bulk)", tk.ID)
		return
	}

	from := tk.Recipient
	if from == "" {
		from = h.mail.DefaultFrom()
	}
	if from == "" {
		h.log.Warnf("iris webhook: no From for auto-reply on ticket %s; skipped", tk.ID)
		return
	}
	// Never auto-reply to ourselves: if the requester is the support address
	// (forwarding loop, misconfig, test mail), From==To would risk a loop.
	if strings.EqualFold(tk.RequesterEmail, from) || strings.EqualFold(tk.RequesterEmail, h.mail.DefaultFrom()) {
		h.log.Infof("iris webhook: skip auto-reply for ticket %s (requester is the support address)", tk.ID)
		return
	}

	body := autoReplyBody(tk.RequesterName)
	body = thread.AppendReference(body, tk.ID)
	msgID := h.mail.GenMessageID(tk.ID, from)

	var refs []string
	if tk.ExternalID != "" {
		refs = append(refs, tk.ExternalID)
	}

	if err := h.mail.Send(ctx, mailer.OutMail{
		From:          from,
		To:            tk.RequesterEmail,
		ToName:        tk.RequesterName,
		Subject:       thread.ReplySubject(tk.Subject),
		Text:          body,
		MessageID:     msgID,
		InReplyTo:     tk.ExternalID,
		References:    refs,
		AutoSubmitted: true,
	}); err != nil {
		h.log.Warnf("iris webhook: auto-reply for ticket %s failed: %v", tk.ID, err)
		return
	}

	// Record the auto-reply as a public comment (author 0 = system) so a reply
	// to it can be threaded via its message-id.
	if _, err := h.comments.Create(ctx, data.NewComment{
		TenantID:   h.tenantID,
		TicketID:   tk.ID,
		Body:       body,
		Internal:   false,
		AuthorID:   0,
		AuthorKind: "system",
		MessageID:  msgID,
	}); err != nil {
		h.log.Warnf("iris webhook: record auto-reply comment for ticket %s failed: %v", tk.ID, err)
	}
	h.log.Infof("iris webhook: auto-reply sent for ticket %s to %q", tk.ID, tk.RequesterEmail)
}

// autoReplyBody builds the acknowledgement text. A custom template may be set
// via TICKET_AUTOREPLY_BODY ({{name}} is substituted); the reference line is
// appended separately so the identifier is always present.
func autoReplyBody(requesterName string) string {
	name := strings.TrimSpace(requesterName)
	if tmpl := os.Getenv("TICKET_AUTOREPLY_BODY"); tmpl != "" {
		return strings.ReplaceAll(tmpl, "{{name}}", name)
	}
	greeting := "Hello"
	if name != "" {
		greeting = "Hello " + name
	}
	return greeting + ",\n\n" +
		"Thank you for contacting support. We have received your request and " +
		"our team will get back to you as soon as possible.\n\n" +
		"You can simply reply to this email to add more information."
}

// findThreadTicket resolves an inbound reply to its ticket: first by the
// In-Reply-To/References chain (against ticket roots and recorded comment
// message-ids), then by the [#<id>] subject token. Returns "" for a new thread.
func (h *IrisHandler) findThreadTicket(ctx context.Context, pm parsedMail, subject string) string {
	candidates := make([]string, 0, len(pm.references)+1)
	if pm.inReplyTo != "" {
		candidates = append(candidates, pm.inReplyTo)
	}
	candidates = append(candidates, pm.references...)

	// (1) match a recorded comment message-id.
	if cid, _ := h.comments.FindTicketIDByMessageIDs(ctx, h.tenantID, candidates); cid != "" {
		return cid
	}
	// (2) match a ticket root (external_id).
	for _, id := range candidates {
		if t, _ := h.repo.FindByExternalID(ctx, h.tenantID, id); t != nil {
			return t.ID
		}
	}
	// (3) reference token [#<id>] in the body (preferred) or subject (legacy).
	id := thread.ParseToken(pm.text)
	if id == "" {
		id = thread.ParseToken(subject)
	}
	if id != "" {
		if t, _ := h.repo.Get(ctx, h.tenantID, id); t != nil {
			return t.ID
		}
	}
	return ""
}

// appendReply records an inbound reply as a public comment and re-opens the
// ticket if it was resolved/closed. Attachments are stored on the ticket.
func (h *IrisHandler) appendReply(ctx context.Context, ticketID string, pm parsedMail, fromEmail, fromName string) {
	body := pm.text
	if body == "" {
		body = "(empty message)"
	}
	if _, err := h.comments.Create(ctx, data.NewComment{
		TenantID:    h.tenantID,
		TicketID:    ticketID,
		Body:        body,
		Internal:    false,
		AuthorEmail: fromEmail,
		AuthorName:  fromName,
		AuthorKind:  "requester",
		MessageID:   pm.messageID,
	}); err != nil {
		h.log.Errorf("iris webhook: append reply to %s failed: %v", ticketID, err)
		return
	}
	h.storeAttachments(ctx, ticketID, pm.attachments)

	if tk, _ := h.repo.Get(ctx, h.tenantID, ticketID); tk != nil {
		if tk.Status == "TICKET_STATUS_RESOLVED" || tk.Status == "TICKET_STATUS_CLOSED" {
			if _, err := h.repo.UpdateStatus(ctx, h.tenantID, ticketID, "TICKET_STATUS_OPEN"); err != nil {
				h.log.Warnf("iris webhook: reopen ticket %s failed: %v", ticketID, err)
			}
		}
	}
}

// effectiveExpr returns the CEL expression for a rule (raw expression override,
// else compiled from its structured conditions).
func (h *IrisHandler) effectiveExpr(r *ent.TicketRule) string {
	if r.Expression != "" {
		return r.Expression
	}
	var conds []rules.Condition
	if r.Conditions != "" {
		_ = json.Unmarshal([]byte(r.Conditions), &conds)
	}
	return rules.BuildExpression(r.Match, conds)
}

// evaluateRules evaluates enabled rules against the email and returns whether
// the message should be dropped (a matched rule carries a "drop" action) and
// the list of matched rules (for action application after the ticket is made).
func (h *IrisHandler) evaluateRules(ctx context.Context, email rules.Email) (bool, []*ent.TicketRule) {
	if h.ruleRepo == nil || h.engine == nil {
		return false, nil
	}
	rls, err := h.ruleRepo.ListEnabled(ctx, h.tenantID)
	if err != nil || len(rls) == 0 {
		return false, nil
	}
	drop := false
	var matched []*ent.TicketRule
	for _, r := range rls {
		ok, eerr := h.engine.Eval(h.effectiveExpr(r), email)
		if eerr != nil {
			h.log.Warnf("iris webhook: rule %q eval failed: %v", r.Name, eerr)
			continue
		}
		if !ok {
			continue
		}
		matched = append(matched, r)
		for _, a := range h.ruleActions(r) {
			if a.Type == "drop" {
				drop = true
				h.log.Infof("iris webhook: rule %q matched with drop action", r.Name)
			}
		}
	}
	return drop, matched
}

// applyMatchedRules applies the non-drop actions of already-matched rules to a
// created ticket. Tags are unioned across rules; assign/status/priority use the
// first matching rule that sets one. Best-effort.
func (h *IrisHandler) applyMatchedRules(ctx context.Context, ticketID string, matched []*ent.TicketRule) {
	if h.tags == nil || len(matched) == 0 {
		return
	}
	seen := map[string]bool{}
	var tagIDs []string
	var assignee *uint32
	var status, priority string

	for _, r := range matched {
		for _, a := range h.ruleActions(r) {
			switch a.Type {
			case "tag", "":
				for _, n := range a.TagNames {
					if n == "" {
						continue
					}
					tag, terr := h.tags.EnsureByName(ctx, h.tenantID, a.TagKind, n)
					if terr != nil || tag == nil || seen[tag.ID] {
						continue
					}
					seen[tag.ID] = true
					tagIDs = append(tagIDs, tag.ID)
				}
			case "assign":
				if assignee == nil {
					v := a.AssigneeID
					assignee = &v
				}
			case "status":
				if status == "" && a.Status != "" {
					status = a.Status
				}
			case "priority":
				if priority == "" && a.Priority != "" {
					priority = a.Priority
				}
			}
		}
		h.log.Infof("iris webhook: rule %q applied to ticket %s", r.Name, ticketID)
	}

	if len(tagIDs) > 0 {
		if err := h.tags.AddTicketTags(ctx, ticketID, tagIDs); err != nil {
			h.log.Warnf("iris webhook: apply tags to %s failed: %v", ticketID, err)
		}
	}
	if assignee != nil {
		if _, err := h.repo.Assign(ctx, h.tenantID, ticketID, *assignee); err != nil {
			h.log.Warnf("iris webhook: assign %s failed: %v", ticketID, err)
		}
	}
	if status != "" {
		if _, err := h.repo.UpdateStatus(ctx, h.tenantID, ticketID, status); err != nil {
			h.log.Warnf("iris webhook: set status on %s failed: %v", ticketID, err)
		}
	}
	if priority != "" {
		if _, err := h.repo.Update(ctx, h.tenantID, ticketID, nil, nil, &priority); err != nil {
			h.log.Warnf("iris webhook: set priority on %s failed: %v", ticketID, err)
		}
	}
}

// ruleActions returns the rule's actions, falling back to the legacy single
// tag action (tag_kind/tag_names) for rules saved before the actions list.
func (h *IrisHandler) ruleActions(r *ent.TicketRule) []rules.Action {
	var actions []rules.Action
	if r.Actions != "" {
		_ = json.Unmarshal([]byte(r.Actions), &actions)
	}
	if len(actions) == 0 && r.TagNames != "" {
		var names []string
		_ = json.Unmarshal([]byte(r.TagNames), &names)
		if len(names) > 0 {
			actions = append(actions, rules.Action{Type: "tag", TagKind: r.TagKind, TagNames: names})
		}
	}
	return actions
}

// storeAttachments uploads each decoded attachment to S3 and records it.
// Best-effort: a failed attachment is logged and skipped, never failing the
// ticket. No-op when storage is not configured.
func (h *IrisHandler) storeAttachments(ctx context.Context, ticketID string, atts []mailAttachment) {
	if h.storage == nil || h.attach == nil || len(atts) == 0 {
		return
	}
	for _, a := range atts {
		name := a.filename
		if name == "" {
			name = "attachment"
		}
		key := fmt.Sprintf("tickets/%s/%s/%s", ticketID, uuid.NewString(), path.Base(name))
		if err := h.storage.Upload(ctx, key, a.contentType, a.data); err != nil {
			h.log.Warnf("iris webhook: upload attachment %q failed: %v", name, err)
			continue
		}
		if _, err := h.attach.Create(ctx, data.NewAttachment{
			TenantID:    h.tenantID,
			TicketID:    ticketID,
			Filename:    name,
			ContentType: a.contentType,
			Size:        int64(len(a.data)),
			StorageKey:  key,
			ContentID:   a.contentID,
			Inline:      a.inline,
		}); err != nil {
			h.log.Warnf("iris webhook: record attachment %q failed: %v", name, err)
		}
	}
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
