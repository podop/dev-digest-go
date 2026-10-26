package app

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	cidomain "dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/runner/domain"
	"dev-digest-go/backend/internal/llm/custom"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Manifest is the agent manifest the export wrote to <dir>/agents/<slug>.yaml, validated the way
// the Node runner's AgentManifest schema does (spec 7.8.4). Unknown keys are ignored.
type Manifest struct {
	Name string
	// Provider is openrouter, openai, anthropic or "custom:<slug>" (FR30, FR36).
	Provider     string
	Model        string
	SystemPrompt string
	// Skills are the slugs, in order; a missing or null list is empty.
	Skills   []string
	Strategy reviewer.Strategy
	CIFailOn reviewer.FailOn
	// Version is the agent version at export; nil when absent or null. It is a float64 because
	// the Node schema accepts any integer-valued number, whatever its size.
	Version *float64
	// CustomProvider is the validated custom_provider block; set exactly when Provider is a custom
	// provider id (FR30).
	CustomProvider *CustomProvider
	// Pricing is the manifest price of the model, nil when the run is unpriced: the block's
	// pricing for a custom provider, the top-level pricing for openai / anthropic. An openrouter
	// manifest never has one: the runner reads no pricing key there (FR36).
	Pricing *cidomain.Pricing
}

// CustomProvider is the custom_provider block of the manifest (FR27): the shape of the provider,
// never its base URL, key or header values.
type CustomProvider struct {
	Protocol custom.Protocol
	AuthMode custom.AuthMode
	// AuthHeader names the credential header of the header auth mode; empty for bearer.
	AuthHeader string
	// ExtraHeaders are the extra header names in order; DEVDIGEST_LLM_HEADER_<n> holds the value of
	// the n-th.
	ExtraHeaders     []string
	StructuredOutput custom.Mode
}

// maxManifestVersion is the largest version the artifact can carry (a JavaScript safe integer).
const maxManifestVersion = 1<<53 - 1

// Enum values of the schema, in the order of the issue texts.
var (
	providerValues = []string{"openai", "anthropic", "openrouter"}
	strategyValues = []string{"auto", "single-pass", "map-reduce"}
	failOnValues   = []string{"never", "critical", "warning", "any"}
)

// FindManifestPath returns the manifest file to load. With an agent (DEVDIGEST_AGENT, a slug) it
// is <dir>/agents/<agent>.yaml and other manifests are ignored; without one the directory must
// hold exactly one *.yaml or *.yml file.
func FindManifestPath(files Files, dir, agent string) (string, error) {
	agentsDir := path.Join(dir, "agents")
	if agent != "" {
		if !domain.ValidSlug(agent) {
			return "", domain.AgentSlugError(agent)
		}
		return path.Join(agentsDir, agent+".yaml"), nil
	}
	entries, err := files.ReadDir(agentsDir)
	if err != nil {
		return "", domain.NewRunnerError("Agent manifest directory not found: " + agentsDir + " (" + err.Error() + ")")
	}
	var yamlFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e, ".yaml") || strings.HasSuffix(e, ".yml") {
			yamlFiles = append(yamlFiles, e)
		}
	}
	switch len(yamlFiles) {
	case 0:
		return "", domain.NewRunnerError("No agent manifest (*.yaml) found in " + agentsDir)
	case 1:
		return path.Join(agentsDir, yamlFiles[0]), nil
	}
	return "", domain.NewRunnerError("Expected exactly one agent manifest in " + agentsDir + ", found " +
		strconv.Itoa(len(yamlFiles)) + ": " + strings.Join(yamlFiles, ", "))
}

