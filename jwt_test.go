package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// makeJWT builds a minimal 2-segment JWT (header.payload) for testing.
// It has no signature segment so it exercises the ≥2-segment acceptance
// path without requiring crypto operations.
func makeJWT(claims any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload)
}

// makeJWTWithSig builds a standard 3-segment JWS with a fake signature.
func makeJWTWithSig(claims any) string {
	return makeJWT(claims) + ".fakesig"
}

func TestExtractAccountID(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		// ── Happy paths ──────────────────────────────────────────────
		{
			name: "top-level chatgpt_account_id",
			token: makeJWTWithSig(map[string]any{
				"chatgpt_account_id": "acct-top-level",
			}),
			want: "acct-top-level",
		},
		{
			name: "auth namespace chatgpt_account_id",
			token: makeJWTWithSig(map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "acct-auth-ns",
				},
			}),
			want: "acct-auth-ns",
		},
		{
			name: "auth namespace organization_id",
			token: makeJWTWithSig(map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"organization_id": "org-auth-ns",
				},
			}),
			want: "org-auth-ns",
		},
		{
			name: "organizations fallback",
			token: makeJWTWithSig(map[string]any{
				"organizations": []map[string]any{
					{"id": "org-fallback-id"},
				},
			}),
			want: "org-fallback-id",
		},
		{
			name: "top-level wins over auth namespace",
			token: makeJWTWithSig(map[string]any{
				"chatgpt_account_id": "acct-wins",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "acct-loses",
				},
			}),
			want: "acct-wins",
		},

		// ── Edge cases: all must return "" cleanly ────────────────────
		{
			name:  "empty string",
			token: "",
			want:  "",
		},
		{
			name:  "single segment, no dot",
			token: "nodots",
			want:  "",
		},
		{
			name:  "2-segment token, no matching claim (sub only)",
			token: makeJWT(map[string]any{"sub": "user123"}),
			want:  "",
		},
		{
			name: "4-segment token (JWE envelope — segment[1] is not JSON)",
			// A JWE segment[1] is a base64url-encoded encrypted key, not JSON.
			token: "hdr." + base64.RawURLEncoding.EncodeToString([]byte("encryptedkey")) + ".iv.ciphertext",
			want:  "",
		},
		{
			name:  "5-segment token (JWE with AAD — segment[1] is not JSON)",
			token: "hdr." + base64.RawURLEncoding.EncodeToString([]byte("encryptedkey")) + ".iv.ciphertext.tag",
			want:  "",
		},
		{
			name:  "payload is valid base64url but not JSON",
			token: "hdr." + base64.RawURLEncoding.EncodeToString([]byte("not-json!")) + ".sig",
			want:  "",
		},
		{
			name: "payload is valid JSON but missing auth claim",
			token: makeJWTWithSig(map[string]any{
				"sub":   "user-sub",
				"email": "user@example.com",
			}),
			want: "",
		},
		// Padding tolerance: segment[1] uses standard base64url WITH padding
		// (some non-standard encoders include '=' chars). RawURLEncoding
		// rejects '='; the fallback path re-encodes with URLEncoding.
		{
			// base64.URLEncoding.EncodeToString([]byte("{}")) = "e30=" (1 pad char)
			name:  "payload with 1 padding char",
			token: "hdr." + base64.URLEncoding.EncodeToString([]byte("{}")) + ".sig",
			want:  "",
		},
		{
			// base64.URLEncoding.EncodeToString([]byte("a")) = "YQ==" (2 pad chars)
			name:  "payload with 2 padding chars",
			token: "hdr." + base64.URLEncoding.EncodeToString([]byte("a")) + ".sig",
			want:  "",
		},
		{
			// "eA" is the RawURLEncoding of []byte("x") (len 2; standard base64
			// never produces 3 '=' pads). "eA===" is an invalid base64url string
			// that no RFC 4648 encoder emits: len("eA===") % 4 == 1, which the
			// tolerant decoder cannot rescue by adding padding. Verifies graceful
			// rejection without panicking.
			name:  "payload segment invalid (synthetic 3-equals suffix no encoder produces)",
			token: "hdr." + base64.RawURLEncoding.EncodeToString([]byte("x")) + "===" + ".sig",
			want:  "",
		},
		{
			// Payload contains valid base64url-encoded JSON with UTF-8 characters
			// outside ASCII (e.g. accented é). json.Unmarshal handles UTF-8 fine;
			// ExtractAccountID must not panic or corrupt the result.
			name: "payload with UTF-8 outside ASCII in claim value",
			token: makeJWTWithSig(map[string]any{
				"chatgpt_account_id": "acct-café",
			}),
			want: "acct-café",
		},
		{
			// Whitespace in segment[1] (the payload position) causes base64url
			// decode to fail for both RawURLEncoding and URLEncoding — the space
			// character is not in the base64url alphabet.
			name:  "whitespace at start of payload segment",
			token: "hdr." + " " + base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"ws-test"}`)) + ".sig",
			want:  "",
		},
		{
			name:  "whitespace at end of payload segment",
			token: "hdr." + base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"ws-test"}`)) + " " + ".sig",
			want:  "",
		},
		{
			// Padding tolerance: padded payload with a real claim should still extract.
			name: "padded payload with real chatgpt_account_id",
			token: func() string {
				p, _ := json.Marshal(map[string]any{"chatgpt_account_id": "acct-padded"})
				return "hdr." + base64.URLEncoding.EncodeToString(p) + ".sig"
			}(),
			want: "acct-padded",
		},
		{
			// auth.chatgpt_account_id wins over auth.organization_id within the same namespace.
			name: "auth chatgpt_account_id wins over auth organization_id",
			token: makeJWTWithSig(map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "acct-wins",
					"organization_id":    "org-loses",
				},
			}),
			want: "acct-wins",
		},
		{
			// auth.organization_id wins over organizations[0].id.
			name: "auth organization_id wins over organizations fallback",
			token: makeJWTWithSig(map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"organization_id": "org-wins",
				},
				"organizations": []map[string]any{{"id": "org-loses"}},
			}),
			want: "org-wins",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractAccountID(c.token)
			if got != c.want {
				t.Errorf("ExtractAccountID() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestExtractAccountID_TypeMismatch documents the all-or-nothing behavior of
// json.Unmarshal: a type mismatch in ANY field aborts the entire decode and
// ExtractAccountID returns "" — even if an otherwise-valid claim is present.
// This is intentional: the implementation favors simplicity (typed struct) over
// per-path resilience (map[string]any + manual type assertions).
func TestExtractAccountID_TypeMismatch(t *testing.T) {
	// chatgpt_account_id is a valid string, but organizations[0].id is a number
	// (not a string). json.Unmarshal fails on the type mismatch and returns error,
	// so ExtractAccountID returns "" despite the top-level claim being present.
	tok := makeJWTWithSig(map[string]any{
		"chatgpt_account_id": "acct-type-mismatch-test",
		"organizations":      []map[string]any{{"id": 12345}}, // int, not string
	})
	if got := ExtractAccountID(tok); got != "" {
		t.Errorf("ExtractAccountID() = %q, want \"\" (type mismatch in any field aborts all decoding)", got)
	}
}

// TestExtractAccountID_NoPanic runs ExtractAccountID with adversarial inputs
// to confirm the function never panics. Any return value is acceptable; only
// panic-freedom matters here.
func TestExtractAccountID_NoPanic(t *testing.T) {
	adversarial := []string{
		"",
		".",
		"..",
		"...",
		strings.Repeat(".", 10),
		"a.!@#$.c",                              // invalid base64 chars
		"a." + strings.Repeat("A", 1000) + ".c", // large but valid base64
		"a.\x00\x01\x02.c",                      // binary in payload segment
	}
	for _, tok := range adversarial {
		label := tok
		if len(label) > 20 {
			label = label[:20]
		}
		t.Run(label, func(_ *testing.T) {
			_ = ExtractAccountID(tok)
		})
	}
}
