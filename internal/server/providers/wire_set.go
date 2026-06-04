//go:build wireinject
// +build wireinject

package providers

import (
	"github.com/google/wire"

	"github.com/go-tangra/go-tangra-ticket/internal/cert"
	"github.com/go-tangra/go-tangra-ticket/internal/server"
	"github.com/go-tangra/go-tangra-ticket/internal/webhook"
)

var ProviderSet = wire.NewSet(
	cert.NewCertManager,
	webhook.NewIrisHandler,
	server.NewGRPCServer,
	server.NewHTTPServer,
)
