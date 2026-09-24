package mailer

// T042: message assembly with threading headers, header-injection refusal,
// TLS modes, plaintext only when allowed, PLAIN auth only over TLS, and no
// credential in any error.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal relay (adapted from the notification module's test).
type fakeSMTP struct {
	ln       net.Listener
	cert     *tls.Certificate
	implicit bool
	noTLS    bool // never offer STARTTLS
	refuseAt string
	mu       sync.Mutex
	last     string
	rcpt     string
	authed   bool
	tlsUsed  bool
}

func newFake(t *testing.T, cert *tls.Certificate, implicit bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, cert: cert, implicit: implicit}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	if f.implicit {
		c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*f.cert}, MinVersion: tls.VersionTLS12})
		f.mu.Lock()
		f.tlsUsed = true
		f.mu.Unlock()
	}
	r := bufio.NewReader(c)
	w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			_, isTLS := c.(*tls.Conn)
			if f.cert != nil && !f.implicit && !isTLS && !f.noTLS {
				w("250-fake")
				w("250-STARTTLS")
				w("250 AUTH PLAIN")
			} else {
				w("250-fake")
				w("250 AUTH PLAIN")
			}
		case cmd == "STARTTLS":
			w("220 ready")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*f.cert}, MinVersion: tls.VersionTLS12})
			if err := tc.Handshake(); err != nil {
				return
			}
			c = tc
			r = bufio.NewReader(c)
			w = func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
			f.mu.Lock()
			f.tlsUsed = true
			f.mu.Unlock()
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			if f.refuseAt == "auth" {
				w("535 authentication failed for " + strings.TrimSpace(line[10:]))
				continue
			}
			f.mu.Lock()
			f.authed = true
			f.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			if f.refuseAt == "rcpt" {
				w("550 no such user " + strings.TrimSpace(line[8:]))
				continue
			}
			f.mu.Lock()
			f.rcpt = strings.TrimSpace(line[8:])
			f.mu.Unlock()
			w("250 ok")
		case cmd == "DATA":
			w("354 go")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			f.mu.Lock()
			f.last = sb.String()
			f.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("500 unknown")
		}
	}
}

func (f *fakeSMTP) message() string { f.mu.Lock(); defer f.mu.Unlock(); return f.last }

func testCert(t *testing.T) (*tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

const secretPW = "TICKET-SMTP-PW-marker"

func pw(context.Context) (string, error) { return secretPW, nil }

func reply() OutMail {
	return OutMail{From: "support@acme.example", FromName: "Acme Support", To: "jane@customer.example", ToName: "Jane Customer",
		Subject: "Re: Printer on floor 3 is jammed", Text: "We replaced the roller.\n\n--\nTicket reference: [#t-1]\n",
		MessageID: "ticket.t-1.abc@acme.example", InReplyTo: "plain-0001@mail.customer.example",
		References: []string{"plain-0001@mail.customer.example", "reply-0001@mail.customer.example"}}
}

func TestBuildThreadingHeaders(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	raw, err := Build(reply(), now)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	from, _ := mail.ParseAddress(m.Header.Get("From"))
	to, _ := mail.ParseAddress(m.Header.Get("To"))
	subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if from.Address != "support@acme.example" || from.Name != "Acme Support" || to.Address != "jane@customer.example" || to.Name != "Jane Customer" {
		t.Fatalf("from/to = %v %v", from, to)
	}
	if subj != "Re: Printer on floor 3 is jammed" || strings.Count(subj, "Re:") != 1 {
		t.Fatalf("subject = %q", subj)
	}
	if m.Header.Get("Message-Id") != "<ticket.t-1.abc@acme.example>" || m.Header.Get("In-Reply-To") != "<plain-0001@mail.customer.example>" ||
		m.Header.Get("References") != "<plain-0001@mail.customer.example> <reply-0001@mail.customer.example>" {
		t.Fatalf("threading = %v", m.Header)
	}
	if m.Header.Get("Auto-Submitted") != "" || m.Header.Get("Precedence") != "" {
		t.Fatal("a human reply must not be marked automatic")
	}
	if d, _ := m.Header.Date(); !d.Equal(now) {
		t.Fatalf("date = %v", d)
	}
	raw2, _ := io.ReadAll(quotedprintable.NewReader(m.Body))
	body := strings.ReplaceAll(string(raw2), "\r\n", "\n")
	if strings.TrimRight(body, "\n") != strings.TrimRight(reply().Text, "\n") {
		t.Fatalf("body = %q", body)
	}
	if strings.Count(body, "Ticket reference:") != 1 {
		t.Fatal("reference line not exactly once")
	}

	ack := reply()
	ack.AutoReply = true
	ack.Subject = "Re: Grüße — ein sehr langer Betreff, der über die Zeilenlänge hinausgeht und gefaltet werden muss, damit nichts bricht"
	raw, err = Build(ack, now)
	if err != nil {
		t.Fatal(err)
	}
	m, _ = mail.ReadMessage(bytes.NewReader(raw))
	if m.Header.Get("Auto-Submitted") != "auto-replied" || m.Header.Get("Precedence") != "auto_reply" {
		t.Fatal("acknowledgement headers missing")
	}
	if got, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject")); got != ack.Subject {
		t.Fatalf("long subject = %q", got)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatal("header line over 998")
		}
	}
}

