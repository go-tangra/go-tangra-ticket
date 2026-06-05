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
	got := ReplySubject("Printer broken")
	if got != "Re: Printer broken" {
		t.Fatalf("want %q, got %q", "Re: Printer broken", got)
	}
	// No token is added to the subject any more.
	if ParseToken(got) != "" {
		t.Fatalf("subject must not carry a token: %q", got)
	}
	// Existing Re: is not doubled.
	if again := ReplySubject(got); again != got {
		t.Fatalf("Re: doubled: %q", again)
	}
}

func TestAppendReference(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"

	body := "Thanks for reaching out, we are on it."
	out := AppendReference(body, id)
	if ParseToken(out) != id {
		t.Fatalf("reference token missing from body: %q", out)
	}
	// Idempotent: don't append twice if the token is already present.
	if again := AppendReference(out, id); again != out {
		t.Fatalf("reference appended twice:\n%q", again)
	}
}
