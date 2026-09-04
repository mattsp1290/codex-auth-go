package codexauth

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const emptyCatalogJSON = `{"models":[]}`

func TestModelsEndpoint(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "default", want: "https://chatgpt.com/backend-api/codex/models"},
		{name: "nested", raw: "http://127.0.0.1:4321/backend/responses", want: "http://127.0.0.1:4321/backend/models"},
		{name: "nested trailing", raw: "http://127.0.0.1:4321/backend/responses///", want: "http://127.0.0.1:4321/backend/models"},
		{name: "repeated separators", raw: "http://127.0.0.1:4321/backend//responses//", want: "http://127.0.0.1:4321/backend/models"},
		{name: "root", raw: "http://127.0.0.1:4321/", want: "http://127.0.0.1:4321/models"},
		{name: "empty path", raw: "http://127.0.0.1:4321", want: "http://127.0.0.1:4321/models"},
		{name: "drops metadata", raw: "https://user:pass@example.test/custom/response-name?ignored=1#ignored", want: "https://example.test/custom/models"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := modelsEndpoint(test.raw)
			if err != nil {
				t.Fatalf("modelsEndpoint: %v", err)
			}
			if got != test.want {
				t.Fatalf("modelsEndpoint(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestModelsEndpointErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		is   error
	}{
		{name: "insecure", raw: "http://example.test/responses", is: ErrInsecureEndpoint},
		{name: "relative", raw: "backend/responses"},
		{name: "scheme", raw: "ftp://example.test/responses"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := modelsEndpoint(test.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.is)
			}
		})
	}
}

func TestDecodeModelCatalogResponseSuccess(t *testing.T) {
	body := `{
		"unknown_top":"ignored",
		"models":[
			{"slug":"equal-first","display_name":"Equal first","description":null,"default_reasoning_level":"focused","supported_reasoning_levels":[{"effort":"focused","description":"Focused","future":true},{"effort":"low","description":"Low"}],"visibility":"list","priority":20,"future":"ignored"},
			{"slug":"hidden","display_name":"Hidden","supported_reasoning_levels":[],"visibility":"hide","priority":1},
			{"slug":"first","display_name":"First","description":"","supported_reasoning_levels":[],"visibility":"list","priority":10},
			{"slug":"equal-second","display_name":"Equal second","supported_reasoning_levels":[],"visibility":"list","priority":20},
			{"slug":"future-hidden","display_name":"Future hidden","supported_reasoning_levels":[],"visibility":"future-value","priority":0}
		]
	}`
	got, err := decodeModelCatalogResponse(responseWithBody(http.StatusOK, body))
	if err != nil {
		t.Fatalf("decodeModelCatalogResponse: %v", err)
	}
	if names := []string{got[0].Slug, got[1].Slug, got[2].Slug}; !reflect.DeepEqual(names, []string{"first", "equal-first", "equal-second"}) {
		t.Fatalf("stable priority order = %v", names)
	}
	if got[0].Description == nil || *got[0].Description != "" {
		t.Fatalf("explicit empty description = %#v, want pointer to empty string", got[0].Description)
	}
	if got[1].Description != nil {
		t.Fatalf("null description = %#v, want nil", got[1].Description)
	}
	if got[1].DefaultReasoningEffort == nil || *got[1].DefaultReasoningEffort != "focused" {
		t.Fatalf("default effort = %#v", got[1].DefaultReasoningEffort)
	}
	if got[0].DefaultReasoningEffort != nil {
		t.Fatalf("omitted default effort = %#v, want nil", got[0].DefaultReasoningEffort)
	}
	wantOptions := []ReasoningEffortOption{{Effort: "focused", Description: "Focused"}, {Effort: "low", Description: "Low"}}
	if !reflect.DeepEqual(got[1].SupportedReasoningEfforts, wantOptions) {
		t.Fatalf("reasoning option order = %#v, want %#v", got[1].SupportedReasoningEfforts, wantOptions)
	}
}

