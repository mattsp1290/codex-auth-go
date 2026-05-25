package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPostForm_Success(t *testing.T) {
	fixture := tokenResponse{
		AccessToken:  "at-abc123",
		RefreshToken: "rt-xyz789",
		IDToken:      "it-def456",
		ExpiresIn:    3600,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fixture) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := postForm(context.Background(), srv.URL, url.Values{"grant_type": {"authorization_code"}})
	if err != nil {
		t.Fatalf("postForm error: %v", err)
	}
	if got.AccessToken != fixture.AccessToken {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, fixture.AccessToken)
	}
	if got.RefreshToken != fixture.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, fixture.RefreshToken)
	}
	if got.IDToken != fixture.IDToken {
		t.Errorf("IDToken = %q, want %q", got.IDToken, fixture.IDToken)
	}
	if got.ExpiresIn != fixture.ExpiresIn {
		t.Errorf("ExpiresIn = %d, want %d", got.ExpiresIn, fixture.ExpiresIn)
	}
}

// TestPostForm_InvalidGrant checks that a 400 with error=invalid_grant
// returns a *AuthError inspectable via errors.As.
func TestPostForm_InvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"error":             "invalid_grant",
			"error_description": "The refresh token has expired",
		})
	}))
	defer srv.Close()

	_, err := postForm(context.Background(), srv.URL, url.Values{})
	if err == nil {
		t.Fatal("postForm returned nil error for invalid_grant response")
	}
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("error type = %T, want *AuthError", err)
	}
	if ae.Code != "invalid_grant" {
		t.Errorf("AuthError.Code = %q, want \"invalid_grant\"", ae.Code)
	}
	if ae.Description == "" {
		t.Error("AuthError.Description is empty, want non-empty")
	}
}

// TestPostForm_ServerError checks that a 503 returns a wrapped error
// containing the status code (not an *AuthError).
func TestPostForm_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable")) //nolint:errcheck
	}))
	defer srv.Close()

	_, err := postForm(context.Background(), srv.URL, url.Values{})
	if err == nil {
		t.Fatal("postForm returned nil error for 503 response")
	}
	var ae *AuthError
	if errors.As(err, &ae) {
		t.Errorf("503 response should not yield *AuthError, got Code=%q", ae.Code)
	}
}

// TestPostForm_ContextCancel checks that a cancelled context propagates the
// cancellation error instead of returning a partial response.
func TestPostForm_ContextCancel(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done() // hang until client disconnects
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := postForm(ctx, srv.URL, url.Values{})
		errc <- err
	}()

	<-started
	cancel()
	err := <-errc
	if err == nil {
		t.Fatal("postForm returned nil error after context cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not wrap context.Canceled: %v", err)
	}
}
