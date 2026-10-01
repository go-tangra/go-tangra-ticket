package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// parseList is the handler-side guard behind the OpenAPI validator (and the
// only one for routes the validator does not see).
func TestParseListRefusals(t *testing.T) {
	for q, param := range map[string]string{
		"page=0": "page", "page=x": "page", "page_size=0": "page_size", "page_size=201": "page_size",
		"sort=password": "sort", "order=sideways": "order", "cursor=abc&page=2": "cursor",
	} {
		w := httptest.NewRecorder()
		if _, ok := parseList(w, httptest.NewRequest("GET", "/x?"+q, nil), store.TicketList); ok {
			t.Fatalf("%s accepted", q)
		}
		var body struct {
			Reason string            `json:"reason"`
			Detail map[string]string `json:"detail"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 422 || body.Reason != "validation_failed" || body.Detail["param"] != param || len(body.Detail) != 1 {
			t.Fatalf("%s = %d %s", q, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "sideways") {
			t.Fatalf("%s echoes the value", q)
		}
	}
	req, ok := parseList(httptest.NewRecorder(), httptest.NewRequest("GET", "/x?sort=priority&page=2&page_size=200", nil), store.TicketList)
	if !ok || req.Sort != "priority" || req.Order != "desc" || req.Page != 2 || req.PageSize != 200 {
		t.Fatalf("valid = %+v %v", req, ok)
	}
}

func TestWritePageShape(t *testing.T) {
	req, _ := parseList(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil), store.TagList)
	w := httptest.NewRecorder()
	writePage[store.Tag](w, nil, 0, req)
	if got := strings.TrimSpace(w.Body.String()); got != `{"items":[],"total":0,"page":1,"page_size":25,"sort":"name","order":"asc"}` {
		t.Fatalf("page = %s", got)
	}
}
