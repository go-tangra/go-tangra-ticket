// Package tags manages a tenant's tag vocabulary (US5, FR-010): tags of kind
// tag or category (default tag, fixed after creation), unique per tenant and
// kind case-insensitively, each with an optional colour and description, and
// the tag set of a ticket (replaced as a whole; unknown ids refused). Deleting
// a tag removes it from every ticket. Every mutation is audited.
//
// Colours are one of the UI palette names (Colors) or a #rgb/#rrggbb value
// kept for imported data; an empty colour means "automatic" — the UI derives
// a stable colour from the name.
package tags

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
	ErrNotFound       = errors.New("tags: not found")
	ErrConflict       = errors.New("tags: name already used for this kind")
	ErrTicketNotFound = errors.New("tags: ticket not found")
	ErrUnknownTag     = errors.New("tags: unknown tag")
)

// ValidationError reports a bad input field.
type ValidationError struct{ Field, Msg string }

func (e ValidationError) Error() string { return "tags: " + e.Field + ": " + e.Msg }

// Bounds.
const (
	MaxName        = 100
	MaxDescription = 1000
	MaxTicketTags  = 100
)

// Colors is the UI palette a tag colour may name.
var Colors = []string{"primary", "secondary", "accent", "info", "success", "warning", "error", "neutral"}

var hexColor = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// ValidColor reports whether c is empty (automatic), a palette name or a hex colour.
func ValidColor(c string) bool {
	if c == "" || hexColor.MatchString(c) {
		return true
	}
	for _, x := range Colors {
		if c == x {
			return true
		}
	}
	return false
}

// Store is the storage the service needs.
type Store interface {
	repo.Tags
	GetTicket(ctx context.Context, tenantID, id string) (store.Ticket, error)
}

// Deps wire the service; Audit is optional.
type Deps struct {
	Store Store
	Audit audit.Recorder
}

// Service manages the vocabulary and ticket tag sets.
type Service struct{ d Deps }

// New builds the service.
func New(d Deps) *Service { return &Service{d: d} }

// Input is the create/update body; nil leaves a field unchanged on update.
// On update Kind may only repeat the current kind.
type Input struct {
	Name        *string `json:"name"`
	Kind        *string `json:"kind"`
	Color       *string `json:"color"`
	Description *string `json:"description"`
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

// CleanName trims and validates a tag name.
func CleanName(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ValidationError{"name", "required"}
	}
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > MaxName || hasControl(v, false) {
		return "", ValidationError{"name", "too long or invalid characters"}
	}
	return v, nil
}

func apply(t *store.Tag, in Input) error {
	if in.Name != nil {
		n, err := CleanName(*in.Name)
		if err != nil {
			return err
		}
		t.Name = n
	}
	if in.Color != nil {
		c := strings.TrimSpace(*in.Color)
		if !ValidColor(c) {
			return ValidationError{"color", "a palette colour (" + strings.Join(Colors, ", ") + ") or #rrggbb"}
		}
		t.Color = c
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if !utf8.ValidString(d) || utf8.RuneCountInString(d) > MaxDescription || hasControl(d, true) {
			return ValidationError{"description", "too long or invalid characters"}
		}
		t.Description = d
	}
	return nil
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, repo.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, repo.ErrConflict):
		return ErrConflict
	}
	return err
}

func (s *Service) audit(ctx context.Context, subj authz.Subjects, t audit.EventType, kind, id, outcome, reason string, details map[string]any) {
	audit.Emit(ctx, s.d.Audit, audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind),
		ActorID: subj.ActorID(), SubjectKind: kind, SubjectID: id, Outcome: outcome, Reason: reason, Details: details})
}

func summary(t store.Tag) map[string]any {
	return map[string]any{"name": t.Name, "kind": t.Kind, "color": t.Color}
}

// List returns the tenant's tags by kind and name ("" kind = every kind).
func (s *Service) List(ctx context.Context, subj authz.Subjects, kind string) ([]store.Tag, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return nil, err
	}
	if kind != "" && !store.ValidKind(kind) {
		return nil, ValidationError{"kind", "tag or category"}
	}
	out, err := s.d.Store.ListTags(ctx, tenantID, kind)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []store.Tag{}
	}
	return out, nil
}

