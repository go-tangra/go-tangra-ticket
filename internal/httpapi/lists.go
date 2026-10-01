package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-tangra/go-tangra/v4/listquery"
)

// parseList reads page, page_size, sort and order against spec; an invalid
// value answers 422 validation_failed naming the parameter (never its value)
// and returns false.
func parseList(w http.ResponseWriter, r *http.Request, spec listquery.Spec) (listquery.Request, bool) {
	req, err := listquery.Parse(r.URL.Query(), spec)
	var le *listquery.Error
	if errors.As(err, &le) {
		WriteDetail(w, ErrValidation, map[string]any{"param": le.Param})
		return req, false
	}
	return req, true
}

// writePage answers one page in the list contract shape
// {items, total, page, page_size, sort, order}.
func writePage[T any](w http.ResponseWriter, items []T, total int, req listquery.Request) {
	WriteJSON(w, http.StatusOK, listquery.NewPage(items, total, req))
}
