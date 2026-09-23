// Package mailparse turns a raw RFC 822 message into the fields a ticket needs
// (research D3): decoded subject and sender (RFC 2047, legacy code pages), the
// plain and HTML bodies (text/plain preferred; HTML→text fallback), attachments
// with content id and inline flag, the threading chain (Message-ID,
// In-Reply-To, References) and the automation indicators (Auto-Submitted,
// Precedence, X-Spam-Score).
//
// Ported from go-tangra-ticket internal/webhook/mailparse.go onto the standard
// library plus golang.org/x/text, with the hardening the spec requires: the
// message size, part count, MIME depth, per-attachment size and body length
// are all bounded (zip-bomb / pathological MIME guard), header values are
// flattened to one line (no CR/LF can reach an outbound reply), bodies are
// valid UTF-8 without NUL/control characters, and attachment names are reduced
// to a safe base name.
package mailparse

import (
	"bytes"
	"encoding/base64"
	"errors"
	"html"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	textunicode "golang.org/x/text/encoding/unicode"
)

// ErrTooLarge is returned for a message above Limits.MaxBodyBytes.
var ErrTooLarge = errors.New("mailparse: message exceeds the size limit")

// NoSubject is the placeholder subject of a message without one.
const NoSubject = "(no subject)"

// SkipTooLarge is the Skipped reason of an attachment above the size cap.
const SkipTooLarge = "too_large"

// Bounds on header-derived values.
const (
	maxSubjectRunes = 998 // RFC 5322 line limit
	maxNameRunes    = 200
	maxAddressBytes = 320
	maxIDBytes      = 998
	maxThreadIDs    = 50
	maxFilename     = 200
)

// Limits bound the parser (config inbound.*).
type Limits struct {
	MaxBodyBytes       int64 // whole raw message (inbound.max_body_bytes)
	MaxTextBytes       int64 // each of the plain and HTML bodies after decoding
	MaxAttachmentBytes int64 // one decoded attachment (inbound.max_attachment_bytes)
	MaxParts           int   // MIME parts visited (inbound.max_parts)
	MaxDepth           int   // multipart nesting (inbound.max_depth)
}

// DefaultLimits are the reference limits (10 MiB message, 25 MiB attachment,
// 100 parts, depth 10) with a 1 MiB body cap (the ticket description bound).
func DefaultLimits() Limits {
	return Limits{MaxBodyBytes: 10 << 20, MaxTextBytes: 1 << 20, MaxAttachmentBytes: 25 << 20, MaxParts: 100, MaxDepth: 10}
}

