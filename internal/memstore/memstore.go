// Package memstore is an in-memory repo.Store for the ticket service, used by
// offline tests and local development. It filters by tenant (mirroring RLS),
// enforces every unique/partial-unique guard of the schema (ticket external id,
// comment message id, tag kind+lower(name), GLOBAL mailbox address, attachment
// storage key), cascades deletes like the foreign keys do, maintains the
// denormalised comment_count/last_message_id, answers the system-scoped
// RouteMailbox lookup, and offers per-method error injection via FailNext.
package memstore

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// injectedErr is the error FailNext arms for a given method.
type injectedErr struct{ method string }

func (e injectedErr) Error() string { return "memstore: injected failure in " + e.method }

// Mem is an in-memory store. It is safe for concurrent use.
type Mem struct {
	mu sync.Mutex

	tickets     map[string]store.Ticket
	comments    map[string]store.Comment
	attachments map[string]store.Attachment
	tags        map[string]store.Tag
	links       map[string]map[string]bool // ticket id → tag id set
	rules       map[string]store.Rule
	mailboxes   map[string]store.Mailbox
	history     []store.History
	audit       []store.AuditRow

	failNext map[string]bool
	// Now is the clock used for defaults (overridable in tests).
	Now func() time.Time
}

var _ repo.Store = (*Mem)(nil)

// New builds an empty store.
func New() *Mem {
	return &Mem{
		tickets:     map[string]store.Ticket{},
		comments:    map[string]store.Comment{},
		attachments: map[string]store.Attachment{},
		tags:        map[string]store.Tag{},
		links:       map[string]map[string]bool{},
		rules:       map[string]store.Rule{},
		mailboxes:   map[string]store.Mailbox{},
		failNext:    map[string]bool{},
		Now:         func() time.Time { return time.Now().UTC() },
	}
}

// FailNext arms the next call to the named method to return an injected error.
func (m *Mem) FailNext(method string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext[method] = true
}

// fail reports (and disarms) an injected failure for method. Callers hold mu.
func (m *Mem) fail(method string) error {
	if m.failNext[method] {
		delete(m.failNext, method)
		return injectedErr{method}
	}
	return nil
}

// Audit returns a copy of the appended audit rows (tests).
func (m *Mem) Audit() []store.AuditRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.AuditRow(nil), m.audit...)
}

// Close is a no-op for the in-memory store.
func (m *Mem) Close() {}

func orNow(t, now time.Time) time.Time {
	if t.IsZero() {
		return now
	}
	return t
}

// ---------------------------------------------------------------- tickets

// CreateTicket implements repo.Tickets.
func (m *Mem) CreateTicket(_ context.Context, t store.Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateTicket"); err != nil {
		return err
	}
	if _, dup := m.tickets[t.ID]; dup {
		return repo.ErrConflict
	}
	if t.ExternalID != "" {
		for _, x := range m.tickets {
			if x.TenantID == t.TenantID && x.ExternalID == t.ExternalID {
				return repo.ErrConflict
			}
		}
	}
	if t.MailboxID != "" {
		if mb, ok := m.mailboxes[t.MailboxID]; !ok || mb.TenantID != t.TenantID {
			return repo.ErrNotFound
		}
	}
	now := m.Now()
	t.CreatedAt = orNow(t.CreatedAt, now)
	t.UpdatedAt = orNow(t.UpdatedAt, t.CreatedAt)
	if t.Status == "" {
		t.Status = store.StatusOpen
	}
	if t.Priority == "" {
		t.Priority = store.PriorityNormal
	}
	if t.Source == "" {
		t.Source = store.SourceManual
	}
	m.tickets[t.ID] = t
	return nil
}

func (m *Mem) ticket(tenantID, id string) (store.Ticket, error) {
	t, ok := m.tickets[id]
	if !ok || t.TenantID != tenantID {
		return store.Ticket{}, repo.ErrNotFound
	}
	return t, nil
}

// GetTicket implements repo.Tickets.
func (m *Mem) GetTicket(_ context.Context, tenantID, id string) (store.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetTicket"); err != nil {
		return store.Ticket{}, err
	}
	return m.ticket(tenantID, id)
}

