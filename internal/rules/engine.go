// Package rules is the inbound triage engine (US4, research D6, SR-005). A rule
// is a set of conditions (field/operator/value) combined with ALL (&&) or ANY
// (||), or a raw CEL expression that overrides them. Conditions compile to a
// CEL expression whose values are emitted as properly escaped string/double
// literals, so a value can never change the expression's structure.
//
// Only the email fields are declared (plus the standard library and the
// strings extension); every program is type-checked to bool when a rule is
// saved and evaluated under a cost limit and the caller's deadline. Compiled
// programs are cached per rule id and version.
package rules

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"

	"github.com/go-freya/freya/services/ticket/internal/mailparse"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

// Condition fields.
const (
	FieldSubject        = "subject"
	FieldBody           = "body"
	FieldFrom           = "from"
	FieldFromName       = "fromName"
	FieldRecipient      = "recipient"
	FieldFromDomain     = "fromDomain"
	FieldHasAttachments = "hasAttachments"
	FieldSpamScore      = "spamScore"
)

// Bounds.
const (
	MaxConditions    = 50
	MaxValueLen      = 1000
	MaxExpressionLen = 4096
	// MaxBodyEval bounds the body text rules see (keeps string operations
	// within the cost budget on large messages).
	MaxBodyEval = 64 << 10

	DefaultCostLimit uint64 = 100000
	DefaultTimeout          = 50 * time.Millisecond

	maxRecursion  = 100
	maxCacheItems = 10000
	maxErrorLen   = 500
)

// TextFields accept the text operators.
var TextFields = []string{FieldSubject, FieldBody, FieldFrom, FieldFromName, FieldRecipient, FieldFromDomain}

// TextOperators compare a text field with the value (case-insensitive, except
// matches which is an RE2 pattern against the raw field; use (?i) there).
var TextOperators = []string{"contains", "not_contains", "equals", "not_equals", "starts_with", "ends_with", "matches"}

// NumericOperators compare spamScore with a number.
var NumericOperators = []string{"gt", "gte", "lt", "lte", "eq", "neq"}

// BoolOperators compare hasAttachments with true/false (empty value: true).
var BoolOperators = []string{"equals", "not_equals", "eq", "neq"}

var numericOps = map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<=", "eq": "==", "neq": "!="}

// InvalidError is a rule refused at save/compile time (reason invalid_rule).
type InvalidError struct {
	Field string
	Msg   string
}

func (e *InvalidError) Error() string { return "rules: " + e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) *InvalidError {
	msg := fmt.Sprintf(format, args...)
	if len(msg) > maxErrorLen {
		msg = msg[:maxErrorLen]
		for !utf8.ValidString(msg) {
			msg = msg[:len(msg)-1]
		}
	}
	return &InvalidError{Field: field, Msg: msg}
}

// Email is the evaluation input.
type Email struct {
	Subject        string
	Body           string
	From           string
	FromName       string
	Recipient      string
	FromDomain     string
	HasAttachments bool
	SpamScore      float64
}

// DomainOf is the lower-cased domain of an address ("" without one).
func DomainOf(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// EmailOf builds the evaluation input of a parsed message routed to recipient.
// Attachments skipped for size still count as attachments.
func EmailOf(msg *mailparse.Message, recipient string) Email {
	if msg == nil {
		return Email{Recipient: recipient}
	}
	return Email{Subject: msg.Subject, Body: truncate(msg.Text, MaxBodyEval), From: msg.FromEmail, FromName: msg.FromName,
		Recipient: recipient, FromDomain: DomainOf(msg.FromEmail), HasAttachments: len(msg.Attachments) > 0 || len(msg.Skipped) > 0,
		SpamScore: msg.SpamScore}
}

func (m Email) vars() map[string]any {
	return map[string]any{
		FieldSubject: m.Subject, FieldBody: truncate(m.Body, MaxBodyEval), FieldFrom: m.From, FieldFromName: m.FromName,
		FieldRecipient: m.Recipient, FieldFromDomain: m.FromDomain, FieldHasAttachments: m.HasAttachments, FieldSpamScore: m.SpamScore,
	}
}

// Config bounds evaluation; zero values take the defaults.
type Config struct {
	CostLimit uint64
	Timeout   time.Duration // per-message deadline (applied by the Triage)
}

// Program is a compiled, type-checked rule.
type Program struct {
	Expr    string
	version int
	prg     cel.Program
}

// Engine holds the CEL environment and the program cache.
type Engine struct {
	env   *cel.Env
	cfg   Config
	mu    sync.Mutex
	cache map[string]*Program
}

// NewEngine builds the environment with only the email fields declared.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.CostLimit == 0 {
		cfg.CostLimit = DefaultCostLimit
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	env, err := cel.NewEnv(
		ext.Strings(),
		cel.ParserRecursionLimit(maxRecursion),
		cel.Variable(FieldSubject, cel.StringType),
		cel.Variable(FieldBody, cel.StringType),
		cel.Variable(FieldFrom, cel.StringType),
		cel.Variable(FieldFromName, cel.StringType),
		cel.Variable(FieldRecipient, cel.StringType),
		cel.Variable(FieldFromDomain, cel.StringType),
		cel.Variable(FieldHasAttachments, cel.BoolType),
		cel.Variable(FieldSpamScore, cel.DoubleType),
	)
	if err != nil {
		return nil, fmt.Errorf("rules: cel env: %w", err)
	}
	return &Engine{env: env, cfg: cfg, cache: map[string]*Program{}}, nil
}

