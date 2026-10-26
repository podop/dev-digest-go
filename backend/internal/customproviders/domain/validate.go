package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"dev-digest-go/backend/internal/llm/custom"
)

// Zod-style issue codes of the issues below (the `code` of an Issue).
const (
	CodeInvalidType = "invalid_type"
	CodeInvalidEnum = "invalid_enum_value"
	CodeInvalidStr  = "invalid_string"
	CodeTooSmall    = "too_small"
	CodeTooBig      = "too_big"
	CodeCustom      = "custom"
)

// Issue is one broken rule of a request, located by Path (strings and ints, as in Zod).
type Issue struct {
	Code    string
	Path    []any
	Message string
}

// KeyOp says what a request does with the API key.
type KeyOp int

// The key operations. The zero value is KeyKeep: the field was omitted.
const (
	// KeyKeep leaves the stored key as it is (on create: no key).
	KeyKeep KeyOp = iota
	// KeyRemove removes the stored key (`api_key: null`).
	KeyRemove
	// KeyReplace stores KeyChange.Value.
	KeyReplace
)

// KeyChange is the `api_key` field of a request.
type KeyChange struct {
	Op    KeyOp
	Value string
}

// HeaderInput is an extra header of a request. Value is nil when the request lists the name
// without a value: the stored value is kept (spec FR5).
type HeaderInput struct {
	Name  string
	Value *string
}

// Input is a create or update request as the transport decoded it: types are right, nothing else
// is checked. An empty string or nil means "omitted".
type Input struct {
	ID               string
	Name             string
	Protocol         custom.Protocol
	BaseURL          string
	AuthMode         custom.AuthMode
	AuthHeader       *string
	StructuredOutput custom.Mode
	APIKey           KeyChange
	ExtraHeaders     []HeaderInput
	Models           []Model
}

// Draft is a validated, normalized request: the definition to store (without timestamps), the
// headers with their optional values, and the key operation.
type Draft struct {
	Provider Provider
	Headers  []HeaderInput
	Key      KeyChange
}

// Build returns the provider to store, stamped with the given times.
func (d Draft) Build(createdAt, updatedAt time.Time) Provider {
	p := d.Provider
	p.CreatedAt, p.UpdatedAt = createdAt, updatedAt
	return p
}

// Validate checks a request against spec FR1, FR3, FR4 and FR5 and returns the normalized draft,
// or every issue found. It checks nothing that needs stored state: a header without a value is
// resolved by MergeHeaders. On update the caller passes the id of the route in Input.ID.
func Validate(in Input) (Draft, []Issue) {
	v := validator{}
	var d Draft

	slug, ok := custom.ParseID(in.ID)
	if !ok {
		v.add(CodeInvalidStr, "Invalid custom provider id: expected custom:<slug> with 1-32 characters of a-z, 0-9 and -", "id")
	}

	name := jsTrim(in.Name)
	switch n := utf16Len(name); {
	case n < 1:
		v.add(CodeTooSmall, "String must contain at least 1 character(s)", "name")
	case n > MaxNameLen:
		v.add(CodeTooBig, fmt.Sprintf("String must contain at most %d character(s)", MaxNameLen), "name")
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		v.add(CodeInvalidStr, "Name must not contain control characters", "name")
	}

	protocolOK := in.Protocol.Valid()
	if !protocolOK {
		v.enum("protocol", []string{string(custom.ProtocolOpenAI), string(custom.ProtocolAnthropic)}, string(in.Protocol))
	}

	var baseURL string
	if in.BaseURL == "" {
		v.add(CodeInvalidType, "Required", "base_url")
	} else if norm, _, msg := custom.CheckBaseURL(in.BaseURL); msg != "" {
		v.add(CodeCustom, msg, "base_url")
	} else {
		baseURL = norm
	}

	authMode, authHeader := v.auth(in, protocolOK)
	mode := v.structuredOutput(in, protocolOK)
	key := v.key(in.APIKey)
	headers := v.headers(in.ExtraHeaders, authHeader)
	models := v.models(in.Models)

	if len(v.issues) > 0 {
		return Draft{}, v.issues
	}
	names := make([]string, len(headers))
	for i, h := range headers {
		names[i] = h.Name
	}
	d.Provider = Provider{
		ID: custom.ID(slug), Name: name, Protocol: in.Protocol, BaseURL: baseURL,
		AuthMode: authMode, AuthHeader: authHeader, StructuredOutput: mode,
		ExtraHeaderNames: names, Models: models,
	}
	d.Headers, d.Key = headers, key
	return d, nil
}

