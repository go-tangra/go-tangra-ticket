package webhook

import (
	"bytes"
	"encoding/base64"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// maxAttachmentBytes caps a single decoded attachment.
const maxAttachmentBytes = 25 << 20 // 25 MiB

// mailAttachment is a decoded file part of an email.
type mailAttachment struct {
	filename    string
	contentType string
	contentID   string
	inline      bool
	data        []byte
}

// parsedMail holds the extracted, decoded fields of an inbound email.
type parsedMail struct {
	subject     string
	fromName    string
	fromEmail   string
	messageID   string
	text        string // plain text (text/plain preferred, else HTML->text)
	html        string // raw HTML body, if the email had one
	attachments []mailAttachment
}

// parseMail parses a raw RFC822 message into ticket fields, decoding MIME
// multipart bodies, Content-Transfer-Encoding (base64/quoted-printable) and
// charsets. Attachments are skipped. Falls back to the raw body if the input
// is not a parseable message.
func parseMail(raw []byte) parsedMail {
	var out parsedMail
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		out.text = strings.TrimSpace(string(raw))
		return out
	}

	dec := wordDecoder()
	out.subject, _ = dec.DecodeHeader(msg.Header.Get("Subject"))
	out.messageID = strings.Trim(msg.Header.Get("Message-Id"), "<>")
	if addr, e := mail.ParseAddress(msg.Header.Get("From")); e == nil {
		out.fromEmail = addr.Address
		out.fromName, _ = dec.DecodeHeader(addr.Name)
	} else {
		out.fromEmail = strings.TrimSpace(msg.Header.Get("From"))
	}

	var text, htmlBody string
	collect := func(mediaType, enc, charset, parentType string, content []byte) {
		decoded := decodeTransfer(content, enc)
		s := decodeCharset(decoded, charset)
		switch {
		case mediaType == "text/plain":
			if text == "" || parentType == "multipart/alternative" {
				text = s
			}
		case mediaType == "text/html":
			htmlBody = s
		default:
			if text == "" && htmlBody == "" {
				text = s
			}
		}
	}

	var atts []mailAttachment
	addAtt := func(a mailAttachment) { atts = append(atts, a) }

	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		b, _ := io.ReadAll(io.LimitReader(msg.Body, maxBodyBytes))
		text = string(b)
	} else if mediaType, params, e := mime.ParseMediaType(ct); e == nil && strings.HasPrefix(mediaType, "multipart/") {
		walkParts(multipart.NewReader(msg.Body, params["boundary"]), mediaType, 0, collect, addAtt)
	} else if e == nil {
		b, _ := io.ReadAll(io.LimitReader(msg.Body, maxBodyBytes))
		collect(mediaType, msg.Header.Get("Content-Transfer-Encoding"), params["charset"], mediaType, b)
	} else {
		b, _ := io.ReadAll(io.LimitReader(msg.Body, maxBodyBytes))
		text = string(b)
	}

	out.attachments = atts
	out.html = htmlBody
	if strings.TrimSpace(text) != "" {
		out.text = strings.TrimSpace(text)
	} else {
		out.text = strings.TrimSpace(htmlToText(htmlBody))
	}
	return out
}

// walkParts recursively walks a multipart reader, collecting text parts as
// body and file parts as attachments (decoded). Logic ported from
// go-imap-admin: disposition + filename (MIME-decoded) determine attachments;
// Content-ID + inline disposition mark inline content. Depth is bounded.
func walkParts(mr *multipart.Reader, parentType string, depth int, collect func(mediaType, enc, charset, parentType string, content []byte), att func(mailAttachment)) {
	if depth > 12 {
		return
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			return
		}
		mediaType, params, perr := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if perr != nil {
			_, _ = io.Copy(io.Discard, part)
			continue
		}

		disp, dparams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		filename := dparams["filename"]
		if filename == "" {
			filename = params["name"]
		}
		filename = decodeHeaderWord(filename)
		contentID := strings.Trim(part.Header.Get("Content-ID"), "<>")
		isAttachment := disp == "attachment" || filename != ""
		isInline := disp == "inline" && contentID != ""

		if isAttachment || isInline {
			content, _ := io.ReadAll(io.LimitReader(part, maxAttachmentBytes))
			data := decodeTransfer(content, part.Header.Get("Content-Transfer-Encoding"))
			att(mailAttachment{
				filename:    filename,
				contentType: mediaType,
				contentID:   contentID,
				inline:      isInline && !isAttachment,
				data:        data,
			})
			continue
		}

		if strings.HasPrefix(mediaType, "multipart/") {
			if b := params["boundary"]; b != "" {
				walkParts(multipart.NewReader(part, b), mediaType, depth+1, collect, att)
			}
			continue
		}

		if strings.HasPrefix(mediaType, "text/") {
			content, _ := io.ReadAll(io.LimitReader(part, maxBodyBytes))
			collect(mediaType, part.Header.Get("Content-Transfer-Encoding"), params["charset"], parentType, content)
		} else {
			_, _ = io.Copy(io.Discard, part)
		}
	}
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
		// Tolerate whitespace/newlines that mailers insert into base64.
		clean := strings.NewReplacer("\r", "", "\n", "", " ", "").Replace(string(content))
		if d, err := base64.StdEncoding.DecodeString(clean); err == nil {
			return d
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
		if enc := charsetDecoder(strings.ToLower(charset)); enc != nil {
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
		return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)
	case "utf-16be":
		return unicode.UTF16(unicode.BigEndian, unicode.UseBOM)
	default:
		return nil
	}
}

var (
	reStyleScript = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reBreaks      = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/li|/h[1-6])\s*/?>`)
	reTags        = regexp.MustCompile(`(?s)<[^>]+>`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
)

// htmlToText produces a readable plain-text rendering of an HTML body for
// HTML-only emails: drop script/style, turn block-ends into newlines, strip
// remaining tags, and unescape entities.
func htmlToText(s string) string {
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
