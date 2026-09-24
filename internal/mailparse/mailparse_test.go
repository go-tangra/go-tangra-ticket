package mailparse

// T032: the parser over the fixture corpus (testdata/mail) and synthetic
// messages exercising the limits.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mail", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func parseFixture(t *testing.T, name string) Message {
	t.Helper()
	m, err := Parse(fixture(t, name), DefaultLimits())
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func TestPlain(t *testing.T) {
	m := parseFixture(t, "plain.eml")
	if m.Subject != "Printer on floor 3 is jammed" || m.FromName != "Jane Customer" || m.FromEmail != "jane@customer.example" {
		t.Fatalf("headers = %+v", m)
	}
	if m.MessageID != "plain-0001@mail.customer.example" || len(m.To) != 1 || m.To[0] != "support@acme.example" {
		t.Fatalf("ids = %q %v", m.MessageID, m.To)
	}
	if !strings.HasPrefix(m.Text, "Hello support,") || !strings.HasSuffix(m.Text, "Jane") || m.HTML != "" {
		t.Fatalf("text = %q html = %q", m.Text, m.HTML)
	}
	if m.IsAuto() || IsDaemonSender(m.FromEmail) || m.SpamScore != 0 || len(m.Attachments) != 0 {
		t.Fatalf("flags = %+v", m)
	}
}

func TestLegacyCharsetAndRFC2047(t *testing.T) {
	m := parseFixture(t, "legacy-charset.eml")
	if m.Subject != "プリンターの故障" {
		t.Errorf("iso-2022-jp subject = %q", m.Subject)
	}
	if m.FromName != "Иван Петров" {
		t.Errorf("windows-1251 name = %q", m.FromName)
	}
	if !strings.Contains(m.Text, "Здравствуйте!") || !strings.Contains(m.Text, "Принтер не работает") {
		t.Errorf("windows-1251 body = %q", m.Text)
	}
}

func TestQuotedPrintable(t *testing.T) {
	m := parseFixture(t, "quoted-printable.eml")
	if m.Subject != "VPN fällt ständig aus" || m.FromName != "Jürgen Müller" {
		t.Errorf("headers = %q %q", m.Subject, m.FromName)
	}
	if !strings.Contains(m.Text, "Grüße aus München — the VPN drops every 10 minutes and the error says “timeout”.") ||
		!strings.Contains(m.Text, "soft-wrapped by quoted-printable encoding because it exceeds") {
		t.Errorf("qp body = %q", m.Text)
	}
}

