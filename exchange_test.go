package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// withTestEndpoint replaces tokenEndpointURL with srvURL for the duration of
// the test and restores it in a deferred cleanup.
// NOTE: Mutates a package-level variable. Tests using this helper must NOT
// call t.Parallel(). Refactor to a client struct when parallel tests are needed.
func withTestEndpoint(t *testing.T, srvURL string) {
	t.Helper()
	old := tokenEndpointURL
	tokenEndpointURL = srvURL
	t.Cleanup(func() { tokenEndpointURL = old })
}

// testTokenServer starts an httptest.Server that returns the given tokenResponse
// as JSON and asserts the expected form fields are present in the request.
// Fields in wantForm must appear with the exact values given; extra fields are allowed.
func testTokenServer(t *testing.T, resp tokenResponse, wantForm url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm error: %v", err)
		}
		for k, want := range wantForm {
			if got := r.PostFormValue(k); got != want[0] {
				t.Errorf("form field %q = %q, want %q", k, got, want[0])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
}

// TestExchangeCode_Success checks the happy path: a valid authorization-code
// response is decoded and the Expires timestamp is within ±10 s of expected.
func TestExchangeCode_Success(t *testing.T) {
	fixture := tokenResponse{
		AccessToken:  "at-exchange",
		RefreshToken: "rt-exchange",
		IDToken:      "it-exchange",
		ExpiresIn:    7200,
	}
	wantForm := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {"mycode123"},
		"code_verifier": {"myverifier456"},
		"redirect_uri":  {RedirectURI},
	}
	srv := testTokenServer(t, fixture, wantForm)
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	before := time.Now().UnixMilli()
	creds, err := ExchangeCode(context.Background(), "mycode123", "myverifier456", RedirectURI)
	after := time.Now().UnixMilli()

	if err != nil {
		t.Fatalf("ExchangeCode error: %v", err)
	}
	if creds.Access != fixture.AccessToken {
		t.Errorf("Access = %q, want %q", creds.Access, fixture.AccessToken)
	}
	if creds.Refresh != fixture.RefreshToken {
		t.Errorf("Refresh = %q, want %q", creds.Refresh, fixture.RefreshToken)
	}
	if creds.IDToken != fixture.IDToken {
		t.Errorf("IDToken = %q, want %q", creds.IDToken, fixture.IDToken)
	}
	if creds.AccountID != "" {
		t.Errorf("AccountID = %q, want \"\" (JWT bead fills this)", creds.AccountID)
	}
	wantExpiresMin := before + int64(fixture.ExpiresIn)*1000
	wantExpiresMax := after + int64(fixture.ExpiresIn)*1000 + 50 // 50 ms grace for CI clock jitter
	if creds.Expires < wantExpiresMin || creds.Expires > wantExpiresMax {
		t.Errorf("Expires = %d, want [%d, %d]", creds.Expires, wantExpiresMin, wantExpiresMax)
	}
}

// TestRefreshToken_Success checks the happy path: a refresh response is decoded
// and the rotated refresh token is preserved in Credentials.Refresh.
func TestRefreshToken_Success(t *testing.T) {
	fixture := tokenResponse{
		AccessToken:  "at-refreshed",
		RefreshToken: "rt-rotated",
		IDToken:      "it-refreshed",
		ExpiresIn:    3600,
	}
	wantForm := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {"old-refresh-token"},
		"scope":         {Scope},
	}
	srv := testTokenServer(t, fixture, wantForm)
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	creds, err := RefreshToken(context.Background(), "old-refresh-token")
	if err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if creds.Access != fixture.AccessToken {
		t.Errorf("Access = %q, want %q", creds.Access, fixture.AccessToken)
	}
	// The rotated refresh token must be preserved.
	if creds.Refresh != fixture.RefreshToken {
		t.Errorf("Refresh = %q, want %q (rotated token must be preserved)", creds.Refresh, fixture.RefreshToken)
	}
}

// TestExchangeCode_InvalidGrant checks that an OAuth invalid_grant response
// bubbles up as a *AuthError with Code="invalid_grant".
func TestExchangeCode_InvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"error":             "invalid_grant",
			"error_description": "The authorization code is invalid",
		})
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	_, err := ExchangeCode(context.Background(), "bad-code", "verifier", RedirectURI)
	if err == nil {
		t.Fatal("ExchangeCode returned nil error for invalid_grant response")
	}
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("error type = %T, want *AuthError", err)
	}
	if ae.Code != "invalid_grant" {
		t.Errorf("AuthError.Code = %q, want \"invalid_grant\"", ae.Code)
	}
}

// TestRefreshToken_InvalidGrant checks the same propagation for RefreshToken.
func TestRefreshToken_InvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"error": "invalid_grant",
		})
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	_, err := RefreshToken(context.Background(), "expired-refresh")
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Code != "invalid_grant" {
		t.Errorf("RefreshToken with invalid_grant: error = %v, want *AuthError{Code:\"invalid_grant\"}", err)
	}
}