func TestDecodeModelCatalogResponseEmptyCatalog(t *testing.T) {
	got, err := decodeModelCatalogResponse(responseWithBody(http.StatusNoContent, emptyCatalogJSON))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("empty catalog = %#v, want non-nil empty slice", got)
	}
}

func TestDecodeModelCatalogResponseRejectsMalformedContract(t *testing.T) {
	valid := `{"slug":"s","display_name":"D","supported_reasoning_levels":[],"visibility":"list","priority":0}`
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"models":[`},
		{name: "wrong top type", body: `{"models":{}}`},
		{name: "missing models", body: `{}`},
		{name: "null models", body: `{"models":null}`},
		{name: "missing slug", body: `{"models":[{"display_name":"D","supported_reasoning_levels":[],"visibility":"list","priority":0}]}`},
		{name: "null slug", body: `{"models":[{"slug":null,"display_name":"D","supported_reasoning_levels":[],"visibility":"list","priority":0}]}`},
		{name: "empty slug", body: `{"models":[{"slug":"","display_name":"D","supported_reasoning_levels":[],"visibility":"list","priority":0}]}`},
		{name: "missing display name", body: `{"models":[{"slug":"s","supported_reasoning_levels":[],"visibility":"list","priority":0}]}`},
		{name: "missing visibility", body: `{"models":[{"slug":"s","display_name":"D","supported_reasoning_levels":[],"priority":0}]}`},
		{name: "missing priority", body: `{"models":[{"slug":"s","display_name":"D","supported_reasoning_levels":[],"visibility":"list"}]}`},
		{name: "missing efforts", body: `{"models":[{"slug":"s","display_name":"D","visibility":"list","priority":0}]}`},
		{name: "null efforts", body: `{"models":[{"slug":"s","display_name":"D","supported_reasoning_levels":null,"visibility":"list","priority":0}]}`},
		{name: "empty default", body: `{"models":[{"slug":"s","display_name":"D","default_reasoning_level":"","supported_reasoning_levels":[],"visibility":"list","priority":0}]}`},
		{name: "missing effort", body: `{"models":[{"slug":"s","display_name":"D","supported_reasoning_levels":[{"description":"D"}],"visibility":"list","priority":0}]}`},
		{name: "empty effort", body: `{"models":[{"slug":"s","display_name":"D","supported_reasoning_levels":[{"effort":"","description":"D"}],"visibility":"list","priority":0}]}`},
		{name: "missing option description", body: `{"models":[{"slug":"s","display_name":"D","supported_reasoning_levels":[{"effort":"low"}],"visibility":"list","priority":0}]}`},
		{name: "trailing object", body: `{"models":[` + valid + `]} {}`},
		{name: "trailing primitive", body: `{"models":[` + valid + `]} true`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeModelCatalogResponse(responseWithBody(http.StatusOK, test.body))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.HasPrefix(err.Error(), "codexauth: list models:") {
				t.Fatalf("unsafe/unstable error prefix: %v", err)
			}
		})
	}
}

func TestDecodeModelCatalogResponseDoesNotEchoMalformedBody(t *testing.T) {
	const canary = "MALFORMED_BODY_CANARY"
	_, err := decodeModelCatalogResponse(responseWithBody(http.StatusOK, `{"models":"`+canary+`"}`))
	if err == nil {
		t.Fatal("expected decode error")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("decode error echoed response body: %v", err)
	}
}

func TestDecodeModelCatalogResponseBodyLimit(t *testing.T) {
	prefix := `{"models":[],"padding":"`
	suffix := `"}`
	atLimit := prefix + strings.Repeat("x", maxModelCatalogResponseBytes-len(prefix)-len(suffix)) + suffix
	if len(atLimit) != maxModelCatalogResponseBytes {
		t.Fatalf("test fixture size = %d", len(atLimit))
	}
	got, err := decodeModelCatalogResponse(responseWithBody(http.StatusOK, atLimit))
	if err != nil || len(got) != 0 {
		t.Fatalf("at-limit response: got=%#v err=%v", got, err)
	}

	overLimit := atLimit + " "
	_, err = decodeModelCatalogResponse(responseWithBody(http.StatusOK, overLimit))
	if err == nil || err.Error() != "codexauth: list models: response exceeds size limit" {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestListModelsCompressedBodyLimit(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = io.WriteString(zw, strings.Repeat("x", maxModelCatalogResponseBytes+1))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer srv.Close()

	c := testCatalogClient(t, srv.URL+"/responses", Credentials{Access: "access", Expires: farFuture()}, nil)
	_, err := c.ListModels(context.Background(), "test")
	if err == nil || err.Error() != "codexauth: list models: response exceeds size limit" {
		t.Fatalf("compressed oversized response error = %v", err)
	}
}

func TestDecodeModelCatalogResponseHTTPErrorIsSafeAndCloses(t *testing.T) {
	const canary = "CATALOG_BODY_CANARY"
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			body := &trackingReadCloser{Reader: strings.NewReader(canary)}
			_, err := decodeModelCatalogResponse(&http.Response{StatusCode: status, Body: body})
			var httpErr *ModelCatalogHTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != status {
				t.Fatalf("error = %v, want typed status %d", err, status)
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaks response body: %v", err)
			}
			if body.reads != 0 || !body.closed {
				t.Fatalf("non-2xx body reads=%d closed=%v, want 0/true", body.reads, body.closed)
			}
		})
	}
}

func TestDecodeModelCatalogResponseAlwaysCloses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "success", status: http.StatusOK, body: emptyCatalogJSON},
		{name: "decode failure", status: http.StatusOK, body: `{"models":`},
		{name: "HTTP failure", status: http.StatusBadGateway, body: "secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &trackingReadCloser{Reader: strings.NewReader(test.body)}
			_, _ = decodeModelCatalogResponse(&http.Response{StatusCode: test.status, Body: body})
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestDecodeModelCatalogResponsePreservesReadCancellation(t *testing.T) {
	body := &errorReadCloser{err: context.Canceled}
	_, err := decodeModelCatalogResponse(&http.Response{StatusCode: http.StatusOK, Body: body})
	if !errors.Is(err, context.Canceled) || !body.closed {
		t.Fatalf("error=%v closed=%v, want context.Canceled and closed", err, body.closed)
	}
}

func TestListModelsRequestContractAndHTTPClientRegression(t *testing.T) {
	const (
		access  = "ACCESS_CANARY"
		account = "ACCOUNT_CANARY"
		version = " 1.2+&injected=value? "
	)
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/backend/models":
			if r.Method != http.MethodGet {
				t.Errorf("method = %s", r.Method)
			}
			if got := r.URL.Query()["client_version"]; !reflect.DeepEqual(got, []string{version}) || len(r.URL.Query()) != 1 {
				t.Errorf("query = %#v, want exactly client_version=%q", r.URL.Query(), version)
			}
			if got := r.Header.Get("Accept"); got != "application/json" {
				t.Errorf("Accept = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+access {
				t.Errorf("Authorization = %q", got)
			}
			if got := r.Header.Get("ChatGPT-Account-Id"); got != account {
				t.Errorf("account = %q", got)
			}
			if got := r.Header.Get("originator"); got != Originator {
				t.Errorf("originator = %q", got)
			}
			if got := r.Header.Get("session_id"); got == "" {
				t.Error("session_id missing")
			}
			if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "catalog-test/"+Version+" ") {
				t.Errorf("User-Agent = %q", got)
			}
			_, _ = io.WriteString(w, `{"models":[{"slug":"visible","display_name":"Visible","supported_reasoning_levels":[],"visibility":"list","priority":2}]}`)
		case "/backend/responses":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "wrong path", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	path := writeFixtureAuth(t, Credentials{Access: access, AccountID: account, Expires: farFuture()})
	c := NewClient(Options{AppName: "catalog-test", Endpoint: srv.URL + "/backend/responses/?drop=1#drop", CredentialPath: path})
	models, err := c.ListModels(context.Background(), version)
	if err != nil || len(models) != 1 || models[0].Slug != "visible" {
		t.Fatalf("ListModels = %#v, %v", models, err)
	}
	hc, err := c.HTTPClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://ignored.invalid/input?caller=kept", nil)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(paths, []string{"/backend/models", "/backend/responses/"}) {
		t.Fatalf("paths = %v", paths)
	}
}

