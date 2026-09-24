package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tags"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tickets"
)

// tagError maps the tags service's errors to the contract reasons.
func tagError(err error) error {
	var ve tags.ValidationError
	switch {
	case errors.Is(err, tags.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, tags.ErrConflict):
		return ErrConflict
	case errors.Is(err, tags.ErrTicketNotFound):
		return ErrTicketNotFound
	case errors.Is(err, tags.ErrUnknownTag), errors.As(err, &ve):
		return ErrValidation
	}
	return err
}

// registerTags mounts the tag vocabulary routes (US5: tickets:read lists,
// tags:manage writes) and, with the tickets service, the set-tags route of a
// ticket (tickets:manage).
func (s *Server) registerTags(svc *tags.Service, tk *tickets.Service) {
	fail := func(w http.ResponseWriter, r *http.Request, err error) { Fail(w, r, s.log, tagError(err)) }

	s.withSubject("GET", Prefix+"/tags", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		items, err := svc.List(r.Context(), subj, r.URL.Query().Get("kind"))
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	s.withSubject("POST", Prefix+"/tags", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in tags.Input
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		t, err := svc.Create(r.Context(), subj, in)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusCreated, t)
	})

	s.withSubject("PUT", Prefix+"/tags/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in tags.Input
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		t, err := svc.Update(r.Context(), subj, r.PathValue("id"), in)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, t)
	})

	s.withSubject("DELETE", Prefix+"/tags/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := svc.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if tk == nil {
		return
	}
	s.withSubject("POST", Prefix+"/tickets/{id}/tags", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			TagIDs []string `json:"tag_ids"`
		}
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		id := r.PathValue("id")
		if err := svc.SetTicketTags(r.Context(), subj, id, in.TagIDs); err != nil {
			fail(w, r, err)
			return
		}
		v, err := tk.Get(r.Context(), subj, id)
		if err != nil {
			Fail(w, r, s.log, ticketError(err))
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
}