// LoadAgentManifest reads, parses and validates the manifest at manifestPath. The file is read
// with the 1 MiB cap of spec 7.8.4; every failure is a *domain.RunnerError with the Node text.
func LoadAgentManifest(files Files, manifestPath string) (Manifest, error) {
	raw, err := files.ReadFile(manifestPath, domain.MaxConfigFileBytes)
	if errors.Is(err, ErrFileTooLarge) {
		return Manifest{}, domain.NewRunnerError("Agent manifest at " + manifestPath + " is larger than " +
			strconv.Itoa(domain.MaxConfigFileBytes) + " bytes")
	}
	if err != nil {
		return Manifest{}, domain.NewRunnerError("Cannot read agent manifest at " + manifestPath + ": " + err.Error())
	}
	root, err := parseYAML(string(raw))
	if err != nil {
		return Manifest{}, domain.NewRunnerError("Agent manifest at " + manifestPath + " is not valid YAML: " + err.Error())
	}
	m, issues := validateManifest(root)
	if len(issues) > 0 {
		parts := make([]string, len(issues))
		for i, is := range issues {
			p := strings.Join(is.path, ".")
			if p == "" {
				p = "(root)"
			}
			parts[i] = p + ": " + is.msg
		}
		return Manifest{}, domain.NewRunnerError("Agent manifest at " + manifestPath + " failed validation: " + strings.Join(parts, "; "))
	}
	return m, nil
}

// LoadManifest locates, loads and validates the manifest of dir.
func LoadManifest(files Files, dir, agent string) (Manifest, error) {
	p, err := FindManifestPath(files, dir, agent)
	if err != nil {
		return Manifest{}, err
	}
	return LoadAgentManifest(files, p)
}

type issue struct {
	path []string
	msg  string
}

// validateManifest applies the AgentManifest schema (Zod 3 texts, keys in schema order).
func validateManifest(root any) (Manifest, []issue) {
	obj, ok := root.(*ymap)
	if !ok {
		return Manifest{}, []issue{{msg: "Expected object, received " + parsedType(root)}}
	}
	var m Manifest
	var issues []issue
	add := func(msg string, at ...string) { issues = append(issues, issue{path: at, msg: msg}) }

	str := func(key string, minOne bool) string {
		v, present := obj.get(key)
		switch s := v.(type) {
		case string:
			if minOne && s == "" {
				add("String must contain at least 1 character(s)", key)
			}
			return s
		default:
			if !present {
				add("Required", key)
			} else {
				add("Expected string, received "+parsedType(v), key)
			}
		}
		return ""
	}
	enum := func(key string, values []string, def string) string {
		v, present := obj.get(key)
		if !present {
			return def
		}
		s, isString := v.(string)
		if !isString {
			add("Expected "+joinValues(values)+", received "+parsedType(v), key)
			return def
		}
		for _, allowed := range values {
			if s == allowed {
				return s
			}
		}
		// Zod echoes the received value; it is the repository's text, of any length, and it
		// would reach the job log, so the issue names the allowed values only (spec 7.8.9).
		add("Invalid enum value. Expected "+joinValues(values), key)
		return def
	}

	m.Name = str("name", true)
	m.Provider = provider(obj, enum)
	m.Model = str("model", true)
	m.SystemPrompt = str("system_prompt", false)

	switch v, _ := obj.get("skills"); list := v.(type) {
	case nil:
		m.Skills = []string{}
	case []any:
		m.Skills = make([]string, 0, len(list))
		for i, item := range list {
			if s, ok := item.(string); ok {
				m.Skills = append(m.Skills, s)
				continue
			}
			add("Expected string, received "+parsedType(item), "skills", strconv.Itoa(i))
		}
	default:
		add("Expected array, received "+parsedType(v), "skills")
	}

	m.Strategy = reviewer.Strategy(enum("strategy", strategyValues, "auto"))
	m.CIFailOn = reviewer.FailOn(enum("ci_fail_on", failOnValues, "critical"))

	switch v, _ := obj.get("version"); n := v.(type) {
	case nil:
	case float64:
		switch {
		case math.IsNaN(n):
			add("Expected number, received nan", "version")
		case n != math.Trunc(n) || math.IsInf(n, 0):
			add("Expected integer, received float", "version")
		default:
			m.Version = &n
		}
	default:
		add("Expected number, received "+parsedType(v), "version")
	}

	switch {
	case cidomain.IsCustomProvider(m.Provider):
		m.CustomProvider, m.Pricing = validateCustomBlock(obj, add)
	case m.Provider == cidomain.ProviderOpenAI || m.Provider == cidomain.ProviderAnthropic:
		v, _ := obj.get("pricing")
		m.Pricing = validatePricing(v, add, "pricing")
	}
	return m, issues
}

