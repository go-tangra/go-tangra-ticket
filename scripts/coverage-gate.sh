#!/usr/bin/env bash
# Service coverage gate: >=80% overall, 100% for packages that hold key material,
# decide access, or parse untrusted input into trust decisions (ticket: the
# authorizer, sealed envelopes, secret resolution, email threading) and the
# rules COMPILE path (untrusted condition values / raw CEL expressions become an
# executable program there).
set -euo pipefail
PROFILE="${1:-coverage.out}"
MODULE="github.com/go-tangra/go-tangra-ticket/v4"
SECURITY_PKGS=("internal/authz" "internal/sealed" "internal/secrets" "internal/thread")
# Functions of internal/rules/engine.go that form the compile path. (NewEngine is
# excluded: its only uncovered branch is the static CEL environment failing to
# build, which cannot happen with the fixed declarations.)
RULES_COMPILE_FUNCS="invalid|quote|doubleLiteral|conditionExpr|NormalizeMatch|BuildExpression|Effective|Compile|Program|Evaluate|Forget|has|asciiLower|truncate"
total=$(go tool cover -func="$PROFILE" | awk '/^total:/ {gsub("%","",$3); print $3}')
echo "coverage: total ${total}%"
fail=0
awk -v t="$total" 'BEGIN { if (t+0 < 80) exit 1 }' || { echo "coverage: total below 80%" >&2; fail=1; }
for p in "${SECURITY_PKGS[@]}"; do
  pct=$(go tool cover -func="$PROFILE" | awk -v pre="$MODULE/$p/" '
    index($1, pre)==1 { rest=substr($1, length(pre)+1); if (rest ~ /\//) next
      if ($1 ~ /doc\.go/) next; gsub("%","",$3); s+=$3; n++ }
    END { if (n==0) print "n/a"; else printf "%.1f", s/n }')
  echo "coverage: $p ${pct}%"
  if [[ "$pct" != "n/a" ]]; then awk -v v="$pct" 'BEGIN { if (v+0 < 100) exit 1 }' || { echo "coverage: $p must be 100%" >&2; fail=1; }; fi
done
rc=$(go tool cover -func="$PROFILE" | awk -v f="$MODULE/internal/rules/engine.go:" -v re="^($RULES_COMPILE_FUNCS)\$" '
  index($1, f)==1 && $2 ~ re { gsub("%","",$3); s+=$3; n++ }
  END { if (n==0) print "n/a"; else printf "%.1f", s/n }')
echo "coverage: rules compile ${rc}%"
if [[ "$rc" == "n/a" ]]; then echo "coverage: rules compile functions not found" >&2; fail=1
else awk -v v="$rc" 'BEGIN { if (v+0 < 100) exit 1 }' || { echo "coverage: rules compile path must be 100%" >&2; fail=1; }; fi
exit $fail
