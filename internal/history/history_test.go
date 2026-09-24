package history

import (
	"context"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

const tn = "11111111-1111-7111-8111-111111111111"

func TestAppendAndList(t *testing.T) {
	st := memstore.New()
	ctx := context.Background()
	tk := store.Ticket{ID: store.NewID(), TenantID: tn, Subject: "x"}
	if err := st.CreateTicket(ctx, tk); err != nil {
		t.Fatal(err)
	}
	l := New(st)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	l.SetClock(func() time.Time { return at })
	if err := l.Append(ctx, tn, tk.ID, store.FieldStatus, "open", "open", store.ActorAgent, "u"); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, tn, tk.ID, store.FieldStatus, "open", "closed", store.ActorAgent, "u"); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, tn, "missing", store.FieldStatus, "open", "closed", store.ActorAgent, "u"); err == nil {
		t.Fatal("history for a missing ticket accepted")
	}
	h, err := l.List(ctx, tn, tk.ID)
	if err != nil || len(h) != 1 || h[0].NewValue != "closed" || !h[0].CreatedAt.Equal(at) || h[0].ID == "" {
		t.Fatalf("list = %+v %v", h, err)
	}
	if h, err := l.List(ctx, tn, "other"); err != nil || h == nil || len(h) != 0 {
		t.Fatalf("empty list = %#v %v", h, err)
	}
	st.FailNext("ListHistory")
	if _, err := l.List(ctx, tn, tk.ID); err == nil {
		t.Fatal("store failure swallowed")
	}
}

func TestActorKind(t *testing.T) {
	cases := map[string]authz.Subjects{
		store.ActorAgent:   {ActorKind: authz.ActorAgent},
		store.ActorInbound: authz.SystemFor(tn),
		store.ActorSystem:  authz.Service(tn, "spiffe://x"),
	}
	for want, s := range cases {
		if got := ActorKind(s); got != want {
			t.Errorf("%+v => %s, want %s", s, got, want)
		}
	}
	if ActorKind(authz.Internal(tn)) != store.ActorSystem {
		t.Fatal("internal")
	}
}