// provider reads the provider key: one of the three built-ins, or a well-formed custom provider
// id. Anything else is the enum issue of the built-in list.
func provider(obj *ymap, enum func(string, []string, string) string) string {
	if v, _ := obj.get("provider"); v != nil {
		if s, ok := v.(string); ok && cidomain.IsCustomProvider(s) {
			return s
		}
	}
	return enum("provider", providerValues, "openrouter")
}

// Enum values of the custom_provider block, in the order of the issue texts.
var (
	protocolValues   = []string{"openai", "anthropic"}
	authModeValues   = []string{"bearer", "header"}
	structuredValues = []string{"strict", "json_object", "prompt_only"}
)

// validateCustomBlock applies the FR27 schema of the custom_provider block and its pricing. Issue
// texts name the allowed values, never the received ones: the manifest is the repository's text.
func validateCustomBlock(obj *ymap, add func(string, ...string)) (*CustomProvider, *cidomain.Pricing) {
	const block = "custom_provider"
	raw, present := obj.get(block)
	cp, isObj := raw.(*ymap)
	if !isObj {
		if !present || raw == nil {
			add("Required", block)
		} else {
			add("Expected object, received "+parsedType(raw), block)
		}
		return nil, nil
	}
	at := func(key string, rest ...string) []string { return append([]string{block, key}, rest...) }
	addAt := func(msg, key string, rest ...string) { add(msg, at(key, rest...)...) }

	enumAt := func(key string, values []string, def string) string {
		v, ok := cp.get(key)
		if !ok || v == nil {
			if def == "" {
				addAt("Required", key)
			}
			return def
		}
		s, isString := v.(string)
		if !isString {
			addAt("Expected "+joinValues(values)+", received "+parsedType(v), key)
			return def
		}
		if !slices.Contains(values, s) {
			addAt("Invalid enum value. Expected "+joinValues(values), key)
			return def
		}
		return s
	}
	out := &CustomProvider{
		Protocol: custom.Protocol(enumAt("protocol", protocolValues, "")),
		AuthMode: custom.AuthMode(enumAt("auth_mode", authModeValues, "")),
	}

	switch v, _ := cp.get("auth_header"); h := v.(type) {
	case nil:
		if out.AuthMode == custom.AuthHeader {
			addAt("Required", "auth_header")
		}
	case string:
		if out.AuthMode == custom.AuthHeader {
			if msg := custom.CheckHeaderName(h, ""); msg != "" {
				addAt(msg, "auth_header")
			}
			out.AuthHeader = h
		}
	default:
		addAt("Expected string, received "+parsedType(v), "auth_header")
	}

	out.ExtraHeaders = []string{}
	switch v, _ := cp.get("extra_headers"); list := v.(type) {
	case nil:
	case []any:
		if len(list) > custom.MaxHeaders {
			addAt(fmt.Sprintf("Array must contain at most %d element(s)", custom.MaxHeaders), "extra_headers")
		}
		for i, item := range list {
			name, ok := item.(string)
			if !ok {
				addAt("Expected string, received "+parsedType(item), "extra_headers", strconv.Itoa(i))
				continue
			}
			msg := custom.CheckHeaderName(name, out.AuthHeader)
			if msg == "" && slices.ContainsFunc(out.ExtraHeaders, func(prev string) bool { return strings.EqualFold(prev, name) }) {
				msg = "Header name is duplicated"
			}
			if msg != "" {
				addAt(msg, "extra_headers", strconv.Itoa(i))
				continue
			}
			out.ExtraHeaders = append(out.ExtraHeaders, name)
		}
	default:
		addAt("Expected array, received "+parsedType(v), "extra_headers")
	}

	out.StructuredOutput = custom.Mode(enumAt("structured_output", structuredValues, string(custom.DefaultMode)))
	if out.StructuredOutput == custom.ModeJSONObject && out.Protocol != custom.ProtocolOpenAI && out.Protocol.Valid() {
		addAt("json_object needs the openai protocol", "structured_output")
	}

	v, _ := cp.get("pricing")
	return out, validatePricing(v, add, block, "pricing")
}

