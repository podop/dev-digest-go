package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The schema code works on an order-preserving tree, so the schema the provider receives keeps
// the property order of the caller's schema (field order steers what a model writes first).
//
//	*jobj          JSON object
//	[]any          JSON array
//	string, bool, json.Number, nil   scalars

// jobj is a JSON object that remembers its key order.
type jobj struct {
	keys []string
	m    map[string]any
}

func (o *jobj) get(k string) (any, bool) { v, ok := o.m[k]; return v, ok }

func (o *jobj) set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *jobj) del(k string) {
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	o.keys = slices.DeleteFunc(o.keys, func(s string) bool { return s == k })
}

// MarshalJSON writes the keys in their original order.
func (o *jobj) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(o.m[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func decodeOrdered(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := readValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return v, nil
}

func readValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		o := &jobj{m: map[string]any{}}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, _ := kt.(string)
			v, err := readValue(dec)
			if err != nil {
				return nil, err
			}
			o.set(k, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return o, nil
	default: // '['
		arr := []any{}
		for dec.More() {
			v, err := readValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return arr, nil
	}
}

// StrictSchema returns the form of schema that providers receive (spec 7.2): every object that
// lists properties requires all of them and forbids others; array length limits are dropped.
func StrictSchema(raw json.RawMessage) (json.RawMessage, error) {
	tree, err := decodeOrdered(raw)
	if err != nil {
		return nil, fmt.Errorf("response schema: %w", err)
	}
	if _, ok := tree.(*jobj); !ok {
		return nil, errors.New("response schema: not a JSON object")
	}
	strictNode(tree)
	return json.Marshal(tree)
}

func strictNode(n any) {
	switch v := n.(type) {
	case []any:
		for _, e := range v {
			strictNode(e)
		}
	case *jobj:
		if props, ok := v.get("properties"); ok {
			if po, ok := props.(*jobj); ok {
				names := make([]any, len(po.keys))
				for i, k := range po.keys {
					names[i] = k
					strictNode(po.m[k])
				}
				v.set("required", names)
				v.set("additionalProperties", false)
			}
		}
		v.del("minItems")
		v.del("maxItems")
		for _, k := range []string{"items", "anyOf", "oneOf", "allOf", "not"} {
			if c, ok := v.get(k); ok {
				strictNode(c)
			}
		}
		for _, k := range []string{"$defs", "definitions"} {
			if c, ok := v.get(k); ok {
				if co, ok := c.(*jobj); ok {
					for _, name := range co.keys {
						strictNode(co.m[name])
					}
				}
			}
		}
	}
}

// issue is one validation failure; its path joins with "." in re-prompts.
type issue struct {
	path []string
	msg  string
}

func (i issue) String() string {
	p := strings.Join(i.path, ".")
	if p == "" {
		p = "(root)"
	}
	return "- " + p + ": " + i.msg
}

// validator checks JSON values against a schema, with the messages Zod gives for the same
// failure. Like Zod it ignores properties the schema does not list (additionalProperties is
// only a request to the provider).
type validator struct {
	root  any
	depth int
}

func newValidator(raw json.RawMessage) (*validator, error) {
	tree, err := decodeOrdered(raw)
	if err != nil {
		return nil, fmt.Errorf("response schema: %w", err)
	}
	if _, ok := tree.(*jobj); !ok {
		return nil, errors.New("response schema: not a JSON object")
	}
	return &validator{root: tree}, nil
}

// validate checks the JSON text and returns its issues (nil = valid).
func (v *validator) validate(text string) []issue {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		return []issue{{msg: err.Error()}}
	}
	var out []issue
	v.check(v.root, inst, nil, &out)
	return out
}

const maxSchemaDepth = 64

func (v *validator) check(node, inst any, path []string, out *[]issue) {
	if v.depth > maxSchemaDepth {
		return
	}
	v.depth++
	defer func() { v.depth-- }()

	add := func(msg string) { *out = append(*out, issue{path: slices.Clone(path), msg: msg}) }
	switch s := node.(type) {
	case bool:
		if !s {
			add("Invalid input")
		}
		return
	case *jobj:
		v.checkObject(s, inst, path, out, add)
	}
}

func (v *validator) checkObject(s *jobj, inst any, path []string, out *[]issue, add func(string)) {
	// "nullable": true (the OpenAPI form the Node schema writer emits for Zod .nullish()) accepts
	// JSON null in place of whatever the node describes.
	if inst == nil {
		if n, ok := s.get("nullable"); ok && n == true {
			return
		}
	}
	if ref, ok := s.get("$ref"); ok {
		if r, ok := ref.(string); ok {
			if target := v.resolve(r); target != nil {
				v.check(target, inst, path, out)
			}
		}
		return
	}
	if all, ok := s.get("allOf"); ok {
		if list, ok := all.([]any); ok {
			for _, sub := range list {
				v.check(sub, inst, path, out)
			}
		}
	}
	for _, k := range []string{"anyOf", "oneOf"} {
		if alts, ok := s.get(k); ok {
			// A schema that is one alternative or null is a Zod nullable/nullish: Zod reports the
			// issues of the inner type, not a union failure.
			if list, ok := alts.([]any); ok {
				if inner, isNullable := nullableInner(list); isNullable {
					if inst != nil {
						v.check(inner, inst, path, out)
					}
					return
				}
			}
			if list, ok := alts.([]any); ok && len(list) > 0 && !v.anyValid(list, inst) {
				add("Invalid input")
				return
			}
		}
	}
	if c, ok := s.get("const"); ok && !jsonEqual(c, inst) {
		add("Invalid literal value, expected " + display(c))
		return
	}
	if e, ok := s.get("enum"); ok {
		if list, ok := e.([]any); ok && !slices.ContainsFunc(list, func(c any) bool { return jsonEqual(c, inst) }) {
			opts := make([]string, len(list))
			for i, c := range list {
				opts[i] = display(c)
			}
			add(fmt.Sprintf("Invalid enum value. Expected %s, received %s", strings.Join(opts, " | "), display(inst)))
			return
		}
	}
	if t, ok := s.get("type"); ok {
		if !typeMatches(t, inst) {
			add(typeMessage(t, inst))
			return
		}
	}

	switch x := inst.(type) {
	case string:
		n := UTF16Len(x)
		if lim, ok := intKeyword(s, "minLength"); ok && n < lim {
			add(fmt.Sprintf("String must contain at least %d character(s)", lim))
		}
		if lim, ok := intKeyword(s, "maxLength"); ok && n > lim {
			add(fmt.Sprintf("String must contain at most %d character(s)", lim))
		}
		if p, ok := s.get("pattern"); ok {
			if ps, ok := p.(string); ok {
				if re, err := regexp.Compile(ps); err == nil && !re.MatchString(x) {
					add("Invalid")
				}
			}
		}
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return
		}
		if lim, ok := floatKeyword(s, "minimum"); ok && f < lim {
			add(fmt.Sprintf("Number must be greater than or equal to %s", fmtNum(lim)))
		}
		if lim, ok := floatKeyword(s, "maximum"); ok && f > lim {
			add(fmt.Sprintf("Number must be less than or equal to %s", fmtNum(lim)))
		}
		if lim, ok := floatKeyword(s, "exclusiveMinimum"); ok && f <= lim {
			add(fmt.Sprintf("Number must be greater than %s", fmtNum(lim)))
		}
		if lim, ok := floatKeyword(s, "exclusiveMaximum"); ok && f >= lim {
			add(fmt.Sprintf("Number must be less than %s", fmtNum(lim)))
		}
	case []any:
		if lim, ok := intKeyword(s, "minItems"); ok && len(x) < lim {
			add(fmt.Sprintf("Array must contain at least %d element(s)", lim))
		}
		if lim, ok := intKeyword(s, "maxItems"); ok && len(x) > lim {
			add(fmt.Sprintf("Array must contain at most %d element(s)", lim))
		}
		if items, ok := s.get("items"); ok {
			for i, e := range x {
				v.check(items, e, append(slices.Clone(path), strconv.Itoa(i)), out)
			}
		}
	case map[string]any:
		if req, ok := s.get("required"); ok {
			if names, ok := req.([]any); ok {
				for _, n := range names {
					if name, ok := n.(string); ok {
						if _, present := x[name]; !present {
							*out = append(*out, issue{path: append(slices.Clone(path), name), msg: "Required"})
						}
					}
				}
			}
		}
		if props, ok := s.get("properties"); ok {
			if po, ok := props.(*jobj); ok {
				for _, name := range po.keys {
					if val, present := x[name]; present {
						v.check(po.m[name], val, append(slices.Clone(path), name), out)
					}
				}
			}
		}
	}
}