// FindTicketByExternalID implements repo.Tickets.
func (m *Mem) FindTicketByExternalID(_ context.Context, tenantID, externalID string) (store.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("FindTicketByExternalID"); err != nil {
		return store.Ticket{}, err
	}
	if externalID == "" {
		return store.Ticket{}, repo.ErrNotFound
	}
	for _, t := range m.tickets {
		if t.TenantID == tenantID && t.ExternalID == externalID {
			return t, nil
		}
	}
	return store.Ticket{}, repo.ErrNotFound
}

func matchesQuery(t store.Ticket, q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	return strings.Contains(strings.ToLower(t.Subject), q) ||
		strings.Contains(strings.ToLower(t.RequesterEmail), q) ||
		strings.Contains(strings.ToLower(t.RequesterName), q)
}

// ListTickets implements repo.Tickets.
func (m *Mem) ListTickets(_ context.Context, tenantID string, f store.TicketFilter) ([]store.Ticket, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListTickets"); err != nil {
		return nil, 0, err
	}
	f = f.Normalized(100)
	var all []store.Ticket
	for _, t := range m.tickets {
		switch {
		case t.TenantID != tenantID,
			f.Status != "" && t.Status != f.Status,
			f.Priority != "" && t.Priority != f.Priority,
			f.AssigneeID == store.AssigneeNone && t.AssigneeID != "",
			f.AssigneeID != "" && f.AssigneeID != store.AssigneeNone && t.AssigneeID != f.AssigneeID,
			f.TagID != "" && !m.links[t.ID][f.TagID],
			!matchesQuery(t, f.Query):
			continue
		}
		all = append(all, t)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	total := int64(len(all))
	off := f.Offset()
	if off >= len(all) {
		return []store.Ticket{}, total, nil
	}
	end := min(off+f.PageSize, len(all))
	return append([]store.Ticket(nil), all[off:end]...), total, nil
}

// UpdateTicket implements repo.Tickets.
func (m *Mem) UpdateTicket(_ context.Context, tenantID, id string, p store.TicketPatch, at time.Time) (store.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateTicket"); err != nil {
		return store.Ticket{}, err
	}
	t, err := m.ticket(tenantID, id)
	if err != nil {
		return t, err
	}
	if p.Subject != nil {
		t.Subject = *p.Subject
	}
	if p.Description != nil {
		t.Description = *p.Description
	}
	if p.Priority != nil {
		t.Priority = *p.Priority
	}
	t.UpdatedAt = orNow(at, m.Now())
	m.tickets[id] = t
	return t, nil
}

// SetAssignee implements repo.Tickets.
func (m *Mem) SetAssignee(_ context.Context, tenantID, id, assigneeID, assigneeName string, at time.Time) (store.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("SetAssignee"); err != nil {
		return store.Ticket{}, err
	}
	t, err := m.ticket(tenantID, id)
	if err != nil {
		return t, err
	}
	t.AssigneeID = assigneeID
	t.AssigneeName = assigneeName
	if assigneeID == "" {
		t.AssigneeName = ""
	}
	t.UpdatedAt = orNow(at, m.Now())
	m.tickets[id] = t
	return t, nil
}

// SetStatus implements repo.Tickets.
func (m *Mem) SetStatus(_ context.Context, tenantID, id, status string, at time.Time) (store.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("SetStatus"); err != nil {
		return store.Ticket{}, err
	}
	t, err := m.ticket(tenantID, id)
	if err != nil {
		return t, err
	}
	at = orNow(at, m.Now())
	if status == store.StatusResolved {
		if t.Status != store.StatusResolved || t.ResolvedAt == nil {
			ra := at
			t.ResolvedAt = &ra
		}
	} else {
		t.ResolvedAt = nil
	}
	t.Status = status
	t.UpdatedAt = at
	m.tickets[id] = t
	return t, nil
}