func TestListModelsTransportErrorDoesNotExposeClientVersion(t *testing.T) {
	const versionCanary = "CLIENT_VERSION_CANARY"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()
	c := testCatalogClient(t, srv.URL+"/responses", Credentials{Access: "access", Expires: farFuture()}, nil)
	_, err := c.ListModels(context.Background(), versionCanary)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), versionCanary) {
		t.Fatalf("transport error exposed clientVersion: %v", err)
	}
}

func TestListModelsLocalFailuresPerformNoIO(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()

	path := writeFixtureAuth(t, Credentials{Access: "access", Expires: farFuture()})
	tests := []struct {
		name    string
		client  *Client
		ctx     context.Context
		version string
		is      error
	}{
		{name: "blank version", client: NewClient(Options{Endpoint: srv.URL + "/responses", CredentialPath: path}), ctx: context.Background(), version: " \t\n"},
		{name: "insecure endpoint", client: NewClient(Options{Endpoint: "http://example.test/responses", CredentialPath: path}), ctx: context.Background(), version: "v", is: ErrInsecureEndpoint},
		{name: "not logged in", client: NewClient(Options{Endpoint: srv.URL + "/responses", CredentialPath: filepath.Join(t.TempDir(), "missing.json")}), ctx: context.Background(), version: "v", is: ErrNotLoggedIn},
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests = append(tests, struct {
		name    string
		client  *Client
		ctx     context.Context
		version string
		is      error
	}{name: "pre-canceled", client: NewClient(Options{Endpoint: srv.URL + "/responses", CredentialPath: path}), ctx: canceled, version: "v", is: context.Canceled})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := calls.Load()
			_, err := test.client.ListModels(test.ctx, test.version)
			if err == nil {
				t.Fatal("expected error")
			}
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.is)
			}
			if calls.Load() != before {
				t.Fatal("unexpected network I/O")
			}
		})
	}
}

