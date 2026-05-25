// Tests in this file mutate the global OTel TracerProvider and MUST NOT call
// t.Parallel(). installRecordingProvider restores the original on cleanup.
package codexauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// authDenylist is the set of substrings that MUST NOT appear as span attribute
// VALUES in any codexauth telemetry. Keys may reference these terms
// (e.g. key="authorization") without triggering a violation — only VALUES are
// checked. The list covers the principal secret surfaces of the OAuth protocol:
// bearer token prefixes, form-encoded credential names, and PKCE material.
var authDenylist = []string{
	"Bearer ",        // access token bearer prefix injected into Authorization headers
	"refresh_token=", // form-encoded refresh-token field sent to the token endpoint
	"code=",          // form-encoded authorization code (exchange step)
	"code_verifier=", // PKCE verifier (secret material; only the challenge is sent to auth server)
}

// installRecordingProvider replaces the global OTel TracerProvider with an
// in-memory recording one for the duration of the test and restores the
// original provider on cleanup. The returned SpanRecorder accumulates all spans
// emitted by any code that obtains a tracer via otel.GetTracerProvider() during
// the test.
func installRecordingProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	orig := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(orig) })
	return sr
}

// assertNoDenylistInSpans walks all ended spans and fails if any string-typed
// attribute VALUE contains an entry from authDenylist or the caller-supplied
// fixture-specific secrets. Attribute KEYS are not checked — key names like
// "authorization" are expected and do not constitute a leak.
func assertNoDenylistInSpans(t *testing.T, sr *tracetest.SpanRecorder, secrets ...string) {
	t.Helper()
	checks := append(authDenylist, secrets...)
	for _, span := range sr.Ended() {
		for _, kv := range span.Attributes() {
			val := kv.Value.AsString()
			if val == "" {
				continue // non-string attribute; skip
			}
			for _, entry := range checks {
				if strings.Contains(val, entry) {
					t.Errorf("span %q: attribute key=%q value=%q leaks secret entry %q",
						span.Name(), kv.Key, val, entry)
				}
			}
		}
	}
}

// TestOTelDenylist_RoundTrip is a regression guard asserting that the
// codexTransport's RoundTrip path does not emit OTel span attributes that
// contain OAuth bearer tokens or other credential material.
//
// The test passes trivially today because codexauth emits no spans. It is
// structured so that a future regression — e.g. someone adding
// span.SetAttributes(attribute.String("auth", req.Header.Get("Authorization")))
// — fails loudly.
func TestOTelDenylist_RoundTrip(t *testing.T) {
	const accessToken = "secret-access-token-otel-denylist-test"
	const accountID = "acct-otel-denylist-test"

	sr := installRecordingProvider(t)

	// Use captureTransport as the base so codexTransport.RoundTrip never makes a
	// real network call. codexTransport unconditionally rewrites the destination
	// URL to chatgpt.com/backend-api/codex/responses before calling base, so
	// http.DefaultTransport would reach the real internet — making the test
	// flaky in firewalled CI environments.
	cap := &captureTransport{}
	tr := &codexTransport{
		base: cap,
		now:  time.Now,
		creds: Credentials{
			Access:    accessToken,
			AccountID: accountID,
			Expires:   time.Now().Add(time.Hour).UnixMilli(),
		},
		saveCreds: func(Credentials) error { return nil },
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://api.openai.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close() //nolint:errcheck

	assertNoDenylistInSpans(t, sr, accessToken, accountID)
}

// TestOTelDenylist_Refresh is a regression guard asserting that the token
// refresh path (ensureFresh) does not emit span attributes that contain OAuth
// credential material such as refresh tokens or newly issued access tokens.
func TestOTelDenylist_Refresh(t *testing.T) {
	const oldRefresh = "secret-refresh-token-otel-denylist-test"
	const newAccess = "new-access-token-otel-denylist-test"
	const newRefresh = "new-refresh-token-otel-denylist-test"

	sr := installRecordingProvider(t)

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":3600}`,
			newAccess, newRefresh)
	}))
	t.Cleanup(tokenSrv.Close)
	withTestEndpoint(t, tokenSrv.URL)

	tr := &codexTransport{
		now: time.Now,
		creds: Credentials{
			Access:  "old-access",
			Refresh: oldRefresh,
			Expires: time.Now().Add(-time.Second).UnixMilli(), // expired → triggers refresh
		},
		saveCreds: func(Credentials) error { return nil },
	}

	if err := tr.ensureFresh(context.Background()); err != nil {
		t.Fatalf("ensureFresh: %v", err)
	}

	assertNoDenylistInSpans(t, sr, oldRefresh, newAccess, newRefresh)
}

// TestOTelDenylist_HTTPClient_NotLoggedIn is a regression guard asserting that
// the HTTPClient factory path (ErrNotLoggedIn branch) does not emit span
// attributes containing credential material. This covers the credstore read
// code path.
func TestOTelDenylist_HTTPClient_NotLoggedIn(t *testing.T) {
	sr := installRecordingProvider(t)
	setTestPath(t) // redirect credstore to an empty temp dir → ErrNotLoggedIn

	_, err := HTTPClient(context.Background())
	if err == nil {
		t.Fatal("expected ErrNotLoggedIn with empty credstore, got nil")
	}

	// No fixture-specific secrets to check; denylist alone is sufficient here.
	assertNoDenylistInSpans(t, sr)
}

// TestOTelDenylist_HTTPClient_WithCreds is a regression guard asserting that
// the HTTPClient factory path (success branch, returning a codexTransport-backed
// client) does not emit span attributes containing the stored credential material.
func TestOTelDenylist_HTTPClient_WithCreds(t *testing.T) {
	const storedAccess = "stored-access-token-otel-denylist-test"
	const storedRefresh = "stored-refresh-token-otel-denylist-test"

	sr := installRecordingProvider(t)
	setTestPath(t)

	// Persist credentials so HTTPClient returns a real client.
	if err := Save(AuthFile{OpenAI: &Credentials{
		Access:  storedAccess,
		Refresh: storedRefresh,
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := HTTPClient(context.Background())
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}

	assertNoDenylistInSpans(t, sr, storedAccess, storedRefresh)
}
