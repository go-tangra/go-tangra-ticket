package data

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	entCrud "github.com/tx7do/go-crud/entgo"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-ticket/internal/data/ent"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent/ticketrule"
)

type RuleRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *log.Helper
}

func NewRuleRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *RuleRepo {
	return &RuleRepo{log: ctx.NewLoggerHelper("ticket/repo/rule"), entClient: entClient}
}

// RuleData carries rule fields (conditions/tag_names are JSON strings).
type RuleData struct {
	TenantID   uint32
	Name       string
	Enabled    bool
	SortOrder  int
	Match      string
	Conditions string
	Expression string
	TagKind    string
	TagNames   string
}

func (r *RuleRepo) Create(ctx context.Context, d RuleData) (*ent.TicketRule, error) {
	return r.entClient.Client().TicketRule.Create().
		SetID(uuid.NewString()).
		SetTenantID(d.TenantID).
		SetName(d.Name).
		SetEnabled(d.Enabled).
		SetSortOrder(d.SortOrder).
		SetMatch(d.Match).
		SetConditions(d.Conditions).
		SetExpression(d.Expression).
		SetTagKind(normKind(d.TagKind)).
		SetTagNames(d.TagNames).
		SetCreateTime(time.Now()).
		SetUpdateTime(time.Now()).
		Save(ctx)
}

func (r *RuleRepo) List(ctx context.Context, tenantID uint32) ([]*ent.TicketRule, error) {
	return r.entClient.Client().TicketRule.Query().
		Where(ticketrule.TenantIDEQ(tenantID)).
		Order(ent.Asc(ticketrule.FieldSortOrder), ent.Asc(ticketrule.FieldCreateTime)).
		All(ctx)
}

func (r *RuleRepo) ListEnabled(ctx context.Context, tenantID uint32) ([]*ent.TicketRule, error) {
	return r.entClient.Client().TicketRule.Query().
		Where(ticketrule.TenantIDEQ(tenantID), ticketrule.EnabledEQ(true)).
		Order(ent.Asc(ticketrule.FieldSortOrder), ent.Asc(ticketrule.FieldCreateTime)).
		All(ctx)
}

func (r *RuleRepo) Get(ctx context.Context, tenantID uint32, id string) (*ent.TicketRule, error) {
	e, err := r.entClient.Client().TicketRule.Query().
		Where(ticketrule.IDEQ(id), ticketrule.TenantIDEQ(tenantID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *RuleRepo) Update(ctx context.Context, tenantID uint32, id string, d RuleData) (*ent.TicketRule, error) {
	e, err := r.entClient.Client().TicketRule.UpdateOneID(id).
		Where(ticketrule.TenantIDEQ(tenantID)).
		SetName(d.Name).
		SetEnabled(d.Enabled).
		SetSortOrder(d.SortOrder).
		SetMatch(d.Match).
		SetConditions(d.Conditions).
		SetExpression(d.Expression).
		SetTagKind(normKind(d.TagKind)).
		SetTagNames(d.TagNames).
		SetUpdateTime(time.Now()).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *RuleRepo) Delete(ctx context.Context, tenantID uint32, id string) error {
	_, err := r.entClient.Client().TicketRule.Delete().
		Where(ticketrule.IDEQ(id), ticketrule.TenantIDEQ(tenantID)).Exec(ctx)
	return err
}
