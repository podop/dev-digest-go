package importgraph

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxJSON5Depth bounds nesting so hostile input cannot exhaust the stack.
const maxJSON5Depth = 64

// ParseJSON5 parses the JSON5 subset that tsconfig files use: JSON plus // and /* */ comments,
// trailing commas, single-quoted strings, unquoted identifier keys, and the JSON5 number forms
// (hexadecimal, leading plus, leading or trailing dot, Infinity, NaN). Objects decode to *Object
// (keys keep their order), arrays to []any, numbers to float64. A byte-order mark is skipped.
func ParseJSON5(data []byte) (any, error) {
	p := &json5Parser{src: strings.TrimPrefix(string(data), "\uFEFF")}
	p.skip()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.pos < len(p.src) {
		return nil, p.errorf("unexpected trailing content")
	}
	return v, nil
}

// Object is a decoded JSON5 object. Keys lists the property names in the order they first
// appear; a repeated key keeps its first position and its last value.
type Object struct {
	Keys   []string
	values map[string]any
}

func (o *Object) set(key string, v any) {
	if _, dup := o.values[key]; !dup {
		o.Keys = append(o.Keys, key)
	}
	o.values[key] = v
}

// Get returns the value of key; ok is false when the key is absent.
func (o *Object) Get(key string) (v any, ok bool) {
	v, ok = o.values[key]
	return v, ok
}

type json5Parser struct {
	src string
	pos int
}

func (p *json5Parser) errorf(format string, args ...any) error {
	return fmt.Errorf("json5: offset %d: %s", p.pos, fmt.Sprintf(format, args...))
}

// skip advances over white space and comments. An unterminated block comment is left in place so
// the next token read fails on it.
func (p *json5Parser) skip() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			p.pos += 2
			for p.pos < len(p.src) && !isLineBreak(p.src[p.pos:]) {
				p.pos++
			}
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			end := strings.Index(p.src[p.pos+2:], "*/")
			if end < 0 {
				return
			}
			p.pos += 2 + end + 2
		default:
			r, size := utf8.DecodeRuneInString(p.src[p.pos:])
			if !unicode.IsSpace(r) && r != '\uFEFF' {
				return
			}
			p.pos += size
		}
	}
}

func isLineBreak(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029'
}

func (p *json5Parser) value(depth int) (any, error) {
	if depth > maxJSON5Depth {
		return nil, p.errorf("nesting deeper than %d", maxJSON5Depth)
	}
	if p.pos >= len(p.src) {
		return nil, p.errorf("unexpected end of input")
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"' || c == '\'':
		return p.str()
	case c == '/':
		return nil, p.errorf("unterminated comment or stray '/'")
	default:
		return p.literal()
	}
}

func (p *json5Parser) object(depth int) (any, error) {
	p.pos++ // {
	obj := &Object{values: map[string]any{}}
	for {
		p.skip()
		if p.pos >= len(p.src) {
			return nil, p.errorf("unterminated object")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			return obj, nil
		}
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, p.errorf("expected ':' after a key")
		}
		p.pos++
		p.skip()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		obj.set(key, v)
		p.skip()
		if p.pos >= len(p.src) {
			return nil, p.errorf("unterminated object")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case '}':
		default:
			return nil, p.errorf("expected ',' or '}'")
		}
	}
}

func (p *json5Parser) array(depth int) (any, error) {
	p.pos++ // [
	arr := []any{}
	for {
		p.skip()
		if p.pos >= len(p.src) {
			return nil, p.errorf("unterminated array")
		}
		if p.src[p.pos] == ']' {
			p.pos++
			return arr, nil
		}
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
		p.skip()
		if p.pos >= len(p.src) {
			return nil, p.errorf("unterminated array")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ']':
		default:
			return nil, p.errorf("expected ',' or ']'")
		}
	}
}

func (p *json5Parser) key() (string, error) {
	if c := p.src[p.pos]; c == '"' || c == '\'' {
		v, err := p.str()
		if err != nil {
			return "", err
		}
		return v.(string), nil
	}
	start := p.pos
	for p.pos < len(p.src) {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if r == '$' || r == '_' || unicode.IsLetter(r) || (p.pos > start && unicode.IsDigit(r)) {
			p.pos += size
			continue
		}
		break
	}
	if p.pos == start {
		return "", p.errorf("expected a property name")
	}
	return p.src[start:p.pos], nil
}