func TestListModelsCancellationAndGateWait(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseServer := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-requestStarted:
		default:
			close(requestStarted)
		}
		select {
		case <-releaseServer:
			_, _ = io.WriteString(w, emptyCatalogJSON)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	c := testCatalogClient(t, srv.URL+"/responses", Credentials{Access: "access", Expires: farFuture()}, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := c.ListModels(context.Background(), "first")
		firstDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not start")
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelWait()
	started := time.Now()
	_, err := c.ListModels(waitCtx, "waiting")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting call error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("gate waiter did not honor its context promptly")
	}
	close(releaseServer)
	if err := <-firstDone; err != nil {
		t.Fatalf("first call: %v", err)
	}

	models, err := c.ListModels(context.Background(), "after")
	if err != nil || models == nil {
		t.Fatalf("gate not released after waiter cancellation: %#v, %v", models, err)
	}
}

func TestListModelsFreshRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, emptyCatalogJSON)
	}))
	defer srv.Close()
	c := testCatalogClient(t, srv.URL+"/responses", Credentials{Access: "access", Expires: farFuture()}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ListModels(ctx, "v")
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request did not return")
	}
	if models, err := c.ListModels(context.Background(), "after-cancel"); err != nil || models == nil {
		t.Fatalf("gate not released after request cancellation: %#v, %v", models, err)
	}
}

func TestListModelsRequestDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := testCatalogClient(t, srv.URL+"/responses", Credentials{Access: "access", Expires: farFuture()}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.ListModels(ctx, "v")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestListModelsRedirectIsNotFollowed(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	c := testCatalogClient(t, source.URL+"/responses", Credentials{Access: "access", Expires: farFuture()}, nil)
	_, err := c.ListModels(context.Background(), "v")
	var httpErr *ModelCatalogHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound {
		t.Fatalf("error = %v, want 302 ModelCatalogHTTPError", err)
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect target received a request")
	}
}

func TestListModelsGateRecoveryAfterFailures(t *testing.T) {
	responses := []string{"status", "decode", "success"}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(calls.Add(1)) - 1
		switch responses[i] {
		case "status":
			http.Error(w, "BODY_CANARY", http.StatusBadGateway)
		case "decode":
			_, _ = io.WriteString(w, `{"models":`)
		case "success":
			_, _ = io.WriteString(w, emptyCatalogJSON)
		}
	}))
	defer srv.Close()
	c := testCatalogClient(t, srv.URL+"/responses", Credentials{Access: "access", Expires: farFuture()}, nil)
	for i := range responses {
		models, err := c.ListModels(context.Background(), "v")
		if i < 2 && err == nil {
			t.Fatalf("call %d: expected error", i)
		}
		if i == 2 && (err != nil || models == nil) {
			t.Fatalf("call %d: models=%#v err=%v", i, models, err)
		}
	}
}

func TestListModelsGateRecoveryAcrossConstructionAndTransportFailures(t *testing.T) {
	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, emptyCatalogJSON)
	}))
	defer successSrv.Close()

	t.Run("endpoint validation", func(t *testing.T) {
		c := testCatalogClient(t, "http://example.test/responses", Credentials{Access: "access", Expires: farFuture()}, nil)
		if _, err := c.ListModels(context.Background(), "v"); !errors.Is(err, ErrInsecureEndpoint) {
			t.Fatalf("first error = %v", err)
		}
		c.endpoint = successSrv.URL + "/responses"
		if _, err := c.ListModels(context.Background(), "v"); err != nil {
			t.Fatalf("recovery call: %v", err)
		}
	})

	t.Run("missing credentials", func(t *testing.T) {
		path := writeEmptyAuthFile(t)
		c := NewClient(Options{AppName: "catalog-test", Endpoint: successSrv.URL + "/responses", CredentialPath: path})
		if _, err := c.ListModels(context.Background(), "v"); !errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("first error = %v", err)
		}
		if err := c.save(AuthFile{OpenAI: &Credentials{Access: "access", Expires: farFuture()}}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ListModels(context.Background(), "v"); err != nil {
			t.Fatalf("recovery call: %v", err)
		}
	})

	t.Run("transport", func(t *testing.T) {
		closedSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		closedURL := closedSrv.URL
		closedSrv.Close()
		c := testCatalogClient(t, closedURL+"/responses", Credentials{Access: "access", Expires: farFuture()}, nil)
		if _, err := c.ListModels(context.Background(), "v"); err == nil {
			t.Fatal("expected transport failure")
		}
		c.endpoint = successSrv.URL + "/responses"
		if _, err := c.ListModels(context.Background(), "v"); err != nil {
			t.Fatalf("recovery call: %v", err)
		}
	})
}

