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

// TicketTag is a label or category that can be attached to tickets
// (many-to-many). kind distinguishes a free-form TAG from a CATEGORY.
type TicketTag struct {
	ent.Schema
}

func (TicketTag) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "ticket_tags"},
		entsql.WithComments(true),
	}
}

func (TicketTag) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Unique().Immutable().Comment("UUID primary key"),
		field.String("name").NotEmpty(),
		field.String("kind").Default("TAG").Comment("TAG or CATEGORY"),
		field.String("color").Optional(),
		field.String("description").Optional(),
	}
}

func (TicketTag) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("tickets", Ticket.Type).Ref("tags"),
	}
}

func (TicketTag) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
		mixin.TenantID[uint32]{},
	}
}

func (TicketTag) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("tenant_id", "kind", "name").Unique(),
	}
}