// DeleteTicket implements repo.Tickets (cascades like the foreign keys).
func (m *Mem) DeleteTicket(_ context.Context, tenantID, id string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteTicket"); err != nil {
		return nil, err
	}
	if _, err := m.ticket(tenantID, id); err != nil {
		return nil, err
	}
	var keys []string
	for aid, a := range m.attachments {
		if a.TicketID == id {
			keys = append(keys, a.StorageKey)
			delete(m.attachments, aid)
		}
	}
	for cid, c := range m.comments {
		if c.TicketID == id {
			delete(m.comments, cid)
		}
	}
	delete(m.links, id)
	kept := m.history[:0]
	for _, h := range m.history {
		if h.TicketID != id {
			kept = append(kept, h)
		}
	}
	m.history = kept
	delete(m.tickets, id)
	sort.Strings(keys)
	return keys, nil
}

// --------------------------------------------------------------- comments

// CreateComment implements repo.Comments.
func (m *Mem) CreateComment(_ context.Context, c store.Comment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateComment"); err != nil {
		return err
	}
	t, err := m.ticket(c.TenantID, c.TicketID)
	if err != nil {
		return err
	}
	if _, dup := m.comments[c.ID]; dup {
		return repo.ErrConflict
	}
	if c.MessageID != "" {
		for _, x := range m.comments {
			if x.TenantID == c.TenantID && x.MessageID == c.MessageID {
				return repo.ErrConflict
			}
		}
	}
	c.CreatedAt = orNow(c.CreatedAt, m.Now())
	if c.Delivery == "" {
		c.Delivery = store.DeliveryNone
	}
	m.comments[c.ID] = c
	t.CommentCount++
	if c.MessageID != "" && !c.Internal {
		t.LastMessageID = c.MessageID
	}
	t.UpdatedAt = c.CreatedAt
	m.tickets[t.ID] = t
	return nil
}

// GetComment implements repo.Comments.
func (m *Mem) GetComment(_ context.Context, tenantID, id string) (store.Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetComment"); err != nil {
		return store.Comment{}, err
	}
	c, ok := m.comments[id]
	if !ok || c.TenantID != tenantID {
		return store.Comment{}, repo.ErrNotFound
	}
	return c, nil
}

// ListComments implements repo.Comments.
func (m *Mem) ListComments(_ context.Context, tenantID, ticketID string) ([]store.Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListComments"); err != nil {
		return nil, err
	}
	out := []store.Comment{}
	for _, c := range m.comments {
		if c.TenantID == tenantID && c.TicketID == ticketID {
			out = append(out, c)
		}
	}
	sortComments(out)
	return out, nil
}

func sortComments(out []store.Comment) {
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
}

// SetCommentDelivery implements repo.Comments.
func (m *Mem) SetCommentDelivery(_ context.Context, tenantID, id, delivery string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("SetCommentDelivery"); err != nil {
		return err
	}
	c, ok := m.comments[id]
	if !ok || c.TenantID != tenantID {
		return repo.ErrNotFound
	}
	c.Delivery = delivery
	m.comments[id] = c
	return nil
}

// DeleteComment implements repo.Comments.
func (m *Mem) DeleteComment(_ context.Context, tenantID, id string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteComment"); err != nil {
		return nil, err
	}
	c, ok := m.comments[id]
	if !ok || c.TenantID != tenantID {
		return nil, repo.ErrNotFound
	}
	var keys []string
	for aid, a := range m.attachments {
		if a.CommentID == id {
			keys = append(keys, a.StorageKey)
			delete(m.attachments, aid)
		}
	}
	delete(m.comments, id)
	if t, ok := m.tickets[c.TicketID]; ok && t.CommentCount > 0 {
		t.CommentCount--
		m.tickets[t.ID] = t
	}
	sort.Strings(keys)
	return keys, nil
}

// FindCommentByMessageID implements repo.Comments.
func (m *Mem) FindCommentByMessageID(_ context.Context, tenantID, messageID string) (store.Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("FindCommentByMessageID"); err != nil {
		return store.Comment{}, err
	}
	if messageID == "" {
		return store.Comment{}, repo.ErrNotFound
	}
	for _, c := range m.comments {
		if c.TenantID == tenantID && c.MessageID == messageID {
			return c, nil
		}
	}
	return store.Comment{}, repo.ErrNotFound
}

