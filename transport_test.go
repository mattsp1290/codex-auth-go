package codexauth

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// freshTokenJSON is a minimal valid token-endpoint response for refresh calls.
const freshTokenJSON = `{"access_token":"newtoken","refresh_token":"newrefresh","expires_in":3600}`

// expiredToken returns a Credentials set that is already expired (Expires 1s in the past).
func expiredToken() Credentials {
	return Credentials{
		Access:  "oldtoken",
		Refresh: "oldrefresh",
		Expires: time.Now().Add(-time.Second).UnixMilli(),
	}
}

// newRefreshServer starts an httptest.Server that counts requests and returns a
// valid token JSON body. The caller is responsible for closing the server.
func newRefreshServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		handler(w, r)
	}))
	return srv, &count
}

// TestEnsureFresh_Singleflight verifies that 10 concurrent goroutines racing on
// an expired token collapse into exactly one token-endpoint HTTP call.
func TestEnsureFresh_Singleflight(t *testing.T) {
	srv, callCount := newRefreshServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Pause long enough for all goroutines to reach sf.Do before this returns.
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, freshTokenJSON)
	})
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	tr := &codexTransport{
		now:       time.Now,
		creds:     expiredToken(),
		saveCreds: func(Credentials) error { return nil },
	}

	const N = 10
	// Release all goroutines simultaneously to maximize contention on sf.Do.
	ready := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, N)
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			<-ready
			errs[i] = tr.ensureFresh(context.Background())
		}()
	}
	close(ready)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, e)
		}
	}
	if got := atomic.LoadInt32(callCount); got != 1 {
		t.Errorf("token-endpoint call count = %d, want 1 (singleflight coalesce failed)", got)
	}

	tr.mu.RLock()
	access := tr.creds.Access
	tr.mu.RUnlock()
	if access != "newtoken" {
		t.Errorf("in-memory creds.Access = %q after refresh, want newtoken", access)
	}
}

// TestEnsureFresh_ClockSkew verifies the 60-second refresh-skew margin.
// Tokens expiring in > 60 s are considered valid; tokens expiring in ≤ 60 s trigger a refresh.
func TestEnsureFresh_ClockSkew(t *testing.T) {
	srv, callCount := newRefreshServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, freshTokenJSON)
	})
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	fixedNow := time.Now()

	// Case 1: expires in 61 s → now+60 s < expires → still valid → no refresh.
	tr1 := &codexTransport{
		now: func() time.Time { return fixedNow },
		creds: Credentials{
			Access:  "tok",
			Refresh: "ref",
			Expires: fixedNow.Add(61 * time.Second).UnixMilli(),
		},
		saveCreds: func(Credentials) error { return nil },
	}
	if err := tr1.ensureFresh(context.Background()); err != nil {
		t.Fatalf("case1 (61s): unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(callCount); n != 0 {
		t.Errorf("case1 (61s): HTTP calls = %d, want 0 (token still valid)", n)
	}

	// Case 2: expires in exactly 60 s → now+60 s == expires → NOT valid → refresh fires.
	tr2 := &codexTransport{
		now: func() time.Time { return fixedNow },
		creds: Credentials{
			Access:  "tok",
			Refresh: "ref",
			Expires: fixedNow.Add(60 * time.Second).UnixMilli(),
		},
		saveCreds: func(Credentials) error { return nil },
	}
	if err := tr2.ensureFresh(context.Background()); err != nil {
		t.Fatalf("case2 (60s): unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(callCount); n != 1 {
		t.Errorf("case2 (60s): HTTP calls = %d, want 1 (at margin boundary)", n)
	}
}

// TestEnsureFresh_DetachedContext verifies that a cancelled caller context has
// no effect on an in-flight refresh. The refresh runs under a detached
// context.Background() timeout, so the disk write must complete even when the
// caller has already cancelled its own context.
func TestEnsureFresh_DetachedContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The request context here is the detached rctx (context.Background()+timeout),
		// not the caller's ctx — so this handler must not observe a cancellation.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, freshTokenJSON)
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	var saved Credentials
	tr := &codexTransport{
		now:   time.Now,
		creds: expiredToken(),
		saveCreds: func(c Credentials) error {
			saved = c
			return nil
		},
	}

	// Pre-cancel the caller context. ensureFresh discards it (_ context.Context),
	// so this must have no effect on the outcome.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := tr.ensureFresh(ctx); err != nil {
		t.Fatalf("pre-cancelled caller ctx caused error: %v", err)
	}
	if saved.Access != "newtoken" {
		t.Errorf("saveCreds not called or wrong token: got %q, want newtoken", saved.Access)
	}
}

