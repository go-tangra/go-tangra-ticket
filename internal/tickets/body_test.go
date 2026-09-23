package tickets

// T045/T049 (service side): the sanitised message body and tenant-scoped
// attachment access.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/go-freya/freya/services/ticket/internal/sanitize"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

func (f *fixture) emailTicket(t *testing.T) (store.Ticket, store.Attachment) {
	t.Helper()
	ctx := context.Background()
	tk := store.Ticket{ID: store.NewID(), TenantID: tenantA, Subject: "html", Status: store.StatusOpen, Priority: store.PriorityNormal,
		Source: store.SourceEmail, BodyHTML: `<p onclick="x()">Hi</p><img src="cid:img1@x"><script>alert(1)</script>`}
	if err := f.st.CreateTicket(ctx, tk); err != nil {
		t.Fatal(err)
	}
	att := store.Attachment{ID: store.NewID(), TenantID: tenantA, TicketID: tk.ID, Filename: "shot.png", ContentType: "image/png",
		Size: 4, ContentID: "img1@x", Inline: true, StorageKey: store.AttachmentKey(tenantA, tk.ID, "a1")}
	if _, err := f.blobs.Put(ctx, att.StorageKey, strings.NewReader("\x89PNG"), 4, "image/png"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateAttachment(ctx, att); err != nil {
		t.Fatal(err)
	}
	return tk, att
}

func TestBodySanitised(t *testing.T) {
	f := newFixture(t)
	tk, att := f.emailTicket(t)
	b, err := f.svc.Body(context.Background(), agentOf(tenantA), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.HTMLSanitized, "script") || strings.Contains(b.HTMLSanitized, "onclick") ||
		!strings.Contains(b.HTMLSanitized, sanitize.AttachmentURL(tk.ID, att.ID)) || b.Text != "Hi" {
		t.Fatalf("body = %+v", b)
	}
	if _, err := f.svc.Body(context.Background(), agentOf(tenantB), tk.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant body: %v", err)
	}
	f.st.FailNext("ListAttachments")
	if _, err := f.svc.Body(context.Background(), agentOf(tenantA), tk.ID); err == nil {
		t.Fatal("attachment listing failure swallowed")
	}
}

func TestOpenAttachmentScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tk, att := f.emailTicket(t)
	a, rc, err := f.svc.OpenAttachment(ctx, agentOf(tenantA), tk.ID, att.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if a.ID != att.ID || string(data) != "\x89PNG" {
		t.Fatalf("attachment = %+v %q", a, data)
	}
	if _, _, err := f.svc.OpenAttachment(ctx, agentOf(tenantB), tk.ID, att.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant ticket: %v", err)
	}
	other, _ := f.svc.Create(ctx, agentOf(tenantA), CreateInput{Subject: "other"})
	if _, _, err := f.svc.OpenAttachment(ctx, agentOf(tenantA), other.ID, att.ID); !errors.Is(err, ErrAttachmentNotFound) {
		t.Fatalf("attachment of another ticket: %v", err)
	}
	_ = f.blobs.Delete(ctx, att.StorageKey)
	if _, _, err := f.svc.OpenAttachment(ctx, agentOf(tenantA), tk.ID, att.ID); err == nil || errors.Is(err, ErrAttachmentNotFound) {
		t.Fatalf("missing object must be an unavailability, not a 404: %v", err)
	}
	noBlobs := New(Deps{Store: f.st})
	if _, _, err := noBlobs.OpenAttachment(ctx, agentOf(tenantA), tk.ID, att.ID); err == nil {
		t.Fatal("no object store")
	}
}
