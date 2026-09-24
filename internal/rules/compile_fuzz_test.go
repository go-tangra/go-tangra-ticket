package rules

// T052: arbitrary condition fields, operators and values never panic and
// always compile to a boolean program or a validation error; a compiled
// equality condition matches exactly its own value (no literal injection).

import (
	"context"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

func FuzzCompileConditions(f *testing.F) {
	seeds := [][4]string{
		{"subject", "contains", "invoice", "all"},
		{"from", "equals", `") || true || ("`, "any"},
		{"body", "matches", `(?i)dc[0-9]+`, "all"},
		{"spamScore", "gt", "5", "all"},
		{"spamScore", "lte", "1e308", "any"},
		{"hasAttachments", "equals", "false", "all"},
		{"fromDomain", "ends_with", "\\", "all"},
		{"recipient", "starts_with", "\"\n\x00", "all"},
		{"bogus", "contains", "x", "all"},
		{"subject", "matches", "(", "all"},
		{"fromName", "not_contains", "\xff\xfe", "any"},
		{"subject", "equals", " }{", ""},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1], s[2], s[3])
	}
	e, err := NewEngine(Config{CostLimit: 100000, Timeout: time.Second})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, field, op, value, match string) {
		r := store.Rule{Match: match, Conditions: []store.Condition{{Field: field, Operator: op, Value: value}, {Field: "subject", Operator: "contains", Value: "x"}},
			Actions: []store.Action{{Type: store.ActionDrop}}}
		p, err := e.Compile(r)
		if err != nil {
			var ie *InvalidError
			if !errors.As(err, &ie) || ie.Msg == "" {
				t.Fatalf("Compile(%q %q %q %q): non-validation error %v", field, op, value, match, err)
			}
			return
		}
		if _, err := e.Evaluate(context.Background(), p, Email{Subject: value, Body: value, From: value}); err != nil {
			t.Fatalf("Evaluate(%q): %v", p.Expr, err)
		}

		// Literal round trip: `subject equals v` matches v and nothing else.
		if !utf8.ValidString(value) || value == "" || len(value) > MaxValueLen {
			return
		}
		eq := store.Rule{Match: "all", Conditions: []store.Condition{{Field: "subject", Operator: "equals", Value: value}}, Actions: r.Actions}
		pe, err := e.Compile(eq)
		if err != nil {
			t.Fatalf("equals %q refused: %v", value, err)
		}
		if ok, err := e.Evaluate(context.Background(), pe, Email{Subject: value}); err != nil || !ok {
			t.Fatalf("equals %q did not match itself (%q): %v %v", value, pe.Expr, ok, err)
		}
		other := value + "\x01"
		if ok, err := e.Evaluate(context.Background(), pe, Email{Subject: other}); err != nil || ok {
			t.Fatalf("equals %q matched a different subject (%q): %v %v", value, pe.Expr, ok, err)
		}
	})
}

func FuzzCompileExpression(f *testing.F) {
	for _, s := range []string{`subject.contains("x")`, `spamScore > 5.0`, `subject +`, `[1].all(x, x > 0)`, `hasAttachments`, `"a".matches("(")`, `body`} {
		f.Add(s)
	}
	e, err := NewEngine(Config{CostLimit: 10000, Timeout: 50 * time.Millisecond})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		p, err := e.Compile(store.Rule{Expression: expr, Actions: []store.Action{{Type: store.ActionDrop}}})
		if err != nil {
			var ie *InvalidError
			if !errors.As(err, &ie) || ie.Msg == "" {
				t.Fatalf("Compile(%q): non-validation error %v", expr, err)
			}
			return
		}
		// Evaluation may fail (cost, runtime errors) but must never panic.
		_, _ = e.Evaluate(context.Background(), p, Email{Subject: "s", Body: "b"})
	})
}