// TestRefreshToken_PreservesRefreshWhenServerOmits checks that when the server
// does not include a refresh_token in the response (RFC 6749 §6 allows this),
// the caller's original refresh token is preserved in Credentials.Refresh.
func TestRefreshToken_PreservesRefreshWhenServerOmits(t *testing.T) {
	srv := testTokenServer(t, tokenResponse{
		AccessToken: "at-no-rotation",
		IDToken:     "it-no-rotation",
		ExpiresIn:   3600,
		// RefreshToken deliberately omitted — server chose not to rotate.
	}, nil)
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	creds, err := RefreshToken(context.Background(), "original-refresh-token")
	if err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if creds.Refresh != "original-refresh-token" {
		t.Errorf("Refresh = %q, want %q (server omitted rotation; original must be preserved)",
			creds.Refresh, "original-refresh-token")
	}
}

// TestCredentials_String_Redacted verifies that fmt.Sprintf("%v", creds) never
// emits token values — String() is the last common leakage path not covered by
// json:"-" or LogValue.
func TestCredentials_String_Redacted(t *testing.T) {
	c := Credentials{
		Access:    "secret-access-token",
		Refresh:   "secret-refresh-token",
		IDToken:   "secret-id-token",
		Expires:   9999999999000,
		AccountID: "user-123",
	}
	for _, secret := range []string{"secret-access-token", "secret-refresh-token", "secret-id-token"} {
		if strings.Contains(fmt.Sprintf("%v", c), secret) {
			t.Errorf("fmt.Sprintf(%%v, Credentials) contains secret %q — token leakage", secret)
		}
		if strings.Contains(fmt.Sprintf("%+v", c), secret) {
			t.Errorf("fmt.Sprintf(%%+v, Credentials) contains secret %q — token leakage", secret)
		}
	}
}

// TestCredentials_JSONMarshal_Redacted verifies that json.Marshal(Credentials{...})
// never emits token values. All fields carry json:"-" so the result is always "{}".
func TestCredentials_JSONMarshal_Redacted(t *testing.T) {
	c := Credentials{
		Access:    "secret-access-token",
		Refresh:   "secret-refresh-token",
		IDToken:   "secret-id-token",
		Expires:   9999999999000,
		AccountID: "user-123",
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	for _, secret := range []string{"secret-access-token", "secret-refresh-token", "secret-id-token"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("json.Marshal(Credentials) contains secret %q — token leakage", secret)
		}
	}
	// All fields are json:"-" so the marshaled form must be the empty object.
	if string(out) != "{}" {
		t.Errorf("json.Marshal(Credentials) = %s, want {}", out)
	}
}

// TestCredentials_LogValue_Redacted verifies that slog logging of a Credentials
// value never emits the raw token strings.
func TestCredentials_LogValue_Redacted(t *testing.T) {
	c := Credentials{
		Access:    "secret-access-token",
		Refresh:   "secret-refresh-token",
		IDToken:   "secret-id-token",
		Expires:   9999999999000,
		AccountID: "user-123",
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Info("credentials", "c", c)

	logged := buf.String()
	for _, secret := range []string{"secret-access-token", "secret-refresh-token", "secret-id-token"} {
		if strings.Contains(logged, secret) {
			t.Errorf("slog output contains secret %q — token leakage; logged: %s", secret, logged)
		}
	}
	// Verify that structural info (hasAccess, accountID) IS present.
	if !strings.Contains(logged, "hasAccess=true") {
		t.Errorf("slog output missing hasAccess=true; logged: %s", logged)
	}
	if !strings.Contains(logged, "accountID=user-123") {
		t.Errorf("slog output missing accountID=user-123; logged: %s", logged)
	}
}

// TestExchangeCode_ExpiresInDefault checks that a zero ExpiresIn defaults to
// 3600 s (1 hour), matching the OpenAI convention documented in the Day-1 probe.
func TestExchangeCode_ExpiresInDefault(t *testing.T) {
	srv := testTokenServer(t, tokenResponse{
		AccessToken:  "at-default-exp",
		RefreshToken: "rt-default-exp",
		IDToken:      "it-default-exp",
		ExpiresIn:    0, // server omitted or sent 0
	}, nil)
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	before := time.Now().UnixMilli()
	creds, err := ExchangeCode(context.Background(), "code", "verifier", RedirectURI)
	after := time.Now().UnixMilli()
	if err != nil {
		t.Fatalf("ExchangeCode error: %v", err)
	}
	// Expires should default to now + 3600 s.
	wantMin := before + 3600*1000
	wantMax := after + 3600*1000 + 50 // 50 ms grace for CI clock jitter
	if creds.Expires < wantMin || creds.Expires > wantMax {
		t.Errorf("Expires = %d, want [%d, %d] (3600 s default)", creds.Expires, wantMin, wantMax)
	}
}
