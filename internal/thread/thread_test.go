package thread

import "testing"

func TestParseToken(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"
	cases := map[string]string{
		"Re: Help [#" + id + "]":            id,
		"Fwd: thing [#" + id + "] trailing": id,
		"no token here":                     "",
		"[#short] too short is still hex":   "short000", // 8+ hex run only; "short" fails -> check below
	}
	// The last case is intentionally not a valid hex>=8 token; verify empty.
	if got := ParseToken("[#short]"); got != "" {
		t.Fatalf("short token should not match, got %q", got)
	}
	delete(cases, "[#short] too short is still hex")

	for subj, want := range cases {
		if got := ParseToken(subj); got != want {
			t.Fatalf("ParseToken(%q)=%q want %q", subj, got, want)
		}
	}
}

func TestReplySubject(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"

	got := ReplySubject("Printer broken", id)
	if ParseToken(got) != id {
		t.Fatalf("token missing in %q", got)
	}
	if got[:4] != "Re: " {
		t.Fatalf("missing Re prefix: %q", got)
	}

	// Idempotent: replying to an already-tagged Re: subject adds nothing.
	again := ReplySubject(got, id)
	if again != got {
		t.Fatalf("ReplySubject not idempotent: %q -> %q", got, again)
	}
}