// ValidateKey checks a key sent on its own (the connection test): 1-4,096 characters without CR,
// LF or NUL. It returns "" when acceptable, else the message of the 422.
func ValidateKey(key string) string {
	_, msg := keyIssue(key)
	return msg
}

func keyIssue(key string) (code, msg string) {
	switch n := utf16Len(key); {
	case n < 1:
		return CodeTooSmall, "String must contain at least 1 character(s)"
	case n > MaxKeyLen:
		return CodeTooBig, fmt.Sprintf("String must contain at most %d character(s)", MaxKeyLen)
	case strings.ContainsAny(key, "\r\n\x00"):
		return CodeCustom, "API key must not contain CR, LF or NUL"
	}
	return "", ""
}

type validator struct{ issues []Issue }

func (v *validator) add(code, message string, path ...any) {
	v.issues = append(v.issues, Issue{Code: code, Path: path, Message: message})
}

func (v *validator) enum(field string, allowed []string, got string) {
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = "'" + a + "'"
	}
	v.add(CodeInvalidEnum, fmt.Sprintf("Invalid enum value. Expected %s, received '%s'", strings.Join(quoted, " | "), got), field)
}

// auth resolves the auth mode and header (spec FR4): the mode defaults by protocol; the header is
// required in header mode (anthropic defaults to x-api-key) and dropped for bearer.
func (v *validator) auth(in Input, protocolOK bool) (custom.AuthMode, string) {
	mode := in.AuthMode
	switch {
	case mode == "" && protocolOK:
		mode = in.Protocol.DefaultAuthMode()
	case mode != "" && !mode.Valid():
		v.enum("auth_mode", []string{string(custom.AuthBearer), string(custom.AuthHeader)}, string(mode))
		return "", ""
	case mode == "":
		return "", ""
	}
	if mode != custom.AuthHeader {
		return mode, ""
	}
	header := ""
	if in.AuthHeader != nil {
		header = *in.AuthHeader
	}
	if header == "" {
		if in.Protocol != custom.ProtocolAnthropic {
			v.add(CodeInvalidType, "Auth header is required in header mode", "auth_header")
			return mode, ""
		}
		header = custom.DefaultAuthHeader
	}
	if msg := custom.CheckHeaderName(header, ""); msg != "" {
		v.add(CodeCustom, msg, "auth_header")
		return mode, ""
	}
	return mode, header
}

func (v *validator) structuredOutput(in Input, protocolOK bool) custom.Mode {
	mode := in.StructuredOutput
	switch {
	case mode == "":
		return custom.DefaultMode
	case !mode.Valid():
		v.enum("structured_output", []string{string(custom.ModeStrict), string(custom.ModeJSONObject), string(custom.ModePromptOnly)}, string(mode))
		return ""
	case protocolOK && !mode.ValidFor(in.Protocol):
		v.add(CodeCustom, "JSON object is available only for OpenAI-compatible gateways", "structured_output")
		return ""
	}
	return mode
}

func (v *validator) key(k KeyChange) KeyChange {
	if k.Op == KeyReplace {
		if code, msg := keyIssue(k.Value); msg != "" {
			v.add(code, msg, "api_key")
		}
	}
	return k
}

