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
	tags     *data.TagRepo
	ruleRepo *data.RuleRepo
	engine   *rules.Engine
	metrics  *metrics.Collector
	token    string
	tenantID uint32
}

func NewIrisHandler(ctx *bootstrap.Context, repo *data.TicketRepo, comments *data.CommentRepo, attach *data.AttachmentRepo, storage *data.StorageClient, tags *data.TagRepo, ruleRepo *data.RuleRepo, engine *rules.Engine, m *metrics.Collector) *IrisHandler {
	tid := uint32(0)
	if v := os.Getenv("TICKET_DEFAULT_TENANT"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			tid = uint32(n)
		}
	}
	h := &IrisHandler{
		log:      ctx.NewLoggerHelper("ticket/webhook/iris"),
		repo:     repo,
		comments: comments,
		attach:   attach,
		storage:  storage,
		tags:     tags,
		ruleRepo: ruleRepo,
		engine:   engine,
		metrics:  m,
		token:    os.Getenv("TICKET_WEBHOOK_TOKEN"),
		tenantID: tid,
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
	h.applyRules(ctx, e.ID, pm, recipient, len(pm.attachments) > 0)

	h.log.Infof("iris webhook: created ticket %s from %q (subject=%q, attachments=%d)", e.ID, fromEmail, subject, len(pm.attachments))
	writeOK(w)
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
	// (3) subject token [#<id>].
	if id := thread.ParseToken(subject); id != "" {
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
	_ = fromName
	if _, err := h.comments.Create(ctx, data.NewComment{
		TenantID:    h.tenantID,
		TicketID:    ticketID,
		Body:        body,
		Internal:    false,
		AuthorEmail: fromEmail,
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

// applyRules evaluates enabled auto-tagging rules against the parsed email and
// applies each matching rule's actions (tag / assign / status / priority).
// Tags are unioned across all matching rules; for assign/status/priority the
// first matching rule that specifies one wins. Best-effort.
func (h *IrisHandler) applyRules(ctx context.Context, ticketID string, pm parsedMail, recipient string, hasAttachments bool) {
	if h.ruleRepo == nil || h.tags == nil || h.engine == nil {
		return
	}
	rls, err := h.ruleRepo.ListEnabled(ctx, h.tenantID)
	if err != nil || len(rls) == 0 {
		return
	}
	email := rules.Email{
		Subject:        pm.subject,
		Body:           pm.text,
		From:           pm.fromEmail,
		FromName:       pm.fromName,
		Recipient:      recipient,
		HasAttachments: hasAttachments,
	}

	seen := map[string]bool{}
	var tagIDs []string
	var assignee *uint32
	var status, priority string

	for _, r := range rls {
		expr := r.Expression
		if expr == "" {
			var conds []rules.Condition
			if r.Conditions != "" {
				_ = json.Unmarshal([]byte(r.Conditions), &conds)
			}
			expr = rules.BuildExpression(r.Match, conds)
		}
		ok, eerr := h.engine.Eval(expr, email)
		if eerr != nil {
			h.log.Warnf("iris webhook: rule %q eval failed: %v", r.Name, eerr)
			continue
		}
		if !ok {
			continue
		}

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
		h.log.Infof("iris webhook: rule %q matched ticket %s", r.Name, ticketID)
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
