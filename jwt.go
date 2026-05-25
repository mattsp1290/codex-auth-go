package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// idClaims is the subset of OpenAI ID-token claims needed to identify the
// account. The server may place the account identifier in any of three
// locations depending on account type; ExtractAccountID tries them in order.
type idClaims struct {
	// Top-level chatgpt_account_id — present on non-enterprise accounts.
	ChatGPTAccountID string `json:"chatgpt_account_id"`

	// https://api.openai.com/auth is a custom claim namespace used by enterprise
	// SSO accounts. Checked after the top-level chatgpt_account_id.
	OAIAuth struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
		OrganizationID   string `json:"organization_id"`
	} `json:"https://api.openai.com/auth"`

	// Organizations is a fallback when neither chatgpt_account_id path is
	// populated; the first entry's ID is used.
	Organizations []struct {
		ID string `json:"id"`
	} `json:"organizations"`
}

// ExtractAccountID returns the account identifier embedded in an OpenAI
// ID token. It tries four locations in order and returns the first
// non-empty value found:
//
//  1. Top-level `chatgpt_account_id` — present on standard consumer accounts.
//  2. `https://api.openai.com/auth.chatgpt_account_id` — custom claim namespace
//     used by enterprise SSO accounts.
//  3. `https://api.openai.com/auth.organization_id` — fallback within the same
//     auth namespace when chatgpt_account_id is absent.
//  4. `organizations[0].id` — last resort for accounts linked to an org.
//
// Returns "" if the token is unparseable, if none of the four claim paths
// are populated, or if the input is malformed in any way. This function does
// NOT verify the JWT signature — it is a passive consumer of OpenAI's claim.
// Safe to call with attacker-controlled input: no crypto, no network, and
// it never panics. It always returns "" on any decode or parse error.
//
// Segment acceptance: the function accepts any token with at least two
// dot-separated segments and treats segment [1] as the base64url payload.
// JWE tokens (4–5 segments) and 2-segment tokens will decode successfully
// but yield empty claims because their second segment is either encrypted
// or missing expected fields — both cases return "" cleanly.
func ExtractAccountID(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := decodeBase64URLTolerant(parts[1])
	if err != nil {
		return ""
	}
	var c idClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return ""
	}
	switch {
	case c.ChatGPTAccountID != "":
		return c.ChatGPTAccountID
	case c.OAIAuth.ChatGPTAccountID != "":
		return c.OAIAuth.ChatGPTAccountID
	case c.OAIAuth.OrganizationID != "":
		return c.OAIAuth.OrganizationID
	case len(c.Organizations) > 0 && c.Organizations[0].ID != "":
		return c.Organizations[0].ID
	}
	return ""
}

// decodeBase64URLTolerant decodes a base64url string that may or may not
// carry padding characters. Standard JWTs omit padding (RFC 7515 §2), so we
// try RawURLEncoding first; if that fails we add the required '=' padding
// and retry with URLEncoding.
func decodeBase64URLTolerant(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Add padding to the next multiple of 4.
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}
