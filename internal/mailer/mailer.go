// Package mailer sends the module's outbound mail — public replies and
// acknowledgements — through the configured relay (research D5), adapted from
// the notification module's email channel: net/smtp, implicit TLS or STARTTLS,
// plaintext only when explicitly allowed (development), PLAIN authentication
// only over TLS, every header checked for CR/LF and control characters, and
// the RFC 5322 threading headers (Message-ID, In-Reply-To, References) plus
// the RFC 3834 auto-reply headers for acknowledgements.
//
// The relay password is read from the secret store at send time (rotation)
// and never appears in an error, log or String(); relay error texts are never
// echoed (they can carry credentials or addresses) — only the reply code.
package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// TLS modes (config smtp.tls).
const (
	TLSImplicit = "implicit"
	TLSStartTLS = "starttls"
	TLSNone     = "none"
)

// Errors (never carry secret material or relay texts).
var (
	ErrDisabled  = errors.New("mailer: outbound mail is not configured")
	ErrConfig    = errors.New("mailer: invalid relay configuration")
	ErrHeader    = errors.New("mailer: header contains a line break or control character")
	ErrAddress   = errors.New("mailer: invalid address")
	ErrPlaintext = errors.New("mailer: plaintext delivery is not allowed")
	ErrAuthTLS   = errors.New("mailer: credentials require TLS")
	ErrAuth      = errors.New("mailer: relay authentication failed")
	ErrRelay     = errors.New("mailer: relay refused the message")
)

// maxReferences bounds the References header.
const maxReferences = 100

// OutMail is one outbound plain-text message. Message ids are bare (no angle
// brackets).
type OutMail struct {
	From       string // the mailbox address
	FromName   string
	To         string // the requester address
	ToName     string
	Subject    string
	Text       string
	MessageID  string
	InReplyTo  string
	References []string
	// AutoReply marks an acknowledgement (Auto-Submitted: auto-replied,
	// Precedence: auto_reply) so other systems never answer it.
	AutoReply bool
}

// Sender delivers outbound mail.
type Sender interface {
	Send(ctx context.Context, m OutMail) error
	// Enabled reports whether a relay is configured.
	Enabled() bool
}

// PasswordFunc yields the relay password at send time.
type PasswordFunc func(ctx context.Context) (string, error)

// Config configures the SMTP sender (config smtp.*). An empty Host disables it.
type Config struct {
	Host           string
	Port           int
	TLS            string
	AllowPlaintext bool
	Username       string
	Password       PasswordFunc
	Timeout        time.Duration
	// Dialer overrides the network dial (tests).
	Dialer func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLSConfig overrides the client TLS configuration (tests: a test CA).
	TLSConfig *tls.Config
	Now       func() time.Time
}

// SMTP is the relay sender.
type SMTP struct{ cfg Config }

// New builds the sender.
func New(cfg Config) *SMTP { return &SMTP{cfg: cfg} }

// Enabled implements Sender.
func (s *SMTP) Enabled() bool { return s != nil && s.cfg.Host != "" }

// String never prints credentials.
func (s *SMTP) String() string {
	return "mailer.SMTP{" + net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)) + " " + s.cfg.TLS + "}"
}

// HeaderSafe reports whether v may appear in a header (no CR/LF, no control
// characters, valid UTF-8).
func HeaderSafe(v string) bool {
	if !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if r == '\r' || r == '\n' || r == 0 || (r < 0x20 && r != '\t') || r == 0x7f {
			return false
		}
	}
	return true
}

func bareAddress(v string) bool {
	if v == "" || len(v) > 320 {
		return false
	}
	a, err := mail.ParseAddress(v)
	return err == nil && a.Address == v && a.Name == ""
}

func validID(id string) bool {
	return id != "" && len(id) <= 998 && HeaderSafe(id) && !strings.ContainsAny(id, "<> \t,")
}

// Validate checks every header value of m (SR-004): ErrHeader for line breaks,
// control characters or malformed message ids, ErrAddress for anything but a
// single bare From/To address.
func Validate(m OutMail) error {
	for _, v := range []string{m.From, m.FromName, m.To, m.ToName, m.Subject} {
		if !HeaderSafe(v) {
			return ErrHeader
		}
	}
	if !validID(m.MessageID) || (m.InReplyTo != "" && !validID(m.InReplyTo)) || len(m.References) > maxReferences {
		return ErrHeader
	}
	for _, r := range m.References {
		if !validID(r) {
			return ErrHeader
		}
	}
	if !bareAddress(m.From) || !bareAddress(m.To) {
		return ErrAddress
	}
	return nil
}

