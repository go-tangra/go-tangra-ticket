package rules

import "testing"

func mustEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestBuildAndEval(t *testing.T) {
	e := mustEngine(t)

	mail := Email{
		Subject:   "URGENT: Server down in DC1",
		Body:      "The production server is not responding.",
		From:      "ops@example.com",
		FromName:  "Ops Team",
		Recipient: "support@verax.net",
	}

	tests := []struct {
		name  string
		match string
		conds []Condition
		want  bool
	}{
		{
			name:  "subject contains (case-insensitive)",
			match: "ALL",
			conds: []Condition{{Field: "subject", Operator: "contains", Value: "server"}},
			want:  true,
		},
		{
			name:  "ALL requires every condition",
			match: "ALL",
			conds: []Condition{
				{Field: "subject", Operator: "contains", Value: "server"},
				{Field: "from", Operator: "equals", Value: "nobody@example.com"},
			},
			want: false,
		},
		{
			name:  "ANY needs only one",
			match: "ANY",
			conds: []Condition{
				{Field: "subject", Operator: "contains", Value: "nope"},
				{Field: "from", Operator: "ends_with", Value: "@example.com"},
			},
			want: true,
		},
		{
			name:  "not_contains",
			match: "ALL",
			conds: []Condition{{Field: "body", Operator: "not_contains", Value: "invoice"}},
			want:  true,
		},
		{
			name:  "starts_with",
			match: "ALL",
			conds: []Condition{{Field: "subject", Operator: "starts_with", Value: "urgent"}},
			want:  true,
		},
		{
			name:  "regex matches",
			match: "ALL",
			conds: []Condition{{Field: "subject", Operator: "matches", Value: "(?i)dc[0-9]+"}},
			want:  true,
		},
		{
			name:  "hasAttachments true matches",
			match: "ALL",
			conds: []Condition{{Field: "hasAttachments", Operator: "equals", Value: "true"}},
			want:  false, // base mail has no attachments
		},
		{
			name:  "hasAttachments false matches when none",
			match: "ALL",
			conds: []Condition{{Field: "hasAttachments", Operator: "equals", Value: "false"}},
			want:  true,
		},
		{
			name:  "subject AND hasAttachments",
			match: "ALL",
			conds: []Condition{
				{Field: "subject", Operator: "contains", Value: "server"},
				{Field: "hasAttachments", Operator: "equals", Value: "false"},
			},
			want: true,
		},
		{
			name:  "unknown field is dropped -> no parts -> false",
			match: "ALL",
			conds: []Condition{{Field: "bogus", Operator: "contains", Value: "x"}},
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expr := BuildExpression(tc.match, tc.conds)
			if err := e.Validate(expr); err != nil {
				t.Fatalf("Validate(%q): %v", expr, err)
			}
			got, err := e.Eval(expr, mail)
			if err != nil {
				t.Fatalf("Eval(%q): %v", expr, err)
			}
			if got != tc.want {
				t.Fatalf("expr=%q got=%v want=%v", expr, got, tc.want)
			}
		})
	}
}

func TestQuoteEscaping(t *testing.T) {
	e := mustEngine(t)
	// A value containing a double quote must not break compilation.
	conds := []Condition{{Field: "subject", Operator: "contains", Value: `say "hi"`}}
	expr := BuildExpression("ALL", conds)
	if err := e.Validate(expr); err != nil {
		t.Fatalf("Validate(%q): %v", expr, err)
	}
	ok, err := e.Eval(expr, Email{Subject: `they say "hi" loudly`})
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if !ok {
		t.Fatalf("expected match for quoted value, expr=%q", expr)
	}
}

func TestHasAttachmentsEval(t *testing.T) {
	e := mustEngine(t)
	expr := BuildExpression("ALL", []Condition{{Field: "hasAttachments", Value: "true"}})
	ok, err := e.Eval(expr, Email{HasAttachments: true})
	if err != nil || !ok {
		t.Fatalf("want match with attachments: ok=%v err=%v expr=%q", ok, err, expr)
	}
	ok, err = e.Eval(expr, Email{HasAttachments: false})
	if err != nil || ok {
		t.Fatalf("want no match without attachments: ok=%v err=%v", ok, err)
	}
}

func TestEmptyConditionsNeverMatch(t *testing.T) {
	e := mustEngine(t)
	expr := BuildExpression("ALL", nil)
	if expr != "false" {
		t.Fatalf("want \"false\", got %q", expr)
	}
	ok, err := e.Eval(expr, Email{})
	if err != nil || ok {
		t.Fatalf("empty rule should not match: ok=%v err=%v", ok, err)
	}
}
