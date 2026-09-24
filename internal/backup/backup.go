// Package backup exports and imports a tenant's ticket data (FR-021, research
// D13). The export is schema-versioned and FK-ordered — tags, mailboxes, rules,
// tickets, tag links, comments, history, attachments (metadata by reference) —
// with ids preserved so a restore round-trips. It carries NO secrets (the relay
// token and SMTP password live in warden and are never stored here) and NO
// object bytes: attachments travel as metadata plus their tenant-prefixed
// object key, restored only into the tenant that owns the key.
//
// Import modes: skip (default) keeps existing rows, overwrite rewrites them.
// A restore into another tenant than the caller's (cross-tenant) or a full
// restore (wipe, then load) requires a platform admin; a cross-tenant restore
// mints fresh ids so primary keys never collide with the origin's rows.
// Comments, history, tag links and attachments are append-only children:
// existing ones are always kept, and history is restored only for tickets the
// import created (a repeated import never duplicates it).
package backup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/audit"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/authz"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/blob"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/repo"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// SchemaVersion is the backup document version this service reads and writes.
const SchemaVersion = 1

// Restore modes.
const (
	ModeSkip      = "skip"
	ModeOverwrite = "overwrite"
)

// Errors.
var (
	ErrBadSchema = errors.New("backup: invalid backup document")
	ErrTooLarge  = errors.New("backup: document exceeds the row limit")
)

// MaxRows bounds any single collection of an import (a parser guard).
const MaxRows = 200000

// Collection names (Result keys, export order).
const (
	CTags        = "tags"
	CMailboxes   = "mailboxes"
	CRules       = "rules"
	CTickets     = "tickets"
	CTagLinks    = "tag_links"
	CComments    = "comments"
	CHistory     = "history"
	CAttachments = "attachments"
)

// TicketRow is a ticket with its raw HTML body (store.Ticket hides it from
// API responses; a backup must carry it).
type TicketRow struct {
	store.Ticket
	BodyHTML string `json:"body_html,omitempty"`
}

// AttachmentRow is attachment metadata with its object key (a reference only:
// the bytes stay in object storage).
type AttachmentRow struct {
	store.Attachment
	StorageKey string `json:"storage_key"`
}

// Backup is the export document.
type Backup struct {
	SchemaVersion int             `json:"schema_version"`
	ExportedAt    time.Time       `json:"exported_at"`
	TenantID      string          `json:"tenant_id"`
	Tags          []store.Tag     `json:"tags"`
	Mailboxes     []store.Mailbox `json:"mailboxes"`
	Rules         []store.Rule    `json:"rules"`
	Tickets       []TicketRow     `json:"tickets"`
	TagLinks      []store.TagLink `json:"tag_links"`
	Comments      []store.Comment `json:"comments"`
	History       []store.History `json:"history"`
	Attachments   []AttachmentRow `json:"attachments"`
}

// Options control an import.
type Options struct {
	Mode     string // skip | overwrite
	TenantID string // target tenant ("" = the caller's; another one is platform-admin only)
	Full     bool   // wipe the target tenant first (platform-admin only)
}

// Result reports what an import did.
type Result struct {
	TenantID string         `json:"tenant_id"`
	Mode     string         `json:"mode"`
	Imported map[string]int `json:"imported"`
	Skipped  map[string]int `json:"skipped"`
	Deleted  int            `json:"deleted"`
}

// Service exports and imports tenant data.
type Service struct {
	st    repo.Store
	blobs blob.Store
	aud   audit.Recorder
	now   func() time.Time
}

