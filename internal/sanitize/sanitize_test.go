package sanitize

// T044: hostile HTML renders inert, cid: images resolve only to the ticket's
// own attachments, remote/unknown images are dropped, links are hardened, and
// arbitrary input never panics or yields active content.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/mailparse"
	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

const ticketID = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func fixtureHTML(t testing.TB, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mail", name))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mailparse.Parse(raw, mailparse.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return m.HTML
}

var forbiddenTags = map[string]bool{"script": true, "style": true, "form": true, "input": true, "button": true, "iframe": true,
	"object": true, "embed": true, "svg": true, "math": true, "link": true, "meta": true, "base": true, "frame": true, "frameset": true,
	"textarea": true, "select": true, "video": true, "audio": true, "source": true}

// inert walks the sanitised output with the HTML tokenizer and reports the
// first active construct it finds ("" when inert).
func inert(out string) string {
	z := html.NewTokenizer(strings.NewReader(out))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				return ""
			}
			return "tokenizer: " + z.Err().Error()
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if forbiddenTags[tok.Data] {
				return "tag " + tok.Data
			}
			for _, a := range tok.Attr {
				k := strings.ToLower(a.Key)
				v := strings.ToLower(strings.TrimSpace(a.Val))
				switch {
				case strings.HasPrefix(k, "on"):
					return "handler " + k
				case k == "style" || k == "srcset" || k == "formaction" || k == "background":
					return "attr " + k
				case strings.HasPrefix(v, "javascript:") || strings.HasPrefix(v, "vbscript:") || strings.HasPrefix(v, "data:"):
					return "url " + v
				case k == "src" && !strings.HasPrefix(a.Val, "/api/ticket/v1/tickets/"):
					return "external src " + a.Val
				}
			}
		}
	}
}

func TestHostileHTMLIsInert(t *testing.T) {
	out := HTML(fixtureHTML(t, "hostile-html.eml"), nil)
	if why := inert(out); why != "" {
		t.Fatalf("active content (%s) in:\n%s", why, out)
	}
	for _, bad := range []string{"tracker.evil.example", "evil.example/", "alert(", "document.cookie", "steal()", "cid:"} {
		if strings.Contains(out, bad) {
			t.Errorf("output still carries %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "Hello") {
		t.Errorf("text lost: %s", out)
	}
	z := html.NewTokenizer(strings.NewReader(out))
	var link bool
	for tt := z.Next(); tt != html.ErrorToken; tt = z.Next() {
		tok := z.Token()
		if tok.Data != "a" || tt != html.StartTagToken {
			continue
		}
		attrs := map[string]string{}
		for _, a := range tok.Attr {
			attrs[a.Key] = a.Val
		}
		if attrs["href"] == "https://docs.example/help" {
			link = strings.Contains(attrs["rel"], "noopener") && strings.Contains(attrs["rel"], "noreferrer") && attrs["target"] == "_blank"
		}
	}
	if !link {
		t.Fatalf("safe link not kept/hardened:\n%s", out)
	}
}

func TestCIDRewrittenToOwnAttachments(t *testing.T) {
	resolve := func(cid string) (string, bool) {
		if cid == "shot1@customer.example" {
			return AttachmentURL(ticketID, "att-1"), true
		}
		return "", false
	}
	out := HTML(fixtureHTML(t, "html-inline-image.eml"), resolve)
	want := `src="/api/ticket/v1/tickets/` + ticketID + `/attachments/att-1"`
	if !strings.Contains(out, want) || strings.Contains(out, "cid:") {
		t.Fatalf("cid not rewritten:\n%s", out)
	}
	if why := inert(out); why != "" {
		t.Fatal(why)
	}
	for _, in := range []string{
		`<p>x</p><img src="cid:unknown@x" alt="u">`,
		`<p>x</p><img src="https://remote.example/p.gif">`,
		`<p>x</p><img src="//remote.example/p.gif">`,
		`<p>x</p><img src="/api/ticket/v1/tickets/other/attachments/forged">`,
		`<p>x</p><img src="data:image/png;base64,AAAA">`,
		`<p>x</p><img>`,
	} {
		out := HTML(in, resolve)
		if strings.Contains(out, "<img") || !strings.Contains(out, "<p>x</p>") {
			t.Errorf("image not dropped: %q -> %q", in, out)
		}
	}
	// cid matching is case-insensitive on the id and tolerates angle brackets
	if out := HTML(`<img src="cid:SHOT1@customer.example">`, func(c string) (string, bool) { return resolve(strings.ToLower(c)) }); !strings.Contains(out, "att-1") {
		t.Fatalf("case-insensitive cid: %s", out)
	}
	// a cid link is not a navigable href
	if out := HTML(`<a href="cid:shot1@customer.example">x</a>`, resolve); strings.Contains(out, "cid:") {
		t.Fatalf("cid href kept: %s", out)
	}
}

func TestMessage(t *testing.T) {
	tk := store.Ticket{ID: ticketID, Description: "plain body", BodyHTML: `<p>Hi <img src="cid:A@x"></p>`}
	atts := []store.Attachment{{ID: "att-a", ContentID: "<a@X>"}, {ID: "att-b"}}
	b := Message(tk, atts)
	if b.Text != "plain body" || !strings.Contains(b.HTMLSanitized, AttachmentURL(ticketID, "att-a")) {
		t.Fatalf("body = %+v", b)
	}
	tk.Description = ""
	if b := Message(tk, nil); b.Text != "Hi" || strings.Contains(b.HTMLSanitized, "<img") {
		t.Fatalf("text fallback = %+v", b)
	}
	if b := Message(store.Ticket{ID: ticketID, Description: "only text"}, nil); b.HTMLSanitized != "" || b.Text != "only text" {
		t.Fatalf("no html = %+v", b)
	}
	if AttachmentURL("a/b", "c?d") != "/api/ticket/v1/tickets/a%2Fb/attachments/c%3Fd" {
		t.Fatal(AttachmentURL("a/b", "c?d"))
	}
}

func FuzzHTML(f *testing.F) {
	for _, name := range []string{"hostile-html.eml", "html-inline-image.eml"} {
		f.Add(fixtureHTML(f, name))
	}
	for _, s := range []string{`<img src="cid:a@b" onerror=x>`, `<a href="jav&#x61;script:x">`, `<svg><script>1</script>`, `<<img src=x>`,
		`<math><mi xlink:href="javascript:1">`, `<p style="x:url(y)">`, `<noscript><img src=x onerror=1></noscript>`, `<img src="cid:">`} {
		f.Add(s)
	}
	resolve := func(cid string) (string, bool) { return AttachmentURL(ticketID, "att-"+cid), cid != "" }
	f.Fuzz(func(t *testing.T, in string) {
		out := HTML(in, resolve)
		if why := inert(out); why != "" {
			t.Fatalf("active content (%s) for %q:\n%s", why, in, out)
		}
		if strings.Contains(strings.ToLower(out), "<script") {
			t.Fatalf("script survived: %s", out)
		}
	})
}
