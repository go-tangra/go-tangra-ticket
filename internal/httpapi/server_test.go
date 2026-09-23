package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-freya/freya/internal/testrt"
	"github.com/go-freya/freya/internal/testutil"
	"github.com/go-freya/freya/services/auth/pkg/authclient"

	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/stream"
)

const (
	tenant = "11111111-1111-7111-8111-111111111111"
	viewer = "44444444-4444-7444-8444-444444444444"
	agent  = "55555555-5555-7555-8555-555555555555"
)

type fakeVerifier map[string]authclient.Identity

func (f fakeVerifier) Verify(_ context.Context, token string) (authclient.Identity, error) {
	if id, ok := f[token]; ok {
		return id, nil
	}
	return authclient.Identity{}, ErrUnauthenticated
}

func newServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	rt := testrt.New(t, testutil.MustCA("example.org"), "ticket")
	v := fakeVerifier{
		"tok-viewer": {UserID: viewer, TenantID: tenant},
		"tok-agent":  {UserID: agent, TenantID: tenant},
		"tok-notent": {UserID: agent},
	}
	checker := authz.Static{viewer: {authz.TicketsRead}, agent: {authz.TicketsRead, authz.TicketsManage}}
	s, err := NewHandler(rt, append([]Option{WithVerifier(v), WithChecker(checker)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func do(s *Server, method, path, token, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method != http.MethodGet {
		r.Header.Set("X-CSRF-Token", "csrf")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func reason(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	r, _ := m["reason"].(string)
	return r
}

func TestHealthIsPublicAndReportsComponents(t *testing.T) {
	s := newServer(t)
	s.Register(Deps{Health: func() map[string]string { return map[string]string{"store": "ok", "object_store": "unreachable"} }})
	w := do(s, "GET", Prefix+"/health", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"degraded"`) {
		t.Fatalf("health = %d %s", w.Code, w.Body)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secure headers missing")
	}
	s2 := newServer(t)
	s2.Register(Deps{})
	if w := do(s2, "GET", Prefix+"/health", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok"`) {
		t.Fatalf("bare health = %s", w.Body)
	}
	if s2.IsPublic("GET", Prefix+"/stream") || !s2.IsPublic("GET", Prefix+"/health") {
		t.Fatal("public flags")
	}
}

// Per-route permission enforcement inside the module (defence in depth).
func TestPermissionEnforcement(t *testing.T) {
	s := newServer(t)
	s.Register(Deps{})
	ok := func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, 200, map[string]any{"items": []any{}, "total": 0})
	}
	s.MustHandle("GET", Prefix+"/tickets", ok)
	s.MustHandle("POST", Prefix+"/tickets", ok)
	s.MustHandle("DELETE", Prefix+"/tickets/{id}", ok)

	if w := do(s, "GET", Prefix+"/tickets", "", ""); w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous = %d", w.Code)
	}
	if w := do(s, "GET", Prefix+"/tickets", "bogus", ""); w.Code != 401 {
		t.Fatalf("bad token = %d", w.Code)
	}
	if w := do(s, "GET", Prefix+"/tickets", "tok-notent", ""); w.Code != 401 {
		t.Fatalf("tenantless identity = %d", w.Code)
	}
	if w := do(s, "GET", Prefix+"/tickets", "tok-viewer", ""); w.Code != 200 {
		t.Fatalf("viewer read = %d %s", w.Code, w.Body)
	}
	body := `{"subject":"Printer on fire"}`
	if w := do(s, "POST", Prefix+"/tickets", "tok-viewer", body); w.Code != 403 || reason(t, w) != "forbidden" {
		t.Fatalf("viewer create = %d %s", w.Code, w.Body)
	}
	if w := do(s, "POST", Prefix+"/tickets", "tok-agent", body); w.Code != 200 {
		t.Fatalf("agent create = %d %s", w.Code, w.Body)
	}
	if w := do(s, "DELETE", Prefix+"/tickets/abc", "tok-agent", ""); w.Code != 403 {
		t.Fatalf("agent delete without tickets:delete = %d", w.Code)
	}
	if s.Permission("DELETE", Prefix+"/tickets/{id}") != authz.TicketsDelete || s.Permission("GET", Prefix+"/health") != "" {
		t.Fatal("permission lookup")
	}

	// no checker installed: fail closed
	closed := newServer(t, WithChecker(nil))
	closed.MustHandle("GET", Prefix+"/tickets", ok)
	if w := do(closed, "GET", Prefix+"/tickets", "tok-agent", ""); w.Code != 403 {
		t.Fatalf("nil checker = %d", w.Code)
	}
	// no verifier installed: unauthenticated
	rt := testrt.New(t, testutil.MustCA("example.org"), "ticket")
	nov, err := NewHandler(rt)
	if err != nil {
		t.Fatal(err)
	}
	nov.MustHandle("GET", Prefix+"/tickets", ok)
	if w := do(nov, "GET", Prefix+"/tickets", "tok-agent", ""); w.Code != 401 {
		t.Fatalf("nil verifier = %d", w.Code)
	}
}

func TestContractValidation(t *testing.T) {
	s := newServer(t)
	s.MustHandle("GET", Prefix+"/tickets", func(w http.ResponseWriter, _ *http.Request) { WriteJSON(w, 200, map[string]any{}) })
	s.MustHandle("POST", Prefix+"/tickets", func(w http.ResponseWriter, _ *http.Request) { WriteJSON(w, 201, map[string]any{}) })
	s.MustHandle("POST", Prefix+"/tickets/{id}/assign", func(w http.ResponseWriter, _ *http.Request) { WriteJSON(w, 200, map[string]any{}) })
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"GET", Prefix + "/tickets?page_size=1000", "", 422},
		{"GET", Prefix + "/tickets?page=0", "", 422},
		{"GET", Prefix + "/tickets?page=2&page_size=50&assignee_id=none", "", 200},
		{"POST", Prefix + "/tickets", `{"subject":""}`, 422},
		{"POST", Prefix + "/tickets", `{"subject":"x","bogus":1}`, 422},
		{"POST", Prefix + "/tickets", `{"subject":"x","priority":"whenever"}`, 422},
		{"POST", Prefix + "/tickets", `{not json`, 400},
		{"POST", Prefix + "/tickets", `{"subject":"ok","priority":"high"}`, 201},
		{"POST", Prefix + "/tickets/abc/assign", `{"assignee_id":null}`, 200},
		{"POST", Prefix + "/tickets/abc/assign", `{"assignee_id":"u1"}`, 200},
		{"POST", Prefix + "/tickets/abc/assign", `{}`, 422},
		{"PATCH", Prefix + "/tickets", "", 405},
		{"GET", "/api/ticket/v1/nowhere", "", 404},
		{"GET", Prefix + "/tickets/abc", "", 501},
	}
	for _, c := range cases {
		w := do(s, c.method, c.path, "tok-agent", c.body)
		if w.Code != c.want {
			t.Errorf("%s %s %s = %d (%s), want %d", c.method, c.path, c.body, w.Code, w.Body, c.want)
		}
	}
	// oversized body on a JSON route
	big := `{"subject":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
	if w := do(s, "POST", Prefix+"/tickets", "tok-agent", big); w.Code != 413 {
		t.Errorf("oversized = %d", w.Code)
	}
	// CSRF header is required on mutations
	r := httptest.NewRequest("POST", Prefix+"/tickets", strings.NewReader(`{"subject":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer tok-agent")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 422 {
		t.Errorf("missing csrf = %d", w.Code)
	}
}

