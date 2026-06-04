// Package webhook implements the inbound iris/KumoMTA email webhook that
// turns forwarded support mail into tickets.
package webhook

import (
	"bytes"
	"crypto/subtle"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-common/viewer"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
	"github.com/go-tangra/go-tangra-ticket/internal/metrics"
)

const maxBodyBytes = 10 << 20 // 10 MiB

// IrisHandler ingests inbound emails forwarded by iris/KumoMTA. iris POSTs
// the raw RFC822 message (Content-Type: message/rfc822) with X-Iris-Recipient
// and X-Iris-Message-Id headers. The endpoint is protected by a shared token.
type IrisHandler struct {
	log      *log.Helper
	repo     *data.TicketRepo
	metrics  *metrics.Collector
	token    string
	tenantID uint32
}

func NewIrisHandler(ctx *bootstrap.Context, repo *data.TicketRepo, m *metrics.Collector) *IrisHandler {
	tid := uint32(0)
	if v := os.Getenv("TICKET_DEFAULT_TENANT"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			tid = uint32(n)
		}
	}
	h := &IrisHandler{
		log:      ctx.NewLoggerHelper("ticket/webhook/iris"),
		repo:     repo,
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

	subject, fromName, fromEmail, text, messageID := parseEmail(body)
	recipient := r.Header.Get("X-Iris-Recipient")
	externalID := r.Header.Get("X-Iris-Message-Id")
	if externalID == "" {
		externalID = messageID
	}
	if subject == "" {
		subject = "(no subject)"
	}

	// This raw HTTP handler bypasses the gRPC middleware, so inject the
	// system viewer that ent's tenant/privacy layer requires for writes.
	ctx := viewer.NewSystemViewerContext(r.Context())

	// Idempotency: a repeated delivery of the same message is a no-op.
	if externalID != "" {
		if existing, _ := h.repo.FindByExternalID(ctx, h.tenantID, externalID); existing != nil {
			h.log.Infof("iris webhook: duplicate message %s -> ticket %s, ignored", externalID, existing.ID)
			writeOK(w)
			return
		}
	}

	e, err := h.repo.Create(ctx, data.NewTicket{
		TenantID:       h.tenantID,
		ExternalID:     externalID,
		Subject:        subject,
		Description:    text,
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
	h.log.Infof("iris webhook: created ticket %s from %q (subject=%q)", e.ID, fromEmail, subject)
	writeOK(w)
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// parseEmail extracts the ticket fields from a raw RFC822 message. Multipart
// bodies are stored as-is in v1 (the raw body becomes the description); a
// future iteration can walk MIME parts to extract text/plain.
func parseEmail(raw []byte) (subject, fromName, fromEmail, text, messageID string) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", "", "", string(raw), ""
	}
	dec := &mime.WordDecoder{}
	subject, _ = dec.DecodeHeader(msg.Header.Get("Subject"))
	messageID = strings.Trim(msg.Header.Get("Message-Id"), "<>")
	if addr, e := mail.ParseAddress(msg.Header.Get("From")); e == nil {
		fromEmail = addr.Address
		fromName, _ = dec.DecodeHeader(addr.Name)
	} else {
		fromEmail = strings.TrimSpace(msg.Header.Get("From"))
	}
	b, _ := io.ReadAll(io.LimitReader(msg.Body, maxBodyBytes))
	text = string(b)
	return
}
