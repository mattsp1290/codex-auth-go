// Tests in this package bind OAuthPort on the loopback interface. They MUST
// NOT call t.Parallel() — concurrent tests would race on the port.
package codexauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// testCallbackTimeout is the per-select deadline used across all callback tests.
const testCallbackTimeout = 2 * time.Second

// TestStartCallbackServer_HappyPath verifies that a GET /auth/callback with the
// correct state delivers the code on codeCh and responds 200.
func TestStartCallbackServer_HappyPath(t *testing.T) {
	const testState = "test-state-abc123"
	const testCode = "auth-code-xyz"

	codeCh, errCh, shutdown, err := StartCallbackServer(context.Background(), testState)
	if err != nil {
		t.Fatalf("StartCallbackServer: %v", err)
	}
	t.Cleanup(shutdown)

	url := fmt.Sprintf("http://127.0.0.1:%d%s?code=%s&state=%s", OAuthPort, callbackPath, testCode, testState)
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	select {
	case code := <-codeCh:
		if code != testCode {
			t.Errorf("code = %q, want %q", code, testCode)
		}
	case e := <-errCh:
		t.Fatalf("unexpected errCh: %v", e)
	case <-time.After(testCallbackTimeout):
		t.Fatal("timed out waiting for code")
	}
}

// TestStartCallbackServer_StateMismatch verifies that a callback with the wrong
// state returns 400 and emits ErrStateMismatch on errCh without sending a code,
// and that a subsequent request with the correct state still succeeds (the server
// does not shut itself down on a state mismatch).
func TestStartCallbackServer_StateMismatch(t *testing.T) {
	const rightState = "right-state"
	const wrongState = "wrong-state"
	const badCode = "auth-code-should-not-arrive"
	const goodCode = "auth-code-correct"

	codeCh, errCh, shutdown, err := StartCallbackServer(context.Background(), rightState)
	if err != nil {
		t.Fatalf("StartCallbackServer: %v", err)
	}
	t.Cleanup(shutdown)

	// First request: wrong state → 400 + ErrStateMismatch.
	badURL := fmt.Sprintf("http://127.0.0.1:%d%s?code=%s&state=%s", OAuthPort, callbackPath, badCode, wrongState)
	resp, err := http.Get(badURL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET callback (wrong state): %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}

	select {
	case e := <-errCh:
		if !errors.Is(e, ErrStateMismatch) {
			t.Errorf("errCh = %v, want ErrStateMismatch", e)
		}
	case code := <-codeCh:
		t.Fatalf("unexpected code on codeCh: %q (state mismatch must not forward code)", code)
	case <-time.After(testCallbackTimeout):
		t.Fatal("timed out waiting for ErrStateMismatch")
	}

	// Confirm errCh is fully drained before the replay (regression guard: a
	// future handler must not latch multiple errors from a single bad request).
	select {
	case unexpectedErr := <-errCh:
		t.Fatalf("errCh not drained before replay: %v", unexpectedErr)
	default:
	}

	// Replay with correct state: server must still be up and accept the code.
	goodURL := fmt.Sprintf("http://127.0.0.1:%d%s?code=%s&state=%s", OAuthPort, callbackPath, goodCode, rightState)
	resp2, err := http.Get(goodURL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET callback (correct state replay): %v", err)
	}
	defer resp2.Body.Close() //nolint:errcheck

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("replay status = %d, want 200; body: %s", resp2.StatusCode, body)
	}

	select {
	case code := <-codeCh:
		if code != goodCode {
			t.Errorf("replay code = %q, want %q", code, goodCode)
		}
	case e := <-errCh:
		t.Fatalf("unexpected errCh on replay: %v", e)
	case <-time.After(testCallbackTimeout):
		t.Fatal("timed out waiting for replay code")
	}

	// Confirm no error was emitted alongside the successful replay.
	select {
	case unexpectedErr := <-errCh:
		t.Fatalf("unexpected errCh after successful replay: %v", unexpectedErr)
	default:
	}
}

// TestStartCallbackServer_PortInUse verifies that StartCallbackServer returns
// *PortInUseError when 127.0.0.1:OAuthPort is already bound.
func TestStartCallbackServer_PortInUse(t *testing.T) {
	// Pre-bind the IPv4 loopback port.
	blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", OAuthPort))
	if err != nil {
		t.Skipf("cannot pre-bind 127.0.0.1:%d: %v", OAuthPort, err)
	}
	t.Cleanup(func() { blocker.Close() }) //nolint:errcheck

	_, _, _, callbackErr := StartCallbackServer(context.Background(), "any-state")
	if callbackErr == nil {
		t.Fatal("StartCallbackServer returned nil error with port already bound")
	}
	var pie *PortInUseError
	if !errors.As(callbackErr, &pie) {
		t.Errorf("error = %T (%v), want *PortInUseError", callbackErr, callbackErr)
	}
}