// TestEnsureFresh_CallerCancelMidRefresh verifies that cancelling the caller's
// context while a refresh HTTP call is already in flight has no effect on the
// outcome. ensureFresh uses a detached context.Background() timeout for the
// HTTP call, so a mid-flight caller cancel cannot abort the refresh or the
// subsequent disk write.
//
// A channel-gated handler guarantees deterministic ordering: the caller context
// is cancelled only after the server confirms the request is in flight, then the
// server is released to respond.
func TestEnsureFresh_CallerCancelMidRefresh(t *testing.T) {
	release := make(chan struct{})
	reqStarted := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reqStarted) // signal: refresh HTTP call is now in flight
		<-release         // block until the test explicitly releases us

		// Direct assertion: the server request context must not be cancelled.
		// If ensureFresh propagated the caller's context instead of using a
		// detached one, the cancelled caller context would close the TCP
		// connection and cancel r.Context() here.
		if err := r.Context().Err(); err != nil {
			t.Errorf("server request context cancelled (caller ctx leaked into refresh): %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, freshTokenJSON)
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)
	setTestPath(t)

	tr := &codexTransport{
		now:   time.Now,
		creds: expiredToken(),
		saveCreds: func(c Credentials) error {
			return Save(AuthFile{OpenAI: &c})
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- tr.ensureFresh(ctx) }()

	<-reqStarted   // wait until the refresh call is in flight
	cancel()       // cancel the caller context mid-flight
	close(release) // let the server respond

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ensureFresh did not return within 5s — possible deadlock")
	}

	if err != nil {
		t.Fatalf("ensureFresh with mid-flight caller cancel returned error: %v", err)
	}

	// Disk write must have completed despite the caller's context being cancelled.
	af, loadErr := Load()
	if loadErr != nil {
		t.Fatalf("Load after refresh: %v", loadErr)
	}
	if af.OpenAI == nil {
		t.Fatal("disk state has nil OpenAI after refresh")
	}
	if af.OpenAI.Access != "newtoken" {
		t.Errorf("disk creds.Access = %q, want newtoken", af.OpenAI.Access)
	}

	// In-memory creds must also reflect the refreshed token (saveCreds → t.creds = fresh).
	tr.mu.RLock()
	access := tr.creds.Access
	tr.mu.RUnlock()
	if access != "newtoken" {
		t.Errorf("in-memory creds.Access = %q after refresh, want newtoken", access)
	}
}

// TestEnsureFresh_InvalidGrant verifies that when the token endpoint returns
// invalid_grant: wipeCreds is called, in-memory creds are zeroed, and the
// returned error carries *AuthError{Code:"invalid_grant"} in the chain.
func TestEnsureFresh_InvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"token revoked"}`)
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	var wipeCalled bool
	tr := &codexTransport{
		now:       time.Now,
		creds:     expiredToken(),
		saveCreds: func(Credentials) error { return nil },
		wipeCreds: func() error {
			wipeCalled = true
			return nil
		},
	}

	err := tr.ensureFresh(context.Background())
	if err == nil {
		t.Fatal("expected error on invalid_grant, got nil")
	}
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AuthError in error chain, got %T: %v", err, err)
	}
	if ae.Code != "invalid_grant" {
		t.Errorf("AuthError.Code = %q, want invalid_grant", ae.Code)
	}
	if !wipeCalled {
		t.Error("wipeCreds was not called on invalid_grant")
	}

	tr.mu.RLock()
	creds := tr.creds
	tr.mu.RUnlock()
	if creds.Access != "" || creds.Refresh != "" {
		t.Errorf("in-memory creds not zeroed after invalid_grant: Access=%q Refresh=%q",
			creds.Access, creds.Refresh)
	}
}

func TestEnsureFresh_InvalidGrantLogsWipeBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"token revoked"}`)
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	var logs bytes.Buffer
	tr := &codexTransport{
		now:       time.Now,
		creds:     expiredToken(),
		saveCreds: func(Credentials) error { return nil },
		wipeCreds: func() error { return nil },
		logger:    slog.New(slog.NewTextHandler(&logs, nil)),
	}

	_ = tr.ensureFresh(context.Background())

	got := logs.String()
	for _, want := range []string{
		"branch_id=wiped_creds",
		"has_creds_after=false",
		"error=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q missing %q", got, want)
		}
	}
	if strings.Contains(got, expiredToken().Refresh) || strings.Contains(got, expiredToken().Access) {
		t.Fatalf("log contains credential material: %q", got)
	}
}