// validatePricing reads a {promptPerM, completionPerM} object: null or absent = no price; both
// numbers must be finite and at least 0.
func validatePricing(v any, add func(string, ...string), at ...string) *cidomain.Pricing {
	if v == nil {
		return nil
	}
	obj, ok := v.(*ymap)
	if !ok {
		add("Expected object, received "+parsedType(v), at...)
		return nil
	}
	bad := false
	num := func(key string) float64 {
		raw, present := obj.get(key)
		path := append(slices.Clone(at), key)
		switch n := raw.(type) {
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				add("Number must be finite and greater than or equal to 0", path...)
				bad = true
			}
			return n
		default:
			if !present || raw == nil {
				add("Required", path...)
			} else {
				add("Expected number, received "+parsedType(raw), path...)
			}
			bad = true
		}
		return 0
	}
	p := cidomain.Pricing{PromptPerM: num("promptPerM"), CompletionPerM: num("completionPerM")}
	if bad {
		return nil
	}
	return &p
}

// joinValues is Zod's text for a set of literals: 'a' | 'b'.
func joinValues(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}
	return strings.Join(quoted, " | ")
}

// parsedType is Zod's name for the type of a parsed value.
func parsedType(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if math.IsNaN(t) {
			return "nan"
		}
		return "number"
	case []any:
		return "array"
	}
	return "object"
}

// versionText is JavaScript's String(version) for the trace line.
func versionText(v *float64) string {
	if v == nil {
		return "none"
	}
	return reviewer.JSNumber(*v)
}

// artifactVersion is the manifest_version of the artifact: the version when it is a safe integer,
// else null (the ingest accepts no other).
func artifactVersion(v *float64) *int64 {
	if v == nil || math.Abs(*v) > maxManifestVersion {
		return nil
	}
	n := int64(*v)
	return &n
}

// ---------------------------------------------------------------------------------------------
// YAML: one document read with the semantics of the Node "yaml" package (YAML 1.2 core schema,
// duplicate keys refused, the same alias budget).

const (
	maxAliasCount = 100
	maxYAMLDepth  = 1000
	maxYAMLVisits = 2_000_000
)

var (
	errYAMLInvalid  = errors.New("invalid YAML")
	errYAMLTooDeep  = errors.New("YAML nests too deeply")
	errYAMLTooMuch  = errors.New("YAML expands too much")
	errYAMLAliases  = errors.New("excessive alias count")
	errYAMLCircular = errors.New("circular alias")
)

// ymap is a mapping with the key semantics of a JavaScript object: a later equal key replaces the
// value (duplicates are refused before that).
type ymap struct {
	keys []string
	vals map[string]any
}

func (m *ymap) get(key string) (any, bool) {
	v, ok := m.vals[key]
	return v, ok
}

func (m *ymap) set(key string, v any) {
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
}

// Plain-scalar resolution of the core schema. Go's (?i) is not used: it folds s with U+017F.
var (
	reNull  = regexp.MustCompile(`^(?:~|[Nn]ull|NULL)?$`)
	reBool  = regexp.MustCompile(`^(?:[Tt]rue|TRUE|[Ff]alse|FALSE)$`)
	reInt   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	reOct   = regexp.MustCompile(`^0o[0-7]+$`)
	reHex   = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	reFloat = regexp.MustCompile(`^(?:[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)[eE][-+]?[0-9]+|[-+]?(?:\.[0-9]+|[0-9]+\.[0-9]*))$`)
	reInf   = regexp.MustCompile(`^[-+]?\.(?:inf|Inf|INF)$`)
	reNaN   = regexp.MustCompile(`^\.(?:nan|NaN|NAN)$`)
)