// New builds the service. blobs (optional) lets a full restore remove the
// objects of wiped attachments the backup does not reference; aud may be nil.
func New(st repo.Store, blobs blob.Store, aud audit.Recorder) *Service {
	return &Service{st: st, blobs: blobs, aud: aud, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock injects the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// target resolves and authorises the tenant an operation acts on.
func target(subj authz.Subjects, tenantID string) (string, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return "", err
	}
	if tenantID == "" || tenantID == subj.TenantID {
		return subj.TenantID, nil
	}
	if err := authz.RequirePlatformAdmin(subj); err != nil {
		return "", err
	}
	if !isUUID(tenantID) {
		return "", fmt.Errorf("%w: tenant_id", ErrBadSchema)
	}
	return tenantID, nil
}

func (s *Service) refused(ctx context.Context, subj authz.Subjects, t audit.EventType, reason string) {
	tenant := subj.TenantID
	if tenant == "" {
		tenant = audit.NilTenant
	}
	audit.Emit(ctx, s.aud, audit.Event{TenantID: tenant, EventType: t, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectBackup, SubjectID: string(t), Outcome: audit.OutcomeRefused, Reason: reason})
}

// Export builds a backup of the caller's tenant (another tenant: platform admin).
func (s *Service) Export(ctx context.Context, subj authz.Subjects, tenantID string) (Backup, error) {
	t, err := target(subj, tenantID)
	if err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			s.refused(ctx, subj, audit.BackupExport, "platform-admin required")
		}
		return Backup{}, err
	}
	b := Backup{SchemaVersion: SchemaVersion, ExportedAt: s.now(), TenantID: t}
	if b.Tags, err = s.st.ListTags(ctx, t, ""); err != nil {
		return Backup{}, err
	}
	if b.Mailboxes, err = s.st.ListMailboxes(ctx, t); err != nil {
		return Backup{}, err
	}
	if b.Rules, err = s.st.ListRules(ctx, t); err != nil {
		return Backup{}, err
	}
	tks, err := s.st.AllTickets(ctx, t)
	if err != nil {
		return Backup{}, err
	}
	for _, tk := range tks {
		b.Tickets = append(b.Tickets, TicketRow{Ticket: tk, BodyHTML: tk.BodyHTML})
	}
	if b.TagLinks, err = s.st.AllTagLinks(ctx, t); err != nil {
		return Backup{}, err
	}
	if b.Comments, err = s.st.AllComments(ctx, t); err != nil {
		return Backup{}, err
	}
	if b.History, err = s.st.AllHistory(ctx, t); err != nil {
		return Backup{}, err
	}
	atts, err := s.st.AllAttachments(ctx, t)
	if err != nil {
		return Backup{}, err
	}
	for _, a := range atts {
		b.Attachments = append(b.Attachments, AttachmentRow{Attachment: a, StorageKey: a.StorageKey})
	}
	nonNil(&b)
	audit.Emit(ctx, s.aud, audit.Event{TenantID: t, EventType: audit.BackupExport, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectBackup, SubjectID: "export", Outcome: audit.OutcomeOK,
		Details: map[string]any{"tickets": len(b.Tickets), "comments": len(b.Comments), "cross_tenant": t != subj.TenantID}})
	return b, nil
}

