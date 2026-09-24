// Package contract verifies the ticket OpenAPI document against the mounted
// HTTP surface and the gateway manifest (T019): the document parses and
// validates; every route declares a permission (only /health is public);
// every mutating route requires the CSRF header; every route the server mounts
// is declared; and no response schema can carry the raw HTML body.
package contract

import (
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/httpapi"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/stream"
	"github.com/go-tangra/go-tangra-ticket/v4/pkg/ticketmanifest"
)

// contractRoutes are the routes of contracts/ticket-api.md §A.
var contractRoutes = []string{
	"GET /api/ticket/v1/tickets", "POST /api/ticket/v1/tickets",
	"GET /api/ticket/v1/tickets/{id}", "PUT /api/ticket/v1/tickets/{id}", "DELETE /api/ticket/v1/tickets/{id}",
	"POST /api/ticket/v1/tickets/{id}/assign", "POST /api/ticket/v1/tickets/{id}/status", "POST /api/ticket/v1/tickets/{id}/tags",
	"GET /api/ticket/v1/tickets/{id}/history", "GET /api/ticket/v1/tickets/{id}/body", "GET /api/ticket/v1/tickets/{id}/attachments/{att_id}",
	"GET /api/ticket/v1/tickets/{id}/comments", "POST /api/ticket/v1/tickets/{id}/comments", "POST /api/ticket/v1/tickets/{id}/reply",
	"DELETE /api/ticket/v1/comments/{id}",
	"GET /api/ticket/v1/tags", "POST /api/ticket/v1/tags", "PUT /api/ticket/v1/tags/{id}", "DELETE /api/ticket/v1/tags/{id}",
	"GET /api/ticket/v1/rules", "POST /api/ticket/v1/rules", "POST /api/ticket/v1/rules/test",
	"GET /api/ticket/v1/rules/{id}", "PUT /api/ticket/v1/rules/{id}", "DELETE /api/ticket/v1/rules/{id}",
	"GET /api/ticket/v1/mailboxes", "POST /api/ticket/v1/mailboxes", "PUT /api/ticket/v1/mailboxes/{id}", "DELETE /api/ticket/v1/mailboxes/{id}",
	"GET /api/ticket/v1/assignable-users", "GET /api/ticket/v1/stats", "GET /api/ticket/v1/stream",
	"POST /api/ticket/v1/backup/export", "POST /api/ticket/v1/backup/import",
	"GET /api/ticket/v1/health",
}

func TestDocumentMatchesContract(t *testing.T) {
	doc, err := httpapi.LoadDocument()
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	declared := map[string]bool{}
	for _, r := range httpapi.DeclaredRoutes(doc) {
		declared[r.String()] = true
	}
	for _, want := range contractRoutes {
		if !declared[want] {
			t.Errorf("contract route %s is not declared", want)
		}
	}
	if len(declared) != len(contractRoutes) {
		t.Errorf("declared %d routes, contract lists %d", len(declared), len(contractRoutes))
	}

	routes, err := ticketmanifest.Routes(doc)
	if err != nil {
		t.Fatalf("manifest routes: %v", err)
	}
	public := httpapi.PublicRoutes(doc)
	if len(public) != 1 || !public[httpapi.Route{Method: "GET", Path: "/api/ticket/v1/health"}] {
		t.Fatalf("exactly /health is public: %v", public)
	}
	for _, r := range routes {
		if r.Public {
			continue
		}
		if !authz.Known(r.Permission) {
			t.Errorf("%s %s: unknown permission %q", r.Method, r.Path, r.Permission)
		}
		// The long-lived SSE stream must escape the gateway's normal 30 s forward
		// timeout: it declares the 5-minute route maximum (the module closes it at
		// 290 s and the client resumes with last_id).
		if r.Path == "/api/ticket/v1/stream" && r.Timeout != 5*time.Minute {
			t.Errorf("/stream must declare the 300 s route maximum, got %v", r.Timeout)
		}
	}
}