// TestEnsureFresh_InvalidGrant_MultiProcessRecovery verifies that when
// invalid_grant fires but loadCreds returns creds with a rotated refresh token
// (another process already refreshed), the in-memory creds adopt the disk state
// and wipeCreds is NOT called — regardless of the skew margin on the loaded creds.
func TestEnsureFresh_InvalidGrant_MultiProcessRecovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"token revoked"}`)
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	// Disk creds from another process: fresh access token, rotated refresh token.
	// Expiry is only 50s out — isValid would reject these under the 60s margin,
	// but the recovery path must adopt them anyway (rotated refresh token proves they're fresh).
	diskCreds := Credentials{
		Access:    "disktoken",
		Refresh:   "rotated-refresh", // different from expiredToken().Refresh ("oldrefresh")
		AccountID: "acc-123",
		Expires:   time.Now().Add(50 * time.Second).UnixMilli(),
	}

	var wipeCalled bool
	tr := &codexTransport{
		now:       time.Now,
		creds:     expiredToken(),
		saveCreds: func(Credentials) error { return nil },
		wipeCreds: func() error {
			wipeCalled = true
			return nil
		},
		loadCreds: func() (Credentials, error) { return diskCreds, nil },
	}

	if err := tr.ensureFresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wipeCalled {
		t.Error("wipeCreds should not be called when disk has a rotated refresh token")
	}

	tr.mu.RLock()
	creds := tr.creds
	tr.mu.RUnlock()
	if creds.Access != "disktoken" {
		t.Errorf("in-memory creds.Access = %q, want disktoken (adopted from disk)", creds.Access)
	}
	if creds.AccountID != "acc-123" {
		t.Errorf("in-memory creds.AccountID = %q, want acc-123", creds.AccountID)
	}
}

func TestEnsureFresh_InvalidGrantLogsRotationRaceBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"token revoked"}`)
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	var logs bytes.Buffer
	tr := &codexTransport{
		now:       time.Now,
		creds:     expiredToken(),
		saveCreds: func(Credentials) error { return nil },
		wipeCreds: func() error { return nil },
		loadCreds: func() (Credentials, error) {
			return Credentials{
				Access:  "disk-access",
				Refresh: "disk-refresh",
				Expires: time.Now().Add(time.Hour).UnixMilli(),
			}, nil
		},
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}

	if err := tr.ensureFresh(context.Background()); err != nil {
		t.Fatalf("ensureFresh: %v", err)
	}

	got := logs.String()
	for _, want := range []string{
		"branch_id=rotation_race",
		"has_creds_after=true",
		"error=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "disk-refresh") || strings.Contains(got, "disk-access") {
		t.Fatalf("log contains credential material: %q", got)
	}
}

// TestEnsureFresh_PreservesAccountID verifies that AccountID is carried forward
// from old in-memory creds to the refreshed token set. Refresh responses omit
// AccountID; losing it would break the ChatGPT-Account-Id header in RoundTrip.
func TestEnsureFresh_PreservesAccountID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, freshTokenJSON) // no account_id in this response
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	var persistedAccountID string
	tr := &codexTransport{
		now: time.Now,
		creds: Credentials{
			Access:    "old",
			Refresh:   "oldrefresh",
			AccountID: "acct-xyz",
			Expires:   time.Now().Add(-time.Second).UnixMilli(),
		},
		saveCreds: func(c Credentials) error {
			persistedAccountID = c.AccountID
			return nil
		},
	}

	if err := tr.ensureFresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tr.mu.RLock()
	got := tr.creds.AccountID
	tr.mu.RUnlock()
	if got != "acct-xyz" {
		t.Errorf("in-memory AccountID = %q, want acct-xyz", got)
	}
	if persistedAccountID != "acct-xyz" {
		t.Errorf("persisted AccountID = %q, want acct-xyz", persistedAccountID)
	}
}

// TestEnsureFresh_SaveCredsError verifies that a saveCreds failure is returned
// to the caller and does NOT leave a freshly-fetched token in memory (the
// on-disk and in-memory states must remain consistent).
func TestEnsureFresh_SaveCredsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, freshTokenJSON)
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	saveErr := errors.New("disk full")
	tr := &codexTransport{
		now:       time.Now,
		creds:     expiredToken(),
		saveCreds: func(Credentials) error { return saveErr },
	}

	err := tr.ensureFresh(context.Background())
	if err == nil {
		t.Fatal("expected error from saveCreds, got nil")
	}
	if !errors.Is(err, saveErr) {
		t.Errorf("expected saveErr in error chain, got: %v", err)
	}

	tr.mu.RLock()
	creds := tr.creds
	tr.mu.RUnlock()
	if creds.Access != "oldtoken" {
		t.Errorf("in-memory creds updated despite saveCreds error: Access = %q, want oldtoken", creds.Access)
	}
}

// ── RoundTrip tests ──────────────────────────────────────────────────────────

