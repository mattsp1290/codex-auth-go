package codexauth

// Tests in this file mutate package-level vars (deviceUserCodeURL, devicePollURL,
// tokenEndpointURL, deviceStderr, deviceSafetyMargin, devicePollCap, pathFunc)
// and MUST NOT call t.Parallel().
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// deviceTestHarness wires up httptest servers for the device init, poll, and
// token exchange endpoints, overriding the package-level URL vars and
// redirecting stderr output to a buffer.
type deviceTestHarness struct {
	initSrv    *httptest.Server
	pollSrv    *httptest.Server
	tokenSrv   *httptest.Server
	stderrBuf  *bytes.Buffer
	origInit   string
	origPoll   string
	origToken  string
	origStderr io.Writer
}

// origSafetyMargin holds the real values so close() can restore them.
var origSafetyMargin = deviceSafetyMargin

func newDeviceTestHarness(
	initHandler http.HandlerFunc,
	pollHandler http.HandlerFunc,
	tokenHandler http.HandlerFunc,
) *deviceTestHarness {
	h := &deviceTestHarness{
		stderrBuf: &bytes.Buffer{},
		origInit:  deviceUserCodeURL,
		origPoll:  devicePollURL,
		origToken: tokenEndpointURL,
	}
	h.initSrv = httptest.NewServer(initHandler)
	h.pollSrv = httptest.NewServer(pollHandler)
	if tokenHandler != nil {
		h.tokenSrv = httptest.NewServer(tokenHandler)
		tokenEndpointURL = h.tokenSrv.URL
	}
	deviceUserCodeURL = h.initSrv.URL
	devicePollURL = h.pollSrv.URL
	h.origStderr = deviceStderr
	deviceStderr = h.stderrBuf
	// Zero out the safety margin so tests don't sleep 3 s per poll.
	deviceSafetyMargin = 0
	return h
}

func (h *deviceTestHarness) close() {
	h.initSrv.Close()
	h.pollSrv.Close()
	if h.tokenSrv != nil {
		h.tokenSrv.Close()
	}
	deviceUserCodeURL = h.origInit
	devicePollURL = h.origPoll
	tokenEndpointURL = h.origToken
	deviceStderr = h.origStderr
	deviceSafetyMargin = origSafetyMargin
	// devicePollCap is restored per-test by tests that override it.
}

// validTokenHandler returns a handler that responds with a minimal valid TokenResponse.
func validTokenHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"access_token":"at-device",
			"refresh_token":"rt-device",
			"id_token":"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEyMyIsImh0dHBzOi8vYXBpLm9wZW5haS5jb20vYWNjb3VudF9pZCI6ImFjY3QtZGV2aWNlIn0.sig",
			"expires_in":3600
		}`)
	}
}

// TestLoginDevice_HappyPath verifies the full flow: init → 2×403 → 200 → exchange → creds.
func TestLoginDevice_HappyPath(t *testing.T) {
	// Override credstore so we don't write to disk.
	tmpDir := t.TempDir()
	pathMu.Lock()
	origPath := pathFunc
	pathFunc = func() (string, error) { return tmpDir + "/auth.json", nil }
	pathMu.Unlock()
	t.Cleanup(func() {
		pathMu.Lock()
		pathFunc = origPath
		pathMu.Unlock()
	})

	pollCount := 0
	h := newDeviceTestHarness(
		// Init handler.
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("init: method = %s, want POST", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"device_auth_id":"DAU_test","user_code":"ABCD-1234","interval":"1"}`)
		},
		// Poll handler: 2×403, then 200.
		func(w http.ResponseWriter, r *http.Request) {
			pollCount++
			if pollCount < 3 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"authorization_code":"auth-code-xyz","code_verifier":"verifier-xyz"}`)
		},
		validTokenHandler(),
	)
	defer h.close()

	creds, err := LoginDevice(context.Background())
	if err != nil {
		t.Fatalf("LoginDevice error: %v", err)
	}
	if creds.Access != "at-device" {
		t.Errorf("Access = %q, want at-device", creds.Access)
	}
	if creds.Refresh != "rt-device" {
		t.Errorf("Refresh = %q, want rt-device", creds.Refresh)
	}
	if pollCount != 3 {
		t.Errorf("poll count = %d, want 3 (2 pending + 1 success)", pollCount)
	}
	if !strings.Contains(h.stderrBuf.String(), "ABCD-1234") {
		t.Errorf("stderr %q does not contain user_code ABCD-1234", h.stderrBuf.String())
	}
	if !strings.Contains(h.stderrBuf.String(), Issuer+"/codex/device") {
		t.Errorf("stderr %q does not contain device URL %s/codex/device", h.stderrBuf.String(), Issuer)
	}
}

// TestLoginDevice_404Retried verifies that HTTP 404 from the poll endpoint is
// treated as transient (retried), not fatal.
func TestLoginDevice_404Retried(t *testing.T) {
	tmpDir := t.TempDir()
	pathMu.Lock()
	origPath := pathFunc
	pathFunc = func() (string, error) { return tmpDir + "/auth.json", nil }
	pathMu.Unlock()
	t.Cleanup(func() {
		pathMu.Lock()
		pathFunc = origPath
		pathMu.Unlock()
	})

	pollCount := 0
	h := newDeviceTestHarness(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"device_auth_id":"DAU_x","user_code":"ZZZZ-9999","interval":"1"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			pollCount++
			if pollCount < 2 {
				w.WriteHeader(http.StatusNotFound) // transient 404
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"authorization_code":"ac2","code_verifier":"cv2"}`)
		},
		validTokenHandler(),
	)
	defer h.close()

	creds, err := LoginDevice(context.Background())
	if err != nil {
		t.Fatalf("LoginDevice error: %v", err)
	}
	if creds.Access != "at-device" {
		t.Errorf("Access = %q, want at-device", creds.Access)
	}
	if pollCount != 2 {
		t.Errorf("poll count = %d, want 2 (1 not-found + 1 success)", pollCount)
	}
}

