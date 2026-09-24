package mailparse

// T033: arbitrary bytes never panic, never break the limits and never yield
// header values that could inject into a reply.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzParse(f *testing.F) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "testdata", "mail"))
	if err != nil {
		f.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".eml") {
			f.Add(fixture(f, e.Name()))
		}
	}
	f.Add([]byte(""))
	f.Add([]byte("Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x--"))
	f.Add([]byte("Subject: =?utf-16?b?//4=?=\r\nContent-Type: text/plain; charset=utf-16\r\nContent-Transfer-Encoding: base64\r\n\r\n//5hAA=="))

	lim := Limits{MaxBodyBytes: 1 << 20, MaxTextBytes: 64 << 10, MaxAttachmentBytes: 4 << 10, MaxParts: 8, MaxDepth: 4}
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := Parse(raw, lim)
		if err != nil {
			if len(raw) <= int(lim.MaxBodyBytes) {
				t.Fatalf("error below the cap: %v", err)
			}
			return
		}
		if len(m.Attachments)+len(m.Skipped) > lim.MaxParts {
			t.Fatalf("parts %d+%d exceed cap", len(m.Attachments), len(m.Skipped))
		}
		total := 0
		for _, a := range m.Attachments {
			if int64(len(a.Data)) > lim.MaxAttachmentBytes {
				t.Fatalf("attachment %d bytes exceeds cap", len(a.Data))
			}
			if a.Filename == "" || strings.ContainsAny(a.Filename, "/\\\x00\r\n") {
				t.Fatalf("unsafe filename %q", a.Filename)
			}
			total += len(a.Data)
		}
		if int64(total) > lim.MaxBodyBytes*2 {
			t.Fatalf("decoded attachments %d far exceed the message cap", total)
		}
		if len(m.Text) > int(lim.MaxTextBytes) || len(m.HTML) > int(lim.MaxTextBytes) {
			t.Fatalf("body cap: text=%d html=%d", len(m.Text), len(m.HTML))
		}
		for _, v := range append([]string{m.Subject, m.FromName, m.FromEmail, m.MessageID, m.Text, m.HTML}, m.ThreadIDs()...) {
			if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
				t.Fatalf("invalid output %q", v)
			}
		}
		for _, v := range append([]string{m.Subject, m.FromName, m.FromEmail, m.MessageID}, m.ThreadIDs()...) {
			if strings.ContainsAny(v, "\r\n") {
				t.Fatalf("header value with a line break: %q", v)
			}
		}
		if m.Subject == "" {
			t.Fatal("empty subject")
		}
	})
}
