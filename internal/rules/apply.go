package rules

// Triage runs the tenant's enabled rules against a message that would open a
// new ticket (never a reply) and applies the matched actions once the ticket
// exists (research D6, FR-012):
//
//   - rules run in sort order, bounded by MaxRules;
//   - tags accumulate across every matching rule (created when missing);
//   - for assign/status/priority the first matching rule that sets one wins;
//   - any matching drop discards the message;
//   - a rule that fails to compile or evaluate is skipped, logged, audited
//     and counted (ticket_rules_errors_total) — ingestion always continues.
//
// History rows of rule-made changes carry actor "rule".

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/inbound"
	"github.com/go-freya/freya/services/ticket/internal/metrics"
	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

// TicketActions applies assign/status/priority with an explicit history actor
// (*tickets.Service).
type TicketActions interface {
	AssignAs(ctx context.Context, subj authz.Subjects, id, assigneeID, actorKind string) (tickets.View, error)
	SetStatusAs(ctx context.Context, subj authz.Subjects, id, status, actorKind string) (tickets.View, error)
	SetPriorityAs(ctx context.Context, subj authz.Subjects, id, priority, actorKind string) (tickets.View, error)
}

// RuleStore is the storage the triage needs.
type RuleStore interface {
	repo.Rules
	repo.Tags
}

// TriageDeps wire the triage; Metrics, Audit and Log are optional.
type TriageDeps struct {
	Engine   *Engine
	Store    RuleStore
	Tickets  TicketActions
	Metrics  *metrics.Metrics
	Audit    audit.Recorder
	Log      *slog.Logger
	Timeout  time.Duration // per-message deadline (default: the engine's)
	MaxRules int           // rules evaluated per message (<= 0: all)
}

// Triage implements inbound.Triage.
type Triage struct{ d TriageDeps }

var _ inbound.Triage = (*Triage)(nil)

// NewTriage builds the triage.
func NewTriage(d TriageDeps) *Triage {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Timeout <= 0 && d.Engine != nil {
		d.Timeout = d.Engine.Timeout()
	}
	return &Triage{d: d}
}

// TagRef names a tag of a kind.
type TagRef struct{ Kind, Name string }

// Decision is the combined outcome of the matching rules.
type Decision struct {
	Drop         bool
	MatchedIDs   []string
	MatchedNames []string
	Tags         []TagRef
	AssigneeID   string
	Status       string
	Priority     string
	Errors       int // rules skipped because they failed
}

// Empty reports whether the decision changes nothing.
func (d Decision) Empty() bool {
	return len(d.Tags) == 0 && d.AssigneeID == "" && d.Status == "" && d.Priority == ""
}

// merge folds one matching rule's actions into d.
func (d *Decision) merge(r store.Rule, seen map[string]bool) {
	d.MatchedIDs = append(d.MatchedIDs, r.ID)
	d.MatchedNames = append(d.MatchedNames, r.Name)
	for _, a := range r.Actions {
		switch a.Type {
		case store.ActionTag:
			kind := a.TagKind
			if kind == "" {
				kind = store.KindTag
			}
			for _, n := range a.TagNames {
				n = strings.TrimSpace(n)
				key := kind + "\x00" + strings.ToLower(n)
				if n == "" || seen[key] {
					continue
				}
				seen[key] = true
				d.Tags = append(d.Tags, TagRef{Kind: kind, Name: n})
			}
		case store.ActionAssign:
			if d.AssigneeID == "" {
				d.AssigneeID = a.AssigneeID
			}
		case store.ActionStatus:
			if d.Status == "" {
				d.Status = a.Status
			}
		case store.ActionPriority:
			if d.Priority == "" {
				d.Priority = a.Priority
			}
		case store.ActionDrop:
			d.Drop = true
		}
	}
}

func (t *Triage) ruleError(ctx context.Context, tenantID string, r store.Rule, err error) {
	t.d.Metrics.RuleError()
	t.d.Log.WarnContext(ctx, "rule skipped: evaluation failed", "tenant_id", tenantID, "rule_id", r.ID, "err", err)
	audit.Emit(ctx, t.d.Audit, audit.Event{TenantID: tenantID, EventType: audit.RuleError, ActorKind: audit.ActorRule, ActorID: r.ID,
		SubjectKind: audit.SubjectRule, SubjectID: r.ID, Outcome: audit.OutcomeError, Reason: "evaluation_failed"})
}

