// Package config loads and validates the ticket (helpdesk) service
// configuration: the Freya framework config plus the module's own sections.
// Every value is explicit; insecure opt-outs are named and surfaced at start
// (Constitution I/VII). The service refuses to start without a KEK, a store, an
// object store and a relay-token reference for the inbound mail edge.
//
// Secrets never live in this file: the relay token and the SMTP password are
// warden secret references (research D2/D5), resolved at use time by the
// secrets package. The module's yaml keys are chosen to NOT collide with the
// framework sections the embedded config already owns (server, admin,
// discovery, limits, identity, authz): the module's request bounds live under
// "limits_ticket", never the framework "limits".
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	fconfig "github.com/go-tangra/go-tangra/v4/config"
	"gopkg.in/yaml.v3"
)

// Config is the ticket service configuration. The embedded framework config
// (inline) already carries service_name, trust_domain, env, identity, authz,
// limits, admin, discovery and server (grpc_addr/http_addr).
type Config struct {
	fconfig.Config `yaml:",inline"`

	DB          DB          `yaml:"db"`
	Valkey      Valkey      `yaml:"valkey"`
	KEK         KEK         `yaml:"kek"`
	ObjectStore ObjectStore `yaml:"object_store"`
	Inbound     Inbound     `yaml:"inbound"`
	SMTP        SMTP        `yaml:"smtp"`
	Rules       Rules       `yaml:"rules"`
	Secrets     Secrets     `yaml:"secrets"`
	Events      Events      `yaml:"events"`
	Gateway     Gateway     `yaml:"gateway"`
	MeshEnroll  MeshEnroll  `yaml:"mesh_enroll"`
	Limits      Limits      `yaml:"limits_ticket"`
}

// DB configures the PostgreSQL/TimescaleDB store.
type DB struct {
	DSN        string `yaml:"dsn"`
	MigrateDSN string `yaml:"migrate_dsn"`
	MaxConns   int32  `yaml:"max_conns"`
}

// Valkey configures the platform event bus.
type Valkey struct {
	Addresses      []string `yaml:"addresses"`
	Username       string   `yaml:"username"`
	Password       string   `yaml:"password"`
	AllowPlaintext bool     `yaml:"allow_plaintext"`
	CAFile         string   `yaml:"ca_file"`
}

// KEK names where the 32-byte key-encryption key comes from.
type KEK struct {
	Source string `yaml:"source"` // file | env
	Path   string `yaml:"path"`
	Env    string `yaml:"env"`
}

// ObjectStore configures S3-compatible storage for ticket attachments. The
// access/secret keys are never returned to callers.
type ObjectStore struct {
	Endpoint   string `yaml:"endpoint"`
	Bucket     string `yaml:"bucket"`
	Region     string `yaml:"region"`
	UseSSL     bool   `yaml:"use_ssl"`
	AccessKey  string `yaml:"access_key"`
	SecretKey  string `yaml:"secret_key"`
	PresignTTL int    `yaml:"presign_ttl_seconds"`
}

// Inbound is the off-mesh inbound mail edge (POST /inbound/mail): a separate
// listener the mail relay posts raw RFC 822 messages to, authenticated by the
// relay token (a warden reference). TLS is required unless insecure_dev.
type Inbound struct {
	Addr               string `yaml:"addr"`
	TLSCertFile        string `yaml:"tls_cert_file"`
	TLSKeyFile         string `yaml:"tls_key_file"`
	InsecureDev        bool   `yaml:"insecure_dev"`
	RelayTokenRef      string `yaml:"relay_token_ref"`
	MaxBodyBytes       int64  `yaml:"max_body_bytes"`
	MaxAttachmentBytes int64  `yaml:"max_attachment_bytes"`
	MaxParts           int    `yaml:"max_parts"`
	MaxDepth           int    `yaml:"max_depth"`
}

// SMTP modes.
const (
	TLSImplicit = "implicit"
	TLSStartTLS = "starttls"
	TLSNone     = "none"
)

