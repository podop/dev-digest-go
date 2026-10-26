package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// The agent providers an export can carry (FR24, FR35): the three built-ins, or a custom provider
// whose id is "custom:<slug>".
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	// CustomPrefix starts the id of a custom provider.
	CustomPrefix = "custom:"
)

// MaxExtraHeaders is the largest number of extra headers a custom provider has (FR1); each one
// travels in its own CI secret (FR26).
const MaxExtraHeaders = 5

// The built-in secret names (spec 7): the studio's own key names for these providers.
const (
	SecretOpenRouter = "OPENROUTER_API_KEY"
	SecretOpenAI     = "OPENAI_API_KEY"
	SecretAnthropic  = "ANTHROPIC_API_KEY"
)

// customSlugPattern is the slug shape of FR1.
var customSlugPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// CustomSlug returns the slug of a custom provider id "custom:<slug>", or false when provider is
// not such an id (FR1: the slug matches ^[a-z0-9-]{1,32}$).
func CustomSlug(provider string) (string, bool) {
	slug, ok := strings.CutPrefix(provider, CustomPrefix)
	if !ok || !customSlugPattern.MatchString(slug) {
		return "", false
	}
	return slug, true
}

// IsCustomProvider reports whether provider is a well-formed custom provider id.
func IsCustomProvider(provider string) bool {
	_, ok := CustomSlug(provider)
	return ok
}

// EnvName is <S> of FR26: the slug upper-cased with "-" replaced by "_". It is the middle part of
// the derived names DEVDIGEST_<S>_BASE_URL, DEVDIGEST_<S>_API_KEY and DEVDIGEST_<S>_HEADER_<n>
// (always matching ^DEVDIGEST_[A-Z0-9_]{1,32}_(BASE_URL|API_KEY|HEADER_[1-5])$). The mapping is
// injective because "_" is not a slug character (EC26). It reports false for a string that is not
// a slug, so no user text reaches a workflow through it (NFR8).
func EnvName(slug string) (string, bool) {
	if !customSlugPattern.MatchString(slug) {
		return "", false
	}
	return strings.ToUpper(strings.ReplaceAll(slug, "-", "_")), true
}

// BaseURLVariable is the repository variable that holds a custom provider's base URL.
func BaseURLVariable(envName string) string { return "DEVDIGEST_" + envName + "_BASE_URL" }

// APIKeySecret is the repository secret that holds a custom provider's key.
func APIKeySecret(envName string) string { return "DEVDIGEST_" + envName + "_API_KEY" }

// HeaderSecret is the repository secret that holds the value of the n-th (1-based) extra header.
func HeaderSecret(envName string, n int) string {
	return "DEVDIGEST_" + envName + "_HEADER_" + strconv.Itoa(n)
}

// builtinSecret is the fixed secret name of a built-in provider.
func builtinSecret(provider string) (string, bool) {
	switch provider {
	case ProviderOpenRouter:
		return SecretOpenRouter, true
	case ProviderOpenAI:
		return SecretOpenAI, true
	case ProviderAnthropic:
		return SecretAnthropic, true
	}
	return "", false
}

// SecretNames are the secret names the workflow of an export on provider may reference, except
// GITHUB_TOKEN, which every workflow may reference (FR28): the fixed key name of a built-in, or
// DEVDIGEST_<S>_API_KEY and DEVDIGEST_<S>_HEADER_1…5 of a custom provider. It reports false for a
// provider that is neither.
func SecretNames(provider string) ([]string, bool) {
	if name, ok := builtinSecret(provider); ok {
		return []string{name}, true
	}
	slug, ok := CustomSlug(provider)
	if !ok {
		return nil, false
	}
	env, _ := EnvName(slug)
	names := []string{APIKeySecret(env)}
	for n := 1; n <= MaxExtraHeaders; n++ {
		names = append(names, HeaderSecret(env, n))
	}
	return names, true
}

// AllowedSecretNames are the secret names an exported (or edited) workflow of provider may read
// (FR28, AC33): the key of a built-in; for a custom provider DEVDIGEST_<S>_API_KEY plus
// DEVDIGEST_<S>_HEADER_<n> for the provider's own headerCount extra headers only (capped at
// MaxExtraHeaders), so a header the provider does not define stays refused. GITHUB_TOKEN is
// always allowed by wfrules.CheckWith.
func AllowedSecretNames(provider string, headerCount int) ([]string, bool) {
	names, ok := SecretNames(provider)
	if !ok || !IsCustomProvider(provider) {
		return names, ok
	}
	return names[:1+min(max(headerCount, 0), MaxExtraHeaders)], true
}

// RequiredSecrets are the secrets the user must add before the workflow works (FR29,
// GITHUB_TOKEN excluded): the key of a built-in; for a custom provider DEVDIGEST_<S>_API_KEY only
// when keyConfigured, plus DEVDIGEST_<S>_HEADER_<n> for each of headerCount extra headers (capped
// at MaxExtraHeaders). It is never nil for a valid provider, and nil for an invalid one.
func RequiredSecrets(provider string, keyConfigured bool, headerCount int) []string {
	if name, ok := builtinSecret(provider); ok {
		return []string{name}
	}
	slug, ok := CustomSlug(provider)
	if !ok {
		return nil
	}
	env, _ := EnvName(slug)
	out := []string{}
	if keyConfigured {
		out = append(out, APIKeySecret(env))
	}
	for n := 1; n <= min(max(headerCount, 0), MaxExtraHeaders); n++ {
		out = append(out, HeaderSecret(env, n))
	}
	return out
}

// RequiredVariables are the repository variables the user must add (FR29):
// DEVDIGEST_<S>_BASE_URL for a custom provider, none (an empty, non-nil list) for a built-in. It
// is nil for an invalid provider.
func RequiredVariables(provider string) []string {
	if _, ok := builtinSecret(provider); ok {
		return []string{}
	}
	slug, ok := CustomSlug(provider)
	if !ok {
		return nil
	}
	env, _ := EnvName(slug)
	return []string{BaseURLVariable(env)}
}

// Runner is where the generated job runs (FR25). The empty value is GitHubHosted.
type Runner string

// The runners an export body accepts.
const (
	RunnerGitHubHosted Runner = "github_hosted"
	RunnerSelfHosted   Runner = "self_hosted"
)

// RunsOnSelfHosted is the runs-on value of RunnerSelfHosted; RunnerGitHubHosted uses RunnerOS.
const RunsOnSelfHosted = "[self-hosted, linux, x64]"

// Valid reports whether r is one of the two runners or empty (the default).
func (r Runner) Valid() bool {
	return r == "" || r == RunnerGitHubHosted || r == RunnerSelfHosted
}

// RunsOn is the text written after "runs-on: ": the only values that reach the workflow from the
// runner choice (NFR8). It reports false for an unknown runner.
func (r Runner) RunsOn() (string, bool) {
	switch r {
	case "", RunnerGitHubHosted:
		return RunnerOS, true
	case RunnerSelfHosted:
		return RunsOnSelfHosted, true
	}
	return "", false
}
