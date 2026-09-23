package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/comments"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

// commentError maps the comments service's errors to the contract reasons.
func commentError(err error) error {
	var ve comments.ValidationError
	switch {
	case errors.Is(err, comments.ErrCommentNotFound):
		return ErrCommentNotFound
	case errors.Is(err, comments.ErrReplyUnavailable):
		return ErrReplyUnavailable
	case errors.Is(err, comments.ErrDeliveryFailed):
		return ErrDeliveryFailed
	case errors.Is(err, comments.ErrUnsafeHeader), errors.As(err, &ve):
		return ErrValidation
	}
	return ticketError(err)
}

// registerComments mounts the conversation routes (US3).
func (s *Server) registerComments(svc *comments.Service) {
	fail := func(w http.ResponseWriter, r *http.Request, err error) { Fail(w, r, s.log, commentError(err)) }

	s.withSubject("GET", Prefix+"/tickets/{id}/comments", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		items, err := svc.List(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	s.withSubject("POST", Prefix+"/tickets/{id}/comments", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			Body     string `json:"body"`
			Internal bool   `json:"internal"`
		}
		if err := DecodeJSON(r, &in, 2<<20); err != nil {
			fail(w, r, err)
			return
		}
		if !in.Internal { // public comments are replies (POST /reply)
			fail(w, r, ErrValidation)
			return
		}
		c, err := svc.AddNote(r.Context(), subj, r.PathValue("id"), in.Body)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusCreated, c)
	})

	s.withSubject("POST", Prefix+"/tickets/{id}/reply", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			Body string `json:"body"`
		}
		if err := DecodeJSON(r, &in, 2<<20); err != nil {
			fail(w, r, err)
			return
		}
		c, err := svc.Reply(r.Context(), subj, r.PathValue("id"), in.Body)
		if errors.Is(err, comments.ErrDeliveryFailed) {
			// the reply is recorded (delivery=failed); the agent is told it was not delivered
			WriteDetail(w, ErrDeliveryFailed, map[string]any{"comment_id": c.ID})
			return
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusCreated, c)
	})

	s.withSubject("DELETE", Prefix+"/comments/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := svc.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// inlineImages are the attachment types that may be served inline (cid images
// of the sanitised message view); everything else is an opaque download.
var inlineImages = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// downloadHeaders are the safe response headers of an attachment (research D8).
func downloadHeaders(a store.Attachment) (contentType, disposition string) {
	contentType, disposition = "application/octet-stream", "attachment"
	if mt, _, err := mime.ParseMediaType(a.ContentType); err == nil && inlineImages[mt] {
		contentType = mt
		if a.Inline || a.ContentID != "" {
			disposition = "inline"
		}
	}
	if d := mime.FormatMediaType(disposition, map[string]string{"filename": SanitizeFilename(a.Filename)}); d != "" {
		return contentType, d
	}
	return contentType, disposition
}

// registerMessage mounts the message-body and attachment routes (US3).
func (s *Server) registerMessage(svc *tickets.Service) {
	s.withSubject("GET", Prefix+"/tickets/{id}/body", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		b, err := svc.Body(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			Fail(w, r, s.log, ticketError(err))
			return
		}
		WriteJSON(w, http.StatusOK, b)
	})

	s.withSubject("GET", Prefix+"/tickets/{id}/attachments/{att_id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		a, rc, err := svc.OpenAttachment(r.Context(), subj, r.PathValue("id"), r.PathValue("att_id"))
		if errors.Is(err, tickets.ErrAttachmentNotFound) {
			Fail(w, r, s.log, ErrNotFound)
			return
		}
		if err != nil {
			Fail(w, r, s.log, ticketError(err))
			return
		}
		defer rc.Close()
		ct, disp := downloadHeaders(a)
		h := w.Header()
		h.Set("Content-Type", ct)
		h.Set("Content-Disposition", disp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		h.Set("Cache-Control", "private, no-store")
		if a.Size > 0 {
			h.Set("Content-Length", strconv.FormatInt(a.Size, 10))
		}
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, rc); err != nil && s.log != nil {
			s.log.WarnContext(r.Context(), "attachment stream interrupted", "request_id", RequestID(r), "err", err)
		}
	})
}
