// Package app wires the ticket service: configuration -> Freya runtime (mesh
// identity, admin listener) -> store/KEK/object store/secrets/agents/events/
// metrics -> the mesh HTTP surface (reached only through the gateway), the
// ticket.v1 gRPC surface (module-to-module) and the SEPARATE off-mesh inbound
// mail edge, plus gateway registration and permission seeding. It refuses to
// start without a KEK, a store, an object store and a relay-token reference
// (config.Validate), and never starts the inbound edge in plaintext unless the
// development opt-out is set (Constitution I).
//
// Domain services are added by the user-story phases (US1 tickets, US2 inbound
// mail + mailboxes, US3 conversation + outbound mail, US4 triage rules, US5
// tags); routes of services not yet wired answer 501.
package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-freya/freya"
	"github.com/go-freya/freya/services/lcm/pkg/lcmidentity"

	authv1 "github.com/go-freya/freya/services/auth/api/proto/auth/v1"
	"github.com/go-freya/freya/services/auth/pkg/authclient"
	"github.com/go-freya/freya/services/gateway/pkg/gatewayclient"

	"github.com/go-freya/freya/services/ticket/internal/agents"
	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/backup"
	"github.com/go-freya/freya/services/ticket/internal/blob"
	"github.com/go-freya/freya/services/ticket/internal/comments"
	"github.com/go-freya/freya/services/ticket/internal/config"
	"github.com/go-freya/freya/services/ticket/internal/events"
	"github.com/go-freya/freya/services/ticket/internal/grpcapi"
	"github.com/go-freya/freya/services/ticket/internal/httpapi"
	"github.com/go-freya/freya/services/ticket/internal/inbound"
	"github.com/go-freya/freya/services/ticket/internal/mailboxes"
	"github.com/go-freya/freya/services/ticket/internal/mailer"
	"github.com/go-freya/freya/services/ticket/internal/mailparse"
	"github.com/go-freya/freya/services/ticket/internal/metrics"
	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/repo/repodb"
	"github.com/go-freya/freya/services/ticket/internal/rules"
	"github.com/go-freya/freya/services/ticket/internal/sealed"
	"github.com/go-freya/freya/services/ticket/internal/secrets"
	"github.com/go-freya/freya/services/ticket/internal/stats"
	"github.com/go-freya/freya/services/ticket/internal/store"
	"github.com/go-freya/freya/services/ticket/internal/stream"
	"github.com/go-freya/freya/services/ticket/internal/stream/valkeykv"
	"github.com/go-freya/freya/services/ticket/internal/tags"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
	"github.com/go-freya/freya/services/ticket/pkg/ticketmanifest"
)

// Options override infrastructure (tests) and attach optional parts.
type Options struct {
	Logger   slog.Handler
	KEK      []byte
	Verifier httpapi.Verifier
	Checker  authz.Checker    // API-permission checker override (default: auth Authorization/Check)
	Blob     blob.Store       // object store override (tests: blob.NewFake())
	Repo     repo.Store       // store override (tests: memstore); skips the DB
	Agents   agents.Directory // assignable-user directory override
	Secrets  secrets.Source   // relay token / SMTP password override
	Stream   stream.Client    // event-bus client override (tests: stream.NewMemory())
	Inbound  http.Handler     // inbound mail handler override (default: the wired mail handler)
	Mailer   mailer.Sender    // outbound mail override (tests: &mailer.Fake{})
	Triage   inbound.Triage   // inbound rules hook override (default: the tenant's CEL rules, US4)
	Freya    []freya.Option
	Migrate  bool
	Remote   fs.FS // built federated UI remote (nil serves no remote)
	// InboundListener overrides the inbound edge listener (tests).
	InboundListener net.Listener
}

