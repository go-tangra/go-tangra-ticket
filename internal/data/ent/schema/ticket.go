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

// Ticket is a support request. Most tickets originate from an inbound
// email forwarded by iris/KumoMTA; some are created manually by agents.
type Ticket struct {
	ent.Schema
}

func (Ticket) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "ticket_tickets"},
		entsql.WithComments(true),
	}
}

func (Ticket) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			NotEmpty().
			Unique().
			Immutable().
			Comment("UUID primary key"),
		field.String("external_id").
			Optional().
			Comment("iris/RFC822 Message-Id for dedup"),
		field.String("subject").
			NotEmpty(),
		field.Text("description").
			Optional(),
		field.String("status").
			Default("TICKET_STATUS_OPEN").
			Comment("Lifecycle status"),
		field.String("priority").
			Default("TICKET_PRIORITY_NORMAL"),
		field.String("source").
			Default("manual").
			Comment("Origin: iris, manual"),
		field.String("requester_email").
			Optional(),
		field.String("requester_name").
			Optional(),
		field.String("recipient").
			Optional().
			Comment("Support address the mail was delivered to"),
		field.Uint32("assignee_id").
			Default(0).
			Comment("Internal user the ticket is assigned to (0 = unassigned)"),
	}
}

func (Ticket) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("comments", TicketComment.Type),
	}
}

func (Ticket) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.CreateBy{},
		mixin.Time{},
		mixin.TenantID[uint32]{},
	}
}

func (Ticket) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("tenant_id", "status"),
		index.Fields("tenant_id", "assignee_id"),
		// External id is unique per tenant when present (dedup inbound mail).
		index.Fields("tenant_id", "external_id").Unique(),
	}
}
