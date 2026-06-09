// Package rules evaluates Cloudflare-style ticket auto-tagging rules. A rule
// is a set of conditions (field/operator/value) combined with match=ALL|ANY;
// the engine compiles them to a CEL expression and evaluates it against the
// parsed email. A raw CEL expression can also be supplied to override.
package rules

import (
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
)

// Condition is one row of the rule builder.
type Condition struct {
	Field    string `json:"field"`    // subject | body | from | fromName | recipient | fromDomain | hasAttachments | spamScore
	Operator string `json:"operator"` // string: contains|not_contains|equals|not_equals|starts_with|ends_with|matches ; numeric: gt|gte|lt|lte|eq|neq
	Value    string `json:"value"`
}

// Action is one row of the "Then apply" block.
type Action struct {
	Type       string   `json:"type"` // tag | assign | status | priority | drop
	TagKind    string   `json:"tagKind"`
	TagNames   []string `json:"tagNames"`
	AssigneeID uint32   `json:"assigneeId"`
	Status     string   `json:"status"`
	Priority   string   `json:"priority"`
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

// String fields support text operators; hasAttachments is boolean; spamScore
// is numeric.
var allowedFields = map[string]bool{
	"subject": true, "body": true, "from": true, "fromName": true, "recipient": true, "fromDomain": true,
}

// Engine holds a reusable CEL environment.
type Engine struct {
	env *cel.Env
}

func NewEngine() (*Engine, error) {
	env, err := cel.NewEnv(
		ext.Strings(),
		cel.Variable("subject", cel.StringType),
		cel.Variable("body", cel.StringType),
		cel.Variable("from", cel.StringType),
		cel.Variable("fromName", cel.StringType),
		cel.Variable("recipient", cel.StringType),
		cel.Variable("fromDomain", cel.StringType),
		cel.Variable("hasAttachments", cel.BoolType),
		cel.Variable("spamScore", cel.DoubleType),
	)
	if err != nil {
		return nil, err
	}
	return &Engine{env: env}, nil
}

// BuildExpression compiles structured conditions into a CEL boolean
// expression. Empty -> "false" (a rule with no conditions never matches).
func BuildExpression(match string, conds []Condition) string {
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		if e := conditionExpr(c); e != "" {
			parts = append(parts, "("+e+")")
		}
	}
	if len(parts) == 0 {
		return "false"
	}
	op := " && "
	if strings.EqualFold(match, "ANY") {
		op = " || "
	}
	return strings.Join(parts, op)
}

func conditionExpr(c Condition) string {
	// Boolean field: hasAttachments. Operator is ignored; the value
	// ("true"/"false", default true) decides the comparison.
	if c.Field == "hasAttachments" {
		if strings.EqualFold(strings.TrimSpace(c.Value), "false") {
			return "hasAttachments == false"
		}
		return "hasAttachments == true"
	}
	// Numeric field: spamScore. The value is parsed as a float and emitted as
	// a CEL double literal (never interpolated as text), so it can't inject.
	if c.Field == "spamScore" {
		v, err := strconv.ParseFloat(strings.TrimSpace(c.Value), 64)
		if err != nil {
			return ""
		}
		// Emit a CEL double literal (must contain a '.', else CEL treats it as
		// an int and `double > int` fails to type-check).
		lit := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(lit, ".") {
			lit += ".0"
		}
		switch c.Operator {
		case "gt":
			return "spamScore > " + lit
		case "gte":
			return "spamScore >= " + lit
		case "lt":
			return "spamScore < " + lit
		case "lte":
			return "spamScore <= " + lit
		case "eq", "equals":
			return "spamScore == " + lit
		case "neq", "ne", "not_equals":
			return "spamScore != " + lit
		default:
			return ""
		}
	}
	if !allowedFields[c.Field] {
		return ""
	}
	lf := c.Field + ".lowerAscii()"
	lv := quote(strings.ToLower(c.Value))
	switch c.Operator {
	case "contains":
		return lf + ".contains(" + lv + ")"
	case "not_contains":
		return "!(" + lf + ".contains(" + lv + "))"
	case "equals":
		return lf + " == " + lv
	case "not_equals":
		return lf + " != " + lv
	case "starts_with":
		return lf + ".startsWith(" + lv + ")"
	case "ends_with":
		return lf + ".endsWith(" + lv + ")"
	case "matches": // regex, evaluated against the raw field (use (?i) for case-insensitivity)
		return c.Field + ".matches(" + quote(c.Value) + ")"
	default:
		return ""
	}
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// Validate compiles an expression and returns an error if it is not a valid
// boolean CEL expression. Used by the rule service before saving.
func (e *Engine) Validate(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return nil
	}
	_, iss := e.env.Compile(expr)
	if iss != nil {
		return iss.Err()
	}
	return nil
}

// Eval compiles and evaluates expr against m. Returns false on any error.
func (e *Engine) Eval(expr string, m Email) (bool, error) {
	if strings.TrimSpace(expr) == "" {
		return false, nil
	}
	ast, iss := e.env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return false, iss.Err()
	}
	prg, err := e.env.Program(ast)
	if err != nil {
		return false, err
	}
	out, _, err := prg.Eval(map[string]any{
		"subject":        m.Subject,
		"body":           m.Body,
		"from":           m.From,
		"fromName":       m.FromName,
		"recipient":      m.Recipient,
		"fromDomain":     m.FromDomain,
		"hasAttachments": m.HasAttachments,
		"spamScore":      m.SpamScore,
	})
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	return ok && b, nil
}