// captureTransport is a mock http.RoundTripper that records every request it
// receives and returns a canned 200 response with no body.
type captureTransport struct {
	mu  sync.Mutex
	got []*http.Request
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Deep-copy the URL so later mutations don't retroactively change the record.
	u := *req.URL
	reqCopy := *req
	reqCopy.URL = &u
	// Snapshot headers.
	reqCopy.Header = req.Header.Clone()
	c.mu.Lock()
	c.got = append(c.got, &reqCopy)
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       http.NoBody,
	}, nil
}

func (c *captureTransport) last() *http.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) == 0 {
		return nil
	}
	return c.got[len(c.got)-1]
}

// validTransport returns a codexTransport with already-valid credentials (no
// refresh needed) and the given base RoundTripper.
func validTransport(base http.RoundTripper) *codexTransport {
	return &codexTransport{
		base: base,
		now:  time.Now,
		creds: Credentials{
			Access:    "my-access-token",
			Refresh:   "my-refresh-token",
			AccountID: "acct-001",
			Expires:   time.Now().Add(time.Hour).UnixMilli(),
		},
		saveCreds: func(Credentials) error { return nil },
	}
}

// TestRoundTrip_URLRewrite verifies that an arbitrary OpenAI endpoint is
// rewritten to the Codex Responses-API URL. The original query string is
// preserved.
func TestRoundTrip_URLRewrite(t *testing.T) {
	cap := &captureTransport{}
	tr := validTransport(cap)

	req, _ := http.NewRequest(http.MethodPost,
		"https://api.openai.com/v1/chat/completions?stream=true", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	got := cap.last()
	if got == nil {
		t.Fatal("base RoundTripper was not called")
	}
	want, _ := url.Parse("https://chatgpt.com/backend-api/codex/responses?stream=true")
	if got.URL.String() != want.String() {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
}

// TestRoundTrip_AuthorizationHeader verifies that the dummy API key placed by
// the SDK is replaced with the real OAuth bearer token.
func TestRoundTrip_AuthorizationHeader(t *testing.T) {
	cap := &captureTransport{}
	tr := validTransport(cap)

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer codex-oauth-dummy")

	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	got := cap.last()
	wantAuth := "Bearer my-access-token"
	if a := got.Header.Get("Authorization"); a != wantAuth {
		t.Errorf("Authorization = %q, want %q", a, wantAuth)
	}
	// Dummy key must not appear on the wire.
	if a := got.Header.Get("Authorization"); a == "Bearer codex-oauth-dummy" {
		t.Error("dummy API key leaked onto the wire")
	}
}

// TestRoundTrip_HostHeaders verifies that both clone.Host and the Host header
// are set to chatgpt.com so that proxies and the target server agree on the
// intended host.
func TestRoundTrip_HostHeaders(t *testing.T) {
	cap := &captureTransport{}
	tr := validTransport(cap)

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	got := cap.last()
	if got.Host != "chatgpt.com" {
		t.Errorf("Request.Host = %q, want chatgpt.com", got.Host)
	}
	// Note: Go's net/http strips Header["Host"] for outgoing client requests
	// and derives the wire host from Request.Host instead. We assert only Host.
}

// TestRoundTrip_CallerRequestImmutable verifies that RoundTrip never mutates
// the caller's *http.Request — the URL, headers, and Host must be byte-equal
// before and after the call.
func TestRoundTrip_CallerRequestImmutable(t *testing.T) {
	cap := &captureTransport{}
	tr := validTransport(cap)

	origURL := "https://api.openai.com/v1/chat/completions"
	req, _ := http.NewRequest(http.MethodPost, origURL, nil)
	req.Header.Set("Authorization", "Bearer codex-oauth-dummy")
	origHost := req.Host

	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	if req.URL.String() != origURL {
		t.Errorf("caller URL mutated: got %q, want %q", req.URL, origURL)
	}
	if req.Header.Get("Authorization") != "Bearer codex-oauth-dummy" {
		t.Errorf("caller Authorization mutated: got %q", req.Header.Get("Authorization"))
	}
	if req.Host != origHost {
		t.Errorf("caller Host mutated: got %q, want %q", req.Host, origHost)
	}
}

// TestRoundTrip_EnsureFreshError verifies that RoundTrip returns an error (and does
// not call the base RoundTripper) when ensureFresh fails — e.g. invalid_grant.
func TestRoundTrip_EnsureFreshError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"token revoked"}`)
	}))
	defer srv.Close()
	withTestEndpoint(t, srv.URL)

	rec := &captureTransport{}
	tr := &codexTransport{
		base:      rec,
		now:       time.Now,
		creds:     expiredToken(),
		saveCreds: func(Credentials) error { return nil },
	}

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	_, err := tr.RoundTrip(req)

	if err == nil {
		t.Fatal("expected error from RoundTrip when ensureFresh fails, got nil")
	}
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Code != "invalid_grant" {
		t.Errorf("expected invalid_grant AuthError, got: %v", err)
	}
	rec.mu.Lock()
	calls := len(rec.got)
	rec.mu.Unlock()
	if calls != 0 {
		t.Errorf("base RoundTripper called %d times, want 0", calls)
	}
}

// TestRoundTrip_Idempotent verifies that calling RoundTrip twice with the same
// *http.Request works correctly. Both calls must rewrite the URL and inject the
// current bearer token.
func TestRoundTrip_Idempotent(t *testing.T) {
	cap := &captureTransport{}
	tr := validTransport(cap)

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)

	for call := range 2 {
		if _, err := tr.RoundTrip(req); err != nil {
			t.Fatalf("call %d: RoundTrip error: %v", call+1, err)
		}
	}

	cap.mu.Lock()
	requests := cap.got
	cap.mu.Unlock()

	if len(requests) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(requests))
	}
	for i, r := range requests {
		if r.URL.Host != "chatgpt.com" {
			t.Errorf("call %d: URL.Host = %q, want chatgpt.com", i+1, r.URL.Host)
		}
		if a := r.Header.Get("Authorization"); a != "Bearer my-access-token" {
			t.Errorf("call %d: Authorization = %q, want Bearer my-access-token", i+1, a)
		}
	}
}

// TestRoundTrip_Idempotent_WithBody verifies that calling RoundTrip twice with
// the same *http.Request that carries a body works correctly. For each call the
// body must arrive intact at the base RoundTripper, the caller's *http.Request
// must not be mutated between calls, and the caller's original body reader must
// remain readable after both calls (i.e. GetBody is used for clones, not req.Body).
func TestRoundTrip_Idempotent_WithBody(t *testing.T) {
	const bodyText = `{"model":"codex","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`

	cap := &captureTransport{}
	tr := validTransport(cap)

	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", strings.NewReader(bodyText))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	// Set a sentinel Authorization header so the mutation assertion proves the
	// clone's overwrite did not bleed back to the caller's request.
	req.Header.Set("Authorization", "Bearer caller-dummy-key")

	// Capture caller's original state before any RoundTrip call.
	origHost := req.URL.Host
	origPath := req.URL.Path
	origReqHost := req.Host
	origAuth := req.Header.Get("Authorization")
	_, hadOriginator := req.Header["originator"] //nolint:staticcheck

	for call := range 2 {
		if _, err := tr.RoundTrip(req); err != nil {
			t.Fatalf("call %d: RoundTrip error: %v", call+1, err)
		}
	}

	// Caller's *http.Request must not have been mutated.
	if req.URL.Host != origHost {
		t.Errorf("caller req.URL.Host mutated: got %q, want %q", req.URL.Host, origHost)
	}
	if req.URL.Path != origPath {
		t.Errorf("caller req.URL.Path mutated: got %q, want %q", req.URL.Path, origPath)
	}
	if req.Host != origReqHost {
		t.Errorf("caller req.Host mutated: got %q, want %q", req.Host, origReqHost)
	}
	if req.Header.Get("Authorization") != origAuth {
		t.Errorf("caller req Authorization header mutated: got %q, want %q", req.Header.Get("Authorization"), origAuth)
	}
	if _, ok := req.Header["originator"]; ok != hadOriginator { //nolint:staticcheck
		t.Errorf("caller req.Header[\"originator\"] mutated: present=%v, want %v", ok, hadOriginator)
	}

	cap.mu.Lock()
	requests := cap.got
	cap.mu.Unlock()

	if len(requests) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(requests))
	}
	// Each call must receive a distinct reader from GetBody() — not the same shared instance.
	if requests[0].Body == requests[1].Body {
		t.Error("both calls received the same Body reader instance; expected distinct readers from GetBody()")
	}
	for i, r := range requests {
		if r.URL.Host != "chatgpt.com" {
			t.Errorf("call %d: URL.Host = %q, want chatgpt.com", i+1, r.URL.Host)
		}
		if r.URL.Scheme != "https" {
			t.Errorf("call %d: URL.Scheme = %q, want https", i+1, r.URL.Scheme)
		}
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("call %d: URL.Path = %q, want /backend-api/codex/responses", i+1, r.URL.Path)
		}
		if r.Host != "chatgpt.com" {
			t.Errorf("call %d: Host = %q, want chatgpt.com", i+1, r.Host)
		}
		if a := r.Header.Get("Authorization"); a != "Bearer my-access-token" {
			t.Errorf("call %d: Authorization = %q, want Bearer my-access-token", i+1, a)
		}
		if r.Body == nil {
			t.Errorf("call %d: Body is nil, want %q", i+1, bodyText)
			continue
		}
		got, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("call %d: ReadAll body: %v", i+1, readErr)
			continue
		}
		if string(got) != bodyText {
			t.Errorf("call %d: body = %q, want %q", i+1, got, bodyText)
		}
	}
	// The caller's original body reader must be untouched — RoundTrip must not
	// consume it. GetBody() is used for each clone, leaving req.Body at position 0.
	// Read this last since it drains the reader.
	if req.Body == nil {
		t.Error("caller req.Body became nil")
	} else {
		callerBody, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Fatalf("ReadAll caller req.Body: %v", readErr)
		}
		if string(callerBody) != bodyText {
			t.Errorf("caller req.Body consumed/altered: got %q, want %q", callerBody, bodyText)
		}
	}
}

