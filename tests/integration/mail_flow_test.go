//go:build integration

// Package integration drives the whole email loop of the ticket module against
// real infrastructure (T075): TimescaleDB (migrations + RLS app role), Valkey
// (event bus), an S3 store (RustFS) for attachments and Mailpit as the SMTP
// relay. Only the auth peers (token verifier, permission checker, agent
// directory) are fakes; the relay token goes through the real secret resolver
// (a file: reference).
//
//	fixture in → ticket (+ attachment in S3, event on Valkey)
//	         → auto-acknowledgement in Mailpit (RFC 3834 headers)
//	agent reply out → Mailpit (threading headers + reference token)
//	requester's answer to that reply → threads back into the same ticket
//
// Run with Docker reachable (testcontainers starts every dependency):
//
//	sg docker -c 'go test -tags integration -count=1 ./tests/integration/...'
//
// or point it at running services instead (each variable replaces one
// container; the database is a throwaway one created and dropped on the server):
//
//	TICKET_IT_ADMIN_DSN     superuser DSN of a TimescaleDB server
//	TICKET_IT_APP_PASSWORD  password of an EXISTING ticket_app role (default: the role is created)
//	TICKET_IT_VALKEY        host:port (+ TICKET_IT_VALKEY_USER / TICKET_IT_VALKEY_PASSWORD)
//	TICKET_IT_S3            host:port (+ TICKET_IT_S3_ACCESS / TICKET_IT_S3_SECRET)
//	TICKET_IT_SMTP          host:port of Mailpit's SMTP listener
//	TICKET_IT_MAILPIT_API   http://host:port of Mailpit's API
//
// A dependency that is neither configured nor startable skips the test.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/agents"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/app"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/config"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/stream"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/stream/valkeykv"
	"github.com/go-tangra/go-tangra/v4"
)

const tenant = "22222222-2222-7222-8222-222222222222"

// ---- infrastructure

type infra struct {
	adminDSN, appDSN       string
	valkey, valkeyUser     string
	valkeyPass             string
	s3, s3Access, s3Secret string
	smtpHost               string
	smtpPort               int
	mailpitAPI             string
}

func start(t *testing.T, ctx context.Context, req testcontainers.ContainerRequest) (host string, ports map[string]string) {
	t.Helper()
	req.Name = ""
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		t.Skipf("testcontainers unavailable (%s): %v", req.Image, err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err = c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ports = map[string]string{}
	for _, p := range req.ExposedPorts {
		mp, err := c.MappedPort(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		ports[p] = mp.Port()
	}
	return host, ports
}

func setupDB(t *testing.T, ctx context.Context, in *infra) {
	t.Helper()
	admin := os.Getenv("TICKET_IT_ADMIN_DSN")
	if admin == "" {
		host, ports := start(t, ctx, testcontainers.ContainerRequest{
			Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"},
			Env:        map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "postgres"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		})
		admin = "postgres://postgres:test@" + net.JoinHostPort(host, ports["5432/tcp"]) + "/postgres?sslmode=disable"
	}
	cfg, err := pgx.ParseConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Skipf("database unreachable: %v", err)
	}
	name := "ticket_it_" + strings.ReplaceAll(store.NewID()[:13], "-", "")
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	appPass := os.Getenv("TICKET_IT_APP_PASSWORD")
	created := false
	var exists bool
	_ = conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ticket_app')").Scan(&exists)
	switch {
	case !exists:
		appPass, created = "it-app", true
		if _, err := conn.Exec(ctx, "CREATE ROLE ticket_app LOGIN PASSWORD 'it-app' NOBYPASSRLS"); err != nil {
			t.Fatal(err)
		}
	case appPass == "":
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
		t.Skip("role ticket_app exists on this server: set TICKET_IT_APP_PASSWORD to reuse it")
	}
	t.Cleanup(func() {
		c2, err := pgx.ConnectConfig(context.Background(), cfg)
		if err != nil {
			return
		}
		defer func() { _ = c2.Close(context.Background()) }()
		_, _ = c2.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		if created {
			_, _ = c2.Exec(context.Background(), "DROP ROLE IF EXISTS ticket_app")
		}
	})
	_ = conn.Close(ctx)
	host := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	in.adminDSN = fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", url.PathEscape(cfg.User), url.PathEscape(cfg.Password), host, name)
	in.appDSN = fmt.Sprintf("postgres://ticket_app:%s@%s/%s?sslmode=disable", url.PathEscape(appPass), host, name)
	db, err := pgx.Connect(ctx, in.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(ctx) }()
	if _, err := db.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); err != nil {
		t.Skipf("timescaledb unavailable: %v", err)
	}
}