// SMTP configures the outbound relay for public replies and acknowledgements.
// An empty host disables outbound mail (replies are refused, acks skipped).
type SMTP struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	TLS            string `yaml:"tls"` // implicit | starttls | none
	AllowPlaintext bool   `yaml:"allow_plaintext"`
	Username       string `yaml:"username"`
	PasswordRef    string `yaml:"password_ref"`
	MailDomain     string `yaml:"mail_domain"` // Message-ID domain fallback
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// Rules bounds CEL rule evaluation (research D6, SR-005).
type Rules struct {
	CostLimit     uint64 `yaml:"cost_limit"`
	EvalTimeoutMS int    `yaml:"eval_timeout_ms"`
	MaxRules      int    `yaml:"max_rules"`
}

// Secrets configures how warden references are resolved: the warden service
// name on the mesh, the file holding the module's platform token used to call
// it, and how long a resolved value is cached before it is re-read (rotation).
type Secrets struct {
	WardenService string `yaml:"warden_service"`
	TokenFile     string `yaml:"token_file"`
	RefreshSecs   int    `yaml:"refresh_seconds"`
}

// Events toggles the realtime publisher.
type Events struct {
	Enabled bool `yaml:"enabled"`
}

// Gateway names the application gateway and the platform token issuer.
type Gateway struct {
	Service string `yaml:"service"`
	Issuer  string `yaml:"issuer"`
}

// MeshEnroll configures how the ticket SERVER obtains its own mesh SVID by
// enrolling with lcm over the network (identity.provider=provided).
type MeshEnroll struct {
	Enabled       bool   `yaml:"enabled"`
	EnrollURL     string `yaml:"enroll_url"`
	LCMGRPCTarget string `yaml:"lcm_grpc"`
	TenantID      string `yaml:"tenant_id"`
	TokenFile     string `yaml:"token_file"`
	StateFile     string `yaml:"state_file"`
	Insecure      bool   `yaml:"insecure"`
}

// Limits bound the module's browser request shapes.
type Limits struct {
	MaxRequestBytes int64 `yaml:"max_request_bytes"`
	MaxBackupBytes  int64 `yaml:"max_backup_bytes"`
	MaxPageSize     int   `yaml:"max_page_size"`
}

// Default returns secure defaults on top of the Freya defaults.
func Default() Config {
	return Config{
		Config:      fconfig.Default(),
		DB:          DB{MaxConns: 16},
		KEK:         KEK{Source: "file"},
		ObjectStore: ObjectStore{Region: "us-east-1", PresignTTL: 300},
		Inbound: Inbound{
			MaxBodyBytes:       10 << 20,
			MaxAttachmentBytes: 25 << 20,
			MaxParts:           100,
			MaxDepth:           10,
		},
		SMTP:    SMTP{Port: 587, TLS: TLSStartTLS, TimeoutSeconds: 30},
		Rules:   Rules{CostLimit: 100000, EvalTimeoutMS: 50, MaxRules: 500},
		Secrets: Secrets{WardenService: "warden", RefreshSecs: 300},
		Events:  Events{Enabled: true},
		Gateway: Gateway{Service: "gateway"},
		Limits:  Limits{MaxRequestBytes: 1 << 20, MaxBackupBytes: 64 << 20, MaxPageSize: 100},
	}
}

// Load reads YAML over Default(); unknown fields are rejected.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

func within[T int | int64 | uint64](v, lo, hi T) bool { return v >= lo && v <= hi }

// Validate checks the Freya config and every module section. It refuses a
// missing db, kek, object store or relay-token reference outright and enforces
// the production TLS guards.
func (c Config) Validate() error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	for _, check := range []func() error{c.validateInfra, c.validateInbound, c.validateSMTP, c.validateRest} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validateInfra() error {
	prod := c.IsProduction()
	if c.DB.DSN == "" {
		return errors.New("config: db.dsn is required")
	}
	if prod && !strings.Contains(c.DB.DSN, "sslmode=verify-full") && !strings.Contains(c.DB.DSN, "sslmode=verify-ca") {
		return errors.New("config: db.dsn must use sslmode=verify-full (or verify-ca) in production")
	}
	if len(c.Valkey.Addresses) == 0 {
		return errors.New("config: valkey.addresses is required")
	}
	if prod && c.Valkey.AllowPlaintext {
		return errors.New("config: valkey.allow_plaintext is not permitted in production")
	}
	switch c.KEK.Source {
	case "file":
		if c.KEK.Path == "" {
			return errors.New("config: kek.path is required for kek.source file")
		}
	case "env":
		if c.KEK.Env == "" {
			return errors.New("config: kek.env is required for kek.source env")
		}
	default:
		return errors.New("config: kek.source must be file or env")
	}
	if c.ObjectStore.Endpoint == "" || c.ObjectStore.Bucket == "" {
		return errors.New("config: object_store.endpoint and object_store.bucket are required")
	}
	if !within(c.ObjectStore.PresignTTL, 30, 3600) {
		return errors.New("config: object_store.presign_ttl_seconds must be within [30, 3600]")
	}
	if prod && !c.ObjectStore.UseSSL {
		return errors.New("config: object_store.use_ssl is required in production")
	}
	return nil
}

