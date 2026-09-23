package backup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-freya/freya/services/ticket/internal/authz"
	"github.com/go-freya/freya/services/ticket/internal/memstore"
)

// FuzzImportParser: arbitrary JSON never panics the validator or the importer;
// a document of the wrong shape is refused before any write, and whatever is
// imported lands in the caller's tenant only.
func FuzzImportParser(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema_version":1}`))
	f.Add([]byte(`{"schema_version":1,"tenant_id":"11111111-1111-7111-8111-111111111111","tickets":[{"id":"01900000-0000-7000-8000-000000000001","subject":"s"}]}`))
	f.Add([]byte(`{"schema_version":1,"tags":[{"id":"01900000-0000-7000-8000-000000000002","name":"a","kind":"tag"}],"tag_links":[{"ticket_id":"01900000-0000-7000-8000-000000000001","tag_id":"01900000-0000-7000-8000-000000000002"}]}`))
	f.Add([]byte(`{"schema_version":1,"attachments":[{"id":"01900000-0000-7000-8000-000000000003","ticket_id":"01900000-0000-7000-8000-000000000001","storage_key":"tenants/22222222-2222-7222-8222-222222222222/x"}]}`))
	f.Add([]byte(`{"schema_version":1,"mailboxes":[{"id":"01900000-0000-7000-8000-000000000004","address":"A@B.example"}]}`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var b Backup
		if err := json.Unmarshal(raw, &b); err != nil {
			return
		}
		verr := Validate(b)
		mem := memstore.New()
		subj := authz.Subjects{TenantID: tenantA, UserID: "u", ActorKind: authz.ActorAgent}
		res, err := New(mem, nil, nil).Import(context.Background(), subj, b, Options{Mode: ModeOverwrite})
		if verr != nil && err == nil {
			t.Fatal("invalid document imported")
		}
		ids, _ := mem.TenantIDs(context.Background())
		for _, id := range ids {
			if id != tenantA {
				t.Fatalf("import wrote into tenant %s", id)
			}
		}
		if err != nil {
			return
		}
		for _, n := range res.Imported {
			if n < 0 {
				t.Fatal("negative count")
			}
		}
	})
}