// Create adds a tag (kind tag unless given); ErrConflict when the name is used
// for that kind already (case-insensitive).
func (s *Service) Create(ctx context.Context, subj authz.Subjects, in Input) (store.Tag, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Tag{}, err
	}
	if in.Name == nil {
		return store.Tag{}, ValidationError{"name", "required"}
	}
	t := store.Tag{ID: store.NewID(), TenantID: tenantID, Kind: store.KindTag}
	if in.Kind != nil && *in.Kind != "" {
		if !store.ValidKind(*in.Kind) {
			return store.Tag{}, ValidationError{"kind", "tag or category"}
		}
		t.Kind = *in.Kind
	}
	if err := apply(&t, in); err != nil {
		return store.Tag{}, err
	}
	if err := s.d.Store.CreateTag(ctx, t); err != nil {
		err = mapErr(err)
		if errors.Is(err, ErrConflict) {
			s.audit(ctx, subj, audit.TagCreate, audit.SubjectTag, t.ID, audit.OutcomeRefused, "conflict", nil)
		}
		return store.Tag{}, err
	}
	stored, err := s.d.Store.GetTag(ctx, tenantID, t.ID)
	if err != nil {
		return store.Tag{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.TagCreate, audit.SubjectTag, t.ID, audit.OutcomeOK, "", summary(stored))
	return stored, nil
}

// Update renames/recolours/redescribes a tag; the kind is immutable.
func (s *Service) Update(ctx context.Context, subj authz.Subjects, id string, in Input) (store.Tag, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Tag{}, err
	}
	t, err := s.d.Store.GetTag(ctx, tenantID, id)
	if err != nil {
		return store.Tag{}, mapErr(err)
	}
	if in.Kind != nil && *in.Kind != "" && *in.Kind != t.Kind {
		return store.Tag{}, ValidationError{"kind", "cannot be changed after creation"}
	}
	if err := apply(&t, in); err != nil {
		return store.Tag{}, err
	}
	if err := s.d.Store.UpdateTag(ctx, t); err != nil {
		err = mapErr(err)
		if errors.Is(err, ErrConflict) {
			s.audit(ctx, subj, audit.TagUpdate, audit.SubjectTag, id, audit.OutcomeRefused, "conflict", nil)
		}
		return store.Tag{}, err
	}
	stored, err := s.d.Store.GetTag(ctx, tenantID, id)
	if err != nil {
		return store.Tag{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.TagUpdate, audit.SubjectTag, id, audit.OutcomeOK, "", summary(stored))
	return stored, nil
}

// Delete removes a tag and its links to tickets.
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) error {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return err
	}
	if err := s.d.Store.DeleteTag(ctx, tenantID, id); err != nil {
		return mapErr(err)
	}
	s.audit(ctx, subj, audit.TagDelete, audit.SubjectTag, id, audit.OutcomeOK, "", nil)
	return nil
}

// SetTicketTags replaces the ticket's tag set with ids (duplicates ignored).
// ErrTicketNotFound for a ticket outside the tenant, ErrUnknownTag when any id
// is not a tag of the tenant (nothing changes then).
func (s *Service) SetTicketTags(ctx context.Context, subj authz.Subjects, ticketID string, ids []string) error {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) > MaxTicketTags {
		return ValidationError{"tag_ids", fmt.Sprintf("at most %d tags", MaxTicketTags)}
	}
	if _, err := s.d.Store.GetTicket(ctx, tenantID, ticketID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return ErrTicketNotFound
		}
		return err
	}
	if err := s.d.Store.SetTicketTags(ctx, tenantID, ticketID, uniq); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			s.audit(ctx, subj, audit.TicketTags, audit.SubjectTicket, ticketID, audit.OutcomeRefused, "unknown_tag", nil)
			return ErrUnknownTag
		}
		return err
	}
	s.audit(ctx, subj, audit.TicketTags, audit.SubjectTicket, ticketID, audit.OutcomeOK, "", map[string]any{"tags": len(uniq)})
	return nil
}
