//go:build integration

// Package repodb integration test: runs the repo conformance suite against a
// real TimescaleDB (testcontainers) and adds the checks only a database can
// make: migrations are idempotent, row-level security isolates two tenants even
// for raw SQL under the app role, the composite (tenant_id, id) foreign keys
// refuse cross-tenant links, and ticket_route_mailbox returns routing fields
// only. Run with:
//
//	go test -tags integration ./internal/repo/repodb/
//
// It skips cleanly when Docker/testcontainers is unavailable. Set
// TICKET_IT_ADMIN_DSN (a superuser DSN of an existing TimescaleDB server) to
// run against it instead: a throwaway database is created and dropped.
package repodb_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

type dbEnv struct {
	adminDSN, appDSN string
}

// existingDB prepares a throwaway database on the server named by
// TICKET_IT_ADMIN_DSN.
func existingDB(t *testing.T, admin string) dbEnv {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Skipf("TICKET_IT_ADMIN_DSN unreachable: %v", err)
	}
	name := "ticket_it_" + strings.ReplaceAll(store.NewID()[:13], "-", "")
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	var roleExists bool
	_ = conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ticket_app')").Scan(&roleExists)
	if !roleExists {
		if _, err := conn.Exec(ctx, "CREATE ROLE ticket_app LOGIN PASSWORD 'app' NOBYPASSRLS"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		if !roleExists {
			_, _ = conn.Exec(ctx, "DROP ROLE IF EXISTS ticket_app")
		}
		_ = conn.Close(ctx)
	})
	adminCfg := cfg.Copy()
	adminCfg.Database = name
	host := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	env := dbEnv{
		adminDSN: fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", cfg.User, cfg.Password, host, name),
		appDSN:   "postgres://ticket_app:app@" + host + "/" + name + "?sslmode=disable",
	}
	db, err := pgx.Connect(ctx, env.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); err != nil {
		t.Skipf("timescaledb unavailable: %v", err)
	}
	_ = db.Close(ctx)
	if !roleExists {
		return env
	}
	t.Skip("role ticket_app already exists on this server; refusing to reuse it")
	return env
}

