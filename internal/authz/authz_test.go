package authz

import (
	"context"
	"errors"
	"testing"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

var ctx = context.Background()

func TestRequireAgent(t *testing.T) {
	c := Static{"viewer": {TicketsRead}, "agent": {TicketsRead, TicketsManage, TagsManage}}
	viewer := Subjects{TenantID: tn, UserID: "viewer", ActorKind: ActorAgent}
	agent := Subjects{TenantID: tn, UserID: "agent", ActorKind: ActorAgent}
	if err := Require(ctx, c, viewer, TicketsRead); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{TicketsManage, TicketsDelete, RulesManage, BackupManage} {
		if err := Require(ctx, c, viewer, p); !errors.Is(err, ErrForbidden) {
			t.Fatalf("viewer %s: %v", p, err)
		}
	}
	if !Allowed(ctx, c, agent, TagsManage) || Allowed(ctx, c, agent, MailboxesManage) {
		t.Fatal("agent grants")
	}
	if Allowed(ctx, nil, agent, TicketsRead) {
		t.Fatal("nil checker must refuse")
	}
	if Allowed(ctx, c, Subjects{TenantID: tn, ActorKind: ActorAgent}, TicketsRead) {
		t.Fatal("agent without user id")
	}
	if Allowed(ctx, c, Subjects{UserID: "agent", ActorKind: ActorAgent}, TicketsRead) {
		t.Fatal("no tenant")
	}
	if Allowed(ctx, c, agent, "tickets:fly") {
		t.Fatal("unknown permission")
	}
	if Allowed(ctx, c, Subjects{TenantID: tn, UserID: "agent", ActorKind: "robot"}, TicketsRead) {
		t.Fatal("unknown actor kind")
	}
	called := false
	fn := CheckerFunc(func(_ context.Context, tenant, user, perm string) bool {
		called = true
		return tenant == tn && user == "agent" && perm == StatsRead
	})
	if !Allowed(ctx, fn, agent, StatsRead) || !called {
		t.Fatal("checker func")
	}
}

func TestRequireNonHuman(t *testing.T) {
	in := SystemFor(tn)
	if in.TenantID != tn || in.UserID != InboundActorID || in.ActorKind != ActorInbound || in.IsHuman() {
		t.Fatalf("SystemFor = %+v", in)
	}
	for _, p := range []string{TicketsRead, TicketsManage, TagsManage} {
		if err := Require(ctx, nil, in, p); err != nil {
			t.Fatalf("inbound %s: %v", p, err)
		}
	}
	for _, p := range []string{TicketsDelete, RulesManage, MailboxesManage, BackupManage, StatsRead} {
		if Allowed(ctx, nil, in, p) {
			t.Fatalf("inbound must not hold %s", p)
		}
	}
	if Allowed(ctx, nil, SystemFor(""), TicketsRead) {
		t.Fatal("inbound without tenant")
	}
	svc := Service(tn, "spiffe://example.org/svc/monitor")
	if !Allowed(ctx, nil, svc, TicketsManage) || Allowed(ctx, nil, svc, TicketsDelete) {
		t.Fatal("service grants")
	}
	if Allowed(ctx, nil, Service(tn, ""), TicketsRead) {
		t.Fatal("service without identity")
	}
	sys := Internal(tn)
	for _, p := range Permissions {
		if !Allowed(ctx, nil, sys, p) {
			t.Fatalf("system %s", p)
		}
	}
}

func TestSubjectsHelpers(t *testing.T) {
	a := Subjects{TenantID: tn, UserID: "u", Roles: []string{"member", RolePlatformAdmin}, ActorKind: ActorAgent}
	if !a.HasRole("member") || a.HasRole("owner") || !a.IsPlatformAdmin() || !a.IsHuman() || a.ActorID() != "u" {
		t.Fatal("helpers")
	}
	if (Subjects{ActorKind: ActorAgent}).IsPlatformAdmin() || !Internal(tn).IsPlatformAdmin() {
		t.Fatal("platform admin")
	}
	if (Subjects{ActorKind: ActorSystem}).ActorID() != ActorSystem {
		t.Fatal("actor id fallback")
	}
	if err := RequirePlatformAdmin(a); err != nil {
		t.Fatal(err)
	}
	if err := RequirePlatformAdmin(Subjects{ActorKind: ActorAgent}); !errors.Is(err, ErrForbidden) {
		t.Fatal("non-admin")
	}
	if err := RequireTenant(a, tn); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(RequireTenant(a, "other"), ErrForbidden) || !errors.Is(RequireTenant(a, ""), ErrForbidden) {
		t.Fatal("tenant mismatch")
	}
	if !Known(BackupManage) || Known("x:y") {
		t.Fatal("known")
	}
	if r, act, ok := Split(TicketsManage); !ok || r != "tickets" || act != "manage" {
		t.Fatal("split")
	}
	for _, bad := range []string{"tickets", ":read", "tickets:"} {
		if _, _, ok := Split(bad); ok {
			t.Fatalf("split %q", bad)
		}
	}
	if len(Permissions) != 8 {
		t.Fatalf("permissions = %d", len(Permissions))
	}
}
