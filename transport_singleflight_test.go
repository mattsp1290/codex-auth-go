package codexauth

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRoundTrip_Singleflight_StressTest verifies that 10 goroutines simultaneously
// calling RoundTrip with expired credentials trigger exactly one token-endpoint
// call (≤1), and that all outgoing requests carry the refreshed access token.
// Unlike TestEnsureFresh_Singleflight (which calls ensureFresh directly), this
// test exercises the full RoundTrip path: token refresh + URL rewrite + header
// injection, verifying the refreshed token reaches the base transport.
// Run with go test -race to verify the singleflight + mu locking is race-free.
func TestRoundTrip_Singleflight_StressTest(t *testing.T) {
	// Token refresh server: sleeps 100 ms so all goroutines pile into sf.Do
	// before the first response returns, maximising singleflight contention.
	refreshSrv, refreshCount := newRefreshServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, freshTokenJSON)
	})
	defer refreshSrv.Close()
	withTestEndpoint(t, refreshSrv.URL)
	setTestPath(t)

	cap := &captureTransport{}
	tr := &codexTransport{
		base:  cap,
		now:   time.Now,
		creds: expiredToken(),
		saveCreds: func(c Credentials) error {
			return Save(AuthFile{OpenAI: &c})
		},
	}

	const N = 10
	ready := make(chan struct{})
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
			<-ready
			_, errs[i] = tr.RoundTrip(req)
		}()
	}
	close(ready)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutines did not complete within 5s — possible singleflight deadlock")
	}

	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: RoundTrip error: %v", i, e)
		}
	}

	// Exactly one token-endpoint hit — singleflight coalesced all the rest.
	if got := atomic.LoadInt32(refreshCount); got != 1 {
		t.Errorf("token-endpoint call count = %d, want 1 (singleflight coalesce failed)", got)
	}

	// In-memory credentials must reflect the refreshed token.
	tr.mu.RLock()
	access := tr.creds.Access
	tr.mu.RUnlock()
	if access != "newtoken" {
		t.Errorf("in-memory creds.Access = %q after refresh, want newtoken", access)
	}

	// All N base requests must carry the refreshed token.
	cap.mu.Lock()
	requests := cap.got
	cap.mu.Unlock()

	if len(requests) != N {
		t.Errorf("base RoundTripper received %d requests, want %d", len(requests), N)
	}
	for i, r := range requests {
		if a := r.Header.Get("Authorization"); a != "Bearer newtoken" {
			t.Errorf("request %d: Authorization = %q, want Bearer newtoken", i+1, a)
		}
	}
}
