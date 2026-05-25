package codexauth

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestGenerateVerifier_LengthAndCharset confirms the verifier is exactly
// 43 characters drawn exclusively from the URL-unreserved alphabet.
func TestGenerateVerifier_LengthAndCharset(t *testing.T) {
	v, err := GenerateVerifier()
	if err != nil {
		t.Fatalf("GenerateVerifier() error: %v", err)
	}
	if len(v) != 43 {
		t.Errorf("GenerateVerifier() len = %d, want 43", len(v))
	}
	for i, ch := range v {
		if !strings.ContainsRune(pkceAlphabet, ch) {
			t.Errorf("GenerateVerifier()[%d] = %q, not in URL-unreserved alphabet", i, ch)
		}
	}
}

// TestGenerateVerifier_NoDuplicates generates 1_000 verifiers and asserts
// no two are identical. A collision in 1_000 draws from 66^43 possible
// values is astronomically unlikely — any failure here almost certainly
// indicates a broken generator (e.g. seeded with a constant).
func TestGenerateVerifier_NoDuplicates(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := range 1000 {
		v, err := GenerateVerifier()
		if err != nil {
			t.Fatalf("GenerateVerifier() error on iteration %d: %v", i, err)
		}
		if _, dup := seen[v]; dup {
			t.Fatalf("GenerateVerifier() returned duplicate value %q", v)
		}
		seen[v] = struct{}{}
	}
}

// TestChallengeFromVerifier_RFC7636AppendixB checks the implementation
// against the fixture vector from RFC 7636 Appendix B.
//
// Verifier:  dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk
// Challenge: E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM
func TestChallengeFromVerifier_RFC7636AppendixB(t *testing.T) {
	const (
		verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		wantChall = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)
	got := ChallengeFromVerifier(verifier)
	if got != wantChall {
		t.Errorf("ChallengeFromVerifier(%q) = %q, want %q", verifier, got, wantChall)
	}
}

// TestChallengeFromVerifier_NoPadding asserts the challenge string contains
// no base64 padding characters.
func TestChallengeFromVerifier_NoPadding(t *testing.T) {
	v, err := GenerateVerifier()
	if err != nil {
		t.Fatalf("GenerateVerifier() error: %v", err)
	}
	ch := ChallengeFromVerifier(v)
	if strings.Contains(ch, "=") {
		t.Errorf("ChallengeFromVerifier() contains padding: %q", ch)
	}
}

// TestGenerateState_FormatAndLength confirms the state token is valid
// base64url without padding and decodes to exactly 32 bytes.
func TestGenerateState_FormatAndLength(t *testing.T) {
	s, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() error: %v", err)
	}
	if strings.Contains(s, "=") {
		t.Errorf("GenerateState() contains padding: %q", s)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Errorf("GenerateState() is not valid base64url: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("GenerateState() decoded len = %d, want 32", len(decoded))
	}
}

// TestGenerateState_NoDuplicates generates 1_000 state tokens and asserts
// no two are identical.
func TestGenerateState_NoDuplicates(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := range 1000 {
		s, err := GenerateState()
		if err != nil {
			t.Fatalf("GenerateState() error on iteration %d: %v", i, err)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("GenerateState() returned duplicate value %q", s)
		}
		seen[s] = struct{}{}
	}
}
