package domain_test

import (
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/onboarding/domain"
)

func TestRedactSecrets_EqualsNode(t *testing.T) {
	for i, c := range loadCases(t).Redact {
		if got := domain.RedactSecrets(c.In); got != c.Out {
			t.Errorf("case %d: RedactSecrets(%q) = %q, want %q", i, shorten(c.In), shorten(got), shorten(c.Out))
		}
	}
}

// The behaviours AC5050 names, with explicit expectations (the Node tests' own cases plus the
// YAML, CRLF and JSON ones).
func TestRedactSecrets_Behaviour(t *testing.T) {
	ref := "$" + "{POSTGRES_PASSWORD}"
	tests := []struct {
		name, in, want string
	}{
		{"url password", "DATABASE_URL: postgres://app:hunter2@db:5432/app", "DATABASE_URL: postgres://app:<redacted>@db:5432/app"},
		{"url without password", "see https://github.com/o/r and ssh://git@github.com/o/r", "see https://github.com/o/r and ssh://git@github.com/o/r"},
		{"url with variable", "postgres://app:" + "${PGPASSWORD}" + "@db/app", "postgres://app:" + "${PGPASSWORD}" + "@db/app"},
		{"key value keeps the key", "      POSTGRES_PASSWORD: s3cret", "      POSTGRES_PASSWORD: <redacted>"},
		{"list item", "      - API_KEY=abc123", "      - API_KEY=<redacted>"},
		{"export and quotes", `export AUTH_TOKEN="quoted-secret"`, "export AUTH_TOKEN=<redacted>"},
		{"reference stays", "DB_PASSWORD: " + ref, "DB_PASSWORD: " + ref},
		{"benign max_tokens", "max_tokens: 4096", "max_tokens: 4096"},
		{"benign json", `{"max_tokens": 100, "model": "x"}`, `{"max_tokens": 100, "model": "x"}`},
		{"embedded json pair", `{"user":"a","api_key": "k-123456"}`, `{"user":"a","api_key": "<redacted>"}`},
		{"yaml flow map", "environment: {PASSWORD: hunter2, USER: app}", "environment: {PASSWORD: <redacted>, USER: app}"},
		{"authorization header", `curl -H "Authorization: Bearer abc123def" https://x`, `curl -H "Authorization: <redacted>" https://x`},
		{"block scalar", "config:\n  password: |\n    one\n\n    two\n  user: app", "config:\n  password: |\n    <redacted>\n\n    <redacted>\n  user: app"},
		{"block scalar CRLF", "secret: >\r\n  body\r\nnext: 1\r\n", "secret: >\r\n  <redacted>\r\nnext: 1\r\n"},
		{"CRLF value", "password: x\r\nname: y\r\n", "password: <redacted>\r\nname: y\r\n"},
		{"private key", "a\n-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----\nb", "a\n<redacted>\nb"},
		{"token pattern", "key sk-abcdefghijklmnop1234 here", "key <redacted> here"},
		{"ordinary text", "Run `pnpm dev`.\nrisk-assessment and desk-booking-system stay.", "Run `pnpm dev`.\nrisk-assessment and desk-booking-system stay."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.RedactSecrets(tc.in)
			if got != tc.want {
				t.Errorf("RedactSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if again := domain.RedactSecrets(got); again != got {
				t.Errorf("RedactSecrets is not idempotent on %q: second pass gave %q", got, again)
			}
		})
	}
}

// A hostile README (a blob is at most 512 KiB; this is a quarter) must not take quadratic time (minutes at this size;
// -race makes the linear case ten times slower, hence the generous bound): a long hyphenated word, repeated
// quotes and a long run of key characters (INSIGHTS: hostile scans, one pass per file).
func TestRedactSecrets_HostileInputsAreLinear(t *testing.T) {
	const size = 128 * 1024
	inputs := map[string]string{
		"hyphen chain":     strings.Repeat("a-", size/2),
		"quotes":           strings.Repeat(`"`, size),
		"key without sep":  strings.Repeat("k", size),
		"key then spaces":  "k" + strings.Repeat(" ", size),
		"unterminated":     `password="` + strings.Repeat("a", size),
		"many keys":        strings.Repeat("k=", size/2),
		"many lines":       strings.Repeat("x\n", size/2),
		"dots":             strings.Repeat("a.", size/2),
		"quote key chain":  strings.Repeat(`"a-`, size/3),
		"userinfo schemes": strings.Repeat("a://b:", size/6),
		"value quote runs": strings.Repeat(`a:"\`, size/4),
		"mixed quotes":     `a:'` + strings.Repeat(`b:"\`, size/4),
		"bearer pairs":     strings.Repeat("a: bearer ", size/10),
		"userinfo colons":  "a://" + strings.Repeat("b:", size/2),
		"private keys":     strings.Repeat("-----BEGIN PRIVATE KEY-----", size/27),
		"camel key":        "password" + strings.Repeat("Ab", size/2) + "=x",
		"unicode spaces":   strings.Repeat("a \u2003", size/5),
		"spaced pairs":     strings.Repeat("a"+strings.Repeat(" ", 50)+":", size/52),
		"block bodies":     "password: |\n" + strings.Repeat("  x\n", size/4),
		"unclosed refs":    strings.Repeat("k=${x ", size/6),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			domain.RedactSecrets(in)
			if d := time.Since(start); d > 20*time.Second {
				t.Errorf("RedactSecrets took %v on %d bytes", d, len(in))
			}
		})
	}
}

// A run of `${` (F2 of the Phase 5 security review): each pair's `${...}` scan ran to the end of the
// line before failing, so 512 KiB of `a:${` took a minute and 256 KiB (below) 15 s. The failed scan is now remembered, and
// the output is unchanged (the Node cases above and the differential below).
func TestRedactSecrets_RunsOfDollarBraceAreLinear(t *testing.T) {
	const size = 256 * 1024
	inputs := map[string]string{
		"pairs":          strings.Repeat("a:${", size/4),
		"secret keys":    strings.Repeat("token=${", size/8),
		"closed at end":  strings.Repeat("a:${", size/4) + "}",
		"after a space":  strings.Repeat("a:${", size/4) + " b:${}",
		"in a long line": strings.Repeat("x ", 100) + strings.Repeat("a:${", size/4),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			domain.RedactSecrets(in)
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("RedactSecrets took %v on %d bytes, want linear time (well under a second without -race)", d, len(in))
			}
		})
	}
}

// The remembered failed scan must not change what is redacted: a `${` scan that ended at a space
// or the line end hides later references that start before that point only.
func TestRedactSecrets_FailedDollarScansDoNotHideLaterMatches(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"closed after failures", "a:${ token=${x}", "a:${ token=${x}"},
		{"secret after a space", "a:${ password=hunter2", "a:${ password=<redacted>"},
		{"nested openers then closed", "see token=${${${x} y", "see token=<redacted> y"},
		{"unclosed secret value", "password=${abc", "password=<redacted>"},
		{"unclosed twice", "api_key:${a:${b", "api_key:<redacted>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.RedactSecrets(tc.in); got != tc.want {
				t.Errorf("RedactSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func FuzzRedactSecrets(f *testing.F) {
	for _, s := range []string{"password: x", `{"token":"a"}`, "a: |\n  b", "x=1 api_key=$A", `"k": 'v'`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = domain.RedactSecrets(s)
	})
}
