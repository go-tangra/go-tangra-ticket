//go:build wireinject
// +build wireinject

package providers

import (
	"github.com/google/wire"

	"github.com/go-tangra/go-tangra-ticket/internal/client"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
)

var ProviderSet = wire.NewSet(
	data.NewRedisClient,
	data.NewEntClient,
	data.NewRegistrationClient,
	data.NewModuleDialer,
	data.NewTicketRepo,
	data.NewCommentRepo,
	data.NewAttachmentRepo,
	data.NewStorageClient,
	data.NewTagRepo,
	data.NewRuleRepo,
	client.NewAdminClient,
)
