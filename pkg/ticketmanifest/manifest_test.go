package ticketmanifest

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-ticket/v4/api/openapi"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
)

func TestOpenAPIParsesAndValidates(t *testing.T) {
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562))
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(openapi.Ticket)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(loader.Context, openapi3.DisableExamplesValidation()); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestManifestBuilds(t *testing.T) {
	m, err := Manifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Module != "ticket" || len(m.Prefixes) != 1 || m.Prefixes[0] != "/api/ticket" {
		t.Fatalf("identity = %+v", m)
	}
	if len(m.Routes) < 30 {
		t.Fatalf("routes = %d", len(m.Routes))
	}
	public := 0
	for _, r := range m.Routes {
		if !strings.HasPrefix(r.Path, "/api/ticket/v1/") {
			t.Fatalf("route outside prefix: %s", r.Path)
		}
		if r.Public {
			public++
			if r.Path != "/api/ticket/v1/health" {
				t.Fatalf("unexpected public route %s", r.Path)
			}
		}
	}
	if public != 1 {
		t.Fatalf("public routes = %d", public)
	}
	titles := []string{}
	for _, n := range m.Nav {
		titles = append(titles, n.Title)
	}
	if strings.Join(titles, ",") != "Tickets,Dashboard,Rules,Tags,Mailboxes" {
		t.Fatalf("nav = %v", titles)
	}
	if len(m.Abilities) != 8 || len(m.Permissions) != 8 {
		t.Fatalf("abilities/permissions = %d/%d", len(m.Abilities), len(m.Permissions))
	}
}

// The manifest's permission vocabulary is exactly the module's authz set.
func TestPermissionsMatchAuthz(t *testing.T) {
	got := PermissionRefs()
	want := append([]string(nil), authz.Permissions...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("manifest %v != authz %v", got, want)
	}
}

func TestRolesAndGrants(t *testing.T) {
	if len(Roles["ticket admin"]) != 8 {
		t.Fatal("admin must hold everything")
	}
	agent := strings.Join(Roles["ticket agent"], ",")
	if agent != "tickets:read,tickets:manage,tags:manage" {
		t.Fatalf("agent = %s", agent)
	}
	if strings.Join(Roles["ticket viewer"], ",") != "tickets:read" {
		t.Fatal("viewer")
	}
	for _, slug := range BuiltinRoles {
		if len(Grants[slug]) == 0 {
			t.Fatalf("no grant for %s", slug)
		}
	}
	for _, r := range Grants["operator"] {
		if r == "tickets:delete" || r == "backup:manage" || r == "rules:manage" {
			t.Fatalf("operator over-granted: %s", r)
		}
	}
	req := SeedRequest()
	if len(req.GetPermissions()) != 8 || len(req.GetBuiltinGrants()) != 5 {
		t.Fatalf("seed = %+v", req)
	}
}

func docWith(ext map[string]any) *openapi3.T {
	paths := openapi3.NewPaths()
	paths.Set("/x", &openapi3.PathItem{Get: &openapi3.Operation{Extensions: ext}})
	return &openapi3.T{Paths: paths}
}

func TestRoutesValidationBranches(t *testing.T) {
	routes, err := Routes(docWith(map[string]any{PublicExtension: true}))
	if err != nil || !routes[0].Public {
		t.Fatalf("public: %v", err)
	}
	routes, err = Routes(docWith(map[string]any{PermissionExtension: "tickets:read", BodyLimitExtension: float64(2048), TimeoutExtension: float64(30)}))
	if err != nil || routes[0].MaxBodyBytes != 2048 || routes[0].Timeout.Seconds() != 30 {
		t.Fatalf("valid: %v %+v", err, routes)
	}
	bad := []map[string]any{
		{},
		{PermissionExtension: "made:up"},
		{PermissionExtension: "tickets:read", BodyLimitExtension: float64(0)},
		{PermissionExtension: "tickets:read", BodyLimitExtension: "big"},
		{PermissionExtension: "tickets:read", TimeoutExtension: float64(9999)},
		{PermissionExtension: "tickets:read", TimeoutExtension: "slow"},
	}
	for i, ext := range bad {
		if _, err := Routes(docWith(ext)); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

type fakeAuthz struct {
	grpc.ClientConnInterface
	got *authv1.RegisterPermissionsRequest
	err error
}

func (f *fakeAuthz) Invoke(_ context.Context, method string, args, _ any, _ ...grpc.CallOption) error {
	if !strings.HasSuffix(method, "/RegisterPermissions") {
		return errors.New("unexpected " + method)
	}
	f.got = args.(*authv1.RegisterPermissionsRequest)
	return f.err
}

func TestSeedPermissions(t *testing.T) {
	f := &fakeAuthz{}
	if err := SeedPermissions(context.Background(), f); err != nil || f.got == nil {
		t.Fatalf("seed: %v", err)
	}
	f.err = errors.New("auth down")
	if err := SeedPermissions(context.Background(), f); err == nil {
		t.Fatal("error swallowed")
	}
}
