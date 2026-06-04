package service

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	ticketpb "github.com/go-tangra/go-tangra-ticket/gen/go/ticket/service/v1"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent"
	"github.com/go-tangra/go-tangra-ticket/internal/rules"
)

func u32(p *uint32) uint32 {
	if p == nil {
		return 0
	}
	return *p
}

func ts(p *time.Time) *timestamppb.Timestamp {
	if p == nil || p.IsZero() {
		return nil
	}
	return timestamppb.New(*p)
}

func statusEnum(s string) ticketpb.TicketStatus {
	return ticketpb.TicketStatus(ticketpb.TicketStatus_value[s])
}

func priorityEnum(s string) ticketpb.TicketPriority {
	return ticketpb.TicketPriority(ticketpb.TicketPriority_value[s])
}

// priorityName converts a proto priority enum to its stored string name,
// returning "" for UNSPECIFIED so the repo applies its default.
func priorityName(p ticketpb.TicketPriority) string {
	if p == ticketpb.TicketPriority_TICKET_PRIORITY_UNSPECIFIED {
		return ""
	}
	return p.String()
}

func ticketToProto(e *ent.Ticket, commentCount int32, assigneeName string) *ticketpb.Ticket {
	if e == nil {
		return nil
	}
	out := &ticketpb.Ticket{
		Id:             e.ID,
		TenantId:       u32(e.TenantID),
		ExternalId:     e.ExternalID,
		Subject:        e.Subject,
		Description:    e.Description,
		BodyHtml:       e.BodyHTML,
		Status:         statusEnum(e.Status),
		Priority:       priorityEnum(e.Priority),
		Source:         e.Source,
		RequesterEmail: e.RequesterEmail,
		RequesterName:  e.RequesterName,
		Recipient:      e.Recipient,
		AssigneeId:     e.AssigneeID,
		AssigneeName:   assigneeName,
		CommentCount:   commentCount,
		CreateBy:       u32(e.CreateBy),
		CreateTime:     ts(e.CreateTime),
		UpdateTime:     ts(e.UpdateTime),
	}
	for _, tg := range e.Edges.Tags {
		out.Tags = append(out.Tags, tagToProto(tg))
	}
	return out
}

func tagToProto(e *ent.TicketTag) *ticketpb.TicketTag {
	if e == nil {
		return nil
	}
	return &ticketpb.TicketTag{
		Id:          e.ID,
		TenantId:    u32(e.TenantID),
		Name:        e.Name,
		Kind:        e.Kind,
		Color:       e.Color,
		Description: e.Description,
		CreateTime:  ts(e.CreateTime),
	}
}

func ruleToProto(e *ent.TicketRule) *ticketpb.TicketRule {
	if e == nil {
		return nil
	}
	r := &ticketpb.TicketRule{
		Id:         e.ID,
		TenantId:   u32(e.TenantID),
		Name:       e.Name,
		Enabled:    e.Enabled,
		SortOrder:  int32(e.SortOrder),
		Match:      e.Match,
		Expression: e.Expression,
		TagKind:    e.TagKind,
		TagNames:   jsonStrings(e.TagNames),
		CreateTime: ts(e.CreateTime),
		UpdateTime: ts(e.UpdateTime),
	}
	var conds []rules.Condition
	if e.Conditions != "" {
		_ = json.Unmarshal([]byte(e.Conditions), &conds)
	}
	for _, c := range conds {
		r.Conditions = append(r.Conditions, &ticketpb.RuleCondition{
			Field: c.Field, Operator: c.Operator, Value: c.Value,
		})
	}
	return r
}

func jsonStrings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func attachmentToProto(e *ent.TicketAttachment) *ticketpb.TicketAttachment {
	if e == nil {
		return nil
	}
	return &ticketpb.TicketAttachment{
		Id:          e.ID,
		TicketId:    e.TicketID,
		Filename:    e.Filename,
		ContentType: e.ContentType,
		Size:        e.Size,
		ContentId:   e.ContentID,
		Inline:      e.Inline,
		// Path served by the module HTTP server via the admin gateway proxy.
		DownloadUrl: "/modules/ticket/attachments/" + e.ID,
		CreateTime:  ts(e.CreateTime),
	}
}

func commentToProto(e *ent.TicketComment, authorName string) *ticketpb.TicketComment {
	if e == nil {
		return nil
	}
	return &ticketpb.TicketComment{
		Id:          e.ID,
		TenantId:    u32(e.TenantID),
		TicketId:    e.TicketID,
		Body:        e.Body,
		Internal:    e.Internal,
		AuthorId:    e.AuthorID,
		AuthorName:  authorName,
		AuthorEmail: e.AuthorEmail,
		CreateTime:  ts(e.CreateTime),
	}
}
