package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra/v4/authz"
	"github.com/go-tangra/go-tangra/v4/catalogue"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/config"
	"github.com/go-tangra/go-tangra-ticket/v4/pkg/ticketmanifest"
)

var placeholderRE = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

func bundlePath(name string) string { return filepath.Join("..", "..", "deploy", "bundle", name) }

// joinValues are values of the shape the gateway fills in: core values,
// generated secrets and the host inputs.
func joinValues(smtp map[string]string) map[string]string {
	v := map[string]string{
		"TRUST_DOMAIN": "infra.example.org", "GATEWAY_ISSUER": "https://portal.example.org:8443",
		"LCM_ENROLL_URL": "https://portal.example.org:8443/api/lcm/v1/enroll", "LCM_GRPC": "portal.example.org:9945",
		"GATEWAY_GRPC": "portal.example.org:9643", "AUTH_GRPC": "portal.example.org:9543", "MESH_TENANT_ID": "00000000-0000-0000-0000-000000000001",
		"MODULE": "ticket", "MODULE_VERSION": "4.2.0", "MODULE_IMAGE": "ghcr.io/go-tangra/go-tangra-ticket",
		"GEN_PASSWORD_1": strings.Repeat("a", 48), "GEN_PASSWORD_2": strings.Repeat("b", 48),
		"GEN_PASSWORD_3": strings.Repeat("d", 48),
		"VALKEY_ADDRESS": "valkey.example.org:6380", "VALKEY_PASSWORD": strings.Repeat("c", 32), "VALKEY_CA_FILE": "/etc/tangra/valkey-ca.pem",
		"GEN_KEY_1": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "GEN_KEY_2": "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA=",
		"MODULE_ADVERTISE_HOST": "ticket.example.org", "MODULE_BIND_IP": "10.0.0.7",
		"TICKET_INBOUND_LISTEN": "0.0.0.0:9957", "TICKET_INBOUND_TLS_DIR": "/etc/tangra/edge",
		"TICKET_MAIL_DOMAIN": "example.org", "TICKET_SMTP_PORT": "587", "TICKET_SMTP_TLS": "starttls",
		// Optional inputs left empty render as "" (gateway 4.12.0).
		"TICKET_SMTP_HOST": "", "TICKET_SMTP_USERNAME": "", "TICKET_SMTP_PASSWORD": "",
	}
	for k, s := range smtp {
		v[k] = s
	}
	return v
}

func render(t *testing.T, name string, values map[string]string) string {
	t.Helper()
	raw, err := os.ReadFile(bundlePath(name))
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	out := placeholderRE.ReplaceAllStringFunc(string(raw), func(m string) string {
		k := placeholderRE.FindStringSubmatch(m)[1]
		v, ok := values[k]
		if !ok {
			missing = append(missing, k)
		}
		return v
	})
	if len(missing) > 0 {
		t.Fatalf("%s uses placeholders the gateway does not fill: %v", name, missing)
	}
	return out
}

// tangra-module.yaml is a valid catalogue descriptor for this module: its
// name is the manifest's, its routes cover what the module serves, and every
// host input the bundle uses is declared.
func TestCatalogueDescriptor(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tangra-module.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := catalogue.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.Module != ticketmanifest.Module || d.Routes.Prefixes[0] != ticketmanifest.APIPrefix {
		t.Fatalf("%s %v", d.Module, d.Routes)
	}
	declared := map[string]bool{}
	for _, in := range d.HostInputs {
		declared[in.Key] = true
	}
	compose, err := os.ReadFile(bundlePath("compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range placeholderRE.FindAllStringSubmatch(string(compose), -1) {
		if k := m[1]; !catalogue.Reserved(k) && !declared[k] {
			t.Errorf("compose.yaml uses %s, neither a gateway value nor a host input", k)
		}
	}
	for _, h := range d.Bundle.TLSHosts {
		if !strings.Contains(string(compose), "/tls/"+h+".key") {
			t.Errorf("tls host %s: its key is not used by compose.yaml", h)
		}
	}
}

// The join bundle's templates, rendered with values of the shape the gateway
// fills in, are a valid configuration that meets every production guard; it
// keeps env dev only because production mode refuses its file: secret
// references (deploy/README.md).
func TestJoinBundleRendersToValidConfig(t *testing.T) {
	for name, smtp := range map[string]map[string]string{
		"no outbound mail": nil,
		"smtp relay":       {"TICKET_SMTP_HOST": "smtp.example.org", "TICKET_SMTP_USERNAME": "helpdesk@example.org", "TICKET_SMTP_PASSWORD": "relay-pw"},
	} {
		t.Run(name, func(t *testing.T) {
			values := joinValues(smtp)
			dir := t.TempDir()
			for _, f := range []string{"config.yaml", "policy.yaml"} {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(render(t, f, values)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("rendered config invalid: %v", err)
			}
			if cfg.Gateway.Issuer != values["GATEWAY_ISSUER"] || cfg.SMTPEnabled() != (smtp != nil) || cfg.SMTP.Username != values["TICKET_SMTP_USERNAME"] {
				t.Fatalf("%+v %+v", cfg.Gateway, cfg.SMTP)
			}
			if cfg.Valkey.AllowPlaintext || cfg.Valkey.Addresses[0] != values["VALKEY_ADDRESS"] || cfg.Valkey.Username != "ticket" || cfg.Valkey.CAFile == "" {
				t.Fatalf("event bus is not the core's over TLS: %+v", cfg.Valkey.Addresses)
			}
			cfg.Env = "production"
			if err := cfg.Validate(); err != nil {
				t.Fatalf("rendered config misses a production guard: %v", err)
			}
			// Accepted: the file: references, no relay, and the request limit
			// raised for backup import (as in the platform stack).
			for _, w := range cfg.Warnings() {
				if !strings.Contains(w, "file:") && !strings.Contains(w, "smtp.host empty") && !strings.HasPrefix(w, "limits.max_request_bytes") {
					t.Errorf("insecure opt-out in the bundle: %s", w)
				}
			}
			pol, err := os.Open(filepath.Join(dir, "policy.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			defer pol.Close()
			p, err := authz.Load(pol)
			if err != nil {
				t.Fatalf("rendered policy invalid: %v", err)
			}
			if raw, _ := os.ReadFile(filepath.Join(dir, "policy.yaml")); p == nil || !strings.Contains(string(raw), "spiffe://infra.example.org/svc/gateway") {
				t.Fatal(string(raw))
			}
		})
	}
}
