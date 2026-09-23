// Package audit records every ticket-module operation in a closed vocabulary
// (research D11, SR-006): mutations, every inbound-mail ingestion outcome and
// every outbound email. A detail guard keeps message bodies, descriptions,
// requester/author PII, tokens and passwords out of the log at any depth.
// Events are buffered and written to the audit hypertable by a background
// goroutine so recording never blocks the caller.
package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/store"
)

// EventType is the closed audit vocabulary.
type EventType string

// Event types.
const (
	TicketCreate EventType = "ticket.create"
	TicketUpdate EventType = "ticket.update"
	TicketDelete EventType = "ticket.delete"
	TicketAssign EventType = "ticket.assign"
	TicketStatus EventType = "ticket.status"
	TicketTags   EventType = "ticket.tags"

	CommentCreate EventType = "comment.create"
	CommentDelete EventType = "comment.delete"

	ReplySent   EventType = "reply.sent"
	ReplyFailed EventType = "reply.failed"
	AckSent     EventType = "ack.sent"
	AckSkipped  EventType = "ack.skipped"

	InboundCreated   EventType = "inbound.created"
	InboundThreaded  EventType = "inbound.threaded"
	InboundDuplicate EventType = "inbound.duplicate"
	InboundDropped   EventType = "inbound.dropped"
	InboundRefused   EventType = "inbound.refused"

	RuleCreate EventType = "rule.create"
	RuleUpdate EventType = "rule.update"
	RuleDelete EventType = "rule.delete"
	RuleError  EventType = "rule.error"

	TagCreate EventType = "tag.create"
	TagUpdate EventType = "tag.update"
	TagDelete EventType = "tag.delete"

	MailboxCreate EventType = "mailbox.create"
	MailboxUpdate EventType = "mailbox.update"
	MailboxDelete EventType = "mailbox.delete"

	BackupExport EventType = "backup.export"
	BackupImport EventType = "backup.import"

	AccessRefused EventType = "access.refused"
)

// Subject kinds (closed set).
const (
	SubjectTicket     = "ticket"
	SubjectComment    = "comment"
	SubjectAttachment = "attachment"
	SubjectTag        = "tag"
	SubjectRule       = "rule"
	SubjectMailbox    = "mailbox"
	SubjectMessage    = "message" // an inbound or outbound email
	SubjectBackup     = "backup"
	SubjectSystem     = "system"
)

// Outcomes (closed set).
const (
	OutcomeOK      = "ok"
	OutcomeRefused = "refused"
	OutcomeError   = "error"
)

// Actor kinds (closed set; mirrors authz).
const (
	ActorAgent   = "agent"
	ActorInbound = "inbound"
	ActorService = "service"
	ActorSystem  = "system"
	ActorRule    = "rule"
)

// NilTenant is recorded for refusals that could not be routed to a tenant
// (inbound edge: bad token, unknown mailbox).
const NilTenant = "00000000-0000-0000-0000-000000000000"

var known = map[EventType]struct{}{}

// Vocabulary lists every event type.
var Vocabulary = []EventType{
	TicketCreate, TicketUpdate, TicketDelete, TicketAssign, TicketStatus, TicketTags,
	CommentCreate, CommentDelete,
	ReplySent, ReplyFailed, AckSent, AckSkipped,
	InboundCreated, InboundThreaded, InboundDuplicate, InboundDropped, InboundRefused,
	RuleCreate, RuleUpdate, RuleDelete, RuleError,
	TagCreate, TagUpdate, TagDelete,
	MailboxCreate, MailboxUpdate, MailboxDelete,
	BackupExport, BackupImport,
	AccessRefused,
}

func init() {
	for _, t := range Vocabulary {
		known[t] = struct{}{}
	}
}

// Known reports whether t is in the vocabulary.
func Known(t string) bool { _, ok := known[EventType(t)]; return ok }

// Event is one record before persistence. At is filled by Record.
type Event struct {
	TenantID    string
	EventType   EventType
	ActorKind   string
	ActorID     string
	SubjectKind string
	SubjectID   string
	Outcome     string // ok | refused | error
	Reason      string
	Details     map[string]any
}

// Store persists audit rows. repo.Store satisfies it, as does any test double.
type Store interface {
	AppendAudit(ctx context.Context, row store.AuditRow) error
}

// Validate checks the closed vocabulary and required fields. An unknown event
// type is a programming error and is surfaced to the caller.
func Validate(e Event) error {
	if _, ok := known[e.EventType]; !ok {
		return fmt.Errorf("audit: unknown event type %q", e.EventType)
	}
	if e.TenantID == "" {
		return errors.New("audit: tenant_id is required")
	}
	switch e.ActorKind {
	case ActorAgent, ActorInbound, ActorService, ActorSystem, ActorRule:
	default:
		return fmt.Errorf("audit: actor_kind %q", e.ActorKind)
	}
	switch e.SubjectKind {
	case SubjectTicket, SubjectComment, SubjectAttachment, SubjectTag, SubjectRule,
		SubjectMailbox, SubjectMessage, SubjectBackup, SubjectSystem:
	default:
		return fmt.Errorf("audit: subject_kind %q", e.SubjectKind)
	}
	switch e.Outcome {
	case OutcomeOK, OutcomeRefused, OutcomeError:
	default:
		return fmt.Errorf("audit: outcome %q", e.Outcome)
	}
	return nil
}

