package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/tx7do/go-crud/entgo/mixin"
)

// TicketAttachment is a file extracted from an inbound email, stored in
// S3/RustFS and referenced by the ticket.
type TicketAttachment struct {
	ent.Schema
}

func (TicketAttachment) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "ticket_attachments"},
		entsql.WithComments(true),
	}
}

func (TicketAttachment) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Unique().Immutable().Comment("UUID primary key"),
		field.String("ticket_id").NotEmpty().Comment("Owning ticket UUID"),
		field.String("filename").Optional(),
		field.String("content_type").Optional(),
		field.Int64("size").Default(0),
		field.String("storage_key").NotEmpty().Comment("Object key in S3/RustFS"),
		field.String("content_id").Optional().Comment("MIME Content-ID for inline images"),
		field.Bool("inline").Default(false),
	}
}

func (TicketAttachment) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
		mixin.TenantID[uint32]{},
	}
}

func (TicketAttachment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("ticket_id"),
	}
}