func TestListModelsGateRecoveryAfterRefreshFailure(t *testing.T) {
	var refreshCalls atomic.Int32
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if refreshCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"first failure"}`)
			return
		}
		_, _ = io.WriteString(w, freshTokenJSON)
	}))
	defer refreshSrv.Close()
	withTestEndpoint(t, refreshSrv.URL)
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, emptyCatalogJSON)
	}))
	defer catalogSrv.Close()
	c := testCatalogClient(t, catalogSrv.URL+"/responses", Credentials{
		Access: "old", Refresh: "refresh", Expires: time.Now().Add(-time.Hour).UnixMilli(),
	}, nil)
	if _, err := c.ListModels(context.Background(), "v"); err == nil {
		t.Fatal("expected refresh failure")
	}
	if _, err := c.ListModels(context.Background(), "v"); err != nil {
		t.Fatalf("recovery call: %v", err)
	}
}

func TestListModelsPreCanceledStaleCallPerformsNoIO(t *testing.T) {
	var refreshCalls atomic.Int32
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { refreshCalls.Add(1) }))
	defer refreshSrv.Close()
	withTestEndpoint(t, refreshSrv.URL)
	var catalogCalls atomic.Int32
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { catalogCalls.Add(1) }))
	defer catalogSrv.Close()
	c := testCatalogClient(t, catalogSrv.URL+"/responses", Credentials{
		Access: "old", Refresh: "refresh", Expires: time.Now().Add(-time.Hour).UnixMilli(),
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListModels(ctx, "v")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if refreshCalls.Load() != 0 || catalogCalls.Load() != 0 {
		t.Fatalf("refresh calls=%d catalog calls=%d, want zero", refreshCalls.Load(), catalogCalls.Load())
	}
}

func TestListModelsHTTPFailureDoesNotLeakBodyCredentialsOrLogs(t *testing.T) {
	const bodyCanary = "HTTP_BODY_CANARY"
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, bodyCanary)
	}))
	defer srv.Close()
	const access = "HTTP_ACCESS_CANARY"
	const account = "HTTP_ACCOUNT_CANARY"
	c := testCatalogClient(t, srv.URL+"/responses", Credentials{Access: access, AccountID: account, Expires: farFuture()}, logger)
	_, err := c.ListModels(context.Background(), "v")
	var httpErr *ModelCatalogHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusForbidden {
		t.Fatalf("error = %v", err)
	}
	combined := err.Error() + "\n" + logs.String()
	for _, canary := range []string{bodyCanary, access, account} {
		if strings.Contains(combined, canary) {
			t.Fatalf("error/log output leaked %q: %s", canary, combined)
		}
	}
}

func TestListModelsConcurrentStaleCallsRefreshOnce(t *testing.T) {
	var refreshCalls atomic.Int32
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		if got := r.FormValue("refresh_token"); got != "old-refresh" {
			t.Errorf("refresh token = %q", got)
		}
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer refreshSrv.Close()
	withTestEndpoint(t, refreshSrv.URL)

	var catalogCalls atomic.Int32
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer new-access" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct-original" {
			t.Errorf("account ID = %q", got)
		}
		_, _ = io.WriteString(w, emptyCatalogJSON)
	}))
	defer catalogSrv.Close()

	c := testCatalogClient(t, catalogSrv.URL+"/responses", Credentials{
		Access: "old-access", Refresh: "old-refresh", AccountID: "acct-original", Expires: time.Now().Add(-time.Hour).UnixMilli(),
	}, nil)

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := c.ListModels(context.Background(), fmt.Sprintf("caller-%d", i))
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ListModels: %v", err)
		}
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	if got := catalogCalls.Load(); got != callers {
		t.Fatalf("catalog calls = %d, want %d", got, callers)
	}
	stored, err := c.load()
	if err != nil || stored.OpenAI == nil {
		t.Fatalf("load persisted credentials: %#v, %v", stored, err)
	}
	if stored.OpenAI.Access != "new-access" || stored.OpenAI.Refresh != "new-refresh" || stored.OpenAI.AccountID != "acct-original" {
		t.Fatalf("persisted credentials = %#v", stored.OpenAI)
	}
}

func TestListModelsCancelDuringRefreshPersistsAndSkipsCatalog(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(refreshStarted)
		<-releaseRefresh
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"persisted-access","refresh_token":"persisted-refresh","expires_in":3600}`)
	}))
	defer refreshSrv.Close()
	withTestEndpoint(t, refreshSrv.URL)

	var catalogCalls atomic.Int32
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		catalogCalls.Add(1)
		_, _ = io.WriteString(w, emptyCatalogJSON)
	}))
	defer catalogSrv.Close()
	c := testCatalogClient(t, catalogSrv.URL+"/responses", Credentials{
		Access: "old-access", Refresh: "old-refresh", AccountID: "acct", Expires: time.Now().Add(-time.Hour).UnixMilli(),
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ListModels(ctx, "v")
		done <- err
	}()
	<-refreshStarted
	cancel()
	select {
	case err := <-done:
		t.Fatalf("ListModels returned before detached refresh finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseRefresh)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListModels did not return after refresh")
	}
	if catalogCalls.Load() != 0 {
		t.Fatal("catalog request was sent after cancellation")
	}
	stored, err := c.load()
	if err != nil || stored.OpenAI == nil || stored.OpenAI.Access != "persisted-access" || stored.OpenAI.Refresh != "persisted-refresh" {
		t.Fatalf("refresh was not safely persisted: %#v, %v", stored, err)
	}
}