// LastMessageID implements repo.Comments.
func (m *Mem) LastMessageID(_ context.Context, tenantID, ticketID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("LastMessageID"); err != nil {
		return "", err
	}
	t, err := m.ticket(tenantID, ticketID)
	if err != nil {
		return "", err
	}
	return t.LastMessageID, nil
}

// ------------------------------------------------------------ attachments

// CreateAttachment implements repo.Attachments.
func (m *Mem) CreateAttachment(_ context.Context, a store.Attachment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateAttachment"); err != nil {
		return err
	}
	if _, err := m.ticket(a.TenantID, a.TicketID); err != nil {
		return err
	}
	if a.CommentID != "" {
		if c, ok := m.comments[a.CommentID]; !ok || c.TenantID != a.TenantID || c.TicketID != a.TicketID {
			return repo.ErrNotFound
		}
	}
	if _, dup := m.attachments[a.ID]; dup {
		return repo.ErrConflict
	}
	for _, x := range m.attachments {
		if x.StorageKey == a.StorageKey {
			return repo.ErrConflict
		}
	}
	a.CreatedAt = orNow(a.CreatedAt, m.Now())
	m.attachments[a.ID] = a
	return nil
}

// ListAttachments implements repo.Attachments.
func (m *Mem) ListAttachments(_ context.Context, tenantID, ticketID string) ([]store.Attachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListAttachments"); err != nil {
		return nil, err
	}
	out := []store.Attachment{}
	for _, a := range m.attachments {
		if a.TenantID == tenantID && a.TicketID == ticketID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetAttachment implements repo.Attachments.
func (m *Mem) GetAttachment(_ context.Context, tenantID, ticketID, id string) (store.Attachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetAttachment"); err != nil {
		return store.Attachment{}, err
	}
	a, ok := m.attachments[id]
	if !ok || a.TenantID != tenantID || a.TicketID != ticketID {
		return store.Attachment{}, repo.ErrNotFound
	}
	return a, nil
}

// DeleteAttachmentsByTicket implements repo.Attachments.
func (m *Mem) DeleteAttachmentsByTicket(_ context.Context, tenantID, ticketID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteAttachmentsByTicket"); err != nil {
		return nil, err
	}
	var keys []string
	for id, a := range m.attachments {
		if a.TenantID == tenantID && a.TicketID == ticketID {
			keys = append(keys, a.StorageKey)
			delete(m.attachments, id)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// ------------------------------------------------------------------- tags

func (m *Mem) tagConflict(t store.Tag) bool {
	for _, x := range m.tags {
		if x.ID != t.ID && x.TenantID == t.TenantID && x.Kind == t.Kind && strings.EqualFold(x.Name, t.Name) {
			return true
		}
	}
	return false
}

// CreateTag implements repo.Tags.
func (m *Mem) CreateTag(_ context.Context, t store.Tag) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateTag"); err != nil {
		return err
	}
	return m.createTag(t)
}

func (m *Mem) createTag(t store.Tag) error {
	if t.Kind == "" {
		t.Kind = store.KindTag
	}
	if _, dup := m.tags[t.ID]; dup || m.tagConflict(t) {
		return repo.ErrConflict
	}
	t.CreatedAt = orNow(t.CreatedAt, m.Now())
	m.tags[t.ID] = t
	return nil
}

// GetTag implements repo.Tags.
func (m *Mem) GetTag(_ context.Context, tenantID, id string) (store.Tag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetTag"); err != nil {
		return store.Tag{}, err
	}
	t, ok := m.tags[id]
	if !ok || t.TenantID != tenantID {
		return store.Tag{}, repo.ErrNotFound
	}
	return t, nil
}

func sortTags(out []store.Tag) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].ID < out[j].ID
	})
}

// ListTags implements repo.Tags.
func (m *Mem) ListTags(_ context.Context, tenantID, kind string) ([]store.Tag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListTags"); err != nil {
		return nil, err
	}
	out := []store.Tag{}
	for _, t := range m.tags {
		if t.TenantID == tenantID && (kind == "" || t.Kind == kind) {
			out = append(out, t)
		}
	}
	sortTags(out)
	return out, nil
}

