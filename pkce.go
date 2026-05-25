package codexauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// pkceAlphabet is the URL-unreserved character set defined by RFC 7636 §4.1.
// Length is 66 characters: 26 upper + 26 lower + 10 digits + 4 punctuation.
const pkceAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// Compile-time assertion: alphabetN must equal len(pkceAlphabet). Both
// subtraction directions must succeed as unsigned integers; if the values
// diverge, exactly one of these lines refuses to compile.
const (
	_ = uint(len(pkceAlphabet) - 66) // fails if len(pkceAlphabet) < 66
	_ = uint(66 - len(pkceAlphabet)) // fails if len(pkceAlphabet) > 66
)

// GenerateVerifier returns a 43-character PKCE code verifier sampled from the
// URL-unreserved alphabet (RFC 7636 §4.1) using rejection-sampling on
// crypto/rand bytes to eliminate modulo bias. Entropy: 43 × log2(66) ≈ 260
// bits, exceeding RFC 7636 §7.1's 128-bit recommendation. The rejection
// threshold is (256/alphabetN)*alphabetN so each accepted byte maps to
// exactly one of the 66 alphabet positions with uniform probability.
func GenerateVerifier() (string, error) {
	const (
		length    = 43
		alphabetN = 66
		threshold = (256 / alphabetN) * alphabetN // = 198; derived, not magic
	)
	out := make([]byte, length)
	filled := 0
	for filled < length {
		// Over-read to amortise crypto/rand syscall cost; worst-case
		// rejection rate is ~22.7% so 2× over-read converges quickly.
		buf := make([]byte, (length-filled)*2)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < threshold {
				out[filled] = pkceAlphabet[int(b)%alphabetN]
				filled++
				if filled == length {
					break
				}
			}
		}
	}
	return string(out), nil
}

// ChallengeFromVerifier computes the PKCE S256 code challenge from a verifier
// string. Returns base64url(sha256(verifier)) with no padding, as required by
// RFC 7636 §4.2.
func ChallengeFromVerifier(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateState returns a 32-byte cryptographically random nonce encoded as
// base64url with no padding. Used as the OAuth2 state parameter to prevent
// CSRF (RFC 6749 §10.12).
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
