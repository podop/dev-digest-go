package domain

import (
	"fmt"
	"slices"
	"time"
)

// TokenAccess is what a token can do (spec FR11). The zero value means unknown (null).
type TokenAccess string

// The token accesses.
const (
	// AccessFull: the scopes contain api.
	AccessFull TokenAccess = "full"
	// AccessReadOnly: the scopes contain read_api but not api.
	AccessReadOnly TokenAccess = "read_only"
)

// Warning codes of a connection test, in the order Evaluate lists them (spec FR11).
const (
	WarnReadOnlyToken      = "read_only_token"
	WarnNoRepositoryScope  = "no_repository_scope"
	WarnTokenExpiring      = "token_expiring"
	WarnVersionUnsupported = "version_unsupported"
	WarnScopesUnknown      = "scopes_unknown"
	WarnVersionUnknown     = "version_unknown"
)

// ExpiryWindowDays is how many days before its expiry a token is reported as expiring.
const ExpiryWindowDays = 14

// MinMajor and MinMinor are the oldest GitLab version without a warning (spec D8): 17.0.
const (
	MinMajor = 17
	MinMinor = 0
)

// MsgNeedsScope is the message of a token without the api or read_api scope.
const MsgNeedsScope = "The token needs the api or read_api scope"

// Warning is one hint of a successful test.
type Warning struct {
	Code    string
	Message string
}

// Evidence is what the three calls of a test (spec FR10) learned about the token. A call that
// failed leaves its fields at the zero value.
type Evidence struct {
	// Login is the username of GET /user.
	Login string
	// Scopes are the scopes of GET /personal_access_tokens/self. nil means unknown (the call
	// failed): an empty, non-nil slice means a token without scopes.
	Scopes []string
	// ExpiresAt is the expiry of the token; nil when it never expires or is unknown. Only the UTC
	// date counts.
	ExpiresAt *time.Time
	// Version is the version of GET /metadata ("17.5.1-ee"); "" means unknown (the call failed).
	Version string
}

// Result is the outcome of Evaluate.
type Result struct {
	// OK is false when the token cannot be used; Message then says why.
	OK bool
	// Message is "Connected as @<login>" on success, else the reason.
	Message string
	// TokenAccess is "" (null) when the scopes are unknown or the test failed.
	TokenAccess TokenAccess
	// Warnings are in the FR11 order; never nil.
	Warnings []Warning
}

// Evaluate judges the evidence of a test whose /user call succeeded (spec FR11, D6, D8, D34),
// at the time now (only its UTC date is used).
//
//   - Scopes unknown (nil) -> TokenAccess null and the warning scopes_unknown; the test still
//     passes (D34: a project or group token answers 400 on /personal_access_tokens/self).
//   - Scopes without api and without read_api -> not OK, MsgNeedsScope, no warnings.
//   - api -> full; read_api without api -> read_only and read_only_token.
//   - Scopes with none of api, read_repository and write_repository -> no_repository_scope.
//   - ExpiresAt within ExpiryWindowDays days of today (both ends included; a past date too, worded
//     as expired) -> token_expiring.
//   - A version below 17.0 -> version_unsupported; an empty or unparsable version ->
//     version_unknown.
//
// Warnings come in the order read_only_token, no_repository_scope, token_expiring,
// version_unsupported, scopes_unknown, version_unknown.
func Evaluate(e Evidence, now time.Time) Result {
	res := Result{Warnings: []Warning{}}
	has := func(s string) bool { return slices.Contains(e.Scopes, s) }

	if e.Scopes != nil && !has("api") && !has("read_api") {
		res.Message = MsgNeedsScope
		return res
	}
	res.OK = true
	res.Message = "Connected as @" + e.Login

	warn := func(code, msg string) { res.Warnings = append(res.Warnings, Warning{Code: code, Message: msg}) }

	if e.Scopes != nil {
		if has("api") {
			res.TokenAccess = AccessFull
		} else {
			res.TokenAccess = AccessReadOnly
			warn(WarnReadOnlyToken, "Read-only token: sync works; posting to merge requests and GitLab CI export will fail")
		}
		if !has("api") && !has("read_repository") && !has("write_repository") {
			warn(WarnNoRepositoryScope, "Without read_repository, private projects may not be cloned")
		}
	}

	if e.ExpiresAt != nil {
		if days := daysUntil(*e.ExpiresAt, now); days <= ExpiryWindowDays {
			date := e.ExpiresAt.UTC().Format(time.DateOnly)
			if days < 0 {
				warn(WarnTokenExpiring, "Token expired on "+date)
			} else {
				warn(WarnTokenExpiring, fmt.Sprintf("Token expires on %s (in %d days)", date, days))
			}
		}
	}

	versionKnown := false
	if e.Version != "" {
		if major, minor, ok := parseVersion(e.Version); ok {
			versionKnown = true
			if major < MinMajor || (major == MinMajor && minor < MinMinor) {
				warn(WarnVersionUnsupported, fmt.Sprintf(
					"GitLab %s is older than the supported %d.%d; some features may fail", e.Version, MinMajor, MinMinor))
			}
		}
	}

	if e.Scopes == nil {
		warn(WarnScopesUnknown, "The token's scopes could not be read")
	}
	if !versionKnown {
		warn(WarnVersionUnknown, "The GitLab version could not be read")
	}
	return res
}

// daysUntil is the number of UTC calendar days from now's date to t's date.
func daysUntil(t, now time.Time) int {
	ty, tm, td := t.UTC().Date()
	ny, nm, nd := now.UTC().Date()
	a := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	b := time.Date(ny, nm, nd, 0, 0, 0, 0, time.UTC)
	return int(a.Sub(b) / (24 * time.Hour))
}

// parseVersion reads major and minor from "17.5.1-ee", "16.11.3" or "17.0". Anything else (no
// leading digits, no dot, a number longer than 6 digits) is not a version.
func parseVersion(v string) (major, minor int, ok bool) {
	major, rest, ok := leadingInt(v)
	if !ok || rest == "" || rest[0] != '.' {
		return 0, 0, false
	}
	minor, _, ok = leadingInt(rest[1:])
	return major, minor, ok
}

func leadingInt(s string) (n int, rest string, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		if i >= 6 {
			return 0, "", false
		}
		n = n*10 + int(s[i]-'0')
		i++
	}
	return n, s[i:], i > 0
}