func (l Limits) orDefaults() Limits {
	d := DefaultLimits()
	if l.MaxBodyBytes <= 0 {
		l.MaxBodyBytes = d.MaxBodyBytes
	}
	if l.MaxTextBytes <= 0 {
		l.MaxTextBytes = d.MaxTextBytes
	}
	if l.MaxAttachmentBytes <= 0 {
		l.MaxAttachmentBytes = d.MaxAttachmentBytes
	}
	if l.MaxParts <= 0 {
		l.MaxParts = d.MaxParts
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	return l
}

// Attachment is one decoded file part.
type Attachment struct {
	Filename    string // safe base name
	ContentType string // media type only (parameters dropped)
	ContentID   string // MIME Content-ID without angle brackets
	Inline      bool   // referenced from the HTML body (cid:)
	Data        []byte
}

// Skipped notes an attachment that was not kept (over the size cap).
type Skipped struct {
	Filename string
	Size     int64 // decoded size when known, else the encoded size read
	Reason   string
}

// Message is the parsed, decoded, bounded message.
type Message struct {
	Subject       string // one line; NoSubject when absent
	FromName      string
	FromEmail     string // "" unless a valid address
	To            []string
	DeliveredTo   []string
	MessageID     string   // without angle brackets
	InReplyTo     []string // message ids without angle brackets
	References    []string
	AutoSubmitted string // lower-cased
	Precedence    string // lower-cased
	SpamScore     float64
	Text          string // plain text (text/plain preferred, else derived from HTML)
	HTML          string // raw HTML as received (display only after sanitising)
	Attachments   []Attachment
	Skipped       []Skipped
	Truncated     bool // the part-count or depth cap stopped the walk
}

// IsAuto reports whether the message is itself automated or bulk mail, which
// must never be answered automatically (RFC 3834 mail-loop protection).
func (m Message) IsAuto() bool {
	if m.AutoSubmitted != "" && m.AutoSubmitted != "no" {
		return true
	}
	switch m.Precedence {
	case "bulk", "list", "junk", "auto_reply":
		return true
	}
	return false
}

// ThreadIDs is the threading chain: In-Reply-To first, then References from
// the most recent ancestor backwards, de-duplicated and bounded.
func (m Message) ThreadIDs() []string {
	out := make([]string, 0, len(m.InReplyTo)+len(m.References))
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] && len(out) < maxThreadIDs {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range m.InReplyTo {
		add(id)
	}
	for i := len(m.References) - 1; i >= 0; i-- {
		add(m.References[i])
	}
	return out
}

// IsDaemonSender reports whether from looks like a non-human or no-reply
// address (another auto-reply loop guard); an empty address counts as one.
func IsDaemonSender(from string) bool {
	f := strings.ToLower(strings.TrimSpace(from))
	if f == "" {
		return true
	}
	local := f
	if i := strings.Index(f, "@"); i >= 0 {
		local = f[:i]
	}
	for _, bad := range []string{"mailer-daemon", "postmaster", "no-reply", "noreply", "do-not-reply", "donotreply", "autoreply", "auto-reply", "bounce"} {
		if strings.Contains(local, bad) {
			return true
		}
	}
	return false
}

// Parse parses raw within lim. Only a message above the size cap is an error:
// anything unparseable degrades to its raw text so no delivery is lost.
func Parse(raw []byte, lim Limits) (Message, error) {
	lim = lim.orDefaults()
	if int64(len(raw)) > lim.MaxBodyBytes {
		return Message{}, ErrTooLarge
	}
	p := &parser{lim: lim}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		p.out.Subject = NoSubject
		p.out.Text = p.cleanBody(string(raw))
		return p.out, nil
	}
	p.headers(msg.Header)

	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	body, _ := io.ReadAll(msg.Body)
	mediaType, params, perr := mime.ParseMediaType(ct)
	switch {
	case perr != nil:
		p.text = string(body)
	case strings.HasPrefix(mediaType, "multipart/"):
		p.parts = 1
		p.walk(multipart.NewReader(bytes.NewReader(body), params["boundary"]), mediaType, 0)
	case strings.HasPrefix(mediaType, "text/"):
		p.collect(mediaType, msg.Header.Get("Content-Transfer-Encoding"), params["charset"], mediaType, body)
	default: // a single non-text part: the whole message is one attachment
		name := params["name"]
		if _, dp, err := mime.ParseMediaType(msg.Header.Get("Content-Disposition")); err == nil && dp["filename"] != "" {
			name = dp["filename"]
		}
		p.attach(name, mediaType, "", false, msg.Header.Get("Content-Transfer-Encoding"), body)
	}

	p.out.HTML = p.cleanBody(p.html)
	if t := p.cleanBody(p.text); t != "" {
		p.out.Text = t
	} else {
		p.out.Text = p.cleanBody(HTMLToText(p.out.HTML))
	}
	return p.out, nil
}

type parser struct {
	lim   Limits
	out   Message
	text  string
	html  string
	parts int
}

