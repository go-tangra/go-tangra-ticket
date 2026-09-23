package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fconfig "github.com/go-freya/freya/config"
)

// valid returns a Config that passes both the framework and module Validate.
func valid() Config {
	c := Default()
	c.ServiceName = "ticket"
	c.TrustDomain = "example.org"
	c.Authz.Path = "/etc/ticket/policy.yaml"
	c.DB.DSN = "postgres://localhost/ticket"
	c.Valkey.Addresses = []string{"valkey:6379"}
	c.KEK = KEK{Source: "file", Path: "/etc/ticket/kek"}
	c.ObjectStore.Endpoint = "rustfs.example.org:9000"
	c.ObjectStore.Bucket = "ticket"
	c.ObjectStore.UseSSL = true
	c.Inbound.Addr = "0.0.0.0:9957"
	c.Inbound.TLSCertFile = "/etc/ticket/inbound.crt"
	c.Inbound.TLSKeyFile = "/etc/ticket/inbound.key"
	c.Inbound.RelayTokenRef = "warden:0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	c.Gateway.Issuer = "https://gw.example.org"
	return c
}

func TestDefaultSecure(t *testing.T) {
	d := Default()
	if d.KEK.Source != "file" || d.Valkey.AllowPlaintext || !d.Events.Enabled {
		t.Fatalf("insecure defaults: %+v %+v %+v", d.KEK, d.Valkey, d.Events)
	}
	if d.Inbound.MaxBodyBytes != 10<<20 || d.Inbound.MaxAttachmentBytes != 25<<20 || d.Inbound.MaxParts != 100 || d.Inbound.MaxDepth != 10 {
		t.Fatalf("inbound caps: %+v", d.Inbound)
	}
	if d.Inbound.InsecureDev {
		t.Fatal("inbound.insecure_dev must default to false")
	}
	if d.SMTP.TLS != TLSStartTLS || d.SMTP.AllowPlaintext {
		t.Fatalf("smtp defaults: %+v", d.SMTP)
	}
	if d.Rules.CostLimit == 0 || d.Rules.EvalTimeoutMS == 0 {
		t.Fatalf("rules defaults: %+v", d.Rules)
	}
	if d.Limits.MaxPageSize != 100 {
		t.Fatalf("page size default: %d", d.Limits.MaxPageSize)
	}
	if err := Default().Validate(); err == nil {
		t.Fatal("Default() must not validate without required fields")
	}
}

