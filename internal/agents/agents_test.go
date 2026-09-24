package agents

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
)

const tn = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

type fakeProfiles struct {
	authv1.ProfilesClient
	members   []string
	names     map[string]string
	listErr   error
	lookupErr error
	lists     int
}

func (f *fakeProfiles) ListMembers(_ context.Context, in *authv1.ListMembersRequest, _ ...grpc.CallOption) (*authv1.ListMembersResponse, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	// two pages to exercise the cursor
	if in.GetCursor() == "" && len(f.members) > 1 {
		return &authv1.ListMembersResponse{UserIds: f.members[:1], NextCursor: f.members[0]}, nil
	}
	if in.GetCursor() != "" {
		return &authv1.ListMembersResponse{UserIds: f.members[1:]}, nil
	}
	return &authv1.ListMembersResponse{UserIds: f.members}, nil
}

func (f *fakeProfiles) Lookup(_ context.Context, in *authv1.LookupProfilesRequest, _ ...grpc.CallOption) (*authv1.LookupProfilesResponse, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	out := &authv1.LookupProfilesResponse{}
	for _, id := range in.GetUserIds() {
		if n, ok := f.names[id]; ok {
			out.Profiles = append(out.Profiles, &authv1.PublicProfile{UserId: id, DisplayName: n})
		}
	}
	return out, nil
}

func TestAuthDirectory(t *testing.T) {
	ctx := context.Background()
	fp := &fakeProfiles{members: []string{"u-zed", "u-ada", "u-viewer"}, names: map[string]string{"u-zed": "Zed", "u-ada": "ada", "u-viewer": "Vic", "u-new": "Newbie"}}
	grants := authz.Static{"u-zed": {authz.TicketsManage}, "u-ada": {authz.TicketsManage}, "u-viewer": {authz.TicketsRead}}
	now := time.Unix(1_700_000_000, 0)
	d := &Auth{Profiles: fp, Perms: grants, TTL: time.Minute, Now: func() time.Time { return now }}

	users, err := d.Assignable(ctx, tn)
	if err != nil || len(users) != 2 || users[0].Name != "ada" || users[1].ID != "u-zed" {
		t.Fatalf("assignable = %+v %v", users, err)
	}
	if _, err := d.Assignable(ctx, tn); err != nil || fp.lists != 2 {
		t.Fatalf("cache miss: lists=%d", fp.lists)
	}
	u, err := d.Get(ctx, tn, "u-ada")
	if err != nil || u.Name != "ada" {
		t.Fatalf("get = %+v %v", u, err)
	}
	if _, err := d.Get(ctx, tn, "u-viewer"); !errors.Is(err, ErrNotAssignable) {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := d.Get(ctx, tn, "u-ghost"); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("ghost: %v", err)
	}
	if _, err := d.Get(ctx, tn, ""); !errors.Is(err, ErrUnknownUser) {
		t.Fatal("empty id")
	}
	grants["u-new"] = []string{authz.TicketsManage}
	if u, err := d.Get(ctx, tn, "u-new"); err != nil || u.Name != "Newbie" {
		t.Fatalf("just granted = %+v %v", u, err)
	}

	// directory down: cached list still serves; uncached tenant errors
	fp.listErr = errors.New("down")
	now = now.Add(2 * time.Minute)
	if users, err := d.Assignable(ctx, tn); err == nil && len(users) == 0 {
		t.Fatal("stale list empty")
	}
	d.Forget(tn)
	if _, err := d.Assignable(ctx, tn); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("uncached + down: %v", err)
	}
	if _, err := d.Get(ctx, tn, "u-ada"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("get while down")
	}
	fp.listErr = nil
	fp.lookupErr = errors.New("down")
	if _, err := d.Assignable(ctx, "other-tenant"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("lookup failure")
	}
	fp.lookupErr = nil
	if _, err := d.Assignable(ctx, tn); err != nil {
		t.Fatal(err)
	}
	fp.lookupErr = fmt.Errorf("down")
	if _, err := d.Get(ctx, tn, "u-unlisted"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("direct lookup failure")
	}
	fp.lookupErr = nil
	noPerms := &Auth{Profiles: fp}
	if users, _ := noPerms.Assignable(ctx, tn); len(users) != 0 {
		t.Fatal("nil checker must yield nobody")
	}
	if _, err := noPerms.Get(ctx, tn, "u-ada"); !errors.Is(err, ErrNotAssignable) {
		t.Fatal("nil checker get")
	}
	if New(nil, grants).TTL != time.Minute || (&Auth{}).now().IsZero() {
		t.Fatal("constructor defaults")
	}
	single := &Auth{Profiles: &fakeProfiles{members: []string{"u-zed"}, names: map[string]string{"u-zed": ""}}, Perms: grants, TTL: time.Minute}
	if users, _ := single.Assignable(ctx, tn); len(users) != 1 || users[0].DisplayName() != "u-zed" {
		t.Fatalf("name fallback = %+v", users)
	}
}

func TestFake(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	f.Add(tn, User{ID: "b", Name: "Bob"}, true)
	f.Add(tn, User{ID: "a", Name: "Ann"}, true)
	f.Add(tn, User{ID: "v", Name: "Vic"}, false)
	users, err := f.Assignable(ctx, tn)
	if err != nil || len(users) != 2 || users[0].ID != "a" {
		t.Fatalf("assignable = %+v", users)
	}
	if u, err := f.Get(ctx, tn, "b"); err != nil || u.DisplayName() != "Bob" {
		t.Fatal("get")
	}
	if _, err := f.Get(ctx, tn, "v"); !errors.Is(err, ErrNotAssignable) {
		t.Fatal("not assignable")
	}
	if _, err := f.Get(ctx, "other", "a"); !errors.Is(err, ErrUnknownUser) {
		t.Fatal("tenant isolation")
	}
	f.Err = ErrUnavailable
	if _, err := f.Assignable(ctx, tn); !errors.Is(err, ErrUnavailable) {
		t.Fatal("err assignable")
	}
	if _, err := f.Get(ctx, tn, "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("err get")
	}
}