func setupInfra(t *testing.T) *infra {
	t.Helper()
	ctx := context.Background()
	in := &infra{}
	setupDB(t, ctx, in)

	if in.valkey = os.Getenv("TICKET_IT_VALKEY"); in.valkey == "" {
		host, ports := start(t, ctx, testcontainers.ContainerRequest{
			Image: "valkey/valkey:8", ExposedPorts: []string{"6379/tcp"}, Cmd: []string{"valkey-server", "--requirepass", "it"},
			WaitingFor: wait.ForLog("Ready to accept connections").WithStartupTimeout(time.Minute),
		})
		in.valkey, in.valkeyPass = net.JoinHostPort(host, ports["6379/tcp"]), "it"
	} else {
		in.valkeyUser, in.valkeyPass = os.Getenv("TICKET_IT_VALKEY_USER"), os.Getenv("TICKET_IT_VALKEY_PASSWORD")
	}

	if in.s3 = os.Getenv("TICKET_IT_S3"); in.s3 == "" {
		host, ports := start(t, ctx, testcontainers.ContainerRequest{
			Image: "rustfs/rustfs:latest", ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"RUSTFS_ACCESS_KEY": "itaccess", "RUSTFS_SECRET_KEY": "it-secret-key-123", "RUSTFS_VOLUMES": "/data"},
			WaitingFor: wait.ForHTTP("/health").WithPort("9000/tcp").WithStartupTimeout(time.Minute),
		})
		in.s3, in.s3Access, in.s3Secret = net.JoinHostPort(host, ports["9000/tcp"]), "itaccess", "it-secret-key-123"
	} else {
		in.s3Access, in.s3Secret = os.Getenv("TICKET_IT_S3_ACCESS"), os.Getenv("TICKET_IT_S3_SECRET")
	}

	smtp, api := os.Getenv("TICKET_IT_SMTP"), os.Getenv("TICKET_IT_MAILPIT_API")
	if smtp == "" || api == "" {
		host, ports := start(t, ctx, testcontainers.ContainerRequest{
			Image: "axllent/mailpit:latest", ExposedPorts: []string{"1025/tcp", "8025/tcp"},
			WaitingFor: wait.ForHTTP("/livez").WithPort("8025/tcp").WithStartupTimeout(time.Minute),
		})
		smtp, api = net.JoinHostPort(host, ports["1025/tcp"]), "http://"+net.JoinHostPort(host, ports["8025/tcp"])
	}
	h, p, err := net.SplitHostPort(smtp)
	if err != nil {
		t.Fatal(err)
	}
	in.smtpHost, in.mailpitAPI = h, strings.TrimRight(api, "/")
	in.smtpPort, _ = strconv.Atoi(p)
	return in
}

// ---- app

type verifier struct{}

func (verifier) Verify(_ context.Context, token string) (authclient.Identity, error) {
	if token == "agent" {
		return authclient.Identity{UserID: "agent-1", TenantID: tenant}, nil
	}
	return authclient.Identity{}, fmt.Errorf("unauthenticated")
}