// parseYAML reads text as exactly one document: nil for an empty one, else nil, bool, float64,
// string, []any or *ymap. A syntax error, a second document, a duplicate key, an unknown or
// circular alias, an over-used anchor or a too large expansion is an error.
func parseYAML(text string) (root any, err error) {
	// The parser reports malformed input as errors; a panic would be a parser bug on hostile
	// input, and the caller's answer is the same.
	defer func() {
		if r := recover(); r != nil {
			root, err = nil, fmt.Errorf("%w: %v", errYAMLInvalid, r)
		}
	}()

	dec := yaml.NewDecoder(strings.NewReader(text))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, fmt.Errorf("%w: more than one document", errYAMLInvalid)
	case !errors.Is(err, io.EOF):
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, nil
	}
	c := &yamlConverter{anchors: map[*yaml.Node]*anchorData{}, active: map[*yaml.Node]bool{}}
	return c.node(doc.Content[0], 0)
}

// anchorData is the bookkeeping of the Node package's alias limit for one anchored node.
type anchorData struct {
	count      int // 1 + uses so far
	aliasCount int // cached nesting factor, 0 until first computed
	res        any
}

type yamlConverter struct {
	visits    int
	anchors   map[*yaml.Node]*anchorData
	active    map[*yaml.Node]bool
	complexes int
}

func (c *yamlConverter) tick() error {
	c.visits++
	if c.visits > maxYAMLVisits {
		return errYAMLTooMuch
	}
	return nil
}

func (c *yamlConverter) node(n *yaml.Node, depth int) (any, error) {
	if depth > maxYAMLDepth {
		return nil, errYAMLTooDeep
	}
	if err := c.tick(); err != nil {
		return nil, err
	}
	if n.Kind == yaml.AliasNode {
		return c.alias(n)
	}
	var data *anchorData
	if n.Anchor != "" {
		data = &anchorData{count: 1}
		c.anchors[n] = data
		c.active[n] = true
		defer delete(c.active, n)
	}
	var v any
	var err error
	switch n.Kind {
	case yaml.ScalarNode:
		v = scalarValue(n)
	case yaml.SequenceNode:
		v, err = c.sequence(n, depth)
	case yaml.MappingNode:
		v, err = c.mapping(n, depth)
	default:
		err = fmt.Errorf("%w: unexpected node kind %d", errYAMLInvalid, n.Kind)
	}
	if err != nil {
		return nil, err
	}
	if data != nil {
		data.res = v
	}
	return v, nil
}

func (c *yamlConverter) alias(n *yaml.Node) (any, error) {
	target := n.Alias
	if target == nil {
		return nil, fmt.Errorf("%w: alias %q has no anchor", errYAMLInvalid, n.Value)
	}
	if c.active[target] {
		return nil, fmt.Errorf("%w: alias %q", errYAMLCircular, n.Value)
	}
	data := c.anchors[target]
	if data == nil {
		return nil, fmt.Errorf("%w: alias %q is not resolved", errYAMLInvalid, n.Value)
	}
	data.count++
	if data.aliasCount == 0 {
		count, err := c.aliasCount(target)
		if err != nil {
			return nil, err
		}
		data.aliasCount = count
	}
	if data.count*data.aliasCount > maxAliasCount {
		return nil, errYAMLAliases
	}
	return data.res, nil
}

// aliasCount ports getAliasCount of the Node package: the largest alias factor nested in n, 1 for
// a scalar and 0 for an empty collection.
func (c *yamlConverter) aliasCount(n *yaml.Node) (int, error) {
	if err := c.tick(); err != nil {
		return 0, err
	}
	switch n.Kind {
	case yaml.AliasNode:
		if d := c.anchors[n.Alias]; d != nil {
			return d.count * d.aliasCount, nil
		}
		return 0, nil
	case yaml.SequenceNode, yaml.MappingNode:
		most := 0
		for _, child := range n.Content {
			count, err := c.aliasCount(child)
			if err != nil {
				return 0, err
			}
			most = max(most, count)
		}
		return most, nil
	}
	return 1, nil
}

