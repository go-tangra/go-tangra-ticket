// Package agents answers "who may work tickets" (research D9): the assignable
// users of a tenant are its active members holding tickets:manage, resolved
// through the auth service (Profiles for membership and display names,
// Authorization/Check for the permission) over the Freya SPIFFE channel.
// Results are cached per tenant so assignment of an already-known agent keeps
// working while the directory is briefly unreachable. Phone numbers are never
// returned by auth; e-mail addresses are not exposed by the profile API, so
// User.Email stays empty unless a future directory provides it.
package agents

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
)

// Errors.
var (
	ErrUnknownUser   = errors.New("agents: unknown user")
	ErrNotAssignable = errors.New("agents: user may not work tickets")
	ErrUnavailable   = errors.New("agents: directory unavailable")
)

// User is an agent as the ticket module sees it.
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// Directory resolves assignable agents.
type Directory interface {
	// Assignable lists the tenant's users holding tickets:manage, by name.
	Assignable(ctx context.Context, tenantID string) ([]User, error)
	// Get resolves one assignable user: ErrUnknownUser when not a member,
	// ErrNotAssignable when the member lacks tickets:manage.
	Get(ctx context.Context, tenantID, userID string) (User, error)
}

// DisplayName is the user's name, falling back to the id.
func (u User) DisplayName() string {
	if u.Name != "" {
		return u.Name
	}
	return u.ID
}

// ---- auth-backed implementation

type cached struct {
	users []User
	at    time.Time
}

// Auth is the Directory over the auth service.
type Auth struct {
	Profiles authv1.ProfilesClient
	Perms    authz.Checker
	TTL      time.Duration
	Now      func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

// New builds the auth-backed directory on a mesh connection to "auth"; perms
// answers tickets:manage (the auth Authorization/Check adapter).
func New(conn grpc.ClientConnInterface, perms authz.Checker) *Auth {
	return &Auth{Profiles: authv1.NewProfilesClient(conn), Perms: perms, TTL: time.Minute}
}

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Auth) load(ctx context.Context, tenantID string) ([]User, error) {
	var ids []string
	cursor := ""
	for {
		res, err := a.Profiles.ListMembers(ctx, &authv1.ListMembersRequest{TenantId: tenantID, Cursor: cursor, Limit: 1000})
		if err != nil {
			return nil, ErrUnavailable
		}
		ids = append(ids, res.GetUserIds()...)
		if res.GetNextCursor() == "" || len(res.GetUserIds()) == 0 || len(ids) >= 100_000 {
			break
		}
		cursor = res.GetNextCursor()
	}
	var eligible []string
	for _, id := range ids {
		if a.Perms != nil && a.Perms.Has(ctx, tenantID, id, authz.TicketsManage) {
			eligible = append(eligible, id)
		}
	}
	out := make([]User, 0, len(eligible))
	for i := 0; i < len(eligible); i += 100 {
		end := min(i+100, len(eligible))
		res, err := a.Profiles.Lookup(ctx, &authv1.LookupProfilesRequest{TenantId: tenantID, UserIds: eligible[i:end]})
		if err != nil {
			return nil, ErrUnavailable
		}
		named := map[string]string{}
		for _, p := range res.GetProfiles() {
			named[p.GetUserId()] = p.GetDisplayName()
		}
		for _, id := range eligible[i:end] {
			out = append(out, User{ID: id, Name: named[id]})
		}
	}
	sortUsers(out)
	return out, nil
}

func sortUsers(out []User) {
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].DisplayName()), strings.ToLower(out[j].DisplayName())
		if li != lj {
			return li < lj
		}
		return out[i].ID < out[j].ID
	})
}

// Assignable implements Directory (cached for TTL; a stale list is served when
// the directory fails).
func (a *Auth) Assignable(ctx context.Context, tenantID string) ([]User, error) {
	a.mu.Lock()
	c, ok := a.cache[tenantID]
	a.mu.Unlock()
	if ok && a.now().Sub(c.at) < a.TTL {
		return append([]User(nil), c.users...), nil
	}
	users, err := a.load(ctx, tenantID)
	if err != nil {
		if ok {
			return append([]User(nil), c.users...), nil
		}
		return nil, err
	}
	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[string]cached{}
	}
	a.cache[tenantID] = cached{users: users, at: a.now()}
	a.mu.Unlock()
	return append([]User(nil), users...), nil
}

// Get implements Directory.
func (a *Auth) Get(ctx context.Context, tenantID, userID string) (User, error) {
	if userID == "" {
		return User{}, ErrUnknownUser
	}
	users, err := a.Assignable(ctx, tenantID)
	if err != nil {
		return User{}, err
	}
	for _, u := range users {
		if u.ID == userID {
			return u, nil
		}
	}
	// Not in the (possibly cached) list: ask directly so a just-granted agent works.
	res, err := a.Profiles.Lookup(ctx, &authv1.LookupProfilesRequest{TenantId: tenantID, UserIds: []string{userID}})
	if err != nil {
		return User{}, ErrUnavailable
	}
	for _, p := range res.GetProfiles() {
		if p.GetUserId() != userID {
			continue
		}
		if a.Perms == nil || !a.Perms.Has(ctx, tenantID, userID, authz.TicketsManage) {
			return User{}, ErrNotAssignable
		}
		a.Forget(tenantID)
		return User{ID: userID, Name: p.GetDisplayName()}, nil
	}
	return User{}, ErrUnknownUser
}

// Forget drops the tenant's cached list.
func (a *Auth) Forget(tenantID string) {
	a.mu.Lock()
	delete(a.cache, tenantID)
	a.mu.Unlock()
}

// ---- fake

// Fake is an in-memory Directory for tests: users per tenant, each marked
// assignable or not.
type Fake struct {
	mu    sync.Mutex
	users map[string]map[string]fakeUser
	// Err, when set, fails every call.
	Err error
}

type fakeUser struct {
	User
	assignable bool
}

// NewFake builds an empty directory.
func NewFake() *Fake { return &Fake{users: map[string]map[string]fakeUser{}} }

// Add registers a member; assignable marks tickets:manage.
func (f *Fake) Add(tenantID string, u User, assignable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.users[tenantID] == nil {
		f.users[tenantID] = map[string]fakeUser{}
	}
	f.users[tenantID][u.ID] = fakeUser{User: u, assignable: assignable}
}

// Assignable implements Directory.
func (f *Fake) Assignable(_ context.Context, tenantID string) ([]User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	out := []User{}
	for _, u := range f.users[tenantID] {
		if u.assignable {
			out = append(out, u.User)
		}
	}
	sortUsers(out)
	return out, nil
}

// Get implements Directory.
func (f *Fake) Get(_ context.Context, tenantID, userID string) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return User{}, f.Err
	}
	u, ok := f.users[tenantID][userID]
	if !ok {
		return User{}, ErrUnknownUser
	}
	if !u.assignable {
		return User{}, ErrNotAssignable
	}
	return u.User, nil
}

var (
	_ Directory = (*Auth)(nil)
	_ Directory = (*Fake)(nil)
)