// TestLoginDevice_FatalPollStatus verifies that a non-200/403/404 poll response
// returns a *DeviceError with Code "device_code_expired".
func TestLoginDevice_FatalPollStatus(t *testing.T) {
	h := newDeviceTestHarness(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"device_auth_id":"DAU_y","user_code":"DEAD-0000","interval":"1"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `internal server error`)
		},
		nil,
	)
	defer h.close()

	_, err := LoginDevice(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 poll response, got nil")
	}
	var de *DeviceError
	if !errors.As(err, &de) {
		t.Fatalf("error type = %T, want *DeviceError; err = %v", err, err)
	}
	if de.Code != "device_code_expired" {
		t.Errorf("DeviceError.Code = %q, want device_code_expired", de.Code)
	}
	if !strings.Contains(de.Message, "500") {
		t.Errorf("DeviceError.Message = %q does not mention status 500", de.Message)
	}
}

// TestLoginDevice_InvalidUserCode verifies that a user_code failing charset
// validation is rejected before printing to stderr.
func TestLoginDevice_InvalidUserCode(t *testing.T) {
	h := newDeviceTestHarness(
		func(w http.ResponseWriter, r *http.Request) {
			// Return a malicious-looking user_code.
			fmt.Fprint(w, `{"device_auth_id":"DAU_z","user_code":"INVALID;rm -rf /","interval":"5"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Error("poll endpoint should not be reached for invalid user_code")
		},
		nil,
	)
	defer h.close()

	_, err := LoginDevice(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid user_code, got nil")
	}
	var de *DeviceError
	if !errors.As(err, &de) {
		t.Fatalf("error type = %T, want *DeviceError; err = %v", err, err)
	}
	if de.Code != "invalid_user_code" {
		t.Errorf("DeviceError.Code = %q, want invalid_user_code", de.Code)
	}
	// Malicious code must not appear in stderr.
	if strings.Contains(h.stderrBuf.String(), "rm -rf") {
		t.Error("malicious user_code fragment appeared in stderr output")
	}
}

// TestLoginDevice_ContextCancelMidPoll verifies that cancelling the caller's
// context during the poll loop returns ctx.Err().
func TestLoginDevice_ContextCancelMidPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	pollCount := 0
	h := newDeviceTestHarness(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"device_auth_id":"DAU_c","user_code":"AAAA-1111","interval":"1"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			pollCount++
			if pollCount == 1 {
				cancel() // cancel during first poll
			}
			w.WriteHeader(http.StatusForbidden)
		},
		nil,
	)
	defer h.close()

	_, err := LoginDevice(ctx)
	if err == nil {
		t.Fatal("expected error after context cancel, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// TestParsePollInterval verifies that parsePollInterval correctly converts
// the server-returned interval string to seconds. The Codex device-auth
// endpoint encodes interval as a JSON string (not a number). Values that
// are zero, negative, absent, or non-numeric fall back to the minimum (5s).
func TestParsePollInterval(t *testing.T) {
	rows := []struct {
		name    string
		input   string
		wantSec int
	}{
		// Documented value: "5" is the canonical server-returned minimum.
		{name: `"5" → 5`, input: "5", wantSec: 5},
		// Higher valid value is passed through unchanged.
		{name: `"10" → 10`, input: "10", wantSec: 10},
		// Empty string (and absent JSON key, which zero-values to "") falls back.
		{name: `"" or absent key → 5`, input: "", wantSec: 5},
		// Non-numeric falls back: strconv.Atoi fails.
		{name: `"abc" → 5 (non-numeric)`, input: "abc", wantSec: 5},
		// Zero is unsafe (poll immediately); does not pass the i > 0 guard.
		{name: `"0" → 5 (zero is unsafe)`, input: "0", wantSec: 5},
		// Negative values are unsafe; fallback.
		{name: `"-1" → 5 (negative)`, input: "-1", wantSec: 5},
	}

	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePollInterval(tt.input)
			if got != tt.wantSec {
				t.Errorf("parsePollInterval(%q) = %d, want %d", tt.input, got, tt.wantSec)
			}
		})
	}
}

// TestLoginDevice_IntervalParsing verifies that a valid numeric interval is
// accepted and the flow completes. Also checks that the device redirect URI is
// passed to the token exchange endpoint.
func TestLoginDevice_IntervalParsing(t *testing.T) {
	tmpDir := t.TempDir()
	pathMu.Lock()
	origPath := pathFunc
	pathFunc = func() (string, error) { return tmpDir + "/auth.json", nil }
	pathMu.Unlock()
	t.Cleanup(func() {
		pathMu.Lock()
		pathFunc = origPath
		pathMu.Unlock()
	})

	h := newDeviceTestHarness(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"device_auth_id":"DAU_i","user_code":"IIII-0000","interval":"1"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err == nil {
				if got := r.FormValue("redirect_uri"); got != DeviceRedirectURI {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"error":"bad_redirect","error_description":"redirect_uri=%s"}`, got)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","id_token":"","expires_in":3600}`)
		},
	)
	defer h.close()

	_, err := LoginDevice(context.Background())
	if err != nil {
		t.Fatalf("LoginDevice: %v", err)
	}
}