// App is the wired service.
type App struct {
	Cfg      config.Config
	Log      *slog.Logger
	Freya    *freya.App
	Store    *store.Store
	Repo     repo.Store
	Env      *sealed.Envelope
	Blob     blob.Store
	Audit    *audit.Writer
	Verifier httpapi.Verifier
	Checker  authz.Checker
	Agents   agents.Directory
	Secrets  secrets.Source
	Events   events.Publisher
	Metrics  *metrics.Metrics
	HTTP     *httpapi.Server
	Hub      *stream.Hub
	Inbound  *inbound.Server
	Mailer   mailer.Sender
	Tickets  *tickets.Service
	Comments *comments.Service
	Mailbox  *mailboxes.Service
	Tags     *tags.Service
	Rules    *rules.Service
	Engine   *rules.Engine
	Triage   inbound.Triage
	Mail     *inbound.Handler

	inboundLis net.Listener
	closers    []func()
	workers    []func(context.Context)
}

// Build wires the service.
func Build(ctx context.Context, cfg config.Config, o Options) (a *App, err error) {
	a = &App{Cfg: cfg}
	handler := o.Logger
	if handler == nil {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	a.Log = slog.New(handler)
	built := a
	defer func() { // release what was opened when wiring fails half-way
		if err != nil {
			built.Close()
		}
	}()

	if err = a.buildRuntime(ctx, cfg, handler, o.Freya); err != nil {
		return nil, err
	}
	if err = a.buildStorage(ctx, cfg, o); err != nil {
		return nil, err
	}
	if err = a.buildPeers(ctx, cfg, o); err != nil {
		return nil, err
	}
	if err = a.buildEvents(cfg, o.Stream); err != nil {
		return nil, err
	}
	if a.Metrics, err = metrics.New(a.Freya.Metrics().Meter(metrics.Scope)); err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}

	// Outbound mail (replies, acknowledgements): the relay password is read
	// from warden at send time; an empty smtp.host disables it.
	a.Mailer = o.Mailer
	if a.Mailer == nil {
		a.Mailer = mailer.New(mailer.Config{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, TLS: cfg.SMTP.TLS, AllowPlaintext: cfg.SMTP.AllowPlaintext,
			Username: cfg.SMTP.Username, Password: a.Secrets.SMTPPassword, Timeout: time.Duration(cfg.SMTP.TimeoutSeconds) * time.Second})
	}

	// Domain services.
	a.Tickets = tickets.New(tickets.Deps{Store: a.Repo, Agents: a.Agents, Events: a.Events, Audit: a.Audit,
		Metrics: a.Metrics, Blobs: a.Blob, Log: a.Log})
	a.Comments = comments.New(comments.Deps{Store: a.Repo, Mailer: a.Mailer, Tickets: a.Tickets, Agents: a.Agents, Events: a.Events,
		Audit: a.Audit, Metrics: a.Metrics, Blobs: a.Blob, Log: a.Log, MailDomain: cfg.SMTP.MailDomain})
	a.Mailbox = mailboxes.New(mailboxes.Deps{Store: a.Repo, Audit: a.Audit})
	a.Tags = tags.New(tags.Deps{Store: a.Repo, Audit: a.Audit})

	// Triage rules (CEL, cost-limited, per-message deadline; research D6).
	if a.Engine, err = rules.NewEngine(rules.Config{CostLimit: cfg.Rules.CostLimit,
		Timeout: time.Duration(cfg.Rules.EvalTimeoutMS) * time.Millisecond}); err != nil {
		return nil, err
	}
	a.Rules = rules.NewService(rules.Deps{Store: a.Repo, Engine: a.Engine, Agents: a.Agents, Audit: a.Audit, MaxRules: cfg.Rules.MaxRules})
	a.Triage = o.Triage
	if a.Triage == nil {
		a.Triage = rules.NewTriage(rules.TriageDeps{Engine: a.Engine, Store: a.Repo, Tickets: a.Tickets, Metrics: a.Metrics,
			Audit: a.Audit, Log: a.Log, MaxRules: cfg.Rules.MaxRules})
	}
	a.Mail = inbound.NewHandler(inbound.Deps{Store: a.Repo, Blobs: a.Blob, Secrets: a.Secrets, Tickets: a.Tickets, Audit: a.Audit,
		Events: a.Events, Metrics: a.Metrics, Log: a.Log, Triage: a.Triage, Mailer: a.Mailer, MailDomain: cfg.SMTP.MailDomain, Limits: mailparse.Limits{
			MaxBodyBytes: cfg.Inbound.MaxBodyBytes, MaxAttachmentBytes: cfg.Inbound.MaxAttachmentBytes,
			MaxParts: cfg.Inbound.MaxParts, MaxDepth: cfg.Inbound.MaxDepth}})

	// Mesh HTTP surface (reached only through the gateway).
	hopts := []httpapi.Option{httpapi.WithVerifier(a.Verifier), httpapi.WithChecker(a.Checker), httpapi.WithLogger(a.Log)}
	if o.Remote != nil {
		hopts = append(hopts, httpapi.WithRemote(o.Remote))
	}
	if a.HTTP, err = httpapi.NewHandler(a.Freya, hopts...); err != nil {
		return nil, err
	}
	a.HTTP.Register(httpapi.Deps{Hub: a.Hub, Health: a.health, Tickets: a.Tickets, Comments: a.Comments, Mailboxes: a.Mailbox,
		Rules: a.Rules, Tags: a.Tags, Stats: stats.New(a.Repo), Backup: backup.New(a.Repo, a.Blob, a.Audit)})
	a.Freya.HTTP().HandlePrefix("/", a.HTTP.Handler())

	// Service-to-service gRPC surface (ticket.v1), SPIFFE mTLS, not gateway-proxied.
	grpcapi.Register(a.Freya.GRPC(), grpcapi.Deps{Tickets: a.Tickets, Comments: a.Comments})

	// Off-mesh INBOUND MAIL EDGE: a separate listener for the mail relay.
	var mail http.Handler = a.Mail
	if o.Inbound != nil {
		mail = o.Inbound
	}
	a.Inbound = inbound.NewServer(inbound.ServerConfig{
		Addr: cfg.Inbound.Addr, CertFile: cfg.Inbound.TLSCertFile, KeyFile: cfg.Inbound.TLSKeyFile,
		Insecure: cfg.Inbound.InsecureDev, MaxBodyBytes: cfg.Inbound.MaxBodyBytes,
	}, mail, a.Log)
	if _, err = a.Inbound.TLSConfig(); err != nil { // refuse to start with an unusable edge certificate
		return nil, err
	}
	a.inboundLis = o.InboundListener
	a.workers = append(a.workers, a.serveInbound)
	return a, nil
}