func TestHeaderInjectionRefused(t *testing.T) {
	mut := []func(*OutMail){
		func(o *OutMail) { o.To = "jane@customer.example\r\nBcc: victim@evil.example" },
		func(o *OutMail) { o.Subject = "Help\r\nBcc: victim@evil.example" },
		func(o *OutMail) { o.Subject = "Help\nX: y" },
		func(o *OutMail) { o.FromName = "Acme\rBcc: x@y" },
		func(o *OutMail) { o.ToName = "Eve\r\nBcc: attacker@evil.example" },
		func(o *OutMail) { o.MessageID = "a@b>\r\nBcc: x@y" },
		func(o *OutMail) { o.MessageID = "a b@c" },
		func(o *OutMail) { o.InReplyTo = "x@y\nBcc: z" },
		func(o *OutMail) { o.References = []string{"ok@x", "bad\r\n@y"} },
		func(o *OutMail) { o.Subject = "nul\x00byte" },
	}
	for i, m := range mut {
		o := reply()
		m(&o)
		if _, err := Build(o, time.Now()); !errors.Is(err, ErrHeader) {
			t.Errorf("case %d: %v", i, err)
		}
		if err := Validate(o); !errors.Is(err, ErrHeader) {
			t.Errorf("case %d validate: %v", i, err)
		}
	}
	for i, m := range []func(*OutMail){
		func(o *OutMail) { o.To = "" },
		func(o *OutMail) { o.To = "Jane <jane@customer.example>" },
		func(o *OutMail) { o.To = "a@b.example, c@d.example" },
		func(o *OutMail) { o.From = "not-an-address" },
		func(o *OutMail) { o.MessageID = "" },
	} {
		o := reply()
		m(&o)
		if err := Validate(o); !errors.Is(err, ErrAddress) && !errors.Is(err, ErrHeader) {
			t.Errorf("address case %d: %v", i, err)
		}
	}
}