// nullableInner recognises [X, {"type":"null"}] (either order) and returns X.
func nullableInner(alts []any) (inner any, ok bool) {
	if len(alts) != 2 {
		return nil, false
	}
	isNull := func(n any) bool {
		o, ok := n.(*jobj)
		if !ok || len(o.keys) != 1 {
			return false
		}
		t, ok := o.get("type")
		return ok && t == "null"
	}
	switch {
	case isNull(alts[1]) && !isNull(alts[0]):
		return alts[0], true
	case isNull(alts[0]) && !isNull(alts[1]):
		return alts[1], true
	}
	return nil, false
}

func (v *validator) anyValid(alts []any, inst any) bool {
	for _, a := range alts {
		var sub []issue
		v.check(a, inst, nil, &sub)
		if len(sub) == 0 {
			return true
		}
	}
	return false
}

// resolve follows a local "#/a/b" reference.
func (v *validator) resolve(ref string) any {
	if ref == "#" {
		return v.root
	}
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil
	}
	cur := v.root
	for _, part := range strings.Split(rest, "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case *jobj:
			cur, _ = c.get(part)
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(c) {
				return nil
			}
			cur = c[i]
		default:
			return nil
		}
	}
	return cur
}

func intKeyword(s *jobj, k string) (int, bool) {
	f, ok := floatKeyword(s, k)
	return int(f), ok
}

