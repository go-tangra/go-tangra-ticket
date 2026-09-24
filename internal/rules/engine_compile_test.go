package rules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-ticket/v4/internal/store"
)

// The compile path (quote → conditionExpr → BuildExpression → Effective →
// Compile → Program) is a trust boundary: condition values and raw expressions
// come from rule authors. These cases close the remaining edges of that path.

func TestInvalidMessageTruncatedOnRuneBoundary(t *testing.T) {
	long := "a" + strings.Repeat("я", maxErrorLen) // odd byte offset: the cut splits a rune
	ie := invalid("expression", "%s", long)
	if len(ie.Msg) > maxErrorLen || !utf8.ValidString(ie.Msg) || !strings.HasPrefix(ie.Msg, "aя") {
		t.Fatalf("truncated message: len %d valid %v", len(ie.Msg), utf8.ValidString(ie.Msg))
	}
	if got := truncate("a"+strings.Repeat("я", 10), 6); got != "aяя" {
		t.Fatalf("truncate = %q", got)
	}
}

func TestQuoteEscapesAstralNonPrintable(t *testing.T) {
	const tag = "\U000E0001" // LANGUAGE TAG: format char above the BMP
	if q := quote("x" + tag); q != `"x\U000e0001"` {
		t.Fatalf("quote = %s", q)
	}
	e := mustEngine(t)
	r := rule("all", cond("subject", "contains", tag))
	if !eval(t, e, r, Email{Subject: "hi" + tag}) || eval(t, e, r, Email{Subject: "hi"}) {
		t.Fatal("escaped astral literal does not round-trip")
	}
}

func TestExpressionMustBeUTF8(t *testing.T) {
	e := mustEngine(t)
	r := rule("all")
	r.Expression = "subject == \"\xff\""
	var ie *InvalidError
	if _, err := e.Compile(r); !errors.As(err, &ie) || ie.Field != "expression" {
		t.Fatalf("non-UTF-8 expression accepted: %v", err)
	}
}

func TestProgramCacheIsBounded(t *testing.T) {
	e := mustEngine(t)
	for i := 0; i < maxCacheItems; i++ {
		e.cache[fmt.Sprintf("x%d", i)] = &Program{}
	}
	if _, err := e.Program(rule("all", cond("subject", "contains", "a"))); err != nil {
		t.Fatal(err)
	}
	if e.CacheLen() != 1 {
		t.Fatalf("full cache must reset, len %d", e.CacheLen())
	}
}

func TestEvaluateRefusesNonBoolean(t *testing.T) {
	e := mustEngine(t)
	ast, iss := e.env.Compile("subject")
	if iss != nil && iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	prg, err := e.env.Program(ast)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Evaluate(context.Background(), &Program{prg: prg}, Email{Subject: "s"}); err == nil {
		t.Fatal("non-boolean result accepted")
	}
	if _, err := e.Program(store.Rule{ID: "n", Version: 1, Expression: "subject"}); err == nil {
		t.Fatal("non-boolean rule compiled")
	}
}