// uuidV4Re matches a lowercase UUID v4: 8-4-4-4-12 hex digits with the version
// nibble fixed at 4 and the variant nibble in [89ab].
var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// newTestTransportWithAccount returns a validTransport wired with the given accountID.
func newTestTransportWithAccount(cap *captureTransport, accountID string) *codexTransport {
	tr := validTransport(cap)
	tr.creds.AccountID = accountID
	tr.sessionID = "test-session-id-fixed" // stable value for header-presence tests
	return tr
}

// TestDecorateHeaders_AllHeadersPresent verifies that every request carries
// originator, User-Agent, session_id, and ChatGPT-Account-Id (when non-empty).
func TestDecorateHeaders_AllHeadersPresent(t *testing.T) {
	cap := &captureTransport{}
	tr := newTestTransportWithAccount(cap, "acct-123")

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	cap.mu.Lock()
	if len(cap.got) == 0 {
		cap.mu.Unlock()
		t.Fatal("base RoundTripper was not called")
	}
	got := cap.got[0]
	cap.mu.Unlock()

	// originator and session_id are stored via direct map assignment (not Header.Set)
	// to preserve exact wire casing. Use direct map lookup to match the stored key.
	if v := got.Header["originator"]; len(v) == 0 || v[0] != Originator { //nolint:staticcheck
		t.Errorf("originator = %v, want [%q]", v, Originator)
	}
	if v := got.Header["session_id"]; len(v) == 0 || v[0] != "test-session-id-fixed" { //nolint:staticcheck
		t.Errorf("session_id = %v, want [test-session-id-fixed]", v)
	}
	// ChatGPT-Account-Id uses Header.Set, so use Header.Get (canonical lookup).
	if v := got.Header.Get("ChatGPT-Account-Id"); v != "acct-123" {
		t.Errorf("ChatGPT-Account-Id = %q, want acct-123", v)
	}
	ua := got.Header.Get("User-Agent")
	if !strings.Contains(ua, "advisor/") {
		t.Errorf("User-Agent %q missing advisor/ prefix", ua)
	}
}

