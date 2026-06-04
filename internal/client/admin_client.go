package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	adminstubpb "github.com/go-tangra/go-tangra-common/gen/go/common/admin_stub/v1"
	paginationV1 "github.com/tx7do/go-crud/api/gen/go/pagination/v1"

	"github.com/go-tangra/go-tangra-ticket/internal/cert"
)

// AdminClient calls the admin-service gRPC API for user listing (assignees).
type AdminClient struct {
	log  *log.Helper
	conn *grpc.ClientConn
}

func NewAdminClient(ctx *bootstrap.Context, certManager *cert.CertManager) (*AdminClient, func(), error) {
	l := ctx.NewLoggerHelper("ticket/client/admin")

	endpoint := os.Getenv("ADMIN_GRPC_ENDPOINT")
	if endpoint == "" {
		l.Warn("ADMIN_GRPC_ENDPOINT not set, falling back to localhost:7787 (dev only)")
		endpoint = "localhost:7787"
	}

	var transportCreds grpc.DialOption
	if certManager != nil && certManager.IsTLSEnabled() {
		tlsCreds, err := loadAdminClientTLS(certManager, l)
		if err != nil {
			l.Warnf("Failed to load mTLS credentials for admin client: %v, falling back to insecure", err)
			transportCreds = grpc.WithTransportCredentials(insecure.NewCredentials())
		} else {
			transportCreds = grpc.WithTransportCredentials(tlsCreds)
			l.Info("Admin gRPC client configured with mTLS")
		}
	} else {
		transportCreds = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.NewClient(endpoint, transportCreds)
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {
		if conn != nil {
			conn.Close()
		}
	}

	l.Infof("Admin gRPC client configured for endpoint: %s", endpoint)
	return &AdminClient{log: l, conn: conn}, cleanup, nil
}

func loadAdminClientTLS(certManager *cert.CertManager, l *log.Helper) (credentials.TransportCredentials, error) {
	certsDir := os.Getenv("CERTS_DIR")
	if certsDir == "" {
		certsDir = "/app/certs"
	}

	caCertPath := filepath.Join(certsDir, "ca", "ca.crt")
	clientCertPath := filepath.Join(certsDir, "ticket", "ticket.crt")
	clientKeyPath := filepath.Join(certsDir, "ticket", "ticket.key")
	if _, err := os.Stat(clientCertPath); os.IsNotExist(err) {
		clientCertPath = filepath.Join(certsDir, "ticket-server", "server.crt")
		clientKeyPath = filepath.Join(certsDir, "ticket-server", "server.key")
	}

	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert: %w", err)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}
	clientCert, err := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load client cert: %w", err)
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caCertPool,
		ServerName:   "admin-service",
		MinVersion:   tls.VersionTLS12,
	}), nil
}

// ListUsers calls admin.service.v1.UserService/List via gRPC.
func (c *AdminClient) ListUsers(ctx context.Context) (*adminstubpb.ListAdminUsersResponse, error) {
	noPaging := true
	req := &paginationV1.PagingRequest{NoPaging: &noPaging}

	resp := &adminstubpb.ListAdminUsersResponse{}
	if err := c.conn.Invoke(ctx, "/admin.service.v1.UserService/List", req, resp); err != nil {
		c.log.Errorf("Failed to list users from admin-service: %v", err)
		return nil, err
	}
	return resp, nil
}