func nonNil(b *Backup) {
	if b.Tags == nil {
		b.Tags = []store.Tag{}
	}
	if b.Mailboxes == nil {
		b.Mailboxes = []store.Mailbox{}
	}
	if b.Rules == nil {
		b.Rules = []store.Rule{}
	}
	if b.Tickets == nil {
		b.Tickets = []TicketRow{}
	}
	if b.TagLinks == nil {
		b.TagLinks = []store.TagLink{}
	}
	if b.Comments == nil {
		b.Comments = []store.Comment{}
	}
	if b.History == nil {
		b.History = []store.History{}
	}
	if b.Attachments == nil {
		b.Attachments = []AttachmentRow{}
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRE.MatchString(s) }

func bad(what string) error { return fmt.Errorf("%w: %s", ErrBadSchema, what) }

// ids checks that every required id is a UUID and every optional one is empty
// or a UUID.
func ids(what string, required []string, optional ...string) error {
	for _, v := range required {
		if !isUUID(v) {
			return bad(what + " id")
		}
	}
	for _, v := range optional {
		if v != "" && !isUUID(v) {
			return bad(what + " reference")
		}
	}
	return nil
}

// Validate checks a parsed backup document before anything is written.
func Validate(b Backup) error {
	if b.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema version %d", ErrBadSchema, b.SchemaVersion)
	}
	for name, n := range map[string]int{CTags: len(b.Tags), CMailboxes: len(b.Mailboxes), CRules: len(b.Rules), CTickets: len(b.Tickets),
		CTagLinks: len(b.TagLinks), CComments: len(b.Comments), CHistory: len(b.History), CAttachments: len(b.Attachments)} {
		if n > MaxRows {
			return fmt.Errorf("%w: %s has %d rows", ErrTooLarge, name, n)
		}
	}
	if b.TenantID != "" && !isUUID(b.TenantID) {
		return bad("tenant_id")
	}
	for _, t := range b.Tags {
		if err := ids("tag", []string{t.ID}); err != nil {
			return err
		}
		if strings.TrimSpace(t.Name) == "" || !store.ValidKind(t.Kind) {
			return bad("tag name/kind")
		}
	}
	for _, m := range b.Mailboxes {
		if err := ids("mailbox", []string{m.ID}); err != nil {
			return err
		}
		if !strings.Contains(m.Address, "@") {
			return bad("mailbox address")
		}
	}
	for _, r := range b.Rules {
		if err := ids("rule", []string{r.ID}); err != nil {
			return err
		}
		if strings.TrimSpace(r.Name) == "" {
			return bad("rule name")
		}
	}
	for _, t := range b.Tickets {
		if err := ids("ticket", []string{t.ID}, t.MailboxID); err != nil {
			return err
		}
		if (t.Status != "" && !store.ValidStatus(t.Status)) || (t.Priority != "" && !store.ValidPriority(t.Priority)) {
			return bad("ticket status/priority")
		}
	}
	for _, l := range b.TagLinks {
		if err := ids("tag link", []string{l.TicketID, l.TagID}); err != nil {
			return err
		}
	}
	for _, c := range b.Comments {
		if err := ids("comment", []string{c.ID, c.TicketID}); err != nil {
			return err
		}
	}
	for _, h := range b.History {
		if err := ids("history", []string{h.ID, h.TicketID}); err != nil {
			return err
		}
	}
	for _, a := range b.Attachments {
		if err := ids("attachment", []string{a.ID, a.TicketID}, a.CommentID); err != nil {
			return err
		}
	}
	return nil
}

// importer carries one import's state.
type importer struct {
	s      *Service
	ctx    context.Context
	target string
	mode   string
	res    Result
	// created: tickets inserted by this import (their history is restored).
	created map[string]bool
}

func (im *importer) imported(c string) { im.res.Imported[c]++ }
func (im *importer) skipped(c string)  { im.res.Skipped[c]++ }

// upsert applies the skip/overwrite policy to one row: exists reports whether
// the row is present, create/update write it. A conflict (unique name/address
// held by another row) skips the row.
func (im *importer) upsert(c string, exists func() error, create, update func() error) (bool, error) {
	err := exists()
	switch {
	case err == nil:
		if im.mode != ModeOverwrite {
			im.skipped(c)
			return false, nil
		}
		err = update()
	case errors.Is(err, repo.ErrNotFound):
		err = create()
		if err == nil {
			im.imported(c)
			return true, nil
		}
	default:
		return false, err
	}
	switch {
	case err == nil:
		im.imported(c)
	case errors.Is(err, repo.ErrConflict), errors.Is(err, repo.ErrNotFound):
		im.skipped(c)
	default:
		return false, err
	}
	return false, nil
}

