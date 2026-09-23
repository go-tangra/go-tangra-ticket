package rules

// T051: ported from go-tangra-ticket internal/rules/engine_test.go and
// extended: every field/operator, ALL/ANY, the raw-expression override, literal
// quoting (a value cannot change the expression), refusal of invalid
// conditions/expressions at compile time, the program cache, and the cost limit
// and deadline stopping pathological expressions.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-freya/freya/services/ticket/internal/mailparse"
	"github.com/go-freya/freya/services/ticket/internal/store"
)

func mustEngine(t testing.TB, cfg ...Config) *Engine {
	t.Helper()
	c := Config{CostLimit: 100000, Timeout: time.Second}
	if len(cfg) > 0 {
		c = cfg[0]
	}
	e, err := NewEngine(c)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func rule(match string, conds ...store.Condition) store.Rule {
	return store.Rule{ID: "r1", Version: 1, Match: match, Conditions: conds, Actions: []store.Action{{Type: store.ActionDrop}}}
}

func cond(field, op, value string) store.Condition {
	return store.Condition{Field: field, Operator: op, Value: value}
}

// eval compiles r (uncached) and evaluates it against m.
func eval(t *testing.T, e *Engine, r store.Rule, m Email) bool {
	t.Helper()
	p, err := e.Compile(r)
	if err != nil {
		t.Fatalf("Compile(%+v): %v", r, err)
	}
	ok, err := e.Evaluate(context.Background(), p, m)
	if err != nil {
		t.Fatalf("Evaluate(%q): %v", p.Expr, err)
	}
	return ok
}

var baseMail = Email{
	Subject:    "URGENT: Server down in DC1",
	Body:       "The production server is not responding.",
	From:       "ops@example.com",
	FromName:   "Ops Team",
	Recipient:  "support@verax.net",
	FromDomain: "example.com",
}

func TestBuildAndEval(t *testing.T) {
	e := mustEngine(t)
	tests := []struct {
		name  string
		match string
		conds []store.Condition
		want  bool
	}{
		{"subject contains (case-insensitive)", "all", []store.Condition{cond("subject", "contains", "SERVER")}, true},
		{"ALL requires every condition", "all", []store.Condition{cond("subject", "contains", "server"), cond("from", "equals", "nobody@example.com")}, false},
		{"ANY needs only one", "any", []store.Condition{cond("subject", "contains", "nope"), cond("from", "ends_with", "@example.com")}, true},
		{"ANY none match", "any", []store.Condition{cond("subject", "contains", "nope"), cond("from", "ends_with", "@nope.example")}, false},
		{"not_contains", "all", []store.Condition{cond("body", "not_contains", "invoice")}, true},
		{"not_contains present", "all", []store.Condition{cond("body", "not_contains", "PRODUCTION")}, false},
		{"equals", "all", []store.Condition{cond("from", "equals", "OPS@example.com")}, true},
		{"not_equals", "all", []store.Condition{cond("from", "not_equals", "ops@example.com")}, false},
		{"starts_with", "all", []store.Condition{cond("subject", "starts_with", "urgent")}, true},
		{"ends_with", "all", []store.Condition{cond("recipient", "ends_with", "VERAX.NET")}, true},
		{"fromName contains", "all", []store.Condition{cond("fromName", "contains", "ops t")}, true},
		{"fromDomain equals", "all", []store.Condition{cond("fromDomain", "equals", "Example.COM")}, true},
		{"regex matches", "all", []store.Condition{cond("subject", "matches", "(?i)dc[0-9]+")}, true},
		{"regex is case-sensitive without (?i)", "all", []store.Condition{cond("subject", "matches", "dc[0-9]+")}, false},
		{"hasAttachments true", "all", []store.Condition{cond("hasAttachments", "equals", "true")}, false},
		{"hasAttachments false", "all", []store.Condition{cond("hasAttachments", "equals", "false")}, true},
		{"hasAttachments not_equals true", "all", []store.Condition{cond("hasAttachments", "not_equals", "true")}, true},
		{"subject AND hasAttachments", "all", []store.Condition{cond("subject", "contains", "server"), cond("hasAttachments", "eq", "FALSE")}, true},
		{"default match is ALL", "", []store.Condition{cond("subject", "contains", "server"), cond("body", "contains", "nope")}, false},
		{"match is case-insensitive", "ANY", []store.Condition{cond("subject", "contains", "nope"), cond("body", "contains", "server")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := eval(t, e, rule(tc.match, tc.conds...), baseMail); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSpamScoreAndDomain(t *testing.T) {
	e := mustEngine(t)
	mail := Email{From: "x@spammer.io", FromDomain: "spammer.io", SpamScore: 7.5}
	cases := []struct {
		name  string
		conds []store.Condition
		want  bool
	}{
		{"gt", []store.Condition{cond("spamScore", "gt", "5")}, true},
		{"gte exact", []store.Condition{cond("spamScore", "gte", "7.5")}, true},
		{"lt", []store.Condition{cond("spamScore", "lt", "5")}, false},
		{"lte", []store.Condition{cond("spamScore", "lte", "7.5")}, true},
		{"eq", []store.Condition{cond("spamScore", "eq", "7.5")}, true},
		{"neq", []store.Condition{cond("spamScore", "neq", "7.5")}, false},
		{"float value", []store.Condition{cond("spamScore", "gt", "7.49")}, true},
		{"negative value", []store.Condition{cond("spamScore", "gt", "-1")}, true},
		{"exponent value", []store.Condition{cond("spamScore", "lt", "1e2")}, true},
		{"domain ends_with", []store.Condition{cond("fromDomain", "ends_with", ".io")}, true},
		{"domain + spam", []store.Condition{cond("fromDomain", "equals", "spammer.io"), cond("spamScore", "gte", "7")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eval(t, e, rule("all", tc.conds...), mail); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	// No spam score header: 0 never exceeds 5.
	if eval(t, e, rule("all", cond("spamScore", "gt", "5")), Email{}) {
		t.Fatal("zero spam score matched > 5")
	}
}

func TestHasAttachmentsDefaultsToTrue(t *testing.T) {
	e := mustEngine(t)
	r := rule("all", cond("hasAttachments", "equals", ""))
	if !eval(t, e, r, Email{HasAttachments: true}) || eval(t, e, r, Email{}) {
		t.Fatal("empty hasAttachments value must mean true")
	}
}

func TestRawExpressionOverridesConditions(t *testing.T) {
	e := mustEngine(t)
	r := rule("all", cond("subject", "contains", "never-there"))
	r.Expression = `subject.lowerAscii().contains("server") && spamScore < 1.0 && !hasAttachments`
	p, err := e.Compile(r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Expr != r.Expression {
		t.Fatalf("effective expression %q, want the raw expression", p.Expr)
	}
	if !eval(t, e, r, baseMail) {
		t.Fatal("raw expression should match")
	}
	// The strings extension is available to power users.
	r.Expression = `fromName.split(" ").size() == 2 && body.indexOf("server") > 0`
	if !eval(t, e, r, baseMail) {
		t.Fatal("strings extension expression should match")
	}
}

func TestLiteralQuotingCannotInject(t *testing.T) {
	e := mustEngine(t)
	values := []string{
		`say "hi"`,
		`") || true || ("`,
		`" || subject != "`,
		`\" || true || \"`,
		`\`,
		`a\nb`,
		"line\nbreak\r\ttab",
		"nul\x00byte",
		"del\x7fchar",
		"emoji 🎫 and ümlaut",
		`'single' and """triple"""`,
		" sep ",
		"}{)(][",
	}
	for _, v := range values {
		for _, op := range []string{"equals", "contains", "starts_with", "ends_with"} {
			r := rule("all", cond("subject", op, v))
			// The exact value matches...
			if !eval(t, e, r, Email{Subject: v}) {
				t.Fatalf("%s %q: value did not match itself", op, v)
			}
			// ...and nothing else does (an injected "|| true" would).
			if eval(t, e, r, Email{Subject: "unrelated"}) {
				t.Fatalf("%s %q: value changed the expression", op, v)
			}
		}
		neg := rule("all", cond("body", "not_equals", v))
		if eval(t, e, neg, Email{Body: v}) {
			t.Fatalf("not_equals %q: injection", v)
		}
	}
	// The value appears only inside one string literal.
	expr, err := BuildExpression("all", []store.Condition{cond("subject", "contains", `") || true || ("`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(expr, "||") != 2 || !strings.Contains(expr, `\") || true || (\""`) {
		t.Fatalf("value escaped its literal: %s", expr)
	}
	// A regex value is a literal too.
	if eval(t, e, rule("all", cond("subject", "matches", `"\) && true && \("`)), Email{Subject: "nope"}) {
		t.Fatal("regex value changed the expression")
	}
}

func TestInvalidConditionsRefused(t *testing.T) {
	e := mustEngine(t)
	bad := []store.Rule{
		rule("all", cond("bogus", "contains", "x")),
		rule("all", cond("subject", "bogus", "x")),
		rule("all", cond("subject", "gt", "x")),
		rule("all", cond("subject", "contains", "")),
		rule("all", cond("subject", "matches", "(unclosed")),
		rule("all", cond("subject", "contains", "bad\xffutf8")),
		rule("all", cond("subject", "contains", strings.Repeat("x", MaxValueLen+1))),
		rule("all", cond("spamScore", "gt", "abc")),
		rule("all", cond("spamScore", "gt", "NaN")),
		rule("all", cond("spamScore", "gt", "Inf")),
		rule("all", cond("spamScore", "contains", "5")),
		rule("all", cond("hasAttachments", "equals", "maybe")),
		rule("all", cond("hasAttachments", "gt", "true")),
		rule("sometimes", cond("subject", "contains", "x")),
		rule("all"), // neither conditions nor an expression
	}
	for _, r := range bad {
		_, err := e.Compile(r)
		var ie *InvalidError
		if !errors.As(err, &ie) || ie.Msg == "" {
			t.Fatalf("Compile(%+v) = %v, want an InvalidError", r, err)
		}
	}
	many := rule("all")
	for i := 0; i <= MaxConditions; i++ {
		many.Conditions = append(many.Conditions, cond("subject", "contains", "x"))
	}
	if _, err := e.Compile(many); err == nil {
		t.Fatal("too many conditions accepted")
	}
}

func TestInvalidExpressionRefused(t *testing.T) {
	e := mustEngine(t)
	bad := []string{
		`subject.contains(`,            // syntax
		`subject + 1`,                  // type error
		`subject`,                      // not boolean
		`spamScore`,                    // not boolean
		`unknownVar == "x"`,            // undeclared variable
		`subject.matches("(unclosed")`, // invalid regex literal
		`size(subject) > 1 && nope()`,  // unknown function
		strings.Repeat("(", 300) + "true" + strings.Repeat(")", 300), // recursion limit
		strings.Repeat("x", MaxExpressionLen+1),
	}
	for _, x := range bad {
		r := rule("all")
		r.Expression = x
		_, err := e.Compile(r)
		var ie *InvalidError
		if !errors.As(err, &ie) || ie.Field != "expression" || ie.Msg == "" {
			t.Fatalf("Compile(%.40q) = %v, want an expression InvalidError", x, err)
		}
	}
	if err := (&InvalidError{Field: "expression", Msg: "boom"}).Error(); !strings.Contains(err, "expression") || !strings.Contains(err, "boom") {
		t.Fatalf("error text %q", err)
	}
}

func TestProgramCacheKeyedByIDAndVersion(t *testing.T) {
	e := mustEngine(t)
	r := rule("all", cond("subject", "contains", "server"))
	p1, err := e.Program(r)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := e.Program(r)
	if err != nil || p1 != p2 {
		t.Fatalf("same id+version must reuse the program (%p %p %v)", p1, p2, err)
	}
	if e.CacheLen() != 1 {
		t.Fatalf("cache len %d", e.CacheLen())
	}
	r.Version = 2
	r.Conditions = []store.Condition{cond("subject", "contains", "nothing-like-it")}
	p3, err := e.Program(r)
	if err != nil || p3 == p1 {
		t.Fatal("a new version must recompile")
	}
	ok, err := e.Evaluate(context.Background(), p3, baseMail)
	if err != nil || ok {
		t.Fatalf("stale program used: %v %v", ok, err)
	}
	if e.CacheLen() != 1 {
		t.Fatalf("old versions must be replaced, cache len %d", e.CacheLen())
	}
	// A rule without id (dry run) is compiled but not cached.
	r.ID = ""
	if _, err := e.Program(r); err != nil || e.CacheLen() != 1 {
		t.Fatalf("id-less rule cached: %v %d", err, e.CacheLen())
	}
	e.Forget("r1")
	if e.CacheLen() != 0 {
		t.Fatal("Forget did not evict")
	}
	// Invalid rules are never cached.
	if _, err := e.Program(store.Rule{ID: "bad", Version: 1, Expression: "nope("}); err == nil || e.CacheLen() != 0 {
		t.Fatal("invalid rule cached")
	}
}

func TestCostLimitStopsPathologicalExpressions(t *testing.T) {
	e := mustEngine(t, Config{CostLimit: 1000, Timeout: time.Second})
	r := rule("all")
	// Quadratic comprehension over the body's characters.
	r.Expression = `body.split("").all(a, body.split("").all(b, a != "\u0000"))`
	p, err := e.Compile(r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Evaluate(context.Background(), p, Email{Body: strings.Repeat("a", 2000)})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cost") {
		t.Fatalf("want a cost-limit error, got %v", err)
	}
	// A regex over a large field is bounded by the same budget (RE2 is linear).
	r.Expression = `body.matches("(a*)*b")`
	if p, err = e.Compile(r); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Evaluate(context.Background(), p, Email{Body: strings.Repeat("a", MaxBodyEval)}); err == nil {
		t.Fatal("regex over a large body within a tiny budget should exceed the cost limit")
	}
}

func TestDeadlineStopsEvaluation(t *testing.T) {
	e := mustEngine(t, Config{CostLimit: 10_000_000, Timeout: time.Second})
	r := rule("all")
	r.Expression = `[1,2,3,4,5,6,7,8,9,10].all(a, [1,2,3,4,5,6,7,8,9,10].all(b, [1,2,3,4,5,6,7,8,9,10].all(c, a+b+c > 0)))`
	p, err := e.Compile(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Evaluate(ctx, p, Email{}); err == nil {
		t.Fatal("an expired deadline must interrupt evaluation")
	}
	if ok, err := e.Evaluate(context.Background(), p, Email{}); err != nil || !ok {
		t.Fatalf("live context: %v %v", ok, err)
	}
	if _, err := e.Evaluate(context.Background(), nil, Email{}); err == nil {
		t.Fatal("nil program accepted")
	}
}

func TestEngineConfigBounds(t *testing.T) {
	if _, err := NewEngine(Config{}); err != nil {
		t.Fatalf("zero config must use defaults: %v", err)
	}
	e := mustEngine(t, Config{})
	if e.Timeout() != DefaultTimeout {
		t.Fatalf("timeout %v", e.Timeout())
	}
}

func TestEmailOfMessage(t *testing.T) {
	msg := &mailparse.Message{Subject: "Hi", Text: strings.Repeat("é", MaxBodyEval), FromEmail: "Ann@Mail.Example.COM", FromName: "Ann",
		SpamScore: 3.2, Skipped: []mailparse.Skipped{{Filename: "big.bin", Size: 1}}}
	m := EmailOf(msg, "support@acme.example")
	if m.Subject != "Hi" || m.From != "Ann@Mail.Example.COM" || m.FromName != "Ann" || m.Recipient != "support@acme.example" ||
		m.FromDomain != "mail.example.com" || !m.HasAttachments || m.SpamScore != 3.2 {
		t.Fatalf("email = %+v", m)
	}
	if len(m.Body) > MaxBodyEval || !strings.HasPrefix(msg.Text, m.Body) || !strings.HasSuffix(m.Body, "é") {
		t.Fatalf("body not truncated on a rune boundary: %d bytes", len(m.Body))
	}
	if EmailOf(&mailparse.Message{FromEmail: "no-at-sign"}, "").FromDomain != "" {
		t.Fatal("domain of an address without @")
	}
	if !EmailOf(&mailparse.Message{Attachments: []mailparse.Attachment{{Filename: "a"}}}, "").HasAttachments {
		t.Fatal("attachments not detected")
	}
	if got := EmailOf(nil, "x"); got.Recipient != "x" {
		t.Fatalf("nil message: %+v", got)
	}
	if d := DomainOf("a@B.example"); d != "b.example" {
		t.Fatalf("DomainOf = %q", d)
	}
}