func (a *App) buildRuntime(ctx context.Context, cfg config.Config, handler slog.Handler, extra []freya.Option) error {
	fopts := append([]freya.Option{freya.WithLogger(handler)}, extra...)
	if cfg.MeshEnroll.Enabled {
		raw, err := os.ReadFile(cfg.MeshEnroll.TokenFile)
		if err != nil {
			return fmt.Errorf("ticket: mesh enroll token: %w", err)
		}
		prov, err := lcmidentity.NewNet(ctx, lcmidentity.NetConfig{
			EnrollURL: cfg.MeshEnroll.EnrollURL, LCMGRPCTarget: cfg.MeshEnroll.LCMGRPCTarget,
			TenantID: cfg.MeshEnroll.TenantID, TrustDomain: cfg.Config.TrustDomain, ServiceName: cfg.Config.ServiceName,
			EnrollmentToken: strings.TrimSpace(string(raw)), Insecure: cfg.MeshEnroll.Insecure, StateFile: cfg.MeshEnroll.StateFile,
		})
		if err != nil {
			return fmt.Errorf("ticket: mesh enroll: %w", err)
		}
		a.closers = append(a.closers, func() { _ = prov.Close() })
		fopts = append(fopts, freya.WithIdentityProvider(prov))
	}
	f, err := freya.New(cfg.Config, fopts...)
	if err != nil {
		return err
	}
	a.Freya = f
	a.closers = append(a.closers, f.Close)
	return nil
}