// Import loads a backup into the target tenant (the caller's by default).
func (s *Service) Import(ctx context.Context, subj authz.Subjects, b Backup, opts Options) (Result, error) {
	res := Result{Imported: map[string]int{}, Skipped: map[string]int{}}
	tgt, err := target(subj, opts.TenantID)
	if err == nil && opts.Full {
		err = authz.RequirePlatformAdmin(subj)
	}
	if err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			s.refused(ctx, subj, audit.BackupImport, "platform-admin required")
		}
		return res, err
	}
	if err := Validate(b); err != nil {
		return res, err
	}
	res.TenantID = tgt
	res.Mode = ModeSkip
	if opts.Mode == ModeOverwrite {
		res.Mode = ModeOverwrite
	}
	if b.TenantID != tgt {
		// another tenant's rows (or an origin-less document): fresh ids so
		// primary keys never collide; its object keys never match the target.
		b = remap(b)
	}
	if opts.Full {
		n, err := s.wipe(ctx, tgt, b)
		res.Deleted = n
		if err != nil {
			return res, err
		}
	}
	im := &importer{s: s, ctx: ctx, target: tgt, mode: res.Mode, res: res, created: map[string]bool{}}
	if err := im.run(b); err != nil {
		return im.res, err
	}
	audit.Emit(ctx, s.aud, audit.Event{TenantID: tgt, EventType: audit.BackupImport, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectBackup, SubjectID: "import", Outcome: audit.OutcomeOK,
		Details: map[string]any{"mode": im.res.Mode, "full": opts.Full, "cross_tenant": tgt != subj.TenantID, "tickets": im.res.Imported[CTickets]}})
	return im.res, nil
}

func (im *importer) run(b Backup) error {
	ctx, st, tgt := im.ctx, im.s.st, im.target
	for _, t := range b.Tags {
		t.TenantID = tgt
		if _, err := im.upsert(CTags, func() error { _, err := st.GetTag(ctx, tgt, t.ID); return err },
			func() error { return st.CreateTag(ctx, t) }, func() error { return st.UpdateTag(ctx, t) }); err != nil {
			return err
		}
	}
	for _, m := range b.Mailboxes {
		m.TenantID = tgt
		m.Address = strings.ToLower(strings.TrimSpace(m.Address))
		if _, err := im.upsert(CMailboxes, func() error { _, err := st.GetMailbox(ctx, tgt, m.ID); return err },
			func() error { return st.CreateMailbox(ctx, m) }, func() error { return st.UpdateMailbox(ctx, m) }); err != nil {
			return err
		}
	}
	for _, r := range b.Rules {
		r.TenantID = tgt
		if _, err := im.upsert(CRules, func() error { _, err := st.GetRule(ctx, tgt, r.ID); return err },
			func() error { return st.CreateRule(ctx, r) }, func() error { _, err := st.UpdateRule(ctx, r); return err }); err != nil {
			return err
		}
	}
	if err := im.tickets(b.Tickets); err != nil {
		return err
	}
	if err := im.links(b.TagLinks); err != nil {
		return err
	}
	for _, c := range b.Comments {
		c.TenantID = tgt
		if err := im.child(CComments, func() error { _, err := st.GetComment(ctx, tgt, c.ID); return err },
			func() error { return st.CreateComment(ctx, c) }); err != nil {
			return err
		}
	}
	for _, h := range b.History {
		if !im.created[h.TicketID] {
			im.skipped(CHistory)
			continue
		}
		h.TenantID = tgt
		if err := st.AppendHistory(ctx, h); err != nil {
			if errors.Is(err, repo.ErrNotFound) || errors.Is(err, repo.ErrConflict) {
				im.skipped(CHistory)
				continue
			}
			return err
		}
		im.imported(CHistory)
	}
	prefix := "tenants/" + tgt + "/"
	for _, a := range b.Attachments {
		if !strings.HasPrefix(a.StorageKey, prefix) || strings.Contains(a.StorageKey, "..") {
			im.skipped(CAttachments) // bytes are never exported: only the owner's objects can be re-linked
			continue
		}
		row := a.Attachment
		row.TenantID, row.StorageKey = tgt, a.StorageKey
		if err := im.child(CAttachments, func() error { _, err := st.GetAttachment(ctx, tgt, row.TicketID, row.ID); return err },
			func() error { return st.CreateAttachment(ctx, row) }); err != nil {
			return err
		}
	}
	return nil
}