func TestListModelsRefreshFailuresAreSanitized(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantCode     string
		wantSentinel bool
	}{
		{name: "allowlisted OAuth code", body: `{"error":"invalid_client","error_description":"DESCRIPTION_CANARY"}`, wantCode: "invalid_client"},
		{name: "unknown OAuth code", body: `{"error":"UNKNOWN_CODE_CANARY","error_description":"DESCRIPTION_CANARY"}`, wantSentinel: true},
		{name: "non-JSON body", body: `NON_JSON_BODY_CANARY`, wantSentinel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, test.body)
			}))
			defer refreshSrv.Close()
			withTestEndpoint(t, refreshSrv.URL)

			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			catalogSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("catalog endpoint called after refresh failure")
			}))
			defer catalogSrv.Close()
			const access = "ACCESS_CREDENTIAL_CANARY"
			const refresh = "REFRESH_CREDENTIAL_CANARY"
			const account = "ACCOUNT_CREDENTIAL_CANARY"
			c := testCatalogClient(t, catalogSrv.URL+"/responses", Credentials{
				Access: access, Refresh: refresh, AccountID: account, Expires: time.Now().Add(-time.Hour).UnixMilli(),
			}, logger)

			_, err := c.ListModels(context.Background(), "v")
			if err == nil {
				t.Fatal("expected refresh failure")
			}
			if test.wantCode != "" {
				var authErr *AuthError
				if !errors.As(err, &authErr) || authErr.Code != test.wantCode || authErr.Description != "" {
					t.Fatalf("error = %v, AuthError = %#v", err, authErr)
				}
			}
			if test.wantSentinel && !errors.Is(err, ErrRefreshFailed) {
				t.Fatalf("error = %v, want ErrRefreshFailed", err)
			}
			combined := err.Error() + "\n" + logs.String()
			for _, canary := range []string{"DESCRIPTION_CANARY", "UNKNOWN_CODE_CANARY", "NON_JSON_BODY_CANARY", access, refresh, account} {
				if strings.Contains(combined, canary) {
					t.Fatalf("error/log output leaked %q: %s", canary, combined)
				}
			}
		})
	}
}