func (p *parser) headers(h mail.Header) {
	dec := wordDecoder()
	subject, err := dec.DecodeHeader(h.Get("Subject"))
	if err != nil {
		subject = h.Get("Subject")
	}
	p.out.Subject = truncRunes(oneLine(subject), maxSubjectRunes)
	if p.out.Subject == "" {
		p.out.Subject = NoSubject
	}
	ap := &mail.AddressParser{WordDecoder: dec}
	if a, err := ap.Parse(h.Get("From")); err == nil {
		p.out.FromEmail = cleanAddress(a.Address)
		p.out.FromName = truncRunes(oneLine(a.Name), maxNameRunes)
	} else {
		p.out.FromEmail = cleanAddress(bracketAddress(h.Get("From")))
	}
	p.out.To = addressList(ap, h["To"])
	p.out.DeliveredTo = addressList(ap, h["Delivered-To"])
	if ids := messageIDList(h.Get("Message-Id")); len(ids) > 0 {
		p.out.MessageID = ids[0]
	}
	p.out.InReplyTo = messageIDList(h.Get("In-Reply-To"))
	p.out.References = messageIDList(h.Get("References"))
	p.out.AutoSubmitted = strings.ToLower(oneLine(h.Get("Auto-Submitted")))
	p.out.Precedence = strings.ToLower(oneLine(h.Get("Precedence")))
	p.out.SpamScore = parseSpamScore(h.Get("X-Spam-Score"))
}

var reBracketAddr = regexp.MustCompile(`<([^<>\s@]+@[^<>\s@]+)>`)

func bracketAddress(v string) string {
	if m := reBracketAddr.FindStringSubmatch(v); len(m) == 2 {
		return m[1]
	}
	return ""
}

// cleanAddress keeps a single, header-safe, bounded addr-spec ("" otherwise).
func cleanAddress(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxAddressBytes || strings.ContainsAny(v, " <>\"") || hasControl(v) {
		return ""
	}
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v {
		return ""
	}
	return v
}

func addressList(ap *mail.AddressParser, values []string) []string {
	var out []string
	for _, v := range values {
		list, err := ap.ParseList(v)
		if err != nil {
			if a := cleanAddress(bracketAddress(v)); a != "" {
				out = append(out, a)
			} else if a := cleanAddress(v); a != "" {
				out = append(out, a)
			}
			continue
		}
		for _, a := range list {
			if c := cleanAddress(a.Address); c != "" && len(out) < maxThreadIDs {
				out = append(out, c)
			}
		}
	}
	return out
}

// ParseMessageIDs extracts the bare, header-safe message ids of a header
// value such as a relay's X-Iris-Message-Id.
func ParseMessageIDs(v string) []string { return messageIDList(v) }

// NormalizeRecipient reduces a recipient header value ("Name <a@b>" or a
// bare address) to a lower-cased bare address ("" when unusable).
func NormalizeRecipient(v string) string {
	if a := bracketAddress(v); a != "" {
		v = a
	}
	return strings.ToLower(cleanAddress(strings.TrimSpace(v)))
}

// messageIDList splits a header holding zero or more <id> tokens into bare,
// header-safe ids.
func messageIDList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || r == ',' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		id := strings.Trim(f, "<>")
		if id == "" || len(id) > maxIDBytes || strings.ContainsAny(id, "<>") || hasControl(id) || !utf8.ValidString(id) {
			continue
		}
		if len(out) == maxThreadIDs {
			break
		}
		out = append(out, id)
	}
	return out
}