// Timeout is the per-message evaluation deadline.
func (e *Engine) Timeout() time.Duration { return e.cfg.Timeout }

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// asciiLower lower-cases ASCII letters only (the CEL lowerAscii semantics).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// quote renders s as a CEL double-quoted string literal. Backslash and quote
// are escaped and every control or non-printable rune becomes a \u/\U escape,
// so the literal is always a single token whatever s contains. s must be
// valid UTF-8 (checked by the caller).
func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r < 0x80 && unicode.IsPrint(r):
			b.WriteRune(r)
		case r >= 0x80 && unicode.IsPrint(r) && !unicode.Is(unicode.Zl, r) && !unicode.Is(unicode.Zp, r):
			b.WriteRune(r)
		case r <= 0xFFFF:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// doubleLiteral renders v as a CEL double literal (always with a '.' or an
// exponent so it never type-checks as an int).
func doubleLiteral(v float64) string {
	lit := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(lit, ".eE") {
		lit += ".0"
	}
	return lit
}

// conditionExpr validates c and renders it.
func conditionExpr(i int, c store.Condition) (string, error) {
	field, op := strings.TrimSpace(c.Field), strings.ToLower(strings.TrimSpace(c.Operator))
	at := fmt.Sprintf("conditions[%d]", i)
	if !utf8.ValidString(c.Value) {
		return "", invalid(at+".value", "must be valid UTF-8")
	}
	if len(c.Value) > MaxValueLen {
		return "", invalid(at+".value", "longer than %d bytes", MaxValueLen)
	}
	switch field {
	case FieldHasAttachments:
		if op != "" && !has(BoolOperators, op) {
			return "", invalid(at+".operator", "%q is not an operator of hasAttachments (equals, not_equals)", c.Operator)
		}
		var want bool
		switch strings.ToLower(strings.TrimSpace(c.Value)) {
		case "", "true":
			want = true
		case "false":
		default:
			return "", invalid(at+".value", "hasAttachments takes true or false")
		}
		cmp := "=="
		if op == "not_equals" || op == "neq" {
			cmp = "!="
		}
		return FieldHasAttachments + " " + cmp + " " + strconv.FormatBool(want), nil
	case FieldSpamScore:
		sym, ok := numericOps[op]
		if !ok {
			return "", invalid(at+".operator", "%q is not a numeric operator (gt, gte, lt, lte, eq, neq)", c.Operator)
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(c.Value), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return "", invalid(at+".value", "spamScore takes a number")
		}
		return FieldSpamScore + " " + sym + " " + doubleLiteral(v), nil
	}
	if !has(TextFields, field) {
		return "", invalid(at+".field", "unknown field %q", c.Field)
	}
	if !has(TextOperators, op) {
		return "", invalid(at+".operator", "%q is not a text operator", c.Operator)
	}
	if c.Value == "" && op != "equals" && op != "not_equals" {
		return "", invalid(at+".value", "a value is required")
	}
	if op == "matches" {
		if _, err := regexp.Compile(c.Value); err != nil {
			return "", invalid(at+".value", "invalid pattern: %v", err)
		}
		return field + ".matches(" + quote(c.Value) + ")", nil
	}
	lf, lv := field+".lowerAscii()", quote(asciiLower(c.Value))
	switch op {
	case "contains":
		return lf + ".contains(" + lv + ")", nil
	case "not_contains":
		return "!" + lf + ".contains(" + lv + ")", nil
	case "equals":
		return lf + " == " + lv, nil
	case "not_equals":
		return lf + " != " + lv, nil
	case "starts_with":
		return lf + ".startsWith(" + lv + ")", nil
	default: // ends_with
		return lf + ".endsWith(" + lv + ")", nil
	}
}