// TestStartCallbackServer_IPv6 verifies that both the IPv6 ([::1]) and IPv4
// (127.0.0.1) loopback paths reach the same server instance. Skipped when IPv6
// loopback is not available on the host.
func TestStartCallbackServer_IPv6(t *testing.T) {
	// Probe for IPv6 availability before starting the server.
	probe, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback not available: %v", err)
	}
	probe.Close() //nolint:errcheck

	const testState = "ipv6-state-12345"
	const ipv6Code = "ipv6-code-xyz"
	const ipv4Code = "ipv4-code-abc"

	codeCh, errCh, shutdown, err := StartCallbackServer(context.Background(), testState)
	if err != nil {
		t.Fatalf("StartCallbackServer: %v", err)
	}
	t.Cleanup(shutdown)

	// Verify IPv6 path delivers the code.
	url6 := fmt.Sprintf("http://[::1]:%d%s?code=%s&state=%s", OAuthPort, callbackPath, ipv6Code, testState)
	resp6, err := http.Get(url6) //nolint:noctx
	if err != nil {
		t.Skipf("IPv6 request failed (loopback not fully configured): %v", err)
	}
	defer resp6.Body.Close() //nolint:errcheck

	if resp6.StatusCode != http.StatusOK {
		t.Errorf("IPv6 status = %d, want 200", resp6.StatusCode)
	}

	select {
	case code := <-codeCh:
		if code != ipv6Code {
			t.Errorf("IPv6 code = %q, want %q", code, ipv6Code)
		}
	case e := <-errCh:
		t.Fatalf("unexpected errCh on IPv6 request: %v", e)
	case <-time.After(testCallbackTimeout):
		t.Fatal("timed out waiting for IPv6 code")
	}

	// Verify IPv4 path also reaches the same server instance (codeCh already
	// drained by the IPv6 select, so the cap-1 buffer accepts the next send).
	url4 := fmt.Sprintf("http://127.0.0.1:%d%s?code=%s&state=%s", OAuthPort, callbackPath, ipv4Code, testState)
	resp4, err := http.Get(url4) //nolint:noctx
	if err != nil {
		t.Fatalf("IPv4 request failed: %v", err)
	}
	defer resp4.Body.Close() //nolint:errcheck

	if resp4.StatusCode != http.StatusOK {
		t.Errorf("IPv4 status = %d, want 200", resp4.StatusCode)
	}

	select {
	case code := <-codeCh:
		if code != ipv4Code {
			t.Errorf("IPv4 code = %q, want %q", code, ipv4Code)
		}
	case e := <-errCh:
		t.Fatalf("unexpected errCh on IPv4 request: %v", e)
	case <-time.After(testCallbackTimeout):
		t.Fatal("timed out waiting for IPv4 code")
	}
}

// TestStartCallbackServer_GracefulShutdown verifies that cancelling the context
// shuts down the server within 5 s and that subsequent requests are rejected.
func TestStartCallbackServer_GracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, _, shutdown, err := StartCallbackServer(ctx, "shutdown-state")
	if err != nil {
		t.Fatalf("StartCallbackServer: %v", err)
	}

	// Verify the server is up by making a successful request.
	url := fmt.Sprintf("http://127.0.0.1:%d%s?code=c&state=shutdown-state", OAuthPort, callbackPath)
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		t.Fatalf("GET before shutdown: %v", err)
	}
	resp.Body.Close() //nolint:errcheck

	// Cancel context → server should shut down.
	cancel()

	// Wait for the port to be released (up to 5 s as per spec).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probe, probeErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", OAuthPort))
		if probeErr == nil {
			probe.Close() //nolint:errcheck
			return        // port released → shutdown complete
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(shutdown)
	t.Errorf("port %d not released within 5 s after context cancel", OAuthPort)
}

// TestStartCallbackServer_OAuthError verifies that a redirect carrying
// ?error=access_denied with the correct state returns 400, emits a wrapped
// ErrAuthorizationDenied on errCh, and does NOT send a code on codeCh.
func TestStartCallbackServer_OAuthError(t *testing.T) {
	const testState = "oauth-error-state"

	codeCh, errCh, shutdown, err := StartCallbackServer(context.Background(), testState)
	if err != nil {
		t.Fatalf("StartCallbackServer: %v", err)
	}
	t.Cleanup(shutdown)

	url := fmt.Sprintf("http://127.0.0.1:%d%s?state=%s&error=access_denied&error_description=User+denied",
		OAuthPort, callbackPath, testState)
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}

	select {
	case e := <-errCh:
		if !errors.Is(e, ErrAuthorizationDenied) {
			t.Errorf("errCh = %v, want error wrapping ErrAuthorizationDenied", e)
		}
	case code := <-codeCh:
		t.Fatalf("unexpected code %q on codeCh (OAuth error must not forward code)", code)
	case <-time.After(testCallbackTimeout):
		t.Fatal("timed out waiting for ErrAuthorizationDenied")
	}
}
