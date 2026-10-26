package httpx

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/labstack/echo/v5"
)

// Zod issue codes used by the field helpers (the `code` of an Issue).
const (
	CodeInvalidType = "invalid_type"
	CodeInvalidEnum = "invalid_enum_value"
	CodeInvalidStr  = "invalid_string"
	CodeTooSmall    = "too_small"
	CodeTooBig      = "too_big"
	CodeCustom      = "custom"
)

// maxVersion is the largest :version value (a PostgreSQL integer).
const maxVersion = 1<<31 - 1

var (
	versionDigits  = regexp.MustCompile(`^[0-9]+$`)
	versionDecimal = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
)

// UTF16Len returns the length of s in UTF-16 code units, which is what JavaScript's
// String.length (and so the UI's Zod min/max checks) counts. An emoji counts 2.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r) // never -1: ranging over a string yields valid runes only
	}
	return n
}

// JSTrim removes leading and trailing white space the way JavaScript's String.prototype.trim
// does: Unicode Zs/Zl/Zp, tab, VT, FF, CR, LF and U+FEFF, but not U+0085.
func JSTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == 0xFEFF || (r != '\u0085' && unicode.IsSpace(r))
	})
}

// StringRule describes the checks of a Zod string: z.string().trim().min().max().regex().
// Lengths are UTF-16 code units. A zero Min or Max switches that check off.
type StringRule struct {
	Trim           bool           // trim (JSTrim) before checking; the trimmed value is returned
	Min, Max       int            // inclusive length bounds
	Pattern        *regexp.Regexp // value must match; nil = no pattern
	PatternMessage string         // message of the pattern issue; empty = "Invalid"
}

// Check applies the rule to value and returns the (possibly trimmed) value and every issue,
// in Zod's order: min, max, pattern. Issues are located at path.
func (r StringRule) Check(value string, path ...any) (string, []Issue) {
	if r.Trim {
		value = JSTrim(value)
	}
	var issues []Issue
	n := UTF16Len(value)
	if r.Min > 0 && n < r.Min {
		issues = append(issues, Issue{Code: CodeTooSmall, Path: path,
			Message: fmt.Sprintf("String must contain at least %d character(s)", r.Min)})
	}
	if r.Max > 0 && n > r.Max {
		issues = append(issues, Issue{Code: CodeTooBig, Path: path,
			Message: fmt.Sprintf("String must contain at most %d character(s)", r.Max)})
	}
	if r.Pattern != nil && !r.Pattern.MatchString(value) {
		msg := r.PatternMessage
		if msg == "" {
			msg = "Invalid"
		}
		issues = append(issues, Issue{Code: CodeInvalidStr, Path: path, Message: msg})
	}
	return value, issues
}

// RefineIssue is the issue of a failed cross-field rule (a Zod refine): the whole object is
// the location, so instancePath is "/".
func RefineIssue(message string) Issue {
	return Issue{Code: CodeCustom, Path: []any{}, Message: message}
}

// RequiredIssue is the issue of a missing required field.
func RequiredIssue(path ...any) Issue {
	return Issue{Code: CodeInvalidType, Path: path, Message: "Required"}
}

// TypeIssue is the issue of a value of the wrong JSON type: "Expected string, received number".
func TypeIssue(expected, received string, path ...any) Issue {
	return Issue{Code: CodeInvalidType, Path: path, Message: "Expected " + expected + ", received " + received}
}

// EnumIssue is the issue of a value outside a closed set of strings.
func EnumIssue(allowed []string, got string, path ...any) Issue {
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = "'" + a + "'"
	}
	return Issue{Code: CodeInvalidEnum, Path: path,
		Message: fmt.Sprintf("Invalid enum value. Expected %s, received '%s'", strings.Join(quoted, " | "), got)}
}

// PathVersion returns the path param name as a version number: decimal digits only, 1 up to
// 2,147,483,647. Anything else (including "1.5", "1e2", "0x10", "0") is the 422 validation error
// for that param.
func PathVersion(c *echo.Context, name string) (int, error) {
	v := c.Param(name)
	var issue Issue
	switch n, err := strconv.ParseInt(v, 10, 64); {
	case !versionDigits.MatchString(v):
		if versionDecimal.MatchString(v) {
			issue = TypeIssue("integer", "float", name)
		} else {
			issue = TypeIssue("number", "nan", name)
		}
	case err != nil || n > maxVersion:
		issue = Issue{Code: CodeTooBig, Path: []any{name}, Message: fmt.Sprintf("Number must be less than or equal to %d", maxVersion)}
	case n < 1:
		issue = Issue{Code: CodeTooSmall, Path: []any{name}, Message: "Number must be greater than 0"}
	default:
		return int(n), nil
	}
	return 0, NewValidationError(issue)
}

// PathEnum returns the path param name when it is one of allowed, otherwise the 422 validation
// error for that param.
func PathEnum(c *echo.Context, name string, allowed []string) (string, error) {
	v := c.Param(name)
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", NewValidationError(EnumIssue(allowed, v, name))
}

// QueryString reads an optional query parameter and checks it with rule. An absent parameter
// yields "" and no issue; a present one (even empty) is checked. Unknown query keys are ignored
// by simply not being read.
func QueryString(c *echo.Context, name string, rule StringRule) (string, []Issue) {
	values, ok := c.QueryParams()[name]
	if !ok || len(values) == 0 {
		return "", nil
	}
	return rule.Check(values[0], name)
}

// PathParam returns the path param name as the client meant it: percent-decoded exactly once.
// Echo routes on the raw path when the request has one (URL.RawPath is set when the client's
// encoding differs from Go's default, e.g. "custom%3Asynapse") and then leaves param values
// encoded; otherwise it routes on the decoded path and the value is already decoded. This makes
// "custom%3Asynapse" and "custom:synapse" equal, and "custom%253Asynapse" the literal
// "custom%3Asynapse" in both cases. A malformed escape ("%zz") returns the raw value, which no
// valid id matches. Relies on the router default (UnescapePathParamValues off).
func PathParam(c *echo.Context, name string) string {
	raw := c.Param(name)
	if c.Request().URL.RawPath == "" {
		return raw
	}
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}