// UpdateTag implements repo.Tags (the kind is never changed).
func (m *Mem) UpdateTag(_ context.Context, t store.Tag) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateTag"); err != nil {
		return err
	}
	cur, ok := m.tags[t.ID]
	if !ok || cur.TenantID != t.TenantID {
		return repo.ErrNotFound
	}
	t.Kind, t.CreatedAt = cur.Kind, cur.CreatedAt
	if m.tagConflict(t) {
		return repo.ErrConflict
	}
	m.tags[t.ID] = t
	return nil
}

// DeleteTag implements repo.Tags (links cascade).
func (m *Mem) DeleteTag(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteTag"); err != nil {
		return err
	}
	t, ok := m.tags[id]
	if !ok || t.TenantID != tenantID {
		return repo.ErrNotFound
	}
	delete(m.tags, id)
	for _, set := range m.links {
		delete(set, id)
	}
	return nil
}

// EnsureTagByName implements repo.Tags.
func (m *Mem) EnsureTagByName(_ context.Context, tenantID, kind, name string) (store.Tag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("EnsureTagByName"); err != nil {
		return store.Tag{}, err
	}
	if kind == "" {
		kind = store.KindTag
	}
	for _, t := range m.tags {
		if t.TenantID == tenantID && t.Kind == kind && strings.EqualFold(t.Name, name) {
			return t, nil
		}
	}
	t := store.Tag{ID: store.NewID(), TenantID: tenantID, Kind: kind, Name: name, CreatedAt: m.Now()}
	if err := m.createTag(t); err != nil {
		return store.Tag{}, err
	}
	return t, nil
}

func (m *Mem) checkTags(tenantID, ticketID string, tagIDs []string) error {
	if _, err := m.ticket(tenantID, ticketID); err != nil {
		return err
	}
	for _, id := range tagIDs {
		if t, ok := m.tags[id]; !ok || t.TenantID != tenantID {
			return repo.ErrNotFound
		}
	}
	return nil
}

// SetTicketTags implements repo.Tags.
func (m *Mem) SetTicketTags(_ context.Context, tenantID, ticketID string, tagIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("SetTicketTags"); err != nil {
		return err
	}
	if err := m.checkTags(tenantID, ticketID, tagIDs); err != nil {
		return err
	}
	set := map[string]bool{}
	for _, id := range tagIDs {
		set[id] = true
	}
	m.links[ticketID] = set
	return nil
}

// AddTicketTags implements repo.Tags.
func (m *Mem) AddTicketTags(_ context.Context, tenantID, ticketID string, tagIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AddTicketTags"); err != nil {
		return err
	}
	if err := m.checkTags(tenantID, ticketID, tagIDs); err != nil {
		return err
	}
	if m.links[ticketID] == nil {
		m.links[ticketID] = map[string]bool{}
	}
	for _, id := range tagIDs {
		m.links[ticketID][id] = true
	}
	return nil
}

// TagsForTickets implements repo.Tags.
func (m *Mem) TagsForTickets(_ context.Context, tenantID string, ticketIDs []string) (map[string][]store.Tag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("TagsForTickets"); err != nil {
		return nil, err
	}
	out := map[string][]store.Tag{}
	for _, tid := range ticketIDs {
		if t, ok := m.tickets[tid]; !ok || t.TenantID != tenantID {
			continue
		}
		var tags []store.Tag
		for id := range m.links[tid] {
			if tag, ok := m.tags[id]; ok && tag.TenantID == tenantID {
				tags = append(tags, tag)
			}
		}
		if len(tags) > 0 {
			sortTags(tags)
			out[tid] = tags
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ rules

func sortRules(out []store.Rule) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
}

func cloneRule(r store.Rule) store.Rule {
	r.Conditions = append([]store.Condition(nil), r.Conditions...)
	acts := make([]store.Action, len(r.Actions))
	for i, a := range r.Actions {
		a.TagNames = append([]string(nil), a.TagNames...)
		acts[i] = a
	}
	r.Actions = acts
	return r
}

// CreateRule implements repo.Rules.
func (m *Mem) CreateRule(_ context.Context, r store.Rule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateRule"); err != nil {
		return err
	}
	if _, dup := m.rules[r.ID]; dup {
		return repo.ErrConflict
	}
	now := m.Now()
	r.CreatedAt = orNow(r.CreatedAt, now)
	r.UpdatedAt = orNow(r.UpdatedAt, r.CreatedAt)
	if r.Version <= 0 {
		r.Version = 1
	}
	if r.Match == "" {
		r.Match = store.MatchAll
	}
	m.rules[r.ID] = cloneRule(r)
	return nil
}

// GetRule implements repo.Rules.
func (m *Mem) GetRule(_ context.Context, tenantID, id string) (store.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetRule"); err != nil {
		return store.Rule{}, err
	}
	r, ok := m.rules[id]
	if !ok || r.TenantID != tenantID {
		return store.Rule{}, repo.ErrNotFound
	}
	return cloneRule(r), nil
}

