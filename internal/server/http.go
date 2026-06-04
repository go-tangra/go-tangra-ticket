package server

import (
	"io/fs"
	"net/http"
	"os"
	"time"

	kratosHttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-ticket/cmd/server/assets"
	"github.com/go-tangra/go-tangra-ticket/internal/webhook"
)

// NewHTTPServer builds the public HTTP server. It hosts the inbound iris
// email webhook and serves the embedded module-federation frontend. The
// module's REST API itself is served by the admin gateway via gRPC
// transcoding, so it is not registered here.
func NewHTTPServer(ctx *bootstrap.Context, iris *webhook.IrisHandler) *kratosHttp.Server {
	l := ctx.NewLoggerHelper("ticket/http")

	bind := os.Getenv("HTTP_PUBLIC_BIND")
	if bind == "" {
		bind = ":10801"
	}

	srv := kratosHttp.NewServer(
		kratosHttp.Address(bind),
		kratosHttp.Timeout(30*time.Second),
	)

	// Inbound iris email webhook — token-protected, bypasses gateway auth.
	srv.HandleFunc("/webhooks/iris", iris.Handle)

	// Embedded module-federation frontend (remoteEntry.js + assets).
	if sub, err := fs.Sub(assets.FrontendDist, "frontend-dist"); err == nil {
		srv.HandlePrefix("/", http.FileServer(http.FS(sub)))
		l.Info("serving embedded frontend assets")
	} else {
		l.Warnf("frontend assets unavailable: %v", err)
	}

	l.Infof("HTTP server listening on %s", bind)
	return srv
}