func (v *validator) headers(in []HeaderInput, authHeader string) []HeaderInput {
	if len(in) > custom.MaxHeaders {
		v.add(CodeTooBig, fmt.Sprintf("Array must contain at most %d element(s)", custom.MaxHeaders), "extra_headers")
		in = in[:custom.MaxHeaders]
	}
	out := make([]HeaderInput, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i, h := range in {
		if msg := custom.CheckHeaderName(h.Name, authHeader); msg != "" {
			v.add(CodeCustom, msg, "extra_headers", i, "name")
		} else if _, dup := seen[strings.ToLower(h.Name)]; dup {
			v.add(CodeCustom, "Header name is duplicated", "extra_headers", i, "name")
		}
		seen[strings.ToLower(h.Name)] = struct{}{}
		if h.Value != nil {
			if msg := custom.CheckHeaderValue(*h.Value); msg != "" {
				v.add(CodeCustom, msg, "extra_headers", i, "value")
			}
		}
		out = append(out, h)
	}
	return out
}

func (v *validator) models(in []Model) []Model {
	if len(in) > MaxModels {
		v.add(CodeTooBig, fmt.Sprintf("Array must contain at most %d element(s)", MaxModels), "models")
		in = in[:MaxModels]
	}
	out := make([]Model, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i, m := range in {
		switch n := utf16Len(m.ID); {
		case n < 1:
			v.add(CodeTooSmall, "String must contain at least 1 character(s)", "models", i, "id")
		case n > MaxModelIDLen:
			v.add(CodeTooBig, fmt.Sprintf("String must contain at most %d character(s)", MaxModelIDLen), "models", i, "id")
		case strings.IndexFunc(m.ID, unicode.IsControl) >= 0:
			v.add(CodeInvalidStr, "Model id must not contain control characters", "models", i, "id")
		default:
			if _, dup := seen[m.ID]; dup {
				v.add(CodeCustom, "Model id is duplicated", "models", i, "id")
			}
			seen[m.ID] = struct{}{}
		}
		model := Model{ID: m.ID}
		if m.Label != nil && *m.Label != "" {
			switch {
			case utf16Len(*m.Label) > MaxLabelLen:
				v.add(CodeTooBig, fmt.Sprintf("String must contain at most %d character(s)", MaxLabelLen), "models", i, "label")
			case strings.IndexFunc(*m.Label, unicode.IsControl) >= 0:
				v.add(CodeInvalidStr, "Model label must not contain control characters", "models", i, "label")
			}
			label := *m.Label
			model.Label = &label
		}
		if m.Pricing != nil {
			v.price(m.Pricing.PromptPerM, "models", i, "pricing", "promptPerM")
			v.price(m.Pricing.CompletionPerM, "models", i, "pricing", "completionPerM")
			price := *m.Pricing
			model.Pricing = &price
		}
		out = append(out, model)
	}
	return out
}

// price checks a manual price: finite, 0 to MaxPrice, at most PriceDecimals decimals. A price
// with more decimals is refused, never rounded.
func (v *validator) price(p float64, path ...any) {
	switch {
	case math.IsNaN(p) || math.IsInf(p, 0):
		v.add(CodeInvalidType, "Price must be a finite number", path...)
	case p < 0:
		v.add(CodeTooSmall, "Number must be greater than or equal to 0", path...)
	case p > MaxPrice:
		v.add(CodeTooBig, fmt.Sprintf("Number must be less than or equal to %d", int(MaxPrice)), path...)
	case decimals(p) > PriceDecimals:
		v.add(CodeCustom, fmt.Sprintf("Price must have at most %d decimal places", PriceDecimals), path...)
	}
}

// decimals counts the decimals of the shortest decimal form of p that reads back as p.
func decimals(p float64) int {
	s := strconv.FormatFloat(p, 'f', -1, 64)
	_, frac, found := strings.Cut(s, ".")
	if !found {
		return 0
	}
	return len(frac)
}

// MergeHeaders resolves the extra headers of a request against the stored header values (spec
// FR5): a listed header with a value uses it, one without keeps the stored value of the header
// with the same name (case-insensitive) and is an issue when none is stored; headers not listed
// are dropped. The result keeps the listed order and the listed spelling of each name.
func MergeHeaders(listed []HeaderInput, stored []HeaderValue) ([]HeaderValue, []Issue) {
	var issues []Issue
	out := make([]HeaderValue, 0, len(listed))
	for i, h := range listed {
		switch {
		case h.Value != nil:
			out = append(out, HeaderValue{Name: h.Name, Value: *h.Value})
		case FindHeader(stored, h.Name) >= 0:
			out = append(out, HeaderValue{Name: h.Name, Value: stored[FindHeader(stored, h.Name)].Value})
		default:
			issues = append(issues, Issue{Code: CodeInvalidType, Path: []any{"extra_headers", i, "value"},
				Message: "Value is required: no value is stored for this header"})
		}
	}
	if len(issues) > 0 {
		return nil, issues
	}
	return out, nil
}

// jsTrim removes leading and trailing white space the way JavaScript's String.prototype.trim does
// (Unicode white space and U+FEFF, but not U+0085), so a name has the length the UI counted.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == 0xFEFF || (r != '\u0085' && unicode.IsSpace(r))
	})
}

// utf16Len is the length of s in UTF-16 code units, which is what JavaScript's String.length counts.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
