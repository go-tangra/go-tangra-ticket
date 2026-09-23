package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/rules"
)

// ruleFail answers a rules refusal: invalid_rule carries the offending field
// and the reason (compiler messages for expressions), the other errors map to
// their contract reasons.
func (s *Server) ruleFail(w http.ResponseWriter, r *http.Request, err error) {
	var ie *rules.InvalidError
	switch {
	case errors.As(err, &ie):
		WriteDetail(w, ErrInvalidRule, map[string]any{"field": ie.Field, "message": ie.Msg})
	case errors.Is(err, rules.ErrNotFound):
		Fail(w, r, s.log, ErrNotFound)
	case errors.Is(err, rules.ErrInvalidAssignee):
		Fail(w, r, s.log, ErrInvalidAssignee)
	case errors.Is(err, rules.ErrDirectoryUnavailable):
		Fail(w, r, s.log, ErrDirectoryUnavailable)
	default:
		Fail(w, r, s.log, err)
	}
}

// registerRules mounts the triage-rule routes (US4, rules:manage).
func (s *Server) registerRules(svc *rules.Service) {
	s.withSubject("GET", Prefix+"/rules", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		items, err := svc.List(r.Context(), subj)
		if err != nil {
			s.ruleFail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	s.withSubject("POST", Prefix+"/rules", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in rules.Input
		if err := DecodeJSON(r, &in, 0); err != nil {
			s.ruleFail(w, r, err)
			return
		}
		rule, err := svc.Create(r.Context(), subj, in)
		if err != nil {
			s.ruleFail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusCreated, rule)
	})

	s.withSubject("POST", Prefix+"/rules/test", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			Rule   rules.Input  `json:"rule"`
			Sample rules.Sample `json:"sample"`
		}
		if err := DecodeJSON(r, &in, 256<<10); err != nil {
			s.ruleFail(w, r, err)
			return
		}
		res, err := svc.Test(r.Context(), subj, in.Rule, in.Sample)
		if err != nil {
			s.ruleFail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, res)
	})

	s.withSubject("GET", Prefix+"/rules/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		rule, err := svc.Get(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.ruleFail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, rule)
	})

	s.withSubject("PUT", Prefix+"/rules/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in rules.Input
		if err := DecodeJSON(r, &in, 0); err != nil {
			s.ruleFail(w, r, err)
			return
		}
		rule, err := svc.Update(r.Context(), subj, r.PathValue("id"), in)
		if err != nil {
			s.ruleFail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, rule)
	})

	s.withSubject("DELETE", Prefix+"/rules/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := svc.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			s.ruleFail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
