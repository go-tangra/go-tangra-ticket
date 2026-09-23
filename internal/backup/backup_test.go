package backup

// T065: tenant export/import (FR-021, research D13) — FK order, ids preserved,
// skip/overwrite, a non-admin import pinned to the caller's tenant, full and
// cross-tenant restore refused for non-admins, and neither secrets nor object
// bytes in the document.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/audit"
	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/blob"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

const (
	tenantA     = "11111111-1111-7111-8111-111111111111"
	tenantB     = "22222222-2222-7222-8222-222222222222"
	objectBytes = "OBJECT-BYTES-NEVER-EXPORTED"
)

type rec struct {
	mu sync.Mutex
	ev []audit.Event
}

func (r *rec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ev = append(r.ev, e)
	return nil
}

func (r *rec) of(t audit.EventType) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, e := range r.ev {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

func admin(tenant string) authz.Subjects {
	return authz.Subjects{TenantID: tenant, UserID: "root", Roles: []string{authz.RolePlatformAdmin}, ActorKind: authz.ActorAgent}
}

func member(tenant string) authz.Subjects {
	return authz.Subjects{TenantID: tenant, UserID: "u1", ActorKind: authz.ActorAgent}
}

type fixture struct {
	mem                     *memstore.Mem
	blobs                   *blob.Fake
	aud                     *rec
	svc                     *Service
	tag, cat, mb, rule, tk  string
	tk2, cm, att, attKey    string
	resolvedAt, exportedAtT time.Time
}

func seed(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{mem: memstore.New(), blobs: blob.NewFake(), aud: &rec{}}
	f.svc = New(f.mem, f.blobs, f.aud)
	f.exportedAtT = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	f.svc.SetClock(func() time.Time { return f.exportedAtT })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f.tag, f.cat, f.mb, f.rule, f.tk, f.tk2, f.cm, f.att = store.NewID(), store.NewID(), store.NewID(), store.NewID(), store.NewID(), store.NewID(), store.NewID(), store.NewID()
	must(f.mem.CreateTag(ctx, store.Tag{ID: f.tag, TenantID: tenantA, Name: "printer", Kind: store.KindTag, Color: "#ff0000"}))
	must(f.mem.CreateTag(ctx, store.Tag{ID: f.cat, TenantID: tenantA, Name: "Hardware", Kind: store.KindCategory}))
	must(f.mem.CreateMailbox(ctx, store.Mailbox{ID: f.mb, TenantID: tenantA, Address: "support@acme.example", DisplayName: "Acme", Active: true, AutoAck: true, AutoAckTemplate: "Hi {{name}}"}))
	must(f.mem.CreateRule(ctx, store.Rule{ID: f.rule, TenantID: tenantA, Name: "printers", Enabled: true, Match: "all",
		Conditions: []store.Condition{{Field: "subject", Operator: "contains", Value: "printer"}}, Actions: []store.Action{{Type: "tag", TagKind: store.KindTag, TagNames: []string{"printer"}}}}))
	f.resolvedAt = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	must(f.mem.CreateTicket(ctx, store.Ticket{ID: f.tk, TenantID: tenantA, Subject: "Printer jammed", Description: "it jams", BodyHTML: "<p>it jams</p>",
		Status: store.StatusResolved, Priority: store.PriorityHigh, Source: store.SourceEmail, RequesterEmail: "jane@customer.example",
		RequesterName: "Jane", Recipient: "support@acme.example", MailboxID: f.mb, ExternalID: "root-1@customer.example",
		AssigneeID: "u1", AssigneeName: "Ann", CreatedBy: store.CreatedByInbound, ResolvedAt: &f.resolvedAt,
		CreatedAt: time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)}))
	must(f.mem.CreateTicket(ctx, store.Ticket{ID: f.tk2, TenantID: tenantA, Subject: "Manual", Status: store.StatusOpen, Priority: store.PriorityNormal,
		Source: store.SourceManual, CreatedBy: "u1"}))
	must(f.mem.SetTicketTags(ctx, tenantA, f.tk, []string{f.tag, f.cat}))
	must(f.mem.CreateComment(ctx, store.Comment{ID: f.cm, TenantID: tenantA, TicketID: f.tk, Body: "reply", AuthorKind: store.AuthorAgent,
		AuthorID: "u1", MessageID: "ticket.x@acme.example", Delivery: store.DeliverySent}))
	must(f.mem.CreateComment(ctx, store.Comment{ID: store.NewID(), TenantID: tenantA, TicketID: f.tk, Body: "note", Internal: true, AuthorKind: store.AuthorAgent}))
	must(f.mem.AppendHistory(ctx, store.History{ID: store.NewID(), TenantID: tenantA, TicketID: f.tk, Field: store.FieldStatus, OldValue: "open",
		NewValue: "resolved", ActorKind: store.ActorAgent, ActorID: "u1", CreatedAt: f.resolvedAt}))
	f.attKey = store.AttachmentKey(tenantA, f.tk, f.att)
	if _, err := f.blobs.Put(ctx, f.attKey, strings.NewReader(objectBytes), int64(len(objectBytes)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	must(f.mem.CreateAttachment(ctx, store.Attachment{ID: f.att, TenantID: tenantA, TicketID: f.tk, CommentID: f.cm, Filename: "log.txt",
		ContentType: "text/plain", Size: int64(len(objectBytes)), StorageKey: f.attKey, Checksum: "abc"}))
	// tenant B has its own data that no tenant-A operation may touch
	must(f.mem.CreateTicket(ctx, store.Ticket{ID: store.NewID(), TenantID: tenantB, Subject: "B", Status: store.StatusOpen, Priority: store.PriorityNormal, Source: store.SourceManual}))
	return f
}

func roundTrip(t *testing.T, b Backup) Backup {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var out Backup
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExportShapeOrderAndNoSecrets(t *testing.T) {
	f := seed(t)
	b, err := f.svc.Export(context.Background(), member(tenantA), "")
	if err != nil {
		t.Fatal(err)
	}
	if b.SchemaVersion != SchemaVersion || b.TenantID != tenantA || !b.ExportedAt.Equal(f.exportedAtT) {
		t.Fatalf("header = %+v", b)
	}
	if len(b.Tags) != 2 || len(b.Mailboxes) != 1 || len(b.Rules) != 1 || len(b.Tickets) != 2 || len(b.TagLinks) != 2 ||
		len(b.Comments) != 2 || len(b.History) != 1 || len(b.Attachments) != 1 {
		t.Fatalf("counts = %d %d %d %d %d %d %d %d", len(b.Tags), len(b.Mailboxes), len(b.Rules), len(b.Tickets), len(b.TagLinks), len(b.Comments), len(b.History), len(b.Attachments))
	}
	raw, _ := json.Marshal(b)
	s := string(raw)
	// FK order of the document's collections
	last := -1
	for _, k := range []string{`"tags"`, `"mailboxes"`, `"rules"`, `"tickets"`, `"tag_links"`, `"comments"`, `"history"`, `"attachments"`} {
		i := strings.Index(s, k+":")
		if i <= last {
			t.Fatalf("collection %s out of FK order", k)
		}
		last = i
	}
	if !strings.Contains(s, `"body_html":"\u003cp\u003eit jams\u003c/p\u003e"`) || !strings.Contains(s, `"storage_key":"`+f.attKey+`"`) {
		t.Fatalf("html body / object key reference missing: %s", s)
	}
	if strings.Contains(s, objectBytes) || bytes.Contains(raw, []byte("password")) || bytes.Contains(raw, []byte("token")) {
		t.Fatal("export carries object bytes or secret fields")
	}
	if e := f.aud.of(audit.BackupExport); len(e) != 1 || e[0].Outcome != audit.OutcomeOK {
		t.Fatalf("export audit = %+v", e)
	}
	// another tenant: platform admin only
	if _, err := f.svc.Export(context.Background(), member(tenantA), tenantB); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("member cross-tenant export = %v", err)
	}
	if e := f.aud.of(audit.BackupExport); len(e) != 2 || e[1].Outcome != audit.OutcomeRefused {
		t.Fatalf("refusal audit = %+v", e)
	}
	bb, err := f.svc.Export(context.Background(), admin(tenantA), tenantB)
	if err != nil || bb.TenantID != tenantB || len(bb.Tickets) != 1 {
		t.Fatalf("admin export of B = %+v %v", bb, err)
	}
	if _, err := f.svc.Export(context.Background(), authz.Subjects{UserID: "x"}, ""); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("tenantless export = %v", err)
	}
	if _, err := f.svc.Export(context.Background(), admin(tenantA), "not-a-uuid"); !errors.Is(err, ErrBadSchema) {
		t.Fatalf("bad tenant = %v", err)
	}
	// empty tenant exports empty (not null) collections
	empty, _ := New(memstore.New(), nil, nil).Export(context.Background(), member(tenantA), "")
	raw, _ = json.Marshal(empty)
	if strings.Contains(string(raw), "null") {
		t.Fatalf("empty export has nulls: %s", raw)
	}
}

func TestRestoreIntoEmptyStoreRoundTrips(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	b, _ := f.svc.Export(ctx, member(tenantA), "")
	b = roundTrip(t, b)
	dst := memstore.New()
	svc := New(dst, nil, nil)
	res, err := svc.Import(ctx, member(tenantA), b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{CTags: 2, CMailboxes: 1, CRules: 1, CTickets: 2, CTagLinks: 2, CComments: 2, CHistory: 1, CAttachments: 1}
	for k, v := range want {
		if res.Imported[k] != v {
			t.Errorf("imported[%s] = %d want %d (%+v)", k, res.Imported[k], v, res)
		}
	}
	if res.TenantID != tenantA || res.Mode != ModeSkip {
		t.Fatalf("result = %+v", res)
	}
	tk, err := dst.GetTicket(ctx, tenantA, f.tk)
	if err != nil {
		t.Fatal(err)
	}
	if tk.BodyHTML != "<p>it jams</p>" || tk.CommentCount != 2 || tk.LastMessageID != "ticket.x@acme.example" || tk.MailboxID != f.mb ||
		tk.Status != store.StatusResolved || tk.ResolvedAt == nil || !tk.ResolvedAt.Equal(f.resolvedAt) || tk.AssigneeName != "Ann" {
		t.Fatalf("restored ticket = %+v", tk)
	}
	tags, _ := dst.TagsForTickets(ctx, tenantA, []string{f.tk})
	if len(tags[f.tk]) != 2 {
		t.Fatalf("restored tags = %+v", tags)
	}
	a, err := dst.GetAttachment(ctx, tenantA, f.tk, f.att)
	if err != nil || a.StorageKey != f.attKey || a.CommentID != f.cm {
		t.Fatalf("restored attachment = %+v %v", a, err)
	}
	h, _ := dst.ListHistory(ctx, tenantA, f.tk)
	if len(h) != 1 || h[0].NewValue != store.StatusResolved {
		t.Fatalf("restored history = %+v", h)
	}
}

func TestSkipAndOverwrite(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	b, _ := f.svc.Export(ctx, member(tenantA), "")
	b = roundTrip(t, b)
	// skip: re-importing into the same tenant changes nothing
	res, err := f.svc.Import(ctx, member(tenantA), b, Options{Mode: "whatever"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Imported) != 0 || res.Skipped[CTickets] != 2 || res.Skipped[CComments] != 2 || res.Skipped[CHistory] != 1 || res.Skipped[CAttachments] != 1 {
		t.Fatalf("skip result = %+v", res)
	}
	cs, _ := f.mem.ListComments(ctx, tenantA, f.tk)
	h, _ := f.mem.ListHistory(ctx, tenantA, f.tk)
	if len(cs) != 2 || len(h) != 1 {
		t.Fatalf("skip duplicated children: %d comments, %d history", len(cs), len(h))
	}
	// overwrite: parents are rewritten, children still never duplicated
	b.Tickets[0].Subject = "Printer fixed"
	b.Tickets[0].Status = store.StatusOpen
	b.Tickets[0].AssigneeID, b.Tickets[0].AssigneeName = "", ""
	for i := range b.Tags {
		if b.Tags[i].ID == f.tag {
			b.Tags[i].Color = "#00ff00"
		}
	}
	b.Mailboxes[0].DisplayName = "Acme Help"
	b.Rules[0].Name = "printer rule"
	res, err = f.svc.Import(ctx, member(tenantA), b, Options{Mode: ModeOverwrite})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeOverwrite || res.Imported[CTickets] != 2 || res.Imported[CTags] != 2 || res.Skipped[CComments] != 2 {
		t.Fatalf("overwrite result = %+v", res)
	}
	var tk store.Ticket
	for _, row := range b.Tickets {
		if row.ID == f.tk {
			tk, _ = f.mem.GetTicket(ctx, tenantA, f.tk)
		}
	}
	if tk.Subject != "Printer fixed" || tk.Status != store.StatusOpen || tk.AssigneeID != "" || tk.ResolvedAt != nil {
		t.Fatalf("overwritten ticket = %+v", tk)
	}
	tg, _ := f.mem.GetTag(ctx, tenantA, f.tag)
	mb, _ := f.mem.GetMailbox(ctx, tenantA, f.mb)
	r, _ := f.mem.GetRule(ctx, tenantA, f.rule)
	if tg.Color != "#00ff00" || mb.DisplayName != "Acme Help" || r.Name != "printer rule" {
		t.Fatalf("overwritten tag/mailbox/rule = %+v %+v %+v", tg, mb, r)
	}
	if im := f.aud.of(audit.BackupImport); len(im) != 2 || im[1].Details["mode"] != ModeOverwrite {
		t.Fatalf("import audit = %+v", im)
	}
}

func TestNonAdminPinnedToOwnTenant(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	b, _ := f.svc.Export(ctx, member(tenantA), "")
	before, _ := f.mem.AllTickets(ctx, tenantB)
	for _, o := range []Options{{TenantID: tenantB}, {Full: true}, {TenantID: tenantB, Full: true}} {
		if _, err := f.svc.Import(ctx, member(tenantA), b, o); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("%+v: err = %v", o, err)
		}
	}
	after, _ := f.mem.AllTickets(ctx, tenantB)
	if len(after) != len(before) {
		t.Fatal("refused import wrote into tenant B")
	}
	if r := f.aud.of(audit.BackupImport); len(r) != 3 || r[0].Outcome != audit.OutcomeRefused || r[0].TenantID != tenantA {
		t.Fatalf("refusal audit = %+v", r)
	}
	// a backup of another tenant imported by a member lands in the member's
	// own tenant (with fresh ids), never in the document's tenant
	bb, _ := f.svc.Export(ctx, admin(tenantA), tenantB)
	res, err := f.svc.Import(ctx, member(tenantA), bb, Options{})
	if err != nil || res.TenantID != tenantA || res.Imported[CTickets] != 1 {
		t.Fatalf("foreign document import = %+v %v", res, err)
	}
	if got, _ := f.mem.AllTickets(ctx, tenantB); len(got) != len(before) {
		t.Fatal("tenant B changed")
	}
	if _, err := f.svc.Import(ctx, authz.Subjects{UserID: "x"}, b, Options{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("tenantless import = %v", err)
	}
}

func TestAdminCrossTenantRestore(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	b, _ := f.svc.Export(ctx, member(tenantA), "")
	res, err := f.svc.Import(ctx, admin(tenantA), roundTrip(t, b), Options{TenantID: tenantB})
	if err != nil {
		t.Fatal(err)
	}
	// fresh ids; the mailbox address belongs to tenant A (skipped, the ticket
	// keeps no dangling link); the object key names tenant A (skipped: bytes
	// are never exported)
	if res.TenantID != tenantB || res.Imported[CTickets] != 2 || res.Skipped[CMailboxes] != 1 || res.Skipped[CAttachments] != 1 ||
		res.Imported[CComments] != 2 || res.Imported[CTagLinks] != 2 || res.Imported[CHistory] != 1 {
		t.Fatalf("cross-tenant result = %+v", res)
	}
	bt, _ := f.mem.AllTickets(ctx, tenantB)
	if len(bt) != 3 {
		t.Fatalf("tenant B tickets = %d", len(bt))
	}
	for _, tk := range bt {
		if tk.ID == f.tk || tk.ID == f.tk2 || tk.MailboxID != "" {
			t.Fatalf("cross-tenant ticket kept origin id/link: %+v", tk)
		}
	}
	at, _ := f.mem.AllTickets(ctx, tenantA)
	if len(at) != 2 {
		t.Fatal("origin tenant changed")
	}
	if im := f.aud.of(audit.BackupImport); len(im) != 1 || im[0].TenantID != tenantB || im[0].Details["cross_tenant"] != true {
		t.Fatalf("cross-tenant audit = %+v", im)
	}
}

func TestAdminFullRestore(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	b, _ := f.svc.Export(ctx, member(tenantA), "")
	// data added after the backup disappears; an object the backup still
	// references survives the wipe, an unreferenced one is removed
	extra := store.NewID()
	_ = f.mem.CreateTicket(ctx, store.Ticket{ID: extra, TenantID: tenantA, Subject: "later", Status: store.StatusOpen, Priority: store.PriorityNormal, Source: store.SourceManual})
	extraAtt := store.NewID()
	extraKey := store.AttachmentKey(tenantA, extra, extraAtt)
	_, _ = f.blobs.Put(ctx, extraKey, strings.NewReader("x"), 1, "text/plain")
	_ = f.mem.CreateAttachment(ctx, store.Attachment{ID: extraAtt, TenantID: tenantA, TicketID: extra, Filename: "x", StorageKey: extraKey})
	res, err := f.svc.Import(ctx, admin(tenantA), roundTrip(t, b), Options{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 3+1+2+1 || res.Imported[CTickets] != 2 || res.Imported[CAttachments] != 1 {
		t.Fatalf("full restore = %+v", res)
	}
	if _, err := f.mem.GetTicket(ctx, tenantA, extra); err == nil {
		t.Fatal("post-backup ticket survived a full restore")
	}
	if f.blobs.Len() != 1 {
		t.Fatalf("objects after wipe = %d", f.blobs.Len())
	}
	if bt, _ := f.mem.AllTickets(ctx, tenantB); len(bt) != 1 {
		t.Fatal("full restore touched tenant B")
	}
}

func TestValidate(t *testing.T) {
	good := func() Backup {
		return Backup{SchemaVersion: SchemaVersion, TenantID: tenantA,
			Tags:        []store.Tag{{ID: store.NewID(), Name: "n", Kind: store.KindTag}},
			Mailboxes:   []store.Mailbox{{ID: store.NewID(), Address: "a@b.example"}},
			Rules:       []store.Rule{{ID: store.NewID(), Name: "r"}},
			Tickets:     []TicketRow{{Ticket: store.Ticket{ID: store.NewID(), Status: store.StatusOpen, Priority: store.PriorityLow}}},
			TagLinks:    []store.TagLink{{TicketID: store.NewID(), TagID: store.NewID()}},
			Comments:    []store.Comment{{ID: store.NewID(), TicketID: store.NewID()}},
			History:     []store.History{{ID: store.NewID(), TicketID: store.NewID()}},
			Attachments: []AttachmentRow{{Attachment: store.Attachment{ID: store.NewID(), TicketID: store.NewID()}}}}
	}
	if err := Validate(good()); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Backup){
		"schema":        func(b *Backup) { b.SchemaVersion = 2 },
		"tenant":        func(b *Backup) { b.TenantID = "x" },
		"tag id":        func(b *Backup) { b.Tags[0].ID = "1" },
		"tag kind":      func(b *Backup) { b.Tags[0].Kind = "label" },
		"tag name":      func(b *Backup) { b.Tags[0].Name = " " },
		"mailbox id":    func(b *Backup) { b.Mailboxes[0].ID = "" },
		"mailbox addr":  func(b *Backup) { b.Mailboxes[0].Address = "nope" },
		"rule id":       func(b *Backup) { b.Rules[0].ID = "r" },
		"rule name":     func(b *Backup) { b.Rules[0].Name = "" },
		"ticket id":     func(b *Backup) { b.Tickets[0].ID = "t" },
		"ticket mbox":   func(b *Backup) { b.Tickets[0].MailboxID = "m" },
		"ticket status": func(b *Backup) { b.Tickets[0].Status = "done" },
		"ticket prio":   func(b *Backup) { b.Tickets[0].Priority = "p0" },
		"link":          func(b *Backup) { b.TagLinks[0].TagID = "" },
		"comment":       func(b *Backup) { b.Comments[0].TicketID = "t" },
		"history":       func(b *Backup) { b.History[0].ID = "h" },
		"attachment":    func(b *Backup) { b.Attachments[0].CommentID = "c" },
	}
	for name, mut := range cases {
		b := good()
		mut(&b)
		if err := Validate(b); !errors.Is(err, ErrBadSchema) {
			t.Errorf("%s: %v", name, err)
		}
	}
	big := good()
	big.TagLinks = make([]store.TagLink, MaxRows+1)
	if err := Validate(big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	// Import refuses an invalid document before writing
	mem := memstore.New()
	bad := good()
	bad.SchemaVersion = 0
	if _, err := New(mem, nil, nil).Import(context.Background(), member(tenantA), bad, Options{}); !errors.Is(err, ErrBadSchema) {
		t.Fatalf("import invalid = %v", err)
	}
	if n, _ := mem.TenantIDs(context.Background()); len(n) != 0 {
		t.Fatal("invalid import wrote rows")
	}
}

func TestImportEdgeRows(t *testing.T) {
	ctx := context.Background()
	mem := memstore.New()
	svc := New(mem, nil, nil)
	tk, orphanTicket := store.NewID(), store.NewID()
	b := Backup{SchemaVersion: SchemaVersion, TenantID: tenantA,
		Tickets:     []TicketRow{{Ticket: store.Ticket{ID: tk, Subject: "  ", MailboxID: store.NewID()}}}, // unknown mailbox, blank subject
		TagLinks:    []store.TagLink{{TicketID: tk, TagID: store.NewID()}},                                // unknown tag
		Comments:    []store.Comment{{ID: store.NewID(), TicketID: orphanTicket, Body: "x"}},              // unknown ticket
		History:     []store.History{{ID: store.NewID(), TicketID: orphanTicket}},                         // not created here
		Attachments: []AttachmentRow{{Attachment: store.Attachment{ID: store.NewID(), TicketID: tk}, StorageKey: "tenants/" + tenantA + "/../x"}}}
	res, err := svc.Import(ctx, member(tenantA), b, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported[CTickets] != 1 || res.Skipped[CTagLinks] != 1 || res.Skipped[CComments] != 1 || res.Skipped[CHistory] != 1 || res.Skipped[CAttachments] != 1 {
		t.Fatalf("edge result = %+v", res)
	}
	got, _ := mem.GetTicket(ctx, tenantA, tk)
	if got.Subject != "(no subject)" || got.MailboxID != "" {
		t.Fatalf("edge ticket = %+v", got)
	}
}

func TestStoreFailuresPropagate(t *testing.T) {
	ctx := context.Background()
	f := seed(t)
	b, _ := f.svc.Export(ctx, member(tenantA), "")
	for _, m := range []string{"ListTags", "ListMailboxes", "ListRules", "AllTickets", "AllTagLinks", "AllComments", "AllHistory", "AllAttachments"} {
		f.mem.FailNext(m)
		if _, err := f.svc.Export(ctx, member(tenantA), ""); err == nil {
			t.Errorf("export: %s failure swallowed", m)
		}
	}
	for _, m := range []string{"GetTag", "CreateTag", "GetMailbox", "CreateMailbox", "GetRule", "CreateRule", "ListMailboxes", "GetTicket", "CreateTicket",
		"AddTicketTags", "GetComment", "CreateComment", "AppendHistory", "GetAttachment", "CreateAttachment"} {
		dst := memstore.New()
		dst.FailNext(m)
		if _, err := New(dst, nil, nil).Import(ctx, member(tenantA), b, Options{}); err == nil {
			t.Errorf("import: %s failure swallowed", m)
		}
	}
	for _, m := range []string{"UpdateTag", "UpdateMailbox", "UpdateRule", "UpdateTicket", "SetStatus", "SetAssignee"} {
		f.mem.FailNext(m)
		if _, err := f.svc.Import(ctx, member(tenantA), b, Options{Mode: ModeOverwrite}); err == nil {
			t.Errorf("overwrite: %s failure swallowed", m)
		}
	}
	for _, m := range []string{"AllTickets", "DeleteTicket", "ListRules", "DeleteRule", "ListTags", "DeleteTag", "ListMailboxes", "DeleteMailbox"} {
		g := seed(t)
		g.mem.FailNext(m)
		if _, err := g.svc.Import(ctx, admin(tenantA), b, Options{Full: true}); err == nil {
			t.Errorf("wipe: %s failure swallowed", m)
		}
	}
}
