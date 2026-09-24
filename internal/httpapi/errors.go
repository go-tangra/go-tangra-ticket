package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
)

// Error is a refusal with a stable reason from the closed vocabulary of
// contracts §A (plus the platform envelope reasons).
type Error struct {
	Status int
	Reason string
}

func (e *Error) Error() string { return e.Reason }

// Refusals.
var (
	ErrUnauthenticated = &Error{http.StatusUnauthorized, "unauthenticated"}
	ErrForbidden       = &Error{http.StatusForbidden, "forbidden"}
	ErrNotFound        = &Error{http.StatusNotFound, "not_found"}
	ErrMalformed       = &Error{http.StatusBadRequest, "malformed_body"}
	ErrBadRequest      = &Error{http.StatusBadRequest, "bad_request"}
	ErrValidation      = &Error{http.StatusUnprocessableEntity, "validation_failed"}
	ErrConflict        = &Error{http.StatusConflict, "conflict"}
	ErrBodyTooLarge    = &Error{http.StatusRequestEntityTooLarge, "body_too_large"}
	ErrRateLimited     = &Error{http.StatusTooManyRequests, "rate_limited"}
	ErrUnavailable     = &Error{http.StatusServiceUnavailable, "temporarily_unavailable"}
	ErrNotImplemented  = &Error{http.StatusNotImplemented, "not_implemented"}

	// Ticket-domain reasons (contracts §A).
	ErrInvalidStatus        = &Error{http.StatusUnprocessableEntity, "invalid_status"}
	ErrInvalidAssignee      = &Error{http.StatusUnprocessableEntity, "invalid_assignee"}
	ErrInvalidRule          = &Error{http.StatusUnprocessableEntity, "invalid_rule"}
	ErrTicketNotFound       = &Error{http.StatusNotFound, "ticket_not_found"}
	ErrCommentNotFound      = &Error{http.StatusNotFound, "comment_not_found"}
	ErrReplyUnavailable     = &Error{http.StatusConflict, "reply_unavailable"}
	ErrDeliveryFailed       = &Error{http.StatusBadGateway, "delivery_failed"}
	ErrDirectoryUnavailable = &Error{http.StatusServiceUnavailable, "directory_unavailable"}
)

// MaxBodyBytes bounds JSON bodies of ordinary operations.
const MaxBodyBytes = 64 << 10

// WriteJSON encodes v with status; API responses are never cached.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError emits {"reason": ...} and nothing else.
func WriteError(w http.ResponseWriter, status int, reason string) {
	WriteJSON(w, status, map[string]string{"reason": reason})
}

// WriteDetail emits {"reason": ..., "detail": {...}}; detail values are never secrets.
func WriteDetail(w http.ResponseWriter, e *Error, detail map[string]any) {
	WriteJSON(w, e.Status, map[string]any{"reason": e.Reason, "detail": detail})
}

// Fail maps err to a response: *Error verbatim, store and authz sentinels to
// their reasons, oversized bodies to 413, anything else to 503 (details logged
// only, never returned).
func Fail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	status, reason := Status(err)
	if status >= 500 && log != nil {
		log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "request_id", RequestID(r), "err", err)
	}
	WriteError(w, status, reason)
}

// Status maps an error to its status and reason.
func Status(err error) (int, string) {
	var e *Error
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &e):
		return e.Status, e.Reason
	case errors.As(err, &mbe):
		return ErrBodyTooLarge.Status, ErrBodyTooLarge.Reason
	case errors.Is(err, authz.ErrForbidden):
		return ErrForbidden.Status, ErrForbidden.Reason
	case errors.Is(err, repo.ErrNotFound):
		return ErrNotFound.Status, ErrNotFound.Reason
	case errors.Is(err, repo.ErrConflict), errors.Is(err, repo.ErrNotEmpty):
		return ErrConflict.Status, ErrConflict.Reason
	}
	return ErrUnavailable.Status, ErrUnavailable.Reason
}

// DecodeJSON reads a bounded JSON body into v, refusing unknown fields and
// trailing data. limit <= 0 means MaxBodyBytes.
func DecodeJSON(r *http.Request, v any, limit int64) error {
	if limit <= 0 {
		limit = MaxBodyBytes
	}
	body := http.MaxBytesReader(nil, r.Body, limit)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return ErrBodyTooLarge
		}
		return ErrMalformed
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrMalformed
	}
	return nil
}
