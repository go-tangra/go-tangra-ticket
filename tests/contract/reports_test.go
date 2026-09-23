package contract

// T067: the US6 reporting surface of contracts §A — /stats (stats:read, ?days
// window), /backup/export|import (backup:manage; cross-tenant and full restore
// platform-admin only) and /stream (tickets:read).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/go-freya/freya/internal/testrt"
	"github.com/go-freya/freya/internal/testutil"
	"github.com/go-freya/freya/services/auth/pkg/authclient"

	"github.com/go-freya/freya/services/ticket/internal/agents"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/backup"
	"github.com/go-freya/freya/services/ticket/internal/events"
	"github.com/go-freya/freya/services/ticket/internal/httpapi"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
	"github.com/go-freya/freya/services/ticket/internal/stats"
	"github.com/go-freya/freya/services/ticket/internal/stream"
	"github.com/go-freya/freya/services/ticket/internal/tickets"
)

const rootA = "77777777-7777-7777-8777-777777777777"

func newReportsHarness(t *testing.T) *harness {
	t.Helper()
	rt := testrt.New(t, testutil.MustCA("example.org"), "ticket")
	v := verifier{
		"admin-a":  {UserID: adminA, TenantID: tenantA},
		"viewer-a": {UserID: viewerA, TenantID: tenantA},
		"agent-a":  {UserID: agentA, TenantID: tenantA},
		"agent-b":  {UserID: agentB, TenantID: tenantB},
		"root-a":   authclient.Identity{UserID: rootA, TenantID: tenantA, Roles: []string{authz.RolePlatformAdmin}},
	}
	all := append([]string{}, authz.Permissions...)
	checker := authz.Static{adminA: all, rootA: all, agentA: {authz.TicketsRead, authz.TicketsManage, authz.StatsRead}, agentB: all, viewerA: {authz.TicketsRead}}
	s, err := httpapi.NewHandler(rt, httpapi.WithVerifier(v), httpapi.WithChecker(checker))
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := httpapi.LoadDocument()
	doc.Servers = nil
	rtr, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := agents.NewFake()
	dir.Add(tenantA, agents.User{ID: agentA, Name: "Ada Agent"}, true)
	h := &harness{t: t, s: s, rtr: rtr, st: memstore.New(), pub: &events.Recorder{}}
	s.Register(httpapi.Deps{
		Hub:     stream.NewHub(stream.NewMemory(), stream.Config{}, nil),
		Tickets: tickets.New(tickets.Deps{Store: h.st, Agents: dir, Events: h.pub}),
		Stats:   stats.New(h.st),
		Backup:  backup.New(h.st, nil, nil),
	})
	return h
}

type statsJSON struct {
	Total          int64            `json:"total"`
	ByStatus       map[string]int64 `json:"by_status"`
	ByPriority     map[string]int64 `json:"by_priority"`
	ByAssignee     []map[string]any `json:"by_assignee"`
	UnassignedOpen int64            `json:"unassigned_open"`
	CreatedPerDay  []struct {
		Day   string `json:"day"`
		Count int64  `json:"count"`
	} `json:"created_per_day"`
	ResolvedPerDay []map[string]any `json:"resolved_per_day"`
}