func startDB(t *testing.T) dbEnv {
	t.Helper()
	ctx := context.Background()
	if admin := os.Getenv("TICKET_IT_ADMIN_DSN"); admin != "" {
		env := existingDB(t, admin)
		if err := store.Migrate(ctx, env.adminDSN); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if err := store.Migrate(ctx, env.adminDSN); err != nil {
			t.Fatalf("migrate idempotent: %v", err)
		}
		return env
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"},
			Env:        map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "ticket"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		}, Started: true,
	})
	if err != nil {
		t.Skipf("testcontainers unavailable: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	env := dbEnv{
		adminDSN: "postgres://postgres:test@" + host + ":" + port.Port() + "/ticket?sslmode=disable",
		appDSN:   "postgres://ticket_app:app@" + host + ":" + port.Port() + "/ticket?sslmode=disable",
	}
	conn, err := pgx.Connect(ctx, env.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE ROLE ticket_app LOGIN PASSWORD 'app' NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	if err := store.Migrate(ctx, env.adminDSN); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.Migrate(ctx, env.adminDSN); err != nil {
		t.Fatalf("migrate idempotent: %v", err)
	}
	return env
}

func TestRepoDB(t *testing.T) {
	env := startDB(t)
	ctx := context.Background()
	// One database for the whole suite: each case truncates first.
	st, err := store.Open(ctx, env.appDSN, 4)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(st.Close)
	db := repodb.New(st)
	admin, err := pgx.Connect(ctx, env.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	truncate := func(t *testing.T) {
		t.Helper()
		if _, err := admin.Exec(ctx, `TRUNCATE ticket_tag_links, ticket_history, ticket_attachments, ticket_comments, ticket_tickets, ticket_tags, ticket_rules, ticket_mailboxes CASCADE`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("conformance", func(t *testing.T) {
		repotest.Run(t, func(t *testing.T) repo.Store { truncate(t); return db })
	})

	t.Run("rls isolates raw sql", func(t *testing.T) {
		truncate(t)
		tk := repotest.NewTicket(repotest.TenantA, "tenant A secret", time.Now())
		if err := db.CreateTicket(ctx, tk); err != nil {
			t.Fatal(err)
		}
		var n int
		err := st.Tx(ctx, store.Scope{TenantID: repotest.TenantB}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM ticket_tickets`).Scan(&n)
		})
		if err != nil || n != 0 {
			t.Fatalf("tenant B sees %d rows (%v)", n, err)
		}
		// no scope at all: the policy's cast of an unset tenant admits nothing
		err = st.Tx(ctx, store.Scope{TenantID: "00000000-0000-0000-0000-000000000000"}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM ticket_tickets`).Scan(&n)
		})
		if err != nil || n != 0 {
			t.Fatalf("nil tenant sees %d rows (%v)", n, err)
		}
		// writing a row for another tenant under tenant B's scope is refused by WITH CHECK
		err = st.Tx(ctx, store.Scope{TenantID: repotest.TenantB}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO ticket_tags (id, tenant_id, name) VALUES ($1, $2, 'x')`, store.NewID(), repotest.TenantA)
			return err
		})
		if err == nil {
			t.Fatal("cross-tenant insert admitted")
		}
	})

	t.Run("composite keys refuse cross-tenant links", func(t *testing.T) {
		truncate(t)
		tk := repotest.NewTicket(repotest.TenantA, "A", time.Now())
		if err := db.CreateTicket(ctx, tk); err != nil {
			t.Fatal(err)
		}
		tagB := store.Tag{ID: store.NewID(), TenantID: repotest.TenantB, Name: "b", Kind: store.KindTag}
		if err := db.CreateTag(ctx, tagB); err != nil {
			t.Fatal(err)
		}
		// even with the system scope (no RLS filter) the FK refuses tenant B's tag on tenant A's ticket
		err := st.Tx(ctx, store.Scope{System: true}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO ticket_tag_links (tenant_id, ticket_id, tag_id) VALUES ($1, $2, $3)`, repotest.TenantA, tk.ID, tagB.ID)
			return err
		})
		if err == nil {
			t.Fatal("cross-tenant tag link admitted")
		}
		c := store.Comment{ID: store.NewID(), TenantID: repotest.TenantB, TicketID: tk.ID, Body: "x", AuthorKind: store.AuthorAgent}
		if err := db.CreateComment(ctx, c); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("cross-tenant comment: %v", err)
		}
	})

	t.Run("route mailbox returns routing fields only", func(t *testing.T) {
		truncate(t)
		mb := store.Mailbox{ID: store.NewID(), TenantID: repotest.TenantA, Address: "support@acme.example", DisplayName: "Acme", Active: true, AutoAck: true, AutoAckTemplate: "hi"}
		if err := db.CreateMailbox(ctx, mb); err != nil {
			t.Fatal(err)
		}
		conn, err := pgx.Connect(ctx, env.appDSN)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		rows, err := conn.Query(ctx, `SELECT * FROM ticket_route_mailbox('SUPPORT@acme.example')`)
		if err != nil {
			t.Fatal(err)
		}
		cols := rows.FieldDescriptions()
		rows.Close()
		want := []string{"tenant_id", "mailbox_id", "display_name", "active", "auto_ack", "auto_ack_template"}
		if len(cols) != len(want) {
			t.Fatalf("columns = %d", len(cols))
		}
		for i, c := range cols {
			if c.Name != want[i] {
				t.Fatalf("column %d = %s", i, c.Name)
			}
		}
		// the app role cannot read mailboxes outside a tenant scope directly
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM ticket_mailboxes`).Scan(&n); err == nil && n != 0 {
			t.Fatalf("unscoped app role read %d mailboxes", n)
		}
		r, err := db.RouteMailbox(ctx, " Support@ACME.example ")
		if err != nil || r.TenantID != repotest.TenantA || r.MailboxID != mb.ID {
			t.Fatalf("route = %+v %v", r, err)
		}
	})

	t.Run("malformed ids are not found", func(t *testing.T) {
		truncate(t)
		if _, err := db.GetTicket(ctx, repotest.TenantA, "not-a-uuid"); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("get: %v", err)
		}
		items, total, err := db.ListTickets(ctx, repotest.TenantA, store.TicketFilter{TagID: "nope"})
		if err != nil || total != 0 || len(items) != 0 {
			t.Fatalf("list bad tag: %v", err)
		}
		m, err := db.TagsForTickets(ctx, repotest.TenantA, []string{"nope"})
		if err != nil || len(m) != 0 {
			t.Fatalf("tags for bad id: %v", err)
		}
	})
}