// forbidden detail-key substrings (case-insensitive): message bodies and
// descriptions, raw HTML, requester/author PII, e-mail addresses, tokens,
// passwords and other credentials never belong in a detail.
var forbidden = []string{"body", "description", "html", "requester", "author_email", "author_name", "email", "token", "password", "secret", "credential"}

func forbiddenKey(k string) bool {
	lk := strings.ToLower(k)
	for _, f := range forbidden {
		if strings.Contains(lk, f) {
			return true
		}
	}
	return false
}

func guardValue(v any) any {
	switch x := v.(type) {
	case string:
		if len(x) > 256 {
			return x[:256]
		}
		return x
	case map[string]any:
		return guardMap(x)
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = guardValue(vv)
		}
		return out
	default:
		return v
	}
}

// Redact returns a copy of m with every forbidden key dropped (at any depth)
// and string values truncated to 256 bytes. It is what Record applies.
func Redact(m map[string]any) map[string]any { return guardMap(m) }

func guardMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if forbiddenKey(k) {
			continue
		}
		out[k] = guardValue(v)
	}
	return out
}

// Writer buffers events and writes them one row at a time; Record never blocks.
type Writer struct {
	st      Store
	ch      chan store.AuditRow
	wg      sync.WaitGroup
	mu      sync.Mutex
	closed  bool
	dropped int64
	onError func(error)
	flushCh chan chan struct{}
	tick    time.Duration
}

// NewWriter starts the batch writer (queue 10k, flush every 500 ms).
func NewWriter(st Store, onError func(error)) *Writer {
	w := newWriter(st, onError, 10000)
	w.start()
	return w
}

func newWriter(st Store, onError func(error), queue int) *Writer {
	w := &Writer{st: st, ch: make(chan store.AuditRow, queue), onError: onError, flushCh: make(chan chan struct{}), tick: 500 * time.Millisecond}
	if w.onError == nil {
		w.onError = func(error) {}
	}
	return w
}

func (w *Writer) start() {
	w.wg.Add(1)
	go w.run()
}

// Record validates the event, fills At=now, guards the details and enqueues the
// row. An invalid event (unknown type, missing tenant, bad enum) returns an
// error and is not queued.
func (w *Writer) Record(_ context.Context, e Event) error {
	if err := Validate(e); err != nil {
		return err
	}
	row := store.AuditRow{
		ID:          store.NewID(),
		TenantID:    e.TenantID,
		At:          time.Now(),
		ActorKind:   e.ActorKind,
		ActorID:     e.ActorID,
		Action:      string(e.EventType),
		SubjectKind: e.SubjectKind,
		SubjectID:   e.SubjectID,
		Outcome:     e.Outcome,
		Reason:      truncate(e.Reason),
		Detail:      guardMap(e.Details),
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.dropped++
		return errors.New("audit: writer closed")
	}
	select {
	case w.ch <- row:
	default:
		w.dropped++
		w.onError(errors.New("audit: queue full, event dropped"))
	}
	return nil
}

// Flush writes everything queued so far and returns when it is stored or ctx is done.
func (w *Writer) Flush(ctx context.Context) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	done := make(chan struct{})
	select {
	case w.flushCh <- done:
	case <-ctx.Done():
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Dropped returns the number of dropped events.
func (w *Writer) Dropped() int64 { w.mu.Lock(); defer w.mu.Unlock(); return w.dropped }

func (w *Writer) writeRow(r store.AuditRow) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.st.AppendAudit(ctx, r); err != nil {
		w.onError(err)
	}
}

func (w *Writer) run() {
	defer w.wg.Done()
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case r, ok := <-w.ch:
			if !ok {
				return
			}
			w.writeRow(r)
		case <-t.C:
			// nothing buffered locally; rows are written as they arrive.
		case done := <-w.flushCh:
			for {
				select {
				case r := <-w.ch:
					w.writeRow(r)
					continue
				default:
				}
				break
			}
			close(done)
		}
	}
}

// Close drains and stops the writer.
func (w *Writer) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.ch)
	w.mu.Unlock()
	w.wg.Wait()
}

// Recorder is what domain services record through (the Writer, or a test double).
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// Emit records e on r when r is non-nil. Domain services are wired with an
// optional recorder so unit tests need no writer; validation errors are
// programming errors and are ignored here (Record surfaces them to callers that
// care).
func Emit(ctx context.Context, r Recorder, e Event) {
	if r == nil {
		return
	}
	_ = r.Record(ctx, e)
}

func truncate(s string) string {
	if len(s) > 256 {
		return s[:256]
	}
	return s
}

// ActorOf maps an authz actor kind to the audit vocabulary (identical sets).
func ActorOf(kind string) string {
	switch kind {
	case ActorInbound, ActorService, ActorSystem, ActorRule:
		return kind
	}
	return ActorAgent
}
