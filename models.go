package codexauth

import (
	"regexp"
	"strconv"
)

// codexVersionRegex matches GPT model IDs of the form "gpt-<major>.<minor>[...]"
// and captures the major and minor version integers separately. Two capture
// groups are used instead of a single "\d+\.\d+" so that the minor component
// can be parsed as an integer — float64 parsing of "5.10" yields 5.1, which
// would incorrectly place gpt-5.10 below the 5.4 cutoff.
var codexVersionRegex = regexp.MustCompile(`^gpt-(\d+)\.(\d+)`)

// IsCodexAllowed reports whether model is permitted for use with the Codex
// subscription endpoint. Admission follows two paths:
//
//  1. Explicit allow-list — models licensed for Codex regardless of their
//     version order in the `gpt-<N>.<M>` space (e.g. gpt-5.3-codex, which is
//     below the regex cutoff but ships with Codex access). Sourced from
//     opencode codex.ts §CODEX_MODELS; see .claude/docs/codex-auth/06-models-and-errors.md.
//
//  2. Version regex — any model ID matching ^gpt-<major>.<minor> where the
//     integer pair (major, minor) is strictly greater than (5, 4). Minor
//     versions are compared as integers, not as floats: gpt-5.10 has
//     minor=10 > 4, so it is allowed; a float parse of "5.10" would give
//     5.1 < 5.4 and incorrectly exclude it. The regex has no end anchor, so
//     a trailing patch component (e.g. gpt-5.5.0) is ignored — only the
//     first major.minor pair is examined.
//
// The caller is responsible for normalizing model identifiers before calling
// this function. It is case-sensitive and does not strip provider prefixes
// (e.g. "openai/gpt-5.5" would not match).
func IsCodexAllowed(model string) bool {
	switch model {
	case "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.4", "gpt-5.4-mini", "gpt-5.5":
		return true
	}
	m := codexVersionRegex.FindStringSubmatch(model)
	if len(m) < 3 {
		return false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > 5 || (major == 5 && minor > 4)
}