func (m *Mem) listRules(tenantID string, enabledOnly bool) []store.Rule {
	out := []store.Rule{}
	for _, r := range m.rules {
		if r.TenantID == tenantID && (!enabledOnly || r.Enabled) {
			out = append(out, cloneRule(r))
		}
	}
	sortRules(out)
	return out
}

// ListRules implements repo.Rules.
func (m *Mem) ListRules(_ context.Context, tenantID string) ([]store.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListRules"); err != nil {
		return nil, err
	}
	return m.listRules(tenantID, false), nil
}

// ListEnabledRules implements repo.Rules.
func (m *Mem) ListEnabledRules(_ context.Context, tenantID string) ([]store.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListEnabledRules"); err != nil {
		return nil, err
	}
	return m.listRules(tenantID, true), nil
}

// UpdateRule implements repo.Rules (bumps the version).
func (m *Mem) UpdateRule(_ context.Context, r store.Rule) (store.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateRule"); err != nil {
		return store.Rule{}, err
	}
	cur, ok := m.rules[r.ID]
	if !ok || cur.TenantID != r.TenantID {
		return store.Rule{}, repo.ErrNotFound
	}
	r.CreatedAt = cur.CreatedAt
	r.Version = cur.Version + 1
	r.UpdatedAt = m.Now()
	if r.Match == "" {
		r.Match = store.MatchAll
	}
	m.rules[r.ID] = cloneRule(r)
	return cloneRule(r), nil
}

// DeleteRule implements repo.Rules.
func (m *Mem) DeleteRule(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteRule"); err != nil {
		return err
	}
	r, ok := m.rules[id]
	if !ok || r.TenantID != tenantID {
		return repo.ErrNotFound
	}
	delete(m.rules, id)
	return nil
}

// -------------------------------------------------------------- mailboxes

func normAddr(a string) string { return strings.ToLower(strings.TrimSpace(a)) }

func (m *Mem) addressTaken(id, address string) bool {
	for _, x := range m.mailboxes {
		if x.ID != id && x.Address == address {
			return true
		}
	}
	return false
}

// CreateMailbox implements repo.Mailboxes (global address uniqueness).
func (m *Mem) CreateMailbox(_ context.Context, mb store.Mailbox) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateMailbox"); err != nil {
		return err
	}
	mb.Address = normAddr(mb.Address)
	if _, dup := m.mailboxes[mb.ID]; dup || m.addressTaken(mb.ID, mb.Address) {
		return repo.ErrConflict
	}
	now := m.Now()
	mb.CreatedAt = orNow(mb.CreatedAt, now)
	mb.UpdatedAt = orNow(mb.UpdatedAt, mb.CreatedAt)
	m.mailboxes[mb.ID] = mb
	return nil
}

// GetMailbox implements repo.Mailboxes.
func (m *Mem) GetMailbox(_ context.Context, tenantID, id string) (store.Mailbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetMailbox"); err != nil {
		return store.Mailbox{}, err
	}
	mb, ok := m.mailboxes[id]
	if !ok || mb.TenantID != tenantID {
		return store.Mailbox{}, repo.ErrNotFound
	}
	return mb, nil
}