// child inserts an append-only row unless it exists (never overwritten).
func (im *importer) child(c string, exists func() error, create func() error) error {
	err := exists()
	if err == nil {
		im.skipped(c)
		return nil
	}
	if !errors.Is(err, repo.ErrNotFound) {
		return err
	}
	switch err := create(); {
	case err == nil:
		im.imported(c)
	case errors.Is(err, repo.ErrConflict), errors.Is(err, repo.ErrNotFound):
		im.skipped(c)
	default:
		return err
	}
	return nil
}

func (im *importer) tickets(rows []TicketRow) error {
	ctx, st, tgt := im.ctx, im.s.st, im.target
	mbs, err := st.ListMailboxes(ctx, tgt)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, m := range mbs {
		known[m.ID] = true
	}
	for _, row := range rows {
		t := row.Ticket
		t.TenantID, t.BodyHTML = tgt, row.BodyHTML
		if !known[t.MailboxID] {
			t.MailboxID = "" // its mailbox is not in the target: keep the ticket, drop the link
		}
		if strings.TrimSpace(t.Subject) == "" {
			t.Subject = "(no subject)"
		}
		// counters are rebuilt by the comments that follow
		t.CommentCount, t.LastMessageID = 0, ""
		created, err := im.upsert(CTickets, func() error { _, err := st.GetTicket(ctx, tgt, t.ID); return err },
			func() error { return st.CreateTicket(ctx, t) }, func() error { return im.overwriteTicket(t) })
		if err != nil {
			return err
		}
		if created {
			im.created[t.ID] = true
		}
	}
	return nil
}

// overwriteTicket rewrites the mutable fields of an existing ticket.
func (im *importer) overwriteTicket(t store.Ticket) error {
	ctx, st, tgt := im.ctx, im.s.st, im.target
	at := t.UpdatedAt
	if at.IsZero() {
		at = im.s.now()
	}
	p := store.TicketPatch{Subject: &t.Subject, Description: &t.Description}
	if t.Priority != "" {
		p.Priority = &t.Priority
	}
	if _, err := st.UpdateTicket(ctx, tgt, t.ID, p, at); err != nil {
		return err
	}
	if t.Status != "" {
		if _, err := st.SetStatus(ctx, tgt, t.ID, t.Status, at); err != nil {
			return err
		}
	}
	_, err := st.SetAssignee(ctx, tgt, t.ID, t.AssigneeID, t.AssigneeName, at)
	return err
}

func (im *importer) links(rows []store.TagLink) error {
	byTicket := map[string][]string{}
	var order []string
	for _, l := range rows {
		if _, ok := byTicket[l.TicketID]; !ok {
			order = append(order, l.TicketID)
		}
		byTicket[l.TicketID] = append(byTicket[l.TicketID], l.TagID)
	}
	if len(order) == 0 {
		return nil
	}
	have, err := im.s.st.TagsForTickets(im.ctx, im.target, order)
	if err != nil {
		return err
	}
	for _, tid := range order {
		linked := map[string]bool{}
		for _, t := range have[tid] {
			linked[t.ID] = true
		}
		var add []string
		for _, g := range byTicket[tid] {
			if linked[g] {
				im.skipped(CTagLinks)
				continue
			}
			linked[g] = true
			add = append(add, g)
		}
		if len(add) == 0 {
			continue
		}
		switch err := im.s.st.AddTicketTags(im.ctx, im.target, tid, add); {
		case err == nil:
			im.res.Imported[CTagLinks] += len(add)
		case errors.Is(err, repo.ErrNotFound), errors.Is(err, repo.ErrConflict):
			im.res.Skipped[CTagLinks] += len(add)
		default:
			return err
		}
	}
	return nil
}

