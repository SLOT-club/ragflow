package verify

import (
	"strings"
	"testing"
)

func TestBuildVerifyPrompt(t *testing.T) {
	p := BuildVerifyPrompt("What is 6*7?", "41")
	for _, want := range []string{"What is 6*7?", "41", acceptToken} {
		if !strings.Contains(p, want) {
			t.Fatalf("verify prompt missing %q", want)
		}
	}
}

func TestInterpretAccept(t *testing.T) {
	v := Interpret("my draft", "ACCEPT")
	if !v.Accepted {
		t.Fatal("expected accepted")
	}
	if v.Answer != "my draft" {
		t.Fatalf("answer = %q, want the draft unchanged", v.Answer)
	}
}

func TestInterpretAcceptWithWhitespaceAndSuffix(t *testing.T) {
	if v := Interpret("d", "  ACCEPT\n"); !v.Accepted {
		t.Fatal("trimmed ACCEPT should be accepted")
	}
	if v := Interpret("d", "ACCEPT (looks correct)"); !v.Accepted {
		t.Fatal("ACCEPT with trailing text should be accepted")
	}
}

func TestInterpretCorrection(t *testing.T) {
	v := Interpret("41", "42")
	if v.Accepted {
		t.Fatal("a different answer must not be accepted")
	}
	if v.Answer != "42" {
		t.Fatalf("answer = %q, want the correction 42", v.Answer)
	}
}