// ListMailboxes implements repo.Mailboxes (by address).
func (m *Mem) ListMailboxes(_ context.Context, tenantID string) ([]store.Mailbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListMailboxes"); err != nil {
		return nil, err
	}
	out := []store.Mailbox{}
	for _, mb := range m.mailboxes {
		if mb.TenantID == tenantID {
			out = append(out, mb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

// UpdateMailbox implements repo.Mailboxes.
func (m *Mem) UpdateMailbox(_ context.Context, mb store.Mailbox) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateMailbox"); err != nil {
		return err
	}
	cur, ok := m.mailboxes[mb.ID]
	if !ok || cur.TenantID != mb.TenantID {
		return repo.ErrNotFound
	}
	mb.Address = normAddr(mb.Address)
	if m.addressTaken(mb.ID, mb.Address) {
		return repo.ErrConflict
	}
	mb.CreatedAt = cur.CreatedAt
	mb.UpdatedAt = m.Now()
	m.mailboxes[mb.ID] = mb
	return nil
}

// DeleteMailbox implements repo.Mailboxes.
func (m *Mem) DeleteMailbox(_ context.Context, tenantID, id string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteMailbox"); err != nil {
		return err
	}
	mb, ok := m.mailboxes[id]
	if !ok || mb.TenantID != tenantID {
		return repo.ErrNotFound
	}
	var refs []string
	for tid, t := range m.tickets {
		if t.MailboxID == id {
			refs = append(refs, tid)
		}
	}
	if len(refs) > 0 && !force {
		return repo.ErrNotEmpty
	}
	for _, tid := range refs {
		t := m.tickets[tid]
		t.MailboxID = ""
		m.tickets[tid] = t
	}
	delete(m.mailboxes, id)
	return nil
}

// RouteMailbox implements repo.Mailboxes (system scope: every tenant).
func (m *Mem) RouteMailbox(_ context.Context, address string) (store.MailboxRoute, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("RouteMailbox"); err != nil {
		return store.MailboxRoute{}, err
	}
	a := normAddr(address)
	for _, mb := range m.mailboxes {
		if mb.Address == a && a != "" {
			return store.MailboxRoute{TenantID: mb.TenantID, MailboxID: mb.ID, DisplayName: mb.DisplayName,
				Active: mb.Active, AutoAck: mb.AutoAck, AutoAckTemplate: mb.AutoAckTemplate}, nil
		}
	}
	return store.MailboxRoute{}, repo.ErrNotFound
}

// ---------------------------------------------------------------- history

// AppendHistory implements repo.History.
func (m *Mem) AppendHistory(_ context.Context, h store.History) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AppendHistory"); err != nil {
		return err
	}
	if _, err := m.ticket(h.TenantID, h.TicketID); err != nil {
		return err
	}
	h.CreatedAt = orNow(h.CreatedAt, m.Now())
	m.history = append(m.history, h)
	return nil
}

// ListHistory implements repo.History.
func (m *Mem) ListHistory(_ context.Context, tenantID, ticketID string) ([]store.History, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListHistory"); err != nil {
		return nil, err
	}
	out := []store.History{}
	for _, h := range m.history {
		if h.TenantID == tenantID && h.TicketID == ticketID {
			out = append(out, h)
		}
	}
	sortHistory(out)
	return out, nil
}

func sortHistory(out []store.History) {
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
}

// ------------------------------------------------------------------ stats