// wipe deletes every record of the target tenant (full restore). Objects of
// removed attachments are deleted unless the backup references them again.
func (s *Service) wipe(ctx context.Context, tgt string, b Backup) (int, error) {
	keep := map[string]bool{}
	for _, a := range b.Attachments {
		keep[a.StorageKey] = true
	}
	n := 0
	tks, err := s.st.AllTickets(ctx, tgt)
	if err != nil {
		return n, err
	}
	for _, t := range tks {
		keys, err := s.st.DeleteTicket(ctx, tgt, t.ID)
		if err != nil && !errors.Is(err, repo.ErrNotFound) {
			return n, err
		}
		n++
		for _, k := range keys {
			if !keep[k] && s.blobs != nil {
				_ = s.blobs.Delete(ctx, k) // best effort: an orphan object is harmless
			}
		}
	}
	rules, err := s.st.ListRules(ctx, tgt)
	if err != nil {
		return n, err
	}
	for _, r := range rules {
		if err := s.st.DeleteRule(ctx, tgt, r.ID); err != nil && !errors.Is(err, repo.ErrNotFound) {
			return n, err
		}
		n++
	}
	tags, err := s.st.ListTags(ctx, tgt, "")
	if err != nil {
		return n, err
	}
	for _, t := range tags {
		if err := s.st.DeleteTag(ctx, tgt, t.ID); err != nil && !errors.Is(err, repo.ErrNotFound) {
			return n, err
		}
		n++
	}
	mbs, err := s.st.ListMailboxes(ctx, tgt)
	if err != nil {
		return n, err
	}
	for _, m := range mbs {
		if err := s.st.DeleteMailbox(ctx, tgt, m.ID, true); err != nil && !errors.Is(err, repo.ErrNotFound) {
			return n, err
		}
		n++
	}
	return n, nil
}

// remap rewrites every id and in-document reference with fresh ids; object
// keys are left as they are (they name the origin tenant, so the attachment
// rows are skipped on a cross-tenant restore).
func remap(b Backup) Backup {
	fresh := map[string]string{}
	id := func(old string) string {
		if old == "" {
			return ""
		}
		if n, ok := fresh[old]; ok {
			return n
		}
		n := store.NewID()
		fresh[old] = n
		return n
	}
	out := b
	out.Tags = append([]store.Tag(nil), b.Tags...)
	for i := range out.Tags {
		out.Tags[i].ID = id(out.Tags[i].ID)
	}
	out.Mailboxes = append([]store.Mailbox(nil), b.Mailboxes...)
	for i := range out.Mailboxes {
		out.Mailboxes[i].ID = id(out.Mailboxes[i].ID)
	}
	out.Rules = append([]store.Rule(nil), b.Rules...)
	for i := range out.Rules {
		out.Rules[i].ID = id(out.Rules[i].ID)
	}
	out.Tickets = append([]TicketRow(nil), b.Tickets...)
	for i := range out.Tickets {
		t := &out.Tickets[i]
		t.ID, t.MailboxID = id(t.ID), id(t.MailboxID)
	}
	out.TagLinks = append([]store.TagLink(nil), b.TagLinks...)
	for i := range out.TagLinks {
		out.TagLinks[i].TicketID, out.TagLinks[i].TagID = id(out.TagLinks[i].TicketID), id(out.TagLinks[i].TagID)
	}
	out.Comments = append([]store.Comment(nil), b.Comments...)
	for i := range out.Comments {
		out.Comments[i].ID, out.Comments[i].TicketID = id(out.Comments[i].ID), id(out.Comments[i].TicketID)
	}
	out.History = append([]store.History(nil), b.History...)
	for i := range out.History {
		out.History[i].ID, out.History[i].TicketID = id(out.History[i].ID), id(out.History[i].TicketID)
	}
	out.Attachments = append([]AttachmentRow(nil), b.Attachments...)
	for i := range out.Attachments {
		a := &out.Attachments[i]
		a.ID, a.TicketID, a.CommentID = id(a.ID), id(a.TicketID), id(a.CommentID)
	}
	return out
}
