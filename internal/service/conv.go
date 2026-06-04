package service

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	ticketpb "github.com/go-tangra/go-tangra-ticket/gen/go/ticket/service/v1"
	"github.com/go-tangra/go-tangra-ticket/internal/data/ent"
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
	return &ticketpb.Ticket{
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