func TestListModelsInvalidGrantLogIsSanitized(t *testing.T) {
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"INVALID_GRANT_DESCRIPTION_CANARY"}`)
	}))
	defer refreshSrv.Close()
	withTestEndpoint(t, refreshSrv.URL)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("catalog endpoint called after invalid_grant")
	}))
	defer catalogSrv.Close()
	c := testCatalogClient(t, catalogSrv.URL+"/responses", Credentials{
		Access: "ACCESS_CANARY", Refresh: "REFRESH_CANARY", AccountID: "ACCOUNT_CANARY", Expires: time.Now().Add(-time.Hour).UnixMilli(),
	}, logger)

	_, err := c.ListModels(context.Background(), "v")
	var authErr *AuthError
	if !errors.As(err, &authErr) || authErr.Code != "invalid_grant" || authErr.Description != "" {
		t.Fatalf("error = %v, AuthError = %#v", err, authErr)
	}
	combined := err.Error() + "\n" + logs.String()
	for _, canary := range []string{"INVALID_GRANT_DESCRIPTION_CANARY", "ACCESS_CANARY", "REFRESH_CANARY", "ACCOUNT_CANARY"} {
		if strings.Contains(combined, canary) {
			t.Fatalf("error/log output leaked %q: %s", canary, combined)
		}
	}
	if !strings.Contains(logs.String(), "oauth_code=invalid_grant") {
		t.Fatalf("safe invalid_grant metadata missing: %s", logs.String())
	}
}

func TestListModelsCancellationWinsRefreshFailureRace(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(refreshStarted)
		<-releaseRefresh
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"RACE_DESCRIPTION_CANARY"}`)
	}))
	defer refreshSrv.Close()
	withTestEndpoint(t, refreshSrv.URL)
	var catalogCalls atomic.Int32
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { catalogCalls.Add(1) }))
	defer catalogSrv.Close()
	c := testCatalogClient(t, catalogSrv.URL+"/responses", Credentials{
		Access: "old", Refresh: "refresh", Expires: time.Now().Add(-time.Hour).UnixMilli(),
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ListModels(ctx, "v")
		done <- err
	}()
	<-refreshStarted
	cancel()
	close(releaseRefresh)
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "RACE_DESCRIPTION_CANARY") {
		t.Fatalf("error leaks refresh response: %v", err)
	}
	if catalogCalls.Load() != 0 {
		t.Fatal("catalog request sent after cancellation")
	}
}

func TestHTTPClientRefreshBehaviorRemainsUnsanitized(t *testing.T) {
	refreshSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"HTTPCLIENT_DESCRIPTION"}`)
	}))
	defer refreshSrv.Close()
	withTestEndpoint(t, refreshSrv.URL)
	catalogSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer catalogSrv.Close()
	c := testCatalogClient(t, catalogSrv.URL+"/responses", Credentials{
		Access: "old", Refresh: "refresh", Expires: time.Now().Add(-time.Hour).UnixMilli(),
	}, nil)
	hc, err := c.HTTPClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	transport := hc.Transport.(*codexTransport)
	if transport.safeRefreshErrors {
		t.Fatal("ordinary HTTPClient unexpectedly enabled catalog refresh redaction")
	}
	req, _ := http.NewRequest(http.MethodPost, "https://ignored.invalid", nil)
	_, err = hc.Do(req)
	if err == nil || !strings.Contains(err.Error(), "HTTPCLIENT_DESCRIPTION") {
		t.Fatalf("ordinary HTTPClient refresh error changed: %v", err)
	}
}

func responseWithBody(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func testCatalogClient(t *testing.T, endpoint string, creds Credentials, logger *slog.Logger) *Client {
	t.Helper()
	return NewClient(Options{
		AppName:        "catalog-test",
		Endpoint:       endpoint,
		CredentialPath: writeFixtureAuth(t, creds),
		Logger:         logger,
	})
}

type trackingReadCloser struct {
	io.Reader
	reads  int
	closed bool
}

func (b *trackingReadCloser) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (b *trackingReadCloser) Close() error {
	b.closed = true
	return nil
}

type errorReadCloser struct {
	err    error
	closed bool
}

func (b *errorReadCloser) Read([]byte) (int, error) { return 0, b.err }
func (b *errorReadCloser) Close() error {
	b.closed = true
	return nil
}

func writeEmptyAuthFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
