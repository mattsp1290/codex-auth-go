package codexauth

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// defaultCodexURL is the parsed CodexEndpoint, used when no override is set so
// the nil-endpoint (default) path is allocation-free in RoundTrip.
var defaultCodexURL = mustParseURL(CodexEndpoint)

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic("codexauth: invalid built-in URL " + raw + ": " + err.Error())
	}
	return u
}

// ErrInsecureEndpoint is returned by HTTPClient when Options.Endpoint uses
// cleartext http:// to a non-loopback host (which would leak the bearer token).
var ErrInsecureEndpoint = errors.New("codexauth: refusing cleartext http endpoint to non-loopback host")

// parseEndpoint parses and validates an Options.Endpoint override.
// Empty raw → (nil, nil): caller uses defaultCodexURL.
func parseEndpoint(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("codexauth: parse Endpoint: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("codexauth: Endpoint scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("codexauth: Endpoint must be absolute (missing host): %q", raw)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("%w: %q", ErrInsecureEndpoint, raw)
	}
	return u, nil
}

// isLoopbackHost reports whether host is a loopback address.
//
// Fail-closed by design: accepts only genuine loopback literals (127.0.0.0/8,
// ::1, localhost). Deliberately rejects 0.0.0.0, decimal/hex IPs, shorthand
// 127.1, and any hostname containing "localhost" as a substring — those would
// reopen DNS-rebinding and homoglyph bypass surface.
func isLoopbackHost(host string) bool {
	// Normalize case and a single trailing FQDN dot so "LOCALHOST" and
	// "localhost." are accepted. Pure string normalization — NOT DNS resolution
	// and NOT substring matching.
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() // 127.0.0.0/8 and ::1
}
