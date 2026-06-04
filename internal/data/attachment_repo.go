package data

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	entCrud "github.com/tx7do/go-crud/entgo"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-ticket/internal/data/ent"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent/ticketattachment"
)

type AttachmentRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *log.Helper
}

func NewAttachmentRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *AttachmentRepo {
	return &AttachmentRepo{
		log:       ctx.NewLoggerHelper("ticket/repo/attachment"),
		entClient: entClient,
	}
}

type NewAttachment struct {
	TenantID    uint32
	TicketID    string
	Filename    string
	ContentType string
	Size        int64
	StorageKey  string
	ContentID   string
	Inline      bool
}

func (r *AttachmentRepo) Create(ctx context.Context, a NewAttachment) (*ent.TicketAttachment, error) {
	return r.entClient.Client().TicketAttachment.Create().
		SetID(uuid.NewString()).
		SetTenantID(a.TenantID).
		SetTicketID(a.TicketID).
		SetFilename(a.Filename).
		SetContentType(a.ContentType).
		SetSize(a.Size).
		SetStorageKey(a.StorageKey).
		SetContentID(a.ContentID).
		SetInline(a.Inline).
		SetCreateTime(time.Now()).
		SetUpdateTime(time.Now()).
		Save(ctx)
}

func (r *AttachmentRepo) ListByTicket(ctx context.Context, tenantID uint32, ticketID string) ([]*ent.TicketAttachment, error) {
	return r.entClient.Client().TicketAttachment.Query().
		Where(ticketattachment.TenantIDEQ(tenantID), ticketattachment.TicketIDEQ(ticketID)).
		Order(ent.Asc(ticketattachment.FieldCreateTime)).
		All(ctx)
}

// GetByID returns an attachment by id without tenant scoping — used by the
// download endpoint, which is reached via an unguessable UUID.
func (r *AttachmentRepo) GetByID(ctx context.Context, id string) (*ent.TicketAttachment, error) {
	e, err := r.entClient.Client().TicketAttachment.Query().
		Where(ticketattachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

// ListKeysByTicket returns the storage keys for a ticket (for cleanup on delete).
func (r *AttachmentRepo) ListKeysByTicket(ctx context.Context, tenantID uint32, ticketID string) ([]string, error) {
	rows, err := r.ListByTicket(ctx, tenantID, ticketID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(rows))
	for _, a := range rows {
		keys = append(keys, a.StorageKey)
	}
	return keys, nil
}

func (r *AttachmentRepo) DeleteByTicket(ctx context.Context, tenantID uint32, ticketID string) error {
	_, err := r.entClient.Client().TicketAttachment.Delete().
		Where(ticketattachment.TenantIDEQ(tenantID), ticketattachment.TicketIDEQ(ticketID)).
		Exec(ctx)
	return err
}