// TicketStats implements repo.Stats.
func (m *Mem) TicketStats(_ context.Context, tenantID string, since time.Time) (store.Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("TicketStats"); err != nil {
		return store.Stats{}, err
	}
	now := m.Now()
	st := store.Stats{ByStatus: map[string]int64{}, ByPriority: map[string]int64{}, ByAssignee: []store.AssigneeCount{},
		CreatedPerDay: store.DaySeries(since, now), ResolvedPerDay: store.DaySeries(since, now)}
	for _, s := range store.Statuses {
		st.ByStatus[s] = 0
	}
	for _, p := range store.Priorities {
		st.ByPriority[p] = 0
	}
	byAssignee := map[string]*store.AssigneeCount{}
	for _, t := range m.tickets {
		if t.TenantID != tenantID {
			continue
		}
		st.Total++
		st.ByStatus[t.Status]++
		st.ByPriority[t.Priority]++
		if store.Active(t.Status) {
			if t.AssigneeID == "" {
				st.UnassignedOpen++
			} else {
				ac := byAssignee[t.AssigneeID]
				if ac == nil {
					ac = &store.AssigneeCount{AssigneeID: t.AssigneeID}
					byAssignee[t.AssigneeID] = ac
				}
				if ac.AssigneeName == "" {
					ac.AssigneeName = t.AssigneeName
				}
				ac.Count++
			}
		}
		if !t.CreatedAt.Before(since) {
			store.BumpDay(st.CreatedPerDay, t.CreatedAt)
		}
	}
	for _, h := range m.history {
		if h.TenantID == tenantID && h.Field == store.FieldStatus && h.NewValue == store.StatusResolved && !h.CreatedAt.Before(since) {
			store.BumpDay(st.ResolvedPerDay, h.CreatedAt)
		}
	}
	for _, ac := range byAssignee {
		st.ByAssignee = append(st.ByAssignee, *ac)
	}
	sort.Slice(st.ByAssignee, func(i, j int) bool {
		if st.ByAssignee[i].Count != st.ByAssignee[j].Count {
			return st.ByAssignee[i].Count > st.ByAssignee[j].Count
		}
		return st.ByAssignee[i].AssigneeID < st.ByAssignee[j].AssigneeID
	})
	return st, nil
}

// ----------------------------------------------------------------- backup

// AllTickets implements repo.Backup (oldest first).
func (m *Mem) AllTickets(_ context.Context, tenantID string) ([]store.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AllTickets"); err != nil {
		return nil, err
	}
	out := []store.Ticket{}
	for _, t := range m.tickets {
		if t.TenantID == tenantID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AllComments implements repo.Backup.
func (m *Mem) AllComments(_ context.Context, tenantID string) ([]store.Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AllComments"); err != nil {
		return nil, err
	}
	out := []store.Comment{}
	for _, c := range m.comments {
		if c.TenantID == tenantID {
			out = append(out, c)
		}
	}
	sortComments(out)
	return out, nil
}

// AllAttachments implements repo.Backup.
func (m *Mem) AllAttachments(_ context.Context, tenantID string) ([]store.Attachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AllAttachments"); err != nil {
		return nil, err
	}
	out := []store.Attachment{}
	for _, a := range m.attachments {
		if a.TenantID == tenantID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AllTagLinks implements repo.Backup.
func (m *Mem) AllTagLinks(_ context.Context, tenantID string) ([]store.TagLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AllTagLinks"); err != nil {
		return nil, err
	}
	out := []store.TagLink{}
	for tid, set := range m.links {
		t, ok := m.tickets[tid]
		if !ok || t.TenantID != tenantID {
			continue
		}
		for gid := range set {
			out = append(out, store.TagLink{TenantID: tenantID, TicketID: tid, TagID: gid})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TicketID != out[j].TicketID {
			return out[i].TicketID < out[j].TicketID
		}
		return out[i].TagID < out[j].TagID
	})
	return out, nil
}

// AllHistory implements repo.Backup.
func (m *Mem) AllHistory(_ context.Context, tenantID string) ([]store.History, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AllHistory"); err != nil {
		return nil, err
	}
	out := []store.History{}
	for _, h := range m.history {
		if h.TenantID == tenantID {
			out = append(out, h)
		}
	}
	sortHistory(out)
	return out, nil
}

// TenantIDs implements repo.Backup.
func (m *Mem) TenantIDs(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("TenantIDs"); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	add := func(id string) { seen[id] = true }
	for _, t := range m.tickets {
		add(t.TenantID)
	}
	for _, t := range m.tags {
		add(t.TenantID)
	}
	for _, r := range m.rules {
		add(r.TenantID)
	}
	for _, mb := range m.mailboxes {
		add(mb.TenantID)
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// ------------------------------------------------------------------ audit

// AppendAudit implements repo.Store.
func (m *Mem) AppendAudit(_ context.Context, row store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AppendAudit"); err != nil {
		return err
	}
	m.audit = append(m.audit, row)
	return nil
}
