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
		browserAvailableCheck: canOpenBrowserNative,
		logoutHTTPClient:      newLogoutHTTPClient(),
		logoutStderr:          os.Stderr,
	}
	c.loginBrowserFn = c.LoginBrowser
	c.loginDeviceFn = c.LoginDevice
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