func TestRouteBookkeeping(t *testing.T) {
	s := newServer(t)
	if err := s.HandleFunc("GET", "/api/ticket/v1/undeclared", func(http.ResponseWriter, *http.Request) {}); err == nil {
		t.Fatal("undeclared route accepted")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("MustHandle on undeclared route must panic")
			}
		}()
		s.MustHandle("GET", "/nope", func(http.ResponseWriter, *http.Request) {})
	}()
	before := len(s.Missing())
	s.Register(Deps{})
	if len(s.Implemented()) != 1 || len(s.Missing()) != before-1 || len(s.Declared()) != before {
		t.Fatalf("implemented=%d missing=%d declared=%d", len(s.Implemented()), len(s.Missing()), len(s.Declared()))
	}
	if s.Document() == nil || s.Edge() != nil {
		t.Fatal("accessors")
	}
}

func TestRemoteServing(t *testing.T) {
	dist := fstest.MapFS{
		"mf-manifest.json":   {Data: []byte(`{}`)},
		"assets/app-abc.js":  {Data: []byte(`console.log(1)`)},
		"remoteEntry.js":     {Data: []byte(`export {}`)},
		"assets/sub/dir.txt": {Data: []byte(`x`)},
	}
	s := newServer(t, WithRemote(dist))
	w := do(s, "GET", "/ui/assets/app-abc.js", "", "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if w := do(s, "GET", "/ui/mf-manifest.json", "", ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("manifest = %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"/ui/", "/ui/assets/", "/ui/missing.js"} {
		if w := do(s, "GET", p, "", ""); w.Code != 404 {
			t.Errorf("%s = %d", p, w.Code)
		}
	}
}

