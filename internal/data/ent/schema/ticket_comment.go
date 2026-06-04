package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/tx7do/go-crud/entgo/mixin"
)

// TicketComment is a reply or internal note attached to a ticket.
type TicketComment struct {
	ent.Schema
}

func (TicketComment) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "ticket_comments"},
		entsql.WithComments(true),
	}
}

func (TicketComment) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			NotEmpty().
			Unique().
			Immutable().
			Comment("UUID primary key"),
		field.String("ticket_id").
			NotEmpty().
			Comment("Owning ticket UUID"),
		field.Text("body").
			NotEmpty(),
		field.Bool("internal").
			Default(false).
			Comment("Internal note (agents only) vs public reply"),
		field.Uint32("author_id").
			Default(0).
			Comment("Internal user author (0 if external)"),
		field.String("author_email").
			Optional().
			Comment("External requester email when the reply came by mail"),
		field.String("message_id").
			Optional().
			Comment("RFC822 Message-Id this comment was sent/received as (for threading)"),
	}
}

func (TicketComment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("ticket", Ticket.Type).
			Ref("comments").
			Field("ticket_id").
			Unique().
			Required(),
	}
}

func (TicketComment) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
		mixin.TenantID[uint32]{},
	}
}

func (TicketComment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("ticket_id"),
		index.Fields("tenant_id", "message_id"),
	}
}