func (c Config) validateInbound() error {
	in := c.Inbound
	if in.Addr == "" {
		return errors.New("config: inbound.addr is required (the off-mesh inbound mail edge)")
	}
	if _, _, err := net.SplitHostPort(in.Addr); err != nil {
		return errors.New("config: inbound.addr must be host:port")
	}
	if strings.TrimSpace(in.RelayTokenRef) == "" {
		return errors.New("config: inbound.relay_token_ref is required (warden reference for the relay token)")
	}
	if in.InsecureDev {
		if c.IsProduction() {
			return errors.New("config: inbound.insecure_dev is not permitted in production")
		}
	} else if in.TLSCertFile == "" || in.TLSKeyFile == "" {
		return errors.New("config: inbound.tls_cert_file and inbound.tls_key_file are required (or inbound.insecure_dev for development)")
	}
	if !within(in.MaxBodyBytes, 1<<10, 64<<20) {
		return errors.New("config: inbound.max_body_bytes must be within [1 KiB, 64 MiB]")
	}
	if !within(in.MaxAttachmentBytes, 1<<10, 64<<20) {
		return errors.New("config: inbound.max_attachment_bytes must be within [1 KiB, 64 MiB]")
	}
	if !within(in.MaxParts, 1, 1000) {
		return errors.New("config: inbound.max_parts must be within [1, 1000]")
	}
	if !within(in.MaxDepth, 1, 50) {
		return errors.New("config: inbound.max_depth must be within [1, 50]")
	}
	return nil
}

func (c Config) validateSMTP() error {
	s := c.SMTP
	if s.Host == "" {
		return nil // outbound mail disabled: replies refused, acks skipped
	}
	if strings.ContainsAny(s.Host, " \r\n/") {
		return errors.New("config: smtp.host is not a host name")
	}
	if !within(s.Port, 1, 65535) {
		return errors.New("config: smtp.port must be within [1, 65535]")
	}
	switch s.TLS {
	case TLSImplicit, TLSStartTLS:
	case TLSNone:
		if !s.AllowPlaintext {
			return errors.New("config: smtp.tls none requires smtp.allow_plaintext (development only)")
		}
		if c.IsProduction() {
			return errors.New("config: smtp.tls none is not permitted in production")
		}
	default:
		return errors.New("config: smtp.tls must be implicit, starttls or none")
	}
	if s.Username != "" && strings.TrimSpace(s.PasswordRef) == "" {
		return errors.New("config: smtp.password_ref is required when smtp.username is set")
	}
	if strings.ContainsAny(s.Username, "\r\n") || strings.ContainsAny(s.MailDomain, "\r\n <>@") {
		return errors.New("config: smtp.username/mail_domain contain forbidden characters")
	}
	if !within(s.TimeoutSeconds, 1, 300) {
		return errors.New("config: smtp.timeout_seconds must be within [1, 300]")
	}
	return nil
}

