// Package custom holds the pure rules of user-defined model providers: the id format, the
// protocol, auth and structured-output vocabularies, and the base URL, header and address rules
// (spec FR1, FR3, FR4). It imports the standard library only, so the studio server and the CI
// runner apply the same rules.
package custom

import "strings"

// IDPrefix starts every custom provider id (`custom:<slug>`).
const IDPrefix = "custom:"

// MaxSlugLen is the longest slug of a custom provider id.
const MaxSlugLen = 32

// ParseID splits a custom provider id into its slug. ok is false for anything but
// `custom:` followed by 1-32 characters of [a-z0-9-] (so `custom:Syn`, `custom:`, `custom:a_b`,
// `synapse` and `openai` are refused).
func ParseID(s string) (slug string, ok bool) {
	slug, found := strings.CutPrefix(s, IDPrefix)
	if !found || len(slug) == 0 || len(slug) > MaxSlugLen {
		return "", false
	}
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return "", false
		}
	}
	return slug, true
}

// ID builds the provider id of a slug. It does not validate the slug.
func ID(slug string) string { return IDPrefix + slug }

// Protocol is the wire protocol a custom provider speaks.
type Protocol string

// The protocols.
const (
	// ProtocolOpenAI is chat completions plus a model list.
	ProtocolOpenAI Protocol = "openai"
	// ProtocolAnthropic is the Messages API plus a model list.
	ProtocolAnthropic Protocol = "anthropic"
)

// Valid reports whether p is a known protocol.
func (p Protocol) Valid() bool { return p == ProtocolOpenAI || p == ProtocolAnthropic }

// DefaultAuthMode is the auth mode used when none is configured: bearer for openai, header for
// anthropic. Unknown protocols get bearer.
func (p Protocol) DefaultAuthMode() AuthMode {
	if p == ProtocolAnthropic {
		return AuthHeader
	}
	return AuthBearer
}

// DefaultAuthHeader is the header name used by AuthHeader when none is configured.
const DefaultAuthHeader = "x-api-key"

// AuthMode says how the API key is sent.
type AuthMode string

// The auth modes.
const (
	// AuthBearer sends `Authorization: Bearer <key>`.
	AuthBearer AuthMode = "bearer"
	// AuthHeader sends `<auth_header>: <key>`.
	AuthHeader AuthMode = "header"
)

// Valid reports whether m is a known auth mode.
func (m AuthMode) Valid() bool { return m == AuthBearer || m == AuthHeader }

// Mode is the structured-output mode of a custom provider (spec FR33). The empty Mode means
// "not a custom provider" (built-in adapters ignore it).
type Mode string

// The structured-output modes.
const (
	// ModeStrict sends the strict JSON schema (response_format json_schema, or the forced tool).
	ModeStrict Mode = "strict"
	// ModeJSONObject asks for a JSON object without a schema (protocol openai only).
	ModeJSONObject Mode = "json_object"
	// ModePromptOnly sends no structured-output field; the schema travels in the prompt.
	ModePromptOnly Mode = "prompt_only"
)

// DefaultMode is the mode used when none is given.
const DefaultMode = ModeStrict

// Valid reports whether m is one of the three modes (the empty Mode is not valid).
func (m Mode) Valid() bool { return m == ModeStrict || m == ModeJSONObject || m == ModePromptOnly }

// ValidFor reports whether m is allowed with protocol p: json_object needs openai.
func (m Mode) ValidFor(p Protocol) bool {
	if !m.Valid() || !p.Valid() {
		return false
	}
	return m != ModeJSONObject || p == ProtocolOpenAI
}
