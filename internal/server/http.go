package server

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	kratosHttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-common/viewer"
	"github.com/go-tangra/go-tangra-ticket/cmd/server/assets"
	"github.com/go-tangra/go-tangra-ticket/internal/data"
	"github.com/go-tangra/go-tangra-ticket/internal/webhook"
)

// NewHTTPServer builds the public HTTP server. It hosts the inbound iris
// email webhook, the attachment download endpoint, and the embedded
// module-federation frontend. The module's REST API is served by the admin
// gateway via gRPC transcoding, so it is not registered here.
func NewHTTPServer(
	ctx *bootstrap.Context,
	iris *webhook.IrisHandler,
	attach *data.AttachmentRepo,
	storage *data.StorageClient,
) *kratosHttp.Server {
	l := ctx.NewLoggerHelper("ticket/http")

	bind := os.Getenv("HTTP_PUBLIC_BIND")
	if bind == "" {
		bind = ":10801"
	}

	srv := kratosHttp.NewServer(
		kratosHttp.Address(bind),
		kratosHttp.Timeout(60*time.Second),
	)

	// Inbound iris email webhook — token-protected, bypasses gateway auth.
	srv.HandleFunc("/webhooks/iris", iris.Handle)

	// Attachment download: GET /attachments/{id}. Reached via the admin
	// gateway asset proxy (/modules/ticket/attachments/{id}). The id is an
	// unguessable UUID.
	srv.HandleFunc("/attachments/", attachmentHandler(ctx, attach, storage))

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

func attachmentHandler(ctx *bootstrap.Context, attach *data.AttachmentRepo, storage *data.StorageClient) http.HandlerFunc {
	l := ctx.NewLoggerHelper("ticket/http/attachments")
	return func(w http.ResponseWriter, r *http.Request) {
		if attach == nil || storage == nil {
			http.Error(w, "attachments not available", http.StatusServiceUnavailable)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/attachments/")
		id = strings.Trim(id, "/")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}

		// System viewer: ent privacy requires it; access is gated by the
		// unguessable attachment UUID.
		rctx := viewer.NewSystemViewerContext(r.Context())
		a, err := attach.GetByID(rctx, id)
		if err != nil || a == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		obj, err := storage.Open(rctx, a.StorageKey)
		if err != nil {
			l.Warnf("open attachment %s (%s): %v", id, a.StorageKey, err)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer obj.Close()

		ctype := a.ContentType
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ctype)
		disposition := "attachment"
		if a.Inline {
			disposition = "inline"
		}
		fn := a.Filename
		if fn == "" {
			fn = "attachment"
		}
		w.Header().Set("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(fn, `"`, "")+`"`)
		if _, err := io.Copy(w, obj); err != nil {
			l.Warnf("stream attachment %s: %v", id, err)
		}
	}
}