func (p *json5Parser) str() (any, error) {
	quote := p.src[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == quote:
			p.pos++
			return b.String(), nil
		case c == '\\':
			if err := p.escape(&b); err != nil {
				return nil, err
			}
		case c == '\n' || c == '\r':
			return nil, p.errorf("line break in string")
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return nil, p.errorf("unterminated string")
}

func (p *json5Parser) escape(b *strings.Builder) error {
	p.pos++ // backslash
	if p.pos >= len(p.src) {
		return p.errorf("unterminated escape")
	}
	c := p.src[p.pos]
	p.pos++
	switch c {
	case 'n':
		b.WriteByte('\n')
	case 't':
		b.WriteByte('\t')
	case 'r':
		b.WriteByte('\r')
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case 'v':
		b.WriteByte('\v')
	case '0':
		b.WriteByte(0)
	case '\n':
		// line continuation
	case '\r':
		if p.pos < len(p.src) && p.src[p.pos] == '\n' {
			p.pos++
		}
	case 'x':
		n, err := p.hex(2)
		if err != nil {
			return err
		}
		b.WriteRune(rune(n))
	case 'u':
		n, err := p.hex(4)
		if err != nil {
			return err
		}
		r := rune(n)
		if r >= 0xD800 && r < 0xDC00 && strings.HasPrefix(p.src[p.pos:], `\u`) {
			save := p.pos
			p.pos += 2
			low, err := p.hex(4)
			if err == nil && low >= 0xDC00 && low < 0xE000 {
				r = 0x10000 + (r-0xD800)<<10 + (rune(low) - 0xDC00)
			} else {
				p.pos = save
			}
		}
		b.WriteRune(r)
	default:
		// '"', '\'', '\\', '/' and any other character stand for themselves.
		p.pos--
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		p.pos += size
		b.WriteRune(r)
	}
	return nil
}

func (p *json5Parser) hex(digits int) (int, error) {
	if p.pos+digits > len(p.src) {
		return 0, p.errorf("short hex escape")
	}
	n, err := strconv.ParseUint(p.src[p.pos:p.pos+digits], 16, 32)
	if err != nil {
		return 0, p.errorf("bad hex escape")
	}
	p.pos += digits
	return int(n), nil
}

// literal reads true, false, null, Infinity, NaN or a number.
func (p *json5Parser) literal() (any, error) {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ',' || c == '}' || c == ']' || c == ':' || c == '/' || c == '"' || c == '\'' || c == '{' || c == '[' {
			break
		}
		if r, _ := utf8.DecodeRuneInString(p.src[p.pos:]); unicode.IsSpace(r) {
			break
		}
		p.pos++
	}
	word := p.src[start:p.pos]
	switch word {
	case "":
		return nil, p.errorf("unexpected character")
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	n, ok := number(word)
	if !ok {
		p.pos = start
		return nil, p.errorf("invalid value")
	}
	return n, nil
}

func number(word string) (float64, bool) {
	sign := 1.0
	switch {
	case strings.HasPrefix(word, "+"):
		word = word[1:]
	case strings.HasPrefix(word, "-"):
		sign = -1
		word = word[1:]
	}
	switch word {
	case "Infinity":
		return sign * math.Inf(1), true
	case "NaN":
		return math.NaN(), true
	case "":
		return 0, false
	}
	if strings.HasPrefix(word, "0x") || strings.HasPrefix(word, "0X") {
		n, err := strconv.ParseUint(word[2:], 16, 64)
		return sign * float64(n), err == nil
	}
	// ParseFloat accepts underscores only with base prefixes and "inf"/"nan" spellings that JSON5
	// does not have; reject those first.
	if strings.ContainsAny(word, "_iInN") {
		return 0, false
	}
	n, err := strconv.ParseFloat(word, 64)
	return sign * n, err == nil
}
