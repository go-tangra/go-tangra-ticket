//go:build wireinject
// +build wireinject

package providers

import (
	"github.com/google/wire"

	"github.com/go-tangra/go-tangra-ticket/internal/mailer"
	"github.com/go-tangra/go-tangra-ticket/internal/metrics"
	"github.com/go-tangra/go-tangra-ticket/internal/rules"
	"github.com/go-tangra/go-tangra-ticket/internal/service"
)

var ProviderSet = wire.NewSet(
	rules.NewEngine,
	mailer.NewMailer,
	metrics.NewCollector,
	service.NewTicketService,
	service.NewCommentService,
	service.NewUserService,
	service.NewTagService,
	service.NewRuleService,
)