// TestDecorateHeaders_EmptyAccountID verifies that ChatGPT-Account-Id is absent
// (not just empty) when accountID is empty.
func TestDecorateHeaders_EmptyAccountID(t *testing.T) {
	cap := &captureTransport{}
	tr := newTestTransportWithAccount(cap, "")

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	cap.mu.Lock()
	if len(cap.got) == 0 {
		cap.mu.Unlock()
		t.Fatal("base RoundTripper was not called")
	}
	got := cap.got[0]
	cap.mu.Unlock()

	// "Chatgpt-Account-Id" is the textproto canonical form of "ChatGPT-Account-Id".
	if _, present := got.Header["Chatgpt-Account-Id"]; present {
		t.Error("ChatGPT-Account-Id header must be absent when accountID is empty")
	}
}

// TestDecorateHeaders_SessionIDFormat verifies that the session_id generated by
// newCodexTransport is a valid lowercase UUID v4.
func TestDecorateHeaders_SessionIDFormat(t *testing.T) {
	tr := newCodexTransport(Credentials{
		Access:  "tok",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}, func(Credentials) error { return nil })

	if !uuidV4Re.MatchString(tr.sessionID) {
		t.Errorf("sessionID %q is not a valid lowercase UUID v4", tr.sessionID)
	}
}

