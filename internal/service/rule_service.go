package service

import (
	"context"
	"encoding/json"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/protobuf/types/known/emptypb"

	ticketpb "github.com/go-tangra/go-tangra-ticket/gen/go/ticket/service/v1"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
	"github.com/go-tangra/go-tangra-ticket/internal/rules"
)

type RuleService struct {
	ticketpb.UnimplementedTicketRuleServiceServer

	log    *log.Helper
	repo   *data.RuleRepo
	engine *rules.Engine
}

func NewRuleService(ctx *bootstrap.Context, repo *data.RuleRepo, engine *rules.Engine) *RuleService {
	return &RuleService{log: ctx.NewLoggerHelper("ticket/service/rule"), repo: repo, engine: engine}
}

func (s *RuleService) ListRules(ctx context.Context, _ *ticketpb.ListRulesRequest) (*ticketpb.ListRulesResponse, error) {
	rows, err := s.repo.List(ctx, getTenantID(ctx))
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to list rules")
	}
	out := &ticketpb.ListRulesResponse{Rules: make([]*ticketpb.TicketRule, 0, len(rows))}
	for _, e := range rows {
		out.Rules = append(out.Rules, ruleToProto(e))
	}
	return out, nil
}

func (s *RuleService) GetRule(ctx context.Context, req *ticketpb.GetRuleRequest) (*ticketpb.GetRuleResponse, error) {
	e, err := s.repo.Get(ctx, getTenantID(ctx), req.Id)
	if err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to get rule")
	}
	if e == nil {
		return nil, ticketpb.ErrorNotFound("rule not found")
	}
	return &ticketpb.GetRuleResponse{Rule: ruleToProto(e)}, nil
}

func (s *RuleService) CreateRule(ctx context.Context, req *ticketpb.CreateRuleRequest) (*ticketpb.CreateRuleResponse, error) {
	d, err := s.toRuleData(getTenantID(ctx), req.Rule)
	if err != nil {
		return nil, err
	}
	e, cerr := s.repo.Create(ctx, d)
	if cerr != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to create rule")
	}
	return &ticketpb.CreateRuleResponse{Rule: ruleToProto(e)}, nil
}

func (s *RuleService) UpdateRule(ctx context.Context, req *ticketpb.UpdateRuleRequest) (*ticketpb.UpdateRuleResponse, error) {
	tenantID := getTenantID(ctx)
	d, err := s.toRuleData(tenantID, req.Rule)
	if err != nil {
		return nil, err
	}
	e, uerr := s.repo.Update(ctx, tenantID, req.Id, d)
	if uerr != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to update rule")
	}
	if e == nil {
		return nil, ticketpb.ErrorNotFound("rule not found")
	}
	return &ticketpb.UpdateRuleResponse{Rule: ruleToProto(e)}, nil
}

func (s *RuleService) DeleteRule(ctx context.Context, req *ticketpb.DeleteRuleRequest) (*emptypb.Empty, error) {
	if err := s.repo.Delete(ctx, getTenantID(ctx), req.Id); err != nil {
		return nil, ticketpb.ErrorDatabaseError("failed to delete rule")
	}
	return &emptypb.Empty{}, nil
}

// toRuleData validates the rule (compiles its CEL) and serializes the
// structured fields to JSON for storage.
func (s *RuleService) toRuleData(tenantID uint32, in *ticketpb.RuleInput) (data.RuleData, error) {
	if in == nil || in.Name == "" {
		return data.RuleData{}, ticketpb.ErrorBadRequest("rule name is required")
	}
	conds := make([]rules.Condition, 0, len(in.Conditions))
	for _, c := range in.Conditions {
		conds = append(conds, rules.Condition{Field: c.Field, Operator: c.Operator, Value: c.Value})
	}

	// Validate the effective expression before saving.
	expr := in.Expression
	if expr == "" {
		expr = rules.BuildExpression(in.Match, conds)
	}
	if s.engine != nil {
		if err := s.engine.Validate(expr); err != nil {
			return data.RuleData{}, ticketpb.ErrorBadRequest("invalid rule expression: %s", err.Error())
		}
	}

	condJSON, _ := json.Marshal(conds)
	tagJSON, _ := json.Marshal(in.TagNames)
	match := in.Match
	if match == "" {
		match = "ALL"
	}
	return data.RuleData{
		TenantID:   tenantID,
		Name:       in.Name,
		Enabled:    in.Enabled,
		SortOrder:  int(in.SortOrder),
		Match:      match,
		Conditions: string(condJSON),
		Expression: in.Expression,
		TagKind:    in.TagKind,
		TagNames:   string(tagJSON),
	}, nil
}