func buildApp(t *testing.T, in *infra, relayToken string) *app.App {
	t.Helper()
	tokFile := filepath.Join(t.TempDir(), "relay.token")
	if err := os.WriteFile(tokFile, []byte(relayToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.ServiceName, c.TrustDomain, c.Env = "ticket", "example.org", "dev"
	c.Server.GRPCAddr, c.Server.HTTPAddr, c.Admin.Addr = "127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0"
	c.Discovery.Static = map[string][]string{"lcm": {"127.0.0.1:1"}, "auth": {"127.0.0.1:1"}, "gateway": {"127.0.0.1:1"}, "warden": {"127.0.0.1:1"}}
	c.DB.DSN, c.DB.MigrateDSN = in.appDSN, in.adminDSN
	c.Valkey = config.Valkey{Addresses: []string{in.valkey}, Username: in.valkeyUser, Password: in.valkeyPass, AllowPlaintext: true}
	c.KEK = config.KEK{Source: "file", Path: "unused"}
	c.ObjectStore = config.ObjectStore{Endpoint: in.s3, Bucket: "ticket-it", Region: "us-east-1", AccessKey: in.s3Access, SecretKey: in.s3Secret, PresignTTL: 300}
	c.Inbound = config.Inbound{Addr: "127.0.0.1:0", InsecureDev: true, RelayTokenRef: "file:" + tokFile,
		MaxBodyBytes: 10 << 20, MaxAttachmentBytes: 25 << 20, MaxParts: 100, MaxDepth: 10}
	c.SMTP = config.SMTP{Host: in.smtpHost, Port: in.smtpPort, TLS: config.TLSNone, AllowPlaintext: true, MailDomain: "it.example.org", TimeoutSeconds: 15}
	c.Gateway = config.Gateway{Service: "gateway", Issuer: "https://localhost:8443"}
	dir := agents.NewFake()
	dir.Add(tenant, agents.User{ID: "agent-1", Name: "Ada Agent"}, true)
	a, err := app.Build(context.Background(), c, app.Options{
		KEK: make([]byte, 32), Migrate: true, Verifier: verifier{}, Agents: dir,
		Checker: authz.Static{"agent-1": {authz.TicketsRead, authz.TicketsManage, authz.MailboxesManage}},
		Freya:   []freya.Option{freya.WithInsecureLocalDev(), freya.WithAllowAllPolicy()},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(a.Close)
	return a
}

// ---- helpers

func api(t *testing.T, a *app.App, method, path, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, "https://localhost/api/ticket/v1"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer agent")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		r.Header.Set("X-CSRF-Token", "csrf")
	}
	w := httptest.NewRecorder()
	a.HTTP.Handler().ServeHTTP(w, r)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

type outcome struct {
	Outcome  string `json:"outcome"`
	TicketID string `json:"ticket_id"`
}

func deliver(t *testing.T, edge, token, recipient string, raw []byte) (int, outcome) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, edge+"/inbound/mail", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-Iris-Recipient", recipient)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var o outcome
	_ = json.NewDecoder(res.Body).Decode(&o)
	return res.StatusCode, o
}

func fixture(t *testing.T, name, from, to string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mail", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(raw, []byte(from), []byte(to))
}

type mpMessage struct {
	ID      string `json:"ID"`
	Subject string `json:"Subject"`
}

// waitMail polls Mailpit for messages to addr until want arrive.
func waitMail(t *testing.T, apiURL, addr string, want int) []mpMessage {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		res, err := http.Get(apiURL + "/api/v1/search?query=" + url.QueryEscape("to:"+addr))
		if err == nil {
			var out struct {
				Messages []mpMessage `json:"messages"`
			}
			_ = json.NewDecoder(res.Body).Decode(&out)
			_ = res.Body.Close()
			if len(out.Messages) >= want {
				return out.Messages
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("mailpit: fewer than %d messages to %s", want, addr)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func headers(t *testing.T, apiURL, id string) map[string][]string {
	t.Helper()
	res, err := http.Get(apiURL + "/api/v1/message/" + id + "/headers")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	h := map[string][]string{}
	if err := json.NewDecoder(res.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

func text(t *testing.T, apiURL, id string) string {
	t.Helper()
	res, err := http.Get(apiURL + "/api/v1/message/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var m struct {
		Text string `json:"Text"`
	}
	_ = json.NewDecoder(res.Body).Decode(&m)
	return m.Text
}

func first(h map[string][]string, k string) string {
	if v := h[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// ---- the flow

func TestMailFlow(t *testing.T) {
	in := setupInfra(t)
	const relay = "it-relay-token-0123456789abcdef"
	a := buildApp(t, in, relay)
	ctx := context.Background()
	edge := httptest.NewServer(a.Inbound.Handler())
	t.Cleanup(edge.Close)

	run := strings.ReplaceAll(store.NewID()[:13], "-", "")
	mailbox := "support-" + run + "@it.example.org"
	requester := "jane-" + run + "@customer.example"

	if code, out := api(t, a, "POST", "/mailboxes", `{"address":"`+mailbox+`","display_name":"IT Support","auto_ack":true}`); code != http.StatusCreated {
		t.Fatalf("mailbox: %d %v", code, out)
	}

	// Refusals: wrong token and unknown mailbox get the same generic body.
	plain := fixture(t, "plain.eml", "jane@customer.example", requester)
	if code, o := deliver(t, edge.URL, "wrong", mailbox, plain); code == http.StatusAccepted || o.Outcome != "refused" {
		t.Fatalf("wrong token: %d %+v", code, o)
	}
	if code, o := deliver(t, edge.URL, relay, "nobody-"+run+"@it.example.org", plain); code == http.StatusAccepted || o.Outcome != "refused" {
		t.Fatalf("unknown mailbox: %d %+v", code, o)
	}

	// 1. Fixture in → new ticket; redelivery is a duplicate.
	code, created := deliver(t, edge.URL, relay, mailbox, plain)
	if code != http.StatusAccepted || created.Outcome != "created" || created.TicketID == "" {
		t.Fatalf("inbound: %d %+v", code, created)
	}
	if code, dup := deliver(t, edge.URL, relay, mailbox, plain); code != http.StatusAccepted || dup.Outcome != "duplicate" || dup.TicketID != created.TicketID {
		t.Fatalf("redelivery: %d %+v", code, dup)
	}
	tk, err := a.Repo.GetTicket(ctx, tenant, created.TicketID)
	if err != nil || tk.Source != store.SourceEmail || tk.RequesterEmail != requester || tk.Subject != "Printer on floor 3 is jammed" {
		t.Fatalf("ticket: %+v %v", tk, err)
	}

	// Event on the real bus (ids/subject only, never the requester address).
	vc, err := valkeykv.New(valkeykv.Config{Addresses: []string{in.valkey}, Username: in.valkeyUser, Password: in.valkeyPass, AllowPlaintext: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	var sawCreated bool
	for deadline := time.Now().Add(10 * time.Second); !sawCreated && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		entries, err := vc.XRange(ctx, stream.Key(tenant), "0-0", 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(e.Fields["data"], requester) {
				t.Fatalf("event leaks the requester address: %v", e.Fields)
			}
			if e.Fields["type"] == "ticket.created" && strings.Contains(e.Fields["data"], created.TicketID) {
				sawCreated = true
			}
		}
	}
	if !sawCreated {
		t.Fatal("no ticket.created event on the Valkey stream")
	}

	// 2. Auto-acknowledgement in Mailpit with RFC 3834 headers.
	msgs := waitMail(t, in.mailpitAPI, requester, 1)
	ack := headers(t, in.mailpitAPI, msgs[0].ID)
	if first(ack, "Auto-Submitted") != "auto-replied" || first(ack, "Precedence") != "auto_reply" ||
		first(ack, "In-Reply-To") != "<plain-0001@mail.customer.example>" || !strings.HasPrefix(first(ack, "Subject"), "Re: Printer on floor 3") {
		t.Fatalf("ack headers: %v", ack)
	}
	if body := text(t, in.mailpitAPI, msgs[0].ID); !strings.Contains(body, "[#"+created.TicketID+"]") {
		t.Fatalf("ack lacks the ticket reference: %q", body)
	}

	// 3. Agent reply out → Mailpit, threaded to the conversation, not auto-submitted.
	code, reply := api(t, a, "POST", "/tickets/"+created.TicketID+"/reply", `{"body":"We are sending a technician."}`)
	if code != http.StatusCreated || reply["delivery"] != "sent" {
		t.Fatalf("reply: %d %v", code, reply)
	}
	msgs = waitMail(t, in.mailpitAPI, requester, 2)
	var out map[string][]string
	var outID string
	for _, m := range msgs {
		h := headers(t, in.mailpitAPI, m.ID)
		if first(h, "Auto-Submitted") == "" {
			out, outID = h, m.ID
		}
	}
	if out == nil {
		t.Fatal("agent reply not found in Mailpit")
	}
	mid := first(out, "Message-Id")
	if !strings.HasSuffix(mid, "@it.example.org>") || !strings.Contains(first(out, "References"), "<plain-0001@mail.customer.example>") ||
		first(out, "In-Reply-To") == "" || !strings.Contains(first(out, "From"), mailbox) || first(out, "Bcc") != "" {
		t.Fatalf("reply headers: %v", out)
	}
	if body := text(t, in.mailpitAPI, outID); !strings.Contains(body, "We are sending a technician.") || !strings.Contains(body, "[#"+created.TicketID+"]") {
		t.Fatalf("reply body: %q", body)
	}

	// 4. The requester answers the agent's reply → threads back into the ticket.
	answer := fmt.Sprintf("From: Jane Customer <%s>\r\nTo: IT Support <%s>\r\nSubject: Re: Printer on floor 3 is jammed\r\n"+
		"Date: Wed, 23 Sep 2026 09:00:00 +0000\r\nMessage-ID: <answer-%s@mail.customer.example>\r\nIn-Reply-To: %s\r\nReferences: <plain-0001@mail.customer.example> %s\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\nThanks, the technician fixed it.\r\n", requester, mailbox, run, mid, mid)
	code, threaded := deliver(t, edge.URL, relay, mailbox, []byte(answer))
	if code != http.StatusAccepted || threaded.Outcome != "threaded" || threaded.TicketID != created.TicketID {
		t.Fatalf("thread back: %d %+v", code, threaded)
	}
	code, list := api(t, a, "GET", "/tickets/"+created.TicketID+"/comments", "")
	items, _ := list["items"].([]any)
	var incoming bool
	for _, it := range items {
		c, _ := it.(map[string]any)
		if c["author_kind"] == "requester" && strings.Contains(fmt.Sprint(c["body"]), "technician fixed it") {
			incoming = true
		}
	}
	if code != http.StatusOK || !incoming {
		t.Fatalf("threaded comment missing: %d %v", code, list)
	}

	// 5. Attachments land in the object store and stream back through the module.
	att := fixture(t, "attachments.eml", "jane@customer.example", requester)
	code, withAtt := deliver(t, edge.URL, relay, mailbox, att)
	if code != http.StatusAccepted || withAtt.Outcome != "created" {
		t.Fatalf("attachments: %d %+v", code, withAtt)
	}
	atts, err := a.Repo.ListAttachments(ctx, tenant, withAtt.TicketID)
	if err != nil || len(atts) == 0 {
		t.Fatalf("attachments stored: %+v %v", atts, err)
	}
	r := httptest.NewRequest("GET", "https://localhost/api/ticket/v1/tickets/"+withAtt.TicketID+"/attachments/"+atts[0].ID, nil)
	r.Header.Set("Authorization", "Bearer agent")
	w := httptest.NewRecorder()
	a.HTTP.Handler().ServeHTTP(w, r)
	got, _ := io.ReadAll(w.Body)
	if w.Code != http.StatusOK || len(got) == 0 || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("attachment download: %d %d bytes %v", w.Code, len(got), w.Header())
	}

	// 6. Automated mail is ingested but never acknowledged (loop safety).
	auto := fixture(t, "auto-submitted.eml", "bob@customer.example", requester)
	before := len(waitMail(t, in.mailpitAPI, requester, 1))
	if code, o := deliver(t, edge.URL, relay, mailbox, auto); code != http.StatusAccepted || o.Outcome != "created" {
		t.Fatalf("auto-submitted: %d %+v", code, o)
	}
	time.Sleep(2 * time.Second)
	after := waitMail(t, in.mailpitAPI, requester, 1)
	for _, m := range after {
		if strings.Contains(m.Subject, "Out of office") {
			t.Fatalf("auto-submitted mail was acknowledged (%d → %d messages)", before, len(after))
		}
	}
}
