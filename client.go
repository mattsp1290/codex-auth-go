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
// namespace and test seams.
type Client struct {
	appName      string
	callbackPort int
	logger       *slog.Logger
	pathFunc     func() (string, error)
	devicePrompt func(verificationURI, userCode string) error
	endpoint     string // raw Options.Endpoint; parsed+validated in HTTPClient

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
	// validated lazily in HTTPClient (not NewClient): it must be an absolute URL
	// with scheme http or https. As a safety rail, a cleartext http:// endpoint is
	// permitted only for loopback hosts (127.0.0.0/8, ::1, localhost) so a bearer
	// token is never sent in plaintext to a remote host.
	//
	// Note: only Scheme, Host, and Path from Endpoint are used. Any query string
	// embedded in Endpoint is silently dropped; the caller's query string is
	// preserved unchanged.
	Endpoint string
	// CredentialPath overrides the on-disk auth.json location. Empty preserves the
	// default, os.UserConfigDir()/<AppName>/auth.json. When set, all credential
	// reads and writes for this client target the given path — useful for staging a
	// fixture credential file in tests without touching HOME/XDG.
	//
	// Point this at a file in a DEDICATED directory, not a shared one. On any write
	// (Save/Logout-delete, or a token refresh) the parent directory is MkdirAll'd
	// and chmod'd to 0700, and the atomic rename replaces whatever (including a
	// symlink) sits at the path. Reads do not mutate anything.
	CredentialPath string
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
