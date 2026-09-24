// Package authz is the ticket module's access model (research D11). Every
// browser route declares one API permission (x-freya-permission); the module
// enforces it itself (defence in depth behind the gateway) by asking the auth
// service through a Checker. Callers are scoped to exactly one tenant.
//
// Non-human actors never go through the Checker:
//   - the inbound mail edge acts as SystemFor(tenant): a subject pinned to the
//     tenant the recipient mailbox routed to, allowed only what ingestion needs
//     (read/manage tickets, create tags from rules) — never a bypass;
//   - mesh services (gRPC, SPIFFE-authenticated and policy allow-listed) may
//     read and manage tickets of the tenant named in the request;
//   - the system scope (internal maintenance) may do everything within its tenant.
package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrForbidden is returned when a caller lacks the required permission or scope.
var ErrForbidden = errors.New("authz: forbidden")

// Actor kinds (closed set).
const (
	ActorAgent   = "agent"   // a signed-in platform user (via the gateway)
	ActorInbound = "inbound" // the inbound mail edge, pinned to the routed tenant
	ActorService = "service" // a mesh peer (SPIFFE) acting for a tenant
	ActorSystem  = "system"  // trusted internal maintenance
)

// InboundActorID is the actor id recorded for inbound-mail writes.
const InboundActorID = "inbound-mail"

// API permissions (resource:action).
const (
	TicketsRead     = "tickets:read"
	TicketsManage   = "tickets:manage"
	TicketsDelete   = "tickets:delete"
	TagsManage      = "tags:manage"
	RulesManage     = "rules:manage"
	MailboxesManage = "mailboxes:manage"
	StatsRead       = "stats:read"
	BackupManage    = "backup:manage"
)

// Permissions lists every module permission.
var Permissions = []string{TicketsRead, TicketsManage, TicketsDelete, TagsManage, RulesManage, MailboxesManage, StatsRead, BackupManage}

// RolePlatformAdmin confers cross-tenant maintenance (full restore).
const RolePlatformAdmin = "platform-admin"

// inboundAllowed is what the inbound edge may do within its routed tenant.
var inboundAllowed = map[string]bool{TicketsRead: true, TicketsManage: true, TagsManage: true}

// serviceAllowed is what a mesh peer may do (contracts §C).
var serviceAllowed = map[string]bool{TicketsRead: true, TicketsManage: true}

// Subjects is the authenticated caller.
type Subjects struct {
	TenantID  string
	UserID    string
	Roles     []string
	ActorKind string // agent | inbound | service | system
}

// Checker answers "does this user hold this API permission in this tenant"
// (the auth service's Authorization/Check; a map in tests). A failed lookup is
// a "no".
type Checker interface {
	Has(ctx context.Context, tenantID, userID, permission string) bool
}

// CheckerFunc adapts a function to Checker.
type CheckerFunc func(ctx context.Context, tenantID, userID, permission string) bool

// Has implements Checker.
func (f CheckerFunc) Has(ctx context.Context, tenantID, userID, permission string) bool {
	return f(ctx, tenantID, userID, permission)
}

// Static is a Checker over a fixed user → permissions map (tests, dev).
type Static map[string][]string

// Has implements Checker.
func (s Static) Has(_ context.Context, _, userID, permission string) bool {
	for _, p := range s[userID] {
		if p == permission {
			return true
		}
	}
	return false
}

// SystemFor returns the scoped subject the inbound edge writes under: pinned to
// the routed tenant, recorded as "inbound-mail".
func SystemFor(tenantID string) Subjects {
	return Subjects{TenantID: tenantID, UserID: InboundActorID, ActorKind: ActorInbound}
}

// Internal returns the trusted system subject for one tenant.
func Internal(tenantID string) Subjects {
	return Subjects{TenantID: tenantID, UserID: ActorSystem, ActorKind: ActorSystem}
}

// Service returns the subject of a mesh peer acting for tenantID.
func Service(tenantID, spiffeID string) Subjects {
	return Subjects{TenantID: tenantID, UserID: spiffeID, ActorKind: ActorService}
}

// HasRole reports whether the caller carries role.
func (s Subjects) HasRole(role string) bool {
	for _, r := range s.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// IsPlatformAdmin reports whether the caller may run cross-tenant maintenance
// (full/cross-tenant restore): the platform-admin role, or the system scope.
func (s Subjects) IsPlatformAdmin() bool {
	return s.ActorKind == ActorSystem || s.HasRole(RolePlatformAdmin)
}

// ActorID is the user id, falling back to the actor kind.
func (s Subjects) ActorID() string {
	if s.UserID != "" {
		return s.UserID
	}
	return s.ActorKind
}

// IsHuman reports whether the caller is a signed-in agent.
func (s Subjects) IsHuman() bool { return s.ActorKind == ActorAgent }

// Known reports whether perm is a module permission.
func Known(perm string) bool {
	for _, p := range Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// Require checks that the caller holds perm within its own tenant. Agents are
// checked through c (nil c refuses); inbound and service actors are limited to
// their fixed allow-lists; the system scope is allowed everything.
func Require(ctx context.Context, c Checker, s Subjects, perm string) error {
	if s.TenantID == "" {
		return fmt.Errorf("%w: tenant required", ErrForbidden)
	}
	if !Known(perm) {
		return fmt.Errorf("%w: unknown permission %q", ErrForbidden, perm)
	}
	switch s.ActorKind {
	case ActorSystem:
		return nil
	case ActorInbound:
		if inboundAllowed[perm] {
			return nil
		}
	case ActorService:
		if serviceAllowed[perm] && s.UserID != "" {
			return nil
		}
	case ActorAgent:
		if c != nil && s.UserID != "" && c.Has(ctx, s.TenantID, s.UserID, perm) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s required", ErrForbidden, perm)
}

// Allowed is Require as a boolean.
func Allowed(ctx context.Context, c Checker, s Subjects, perm string) bool {
	return Require(ctx, c, s, perm) == nil
}

// RequireTenant ensures the caller acts within tenantID (its own tenant only;
// there is no cross-tenant actor on the request paths).
func RequireTenant(s Subjects, tenantID string) error {
	if tenantID == "" || s.TenantID != tenantID {
		return fmt.Errorf("%w: tenant mismatch", ErrForbidden)
	}
	return nil
}

// RequirePlatformAdmin permits only platform admins (or the system scope).
func RequirePlatformAdmin(s Subjects) error {
	if s.IsPlatformAdmin() {
		return nil
	}
	return fmt.Errorf("%w: platform-admin required", ErrForbidden)
}

// Split returns the resource and action of a "resource:action" permission.
func Split(perm string) (resource, action string, ok bool) {
	resource, action, ok = strings.Cut(perm, ":")
	return resource, action, ok && resource != "" && action != ""
}