// parseSpamScore reads X-Spam-Score (0 when absent or not a finite number).
func parseSpamScore(v string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// walk visits a multipart body: text parts become the bodies, file parts
// attachments; nesting and the number of parts are bounded.
func (p *parser) walk(mr *multipart.Reader, parentType string, depth int) {
	for {
		if p.out.Truncated {
			return
		}
		part, err := mr.NextPart()
		if err != nil {
			return
		}
		p.parts++
		if p.parts > p.lim.MaxParts {
			p.out.Truncated = true
			return
		}
		ct := part.Header.Get("Content-Type")
		if ct == "" {
			ct = "text/plain"
		}
		mediaType, params, perr := mime.ParseMediaType(ct)
		if perr != nil {
			continue
		}
		disp, dparams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		filename := dparams["filename"]
		if filename == "" {
			filename = params["name"]
		}
		filename = decodeHeaderWord(filename)
		contentID := contentIDOf(part.Header.Get("Content-ID"))
		isMultipart := strings.HasPrefix(mediaType, "multipart/")
		isText := strings.HasPrefix(mediaType, "text/")
		isAttachment := disp == "attachment" || filename != ""
		isInline := contentID != "" && disp != "attachment" && !isMultipart && !isText

		switch {
		case isMultipart && !isAttachment:
			if depth+1 > p.lim.MaxDepth {
				p.out.Truncated = true
				return
			}
			if b := params["boundary"]; b != "" {
				p.walk(multipart.NewReader(part, b), mediaType, depth+1)
			}
		case isAttachment || isInline:
			content, over := p.readEncoded(part)
			if over {
				p.skip(filename, int64(len(content)))
				continue
			}
			p.attach(filename, mediaType, contentID, isInline, part.Header.Get("Content-Transfer-Encoding"), content)
		case isText:
			content, _ := io.ReadAll(io.LimitReader(part, p.lim.MaxBodyBytes))
			p.collect(mediaType, part.Header.Get("Content-Transfer-Encoding"), params["charset"], parentType, content)
		}
	}
}

// readEncoded reads an attachment's encoded bytes, reporting whether they
// already exceed what could decode within the cap (QP can triple the size).
func (p *parser) readEncoded(r io.Reader) ([]byte, bool) {
	limit := 3*p.lim.MaxAttachmentBytes + 4096
	content, _ := io.ReadAll(io.LimitReader(r, limit+1))
	return content, int64(len(content)) > limit
}

func (p *parser) skip(filename string, size int64) {
	p.out.Skipped = append(p.out.Skipped, Skipped{Filename: SafeFilename(filename), Size: size, Reason: SkipTooLarge})
}

func (p *parser) attach(filename, mediaType, contentID string, inline bool, enc string, content []byte) {
	data := decodeTransfer(content, enc)
	if int64(len(data)) > p.lim.MaxAttachmentBytes {
		p.skip(filename, int64(len(data)))
		return
	}
	if mediaType == "" || !headerSafe(mediaType) || len(mediaType) > 127 {
		mediaType = "application/octet-stream"
	}
	p.out.Attachments = append(p.out.Attachments, Attachment{Filename: SafeFilename(filename), ContentType: mediaType,
		ContentID: contentID, Inline: inline, Data: data})
}

func (p *parser) collect(mediaType, enc, charset, parentType string, content []byte) {
	s := decodeCharset(decodeTransfer(content, enc), charset)
	switch {
	case mediaType == "text/plain":
		if p.text == "" || parentType == "multipart/alternative" {
			p.text = s
		}
	case mediaType == "text/html":
		p.html = s
	default:
		if p.text == "" && p.html == "" {
			p.text = s
		}
	}
}

func contentIDOf(v string) string {
	id := strings.TrimSpace(strings.Trim(strings.TrimSpace(v), "<>"))
	if id == "" || len(id) > maxIDBytes || hasControl(id) || strings.ContainsAny(id, "<> ") {
		return ""
	}
	return id
}

func decodeHeaderWord(s string) string {
	if s == "" {
		return s
	}
	if d, err := wordDecoder().DecodeHeader(strings.Trim(s, `" `)); err == nil {
		return d
	}
	return s
}

func decodeTransfer(content []byte, enc string) []byte {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		// Tolerate the whitespace/newlines mailers insert into base64.
		clean := bytes.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, content)
		out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		if n, err := base64.StdEncoding.Decode(out, clean); err == nil {
			return out[:n]
		}
		return content
	case "quoted-printable":
		if d, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(content))); err == nil {
			return d
		}
		return content
	default: // 7bit, 8bit, binary, ""
		return content
	}
}

func decodeCharset(content []byte, charset string) string {
	cs := strings.ToLower(strings.TrimSpace(charset))
	if cs == "" || cs == "utf-8" || cs == "utf8" || cs == "us-ascii" || cs == "ascii" {
		return string(content)
	}
	if enc := charsetDecoder(cs); enc != nil {
		if d, err := enc.NewDecoder().Bytes(content); err == nil {
			return string(d)
		}
	}
	return string(content)
}

func wordDecoder() *mime.WordDecoder {
	dec := new(mime.WordDecoder)
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if enc := charsetDecoder(charset); enc != nil {
			return enc.NewDecoder().Reader(input), nil
		}
		return input, nil // unknown charset: pass through
	}
	return dec
}

