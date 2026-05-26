package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"
)

// devicePollCap is the maximum time LoginDevice will wait for the user to
// authorize the device code. Matches OpenAI's ~15-minute device-code TTL.
// Declared as a var so tests can shrink it to exercise the internal cap path.
var devicePollCap = 15 * time.Minute

// Package vars so tests can redirect to httptest servers without constructors.
var (
	deviceUserCodeURL = Issuer + "/api/accounts/deviceauth/usercode"
	devicePollURL     = Issuer + "/api/accounts/deviceauth/token"

	// deviceSafetyMargin is added to the server-returned poll interval to avoid
	// rate-limit pushback. Tests set this to zero to keep suites fast.
	deviceSafetyMargin = 3 * time.Second
)

// deviceStderr is where the "open URL + enter code" prompt is written.
// Tests override this to suppress or capture the output.
var deviceStderr io.Writer = os.Stderr

func defaultDevicePrompt(verificationURI, userCode string) error {
	_, err := fmt.Fprintf(deviceStderr, "Open %s in a browser and enter code: %s\n", verificationURI, userCode)
	return err
}

// userCodeRe validates the user_code from the device init response before
// printing it to stderr. Rejects strings containing shell metacharacters or
// ANSI escape sequences that could be injected by a hostile server.
var userCodeRe = regexp.MustCompile(`^[A-Z0-9-]{4,16}$`)

// DeviceError is a typed error returned by LoginDevice for flow-level
// failures: invalid user_code charset, fatal poll response, or expiry.
// Err holds an optional underlying error so errors.Is/As can traverse the chain.
type DeviceError struct {
	Code    string
	Message string
	Err     error // underlying error, if any; see Unwrap
}

func (e *DeviceError) Error() string {
	msg := e.Message
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	if msg != "" {
		return "codexauth: device: " + e.Code + ": " + msg
	}
	return "codexauth: device: " + e.Code
}

// Unwrap returns the underlying error so errors.Is and errors.As can traverse
// the chain (e.g. errors.Is(de, context.Canceled) works through a *DeviceError).
func (e *DeviceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// deviceInitResp is the JSON body of the device usercode init response.
type deviceInitResp struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	Interval     string `json:"interval"` // string, not int, per observed wire format
}

// devicePollSuccess is the JSON body returned by the poll endpoint on success.
type devicePollSuccess struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

// parsePollInterval converts the server-returned interval string to seconds,
// applying a minimum of 5 s for zero, negative, or unparseable values.
func parsePollInterval(s string) int {
	if i, err := strconv.Atoi(s); err == nil && i > 0 {
		return i
	}
	return 5
}

// LoginDevice implements OpenAI's headless device-authorization flow for
// SSH / CI environments. The flow:
//
//  1. Requests a user_code from the device init endpoint.
//  2. Validates user_code charset to guard against terminal injection.
//  3. Prompts the user to open the device URL and enter the code.
//  4. Polls the token endpoint at (interval+3)s until authorized or expired.
//  5. Exchanges the authorization_code for OAuth tokens.
//  6. Extracts the account ID and persists credentials to disk.
//
// This is NOT RFC 8628 — OpenAI uses custom /api/accounts/deviceauth/
// endpoints and returns an authorization_code that must be exchanged at
// /oauth/token. See .claude/docs/codex-auth/03-device-flow.md.
//
// Deprecated: use NewClient(Options{AppName: "advisor"}).LoginDevice instead.
func LoginDevice(ctx context.Context) (Credentials, error) {
	return legacyClient().LoginDevice(ctx)
}

// LoginDevice implements OpenAI's headless device-authorization flow for this
// client.
func (c *Client) LoginDevice(ctx context.Context) (Credentials, error) {
	return loginDevice(ctx, c.save, c.devicePrompt)
}

