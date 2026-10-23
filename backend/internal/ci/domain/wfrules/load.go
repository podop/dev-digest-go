package wfrules

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limits that keep a hostile workflow cheap. The text is already capped at maxWorkflowBytes before
// it reaches the parser; these bound what a small text can still expand into.
const (
	// maxAliasCount is the alias-expansion limit of the Node "yaml" package (maxAliasCount: 100),
	// applied with the same metric: uses of an anchor times the aliases nested in it.
	maxAliasCount = 100
	// maxDepth is the deepest nesting of collections the loader follows.
	maxDepth = 1000
	// maxVisits bounds the nodes any one walk over the document visits. Aliases share their value,
	// so a walk over the document is a walk over a DAG and can be exponentially larger than the
	// text (a chain of aliases of empty collections costs nothing in the alias metric).
	maxVisits = 2_000_000
)

var (
	errInvalid  = errors.New("invalid workflow YAML")
	errTooDeep  = errors.New("workflow YAML nests too deeply")
	errTooMuch  = errors.New("workflow YAML expands too much")
	errAliases  = errors.New("excessive alias count")
	errCircular = errors.New("circular alias")
)

// value is a converted YAML node: nil (null), bool, float64, string, opaque, []value or *object.
type value = any

// opaque stands for a node whose JavaScript value is neither a string, a list nor a plain mapping
// (a !!binary scalar). No rule looks into it.
type opaque struct{}

// object is a mapping with the key semantics of a JavaScript object: keys are strings, a second
// pair with an equal key string replaces the first value and keeps its position.
type object struct {
	keys []string
	vals map[string]value
}

func (o *object) get(key string) (value, bool) {
	v, ok := o.vals[key]
	return v, ok
}

func (o *object) set(key string, v value) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// Plain-scalar resolution of the YAML 1.2 core schema, as the Node "yaml" package resolves it.
// Go's (?i) is never used: it would fold s with U+017F and k with U+212A.
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

// parse reads text as exactly one YAML document and converts it. Anything that is not a single
// well-formed document within the limits is an error: a syntax error, a second document,
// duplicate mapping keys, an alias to an unknown, enclosing or over-used anchor, too deep or too
// large an expansion, or a tag on a collection that does not match its kind.
func parse(text string) (root value, err error) {
	// yaml.v3 reports malformed input as errors; a panic would be a parser bug on hostile input.
	// Either way the answer for the caller is the same, so it never escapes this package.
	defer func() {
		if r := recover(); r != nil {
			root, err = nil, fmt.Errorf("parse workflow YAML: %v", r)
		}
	}()

	dec := yaml.NewDecoder(strings.NewReader(text))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse workflow YAML: %w", err)
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, fmt.Errorf("parse workflow YAML: %w: more than one document", errInvalid)
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("parse workflow YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("parse workflow YAML: %w: no document", errInvalid)
	}

	c := &converter{anchors: map[*yaml.Node]*anchorData{}, active: map[*yaml.Node]bool{}}
	return c.node(doc.Content[0], 0)
}

// anchorData is the bookkeeping of the Node package's alias limit for one anchored node.
type anchorData struct {
	count      int // 1 + uses so far
	aliasCount int // cached nesting factor, 0 until first computed
	res        value
}

type converter struct {
	visits    int
	anchors   map[*yaml.Node]*anchorData
	active    map[*yaml.Node]bool // anchored nodes being converted
	complexes int
}

func (c *converter) tick() error {
	c.visits++
	if c.visits > maxVisits {
		return errTooMuch
	}
	return nil
}

func (c *converter) node(n *yaml.Node, depth int) (value, error) {
	if depth > maxDepth {
		return nil, errTooDeep
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

	var v value
	var err error
	switch n.Kind {
	case yaml.ScalarNode:
		v = scalarValue(n)
	case yaml.SequenceNode:
		v, err = c.sequence(n, depth)
	case yaml.MappingNode:
		v, err = c.mapping(n, depth)
	default:
		err = fmt.Errorf("%w: unexpected node kind %d", errInvalid, n.Kind)
	}
	if err != nil {
		return nil, err
	}
	if data != nil {
		data.res = v
	}
	return v, nil
}

func (c *converter) alias(n *yaml.Node) (value, error) {
	target := n.Alias
	if target == nil {
		return nil, fmt.Errorf("%w: alias %q has no anchor", errInvalid, n.Value)
	}
	if c.active[target] {
		return nil, fmt.Errorf("%w: alias %q", errCircular, n.Value)
	}
	data := c.anchors[target]
	if data == nil {
		return nil, fmt.Errorf("%w: alias %q is not resolved", errInvalid, n.Value)
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
		return nil, errAliases
	}
	return data.res, nil
}

// aliasCount ports getAliasCount of the Node package: the largest alias factor nested in n, 1 for
// a scalar and 0 for an empty collection.
func (c *converter) aliasCount(n *yaml.Node) (int, error) {
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
	default:
		return 1, nil
	}
}

func (c *converter) sequence(n *yaml.Node, depth int) (value, error) {
	if err := checkCollectionTag(n, "!!seq"); err != nil {
		return nil, err
	}
	items := make([]value, 0, len(n.Content))
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
// resolved value (two null keys are equal, NaN keys never are).
type dupKey struct {
	kind byte
	s    string
	f    float64
}

func (c *converter) mapping(n *yaml.Node, depth int) (value, error) {
	if err := checkCollectionTag(n, "!!map"); err != nil {
		return nil, err
	}
	obj := &object{vals: map[string]value{}}
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
					return nil, fmt.Errorf("%w: duplicate mapping key %q", errInvalid, keyNode.Value)
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

// checkCollectionTag rejects an explicit tag that gives a collection another kind (!!set, !!omap,
// a local tag, ...). The Node package would silently read such a node as something else, and a
// reader that does not (GitHub's) would then see a different document than the rules did.
func checkCollectionTag(n *yaml.Node, want string) error {
	if n.Style&yaml.TaggedStyle != 0 && n.Tag != want {
		return fmt.Errorf("%w: tag %q on a collection", errInvalid, n.Tag)
	}
	return nil
}

func dupKeyOf(v value) (dupKey, bool) {
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
	default:
		return dupKey{}, false
	}
}

// keyString is the JavaScript property name a converted key becomes. A collection key never
// equals any other key, so each gets a name no string key can have.
func (c *converter) keyString(key value) string {
	switch k := key.(type) {
	case nil:
		return ""
	case string:
		return k
	case bool:
		return strconv.FormatBool(k)
	case float64:
		return jsNumber(k)
	default:
		c.complexes++
		return "\x00collection#" + strconv.Itoa(c.complexes)
	}
}

// jsNumber formats f like JavaScript's String(number).
func jsNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	if abs := math.Abs(f); abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, exp, _ := strings.Cut(s, "e")
	sign := exp[:1]
	digits := strings.TrimLeft(exp[1:], "0")
	return mantissa + "e" + sign + digits
}

// scalarValue resolves a scalar node the way the YAML 1.2 core schema of the Node package does.
// Quoted and block scalars are strings; a plain scalar is null, a boolean, a number or a string;
// an explicit core tag resolves the text by that tag and falls back to the string when the text
// does not fit it; any other tag leaves the string.
func scalarValue(n *yaml.Node) value {
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
		case "!!binary":
			return opaque{}
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
