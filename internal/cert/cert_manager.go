// Package cert is the module-side bridge to go-tangra-common's certificate
// bootstrap pipeline. NewCertManager runs cert.Ensure at every boot — when
// the local cert is valid + fresh it's a fast no-op; when missing/expired or
// inside the renewal window it dials LCM (LCM_BOOTSTRAP_ENDPOINT), signs a
// CSR, and writes the new cert to disk before returning, enabling mTLS.
package cert

import (
	"context"

	commonCert "github.com/go-tangra/go-tangra-common/cert"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
)

type CertManager = commonCert.CertManager

// NewCertManager bootstraps + loads the module's mTLS certificates.
// Required env: LCM_BOOTSTRAP_ENDPOINT, MODULE_BOOTSTRAP_SECRET,
// LCM_CA_FINGERPRINT (CERTS_DIR defaults to /app/certs).
func NewCertManager(ctx *bootstrap.Context) (*CertManager, error) {
	return commonCert.Ensure(context.Background(), commonCert.EnsureConfig{
		ModuleID: "ticket",
		Logger:   ctx.GetLogger(),
	})
}