func (c *yamlConverter) sequence(n *yaml.Node, depth int) (any, error) {
	items := make([]any, 0, len(n.Content))
	for _, child := range n.Content {
		v, err := c.node(child, depth+1)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, nil
}

// dupKey is a mapping key as the Node package compares it for uniqueness: scalars by their
// resolved value.
type dupKey struct {
	kind byte
	s    string
	f    float64
}

func (c *yamlConverter) mapping(n *yaml.Node, depth int) (any, error) {
	obj := &ymap{vals: map[string]any{}}
	seen := map[dupKey]struct{}{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		keyNode, valNode := n.Content[i], n.Content[i+1]
		key, err := c.node(keyNode, depth+1)
		if err != nil {
			return nil, err
		}
		if keyNode.Kind == yaml.ScalarNode {
			if dk, ok := dupKeyOf(key); ok {
				if _, dup := seen[dk]; dup {
					return nil, fmt.Errorf("%w: duplicate mapping key at line %d", errYAMLInvalid, keyNode.Line)
				}
				seen[dk] = struct{}{}
			}
		}
		val, err := c.node(valNode, depth+1)
		if err != nil {
			return nil, err
		}
		obj.set(c.keyString(key), val)
	}
	return obj, nil
}

func dupKeyOf(v any) (dupKey, bool) {
	switch k := v.(type) {
	case nil:
		return dupKey{kind: 'z'}, true
	case string:
		return dupKey{kind: 's', s: k}, true
	case bool:
		if k {
			return dupKey{kind: 'b', s: "t"}, true
		}
		return dupKey{kind: 'b', s: "f"}, true
	case float64:
		if math.IsNaN(k) {
			return dupKey{}, false
		}
		if k == 0 {
			k = 0 // -0 === 0
		}
		return dupKey{kind: 'n', f: k}, true
	}
	return dupKey{}, false
}

// keyString is the JavaScript property name a converted key becomes; a collection key gets a
// name no string key can have.
func (c *yamlConverter) keyString(key any) string {
	switch k := key.(type) {
	case nil:
		return ""
	case string:
		return k
	case bool:
		return strconv.FormatBool(k)
	case float64:
		return reviewer.JSNumber(k)
	}
	c.complexes++
	return "\x00collection#" + strconv.Itoa(c.complexes)
}

// scalarValue resolves a scalar the way the YAML 1.2 core schema of the Node package does:
// quoted and block scalars are strings; a plain scalar is null, a boolean, a number or a string;
// an explicit core tag resolves the text by that tag and falls back to the string when it does
// not fit; any other tag leaves the string.
func scalarValue(n *yaml.Node) any {
	const stringStyles = yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle | yaml.LiteralStyle | yaml.FoldedStyle
	text := n.Value
	if n.Style&yaml.TaggedStyle != 0 {
		switch n.Tag {
		case "!!null":
			if reNull.MatchString(text) {
				return nil
			}
		case "!!bool":
			if reBool.MatchString(text) {
				return text[0] == 't' || text[0] == 'T'
			}
		case "!!int":
			if v, ok := resolveInt(text); ok {
				return v
			}
		case "!!float":
			if v, ok := resolveFloat(text); ok {
				return v
			}
		}
		return text
	}
	if n.Style&stringStyles != 0 {
		return text
	}
	switch {
	case reNull.MatchString(text):
		return nil
	case reBool.MatchString(text):
		return text[0] == 't' || text[0] == 'T'
	}
	if v, ok := resolveInt(text); ok {
		return v
	}
	if v, ok := resolveFloat(text); ok {
		return v
	}
	return text
}

func resolveInt(text string) (float64, bool) {
	var digits string
	var base int
	switch {
	case reInt.MatchString(text):
		digits, base = strings.TrimPrefix(text, "+"), 10
	case reOct.MatchString(text):
		digits, base = text[2:], 8
	case reHex.MatchString(text):
		digits, base = text[2:], 16
	default:
		return 0, false
	}
	z, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return 0, false
	}
	f, _ := new(big.Float).SetInt(z).Float64()
	return f, true
}

func resolveFloat(text string) (float64, bool) {
	switch {
	case reInf.MatchString(text):
		if text[0] == '-' {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case reNaN.MatchString(text):
		return math.NaN(), true
	case reFloat.MatchString(text):
		f, err := strconv.ParseFloat(strings.TrimPrefix(text, "+"), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}