// TestLoginDevice_PollBodyFormat verifies that the poll POST sends both
// device_auth_id and user_code in the request body.
func TestLoginDevice_PollBodyFormat(t *testing.T) {
	tmpDir := t.TempDir()
	pathMu.Lock()
	origPath := pathFunc
	pathFunc = func() (string, error) { return tmpDir + "/auth.json", nil }
	pathMu.Unlock()
	t.Cleanup(func() {
		pathMu.Lock()
		pathFunc = origPath
		pathMu.Unlock()
	})

	var capturedBody []byte
	h := newDeviceTestHarness(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"device_auth_id":"DAU_body","user_code":"BBBB-2222","interval":"1"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
		},
		validTokenHandler(),
	)
	defer h.close()

	if _, err := LoginDevice(context.Background()); err != nil {
		t.Fatalf("LoginDevice error: %v", err)
	}

	var m map[string]string
	if err := json.Unmarshal(capturedBody, &m); err != nil {
		t.Fatalf("poll body is not valid JSON: %v; body = %q", err, capturedBody)
	}
	if m["device_auth_id"] != "DAU_body" {
		t.Errorf("poll body device_auth_id = %q, want DAU_body", m["device_auth_id"])
	}
	if m["user_code"] != "BBBB-2222" {
		t.Errorf("poll body user_code = %q, want BBBB-2222", m["user_code"])
	}
}

// TestLoginDevice_CallerContextExpired verifies that an already-expired caller
// context is surfaced as context.DeadlineExceeded (not a *DeviceError).
func TestLoginDevice_CallerContextExpired(t *testing.T) {
	h := newDeviceTestHarness(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"device_auth_id":"DAU_t","user_code":"TTTT-0000","interval":"1"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		},
		nil,
	)
	defer h.close()

	// Context with a deadline already in the past.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := LoginDevice(ctx)
	if err == nil {
		t.Fatal("expected error for expired context, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
}

// TestLoginDevice_15MinCapFires verifies that the internal 15-minute cap
// (devicePollCap) fires and returns *DeviceError{Code:"device_code_expired"}
// even when the caller's context has no deadline.
func TestLoginDevice_15MinCapFires(t *testing.T) {
	origCap := devicePollCap
	devicePollCap = 150 * time.Millisecond // shrink cap for a fast test
	t.Cleanup(func() { devicePollCap = origCap })

	h := newDeviceTestHarness(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"device_auth_id":"DAU_cap","user_code":"CCCC-0001","interval":"1"}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			// Always pending so the cap fires.
			w.WriteHeader(http.StatusForbidden)
		},
		nil,
	)
	defer h.close()

	_, err := LoginDevice(context.Background())
	if err == nil {
		t.Fatal("expected error when poll cap fires, got nil")
	}
	var de *DeviceError
	if !errors.As(err, &de) {
		t.Fatalf("error type = %T, want *DeviceError; err = %v", err, err)
	}
	if de.Code != "device_code_expired" {
		t.Errorf("DeviceError.Code = %q, want device_code_expired", de.Code)
	}
}
