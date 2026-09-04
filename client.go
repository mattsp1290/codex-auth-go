package codexauth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var defaultAppNameWarnOnce sync.Once

// Client authenticates a single Codex consumer with its own credential
// namespace and test seams. ListModels calls made through one Client are
// serialized; distinct Client values and processes are not coalesced.
type Client struct {
	appName      string
	callbackPort int
	logger       *slog.Logger
	pathFunc     func() (string, error)
	devicePrompt func(verificationURI, userCode string) error
	endpoint     string // raw Options.Endpoint; parsed+validated in HTTPClient or ListModels

	catalogGateOnce sync.Once
	catalogGate     chan struct{}

	browserAvailableCheck func() error
	loginBrowserFn        func(context.Context) (Credentials, error)
	loginDeviceFn         func(context.Context) (Credentials, error)
	logoutHTTPClient      httpDoer
	logoutStderr          io.Writer
}

// NewClient returns a Client configured from opts.
func NewClient(opts Options) *Client {
	defaultedAppName := opts.AppName == ""
	opts = normalizeOptions(opts)
	if defaultedAppName {
		defaultAppNameWarnOnce.Do(func() {
			opts.Logger.Warn("codexauth: Options.AppName empty; defaulting to codex", "appName", opts.AppName)
		})
	}

	c := &Client{
		appName:               opts.AppName,
		callbackPort:          opts.CallbackPort,
		logger:                opts.Logger,
		devicePrompt:          opts.DevicePrompt,
		endpoint:              opts.Endpoint,
		browserAvailableCheck: canOpenBrowserNative,
		logoutHTTPClient:      newLogoutHTTPClient(),
		logoutStderr:          os.Stderr,
	}
	c.loginBrowserFn = c.LoginBrowser
	c.loginDeviceFn = c.LoginDevice
	c.initCatalogGate()
	if opts.CredentialPath != "" {
		c.pathFunc = func() (string, error) { return opts.CredentialPath, nil }
	}
	return c
}

func (c *Client) path() (string, error) {
	if c.pathFunc != nil {
		return c.pathFunc()
	}
	return defaultPath(c.appName)
}

// Options configures a Client.
type Options struct {
	// AppName selects the on-disk credential directory. An empty AppName
	// defaults to "codex" for explicit clients.
	AppName string
	// CallbackPort selects the local OAuth callback port. Zero defaults to
	// OAuthPort.
	CallbackPort int
	// Logger receives structured diagnostics. Nil defaults to slog.Default().
	Logger *slog.Logger
	// DevicePrompt is called during device login after a safe user_code is
	// received and before polling begins. Nil preserves the default stderr
	// prompt.
	DevicePrompt func(verificationURI, userCode string) error
	// Endpoint overrides the Responses-API URL that the transport rewrites every
	// request to. Empty preserves the default (CodexEndpoint,
	// https://chatgpt.com/backend-api/codex/responses). The value is parsed and
	// validated lazily in HTTPClient or ListModels (not NewClient): it must be an
	// absolute URL with scheme http or https. ListModels derives a sibling models
	// route by normalizing trailing slashes and replacing the final path segment
	// with "models". As a safety rail, a cleartext http:// endpoint is permitted
	// only for loopback hosts (127.0.0.0/8, ::1, localhost) so a bearer token is
	// never sent in plaintext to a remote host.
	//
	// Note: only Scheme, Host, and Path from Endpoint are used. Any query string
	// embedded in Endpoint is silently dropped; HTTPClient preserves the caller's
	// query string unchanged, while ListModels supplies only its client_version
	// query. ListModels also drops any Endpoint fragment.
	//
	// Redirect caveat: when Endpoint uses cleartext http:// (loopback only), the
	// server must respond directly (e.g. 200 with the SSE body) and must NOT issue
	// a 3xx redirect. The client's redirect policy hard-refuses any non-https
	// redirect hop and returns "codexauth: refusing non-https redirect". This is
	// intentional defense-in-depth, not a bug — design your test fake accordingly.
	Endpoint string
	// CredentialPath overrides the on-disk auth.json location. Empty preserves the
	// default, os.UserConfigDir()/<AppName>/auth.json (where <AppName> defaults to
	// "codex" for explicit NewClient callers). When set, all credential reads and
	// writes for this client target the given path — useful for staging a fixture
	// credential file in tests without touching HOME/XDG.
	//
	// WARNING: point this at a file inside a directory you own EXCLUSIVELY. On any
	// write (Save/Logout/refresh) the PARENT DIRECTORY of this path is MkdirAll'd
	// AND its permissions are unconditionally overwritten to 0700 — even if the
	// directory already existed. Pointing CredentialPath at a file in a shared
	// directory (e.g. /tmp/auth.json, $HOME/auth.json) will re-permission that
	// directory to owner-only on first write. Use t.TempDir() in tests.
	// Reads do not mutate anything.
	CredentialPath string
}

func (c *Client) initCatalogGate() {
	c.catalogGateOnce.Do(func() {
		c.catalogGate = make(chan struct{}, 1)
		c.catalogGate <- struct{}{}
	})
}

func (c *Client) acquireCatalogGate(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.initCatalogGate()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.catalogGate:
		return func() { c.catalogGate <- struct{}{} }, nil
	}
}

// StatusInfo describes the local credential-store state for a Client.
type StatusInfo struct {
	LoggedIn   bool
	Stale      bool
	ExpiresAt  time.Time
	AccountID  string
	ConfigPath string
}

func normalizeOptions(opts Options) Options {
	if opts.AppName == "" {
		opts.AppName = "codex"
	}
	if opts.CallbackPort == 0 {
		opts.CallbackPort = OAuthPort
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return opts
}

func newLogoutHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func defaultPath(appName string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appName, "auth.json"), nil
}

func resetDefaultAppNameWarnForTest() {
	defaultAppNameWarnOnce = sync.Once{}
}
