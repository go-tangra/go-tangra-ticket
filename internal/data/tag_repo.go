package data

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	entCrud "github.com/tx7do/go-crud/entgo"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-ticket/internal/data/ent"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent/ticket"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent/tickettag"
)

type TagRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *log.Helper
}

func NewTagRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *TagRepo {
	return &TagRepo{log: ctx.NewLoggerHelper("ticket/repo/tag"), entClient: entClient}
}

type NewTag struct {
	TenantID    uint32
	Name        string
	Kind        string
	Color       string
	Description string
}

func normKind(k string) string {
	if k == "CATEGORY" {
		return "CATEGORY"
	}
	return "TAG"
}

func (r *TagRepo) Create(ctx context.Context, t NewTag) (*ent.TicketTag, error) {
	return r.entClient.Client().TicketTag.Create().
		SetID(uuid.NewString()).
		SetTenantID(t.TenantID).
		SetName(t.Name).
		SetKind(normKind(t.Kind)).
		SetColor(t.Color).
		SetDescription(t.Description).
		SetCreateTime(time.Now()).
		SetUpdateTime(time.Now()).
		Save(ctx)
}

func (r *TagRepo) List(ctx context.Context, tenantID uint32, kind string) ([]*ent.TicketTag, error) {
	q := r.entClient.Client().TicketTag.Query().Where(tickettag.TenantIDEQ(tenantID))
	if kind != "" {
		q = q.Where(tickettag.KindEQ(normKind(kind)))
	}
	return q.Order(ent.Asc(tickettag.FieldName)).All(ctx)
}

func (r *TagRepo) Get(ctx context.Context, tenantID uint32, id string) (*ent.TicketTag, error) {
	e, err := r.entClient.Client().TicketTag.Query().
		Where(tickettag.IDEQ(id), tickettag.TenantIDEQ(tenantID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *TagRepo) Update(ctx context.Context, tenantID uint32, id string, name, color, description *string) (*ent.TicketTag, error) {
	upd := r.entClient.Client().TicketTag.UpdateOneID(id).Where(tickettag.TenantIDEQ(tenantID))
	if name != nil {
		upd = upd.SetName(*name)
	}
	if color != nil {
		upd = upd.SetColor(*color)
	}
	if description != nil {
		upd = upd.SetDescription(*description)
	}
	e, err := upd.SetUpdateTime(time.Now()).Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *TagRepo) Delete(ctx context.Context, tenantID uint32, id string) error {
	_, err := r.entClient.Client().TicketTag.Delete().
		Where(tickettag.IDEQ(id), tickettag.TenantIDEQ(tenantID)).Exec(ctx)
	return err
}

// EnsureByName returns the tag with (tenant, kind, name), creating it when
// absent. Used by the rule engine to auto-create tags.
func (r *TagRepo) EnsureByName(ctx context.Context, tenantID uint32, kind, name string) (*ent.TicketTag, error) {
	kind = normKind(kind)
	e, err := r.entClient.Client().TicketTag.Query().
		Where(tickettag.TenantIDEQ(tenantID), tickettag.KindEQ(kind), tickettag.NameEQ(name)).
		First(ctx)
	if err == nil {
		return e, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	created, cerr := r.Create(ctx, NewTag{TenantID: tenantID, Name: name, Kind: kind})
	if cerr != nil && ent.IsConstraintError(cerr) {
		// Lost a create race — fetch the existing one.
		return r.entClient.Client().TicketTag.Query().
			Where(tickettag.TenantIDEQ(tenantID), tickettag.KindEQ(kind), tickettag.NameEQ(name)).First(ctx)
	}
	return created, cerr
}

// SetTicketTags replaces a ticket's tags with the given tag IDs.
func (r *TagRepo) SetTicketTags(ctx context.Context, tenantID uint32, ticketID string, tagIDs []string) error {
	return r.entClient.Client().Ticket.UpdateOneID(ticketID).
		Where(ticket.TenantIDEQ(tenantID)).
		ClearTags().
		AddTagIDs(tagIDs...).
		SetUpdateTime(time.Now()).
		Exec(ctx)
}

// AddTicketTags adds tag IDs to a ticket (idempotent at the DB level).
func (r *TagRepo) AddTicketTags(ctx context.Context, ticketID string, tagIDs []string) error {
	if len(tagIDs) == 0 {
		return nil
	}
	return r.entClient.Client().Ticket.UpdateOneID(ticketID).
		AddTagIDs(tagIDs...).
		SetUpdateTime(time.Now()).
		Exec(ctx)
}
