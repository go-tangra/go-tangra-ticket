package rules

// Service is the rules administration surface (FR-011, rules:manage): CRUD
// with validation on save — every condition known and typed, the effective
// expression type-checked to bool, at least one action, each action complete,
// an assignee assignable — plus the /rules/test dry run. Mutations are audited
// (counts and action types only; values stay out of the audit trail).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-freya/freya/services/ticket/internal/agents"
	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

// Service errors.
var (
	ErrNotFound             = errors.New("rules: not found")
	ErrInvalidAssignee      = errors.New("rules: invalid assignee")
	ErrDirectoryUnavailable = errors.New("rules: user directory unavailable")
)

// Input bounds.
const (
	MaxName     = 200
	MaxActions  = 20
	MaxTagNames = 20
	MaxTagName  = 100
	MaxSort     = 1_000_000
)

// DefaultMaxRules bounds the rules of a tenant when Deps.MaxRules is unset.
const DefaultMaxRules = 500

// Deps wire the service; Audit is optional, Agents is needed for assign actions.
type Deps struct {
	Store    repo.Rules
	Engine   *Engine
	Agents   agents.Directory
	Audit    audit.Recorder
	MaxRules int
}

// Service manages rules.
type Service struct {
	d   Deps
	now func() time.Time
}

// NewService builds the service.
func NewService(d Deps) *Service {
	if d.MaxRules <= 0 {
		d.MaxRules = DefaultMaxRules
	}
	return &Service{d: d, now: func() time.Time { return time.Now().UTC() }}
}

// Input is the RuleInput body (create/update replace the whole rule).
type Input struct {
	Name       string            `json:"name"`
	Enabled    *bool             `json:"enabled"`
	SortOrder  int               `json:"sort_order"`
	Match      string            `json:"match"`
	Conditions []store.Condition `json:"conditions"`
	Expression string            `json:"expression"`
	Actions    []store.Action    `json:"actions"`
}

// Sample is the dry-run message (fromDomain is derived from From).
type Sample struct {
	Subject        string  `json:"subject"`
	Body           string  `json:"body"`
	From           string  `json:"from"`
	FromName       string  `json:"from_name"`
	Recipient      string  `json:"recipient"`
	HasAttachments bool    `json:"has_attachments"`
	SpamScore      float64 `json:"spam_score"`
}

// Email is the evaluation input of the sample.
func (s Sample) Email() Email {
	return Email{Subject: s.Subject, Body: truncate(s.Body, MaxBodyEval), From: s.From, FromName: s.FromName, Recipient: s.Recipient,
		FromDomain: DomainOf(s.From), HasAttachments: s.HasAttachments, SpamScore: s.SpamScore}
}

// TestResult is the dry-run outcome: the rule's actions when it matched.
type TestResult struct {
	Matched bool           `json:"matched"`
	Actions []store.Action `json:"actions"`
}

func tenantOf(subj authz.Subjects) (string, error) {
	if subj.TenantID == "" {
		return "", fmt.Errorf("%w: tenant required", authz.ErrForbidden)
	}
	return subj.TenantID, nil
}

// present gives a rule its API shape (lists never null).
func present(r store.Rule) store.Rule {
	if r.Conditions == nil {
		r.Conditions = []store.Condition{}
	}
	if r.Actions == nil {
		r.Actions = []store.Action{}
	}
	return r
}

