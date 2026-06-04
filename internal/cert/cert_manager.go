package cert

import (
	commonCert "github.com/go-tangra/go-tangra-common/cert"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
)

type CertManager = commonCert.CertManager

func NewCertManager(ctx *bootstrap.Context) (*CertManager, error) {
	return commonCert.NewCertManager(ctx, "TICKET")
}
