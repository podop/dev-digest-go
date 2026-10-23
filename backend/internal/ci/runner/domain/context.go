package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// MaxConfigFileBytes caps the manifest file and each skill file the runner reads (spec 7.8.4,
// D6018); a larger file is a hard failure. The Node runner read them unbounded.
const MaxConfigFileBytes = 1 << 20

const (
	// slugMaxLength is the length cap of an agent or skill slug.
	slugMaxLength = 40
	// AgentSlugPattern is the pattern text of a DEVDIGEST_AGENT value and of a skill slug, as
	// the Node runner prints it.
	AgentSlugPattern = "/^[a-z0-9][a-z0-9-]{0,39}$/"
	// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER.
	maxSafeInteger = 1<<53 - 1
)

// ValidSlug reports whether s matches ^[a-z0-9][a-z0-9-]{0,39}$: the slug the export derives from
// an agent or skill name. A slug is a file name, so a match also means no path separator, no dot
// and no space.
func ValidSlug(s string) bool {
	if s == "" || len(s) > slugMaxLength {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// AgentSlugError is the failure for a DEVDIGEST_AGENT value that is not a slug.
func AgentSlugError(value string) *RunnerError {
	return NewRunnerError("DEVDIGEST_AGENT must match " + AgentSlugPattern + " (got: " + JSONString(value) + ")")
}

// CheckSkillSlug rejects a skill slug of the manifest that is not a valid slug, before any file
// is read (D6018): the Node runner joined whatever the manifest said into a path.
func CheckSkillSlug(slug string) error {
	if !ValidSlug(slug) {
		return NewRunnerError("Skill slug '" + slug + "' is not a valid file name")
	}
	return nil
}

// EnvValue is an environment variable: Set tells "unset" from "set to the empty string", which
// the Node texts print differently.
type EnvValue struct {
	Value string
	Set   bool
}

// PRContext is the pull request under review, resolved from the environment and the event
// payload.
type PRContext struct {
	Owner, Repo string
	Number      int
	// Title and Body are untrusted, author-controlled text; missing = "".
	Title, Body string
	// IsFork is informational only: the workflow itself never schedules the job for a fork.
	IsFork bool
}

// ResolvePRContext resolves the context from GITHUB_REPOSITORY, PR_NUMBER and the content of the
// file GITHUB_EVENT_PATH names (nil when unset or unreadable; an unparseable payload counts as
// none). The repository must contain "/"; like the Node split("/", 2), owner is the text before
// the first "/" and repo the text up to the next one. PR_NUMBER, when set, wins over the payload's
// pull_request.number; the number must resolve to a positive integer.
func ResolvePRContext(repository, prNumber EnvValue, eventJSON []byte) (PRContext, error) {
	if !repository.Set || repository.Value == "" || !strings.Contains(repository.Value, "/") {
		got := "undefined"
		if repository.Set {
			got = JSONString(repository.Value)
		}
		return PRContext{}, NewRunnerError(`GITHUB_REPOSITORY must be set to "owner/name" (got: ` + got + ")")
	}
	parts := strings.SplitN(repository.Value, "/", 3)
	owner, repo := parts[0], parts[1]

	pr := pullRequestOf(eventJSON)
	eventNumber := pr["number"]

	var raw string
	var haveRaw bool
	if prNumber.Set {
		raw, haveRaw = prNumber.Value, true
	} else if s, ok := jsString(eventNumber); ok {
		raw, haveRaw = s, true
	}
	n := math.NaN()
	if haveRaw && raw != "" {
		n = jsToNumber(raw)
	}
	if math.IsNaN(n) || n != math.Trunc(n) || n <= 0 || n > maxSafeInteger {
		envText := "undefined"
		if prNumber.Set {
			envText = JSONString(prNumber.Value)
		}
		return PRContext{}, NewRunnerError("PR_NUMBER must resolve to a positive integer (env PR_NUMBER=" + envText +
			", event pull_request.number=" + jsonStringify(eventNumber) + ")")
	}

	title, _ := jsString(pr["title"])
	body, _ := jsString(pr["body"])
	var fork bool
	_ = json.Unmarshal(objectField(objectField(pr["head"])["repo"])["fork"], &fork)
	return PRContext{Owner: owner, Repo: repo, Number: int(n), Title: title, Body: body, IsFork: fork}, nil
}

// pullRequestOf returns the fields of event.pull_request; empty when the payload is missing, is
// not JSON, or holds no object there. Keys match exactly (encoding/json's struct decoding would
// fold case).
func pullRequestOf(eventJSON []byte) map[string]json.RawMessage {
	var event map[string]json.RawMessage
	if json.Unmarshal(eventJSON, &event) != nil {
		return nil
	}
	return objectField(event["pull_request"])
}

// objectField decodes a JSON object into its raw members; nil for anything else.
func objectField(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// jsString is JavaScript's String(value) of a decoded JSON value; false for undefined and null
// (the operands of "??").
func jsString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	case 't':
		return "true", true
	case 'f':
		return "false", true
	case '{':
		return "[object Object]", true
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return "", false
		}
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i], _ = jsString(it) // null and undefined join as ""
		}
		return strings.Join(parts, ","), true
	}
	f, _ := strconv.ParseFloat(string(raw), 64) // out of range gives ±Inf, as JSON.parse does
	return reviewer.JSNumber(f), true
}

// jsonStringify is JSON.stringify of a decoded JSON value as the Node message prints it:
// "undefined" for an absent value. Objects and arrays are printed in their compact source form.
func jsonStringify(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "undefined"
	}
	switch text[0] {
	case '"':
		s, _ := jsString(raw)
		return JSONString(s)
	case '{', '[':
		var buf bytes.Buffer
		if json.Compact(&buf, raw) != nil {
			return "undefined"
		}
		return buf.String()
	case 'n', 't', 'f':
		return text
	}
	s, _ := jsString(raw)
	return s
}

// decimalNumber is the StrDecimalLiteral of ECMAScript without Infinity.
var decimalNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// jsToNumber is JavaScript's Number(string): surrounding white space (JavaScript's set) is
// ignored, the empty string is 0, 0x, 0o and 0b literals and decimals with an exponent are read,
// "Infinity" is infinite, everything else is NaN.
func jsToNumber(s string) float64 {
	s = reviewer.JSTrim(s)
	switch s {
	case "":
		return 0
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			u, err := strconv.ParseUint(s[2:], base, 64)
			switch {
			case err == nil:
				return float64(u)
			case errors.Is(err, strconv.ErrRange):
				return math.Inf(1)
			}
			return math.NaN()
		}
	}
	if !decimalNumber.MatchString(s) {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(s, 64) // an overflow gives ±Inf with an error, as Number does
	return f
}