// NormalizeMatch maps a match mode to all|any ("" → all).
func NormalizeMatch(match string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(match)) {
	case "", store.MatchAll:
		return store.MatchAll, nil
	case store.MatchAny:
		return store.MatchAny, nil
	}
	return "", invalid("match", "must be all or any")
}

// BuildExpression compiles structured conditions into a CEL boolean
// expression, validating each condition.
func BuildExpression(match string, conds []store.Condition) (string, error) {
	m, err := NormalizeMatch(match)
	if err != nil {
		return "", err
	}
	if len(conds) == 0 {
		return "", invalid("conditions", "add at least one condition or an expression")
	}
	if len(conds) > MaxConditions {
		return "", invalid("conditions", "at most %d conditions", MaxConditions)
	}
	parts := make([]string, 0, len(conds))
	for i, c := range conds {
		e, err := conditionExpr(i, c)
		if err != nil {
			return "", err
		}
		parts = append(parts, "("+e+")")
	}
	op := " && "
	if m == store.MatchAny {
		op = " || "
	}
	return strings.Join(parts, op), nil
}

// Effective returns the rule's CEL expression: the raw expression when set,
// else the one built from its conditions.
func Effective(r store.Rule) (string, error) {
	if x := strings.TrimSpace(r.Expression); x != "" {
		if len(r.Expression) > MaxExpressionLen {
			return "", invalid("expression", "longer than %d bytes", MaxExpressionLen)
		}
		if !utf8.ValidString(x) {
			return "", invalid("expression", "must be valid UTF-8")
		}
		return x, nil
	}
	return BuildExpression(r.Match, r.Conditions)
}

// Compile validates the rule, type-checks its effective expression to bool and
// builds the cost-limited program (not cached). Refusals are *InvalidError.
func (e *Engine) Compile(r store.Rule) (*Program, error) {
	expr, err := Effective(r)
	if err != nil {
		return nil, err
	}
	field := "expression"
	if strings.TrimSpace(r.Expression) == "" {
		field = "conditions"
	}
	ast, iss := e.env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, invalid(field, "%v", iss.Err())
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, invalid(field, "must evaluate to true or false, not %s", ast.OutputType())
	}
	prg, err := e.env.Program(ast, cel.CostLimit(e.cfg.CostLimit), cel.InterruptCheckFrequency(1),
		cel.EvalOptions(cel.OptOptimize))
	if err != nil {
		return nil, invalid(field, "%v", err)
	}
	return &Program{Expr: expr, version: r.Version, prg: prg}, nil
}

// Program returns the rule's compiled program from the cache (keyed by rule
// id and version; a new version replaces the old entry). Rules without an id
// (dry runs) are compiled but not cached; invalid rules are never cached.
func (e *Engine) Program(r store.Rule) (*Program, error) {
	if r.ID == "" {
		return e.Compile(r)
	}
	e.mu.Lock()
	p, ok := e.cache[r.ID]
	e.mu.Unlock()
	if ok && p.version == r.Version {
		return p, nil
	}
	p, err := e.Compile(r)
	if err != nil {
		e.Forget(r.ID)
		return nil, err
	}
	e.mu.Lock()
	if len(e.cache) >= maxCacheItems {
		e.cache = map[string]*Program{}
	}
	e.cache[r.ID] = p
	e.mu.Unlock()
	return p, nil
}

// Forget evicts a rule's program (deleted rules).
func (e *Engine) Forget(id string) {
	e.mu.Lock()
	delete(e.cache, id)
	e.mu.Unlock()
}

// CacheLen is the number of cached programs (tests, metrics).
func (e *Engine) CacheLen() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.cache)
}

// Evaluate runs p against m under the engine's cost limit and ctx's deadline.
func (e *Engine) Evaluate(ctx context.Context, p *Program, m Email) (bool, error) {
	if p == nil || p.prg == nil {
		return false, errors.New("rules: no program")
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("rules: %w", err)
	}
	out, _, err := p.prg.ContextEval(ctx, m.vars())
	if err != nil {
		return false, fmt.Errorf("rules: evaluate: %w", err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, errors.New("rules: expression did not yield a boolean")
	}
	return b, nil
}
