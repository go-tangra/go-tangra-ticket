// Package ticketmanifest declares what the ticket (helpdesk) module registers
// with the application gateway: routes derived from the embedded OpenAPI
// document, the API permissions, the CASL abilities and the navigation
// entries; plus the built-in role grants it seeds with the auth service. The
// ticket gRPC surface is service-to-service and is not proxied by the gateway,
// and the inbound mail edge is a separate, non-gateway listener.
package ticketmanifest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-portal/sdk/v4/pkg/gatewayclient"
	"github.com/go-tangra/go-tangra-ticket/v4/api/openapi"
)

// Module identity.
const (
	Module       = "ticket"
	DisplayName  = "Tickets"
	Version      = "1.0.0"
	RemotePrefix = "/ui"
	APIPrefix    = "/api/ticket"
)

// OpenAPI operation extensions.
const (
	PermissionExtension = "x-freya-permission"
	PublicExtension     = "x-freya-public"
	BodyLimitExtension  = "x-freya-max-body-bytes"
	TimeoutExtension    = "x-freya-timeout-seconds"
)

// Permissions the module registers (research D11).
var Permissions = []gatewayclient.Permission{
	{Resource: "tickets", Action: "read", Description: "List and read tickets, conversations, attachments, tags, assignable users and the live stream"},
	{Resource: "tickets", Action: "manage", Description: "Create and edit tickets, assign, change status, set tags, add notes and send replies"},
	{Resource: "tickets", Action: "delete", Description: "Delete tickets with their conversations and attachments"},
	{Resource: "tags", Action: "manage", Description: "Manage the tag and category vocabulary"},
	{Resource: "rules", Action: "manage", Description: "Manage inbound triage rules"},
	{Resource: "mailboxes", Action: "manage", Description: "Manage inbound mailboxes and acknowledgements"},
	{Resource: "stats", Action: "read", Description: "Read the ticket dashboard statistics"},
	{Resource: "backup", Action: "manage", Description: "Export and import tenant ticket data"},
}

// Module roles (research D11): what each seeded role may do.
var Roles = map[string][]string{
	"ticket admin":  PermissionRefs(),
	"ticket agent":  {"tickets:read", "tickets:manage", "tags:manage"},
	"ticket viewer": {"tickets:read"},
}

// Grants maps the platform's built-in role slugs to the module roles: owners
// and admins are ticket admins, operators are ticket agents, members and
// auditors are ticket viewers.
var Grants = map[string][]string{
	"owner":    Roles["ticket admin"],
	"admin":    Roles["ticket admin"],
	"operator": Roles["ticket agent"],
	"member":   Roles["ticket viewer"],
	"auditor":  Roles["ticket viewer"],
}

// Methods proxied by the gateway: none (ticket gRPC is service to service).
var Methods []gatewayclient.Method

// Abilities are the CASL rules bound to the permissions.
var Abilities = []gatewayclient.Ability{
	{Action: []string{"read"}, Subject: []string{"Ticket", "TicketTag"}, Requires: "tickets:read"},
	{Action: []string{"create", "update", "assign", "comment", "reply"}, Subject: []string{"Ticket"}, Requires: "tickets:manage"},
	{Action: []string{"delete"}, Subject: []string{"Ticket"}, Requires: "tickets:delete"},
	{Action: []string{"manage"}, Subject: []string{"TicketTag"}, Requires: "tags:manage"},
	{Action: []string{"manage"}, Subject: []string{"TicketRule"}, Requires: "rules:manage"},
	{Action: []string{"manage"}, Subject: []string{"TicketMailbox"}, Requires: "mailboxes:manage"},
	{Action: []string{"read"}, Subject: []string{"TicketStats"}, Requires: "stats:read"},
	{Action: []string{"manage"}, Subject: []string{"TicketBackup"}, Requires: "backup:manage"},
}

// Nav lists the navigation contributions.
var Nav = []gatewayclient.NavEntry{
	{Title: "Tickets", Path: "/ticket", Icon: "mdi-ticket-outline", Order: 950, Requires: "tickets:read"},
	{Title: "Dashboard", Path: "/ticket/dashboard", Icon: "mdi-view-dashboard-outline", Order: 955, Requires: "stats:read"},
	{Title: "Rules", Path: "/ticket/rules", Icon: "mdi-filter-cog-outline", Order: 960, Requires: "rules:manage"},
	{Title: "Tags", Path: "/ticket/tags", Icon: "mdi-tag-multiple-outline", Order: 965, Requires: "tickets:read"},
	{Title: "Mailboxes", Path: "/ticket/mailboxes", Icon: "mdi-email-outline", Order: 970, Requires: "mailboxes:manage"},
}