// Decide evaluates the tenant's enabled rules against m under the per-message
// deadline. Only a failure to list the rules is returned as an error.
func (t *Triage) Decide(ctx context.Context, tenantID string, m Email) (Decision, error) {
	var d Decision
	rules, err := t.d.Store.ListEnabledRules(ctx, tenantID)
	if err != nil {
		return d, fmt.Errorf("rules: list: %w", err)
	}
	if t.d.MaxRules > 0 && len(rules) > t.d.MaxRules {
		rules = rules[:t.d.MaxRules]
	}
	if len(rules) == 0 {
		return d, nil
	}
	ectx, cancel := context.WithTimeout(ctx, t.d.Timeout)
	defer cancel()
	seen := map[string]bool{}
	for _, r := range rules {
		p, err := t.d.Engine.Program(r)
		if err == nil {
			var ok bool
			if ok, err = t.d.Engine.Evaluate(ectx, p, m); err == nil {
				if ok {
					d.merge(r, seen)
				}
				continue
			}
		}
		d.Errors++
		t.ruleError(ctx, tenantID, r, err)
	}
	return d, nil
}

// Apply runs the decision's actions on the created ticket, best effort: every
// action is attempted and the failures are returned joined.
func (t *Triage) Apply(ctx context.Context, subj authz.Subjects, ticketID string, d Decision) error {
	tenantID := subj.TenantID
	var errs []error
	if len(d.Tags) > 0 {
		ids := make([]string, 0, len(d.Tags))
		for _, ref := range d.Tags {
			tag, err := t.d.Store.EnsureTagByName(ctx, tenantID, ref.Kind, ref.Name)
			if err != nil {
				errs = append(errs, fmt.Errorf("tag: %w", err))
				continue
			}
			ids = append(ids, tag.ID)
		}
		if len(ids) > 0 {
			if err := t.d.Store.AddTicketTags(ctx, tenantID, ticketID, ids); err != nil {
				errs = append(errs, fmt.Errorf("tags: %w", err))
			} else {
				audit.Emit(ctx, t.d.Audit, audit.Event{TenantID: tenantID, EventType: audit.TicketTags, ActorKind: audit.ActorRule,
					ActorID: strings.Join(d.MatchedIDs, ","), SubjectKind: audit.SubjectTicket, SubjectID: ticketID, Outcome: audit.OutcomeOK,
					Details: map[string]any{"added": len(ids), "rules": len(d.MatchedIDs)}})
			}
		}
	}
	if d.AssigneeID != "" {
		if _, err := t.d.Tickets.AssignAs(ctx, subj, ticketID, d.AssigneeID, store.ActorRule); err != nil {
			errs = append(errs, fmt.Errorf("assign: %w", err))
		}
	}
	if d.Status != "" {
		if _, err := t.d.Tickets.SetStatusAs(ctx, subj, ticketID, d.Status, store.ActorRule); err != nil {
			errs = append(errs, fmt.Errorf("status: %w", err))
		}
	}
	if d.Priority != "" {
		if _, err := t.d.Tickets.SetPriorityAs(ctx, subj, ticketID, d.Priority, store.ActorRule); err != nil {
			errs = append(errs, fmt.Errorf("priority: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Evaluate implements inbound.Triage: decide on the message, drop it or hand
// back the actions to apply after creation.
func (t *Triage) Evaluate(ctx context.Context, subj authz.Subjects, in inbound.TriageInput) (inbound.Plan, error) {
	if subj.TenantID == "" {
		return inbound.Plan{}, fmt.Errorf("%w: tenant required", authz.ErrForbidden)
	}
	d, err := t.Decide(ctx, subj.TenantID, EmailOf(in.Message, in.Recipient))
	if err != nil {
		return inbound.Plan{}, err
	}
	if d.Drop {
		t.d.Log.InfoContext(ctx, "inbound mail dropped by rule", "tenant_id", subj.TenantID, "rules", d.MatchedIDs)
		return inbound.Plan{Drop: true}, nil
	}
	if d.Empty() {
		return inbound.Plan{}, nil
	}
	return inbound.Plan{Apply: func(ctx context.Context, ticketID string) error { return t.Apply(ctx, subj, ticketID, d) }}, nil
}