// TestDecorateHeaders_SessionIDStable verifies that the same session_id is sent
// on every request from the same transport (per-process, not per-request).
// Uses newCodexTransport so sessionID is a real UUID (not the empty string),
// ensuring the assertion is not vacuously true.
func TestDecorateHeaders_SessionIDStable(t *testing.T) {
	cap := &captureTransport{}
	tr := newCodexTransport(Credentials{
		Access:  "my-access-token",
		Refresh: "my-refresh-token",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}, func(Credentials) error { return nil })
	tr.base = cap

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	for range 3 {
		if _, err := tr.RoundTrip(req); err != nil {
			t.Fatalf("RoundTrip error: %v", err)
		}
	}

	cap.mu.Lock()
	requests := cap.got
	cap.mu.Unlock()

	if len(requests) < 3 {
		t.Fatalf("expected 3 captured requests, got %d", len(requests))
	}
	// session_id is stored via direct map assignment — use map lookup to match. //nolint:staticcheck
	first := requests[0].Header["session_id"] //nolint:staticcheck
	if len(first) == 0 || first[0] == "" {
		t.Fatal("session_id not set on first request (empty sessionID from newCodexTransport?)")
	}
	for i, r := range requests[1:] {
		sid := r.Header["session_id"] //nolint:staticcheck
		if len(sid) == 0 || sid[0] != first[0] {
			t.Errorf("call %d: session_id %v != first call %v (must be stable per transport)", i+2, sid, first)
		}
	}
}

// TestDecorateHeaders_UserAgentFormat verifies that the User-Agent contains the
// version string, GOOS, and GOARCH.
func TestDecorateHeaders_UserAgentFormat(t *testing.T) {
	cap := &captureTransport{}
	tr := validTransport(cap)

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	cap.mu.Lock()
	ua := cap.got[0].Header.Get("User-Agent")
	cap.mu.Unlock()

	wantPrefix := "advisor/" + Version
	if !strings.HasPrefix(ua, wantPrefix) {
		t.Errorf("User-Agent %q missing prefix %q", ua, wantPrefix)
	}
	if !strings.Contains(ua, runtime.GOOS) {
		t.Errorf("User-Agent %q missing GOOS %q", ua, runtime.GOOS)
	}
	if !strings.Contains(ua, runtime.GOARCH) {
		t.Errorf("User-Agent %q missing GOARCH %q", ua, runtime.GOARCH)
	}
}

func TestDecorateHeaders_UserAgentAppName(t *testing.T) {
	cap := &captureTransport{}
	tr := newCodexTransportForApp("codex", Credentials{
		Access:  "my-access-token",
		Refresh: "my-refresh-token",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}, func(Credentials) error { return nil })
	tr.base = cap

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	cap.mu.Lock()
	ua := cap.got[0].Header.Get("User-Agent")
	cap.mu.Unlock()

	wantPrefix := "codex/" + Version
	if !strings.HasPrefix(ua, wantPrefix) {
		t.Errorf("User-Agent %q missing prefix %q", ua, wantPrefix)
	}
}

// ── HTTPClient / CheckRedirect tests ─────────────────────────────────────────

// TestHTTPClient_NonHTTPSRedirectRefused verifies that a redirect to an http://
// (non-TLS) URL is refused. Uses http.DefaultTransport so the redirect server
// is actually reached and the 302 response flows to CheckRedirect.
func TestHTTPClient_NonHTTPSRedirectRefused(t *testing.T) {
	// Target server — must never be reached if the policy works.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("non-https redirect target was reached — bearer may have leaked")
	}))
	defer target.Close()

	// Redirect server returns 302 → http:// target (non-TLS).
	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound) // target.URL is http://
	}))
	defer redirectSrv.Close()

	// Use DefaultTransport so the redirect server is actually reached.
	client := makeHTTPClient(http.DefaultTransport)
	req, _ := http.NewRequest(http.MethodGet, redirectSrv.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error for non-https redirect, got nil")
	}
	if !strings.Contains(err.Error(), "non-https") {
		t.Errorf("error %q should mention non-https", err)
	}
}

// TestHTTPClient_CrossHostNonHTTPSRedirectNotFollowed verifies that a redirect
// from one HTTP server to a different HTTP server (both non-TLS) is refused by
// the non-https guard and the target is never reached. Uses http.DefaultTransport
// so the redirect server is actually reached.
func TestHTTPClient_CrossHostNonHTTPSRedirectNotFollowed(t *testing.T) {
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to target (different host:port, both http://).
		// The non-https guard fires first, returning an error before ErrUseLastResponse.
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirectSrv.Close()

	client := makeHTTPClient(http.DefaultTransport)
	req, _ := http.NewRequest(http.MethodGet, redirectSrv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		if !strings.Contains(err.Error(), "non-https") {
			t.Errorf("unexpected error: %v", err)
		}
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Errorf("status = %d, want 302 (redirect not followed)", resp.StatusCode)
		}
	}
	if targetCalled {
		t.Error("target server was reached — redirect should have been stopped")
	}
}