func (a *App) buildStorage(ctx context.Context, cfg config.Config, o Options) (err error) {
	kek := o.KEK
	if len(kek) == 0 {
		if kek, err = sealed.LoadKEK(cfg.KEK.Source, cfg.KEK.Path, cfg.KEK.Env); err != nil {
			return fmt.Errorf("kek: %w", err)
		}
	}
	if a.Env, err = sealed.NewEnvelope(kek); err != nil {
		return err
	}
	a.Repo = o.Repo
	if a.Repo == nil {
		if o.Migrate {
			mdsn := cfg.DB.MigrateDSN
			if mdsn == "" {
				mdsn = cfg.DB.DSN
			}
			if err = store.Migrate(ctx, mdsn); err != nil {
				return err
			}
		}
		if a.Store, err = store.Open(ctx, cfg.DB.DSN, cfg.DB.MaxConns); err != nil {
			return err
		}
		a.closers = append(a.closers, a.Store.Close)
		a.Repo = repodb.New(a.Store)
	}
	a.Audit = audit.NewWriter(a.Repo, func(err error) { a.Log.Error("audit write failed", "err", err) })
	a.closers = append(a.closers, a.Audit.Close)

	// Object store for attachments; the bucket is self-provisioned (non-fatal).
	a.Blob = o.Blob
	if a.Blob == nil {
		if a.Blob, err = blob.New(blob.Config{
			Endpoint: cfg.ObjectStore.Endpoint, Bucket: cfg.ObjectStore.Bucket, Region: cfg.ObjectStore.Region,
			AccessKey: cfg.ObjectStore.AccessKey, SecretKey: cfg.ObjectStore.SecretKey, UseSSL: cfg.ObjectStore.UseSSL,
		}); err != nil {
			return fmt.Errorf("object store: %w", err)
		}
	}
	if berr := a.Blob.EnsureBucket(ctx); berr != nil {
		a.Log.Warn("object store: ensure bucket", "bucket", cfg.ObjectStore.Bucket, "err", berr)
	}
	return nil
}

// buildPeers wires what the module asks other services: the platform-token
// verifier, the permission checker and the agent directory (auth), and the
// secret source (warden). Mesh connections are dialled lazily where possible
// so the module starts while a peer is down.
func (a *App) buildPeers(ctx context.Context, cfg config.Config, o Options) error {
	a.Verifier, a.Checker, a.Agents = o.Verifier, o.Checker, o.Agents
	if a.Verifier == nil || a.Checker == nil || a.Agents == nil {
		conn, err := a.Freya.Client(ctx, "auth")
		if err != nil {
			return fmt.Errorf("auth client: %w", err)
		}
		if a.Verifier == nil {
			a.Verifier = authclient.New(authclient.Config{Issuer: cfg.Gateway.Issuer},
				authclient.GRPCKeys{Client: authv1.NewKeysClient(conn)},
				authclient.GRPCRevocations{Client: authv1.NewSessionsClient(conn)})
		}
		if a.Checker == nil {
			a.Checker = AuthPerms{Client: authv1.NewAuthorizationClient(conn)}
		}
		if a.Agents == nil {
			a.Agents = agents.New(conn, a.Checker)
		}
	}
	a.Secrets = o.Secrets
	if a.Secrets == nil {
		var tok secrets.TokenFunc
		if cfg.Secrets.TokenFile != "" {
			tok = secrets.FileToken(cfg.Secrets.TokenFile)
		}
		resolver := secrets.Resolver{Warden: &lazyWarden{app: a, token: tok}, AllowFile: !cfg.IsProduction()}
		a.Secrets = secrets.NewCached(resolver, cfg.Inbound.RelayTokenRef, cfg.SMTP.PasswordRef, cfg.SecretRefresh())
	}
	return nil
}

func (a *App) buildEvents(cfg config.Config, client stream.Client) error {
	if client == nil {
		sc := valkeykv.Config{Addresses: cfg.Valkey.Addresses, Username: cfg.Valkey.Username, Password: cfg.Valkey.Password, AllowPlaintext: cfg.Valkey.AllowPlaintext}
		if cfg.Valkey.CAFile != "" {
			pem, err := os.ReadFile(cfg.Valkey.CAFile)
			if err != nil {
				return fmt.Errorf("valkey ca: %w", err)
			}
			sc.CAPEM = pem
		}
		c, err := valkeykv.New(sc)
		if err != nil {
			return fmt.Errorf("event bus: %w", err)
		}
		client = c
	}
	a.Hub = stream.NewHub(client, stream.Config{}, a.Log)
	a.closers = append(a.closers, a.Hub.Close)
	a.Events = events.HubPublisher{}
	if cfg.Events.Enabled {
		a.Events = events.HubPublisher{Hub: a.Hub}
	}
	return nil
}

