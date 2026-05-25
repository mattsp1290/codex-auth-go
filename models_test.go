package codexauth

import "testing"

// TestIsCodexAllowed exercises the allow-list and version-regex paths.
// Minor versions are compared as integers, not floats; see the gpt-5.10
// case for why this distinction matters.
func TestIsCodexAllowed(t *testing.T) {
	cases := []struct {
		model string
		want  bool
		note  string
	}{
		// Explicit allow-list entries.
		{"gpt-5.4", true, "allow-list: base Codex model"},
		{"gpt-5.4-mini", true, "allow-list: mini variant"},
		{"gpt-5.3-codex", true, "allow-list: below-cutoff Codex variant"},
		{"gpt-5.3-codex-spark", true, "allow-list: below-cutoff spark variant"},
		{"gpt-5.5", true, "allow-list: also covered by regex (5>4), listed explicitly for safety"},

		// Regex path: version strictly > 5.4 (integer major.minor comparison).
		{"gpt-5.10", true, "regex: minor=10 > 4; float('5.10')=5.1 would give false — intentional integer comparison"},
		{"gpt-6.0", true, "regex: major 6 > 5"},
		{"gpt-5.5-turbo", true, "regex: minor=5 > 4, suffix ignored"},

		// Regex match but version not strictly > 5.4.
		{"gpt-5.3", false, "regex: minor=3 not > 4; not in allow-list"},
		// The regex has no end anchor, so gpt-5.4.1 matches as (major=5,minor=4);
		// the result is false because (5,4) is not strictly > (5,4), not because
		// the regex missed. Conversely gpt-5.5.0 is true — (5,5) > (5,4), trailing .0 ignored.
		{"gpt-5.4.1", false, "regex: matches gpt-5.4 prefix, (5,4) not strictly > (5,4); .1 suffix not captured"},
		{"gpt-5.5.0", true, "regex: matches gpt-5.5 prefix; minor=5 > 4; trailing .0 not captured"},

		// Regex path: additional minor-version coverage.
		{"gpt-5.6", true, "regex: minor=6 > 4"},
		{"gpt-5.0", false, "regex: minor=0 not > 4"},

		// Allow-list asymmetry: gpt-5.3-codex is allow-listed but gpt-5.4-codex
		// is not; the regex gives (5,4) which is not strictly > (5,4), so false.
		{"gpt-5.4-codex", false, "regex: (5,4) not > (5,4); not in allow-list (only gpt-5.3-codex variants are)"},

		// Documented contracts (case-sensitive, no provider-prefix stripping,
		// strconv.Atoi overflow returns false).
		{"GPT-5.5", false, "case-sensitive: uppercase GPT- does not match the regex or allow-list"},
		{"gpt-5.99999999999999999999", false, "overflow: strconv.Atoi on the minor group returns error → false"},

		// No regex match at all.
		{"gpt-4o", false, "no match: version does not fit major.minor pattern after 'gpt-'"},
		{"gpt-4o-mini", false, "no match: '4o' is not a numeric major.minor pair"},
		{"o1-preview", false, "no match: does not start with 'gpt-'"},
		{"o4-mini", false, "no match: does not start with 'gpt-'"},
		{"gpt-5", false, "no match: no dot component"},
		{"gpt-", false, "no match: no digit after 'gpt-'"},
		{"", false, "no match: empty string"},
		{"codex", false, "no match: no gpt- prefix"},
	}

	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			got := IsCodexAllowed(c.model)
			if got != c.want {
				t.Errorf("IsCodexAllowed(%q) = %v, want %v — %s", c.model, got, c.want, c.note)
			}
		})
	}
}
