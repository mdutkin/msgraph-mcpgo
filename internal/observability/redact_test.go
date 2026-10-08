package observability

import "testing"

func TestRedactIsStableAndNonReversible(t *testing.T) {
	const subject = "Q3 layoff plan"

	got := Redact(subject)
	if got == subject {
		t.Fatalf("Redact returned the plaintext value")
	}
	if got != Redact(subject) {
		t.Fatalf("Redact is not stable across calls")
	}
	if len(got) != len("sha256:")+redactDigestLen {
		t.Fatalf("unexpected digest length: %q", got)
	}
}

func TestRedactDistinguishesValues(t *testing.T) {
	if Redact("alpha") == Redact("beta") {
		t.Fatal("different inputs produced the same digest")
	}
}

func TestRedactEmptyStaysEmpty(t *testing.T) {
	if got := Redact(""); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestRedactAll(t *testing.T) {
	if RedactAll(nil) != nil {
		t.Fatal("expected nil for nil input")
	}

	got := RedactAll([]string{"a", "b"})
	if len(got) != 2 || got[0] == "a" || got[0] == got[1] {
		t.Fatalf("unexpected result: %v", got)
	}
}
