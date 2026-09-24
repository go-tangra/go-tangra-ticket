package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra/v4"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/blob"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/config"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/secrets"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/stream"
)

const appTenant = "11111111-1111-7111-8111-111111111111"

type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, token string) (authclient.Identity, error) {
	if token == "agent" {
		return authclient.Identity{UserID: "u1", TenantID: appTenant}, nil
	}
	return authclient.Identity{}, errors.New("unauthenticated")
}

func testConfig() config.Config {
	c := config.Default()
	c.ServiceName, c.TrustDomain, c.Env = "ticket", "example.org", "dev"
	c.DB.DSN = "postgres://unused"
	c.Valkey.Addresses, c.Valkey.AllowPlaintext = []string{"127.0.0.1:1"}, true
	c.Server.GRPCAddr, c.Server.HTTPAddr, c.Admin.Addr = "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0"
	c.Discovery.Static = map[string][]string{"lcm": {"127.0.0.1:1"}, "auth": {"127.0.0.1:1"}, "gateway": {"127.0.0.1:1"}, "warden": {"127.0.0.1:1"}}
	c.ObjectStore.Endpoint, c.ObjectStore.Bucket = "127.0.0.1:1", "ticket"
	c.Inbound = config.Inbound{Addr: "127.0.0.1:0", InsecureDev: true, RelayTokenRef: "warden:relay", MaxBodyBytes: 1 << 20, MaxAttachmentBytes: 1 << 20, MaxParts: 10, MaxDepth: 5}
	c.Gateway.Service, c.Gateway.Issuer = "gateway", "https://localhost:8443"
	return c
}

func options() Options {
	dir := agents.NewFake()
	dir.Add(appTenant, agents.User{ID: "u1", Name: "Ada"}, true)
	return Options{
		KEK: make([]byte, 32), Verifier: fakeVerifier{}, Checker: authz.Static{"u1": {authz.TicketsRead}},
		Blob: blob.NewFake(), Repo: memstore.New(), Agents: dir, Secrets: &secrets.Fake{Relay: "relay-token"},
		Stream: stream.NewMemory(),
		Freya:  []freya.Option{freya.WithInsecureLocalDev(), freya.WithAllowAllPolicy()},
	}
}

func TestBuildWiresTheService(t *testing.T) {
	a, err := Build(context.Background(), testConfig(), options())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer a.Close()
	if a.Freya == nil || a.Repo == nil || a.Env == nil || a.HTTP == nil || a.Hub == nil || a.Audit == nil || a.Blob == nil ||
		a.Metrics == nil || a.Inbound == nil || a.Secrets == nil || a.Agents == nil || a.Events == nil {
		t.Fatalf("app not fully wired: %+v", a)
	}
	do := func(path, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://localhost"+path, nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		a.HTTP.Handler().ServeHTTP(w, r)
		return w
	}
	if w := do("/api/ticket/v1/health", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"relay_token":"ok"`) {
		t.Fatalf("health: %d %s", w.Code, w.Body)
	}
	if w := do("/api/ticket/v1/tickets", ""); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := do("/api/ticket/v1/tickets", "agent"); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("tickets list: %d %s", w.Code, w.Body)
	}
	if w := do("/api/ticket/v1/assignable-users", "agent"); w.Code != 200 || !strings.Contains(w.Body.String(), `"Ada"`) {
		t.Fatalf("assignable users: %d %s", w.Code, w.Body)
	}
	if w := do("/api/ticket/v1/tags", "agent"); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("tags list (US5): %d %s", w.Code, w.Body)
	}
	if a.Tickets == nil {
		t.Fatal("tickets service not wired")
	}
	if w := do("/api/ticket/v1/stats", "agent"); w.Code != 403 {
		t.Fatalf("permission not enforced: %d", w.Code)
	}
	// The module's counters render on the framework's metrics handler.
	a.Metrics.Inbound("created")
	rec := httptest.NewRecorder()
	a.Freya.Metrics().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `ticket_inbound_total{outcome="created"} 1`) {
		t.Fatalf("module metrics not exposed:\n%s", rec.Body)
	}
	// A failing relay-token lookup degrades health.
	a.Secrets = &secrets.Fake{}
	if w := do("/api/ticket/v1/health", ""); !strings.Contains(w.Body.String(), `"relay_token":"unavailable"`) {
		t.Fatalf("degraded health: %s", w.Body)
	}
	a.Close()
	a.Close() // idempotent
}

func TestRunServesTheInboundEdge(t *testing.T) {
	o := options()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	o.InboundListener = lis
	o.Inbound = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	a, err := Build(context.Background(), testConfig(), o)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	base := "http://" + lis.Addr().String()
	var res *http.Response
	for i := 0; i < 50; i++ {
		if res, err = http.Get(base + "/healthz"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("edge healthz: %v", err)
	}
	_ = res.Body.Close()
	res, err = http.Post(base+"/inbound/mail", "message/rfc822", strings.NewReader("Subject: x\r\n\r\nbody"))
	if err != nil || res.StatusCode != http.StatusAccepted {
		t.Fatalf("edge mail: %v %v", err, res)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestBuildRefusesUnusableEdgeCertificate(t *testing.T) {
	c := testConfig()
	c.Inbound.InsecureDev = false
	c.Inbound.TLSCertFile, c.Inbound.TLSKeyFile = "/nonexistent.crt", "/nonexistent.key"
	if _, err := Build(context.Background(), c, options()); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("build accepted a missing edge certificate: %v", err)
	}
	c = testConfig()
	o := options()
	o.KEK = []byte("short")
	if _, err := Build(context.Background(), c, o); err == nil {
		t.Fatal("bad KEK accepted")
	}
}

type fakeAuthz struct {
	authv1.AuthorizationClient
	allow bool
	err   error
	got   *authv1.CheckRequest
}

func (f *fakeAuthz) Check(_ context.Context, in *authv1.CheckRequest, _ ...grpc.CallOption) (*authv1.CheckResponse, error) {
	f.got = in
	if f.err != nil {
		return nil, f.err
	}
	return &authv1.CheckResponse{Allowed: f.allow}, nil
}

func TestAuthPerms(t *testing.T) {
	f := &fakeAuthz{allow: true}
	p := AuthPerms{Client: f}
	if !p.Has(context.Background(), appTenant, "u1", authz.TicketsManage) || f.got.GetResource() != "tickets" || f.got.GetAction() != "manage" {
		t.Fatalf("check = %+v", f.got)
	}
	f.allow = false
	if p.Has(context.Background(), appTenant, "u1", authz.TicketsRead) {
		t.Fatal("denied allowed")
	}
	f.err = errors.New("down")
	if p.Has(context.Background(), appTenant, "u1", authz.TicketsRead) {
		t.Fatal("error must be a no")
	}
	if (AuthPerms{}).Has(context.Background(), appTenant, "u1", authz.TicketsRead) || p.Has(context.Background(), appTenant, "u1", "bogus") {
		t.Fatal("nil client / malformed permission")
	}
}
