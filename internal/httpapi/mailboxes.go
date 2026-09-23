package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/mailboxes"
)

// mailboxError maps the mailboxes service's errors to the contract reasons.
func mailboxError(err error) error {
	var ve mailboxes.ValidationError
	switch {
	case errors.Is(err, mailboxes.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, mailboxes.ErrConflict), errors.Is(err, mailboxes.ErrInUse):
		return ErrConflict
	case errors.As(err, &ve):
		return ErrValidation
	}
	return err
}

// registerMailboxes mounts the support-mailbox routes (US2, mailboxes:manage).
func (s *Server) registerMailboxes(svc *mailboxes.Service) {
	fail := func(w http.ResponseWriter, r *http.Request, err error) { Fail(w, r, s.log, mailboxError(err)) }

	s.withSubject("GET", Prefix+"/mailboxes", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		items, err := svc.List(r.Context(), subj)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	s.withSubject("POST", Prefix+"/mailboxes", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in mailboxes.Input
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		mb, err := svc.Create(r.Context(), subj, in)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusCreated, mb)
	})

	s.withSubject("PUT", Prefix+"/mailboxes/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in mailboxes.Input
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		mb, err := svc.Update(r.Context(), subj, r.PathValue("id"), in)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, mb)
	})

	s.withSubject("DELETE", Prefix+"/mailboxes/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		force := r.URL.Query().Get("force") == "true"
		if err := svc.Delete(r.Context(), subj, r.PathValue("id"), force); err != nil {
			fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
