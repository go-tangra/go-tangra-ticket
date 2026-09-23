// Package mailboxes manages the support mailboxes of a tenant (FR-014): the
// inbound addresses the edge routes to the tenant, the reply identity (display
// name) and the acknowledgement settings. Addresses are lower-cased bare
// addr-specs and globally unique — one address routes to exactly one tenant.
// A mailbox still referenced by tickets is only deleted with force, which
// detaches those tickets. Every mutation is audited (the address is routing
// configuration, not requester PII; templates are never copied into audit).
package mailboxes

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

// Errors.
var (
	ErrNotFound = errors.New("mailboxes: not found")
	ErrConflict = errors.New("mailboxes: address already routed")
	ErrInUse    = errors.New("mailboxes: referenced by tickets")
)

// ValidationError reports a bad input field.
type ValidationError struct{ Field, Msg string }

func (e ValidationError) Error() string { return "mailboxes: " + e.Field + ": " + e.Msg }

// Bounds.
const (
	DefaultDisplayName = "Support"
	MaxAddress         = 320
	MaxDisplayName     = 200
	MaxTemplate        = 8192
)

// Deps wire the service; Audit is optional.
type Deps struct {
	Store repo.Mailboxes
	Audit audit.Recorder
}

// Service manages mailboxes.
type Service struct{ d Deps }

// New builds the service.
func New(d Deps) *Service { return &Service{d: d} }

// Input is the create/update body; nil leaves a field unchanged on update (on
// create: address required, display name "Support", active, no auto-ack).
type Input struct {
	Address         *string `json:"address"`
	DisplayName     *string `json:"display_name"`
	Active          *bool   `json:"active"`
	AutoAck         *bool   `json:"auto_ack"`
	AutoAckTemplate *string `json:"auto_ack_template"`
}

func tenantOf(subj authz.Subjects) (string, error) {
	if subj.TenantID == "" {
		return "", fmt.Errorf("%w: tenant required", authz.ErrForbidden)
	}
	return subj.TenantID, nil
}

func hasControl(v string, allowNewlines bool) bool {
	for _, r := range v {
		if allowNewlines && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// NormalizeAddress lower-cases and validates a bare addr-spec.
func NormalizeAddress(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || len(v) > MaxAddress || hasControl(v, false) || !utf8.ValidString(v) {
		return "", ValidationError{"address", "a single email address is required"}
	}
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || a.Name != "" {
		return "", ValidationError{"address", "a single email address is required"}
	}
	return v, nil
}

// apply validates in onto m.
func apply(m *store.Mailbox, in Input) error {
	if in.Address != nil {
		a, err := NormalizeAddress(*in.Address)
		if err != nil {
			return err
		}
		m.Address = a
	}
	if in.DisplayName != nil {
		n := strings.TrimSpace(*in.DisplayName)
		if utf8.RuneCountInString(n) > MaxDisplayName || hasControl(n, false) || !utf8.ValidString(n) {
			return ValidationError{"display_name", "too long or invalid characters"}
		}
		if n == "" {
			n = DefaultDisplayName
		}
		m.DisplayName = n
	}
	if in.Active != nil {
		m.Active = *in.Active
	}
	if in.AutoAck != nil {
		m.AutoAck = *in.AutoAck
	}
	if in.AutoAckTemplate != nil {
		tpl := *in.AutoAckTemplate
		if len(tpl) > MaxTemplate || hasControl(tpl, true) || !utf8.ValidString(tpl) {
			return ValidationError{"auto_ack_template", "too long or invalid characters"}
		}
		m.AutoAckTemplate = tpl
	}
	return nil
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, repo.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, repo.ErrConflict):
		return ErrConflict
	case errors.Is(err, repo.ErrNotEmpty):
		return ErrInUse
	}
	return err
}

func (s *Service) audit(ctx context.Context, subj authz.Subjects, t audit.EventType, id, outcome string, details map[string]any) {
	audit.Emit(ctx, s.d.Audit, audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind),
		ActorID: subj.ActorID(), SubjectKind: audit.SubjectMailbox, SubjectID: id, Outcome: outcome, Details: details})
}

func settings(m store.Mailbox) map[string]any {
	return map[string]any{"address": m.Address, "active": m.Active, "auto_ack": m.AutoAck}
}

// List returns the tenant's mailboxes by address.
func (s *Service) List(ctx context.Context, subj authz.Subjects) ([]store.Mailbox, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return nil, err
	}
	out, err := s.d.Store.ListMailboxes(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []store.Mailbox{}
	}
	return out, nil
}

// Get returns one mailbox of the tenant.
func (s *Service) Get(ctx context.Context, subj authz.Subjects, id string) (store.Mailbox, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Mailbox{}, err
	}
	m, err := s.d.Store.GetMailbox(ctx, tenantID, id)
	return m, mapErr(err)
}

// Create adds a mailbox; ErrConflict when the address routes anywhere already.
func (s *Service) Create(ctx context.Context, subj authz.Subjects, in Input) (store.Mailbox, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Mailbox{}, err
	}
	if in.Address == nil {
		return store.Mailbox{}, ValidationError{"address", "a single email address is required"}
	}
	m := store.Mailbox{ID: store.NewID(), TenantID: tenantID, DisplayName: DefaultDisplayName, Active: true}
	if err := apply(&m, in); err != nil {
		return store.Mailbox{}, err
	}
	if err := s.d.Store.CreateMailbox(ctx, m); err != nil {
		err = mapErr(err)
		if errors.Is(err, ErrConflict) {
			s.audit(ctx, subj, audit.MailboxCreate, m.ID, audit.OutcomeRefused, map[string]any{"reason": "conflict"})
		}
		return store.Mailbox{}, err
	}
	stored, err := s.d.Store.GetMailbox(ctx, tenantID, m.ID)
	if err != nil {
		return store.Mailbox{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.MailboxCreate, m.ID, audit.OutcomeOK, settings(stored))
	return stored, nil
}

// Update applies a partial update.
func (s *Service) Update(ctx context.Context, subj authz.Subjects, id string, in Input) (store.Mailbox, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Mailbox{}, err
	}
	m, err := s.d.Store.GetMailbox(ctx, tenantID, id)
	if err != nil {
		return store.Mailbox{}, mapErr(err)
	}
	if err := apply(&m, in); err != nil {
		return store.Mailbox{}, err
	}
	if err := s.d.Store.UpdateMailbox(ctx, m); err != nil {
		err = mapErr(err)
		if errors.Is(err, ErrConflict) {
			s.audit(ctx, subj, audit.MailboxUpdate, id, audit.OutcomeRefused, map[string]any{"reason": "conflict"})
		}
		return store.Mailbox{}, err
	}
	stored, err := s.d.Store.GetMailbox(ctx, tenantID, id)
	if err != nil {
		return store.Mailbox{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.MailboxUpdate, id, audit.OutcomeOK, settings(stored))
	return stored, nil
}

// Delete removes a mailbox; while tickets reference it ErrInUse unless force
// (which detaches them).
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string, force bool) error {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return err
	}
	if err := s.d.Store.DeleteMailbox(ctx, tenantID, id, force); err != nil {
		err = mapErr(err)
		if errors.Is(err, ErrInUse) {
			s.audit(ctx, subj, audit.MailboxDelete, id, audit.OutcomeRefused, map[string]any{"reason": "in_use"})
		}
		return err
	}
	s.audit(ctx, subj, audit.MailboxDelete, id, audit.OutcomeOK, map[string]any{"force": force})
	return nil
}
