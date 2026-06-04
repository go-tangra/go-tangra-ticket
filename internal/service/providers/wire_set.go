//go:build wireinject
// +build wireinject

package providers

import (
	"github.com/google/wire"

	"github.com/go-tangra/go-tangra-ticket/internal/metrics"
	"github.com/go-tangra/go-tangra-ticket/internal/service"
)

var ProviderSet = wire.NewSet(
	metrics.NewCollector,
	service.NewTicketService,
	service.NewCommentService,
	service.NewUserService,
)