func loginDevice(ctx context.Context, saveCreds func(AuthFile) error, prompt func(string, string) error) (Credentials, error) {
	if prompt == nil {
		prompt = defaultDevicePrompt
	}

	// Step 1: request a user code.
	dc, err := deviceInit(ctx)
	if err != nil {
		return Credentials{}, fmt.Errorf("codexauth: device init: %w", err)
	}

	// Step 2: validate before printing (terminal-escape injection guard).
	if !userCodeRe.MatchString(dc.UserCode) {
		return Credentials{}, &DeviceError{
			Code:    "invalid_user_code",
			Message: fmt.Sprintf("user_code %q fails charset validation (want ^[A-Z0-9-]{4,16}$)", dc.UserCode),
		}
	}

	// Step 3: prompt user.
	verificationURI := Issuer + "/codex/device"
	if err := prompt(verificationURI, dc.UserCode); err != nil {
		return Credentials{}, fmt.Errorf("codexauth: device prompt: %w", err)
	}

	// Step 4: parse interval (string per wire); add 3 s safety margin.
	pollInterval := time.Duration(parsePollInterval(dc.Interval))*time.Second + deviceSafetyMargin

	// Enforce a 15-minute internal cap regardless of the caller's context.
	innerCtx, cancel := context.WithTimeout(ctx, devicePollCap)
	defer cancel()

	// Build the poll body once outside the loop; bytes.NewReader rewinds each call.
	pollBody, err := json.Marshal(map[string]string{
		"device_auth_id": dc.DeviceAuthID,
		"user_code":      dc.UserCode,
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("codexauth: device poll body: %w", err)
	}

	// Step 5: sleep-then-poll loop.
	// Use time.NewTimer (not time.After) so the timer goroutine can be reclaimed
	// when the context fires before the interval elapses.
	var success devicePollSuccess
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()
	for {
		select {
		case <-innerCtx.Done():
			if ctx.Err() != nil {
				// Caller cancelled the outer context.
				return Credentials{}, ctx.Err()
			}
			// Internal 15-minute cap elapsed.
			return Credentials{}, &DeviceError{
				Code:    "device_code_expired",
				Message: "polling timed out after 15 minutes",
			}
		case <-timer.C:
		}

		result, err := devicePollOnce(innerCtx, pollBody)
		if err != nil {
			return Credentials{}, err // fatal; already wrapped as *DeviceError or ctx.Err()
		}
		if result != nil {
			success = *result
			break
		}
		// nil result → 403/404 (pending/transient) — sleep and poll again.
		// timer.C was already drained by the select above, so Reset is safe here.
		timer.Reset(pollInterval)
	}

	// Step 6: exchange the server-provided authorization_code + code_verifier.
	// Use the outer ctx (not innerCtx) — the 15-min cap is poll-loop-scoped only;
	// the exchange itself should succeed even right at the cap boundary.
	creds, err := ExchangeCode(ctx, success.AuthorizationCode, success.CodeVerifier, DeviceRedirectURI)
	if err != nil {
		return Credentials{}, fmt.Errorf("codexauth: device exchange: %w", err)
	}

	// Step 7: extract account ID (from the one-time ID token) and persist.
	creds.AccountID = ExtractAccountID(creds.IDToken)
	if err := saveCreds(AuthFile{OpenAI: &creds}); err != nil {
		return Credentials{}, fmt.Errorf("codexauth: device flow: save credentials: %w", err)
	}

	return creds, nil
}

// deviceInit POSTs to the usercode endpoint and returns the device init response.
func deviceInit(ctx context.Context) (*deviceInitResp, error) {
	body, err := json.Marshal(map[string]string{"client_id": ClientID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceUserCodeURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("device init %d: %q", resp.StatusCode, snippet)
	}

	var dc deviceInitResp
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, fmt.Errorf("decoding device init response: %w", err)
	}
	return &dc, nil
}

// devicePollOnce POSTs one poll request and returns:
//   - &devicePollSuccess, nil  — user authorized (200)
//   - nil, nil                 — still pending (403 or 404)
//   - nil, ctx.Err()           — request failed due to context cancellation/expiry
//   - nil, *DeviceError        — fatal status (anything else)
func devicePollOnce(ctx context.Context, body []byte) (*devicePollSuccess, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, devicePollURL, bytes.NewReader(body))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &DeviceError{Code: "device_poll_failed", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &DeviceError{Code: "device_poll_failed", Err: err}
	}
	defer resp.Body.Close() //nolint:errcheck

	switch resp.StatusCode {
	case http.StatusOK:
		var ok devicePollSuccess
		if err := json.NewDecoder(resp.Body).Decode(&ok); err != nil {
			return nil, &DeviceError{Code: "device_poll_failed", Err: err}
		}
		return &ok, nil
	case http.StatusForbidden, http.StatusNotFound:
		// 403 = pending; 404 = transient (observed in opencode; not fatal).
		return nil, nil
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &DeviceError{
			Code:    "device_code_expired",
			Message: fmt.Sprintf("poll status %d: %q", resp.StatusCode, snippet),
		}
	}
}