func TestStatsRoute(t *testing.T) {
	h := newReportsHarness(t)
	a := h.create("agent-a", `{"subject":"one","priority":"high"}`)
	h.create("agent-a", `{"subject":"two"}`)
	if w := h.do("POST", p+"/tickets/"+a.ID+"/assign", "agent-a", `{"assignee_id":"`+agentA+`"}`); w.Code != 200 {
		t.Fatalf("assign = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/tickets/"+a.ID+"/status", "agent-a", `{"status":"resolved"}`); w.Code != 200 {
		t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	w := h.do("GET", p+"/stats", "agent-a", "")
	if w.Code != 200 {
		t.Fatalf("stats = %d %s", w.Code, w.Body)
	}
	st := decode[statsJSON](t, w)
	today := time.Now().UTC().Format("2006-01-02")
	if st.Total != 2 || st.ByStatus["resolved"] != 1 || st.ByPriority["high"] != 1 || st.UnassignedOpen != 1 || len(st.ByAssignee) != 0 ||
		len(st.CreatedPerDay) != 30 || st.CreatedPerDay[29].Day != today || st.CreatedPerDay[29].Count != 2 || st.ResolvedPerDay[29]["count"] != float64(1) {
		t.Fatalf("stats = %+v", st)
	}
	if st7 := decode[statsJSON](t, h.do("GET", p+"/stats?days=7", "agent-a", "")); len(st7.CreatedPerDay) != 7 {
		t.Fatalf("?days=7 = %d buckets", len(st7.CreatedPerDay))
	}
	for _, q := range []string{"?days=0", "?days=366", "?days=abc"} {
		if w := h.do("GET", p+"/stats"+q, "agent-a", ""); w.Code != 400 && w.Code != 422 {
			t.Errorf("%s = %d", q, w.Code)
		}
	}
	if w := h.do("GET", p+"/stats", "viewer-a", ""); w.Code != 403 {
		t.Fatalf("viewer stats = %d", w.Code)
	}
	if b := decode[statsJSON](t, h.do("GET", p+"/stats", "agent-b", "")); b.Total != 0 {
		t.Fatalf("tenant B sees tenant A: %+v", b)
	}
}

func TestBackupRoutes(t *testing.T) {
	h := newReportsHarness(t)
	h.create("agent-a", `{"subject":"keep me"}`)
	w := h.do("POST", p+"/backup/export", "admin-a", "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export = %d %s", w.Code, w.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["schema_version"] != float64(backup.SchemaVersion) || doc["tenant_id"] != tenantA || len(doc["tickets"].([]any)) != 1 {
		t.Fatalf("export doc = %v", doc)
	}
	raw := w.Body.String()
	if w := h.do("POST", p+"/backup/export", "admin-a", `{"tenant_id":"`+tenantB+`"}`); w.Code != 403 {
		t.Fatalf("non-admin cross-tenant export = %d", w.Code)
	}
	if w := h.do("POST", p+"/backup/export", "root-a", `{"tenant_id":"`+tenantB+`"}`); w.Code != 200 {
		t.Fatalf("platform-admin export of B = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/backup/export", "agent-a", ""); w.Code != 403 {
		t.Fatalf("agent export = %d", w.Code)
	}
	if w := h.do("POST", p+"/backup/export", "admin-a", `{"bogus":1}`); w.Code != 400 && w.Code != 422 {
		t.Fatalf("unknown export field = %d", w.Code)
	}

	w = h.do("POST", p+"/backup/import?mode=skip", "admin-a", `{"backup":`+raw+`}`)
	if w.Code != 200 {
		t.Fatalf("import = %d %s", w.Code, w.Body)
	}
	res := decode[backup.Result](t, w)
	if res.Skipped["tickets"] != 1 || res.Mode != "skip" || res.TenantID != tenantA {
		t.Fatalf("import result = %+v", res)
	}
	for _, body := range []string{`{"full":true,"backup":` + raw + `}`, `{"tenant_id":"` + tenantB + `","backup":` + raw + `}`} {
		if w := h.do("POST", p+"/backup/import", "admin-a", body); w.Code != 403 {
			t.Fatalf("non-admin restore = %d %s", w.Code, w.Body)
		}
	}
	w = h.do("POST", p+"/backup/import", "root-a", `{"tenant_id":"`+tenantB+`","mode":"overwrite","backup":`+raw+`}`)
	if w.Code != 200 || decode[backup.Result](t, w).Imported["tickets"] != 1 {
		t.Fatalf("platform-admin cross-tenant restore = %d %s", w.Code, w.Body)
	}
	if bt, _ := h.st.AllTickets(context.Background(), tenantB); len(bt) != 1 {
		t.Fatalf("tenant B tickets = %d", len(bt))
	}
	if w := h.do("POST", p+"/backup/import", "admin-a", `{"backup":{"schema_version":9}}`); w.Code != 422 {
		t.Fatalf("bad schema = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/backup/import", "admin-a", `{}`); w.Code != 400 && w.Code != 422 {
		t.Fatalf("missing backup = %d", w.Code)
	}
	if w := h.do("POST", p+"/backup/import", "agent-a", `{"backup":`+raw+`}`); w.Code != 403 {
		t.Fatalf("agent import = %d", w.Code)
	}
}

func TestStreamRoutePermission(t *testing.T) {
	h := newReportsHarness(t)
	if w := h.do("GET", p+"/stream", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous stream = %d", w.Code)
	}
	if perm := h.s.Permission("GET", p+"/stream"); perm != authz.TicketsRead {
		t.Fatalf("stream permission = %q", perm)
	}
}