// PermissionRefs lists "resource:action" for every declared permission.
func PermissionRefs() []string {
	out := make([]string, 0, len(Permissions))
	for _, p := range Permissions {
		out = append(out, p.Resource+":"+p.Action)
	}
	return out
}

// Routes derives the gateway routes from the OpenAPI document.
func Routes(doc *openapi3.T) ([]gatewayclient.Route, error) {
	known := map[string]bool{}
	for _, p := range PermissionRefs() {
		known[p] = true
	}
	var routes []gatewayclient.Route
	for p, item := range doc.Paths.Map() {
		for m, op := range item.Operations() {
			r := gatewayclient.Route{Method: strings.ToUpper(m), Path: p}
			perm, _ := op.Extensions[PermissionExtension].(string)
			public, _ := op.Extensions[PublicExtension].(bool)
			switch {
			case public:
				r.Public = true
			case perm == "":
				return nil, fmt.Errorf("ticketmanifest: %s %s declares no permission", r.Method, p)
			case !known[perm]:
				return nil, fmt.Errorf("ticketmanifest: %s %s uses undeclared permission %q", r.Method, p, perm)
			default:
				r.Permission = perm
			}
			if v, ok := op.Extensions[BodyLimitExtension]; ok {
				n, ok := v.(float64)
				if !ok || n <= 0 {
					return nil, fmt.Errorf("ticketmanifest: %s %s has a bad body limit", r.Method, p)
				}
				r.MaxBodyBytes = uint64(n)
			}
			if v, ok := op.Extensions[TimeoutExtension]; ok {
				n, ok := v.(float64)
				if !ok || n <= 0 || n > 600 {
					return nil, fmt.Errorf("ticketmanifest: %s %s has a bad timeout", r.Method, p)
				}
				r.Timeout = time.Duration(n) * time.Second
			}
			routes = append(routes, r)
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Method+" "+routes[i].Path < routes[j].Method+" "+routes[j].Path })
	return routes, nil
}

// Load parses the embedded document.
func Load() (*openapi3.T, error) {
	return openapi3.NewLoader().LoadFromData(openapi.Ticket)
}

// Manifest builds the gateway manifest from the embedded OpenAPI document.
func Manifest() (gatewayclient.Manifest, error) {
	doc, err := Load()
	if err != nil {
		return gatewayclient.Manifest{}, err
	}
	routes, err := Routes(doc)
	if err != nil {
		return gatewayclient.Manifest{}, err
	}
	return gatewayclient.Manifest{
		Module: Module, DisplayName: DisplayName, Version: Version,
		Prefixes:    []string{APIPrefix},
		Routes:      routes,
		Methods:     Methods,
		Permissions: Permissions,
		Abilities:   Abilities,
		Exposes:     []string{"./routes", "./nav"},
		Nav:         Nav,
	}, nil
}

// BuiltinRoles lists the built-in role slugs seeded, in a stable order.
var BuiltinRoles = []string{"owner", "admin", "member", "auditor", "operator"}

// SeedRequest builds the auth registration request: every module permission
// plus the built-in role grants.
func SeedRequest() *authv1.RegisterPermissionsRequest {
	req := &authv1.RegisterPermissionsRequest{}
	for _, p := range Permissions {
		req.Permissions = append(req.Permissions, &authv1.PermissionDef{Resource: p.Resource, Action: p.Action, Description: p.Description})
	}
	for _, slug := range BuiltinRoles {
		req.BuiltinGrants = append(req.BuiltinGrants, &authv1.BuiltinGrant{Role: slug, Permissions: Grants[slug]})
	}
	return req
}

// SeedPermissions registers the module's permissions with the auth service and
// grants them to the built-in roles (idempotent).
func SeedPermissions(ctx context.Context, cc grpc.ClientConnInterface) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := authv1.NewAuthorizationClient(cc).RegisterPermissions(ctx, SeedRequest())
	return err
}
