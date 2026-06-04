// Package mailer sends outbound ticket reply emails over an SMTP relay and
// stamps them with the RFC822 threading headers (Message-ID, In-Reply-To,
// References) so requester replies thread back onto the same ticket.
package mailer

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	gomail "github.com/wneessen/go-mail"
)

// Config is the SMTP relay configuration, sourced from environment variables.
type Config struct {
	Host        string
	Port        int
	Username    string
	Password    string
	DefaultFrom string
	FromName    string
	TLS         string // starttls | ssl | none
	Insecure    bool
	Domain      string // domain used for Message-ID and subject token host
}

// Mailer sends ticket reply emails. It is a no-op (Enabled()==false) when no
// SMTP host is configured.
type Mailer struct {
	cfg Config
	log *log.Helper
}

// OutMail is a single outbound message.
type OutMail struct {
	From       string
	FromName   string
	To         string
	ToName     string
	Subject    string
	Text       string
	MessageID  string   // bare id (no angle brackets)
	InReplyTo  string   // bare id (no angle brackets)
	References []string // bare ids (no angle brackets)
}

func NewMailer(ctx *bootstrap.Context) *Mailer {
	port := 587
	if v := os.Getenv("TICKET_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}
	from := os.Getenv("TICKET_SMTP_FROM")
	cfg := Config{
		Host:        os.Getenv("TICKET_SMTP_HOST"),
		Port:        port,
		Username:    os.Getenv("TICKET_SMTP_USERNAME"),
		Password:    os.Getenv("TICKET_SMTP_PASSWORD"),
		DefaultFrom: from,
		FromName:    getEnvDefault("TICKET_SMTP_FROM_NAME", "Support"),
		TLS:         strings.ToLower(getEnvDefault("TICKET_SMTP_TLS", "starttls")),
		Insecure:    os.Getenv("TICKET_SMTP_INSECURE") == "1",
		Domain:      os.Getenv("TICKET_MAIL_DOMAIN"),
	}
	if cfg.Domain == "" {
		cfg.Domain = domainOf(from)
	}
	if cfg.Domain == "" {
		cfg.Domain = "tickets.local"
	}
	m := &Mailer{cfg: cfg, log: ctx.NewLoggerHelper("ticket/mailer")}
	if !m.Enabled() {
		m.log.Warn("TICKET_SMTP_HOST not set; outbound ticket replies are disabled")
	} else {
		m.log.Infof("outbound mailer enabled (relay=%s:%d, from=%s)", cfg.Host, cfg.Port, cfg.DefaultFrom)
	}
	return m
}

func (m *Mailer) Enabled() bool { return m.cfg.Host != "" }

// Domain returns the configured mail domain (for Message-ID / subject token).
func (m *Mailer) Domain() string { return m.cfg.Domain }

// DefaultFrom returns the fallback From address.
func (m *Mailer) DefaultFrom() string { return m.cfg.DefaultFrom }

// GenMessageID returns a fresh, unique bare message-id tied to a ticket. The
// domain is taken from the From address (best for deliverability), falling back
// to the configured mail domain.
func (m *Mailer) GenMessageID(ticketID, from string) string {
	domain := domainOf(from)
	if domain == "" {
		domain = m.cfg.Domain
	}
	return fmt.Sprintf("ticket.%s.%s@%s", ticketID, uuid.NewString(), domain)
}

// Send delivers one message over the SMTP relay.
func (m *Mailer) Send(ctx context.Context, o OutMail) error {
	if !m.Enabled() {
		return fmt.Errorf("mailer not configured")
	}
	from := o.From
	if from == "" {
		from = m.cfg.DefaultFrom
	}
	if from == "" {
		return fmt.Errorf("no From address")
	}
	if o.To == "" {
		return fmt.Errorf("no To address")
	}

	msg := gomail.NewMsg()
	if err := msg.FromFormat(orDefault(o.FromName, m.cfg.FromName), from); err != nil {
		return fmt.Errorf("set From: %w", err)
	}
	if o.ToName != "" {
		if err := msg.AddToFormat(o.ToName, o.To); err != nil {
			return fmt.Errorf("set To: %w", err)
		}
	} else if err := msg.To(o.To); err != nil {
		return fmt.Errorf("set To: %w", err)
	}
	msg.Subject(o.Subject)
	if o.MessageID != "" {
		msg.SetMessageIDWithValue(o.MessageID)
	}
	if o.InReplyTo != "" {
		msg.SetGenHeader(gomail.Header("In-Reply-To"), "<"+o.InReplyTo+">")
	}
	if len(o.References) > 0 {
		refs := make([]string, 0, len(o.References))
		for _, r := range o.References {
			refs = append(refs, "<"+r+">")
		}
		msg.SetGenHeader(gomail.Header("References"), strings.Join(refs, " "))
	}
	msg.SetBodyString(gomail.TypeTextPlain, o.Text)

	c, err := m.newClient()
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	if err := c.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

func (m *Mailer) newClient() (*gomail.Client, error) {
	opts := []gomail.Option{gomail.WithPort(m.cfg.Port)}

	switch m.cfg.TLS {
	case "ssl", "tls":
		opts = append(opts, gomail.WithSSLPort(false))
	case "none":
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	default: // starttls
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	}

	if m.cfg.Username != "" {
		opts = append(opts,
			gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
			gomail.WithUsername(m.cfg.Username),
			gomail.WithPassword(m.cfg.Password),
		)
	}
	if m.cfg.Insecure {
		opts = append(opts, gomail.WithTLSConfig(&tls.Config{InsecureSkipVerify: true})) //nolint:gosec // opt-in for self-signed relays
	}
	return gomail.NewClient(m.cfg.Host, opts...)
}

func getEnvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return ""
}