func TestHTMLWithInlineImage(t *testing.T) {
	m := parseFixture(t, "html-inline-image.eml")
	if m.Text != "See the screenshot below." {
		t.Errorf("text alternative preferred = %q", m.Text)
	}
	if !strings.Contains(m.HTML, `src="cid:shot1@customer.example"`) {
		t.Errorf("html = %q", m.HTML)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	a := m.Attachments[0]
	if a.Filename != "shot.png" || a.ContentType != "image/png" || a.ContentID != "shot1@customer.example" || !a.Inline ||
		!bytes.HasPrefix(a.Data, []byte("\x89PNG")) {
		t.Errorf("inline image = %+v", a)
	}
}

func TestAttachmentsAndSafeNames(t *testing.T) {
	m := parseFixture(t, "attachments.eml")
	if len(m.Attachments) != 2 {
		t.Fatalf("attachments = %d", len(m.Attachments))
	}
	pdf, png := m.Attachments[0], m.Attachments[1]
	if pdf.Filename != "invoice.pdf" || pdf.ContentType != "application/pdf" || pdf.Inline || !bytes.HasPrefix(pdf.Data, []byte("%PDF-1.4")) {
		t.Errorf("pdf = %+v", pdf)
	}
	if png.Filename != "device photo.png" || png.ContentType != "image/png" || png.Inline {
		t.Errorf("path traversal name not reduced: %+v", png)
	}
	if !strings.HasPrefix(m.Text, "Please find the invoice") {
		t.Errorf("text = %q", m.Text)
	}
}

func TestHTMLOnlyFallsBackToText(t *testing.T) {
	raw := "From: a@b.example\r\nSubject: html only\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><style>p{color:red}</style><script>x()</script></head><body><p>Line one</p><div>Line &amp; two</div><br>three</body></html>"
	m, err := Parse([]byte(raw), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if m.HTML == "" || m.Text != "Line one\nLine & two\n\nthree" {
		t.Fatalf("fallback text = %q", m.Text)
	}
	if strings.Contains(m.Text, "color") || strings.Contains(m.Text, "x()") {
		t.Fatalf("style/script leaked into text: %q", m.Text)
	}
}

func TestThreadingHeaders(t *testing.T) {
	m := parseFixture(t, "reply-in-reply-to.eml")
	want := "ticket.0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55.0190f7c2-7000-7000-8000-000000000001@acme.example"
	if len(m.InReplyTo) != 1 || m.InReplyTo[0] != want {
		t.Errorf("in-reply-to = %v", m.InReplyTo)
	}
	if len(m.References) != 2 || m.References[0] != "plain-0001@mail.customer.example" || m.References[1] != want {
		t.Errorf("references = %v", m.References)
	}
	chain := m.ThreadIDs()
	if len(chain) != 2 || chain[0] != want || chain[1] != "plain-0001@mail.customer.example" {
		t.Errorf("thread ids (deduplicated, in-reply-to first) = %v", chain)
	}
}

func TestAutomationIndicators(t *testing.T) {
	if m := parseFixture(t, "auto-submitted.eml"); !m.IsAuto() || m.AutoSubmitted != "auto-replied" {
		t.Errorf("auto-submitted = %+v", m.AutoSubmitted)
	}
	if m := parseFixture(t, "bulk-precedence.eml"); !m.IsAuto() || m.Precedence != "bulk" {
		t.Errorf("bulk = %q", m.Precedence)
	}
	if m := parseFixture(t, "noreply-sender.eml"); !IsDaemonSender(m.FromEmail) || m.IsAuto() {
		t.Errorf("no-reply sender = %q", m.FromEmail)
	}
	if m := parseFixture(t, "spam-high-score.eml"); m.SpamScore != 12.5 {
		t.Errorf("spam score = %v", m.SpamScore)
	}
	for _, s := range []string{"", "MAILER-DAEMON@x", "postmaster@x", "bounce+123@x", "do-not-reply@x"} {
		if !IsDaemonSender(s) {
			t.Errorf("%q should be a daemon sender", s)
		}
	}
	if IsDaemonSender("jane@customer.example") {
		t.Error("human flagged as daemon")
	}
	for _, v := range []string{"", "abc", "NaN", "+Inf", "-Inf"} {
		if got := parseSpamScore(v); got != 0 {
			t.Errorf("spam %q = %v", v, got)
		}
	}
	m := Message{AutoSubmitted: "no"}
	if m.IsAuto() {
		t.Error("Auto-Submitted: no is human mail")
	}
	for _, p := range []string{"list", "junk", "auto_reply"} {
		if !(Message{Precedence: p}).IsAuto() {
			t.Errorf("precedence %s", p)
		}
	}
}

func TestHeaderInjectionFlattened(t *testing.T) {
	m := parseFixture(t, "header-injection.eml")
	for _, v := range []string{m.Subject, m.FromName, m.FromEmail, m.MessageID} {
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("control characters survived in %q", v)
		}
	}
	if m.Subject != "Help Bcc: victim@evil.example" || m.FromEmail != "eve@evil.example" {
		t.Fatalf("subject=%q from=%q", m.Subject, m.FromEmail)
	}
}

func TestNoSubjectAndMissingParts(t *testing.T) {
	m, err := Parse([]byte("From: x@y.example\r\n\r\nbody only\r\n"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != NoSubject || m.Text != "body only" || m.MessageID != "" {
		t.Fatalf("m = %+v", m)
	}
	// not a message at all: raw text is kept (bounded) so nothing is lost
	m, err = Parse([]byte("just some text without headers"), DefaultLimits())
	if err != nil || m.Text != "just some text without headers" || m.Subject != NoSubject {
		t.Fatalf("raw fallback = %+v %v", m, err)
	}
	// unparseable From keeps a header-safe bare value only when it is an address
	m, _ = Parse([]byte("From: not an address\r\nSubject: s\r\n\r\nx"), DefaultLimits())
	if m.FromEmail != "" {
		t.Fatalf("garbage From kept: %q", m.FromEmail)
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":       "passwd",
		`C:\Windows\evil.exe`:    "evil.exe",
		"":                       "attachment",
		"..":                     "attachment",
		".hidden":                "hidden",
		"a\x00b\nc.txt":          "a_b_c.txt",
		"normal name.pdf":        "normal name.pdf",
		"/":                      "attachment",
		strings.Repeat("x", 400): strings.Repeat("x", 200),
	}
	for in, want := range cases {
		if got := SafeFilename(in); got != want {
			t.Errorf("SafeFilename(%q) = %q want %q", in, got, want)
		}
	}
}

func TestLimitsTooLarge(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxBodyBytes = 64
	if _, err := Parse(bytes.Repeat([]byte("x"), 65), lim); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized message: %v", err)
	}
}

func multipartMessage(parts int, partBody string) []byte {
	var b strings.Builder
	b.WriteString("From: a@b.example\r\nSubject: many\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n")
	for i := 0; i < parts; i++ {
		fmt.Fprintf(&b, "--B\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"f%d.bin\"\r\n\r\n%s\r\n", i, partBody)
	}
	b.WriteString("--B--\r\n")
	return []byte(b.String())
}

func TestLimitsPartCount(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxParts = 5
	m, err := Parse(multipartMessage(20, "data"), lim)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) > 5 || !m.Truncated {
		t.Fatalf("part cap: %d attachments truncated=%v", len(m.Attachments), m.Truncated)
	}
}

func TestLimitsAttachmentSizeSkipped(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxAttachmentBytes = 16
	m, err := Parse(multipartMessage(2, strings.Repeat("A", 64)), lim)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != 0 || len(m.Skipped) != 2 || m.Skipped[0].Filename != "f0.bin" || m.Skipped[0].Reason != SkipTooLarge {
		t.Fatalf("oversized attachments must be skipped and noted: %+v %+v", m.Attachments, m.Skipped)
	}
	// generated oversized attachment against the default 25 MiB cap
	big := base64Lines(26 << 20)
	raw := "From: a@b.example\r\nSubject: big\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--B\r\nContent-Type: application/zip\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"huge.zip\"\r\n\r\n" + big + "\r\n--B--\r\n"
	lim = DefaultLimits()
	lim.MaxBodyBytes = 64 << 20
	m, err = Parse([]byte(raw), lim)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != 0 || len(m.Skipped) != 1 || m.Skipped[0].Filename != "huge.zip" || m.Text != "see attached" {
		t.Fatalf("25 MiB cap: %d attachments, skipped %+v", len(m.Attachments), m.Skipped)
	}
}

// base64Lines returns n decoded bytes' worth of base64 text in 76-column lines.
func base64Lines(n int) string {
	line := strings.Repeat("QUFB", 19) // 57 bytes decoded per 76-char line
	var b strings.Builder
	for written := 0; written < n; written += 57 {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	return b.String()
}

func TestLimitsDepth(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxDepth = 3
	var b strings.Builder
	b.WriteString("From: a@b.example\r\nSubject: deep\r\nContent-Type: multipart/mixed; boundary=\"L0\"\r\n\r\n")
	const levels = 12
	for i := 1; i <= levels; i++ {
		fmt.Fprintf(&b, "--L%d\r\nContent-Type: multipart/mixed; boundary=\"L%d\"\r\n\r\n", i-1, i)
	}
	fmt.Fprintf(&b, "--L%d\r\nContent-Type: text/plain\r\n\r\ndeep text\r\n--L%d--\r\n", levels, levels)
	for i := levels - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "--L%d--\r\n", i)
	}
	m, err := Parse([]byte(b.String()), lim)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text == "deep text" || !m.Truncated {
		t.Fatalf("depth cap not enforced: %q truncated=%v", m.Text, m.Truncated)
	}
	lim.MaxDepth = 20
	if m, _ = Parse([]byte(b.String()), lim); m.Text != "deep text" {
		t.Fatalf("within depth: %q", m.Text)
	}
}