// Build assembles the RFC 5322 message (CRLF line endings, quoted-printable
// UTF-8 text body).
func Build(m OutMail, now time.Time) ([]byte, error) {
	if err := Validate(m); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", (&mail.Address{Name: m.FromName, Address: m.From}).String())
	fmt.Fprintf(&b, "To: %s\r\n", (&mail.Address{Name: m.ToName, Address: m.To}).String())
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeSubject(m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", m.MessageID)
	if m.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\n", m.InReplyTo)
	}
	if len(m.References) > 0 {
		b.WriteString("References:")
		for i, r := range m.References {
			if i > 0 {
				b.WriteString("\r\n") // fold: one id per line keeps every line short
			}
			fmt.Fprintf(&b, " <%s>", r)
		}
		b.WriteString("\r\n")
	}
	if m.AutoReply {
		b.WriteString("Auto-Submitted: auto-replied\r\nPrecedence: auto_reply\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\nX-Mailer: freya-ticket\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(strings.ToValidUTF8(m.Text, "�")))
	_ = w.Close()
	b.WriteString("\r\n")
	return b.Bytes(), nil
}

// encodeSubject encodes non-ASCII subjects and folds long ones into a chain of
// B-encoded words so no header line exceeds 78 characters (RFC 5322 §2.1.1).
func encodeSubject(subject string) string {
	if enc := mime.QEncoding.Encode("utf-8", subject); len(enc) <= 66 {
		return enc
	}
	var words []string
	rest := subject
	for len(rest) > 0 {
		n := 0
		for n < len(rest) {
			_, size := utf8.DecodeRuneInString(rest[n:])
			if n+size > 45 {
				break
			}
			n += size
		}
		words = append(words, "=?utf-8?b?"+base64.StdEncoding.EncodeToString([]byte(rest[:n]))+"?=")
		rest = rest[n:]
	}
	return strings.Join(words, "\r\n ")
}

// relayErr keeps only the reply code of a relay error (its text may echo
// credentials or addresses).
func relayErr(step string, err error, sentinel error) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		return fmt.Errorf("%w: %s (%d)", sentinel, step, te.Code)
	}
	return fmt.Errorf("mailer: %s: %w", step, err)
}

// Send implements Sender.
func (s *SMTP) Send(ctx context.Context, m OutMail) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if err := Validate(m); err != nil {
		return err
	}
	cfg := s.cfg
	switch cfg.TLS {
	case TLSImplicit, TLSStartTLS:
	case TLSNone:
		if !cfg.AllowPlaintext {
			return ErrPlaintext
		}
		if cfg.Username != "" {
			return ErrAuthTLS
		}
	default:
		return ErrConfig
	}
	password := ""
	if cfg.Username != "" {
		if cfg.Password == nil {
			return fmt.Errorf("%w: password unavailable", ErrAuth)
		}
		p, err := cfg.Password(ctx)
		if err != nil || p == "" {
			return fmt.Errorf("%w: password unavailable", ErrAuth)
		}
		password = p
	}
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	body, err := Build(m, now())
	if err != nil {
		return err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dial := cfg.Dialer
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(dctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return fmt.Errorf("mailer: dial: %w", err)
	}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.TLSConfig != nil {
		tlsCfg = cfg.TLSConfig.Clone()
		tlsCfg.ServerName = cfg.Host
		if tlsCfg.MinVersion < tls.VersionTLS12 {
			tlsCfg.MinVersion = tls.VersionTLS12
		}
	}
	if cfg.TLS == TLSImplicit {
		conn = tls.Client(conn, tlsCfg)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return relayErr("greeting", err, ErrRelay)
	}
	defer c.Close()
	if err := c.Hello(helloName(m.From)); err != nil {
		return relayErr("hello", err, ErrRelay)
	}
	if cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%w: relay offers no STARTTLS", ErrPlaintext)
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("mailer: starttls: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, password, cfg.Host)); err != nil {
			return relayErr("auth", err, ErrAuth)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return relayErr("sender", err, ErrRelay)
	}
	if err := c.Rcpt(m.To); err != nil {
		return relayErr("recipient", err, ErrRelay)
	}
	w, err := c.Data()
	if err != nil {
		return relayErr("data", err, ErrRelay)
	}
	if _, err := w.Write(body); err != nil {
		return relayErr("data", err, ErrRelay)
	}
	if err := w.Close(); err != nil {
		return relayErr("data", err, ErrRelay)
	}
	_ = c.Quit()
	return nil
}

func helloName(from string) string {
	if i := strings.LastIndex(from, "@"); i >= 0 && i < len(from)-1 {
		return from[i+1:]
	}
	return "localhost"
}

// Fake is an in-memory Sender for tests; it validates like the real one.
type Fake struct {
	mu       sync.Mutex
	Sent     []OutMail
	Err      error
	Disabled bool
}

// Enabled implements Sender.
func (f *Fake) Enabled() bool { return !f.Disabled }

// Send implements Sender.
func (f *Fake) Send(_ context.Context, m OutMail) error {
	if f.Disabled {
		return ErrDisabled
	}
	if err := Validate(m); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Sent = append(f.Sent, m)
	return nil
}

// Messages returns a copy of the sent messages.
func (f *Fake) Messages() []OutMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OutMail(nil), f.Sent...)
}

var (
	_ Sender = (*SMTP)(nil)
	_ Sender = (*Fake)(nil)
)