func floatKeyword(s *jobj, k string) (float64, bool) {
	v, ok := s.get(k)
	if !ok {
		return 0, false
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// typeOf names a decoded value the way Zod does.
func typeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any, *jobj:
		return "object"
	default:
		return fmt.Sprintf("%T", x)
	}
}

func isInteger(n json.Number) bool {
	f, err := n.Float64()
	return err == nil && !math.IsInf(f, 0) && f == math.Trunc(f)
}

func typeMatches(t, inst any) bool {
	switch tt := t.(type) {
	case string:
		return oneTypeMatches(tt, inst)
	case []any:
		return slices.ContainsFunc(tt, func(e any) bool {
			s, ok := e.(string)
			return ok && oneTypeMatches(s, inst)
		})
	}
	return true
}

func oneTypeMatches(t string, inst any) bool {
	switch t {
	case "integer":
		n, ok := inst.(json.Number)
		return ok && isInteger(n)
	default:
		return typeOf(inst) == t
	}
}

func typeMessage(t, inst any) string {
	expected := make([]string, 0, 2)
	switch tt := t.(type) {
	case string:
		expected = append(expected, tt)
	case []any:
		for _, e := range tt {
			if s, ok := e.(string); ok {
				expected = append(expected, s)
			}
		}
	}
	received := typeOf(inst)
	if n, ok := inst.(json.Number); ok {
		if slices.Contains(expected, "integer") && !isInteger(n) {
			received = "float"
		}
	} else {
		// Zod's z.number().int() reports a non-number as "Expected number", not "integer".
		for i, e := range expected {
			if e == "integer" {
				expected[i] = "number"
			}
		}
	}
	return fmt.Sprintf("Expected %s, received %s", strings.Join(expected, " | "), received)
}

func jsonEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		fx, e1 := x.Float64()
		fy, e2 := y.Float64()
		return e1 == nil && e2 == nil && fx == fy
	case string, bool, nil:
		return a == b
	}
	ab, e1 := json.Marshal(a)
	bb, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(ab, bb)
}

// display renders a value inside an issue text: strings in single quotes, the rest as JSON.
func display(v any) string {
	if s, ok := v.(string); ok {
		return "'" + s + "'"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
