package codexauth

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNormalizeOptionsDefaults(t *testing.T) {
	opts := normalizeOptions(Options{})

	if opts.AppName != "codex" {
		t.Fatalf("AppName = %q, want codex", opts.AppName)
	}
	if opts.CallbackPort != OAuthPort {
		t.Fatalf("CallbackPort = %d, want %d", opts.CallbackPort, OAuthPort)
	}
	if opts.Logger != slog.Default() {
		t.Fatal("Logger did not default to slog.Default()")
	}
}

func TestNormalizeOptionsPreservesExplicitValues(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	opts := normalizeOptions(Options{
		AppName:      "custom",
		CallbackPort: 1545,
		Logger:       logger,
	})

	if opts.AppName != "custom" {
		t.Fatalf("AppName = %q, want custom", opts.AppName)
	}
	if opts.CallbackPort != 1545 {
		t.Fatalf("CallbackPort = %d, want 1545", opts.CallbackPort)
	}
	if opts.Logger != logger {
		t.Fatal("Logger did not preserve explicit logger")
	}
}

func TestNewClientDefaults(t *testing.T) {
	resetDefaultAppNameWarnForTest()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	c := NewClient(Options{Logger: logger})
	_ = NewClient(Options{Logger: logger})

	if c.appName != "codex" {
		t.Fatalf("appName = %q, want codex", c.appName)
	}
	if c.callbackPort != OAuthPort {
		t.Fatalf("callbackPort = %d, want %d", c.callbackPort, OAuthPort)
	}
	if c.logger != logger {
		t.Fatal("logger did not preserve explicit logger")
	}
	if c.browserAvailableCheck == nil || c.loginBrowserFn == nil || c.loginDeviceFn == nil {
		t.Fatal("client seams were not initialized")
	}
	if c.logoutHTTPClient == nil || c.logoutStderr == nil {
		t.Fatal("logout seams were not initialized")
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "Options.AppName empty") {
		t.Fatalf("default AppName warning missing from log: %q", logOutput)
	}
	if got := strings.Count(logOutput, "Options.AppName empty"); got != 1 {
		t.Fatalf("default AppName warning count = %d, want 1; log: %q", got, logOutput)
	}
}

func TestNewClientExplicitAppNameDoesNotWarn(t *testing.T) {
	resetDefaultAppNameWarnForTest()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	c := NewClient(Options{
		AppName:      "advisor",
		CallbackPort: 1545,
		Logger:       logger,
	})

	if c.appName != "advisor" {
		t.Fatalf("appName = %q, want advisor", c.appName)
	}
	if c.callbackPort != 1545 {
		t.Fatalf("callbackPort = %d, want 1545", c.callbackPort)
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected log output: %q", buf.String())
	}
}

func TestClientPathUsesAppName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	codex := NewClient(Options{AppName: "codex"})
	advisor := NewClient(Options{AppName: "advisor"})

	codexPath, err := codex.path()
	if err != nil {
		t.Fatalf("codex path: %v", err)
	}
	advisorPath, err := advisor.path()
	if err != nil {
		t.Fatalf("advisor path: %v", err)
	}

	if codexPath == advisorPath {
		t.Fatalf("paths are equal: %q", codexPath)
	}
	if !strings.Contains(codexPath, "/codex/auth.json") {
		t.Fatalf("codex path = %q, want codex/auth.json", codexPath)
	}
	if !strings.Contains(advisorPath, "/advisor/auth.json") {
		t.Fatalf("advisor path = %q, want advisor/auth.json", advisorPath)
	}
}

func TestClientAdvisorPathMatchesPackagePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	wrapperPath, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	clientPath, err := NewClient(Options{AppName: "advisor"}).Path()
	if err != nil {
		t.Fatalf("Client.Path: %v", err)
	}

	if clientPath != wrapperPath {
		t.Fatalf("Client advisor path = %q, package path = %q", clientPath, wrapperPath)
	}
}
