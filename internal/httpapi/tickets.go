package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/tickets"
)

// ticketError maps the tickets service's errors to the contract reasons.
func ticketError(err error) error {
	var ve tickets.ValidationError
	switch {
	case errors.Is(err, tickets.ErrNotFound):
		return ErrTicketNotFound
	case errors.Is(err, tickets.ErrInvalidStatus):
		return ErrInvalidStatus
	case errors.Is(err, tickets.ErrInvalidAssignee):
		return ErrInvalidAssignee
	case errors.Is(err, tickets.ErrDirectoryUnavailable):
		return ErrDirectoryUnavailable
	case errors.As(err, &ve):
		return ErrValidation
	}
	return err
}

// withSubject wraps a handler needing the verified caller.
func (s *Server) withSubject(method, path string, fn func(w http.ResponseWriter, r *http.Request, subj authz.Subjects)) {
	s.MustHandle(method, path, func(w http.ResponseWriter, r *http.Request) {
		subj, err := Subjects(r)
		if err != nil {
			Fail(w, r, nil, err)
			return
		}
		fn(w, r, subj)
	})
}

func queryInt(r *http.Request, name string) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0
	}
	return n
}

// registerTickets mounts the ticket lifecycle routes (US1). Route permissions
// are enforced by the authorize middleware from the OpenAPI document.
func (s *Server) registerTickets(svc *tickets.Service) {
	fail := func(w http.ResponseWriter, r *http.Request, err error) { Fail(w, r, s.log, ticketError(err)) }

	s.withSubject("GET", Prefix+"/tickets", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		q := r.URL.Query()
		f := store.TicketFilter{Status: q.Get("status"), Priority: q.Get("priority"), AssigneeID: q.Get("assignee_id"),
			TagID: q.Get("tag_id"), Query: q.Get("query"), Page: queryInt(r, "page"), PageSize: queryInt(r, "page_size")}
		items, total, err := svc.List(r.Context(), subj, f)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
	})

	s.withSubject("POST", Prefix+"/tickets", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in tickets.CreateInput
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		v, err := svc.Create(r.Context(), subj, in)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusCreated, v)
	})

	s.withSubject("GET", Prefix+"/tickets/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		v, err := svc.Get(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})

	s.withSubject("PUT", Prefix+"/tickets/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in tickets.UpdateInput
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		v, err := svc.Update(r.Context(), subj, r.PathValue("id"), in)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})

	s.withSubject("DELETE", Prefix+"/tickets/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := svc.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	s.withSubject("POST", Prefix+"/tickets/{id}/assign", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			AssigneeID *string `json:"assignee_id"`
		}
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		assignee := ""
		if in.AssigneeID != nil {
			assignee = *in.AssigneeID
		}
		v, err := svc.Assign(r.Context(), subj, r.PathValue("id"), assignee)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})

	s.withSubject("POST", Prefix+"/tickets/{id}/status", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			Status string `json:"status"`
		}
		if err := DecodeJSON(r, &in, 0); err != nil {
			fail(w, r, err)
			return
		}
		v, err := svc.SetStatus(r.Context(), subj, r.PathValue("id"), in.Status)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})

	s.withSubject("GET", Prefix+"/tickets/{id}/history", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		items, err := svc.History(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	s.withSubject("GET", Prefix+"/assignable-users", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		users, err := svc.AssignableUsers(r.Context(), subj)
		if err != nil {
			fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": users})
	})
}