func TestStreamRoute(t *testing.T) {
	hub := stream.NewHub(stream.NewMemory(), stream.Config{StreamsPerUser: 1}, nil)
	defer hub.Close()
	s := newServer(t)
	s.Register(Deps{Hub: hub})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("GET", Prefix+"/stream", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer tok-viewer")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.Handler().ServeHTTP(w, r); close(done) }()
	time.Sleep(100 * time.Millisecond)
	// a second stream for the same user exceeds StreamsPerUser=1
	if w2 := do(s, "GET", Prefix+"/stream", "tok-viewer", ""); w2.Code != 429 {
		t.Errorf("second stream = %d", w2.Code)
	}
	<-done
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type = %q", w.Header().Get("Content-Type"))
	}
	// Subjects without an identity
	if _, err := Subjects(httptest.NewRequest("GET", "/", nil)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("subjects without identity")
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		reason string
	}{
		{ErrInvalidStatus, 422, "invalid_status"},
		{fmt.Errorf("wrap: %w", ErrReplyUnavailable), 409, "reply_unavailable"},
		{&http.MaxBytesError{Limit: 1}, 413, "body_too_large"},
		{authz.ErrForbidden, 403, "forbidden"},
		{repo.ErrNotFound, 404, "not_found"},
		{repo.ErrConflict, 409, "conflict"},
		{repo.ErrNotEmpty, 409, "conflict"},
		{errors.New("db exploded"), 503, "temporarily_unavailable"},
	}
	for _, c := range cases {
		st, r := Status(c.err)
		if st != c.status || r != c.reason {
			t.Errorf("%v => %d %s", c.err, st, r)
		}
	}
	w := httptest.NewRecorder()
	Fail(w, httptest.NewRequest("GET", "/x", nil), nil, errors.New("secret detail"))
	if strings.Contains(w.Body.String(), "secret detail") || w.Code != 503 {
		t.Fatalf("fail leaked: %s", w.Body)
	}
	w = httptest.NewRecorder()
	WriteDetail(w, ErrValidation, map[string]any{"field": "subject"})
	if !strings.Contains(w.Body.String(), `"field":"subject"`) || w.Code != 422 {
		t.Fatal("detail")
	}
	if ErrConflict.Error() != "conflict" {
		t.Fatal("error string")
	}
}

func TestDecodeJSON(t *testing.T) {
	var v struct{ A int }
	req := func(b string) *http.Request { return httptest.NewRequest("POST", "/", strings.NewReader(b)) }
	if err := DecodeJSON(req(`{"A":1}`), &v, 0); err != nil || v.A != 1 {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"B":1}`, `{"A":1} {}`, `nope`} {
		if err := DecodeJSON(req(bad), &v, 0); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if err := DecodeJSON(req(`{"A":1111111}`), &v, 4); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("limit: %v", err)
	}
	if SanitizeFilename("a\"b\\c\x01.txt") != "a_b_c_.txt" || SanitizeFilename("") != "download" {
		t.Fatal("sanitize filename")
	}
}