// lazyWarden dials warden on first use so the module starts while it is down.
type lazyWarden struct {
	app   *App
	token secrets.TokenFunc
	mu    sync.Mutex
	w     *secrets.Warden
}

func (l *lazyWarden) Fetch(ctx context.Context, id string) (string, error) {
	l.mu.Lock()
	if l.w == nil {
		conn, err := l.app.Freya.Client(ctx, l.app.Cfg.Secrets.WardenService)
		if err != nil {
			l.mu.Unlock()
			return "", fmt.Errorf("%w: warden client", secrets.ErrUnavailable)
		}
		l.w = secrets.NewWarden(conn, l.token)
	}
	w := l.w
	l.mu.Unlock()
	return w.Fetch(ctx, id)
}

// health reports component reachability for /health.
func (a *App) health() map[string]string {
	out := map[string]string{"store": "ok", "object_store": "ok", "relay_token": "ok"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if a.Store != nil {
		if err := a.Store.Ping(ctx); err != nil {
			out["store"] = "unreachable"
		}
	}
	if _, err := a.Secrets.RelayToken(ctx); err != nil {
		out["relay_token"] = "unavailable"
	}
	return out
}

// serveInbound runs the off-mesh inbound edge until ctx is cancelled.
func (a *App) serveInbound(ctx context.Context) {
	var err error
	if a.inboundLis != nil {
		a.Log.Info("inbound edge listening", "addr", a.inboundLis.Addr().String())
		err = a.Inbound.Serve(ctx, a.inboundLis)
	} else {
		a.Log.Info("inbound edge listening", "addr", a.Cfg.InboundAddr())
		err = a.Inbound.Run(ctx)
	}
	if err != nil && ctx.Err() == nil {
		a.Log.Error("inbound edge", "err", err)
	}
}

// Run starts the verifier, gateway registration, permission seeding, workers
// (the inbound edge) and the Freya runtime.
func (a *App) Run(ctx context.Context) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if v, ok := a.Verifier.(*authclient.Verifier); ok {
		go func() {
			for wctx.Err() == nil {
				if err := v.Start(wctx, func(err error) { a.Log.Warn("verifier", "err", err) }); err == nil {
					return
				}
				select {
				case <-wctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
		}()
	}
	go a.register(wctx)
	for _, w := range a.workers {
		go w(wctx)
	}
	go func() {
		for wctx.Err() == nil && !a.Freya.Ready() {
			time.Sleep(100 * time.Millisecond)
		}
		a.seedLoop(wctx)
	}()
	return a.Freya.Run(ctx)
}

// Close releases resources (idempotent).
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// register keeps the gateway lease for the manifest.
func (a *App) register(ctx context.Context) {
	for ctx.Err() == nil && !a.Freya.Ready() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	man, err := ticketmanifest.Manifest()
	if err != nil {
		a.Log.Error("gateway manifest", "err", err)
		return
	}
	httpEP, err := a.Freya.HTTP().Endpoint()
	if err != nil {
		a.Log.Error("gateway registration: http endpoint", "err", err)
		return
	}
	grpcEP, err := a.Freya.GRPC().Endpoint()
	if err != nil {
		a.Log.Error("gateway registration: grpc endpoint", "err", err)
		return
	}
	var client *gatewayclient.Client
	for ctx.Err() == nil && client == nil {
		conn, cerr := a.Freya.Client(ctx, a.Cfg.Gateway.Service)
		if cerr != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		client, err = gatewayclient.New(conn, gatewayclient.Options{Manifest: man, HTTPURL: "https://" + httpEP.Host, GRPCTarget: grpcEP.Host, Logger: a.Log,
			OnState: func(s gatewayclient.State) {
				a.Log.Info("gateway lease", "registered", s.Registered, "lease", s.LeaseID, "err", s.Err)
			}})
		if err != nil {
			a.Log.Error("gateway client", "err", err)
			return
		}
	}
	if client != nil {
		if err := client.Run(ctx); err != nil {
			a.Log.Error("gateway registration", "err", err)
		}
	}
}