func mapErr(err error) error {
	if errors.Is(err, repo.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// normalizeActions validates and cleans the actions (fields of other action
// types are dropped; tag names trimmed and de-duplicated case-insensitively).
func normalizeActions(in []store.Action) ([]store.Action, error) {
	if len(in) == 0 {
		return nil, invalid("actions", "add at least one action")
	}
	if len(in) > MaxActions {
		return nil, invalid("actions", "at most %d actions", MaxActions)
	}
	out := make([]store.Action, 0, len(in))
	for i, a := range in {
		at := fmt.Sprintf("actions[%d]", i)
		n := store.Action{Type: strings.TrimSpace(a.Type)}
		switch n.Type {
		case store.ActionTag:
			n.TagKind = a.TagKind
			if n.TagKind == "" {
				n.TagKind = store.KindTag
			}
			if !store.ValidKind(n.TagKind) {
				return nil, invalid(at+".tag_kind", "tag or category")
			}
			seen := map[string]bool{}
			for _, name := range a.TagNames {
				name = strings.TrimSpace(name)
				if name == "" || seen[strings.ToLower(name)] {
					continue
				}
				if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxTagName || hasControl(name) {
					return nil, invalid(at+".tag_names", "a tag name is too long or has invalid characters")
				}
				seen[strings.ToLower(name)] = true
				n.TagNames = append(n.TagNames, name)
			}
			if len(n.TagNames) == 0 {
				return nil, invalid(at+".tag_names", "name at least one tag")
			}
			if len(n.TagNames) > MaxTagNames {
				return nil, invalid(at+".tag_names", "at most %d tags", MaxTagNames)
			}
		case store.ActionAssign:
			n.AssigneeID = strings.TrimSpace(a.AssigneeID)
			if n.AssigneeID == "" {
				return nil, invalid(at+".assignee_id", "choose an assignee")
			}
		case store.ActionStatus:
			n.Status = a.Status
			if !store.ValidStatus(n.Status) {
				return nil, invalid(at+".status", "unknown status")
			}
		case store.ActionPriority:
			n.Priority = a.Priority
			if !store.ValidPriority(n.Priority) {
				return nil, invalid(at+".priority", "unknown priority")
			}
		case store.ActionDrop:
		default:
			return nil, invalid(at+".type", "unknown action %q", a.Type)
		}
		out = append(out, n)
	}
	return out, nil
}

func hasControl(v string) bool {
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// build validates in into a rule (without assignee checks).
func (s *Service) build(in Input) (store.Rule, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return store.Rule{}, invalid("name", "required")
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxName || hasControl(name) {
		return store.Rule{}, invalid("name", "too long or invalid characters")
	}
	if in.SortOrder < -MaxSort || in.SortOrder > MaxSort {
		return store.Rule{}, invalid("sort_order", "out of range")
	}
	match, err := NormalizeMatch(in.Match)
	if err != nil {
		return store.Rule{}, err
	}
	actions, err := normalizeActions(in.Actions)
	if err != nil {
		return store.Rule{}, err
	}
	conds := make([]store.Condition, len(in.Conditions))
	for i, c := range in.Conditions {
		conds[i] = store.Condition{Field: strings.TrimSpace(c.Field), Operator: strings.ToLower(strings.TrimSpace(c.Operator)), Value: c.Value}
	}
	r := store.Rule{Name: name, Enabled: in.Enabled == nil || *in.Enabled, SortOrder: in.SortOrder, Match: match,
		Conditions: conds, Expression: strings.TrimSpace(in.Expression), Actions: actions}
	if r.Expression != "" && len(in.Conditions) > MaxConditions {
		return store.Rule{}, invalid("conditions", "at most %d conditions", MaxConditions)
	}
	if r.Expression != "" {
		// Stored conditions stay valid even when the expression overrides them.
		if len(conds) > 0 {
			if _, err := BuildExpression(match, conds); err != nil {
				return store.Rule{}, err
			}
		}
	}
	if _, err := s.d.Engine.Compile(r); err != nil {
		return store.Rule{}, err
	}
	return r, nil
}

// checkAssignees verifies every assign action names an assignable agent.
func (s *Service) checkAssignees(ctx context.Context, tenantID string, r store.Rule) error {
	for _, a := range r.Actions {
		if a.Type != store.ActionAssign {
			continue
		}
		if s.d.Agents == nil {
			return ErrDirectoryUnavailable
		}
		_, err := s.d.Agents.Get(ctx, tenantID, a.AssigneeID)
		switch {
		case err == nil:
		case errors.Is(err, agents.ErrUnknownUser), errors.Is(err, agents.ErrNotAssignable):
			return ErrInvalidAssignee
		default:
			return ErrDirectoryUnavailable
		}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, subj authz.Subjects, t audit.EventType, id, outcome, reason string, r *store.Rule) {
	var details map[string]any
	if r != nil {
		types := make([]any, len(r.Actions))
		for i, a := range r.Actions {
			types[i] = a.Type
		}
		details = map[string]any{"enabled": r.Enabled, "sort_order": r.SortOrder, "match": r.Match, "conditions": len(r.Conditions),
			"expression": r.Expression != "", "actions": types, "version": r.Version}
	}
	audit.Emit(ctx, s.d.Audit, audit.Event{TenantID: subj.TenantID, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectRule, SubjectID: id, Outcome: outcome, Reason: reason, Details: details})
}

func reasonOf(err error) string {
	var ie *InvalidError
	switch {
	case errors.As(err, &ie):
		return "invalid_rule"
	case errors.Is(err, ErrInvalidAssignee):
		return "invalid_assignee"
	case errors.Is(err, ErrDirectoryUnavailable):
		return "directory_unavailable"
	}
	return "error"
}

// List returns every rule of the tenant in evaluation order.
func (s *Service) List(ctx context.Context, subj authz.Subjects) ([]store.Rule, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return nil, err
	}
	out, err := s.d.Store.ListRules(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i] = present(out[i])
	}
	if out == nil {
		out = []store.Rule{}
	}
	return out, nil
}

// Get returns one rule of the tenant.
func (s *Service) Get(ctx context.Context, subj authz.Subjects, id string) (store.Rule, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Rule{}, err
	}
	r, err := s.d.Store.GetRule(ctx, tenantID, id)
	return present(r), mapErr(err)
}

func (s *Service) validate(ctx context.Context, subj authz.Subjects, event audit.EventType, id string, in Input) (store.Rule, error) {
	r, err := s.build(in)
	if err == nil {
		err = s.checkAssignees(ctx, subj.TenantID, r)
	}
	if err != nil && !errors.Is(err, ErrDirectoryUnavailable) {
		s.audit(ctx, subj, event, id, audit.OutcomeRefused, reasonOf(err), nil)
	}
	return r, err
}

// Create validates and stores a new rule (enabled unless stated otherwise).
func (s *Service) Create(ctx context.Context, subj authz.Subjects, in Input) (store.Rule, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Rule{}, err
	}
	existing, err := s.d.Store.ListRules(ctx, tenantID)
	if err != nil {
		return store.Rule{}, err
	}
	if len(existing) >= s.d.MaxRules {
		return store.Rule{}, invalid("rules", "a tenant may have at most %d rules", s.d.MaxRules)
	}
	id := store.NewID()
	r, err := s.validate(ctx, subj, audit.RuleCreate, id, in)
	if err != nil {
		return store.Rule{}, err
	}
	now := s.now()
	r.ID, r.TenantID, r.Version, r.CreatedAt, r.UpdatedAt = id, tenantID, 1, now, now
	if err := s.d.Store.CreateRule(ctx, r); err != nil {
		return store.Rule{}, err
	}
	stored, err := s.d.Store.GetRule(ctx, tenantID, id)
	if err != nil {
		return store.Rule{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.RuleCreate, id, audit.OutcomeOK, "", &stored)
	return present(stored), nil
}

// Update replaces a rule and bumps its version (the program cache key).
func (s *Service) Update(ctx context.Context, subj authz.Subjects, id string, in Input) (store.Rule, error) {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return store.Rule{}, err
	}
	cur, err := s.d.Store.GetRule(ctx, tenantID, id)
	if err != nil {
		return store.Rule{}, mapErr(err)
	}
	r, err := s.validate(ctx, subj, audit.RuleUpdate, id, in)
	if err != nil {
		return store.Rule{}, err
	}
	r.ID, r.TenantID, r.Version, r.CreatedAt, r.UpdatedAt = id, tenantID, cur.Version, cur.CreatedAt, s.now()
	stored, err := s.d.Store.UpdateRule(ctx, r)
	if err != nil {
		return store.Rule{}, mapErr(err)
	}
	s.audit(ctx, subj, audit.RuleUpdate, id, audit.OutcomeOK, "", &stored)
	return present(stored), nil
}

