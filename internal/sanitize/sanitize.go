// Package sanitize produces the only rendition of an HTML email body the
// module ever serves (research D7, SR-003). The raw HTML is stored as received
// but never returned; this package runs it through a bluemonday UGC-style
// policy (no scripts, event handlers, forms, iframes, objects, embeds, style
// elements or style attributes; links restricted to http/https/mailto and
// hardened with rel="nofollow noreferrer noopener" target="_blank") and then
// a second pass over the (well-formed) sanitised output that keeps an <img>
// ONLY when its source is a cid: reference resolving to one of the ticket's
// own attachments — rewritten to the tenant-scoped attachment route — so no
// remote image (tracking pixel) or forged path survives. The UI shows the
// result in a sandboxed srcdoc iframe (no scripts, opaque origin) under the
// edge CSP as the second and third layers.
package sanitize

import (
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"

	"github.com/go-freya/freya/services/ticket/internal/mailparse"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

// Body is the /tickets/{id}/body response.
type Body struct {
	HTMLSanitized string `json:"html_sanitized"`
	Text          string `json:"text"`
}

// AttachmentPrefix is the browser route prefix of ticket attachments.
const AttachmentPrefix = "/api/ticket/v1/tickets/"

// AttachmentURL is the tenant-scoped download route of an attachment.
func AttachmentURL(ticketID, attachmentID string) string {
	return AttachmentPrefix + url.PathEscape(ticketID) + "/attachments/" + url.PathEscape(attachmentID)
}

// policy is immutable after construction and safe for concurrent use.
var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowURLSchemes("mailto", "http", "https", "cid")
	p.AllowRelativeURLs(false)
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// HTML sanitises raw. resolve maps a Content-ID (without "cid:") to the URL of
// the ticket's attachment; an image whose cid does not resolve, and every
// non-cid image, is dropped.
func HTML(raw string, resolve func(contentID string) (string, bool)) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return images(policy.Sanitize(raw), resolve)
}

func cidOf(src string) (string, bool) {
	if len(src) < 4 || !strings.EqualFold(src[:4], "cid:") {
		return "", false
	}
	id := src[4:]
	if u, err := url.PathUnescape(id); err == nil {
		id = u
	}
	id = strings.Trim(strings.TrimSpace(id), "<>")
	return id, id != ""
}

// images re-serialises the sanitised markup, resolving cid: images and
// dropping every other image and cid: link target.
func images(clean string, resolve func(string) (string, bool)) string {
	z := html.NewTokenizer(strings.NewReader(clean))
	var b strings.Builder
	b.Grow(len(clean))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return b.String()
		}
		tok := z.Token()
		if tt == html.StartTagToken || tt == html.SelfClosingTagToken {
			switch tok.Data {
			case "img":
				id, ok := "", false
				for _, a := range tok.Attr {
					if a.Key == "src" {
						id, ok = cidOf(a.Val)
					}
				}
				target := ""
				if ok && resolve != nil {
					target, ok = resolve(id)
				}
				if !ok || !strings.HasPrefix(target, AttachmentPrefix) {
					continue // remote, relative, unknown or missing source: drop the image
				}
				attrs := tok.Attr[:0]
				for _, a := range tok.Attr {
					if a.Key == "src" {
						a.Val = target
					}
					attrs = append(attrs, a)
				}
				tok.Attr = attrs
			case "a":
				attrs := tok.Attr[:0]
				for _, a := range tok.Attr {
					if a.Key == "href" {
						if _, isCID := cidOf(a.Val); isCID {
							continue
						}
					}
					attrs = append(attrs, a)
				}
				tok.Attr = attrs
			}
		}
		b.WriteString(tok.String())
	}
}

func normCID(v string) string { return strings.ToLower(strings.Trim(strings.TrimSpace(v), "<>")) }

// Message renders a ticket's message body: sanitised HTML with its inline
// images bound to the ticket's attachments, and the plain text (the stored
// text body, else derived from the HTML).
func Message(t store.Ticket, atts []store.Attachment) Body {
	byCID := map[string]string{}
	for _, a := range atts {
		if c := normCID(a.ContentID); c != "" {
			byCID[c] = AttachmentURL(t.ID, a.ID)
		}
	}
	resolve := func(cid string) (string, bool) {
		u, ok := byCID[normCID(cid)]
		return u, ok
	}
	b := Body{HTMLSanitized: HTML(t.BodyHTML, resolve), Text: t.Description}
	if strings.TrimSpace(b.Text) == "" && t.BodyHTML != "" {
		b.Text = mailparse.HTMLToText(t.BodyHTML)
	}
	return b
}
