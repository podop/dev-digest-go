package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/domain"
)

func set(v string) domain.EnvValue { return domain.EnvValue{Value: v, Set: true} }

var unset = domain.EnvValue{}

func TestResolvePRContext(t *testing.T) {
	event := `{"pull_request":{"number":7,"title":"Fix it","body":"Because.","head":{"repo":{"fork":true}}}}`
	tests := []struct {
		name       string
		repository domain.EnvValue
		number     domain.EnvValue
		event      string
		want       domain.PRContext
	}{
		{"env number and event", set("acme/widgets"), set("42"), event,
			domain.PRContext{Owner: "acme", Repo: "widgets", Number: 42, Title: "Fix it", Body: "Because.", IsFork: true}},
		{"event number when PR_NUMBER is unset", set("acme/widgets"), unset, event,
			domain.PRContext{Owner: "acme", Repo: "widgets", Number: 7, Title: "Fix it", Body: "Because.", IsFork: true}},
		{"no event payload", set("a/b"), set("3"), "",
			domain.PRContext{Owner: "a", Repo: "b", Number: 3}},
		{"invalid event payload is ignored", set("a/b"), set("3"), "{not json",
			domain.PRContext{Owner: "a", Repo: "b", Number: 3}},
		{"event that is not an object", set("a/b"), set("3"), `[1,2]`,
			domain.PRContext{Owner: "a", Repo: "b", Number: 3}},
		{"null title and body", set("a/b"), set("3"), `{"pull_request":{"title":null,"body":null}}`,
			domain.PRContext{Owner: "a", Repo: "b", Number: 3}},
		{"keys match exactly", set("a/b"), set("3"), `{"Pull_Request":{"title":"x"}}`,
			domain.PRContext{Owner: "a", Repo: "b", Number: 3}},
		{"fork flag of a deleted fork repo", set("a/b"), set("3"), `{"pull_request":{"head":{"repo":null}}}`,
			domain.PRContext{Owner: "a", Repo: "b", Number: 3}},
		{"event number as a string", set("a/b"), unset, `{"pull_request":{"number":"9"}}`,
			domain.PRContext{Owner: "a", Repo: "b", Number: 9}},
		{"hex, exponent and padded numbers read like Number()", set("a/b"), set(" 0x10 "), "",
			domain.PRContext{Owner: "a", Repo: "b", Number: 16}},
		{"exponent", set("a/b"), set("1e3"), "", domain.PRContext{Owner: "a", Repo: "b", Number: 1000}},
		{"trailing dot", set("a/b"), set("5."), "", domain.PRContext{Owner: "a", Repo: "b", Number: 5}},
		{"leading zeros", set("a/b"), set("00012"), "", domain.PRContext{Owner: "a", Repo: "b", Number: 12}},
		{"title that is a number", set("a/b"), set("3"), `{"pull_request":{"title":1e21,"body":[1,null,[2,3],{}]}}`,
			domain.PRContext{Owner: "a", Repo: "b", Number: 3, Title: "1e+21", Body: "1,,2,3,[object Object]"}},
		{"third path part is dropped like split('/', 2)", set("a/b/c"), set("3"), "",
			domain.PRContext{Owner: "a", Repo: "b", Number: 3}},
		{"empty owner and repo", set("/"), set("3"), "", domain.PRContext{Number: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ResolvePRContext(tc.repository, tc.number, []byte(tc.event))
			if err != nil {
				t.Fatalf("ResolvePRContext() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ResolvePRContext() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResolvePRContext_Errors(t *testing.T) {
	const noNumber = "PR_NUMBER must resolve to a positive integer (env PR_NUMBER="
	tests := []struct {
		name       string
		repository domain.EnvValue
		number     domain.EnvValue
		event      string
		want       string
	}{
		{"repository unset", unset, set("1"), "", `GITHUB_REPOSITORY must be set to "owner/name" (got: undefined)`},
		{"repository empty", set(""), set("1"), "", `GITHUB_REPOSITORY must be set to "owner/name" (got: "")`},
		{"repository without slash", set("widgets"), set("1"), "", `GITHUB_REPOSITORY must be set to "owner/name" (got: "widgets")`},
		{"repository text is JSON-escaped", set("a\"b\n"), set("1"), "", `GITHUB_REPOSITORY must be set to "owner/name" (got: "a\"b\n")`},
		{"no number anywhere", set("a/b"), unset, "", noNumber + "undefined, event pull_request.number=undefined)"},
		{"not a number", set("a/b"), set("12abc"), "", noNumber + `"12abc", event pull_request.number=undefined)`},
		{"empty env number wins over the event", set("a/b"), set(""), `{"pull_request":{"number":5}}`, noNumber + `"", event pull_request.number=5)`},
		{"zero", set("a/b"), set("0"), "", noNumber + `"0", event pull_request.number=undefined)`},
		{"negative", set("a/b"), set("-5"), "", noNumber + `"-5", event pull_request.number=undefined)`},
		{"fraction", set("a/b"), set("5.5"), "", noNumber + `"5.5", event pull_request.number=undefined)`},
		{"underscore", set("a/b"), set("1_0"), "", noNumber + `"1_0", event pull_request.number=undefined)`},
		{"infinity", set("a/b"), set("Infinity"), "", noNumber + `"Infinity", event pull_request.number=undefined)`},
		{"beyond a safe integer", set("a/b"), set("9007199254740992"), "", noNumber + `"9007199254740992", event pull_request.number=undefined)`},
		{"event number null", set("a/b"), unset, `{"pull_request":{"number":null}}`, noNumber + "undefined, event pull_request.number=null)"},
		{"event number an object", set("a/b"), unset, `{"pull_request":{"number":{"a": 1}}}`, noNumber + `undefined, event pull_request.number={"a":1})`},
		{"event number a fraction", set("a/b"), unset, `{"pull_request":{"number":1.5}}`, noNumber + "undefined, event pull_request.number=1.5)"},
		{"event number a string", set("a/b"), unset, `{"pull_request":{"number":"x"}}`, noNumber + `undefined, event pull_request.number="x")`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ResolvePRContext(tc.repository, tc.number, []byte(tc.event))
			if err == nil || err.Error() != tc.want {
				t.Errorf("ResolvePRContext() error = %v, want %q", err, tc.want)
			}
			var re *domain.RunnerError
			if !errors.As(err, &re) {
				t.Errorf("ResolvePRContext() error = %T, want *RunnerError", err)
			}
		})
	}
}

func TestValidSlug(t *testing.T) {
	tests := []struct {
		slug string
		want bool
	}{
		{"a", true},
		{"0", true},
		{"code-review", true},
		{"a-", true},
		{strings.Repeat("a", 40), true},
		{strings.Repeat("a", 41), false},
		{"", false},
		{"-a", false},
		{"A", false},
		{"a_b", false},
		{"a.b", false},
		{"a b", false},
		{"../etc/passwd", false},
		{"a/b", false},
		{`a\b`, false},
		{"a\n", false},
		{"é", false},
	}
	for _, tc := range tests {
		if got := domain.ValidSlug(tc.slug); got != tc.want {
			t.Errorf("ValidSlug(%q) = %v, want %v", tc.slug, got, tc.want)
		}
	}
}

func TestSlugErrors(t *testing.T) {
	if err := domain.CheckSkillSlug("good-one"); err != nil {
		t.Errorf("CheckSkillSlug(good-one) = %v, want nil", err)
	}
	err := domain.CheckSkillSlug("../x")
	if err == nil || err.Error() != "Skill slug '../x' is not a valid file name" {
		t.Errorf("CheckSkillSlug(../x) = %v, want the Node-style text", err)
	}
	want := `DEVDIGEST_AGENT must match /^[a-z0-9][a-z0-9-]{0,39}$/ (got: "Bad Agent")`
	if got := domain.AgentSlugError("Bad Agent").Error(); got != want {
		t.Errorf("AgentSlugError() = %q, want %q", got, want)
	}
}
