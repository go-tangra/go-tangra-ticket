// Package stats serves the per-tenant dashboard aggregates (FR-017, research
// D12): ticket counts by status, priority and assignee (open work only),
// unassigned open tickets, and tickets created / resolved per UTC day over a
// window of ?days (default 30, capped at a year). "Resolved per day" counts
// history transitions into resolved, so a ticket resolved twice counts twice
// and a ticket later closed still counts on the day it was resolved.
package stats

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/repo"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

// Window bounds (days).
const (
	DefaultDays = 30
	MaxDays     = 365
)

// Service computes the dashboard.
type Service struct {
	st  repo.Stats
	now func() time.Time
}

// New builds the service.
func New(st repo.Stats) *Service {
	return &Service{st: st, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock injects the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Days parses a ?days value: anything unusable is the default; Get clamps.
func Days(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultDays
	}
	return n
}

// Clamp bounds a window to [1, MaxDays] (<= 0 → DefaultDays).
func Clamp(days int) int {
	switch {
	case days <= 0:
		return DefaultDays
	case days > MaxDays:
		return MaxDays
	}
	return days
}

// Get aggregates the caller's tenant over the last days days (today included).
func (s *Service) Get(ctx context.Context, subj authz.Subjects, days int) (store.Stats, error) {
	if subj.TenantID == "" {
		return store.Stats{}, fmt.Errorf("%w: tenant required", authz.ErrForbidden)
	}
	days = Clamp(days)
	since := s.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	st, err := s.st.TicketStats(ctx, subj.TenantID, since)
	if err != nil {
		return store.Stats{}, err
	}
	if st.ByAssignee == nil {
		st.ByAssignee = []store.AssigneeCount{}
	}
	return st, nil
}
