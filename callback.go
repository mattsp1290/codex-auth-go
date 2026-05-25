package codexauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

const (
	callbackPath            = "/auth/callback"
	callbackHTMLSuccess     = `<!doctype html><html><body><p>Authentication complete. You can close this tab.</p></body></html>`
	callbackHTMLError       = `<!doctype html><html><body><p>Authorization failed. You can close this tab.</p></body></html>`
	callbackReadHdrTimeout  = 10 * time.Second
	callbackWriteTimeout    = 10 * time.Second
	callbackShutdownTimeout = 5 * time.Second
)

// PortInUseError is returned by StartCallbackServer when port OAuthPort (1455)
// is already bound on either loopback address. The caller should surface this
// to the user; the fix is to stop the other process holding the port.
type PortInUseError struct {
	Addr string
	Err  error
}

func (e *PortInUseError) Error() string {
	return fmt.Sprintf("codexauth: port %s is already in use; is another codex-auth client running on this port?", e.Addr)
}

func (e *PortInUseError) Unwrap() error { return e.Err }

// ErrStateMismatch is sent on errCh when the OAuth callback arrives with a
// state parameter that does not match the value passed to StartCallbackServer.
// The caller should treat this as a potential CSRF attempt and abort the flow.
var ErrStateMismatch = errors.New("codexauth: OAuth callback state mismatch")

// ErrAuthorizationDenied is sent on errCh when the authorization server
// redirects back with an ?error= parameter (e.g. "access_denied"). It
// wraps the raw OAuth error string so callers can errors.Is on the sentinel
// while still surfacing the details in log output.
var ErrAuthorizationDenied = errors.New("codexauth: authorization denied")

// StartCallbackServer binds loopback listeners on OAuthPort (1455) for both
// IPv4 (127.0.0.1) and IPv6 ([::1]) and serves a /auth/callback handler.
//
// Both listeners are bound synchronously before the function returns — callers
// may open the browser immediately after a nil err return. If either bind fails
// with EADDRINUSE, *PortInUseError is returned and no goroutines are started.
// If IPv6 is unavailable for reasons other than EADDRINUSE, only IPv4 is used.
//
// The server shuts down automatically when ctx is cancelled or times out. The
// returned shutdown func may also be called directly (e.g. via defer) to close
// the server; subsequent calls to shutdown are no-ops.
func StartCallbackServer(ctx context.Context, state string) (codeCh <-chan string, errCh <-chan error, shutdown func(), err error) {
	l4, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", OAuthPort))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, nil, nil, &PortInUseError{Addr: fmt.Sprintf("127.0.0.1:%d", OAuthPort), Err: err}
		}
		return nil, nil, nil, fmt.Errorf("codexauth: binding callback port (IPv4): %w", err)
	}

	l6, err := net.Listen("tcp", fmt.Sprintf("[::1]:%d", OAuthPort))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			l4.Close() //nolint:errcheck
			return nil, nil, nil, &PortInUseError{Addr: fmt.Sprintf("[::1]:%d", OAuthPort), Err: err}
		}
		// IPv6 not available on this host; continue with IPv4 only.
		l6 = nil
	}

	code := make(chan string, 1)
	// errChan has two producers: the callback handler (state mismatch, OAuth
	// error, missing code) and the serveOn goroutine (listener errors). The
	// cap-1 buffer means the first error wins; subsequent errors are dropped.
	errChan := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, makeCallbackHandler(state, code, errChan))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: callbackReadHdrTimeout,
		WriteTimeout:      callbackWriteTimeout,
	}

	// innerCtx is derived with WithCancel so that shutdownFn can unblock the
	// context-watcher goroutine even when ctx is context.Background() (whose
	// Done channel is nil and would block forever).
	innerCtx, innerCancel := context.WithCancel(ctx)

	var shutdownOnce sync.Once
	shutdownFn := func() {
		shutdownOnce.Do(func() {
			innerCancel() // unblock the watcher goroutine
			shutCtx, cancel := context.WithTimeout(context.Background(), callbackShutdownTimeout)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
		})
	}

	serveOn := func(l net.Listener) {
		if l == nil {
			return
		}
		if serveErr := srv.Serve(l); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			select {
			case errChan <- serveErr:
			default:
			}
		}
	}

	go serveOn(l4)
	if l6 != nil {
		go serveOn(l6)
	}

	// Honour the caller's context deadline (e.g. 5-minute flow timeout).
	go func() {
		<-innerCtx.Done()
		shutdownFn()
	}()

	return code, errChan, shutdownFn, nil
}

// makeCallbackHandler returns a handler that validates the OAuth state and
// forwards the authorization code. Error conditions:
//   - non-GET method → 405, no errCh signal
//   - state mismatch → 400, ErrStateMismatch on errCh
//   - ?error= parameter (RFC 6749 §4.1.2.1) → 400, ErrAuthorizationDenied on errCh
//   - missing code → 400, error on errCh
func makeCallbackHandler(expectedState string, code chan<- string, errCh chan<- error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		q := r.URL.Query()

		if got := q.Get("state"); got != expectedState {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			select {
			case errCh <- ErrStateMismatch:
			default:
			}
			return
		}

		// RFC 6749 §4.1.2.1: authorization server redirects with ?error=...
		// when the user denies or something goes wrong server-side.
		if oauthErr := q.Get("error"); oauthErr != "" {
			desc := q.Get("error_description")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, callbackHTMLError) //nolint:errcheck
			select {
			case errCh <- fmt.Errorf("%w: %s — %s", ErrAuthorizationDenied, oauthErr, desc):
			default:
			}
			return
		}

		c := q.Get("code")
		if c == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("codexauth: callback response missing authorization code"):
			default:
			}
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, callbackHTMLSuccess) //nolint:errcheck
		select {
		case code <- c:
		default:
		}
	}
}