// Delete removes a rule and evicts its program.
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) error {
	tenantID, err := tenantOf(subj)
	if err != nil {
		return err
	}
	if err := s.d.Store.DeleteRule(ctx, tenantID, id); err != nil {
		return mapErr(err)
	}
	s.d.Engine.Forget(id)
	s.audit(ctx, subj, audit.RuleDelete, id, audit.OutcomeOK, "", nil)
	return nil
}

// Test compiles in (validated like a save, without assignee lookups) and
// evaluates it against the sample under the engine's cost limit and deadline.
// Nothing is stored.
func (s *Service) Test(ctx context.Context, subj authz.Subjects, in Input, sample Sample) (TestResult, error) {
	if _, err := tenantOf(subj); err != nil {
		return TestResult{}, err
	}
	r, err := s.build(in)
	if err != nil {
		return TestResult{}, err
	}
	p, err := s.d.Engine.Compile(r)
	if err != nil {
		return TestResult{}, err
	}
	ectx, cancel := context.WithTimeout(ctx, s.d.Engine.Timeout())
	defer cancel()
	ok, err := s.d.Engine.Evaluate(ectx, p, sample.Email())
	if err != nil {
		return TestResult{}, invalid("expression", "evaluation failed: %v", err)
	}
	res := TestResult{Matched: ok, Actions: []store.Action{}}
	if ok {
		res.Actions = r.Actions
	}
	return res, nil
}