func TestBodyCapAndCleanText(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxTextBytes = 10
	m, err := Parse([]byte("From: a@b.example\r\nSubject: s\r\nContent-Type: text/plain\r\n\r\nabc\x00def\x07ghijklmnopqrstuvwxyz\xff"), lim)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Text) > 10 || strings.ContainsRune(m.Text, 0) {
		t.Fatalf("text cap / NUL: %q", m.Text)
	}
	for _, r := range m.Text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			t.Fatalf("control char kept: %q", m.Text)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	if HTMLToText("") != "" {
		t.Fatal("empty")
	}
	if got := HTMLToText("<p>a</p><p>b</p><ul><li>c</li></ul>\r\n\r\n\r\n\r\nd"); got != "a\nb\nc\n\nd" {
		t.Fatalf("got %q", got)
	}
}

func TestCharsetFallbacks(t *testing.T) {
	for _, cs := range []string{"koi8-r", "koi8-u", "cp1251", "windows-1252", "windows-1250", "latin1", "iso-8859-2", "iso-8859-5",
		"iso-8859-15", "gb2312", "big5", "shift_jis", "euc-jp", "iso-2022-jp", "euc-kr", "utf-16", "utf-16be"} {
		if charsetDecoder(cs) == nil {
			t.Errorf("no decoder for %s", cs)
		}
	}
	if charsetDecoder("x-unknown") != nil {
		t.Error("unknown charset")
	}
	if decodeCharset([]byte("abc"), "x-unknown") != "abc" {
		t.Error("unknown charset passthrough")
	}
	if string(decodeTransfer([]byte("!!!not base64"), "base64")) != "!!!not base64" {
		t.Error("bad base64 passthrough")
	}
	if string(decodeTransfer([]byte("a=ZZ"), "quoted-printable")) != "a=ZZ" {
		t.Error("bad qp passthrough")
	}
}

func TestRelayHelpers(t *testing.T) {
	if ids := ParseMessageIDs(" <relay-1@kumo>\r\n <second@x>"); len(ids) != 2 || ids[0] != "relay-1@kumo" {
		t.Fatalf("ids = %v", ids)
	}
	for in, want := range map[string]string{
		"Support@ACME.example":            "support@acme.example",
		"Acme <Help@Acme.Example>":        "help@acme.example",
		"not an address":                  "",
		"a@b.example\r\nBcc: x@y.example": "",
	} {
		if got := NormalizeRecipient(in); got != want {
			t.Errorf("NormalizeRecipient(%q) = %q want %q", in, got, want)
		}
	}
}
