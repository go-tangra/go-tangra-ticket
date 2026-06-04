package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/tx7do/go-crud/entgo/mixin"
)

// TicketRule auto-applies tags/categories to inbound tickets. Conditions are
// a Cloudflare-style structured set (field/operator/value) combined with
// match=ALL|ANY, compiled to CEL and evaluated during mail parse.
type TicketRule struct {
	ent.Schema
}

func (TicketRule) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "ticket_rules"},
		entsql.WithComments(true),
	}
}

func (TicketRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Unique().Immutable().Comment("UUID primary key"),
		field.String("name").NotEmpty(),
		field.Bool("enabled").Default(true),
		field.Int("sort_order").Default(0).Comment("Evaluation order (lower first)"),
		field.String("match").Default("ALL").Comment("ALL (AND) or ANY (OR)"),
		field.Text("conditions").Optional().Comment("JSON: [{field,operator,value}]"),
		field.Text("expression").Optional().Comment("Optional raw CEL expression (overrides conditions)"),
		field.String("tag_kind").Default("TAG").Comment("Legacy single tag action: kind (TAG or CATEGORY)"),
		field.Text("tag_names").Optional().Comment("Legacy single tag action: JSON tag names"),
		field.Text("actions").Optional().Comment("JSON: [{type,tagKind,tagNames,assigneeId,status,priority}] applied when the rule matches"),
	}
}

func (TicketRule) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
		mixin.TenantID[uint32]{},
	}
}

func (TicketRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tenant_id"),
		index.Fields("tenant_id", "enabled"),
	}
}