func TestSendSTARTTLSWithAuth(t *testing.T) {
	cert, pool := testCert(t)
	f := newFake(t, cert, false)
	s := New(Config{Host: "127.0.0.1", Port: f.port(), TLS: TLSStartTLS, Username: "relay-user", Password: pw, TLSConfig: &tls.Config{RootCAs: pool}})
	if !s.Enabled() {
		t.Fatal("configured mailer disabled")
	}
	if err := s.Send(context.Background(), reply()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.tlsUsed || !f.authed || f.rcpt != "<jane@customer.example>" || !strings.Contains(f.last, "In-Reply-To: <plain-0001@mail.customer.example>") {
		t.Fatalf("tls=%v auth=%v rcpt=%q\n%s", f.tlsUsed, f.authed, f.rcpt, f.last)
	}
}

func TestSendImplicitTLS(t *testing.T) {
	cert, pool := testCert(t)
	f := newFake(t, cert, true)
	s := New(Config{Host: "127.0.0.1", Port: f.port(), TLS: TLSImplicit, Username: "u", Password: pw, TLSConfig: &tls.Config{RootCAs: pool}})
	if err := s.Send(context.Background(), reply()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.message(), "Message-ID: <ticket.t-1.abc@acme.example>") {
		t.Fatal(f.message())
	}
}

func TestPlaintextOnlyWhenAllowed(t *testing.T) {
	f := newFake(t, nil, false)
	ctx := context.Background()
	if err := New(Config{Host: "127.0.0.1", Port: f.port(), TLS: TLSNone}).Send(ctx, reply()); !errors.Is(err, ErrPlaintext) {
		t.Fatalf("plaintext without opt-in: %v", err)
	}
	if err := New(Config{Host: "127.0.0.1", Port: f.port(), TLS: TLSNone, AllowPlaintext: true}).Send(ctx, reply()); err != nil {
		t.Fatalf("dev plaintext: %v", err)
	}
	// credentials never travel in plaintext
	err := New(Config{Host: "127.0.0.1", Port: f.port(), TLS: TLSNone, AllowPlaintext: true, Username: "u", Password: pw}).Send(ctx, reply())
	if !errors.Is(err, ErrAuthTLS) {
		t.Fatalf("auth over plaintext: %v", err)
	}
	// a relay that does not offer STARTTLS is refused in starttls mode
	cert, pool := testCert(t)
	g := newFake(t, cert, false)
	g.noTLS = true
	if err := New(Config{Host: "127.0.0.1", Port: g.port(), TLS: TLSStartTLS, TLSConfig: &tls.Config{RootCAs: pool}}).Send(ctx, reply()); !errors.Is(err, ErrPlaintext) {
		t.Fatalf("no STARTTLS: %v", err)
	}
	if err := New(Config{Host: "127.0.0.1", Port: f.port(), TLS: "bogus"}).Send(ctx, reply()); !errors.Is(err, ErrConfig) {
		t.Fatalf("bad mode: %v", err)
	}
}

func TestPasswordNeverInErrors(t *testing.T) {
	cert, pool := testCert(t)
	f := newFake(t, cert, false)
	f.refuseAt = "auth"
	s := New(Config{Host: "127.0.0.1", Port: f.port(), TLS: TLSStartTLS, Username: "relay-user", Password: pw, TLSConfig: &tls.Config{RootCAs: pool}})
	err := s.Send(context.Background(), reply())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("auth refusal: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("\x00relay-user\x00" + secretPW))
	for _, leak := range []string{secretPW, b64, "relay-user"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, err)
		}
	}
	// a refused recipient does not echo the address either
	f.refuseAt = "rcpt"
	err = s.Send(context.Background(), reply())
	if err == nil || strings.Contains(err.Error(), "jane@") || !strings.Contains(err.Error(), "550") {
		t.Fatalf("rcpt refusal: %v", err)
	}
	// password source failure
	s = New(Config{Host: "127.0.0.1", Port: f.port(), TLS: TLSStartTLS, Username: "u",
		Password: func(context.Context) (string, error) { return "", errors.New("warden down " + secretPW) }, TLSConfig: &tls.Config{RootCAs: pool}})
	if err := s.Send(context.Background(), reply()); !errors.Is(err, ErrAuth) || strings.Contains(err.Error(), secretPW) {
		t.Fatalf("password source failure: %v", err)
	}
	if strings.Contains(s.String(), secretPW) {
		t.Fatal("String leaks")
	}
}

func TestDisabledAndDialFailure(t *testing.T) {
	s := New(Config{})
	if s.Enabled() {
		t.Fatal("empty host enabled")
	}
	if err := s.Send(context.Background(), reply()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled send: %v", err)
	}
	var nilS *SMTP
	if nilS.Enabled() {
		t.Fatal("nil enabled")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if err := New(Config{Host: "127.0.0.1", Port: port, TLS: TLSNone, AllowPlaintext: true, Timeout: time.Second}).Send(context.Background(), reply()); err == nil {
		t.Fatal("dial to closed port succeeded")
	}
	bad := reply()
	bad.To = "x\r\n"
	if err := New(Config{Host: "127.0.0.1", Port: port, TLS: TLSNone, AllowPlaintext: true}).Send(context.Background(), bad); !errors.Is(err, ErrHeader) {
		t.Fatalf("validation before dial: %v", err)
	}
}

func TestFake(t *testing.T) {
	f := &Fake{}
	if !f.Enabled() || f.Send(context.Background(), reply()) != nil || len(f.Messages()) != 1 {
		t.Fatal("fake send")
	}
	f.Err = errors.New("down")
	if f.Send(context.Background(), reply()) == nil || len(f.Messages()) != 1 {
		t.Fatal("fake failure")
	}
	f.Err = nil
	bad := reply()
	bad.Subject = "a\r\nb"
	if !errors.Is(f.Send(context.Background(), bad), ErrHeader) {
		t.Fatal("fake validates")
	}
	f.Disabled = true
	if f.Enabled() || !errors.Is(f.Send(context.Background(), reply()), ErrDisabled) {
		t.Fatal("fake disabled")
	}
}