func TestValidateOK(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	c := valid()
	c.KEK = KEK{Source: "env", Env: "TICKET_KEK"}
	c.SMTP = SMTP{Host: "smtp.example.org", Port: 465, TLS: TLSImplicit, Username: "u", PasswordRef: "warden:x", MailDomain: "example.org", TimeoutSeconds: 10}
	if err := c.Validate(); err != nil {
		t.Fatalf("env kek + implicit smtp rejected: %v", err)
	}
	c = valid()
	c.Inbound = Inbound{Addr: ":9957", InsecureDev: true, RelayTokenRef: "file:/run/token", MaxBodyBytes: 10 << 20, MaxAttachmentBytes: 25 << 20, MaxParts: 100, MaxDepth: 10}
	c.SMTP = SMTP{Host: "mailpit", Port: 1025, TLS: TLSNone, AllowPlaintext: true, TimeoutSeconds: 5}
	if err := c.Validate(); err != nil {
		t.Fatalf("dev config rejected: %v", err)
	}
	c = valid()
	c.Env = "production"
	c.DB.DSN = "postgres://db/ticket?sslmode=verify-full"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"framework", func(c *Config) { c.ServiceName = "" }, ""},
		{"no db", func(c *Config) { c.DB.DSN = "" }, "db.dsn"},
		{"db prod tls", func(c *Config) { c.Env = "production" }, "sslmode"},
		{"no valkey", func(c *Config) { c.Valkey.Addresses = nil }, "valkey.addresses"},
		{"valkey plaintext prod", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://x?sslmode=verify-full"
			c.Valkey.AllowPlaintext = true
		}, "valkey.allow_plaintext"},
		{"kek file no path", func(c *Config) { c.KEK = KEK{Source: "file"} }, "kek.path"},
		{"kek env no env", func(c *Config) { c.KEK = KEK{Source: "env"} }, "kek.env"},
		{"kek bad source", func(c *Config) { c.KEK = KEK{Source: "vault"} }, "kek.source"},
		{"no object store", func(c *Config) { c.ObjectStore.Bucket = "" }, "object_store.endpoint"},
		{"presign ttl", func(c *Config) { c.ObjectStore.PresignTTL = 5 }, "presign_ttl"},
		{"object store ssl prod", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://x?sslmode=verify-ca"
			c.ObjectStore.UseSSL = false
		}, "use_ssl"},
		{"no inbound addr", func(c *Config) { c.Inbound.Addr = "" }, "inbound.addr"},
		{"bad inbound addr", func(c *Config) { c.Inbound.Addr = "nohostport" }, "host:port"},
		{"no relay token", func(c *Config) { c.Inbound.RelayTokenRef = "  " }, "relay_token_ref"},
		{"inbound no tls", func(c *Config) { c.Inbound.TLSKeyFile = "" }, "tls_cert_file"},
		{"inbound insecure prod", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://x?sslmode=verify-full"
			c.Inbound.InsecureDev = true
		}, "insecure_dev"},
		{"body cap", func(c *Config) { c.Inbound.MaxBodyBytes = 10 }, "max_body_bytes"},
		{"attachment cap", func(c *Config) { c.Inbound.MaxAttachmentBytes = 1 << 40 }, "max_attachment_bytes"},
		{"parts cap", func(c *Config) { c.Inbound.MaxParts = 0 }, "max_parts"},
		{"depth cap", func(c *Config) { c.Inbound.MaxDepth = 99 }, "max_depth"},
		{"smtp host", func(c *Config) { c.SMTP.Host = "bad host" }, "smtp.host"},
		{"smtp port", func(c *Config) { c.SMTP.Host = "smtp"; c.SMTP.Port = 0 }, "smtp.port"},
		{"smtp tls mode", func(c *Config) { c.SMTP.Host = "smtp"; c.SMTP.TLS = "ssl" }, "smtp.tls"},
		{"smtp plaintext not allowed", func(c *Config) { c.SMTP.Host = "smtp"; c.SMTP.TLS = TLSNone }, "allow_plaintext"},
		{"smtp plaintext prod", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://x?sslmode=verify-full"
			c.SMTP.Host = "smtp"
			c.SMTP.TLS = TLSNone
			c.SMTP.AllowPlaintext = true
		}, "not permitted in production"},
		{"smtp user without password", func(c *Config) { c.SMTP.Host = "smtp"; c.SMTP.Username = "u" }, "password_ref"},
		{"smtp header chars", func(c *Config) { c.SMTP.Host = "smtp"; c.SMTP.MailDomain = "a\r\nb" }, "forbidden characters"},
		{"smtp timeout", func(c *Config) { c.SMTP.Host = "smtp"; c.SMTP.TimeoutSeconds = 0 }, "timeout_seconds"},
		{"rules cost", func(c *Config) { c.Rules.CostLimit = 1 }, "cost_limit"},
		{"rules timeout", func(c *Config) { c.Rules.EvalTimeoutMS = 0 }, "eval_timeout_ms"},
		{"rules max", func(c *Config) { c.Rules.MaxRules = 0 }, "max_rules"},
		{"warden service", func(c *Config) { c.Secrets.WardenService = "" }, "warden_service"},
		{"secret refresh", func(c *Config) { c.Secrets.RefreshSecs = 1 }, "refresh_seconds"},
		{"gateway service", func(c *Config) { c.Gateway.Service = "" }, "gateway.service"},
		{"gateway issuer", func(c *Config) { c.Gateway.Issuer = "http://gw" }, "gateway.issuer"},
		{"mesh insecure prod", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://x?sslmode=verify-full"
			c.MeshEnroll = MeshEnroll{Enabled: true, Insecure: true}
		}, "mesh_enroll.insecure"},
		{"request limit", func(c *Config) { c.Limits.MaxRequestBytes = 1 }, "max_request_bytes"},
		{"backup limit", func(c *Config) { c.Limits.MaxBackupBytes = 1 }, "max_backup_bytes"},
		{"page size", func(c *Config) { c.Limits.MaxPageSize = 1000 }, "max_page_size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("expected rejection")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	c := valid()
	c.Valkey.AllowPlaintext = true
	c.ObjectStore.UseSSL = false
	c.MeshEnroll = MeshEnroll{Enabled: true, Insecure: true}
	c.Inbound.InsecureDev = true
	c.Inbound.RelayTokenRef = "file:/run/t"
	w := strings.Join(c.Warnings(), "\n")
	for _, want := range []string{"valkey.allow_plaintext", "object_store.use_ssl", "mesh_enroll.insecure", "inbound.insecure_dev", "smtp.host empty", "file:"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings missing %q:\n%s", want, w)
		}
	}
	c.SMTP = SMTP{Host: "mailpit", TLS: TLSNone}
	if !strings.Contains(strings.Join(c.Warnings(), "\n"), "smtp.tls=none") {
		t.Error("smtp plaintext warning missing")
	}
	if n := len(valid().Warnings()); n != 1 { // only "smtp.host empty"
		t.Errorf("valid config warnings = %d", n)
	}
}

func TestAccessors(t *testing.T) {
	c := valid()
	c.Config.Server = fconfig.Server{GRPCAddr: ":9955", HTTPAddr: ":9956"}
	c.Config.Admin.Addr = ":9840"
	if c.GRPCAddr() != ":9955" || c.HTTPAddr() != ":9956" || c.AdminAddr() != ":9840" || c.InboundAddr() != "0.0.0.0:9957" {
		t.Fatal("addr accessors")
	}
	if c.PresignTTL() != 300*time.Second || c.RuleTimeout() != 50*time.Millisecond || c.SecretRefresh() != 300*time.Second || c.SMTPTimeout() != 30*time.Second {
		t.Fatal("duration accessors")
	}
	if c.SMTPEnabled() {
		t.Fatal("smtp disabled by default")
	}
	c.SMTP.Host = "smtp"
	if !c.SMTPEnabled() {
		t.Fatal("smtp enabled")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ticket.yaml")
	body := "service_name: ticket\ninbound:\n  addr: \":9957\"\n  relay_token_ref: warden:abc\nsmtp:\n  host: mailpit\n  tls: none\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Inbound.RelayTokenRef != "warden:abc" || c.SMTP.Host != "mailpit" || c.Inbound.MaxParts != 100 {
		t.Fatalf("loaded %+v %+v", c.Inbound, c.SMTP)
	}
	if err := os.WriteFile(p, []byte("bogus_key: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
}
