package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/backup"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/stats"
)

// MaxImportBytes bounds a backup import body (matches the route's declared
// x-freya-max-body-bytes).
const MaxImportBytes = 64 << 20

// registerStats mounts GET /stats (stats:read): the dashboard aggregates of
// the caller's tenant over ?days (default 30, capped at 365 by the contract).
func (s *Server) registerStats(svc *stats.Service) {
	s.withSubject("GET", Prefix+"/stats", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		st, err := svc.Get(r.Context(), subj, stats.Days(r.URL.Query().Get("days")))
		if err != nil {
			Fail(w, r, s.log, err)
			return
		}
		WriteJSON(w, http.StatusOK, st)
	})
}

// backupError maps the backup service's document errors to 422.
func backupError(err error) error {
	if errors.Is(err, backup.ErrBadSchema) || errors.Is(err, backup.ErrTooLarge) {
		return ErrValidation
	}
	return err
}

// registerBackup mounts the tenant export/import (backup:manage; another
// tenant or a full restore additionally requires platform-admin).
func (s *Server) registerBackup(svc *backup.Service) {
	s.withSubject("POST", Prefix+"/backup/export", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			TenantID string `json:"tenant_id"`
		}
		if r.ContentLength != 0 && r.Body != nil && r.Body != http.NoBody {
			if err := DecodeJSON(r, &in, 0); err != nil {
				Fail(w, r, s.log, err)
				return
			}
		}
		b, err := svc.Export(r.Context(), subj, in.TenantID)
		if err != nil {
			Fail(w, r, s.log, backupError(err))
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="ticket-backup.json"`)
		WriteJSON(w, http.StatusOK, b)
	})

	s.withSubject("POST", Prefix+"/backup/import", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			Mode     string        `json:"mode"`
			TenantID string        `json:"tenant_id"`
			Full     bool          `json:"full"`
			Backup   backup.Backup `json:"backup"`
		}
		if err := DecodeJSON(r, &in, MaxImportBytes); err != nil {
			Fail(w, r, s.log, err)
			return
		}
		if in.Mode == "" {
			in.Mode = r.URL.Query().Get("mode")
		}
		res, err := svc.Import(r.Context(), subj, in.Backup, backup.Options{Mode: in.Mode, TenantID: in.TenantID, Full: in.Full})
		if err != nil {
			Fail(w, r, s.log, backupError(err))
			return
		}
		WriteJSON(w, http.StatusOK, res)
	})
}