func charsetDecoder(label string) encoding.Encoding {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "koi8-r", "koi8r":
		return charmap.KOI8R
	case "koi8-u", "koi8u":
		return charmap.KOI8U
	case "windows-1251", "cp1251":
		return charmap.Windows1251
	case "windows-1252", "cp1252":
		return charmap.Windows1252
	case "windows-1250", "cp1250":
		return charmap.Windows1250
	case "iso-8859-1", "latin1":
		return charmap.ISO8859_1
	case "iso-8859-2":
		return charmap.ISO8859_2
	case "iso-8859-5":
		return charmap.ISO8859_5
	case "iso-8859-15":
		return charmap.ISO8859_15
	case "gbk", "gb2312", "gb18030":
		return simplifiedchinese.GBK
	case "big5":
		return traditionalchinese.Big5
	case "shift_jis", "shift-jis", "sjis":
		return japanese.ShiftJIS
	case "euc-jp":
		return japanese.EUCJP
	case "iso-2022-jp":
		return japanese.ISO2022JP
	case "euc-kr":
		return korean.EUCKR
	case "utf-16", "utf-16le":
		return textunicode.UTF16(textunicode.LittleEndian, textunicode.UseBOM)
	case "utf-16be":
		return textunicode.UTF16(textunicode.BigEndian, textunicode.UseBOM)
	default:
		return nil
	}
}

// ---- cleaning helpers

func hasControl(v string) bool {
	for _, r := range v {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func headerSafe(v string) bool { return utf8.ValidString(v) && !hasControl(v) }

// oneLine makes a decoded header value safe for storage and reuse in outbound
// headers: valid UTF-8, every control character (CR/LF included) and run of
// white space collapsed to one space.
func oneLine(v string) string {
	v = strings.ToValidUTF8(v, "�")
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, v)
	return strings.Join(strings.Fields(v), " ")
}

// cleanBody makes a decoded body storable: valid UTF-8, newlines normalised,
// NUL and other control characters (but \n and \t) removed, bounded.
func (p *parser) cleanBody(v string) string {
	if v == "" {
		return ""
	}
	if int64(len(v)) > 4*p.lim.MaxTextBytes { // bound the work before mapping
		v = truncBytes(v, int(4*p.lim.MaxTextBytes))
	}
	v = strings.ToValidUTF8(v, "�")
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.Map(func(r rune) rune {
		switch {
		case r == '\r':
			return '\n'
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, v)
	return strings.TrimSpace(truncBytes(strings.TrimSpace(v), int(p.lim.MaxTextBytes)))
}

// truncBytes cuts v to at most n bytes on a rune boundary.
func truncBytes(v string, n int) string {
	if len(v) <= n {
		return v
	}
	for n > 0 && !utf8.RuneStart(v[n]) {
		n--
	}
	return v[:n]
}

func truncRunes(v string, n int) string {
	if utf8.RuneCountInString(v) <= n {
		return v
	}
	r := []rune(v)
	return strings.TrimSpace(string(r[:n]))
}

// SafeFilename reduces an attachment name to a safe base name: no directory
// components (either separator), no control characters, no leading dots,
// bounded; "attachment" when nothing usable remains.
func SafeFilename(name string) string {
	name = strings.ToValidUTF8(name, "_")
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimLeft(name, ". "))
	if name == "" || name == "/" {
		return "attachment"
	}
	return truncRunes(name, maxFilename)
}

var (
	reStyleScript = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reBreaks      = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/li|/h[1-6])\s*/?>`)
	reTags        = regexp.MustCompile(`(?s)<[^>]+>`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText renders an HTML body as readable plain text (HTML-only mail):
// script/style dropped, block ends become newlines, tags stripped, entities
// unescaped.
func HTMLToText(s string) string {
	if s == "" {
		return ""
	}
	s = reStyleScript.ReplaceAllString(s, "")
	s = reBreaks.ReplaceAllString(s, "\n")
	s = reTags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
