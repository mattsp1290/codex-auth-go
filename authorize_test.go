package codexauth

import (
	"net/url"
	"testing"
)

// knownChallenge and knownState are fixed test fixtures. Using constants
// here ensures the expected URL is a stable string that tests can compare
// directly rather than re-constructing it each time.
const (
	knownChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	knownState     = "abc123"
)

// TestBuildAuthorizeURL_ParsesCleanly asserts the returned URL is valid and
// contains all ten required parameters with the correct values.
func TestBuildAuthorizeURL_ParsesCleanly(t *testing.T) {
	raw := BuildAuthorizeURL(knownChallenge, knownState)

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("BuildAuthorizeURL returned unparseable URL: %v", err)
	}

	wantScheme := "https"
	if u.Scheme != wantScheme {
		t.Errorf("scheme = %q, want %q", u.Scheme, wantScheme)
	}
	wantHost := "auth.openai.com"
	if u.Host != wantHost {
		t.Errorf("host = %q, want %q", u.Host, wantHost)
	}
	wantPath := "/oauth/authorize"
	if u.Path != wantPath {
		t.Errorf("path = %q, want %q", u.Path, wantPath)
	}

	q := u.Query()
	params := []struct {
		key  string
		want string
	}{
		{"client_id", ClientID},
		{"redirect_uri", RedirectURI},
		{"response_type", "code"},
		{"scope", Scope},
		{"code_challenge", knownChallenge},
		{"code_challenge_method", PKCEMethod},
		{"id_token_add_organizations", "true"},
		{"codex_cli_simplified_flow", "true"},
		{"originator", "advisor"},
		{"state", knownState},
	}
	for _, p := range params {
		if got := q.Get(p.key); got != p.want {
			t.Errorf("param %q = %q, want %q", p.key, got, p.want)
		}
	}
	if got, want := len(q), len(params); got != want {
		t.Errorf("query has %d params, want %d — remove or document any extras", got, want)
	}
}

// TestBuildAuthorizeURL_StableOutput asserts the URL is deterministic for
// fixed inputs. Stable output ensures it can be safely logged and re-opened.
func TestBuildAuthorizeURL_StableOutput(t *testing.T) {
	first := BuildAuthorizeURL(knownChallenge, knownState)
	second := BuildAuthorizeURL(knownChallenge, knownState)
	if first != second {
		t.Errorf("BuildAuthorizeURL is not deterministic:\n  first:  %q\n  second: %q", first, second)
	}
}

// TestBuildAuthorizeURL_EmptyInputsArePassedThrough documents the accepted
// behavior when the caller supplies empty strings: the URL still parses and
// the empty values are encoded as empty query params. Callers are responsible
// for supplying valid PKCE challenge and CSRF state before calling.
func TestBuildAuthorizeURL_EmptyInputsArePassedThrough(t *testing.T) {
	raw := BuildAuthorizeURL("", "")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("BuildAuthorizeURL(\"\", \"\") returned unparseable URL: %v", err)
	}
	q := u.Query()
	if got := q.Get("code_challenge"); got != "" {
		t.Errorf("code_challenge = %q, want \"\"", got)
	}
	if got := q.Get("state"); got != "" {
		t.Errorf("state = %q, want \"\"", got)
	}
}

// TestBuildAuthorizeURL_ChallengeAndStateRoundtrip confirms that different
// challenge/state values produce different URLs and round-trip through query
// parsing unchanged.
func TestBuildAuthorizeURL_ChallengeAndStateRoundtrip(t *testing.T) {
	cases := []struct {
		challenge string
		state     string
	}{
		{knownChallenge, knownState},
		{"anotherChallenge", "anotherState"},
		{"challenge-with-dashes_and_underscores", "state-value"},
	}
	seen := make(map[string]struct{})
	for _, c := range cases {
		raw := BuildAuthorizeURL(c.challenge, c.state)
		if _, dup := seen[raw]; dup {
			t.Errorf("BuildAuthorizeURL produced duplicate URL for challenge=%q state=%q", c.challenge, c.state)
		}
		seen[raw] = struct{}{}

		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("unparseable URL for challenge=%q: %v", c.challenge, err)
		}
		q := u.Query()
		if got := q.Get("code_challenge"); got != c.challenge {
			t.Errorf("code_challenge round-trip: got %q, want %q", got, c.challenge)
		}
		if got := q.Get("state"); got != c.state {
			t.Errorf("state round-trip: got %q, want %q", got, c.state)
		}
	}
}