func (c Config) validateRest() error {
	prod := c.IsProduction()
	if !within(c.Rules.CostLimit, 100, 10_000_000) {
		return errors.New("config: rules.cost_limit must be within [100, 10000000]")
	}
	if !within(c.Rules.EvalTimeoutMS, 1, 5000) {
		return errors.New("config: rules.eval_timeout_ms must be within [1, 5000]")
	}
	if !within(c.Rules.MaxRules, 1, 10000) {
		return errors.New("config: rules.max_rules must be within [1, 10000]")
	}
	if c.Secrets.WardenService == "" {
		return errors.New("config: secrets.warden_service is required")
	}
	if !within(c.Secrets.RefreshSecs, 10, 86400) {
		return errors.New("config: secrets.refresh_seconds must be within [10, 86400]")
	}
	if c.Gateway.Service == "" {
		return errors.New("config: gateway.service is required")
	}
	if iu, err := url.Parse(c.Gateway.Issuer); err != nil || iu.Scheme != "https" || iu.Host == "" {
		return errors.New("config: gateway.issuer must be an https origin")
	}
	if prod && c.MeshEnroll.Enabled && c.MeshEnroll.Insecure {
		return errors.New("config: mesh_enroll.insecure is not permitted in production")
	}
	if !within(c.Limits.MaxRequestBytes, 1<<10, 64<<20) {
		return errors.New("config: limits_ticket.max_request_bytes must be within [1 KiB, 64 MiB]")
	}
	if !within(c.Limits.MaxBackupBytes, 1<<10, 256<<20) {
		return errors.New("config: limits_ticket.max_backup_bytes must be within [1 KiB, 256 MiB]")
	}
	if !within(c.Limits.MaxPageSize, 1, 100) {
		return errors.New("config: limits_ticket.max_page_size must be within [1, 100]")
	}
	return nil
}

// Warnings lists accepted insecure opt-outs (surfaced at start).
func (c Config) Warnings() []string {
	w := c.Config.Warnings()
	if c.Valkey.AllowPlaintext {
		w = append(w, "valkey.allow_plaintext: event-bus traffic without TLS (development only)")
	}
	if !c.ObjectStore.UseSSL {
		w = append(w, "object_store.use_ssl=false: attachment traffic without TLS (development only)")
	}
	if c.MeshEnroll.Enabled && c.MeshEnroll.Insecure {
		w = append(w, "mesh_enroll.insecure: SVID enrollment without TLS (development only)")
	}
	if c.Inbound.InsecureDev {
		w = append(w, "inbound.insecure_dev: inbound mail edge without TLS (development only)")
	}
	if c.SMTP.Host != "" && c.SMTP.TLS == TLSNone {
		w = append(w, "smtp.tls=none: outbound mail without TLS (development only)")
	}
	if c.SMTP.Host == "" {
		w = append(w, "smtp.host empty: public replies are refused and acknowledgements skipped")
	}
	if strings.HasPrefix(c.Inbound.RelayTokenRef, "file:") || strings.HasPrefix(c.SMTP.PasswordRef, "file:") {
		w = append(w, "secret reference uses file: (development only; use a warden reference)")
	}
	return w
}

// GRPCAddr is the mesh gRPC listener (framework server section).
func (c Config) GRPCAddr() string { return c.Config.Server.GRPCAddr }

// HTTPAddr is the mesh HTTP listener (framework server section).
func (c Config) HTTPAddr() string { return c.Config.Server.HTTPAddr }

// AdminAddr is the framework admin/operations listener.
func (c Config) AdminAddr() string { return c.Config.Admin.Addr }

// InboundAddr is the off-mesh inbound mail edge listener.
func (c Config) InboundAddr() string { return c.Inbound.Addr }

// PresignTTL is the lifetime of a presigned attachment URL.
func (c Config) PresignTTL() time.Duration {
	return time.Duration(c.ObjectStore.PresignTTL) * time.Second
}

// RuleTimeout is the per-message CEL evaluation deadline.
func (c Config) RuleTimeout() time.Duration {
	return time.Duration(c.Rules.EvalTimeoutMS) * time.Millisecond
}

// SecretRefresh is how long a resolved warden secret is cached.
func (c Config) SecretRefresh() time.Duration {
	return time.Duration(c.Secrets.RefreshSecs) * time.Second
}

// SMTPTimeout bounds one outbound SMTP conversation.
func (c Config) SMTPTimeout() time.Duration {
	return time.Duration(c.SMTP.TimeoutSeconds) * time.Second
}

// SMTPEnabled reports whether outbound mail is configured.
func (c Config) SMTPEnabled() bool { return c.SMTP.Host != "" }
