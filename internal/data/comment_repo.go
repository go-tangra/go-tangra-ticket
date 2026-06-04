package data

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	entCrud "github.com/tx7do/go-crud/entgo"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-ticket/internal/data/ent"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent/ticketcomment"
)

type CommentRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *log.Helper
}

func NewCommentRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *CommentRepo {
	return &CommentRepo{
		log:       ctx.NewLoggerHelper("ticket/repo/comment"),
		entClient: entClient,
	}
}

type NewComment struct {
	TenantID    uint32
	TicketID    string
	Body        string
	Internal    bool
	AuthorID    uint32
	AuthorEmail string
}

func (r *CommentRepo) Create(ctx context.Context, c NewComment) (*ent.TicketComment, error) {
	e, err := r.entClient.Client().TicketComment.Create().
		SetID(uuid.NewString()).
		SetTenantID(c.TenantID).
		SetTicketID(c.TicketID).
		SetBody(c.Body).
		SetInternal(c.Internal).
		SetAuthorID(c.AuthorID).
		SetAuthorEmail(c.AuthorEmail).
		SetCreateTime(time.Now()).
		SetUpdateTime(time.Now()).
		Save(ctx)
	if err != nil {
		r.log.Errorf("create comment failed: %s", err.Error())
		return nil, err
	}
	return e, nil
}

func (r *CommentRepo) ListByTicket(ctx context.Context, tenantID uint32, ticketID string) ([]*ent.TicketComment, error) {
	return r.entClient.Client().TicketComment.Query().
		Where(ticketcomment.TenantIDEQ(tenantID), ticketcomment.TicketIDEQ(ticketID)).
		Order(ent.Asc(ticketcomment.FieldCreateTime)).
		All(ctx)
}

func (r *CommentRepo) Delete(ctx context.Context, tenantID uint32, id string) error {
	_, err := r.entClient.Client().TicketComment.Delete().
		Where(ticketcomment.IDEQ(id), ticketcomment.TenantIDEQ(tenantID)).
		Exec(ctx)
	return err
}