// TestHTTPClient_CheckRedirect_StripsAuthOnCrossHost verifies the CheckRedirect
// policy directly: cross-host redirects must have their Authorization header
// stripped before the redirected request is dispatched.
func TestHTTPClient_CheckRedirect_StripsAuthOnCrossHost(t *testing.T) {
	client := makeHTTPClient(http.DefaultTransport)
	policy := client.CheckRedirect

	// Simulate: original request to host A, redirect target is host B.
	orig, _ := http.NewRequest(http.MethodGet, "https://host-a.example.com/path", nil)
	redirectReq, _ := http.NewRequest(http.MethodGet, "https://host-b.example.com/path", nil)
	redirectReq.Header.Set("Authorization", "Bearer secret-token")

	err := policy(redirectReq, []*http.Request{orig})
	// Policy must return ErrUseLastResponse (cap at 0) but still strip auth.
	if err != http.ErrUseLastResponse {
		t.Errorf("policy returned %v, want http.ErrUseLastResponse", err)
	}
	if auth := redirectReq.Header.Get("Authorization"); auth != "" {
		t.Errorf("Authorization header not stripped on cross-host redirect: got %q", auth)
	}
}

// TestHTTPClient_CheckRedirect_PreservesAuthSameHost verifies that same-host
// redirects retain the Authorization header (though still capped at 0).
func TestHTTPClient_CheckRedirect_PreservesAuthSameHost(t *testing.T) {
	client := makeHTTPClient(http.DefaultTransport)
	policy := client.CheckRedirect

	orig, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	redirectReq, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/other-path", nil)
	redirectReq.Header.Set("Authorization", "Bearer secret-token")

	err := policy(redirectReq, []*http.Request{orig})
	if err != http.ErrUseLastResponse {
		t.Errorf("policy returned %v, want http.ErrUseLastResponse", err)
	}
	// Same host — Authorization must not be stripped.
	if auth := redirectReq.Header.Get("Authorization"); auth != "Bearer secret-token" {
		t.Errorf("Authorization stripped on same-host redirect: got %q", auth)
	}
}

// ── Streaming / SSE passthrough tests ────────────────────────────────────────

// roundTripFunc adapts a function to http.RoundTripper. Used by tests that need
// a custom base transport without the captureTransport overhead.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRoundTrip_SSENoBuffering asserts that RoundTrip does NOT buffer or read
// the response body. This is the critical invariant for SSE streaming: the
// caller must receive chunks incrementally as the server writes them, not in
// one big batch after the body is fully received.
//
// The proof relies on a deadlock trap: chunk 3 is only written to the io.Pipe
// AFTER rtDone is closed, which happens when RoundTrip returns. If RoundTrip
// reads the body before returning (e.g. via io.ReadAll), it blocks indefinitely
// waiting for chunk 3 — rtDone never fires, and the test goroutine times out.
func TestRoundTrip_SSENoBuffering(t *testing.T) {
	const (
		chunk1 = "data: chunk1\n\n"
		chunk2 = "data: chunk2\n\n"
		chunk3 = "data: chunk3\n\n"
	)

	pr, pw := io.Pipe()

	// rtDone is closed by the test goroutine when RoundTrip returns. The writer
	// goroutine blocks on <-rtDone before writing chunk 3. If RoundTrip tries
	// to buffer the whole body, it will deadlock here.
	rtDone := make(chan struct{})

	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       pr,
		}
		go func() {
			defer pw.Close()
			// io.Pipe is unbuffered: each Write blocks until the Scanner below
			// reads the data. The goroutine starts before RoundTrip returns, but
			// the writes complete after close(rtDone) opens the Scanner.
			// The 50ms gap paces chunk1→chunk2 to make the stream feel realistic;
			// it is not load-bearing for the deadlock trap.
			if _, err := io.WriteString(pw, chunk1); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
			if _, err := io.WriteString(pw, chunk2); err != nil {
				return
			}
			// Chunk 3 is gated on RoundTrip returning. If RoundTrip tries to
			// read the body, this receive never fires and the test deadlocks.
			<-rtDone
			io.WriteString(pw, chunk3) //nolint:errcheck
		}()
		return resp, nil
	})

	tr := validTransport(base)
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	// Fast-fail watchdog: if RoundTrip blocks (buffering regression), close the
	// pipe reader with an error after 5 s so the test fails with a clear message
	// instead of hanging until Go's default 10-minute test timeout.
	watchdog := time.AfterFunc(5*time.Second, func() {
		pr.CloseWithError(errors.New("test watchdog: RoundTrip blocked >5s (buffering regression?)"))
	})
	defer watchdog.Stop()

	resp, err := tr.RoundTrip(req)
	// Signal the writer goroutine that RoundTrip has returned. If RoundTrip had
	// buffered the body, this line is unreachable.
	close(rtDone)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	// Read all three chunks and verify they arrive in order.
	var got []string
	sc := bufio.NewScanner(resp.Body)
	sc.Split(bufio.ScanLines)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			got = append(got, line+"\n\n")
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("body read error: %v", err)
	}

	want := []string{chunk1, chunk2, chunk3}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d; chunks: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
