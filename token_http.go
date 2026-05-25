package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tokenHTTPTimeout is the hard deadline applied to every token-endpoint request
// at the httpClient level, independent of any context deadline the caller
// provides. Whichever fires first wins.
const tokenHTTPTimeout = 30 * time.Second

// httpClient is the package-level HTTP client for all token-endpoint calls.
// A dedicated *http.Client (not http.DefaultClient) gives us an independent
// Timeout field. The transport is http.DefaultTransport, which is safe to
// share across clients.
var httpClient = &http.Client{Timeout: tokenHTTPTimeout}

// tokenResponse is the JSON body returned by auth.openai.com/oauth/token for
// both the authorization-code exchange and the refresh-token grant.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// AuthError represents an RFC 6749 error response from the token endpoint.
// The Code field contains the OAuth error code string (e.g. "invalid_grant",
// "invalid_client") as defined in RFC 6749 §5.2. Callers use errors.As to
// inspect Code and map it to the appropriate core.ErrType.
type AuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *AuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("auth: %s: %s", e.Code, e.Description)
	}
	if e.Code != "" {
		return "auth: " + e.Code
	}
	return "auth: unknown_error"
}

// postForm POSTs a form-encoded body to endpoint and decodes the JSON response.
//
// Error mapping:
//   - 2xx + valid JSON with non-empty access_token → *tokenResponse, nil
//   - 4xx + {"error":"..."} JSON body → nil, *AuthError (inspectable via errors.As)
//   - 4xx without parseable OAuth error, 5xx, or network failure → nil, wrapped error
//
// The caller supplies context for timeout/cancellation; tokenHTTPTimeout is
// also applied at the httpClient level — whichever fires first wins.
//
// The response body is bounded to 64 KiB before reading; any larger response
// is treated as an error. The full body is consumed before returning so the
// underlying TCP connection is returned to the pool (keep-alive reuse).
func postForm(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	// Bound the read to 64 KiB. A legitimate token response is under 4 KiB;
	// anything larger is either an error page or an unexpected payload.
	const maxBodyBytes = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae AuthError
		if jsonErr := json.Unmarshal(body, &ae); jsonErr == nil && ae.Code != "" {
			return nil, &ae
		}
		// Truncate and quote the body so control characters can't bloat logs.
		snippet := body
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return nil, fmt.Errorf("token endpoint %d: %q", resp.StatusCode, snippet)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned 2xx but access_token is empty")
	}
	return &tr, nil
}
