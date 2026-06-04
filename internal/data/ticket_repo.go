package data

import (
	"context"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	entCrud "github.com/tx7do/go-crud/entgo"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-ticket/internal/data/ent"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent/ticket"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent/ticketcomment"
)

type TicketRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *log.Helper
}

func NewTicketRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *TicketRepo {
	return &TicketRepo{
		log:       ctx.NewLoggerHelper("ticket/repo/ticket"),
		entClient: entClient,
	}
}

// NewTicket carries the fields needed to create a ticket.
type NewTicket struct {
	TenantID       uint32
	CreateBy       uint32
	ExternalID     string
	Subject        string
	Description    string
	Priority       string
	Source         string
	RequesterEmail string
	RequesterName  string
	Recipient      string
	AssigneeID     uint32
}

// TicketFilter narrows the list query. Zero values are no-ops except where noted.
type TicketFilter struct {
	Status     string
	Priority   string
	ByAssignee bool // when true, filter on AssigneeID (including 0 = unassigned)
	AssigneeID uint32
	Query      string
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (r *TicketRepo) Create(ctx context.Context, t NewTicket) (*ent.Ticket, error) {
	b := r.entClient.Client().Ticket.Create().
		SetID(uuid.NewString()).
		SetTenantID(t.TenantID).
		SetSubject(t.Subject).
		SetDescription(t.Description).
		SetStatus("TICKET_STATUS_OPEN").
		SetPriority(orDefault(t.Priority, "TICKET_PRIORITY_NORMAL")).
		SetSource(orDefault(t.Source, "manual")).
		SetRequesterEmail(t.RequesterEmail).
		SetRequesterName(t.RequesterName).
		SetRecipient(t.Recipient).
		SetAssigneeID(t.AssigneeID)
	if t.CreateBy != 0 {
		b = b.SetCreateBy(t.CreateBy)
	}
	// Only set external_id when present so the unique (tenant, external_id)
	// index keeps NULL for manual tickets (NULLs don't collide).
	if t.ExternalID != "" {
		b = b.SetExternalID(t.ExternalID)
	}
	e, err := b.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, err
		}
		r.log.Errorf("create ticket failed: %s", err.Error())
		return nil, err
	}
	return e, nil
}

func (r *TicketRepo) Get(ctx context.Context, tenantID uint32, id string) (*ent.Ticket, error) {
	e, err := r.entClient.Client().Ticket.Query().
		Where(ticket.IDEQ(id), ticket.TenantIDEQ(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

// FindByExternalID returns the ticket with the given external_id, or (nil, nil).
func (r *TicketRepo) FindByExternalID(ctx context.Context, tenantID uint32, externalID string) (*ent.Ticket, error) {
	if externalID == "" {
		return nil, nil
	}
	e, err := r.entClient.Client().Ticket.Query().
		Where(ticket.TenantIDEQ(tenantID), ticket.ExternalIDEQ(externalID)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *TicketRepo) List(ctx context.Context, tenantID uint32, page, pageSize int32, f TicketFilter) ([]*ent.Ticket, int, error) {
	q := r.entClient.Client().Ticket.Query().Where(ticket.TenantIDEQ(tenantID))
	if f.Status != "" {
		q = q.Where(ticket.StatusEQ(f.Status))
	}
	if f.Priority != "" {
		q = q.Where(ticket.PriorityEQ(f.Priority))
	}
	if f.ByAssignee {
		q = q.Where(ticket.AssigneeIDEQ(f.AssigneeID))
	}
	if f.Query != "" {
		q = q.Where(ticket.Or(
			ticket.SubjectContainsFold(f.Query),
			ticket.RequesterEmailContainsFold(f.Query),
			ticket.RequesterNameContainsFold(f.Query),
		))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	rows, err := q.
		Order(ent.Desc(ticket.FieldCreateTime)).
		Offset(int((page - 1) * pageSize)).
		Limit(int(pageSize)).
		All(ctx)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *TicketRepo) Assign(ctx context.Context, tenantID uint32, id string, assigneeID uint32) (*ent.Ticket, error) {
	e, err := r.entClient.Client().Ticket.UpdateOneID(id).
		Where(ticket.TenantIDEQ(tenantID)).
		SetAssigneeID(assigneeID).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *TicketRepo) UpdateStatus(ctx context.Context, tenantID uint32, id, status string) (*ent.Ticket, error) {
	e, err := r.entClient.Client().Ticket.UpdateOneID(id).
		Where(ticket.TenantIDEQ(tenantID)).
		SetStatus(status).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *TicketRepo) Update(ctx context.Context, tenantID uint32, id string, subject, description, priority *string) (*ent.Ticket, error) {
	upd := r.entClient.Client().Ticket.UpdateOneID(id).Where(ticket.TenantIDEQ(tenantID))
	if subject != nil {
		upd = upd.SetSubject(*subject)
	}
	if description != nil {
		upd = upd.SetDescription(*description)
	}
	if priority != nil {
		upd = upd.SetPriority(*priority)
	}
	e, err := upd.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (r *TicketRepo) Delete(ctx context.Context, tenantID uint32, id string) error {
	// Remove comments first to satisfy the foreign key.
	if _, err := r.entClient.Client().TicketComment.Delete().
		Where(ticketcomment.TicketIDEQ(id), ticketcomment.TenantIDEQ(tenantID)).
		Exec(ctx); err != nil {
		return err
	}
	_, err := r.entClient.Client().Ticket.Delete().
		Where(ticket.IDEQ(id), ticket.TenantIDEQ(tenantID)).
		Exec(ctx)
	return err
}

// CountComments returns the number of comments on a ticket.
func (r *TicketRepo) CountComments(ctx context.Context, ticketID string) int {
	n, err := r.entClient.Client().TicketComment.Query().
		Where(ticketcomment.TicketIDEQ(ticketID)).
		Count(ctx)
	if err != nil {
		return 0
	}
	return n
}

// CountByStatus returns ticket counts grouped by status (for metrics seeding).
func (r *TicketRepo) CountByStatus(ctx context.Context, tenantID uint32, status string) int {
	n, err := r.entClient.Client().Ticket.Query().
		Where(ticket.TenantIDEQ(tenantID), ticket.StatusEQ(status)).
		Count(ctx)
	if err != nil {
		return 0
	}
	return n
}