func TestMutationsRequireCSRF(t *testing.T) {
	doc, err := httpapi.LoadDocument()
	if err != nil {
		t.Fatal(err)
	}
	for p, item := range doc.Paths.Map() {
		for m, op := range item.Operations() {
			if m == "GET" || m == "HEAD" {
				continue
			}
			found := false
			for _, prm := range op.Parameters {
				if prm.Value != nil && prm.Value.In == "header" && prm.Value.Name == "X-CSRF-Token" && prm.Value.Required {
					found = true
				}
			}
			if !found {
				t.Errorf("%s %s does not require X-CSRF-Token", m, p)
			}
		}
	}
}

// schemaMentions reports whether s (recursively) declares property name.
func schemaMentions(s *openapi3.SchemaRef, name string, seen map[*openapi3.Schema]bool) bool {
	if s == nil || s.Value == nil || seen[s.Value] {
		return false
	}
	v := s.Value
	seen[v] = true
	for k, p := range v.Properties {
		if k == name || schemaMentions(p, name, seen) {
			return true
		}
	}
	if schemaMentions(v.Items, name, seen) {
		return true
	}
	if v.AdditionalProperties.Schema != nil && schemaMentions(v.AdditionalProperties.Schema, name, seen) {
		return true
	}
	for _, group := range []openapi3.SchemaRefs{v.AllOf, v.AnyOf, v.OneOf} {
		for _, sub := range group {
			if schemaMentions(sub, name, seen) {
				return true
			}
		}
	}
	return false
}

// No route may return the raw HTML body (research D7): only sanitised HTML via
// /tickets/{id}/body.
func TestNoResponseCarriesRawHTML(t *testing.T) {
	doc, err := httpapi.LoadDocument()
	if err != nil {
		t.Fatal(err)
	}
	for p, item := range doc.Paths.Map() {
		for m, op := range item.Operations() {
			if op.Responses == nil {
				continue
			}
			for code, resp := range op.Responses.Map() {
				if resp.Value == nil {
					continue
				}
				for ct, media := range resp.Value.Content {
					for _, forbidden := range []string{"body_html", "relay_token", "smtp_password", "secret_key", "access_key"} {
						if schemaMentions(media.Schema, forbidden, map[*openapi3.Schema]bool{}) {
							t.Errorf("%s %s %s (%s) exposes %s", m, p, code, ct, forbidden)
						}
					}
				}
			}
		}
	}
	// Sanity: the body route declares the sanitised shape.
	body := doc.Paths.Find("/api/ticket/v1/tickets/{id}/body").Get
	schema := body.Responses.Value("200").Value.Content.Get("application/json").Schema
	if !schemaMentions(schema, "html_sanitized", map[*openapi3.Schema]bool{}) {
		t.Fatal("/tickets/{id}/body must return html_sanitized")
	}
}

// Every route the server mounts is declared (Handle refuses anything else),
// and the foundational routes (/health, /stream) are mounted.
func TestMountedRoutesAreDeclared(t *testing.T) {
	rt := testrt.New(t, testutil.MustCA("example.org"), "ticket")
	s, err := httpapi.NewHandler(rt)
	if err != nil {
		t.Fatal(err)
	}
	hub := stream.NewHub(stream.NewMemory(), stream.Config{}, nil)
	defer hub.Close()
	s.Register(httpapi.Deps{Hub: hub})
	declared := map[string]bool{}
	for _, r := range s.Declared() {
		declared[r.String()] = true
	}
	impl := s.Implemented()
	for _, r := range impl {
		if !declared[r.String()] {
			t.Errorf("mounted but undeclared: %s", r)
		}
	}
	got := []string{}
	for _, r := range impl {
		got = append(got, r.String())
	}
	joined := strings.Join(got, ",")
	for _, want := range []string{"GET /api/ticket/v1/health", "GET /api/ticket/v1/stream"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s not mounted (have %s)", want, joined)
		}
	}
	if err := s.HandleFunc("GET", "/api/ticket/v1/raw-html", nil); err == nil {
		t.Fatal("an undeclared route was mounted")
	}
}
