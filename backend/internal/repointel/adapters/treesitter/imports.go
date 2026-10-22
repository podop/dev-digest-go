package treesitter

import (
	"strconv"
	"strings"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// typeOnlyKinds are the node kinds whose whole subtree is a type position or an ambient
// declaration: TypeScript's emit drops them, so identifiers and import() calls inside never make
// an import a value import.
var typeOnlyKinds = map[string]struct{}{
	"type_annotation": {}, "type_arguments": {}, "type_parameters": {}, "type_alias_declaration": {},
	"interface_declaration": {}, "implements_clause": {}, "extends_type_clause": {},
	"ambient_declaration": {}, "function_signature": {}, "method_signature": {},
	"abstract_method_signature": {}, "property_signature": {}, "index_signature": {},
	"call_signature": {}, "construct_signature": {}, "type_query": {}, "nested_type_identifier": {},
	"generic_type": {}, "type_predicate": {}, "type_predicate_annotation": {}, "asserts": {},
	"asserts_annotation": {}, "opting_type_annotation": {}, "omitting_type_annotation": {},
	"adding_type_annotation": {},
}

// scopeKinds open a lexical scope for the shadowing check; functionKinds also take var
// declarations.
var (
	functionKinds = map[string]struct{}{
		"function_declaration": {}, "generator_function_declaration": {}, "function_expression": {},
		"generator_function": {}, "arrow_function": {}, "method_definition": {},
	}
	blockKinds = map[string]struct{}{
		"statement_block": {}, "for_statement": {}, "for_in_statement": {}, "catch_clause": {},
		"switch_body": {}, "class_static_block": {},
	}
)

// candidate is one import specifier in document order. A candidate with bindings is kept only
// when one of them is used as a value (TypeScript files).
type candidate struct {
	spec     string
	bindings []string
}

type scopeEntry struct {
	id uintptr
	fn bool
}

type valueRef struct {
	name  string
	scope []uintptr // ids of the enclosing scopes, outermost first
}

// importScan collects the import specifiers of one file (spec section 7.5).
type importScan struct {
	src []byte
	// ts: the file is .ts or .tsx, so unused and type-only imports are dropped.
	ts   bool
	want map[string]struct{} // binding names of the file's import statements (ts only)

	cands []candidate
	stack []scopeEntry
	refs  []valueRef
	// bound holds the ids of identifier nodes that declare a wanted name, decls the wanted names
	// declared per scope.
	bound map[uintptr]struct{}
	decls map[uintptr]map[string]struct{}
}

// extractImports returns the import specifiers of a file in first-seen order, without empty
// strings and duplicates. ts selects the TypeScript rules (imports that are never used as a value
// are elided); otherwise every specifier counts.
func extractImports(root sitter.Node, src []byte, ts bool) []string {
	s := &importScan{
		src:   src,
		ts:    ts,
		want:  map[string]struct{}{},
		bound: map[uintptr]struct{}{},
		decls: map[uintptr]map[string]struct{}{},
	}
	if ts {
		for _, top := range children(&root) {
			if top.Kind() == "import_statement" {
				for _, name := range s.importBindings(top) {
					s.want[name] = struct{}{}
				}
			}
		}
	}
	s.visit(&root, false)

	used := s.usedNames()
	var out []string
	seen := map[string]struct{}{}
	for _, c := range s.cands {
		if c.spec == "" {
			continue
		}
		if len(c.bindings) > 0 && !anyIn(used, c.bindings) {
			continue
		}
		if _, dup := seen[c.spec]; dup {
			continue
		}
		seen[c.spec] = struct{}{}
		out = append(out, c.spec)
	}
	return out
}

func anyIn(set map[string]struct{}, names []string) bool {
	for _, n := range names {
		if _, ok := set[n]; ok {
			return true
		}
	}
	return false
}

// usedNames returns the wanted names that have a value reference not shadowed by a local
// declaration.
func (s *importScan) usedNames() map[string]struct{} {
	used := map[string]struct{}{}
	for _, r := range s.refs {
		shadowed := false
		for _, id := range r.scope {
			if _, ok := s.decls[id][r.name]; ok {
				shadowed = true
				break
			}
		}
		if !shadowed {
			used[r.name] = struct{}{}
		}
	}
	return used
}

func hasChildKind(n *sitter.Node, kind string) bool {
	for i := range n.ChildCount() {
		if c := n.Child(i); c != nil && c.Kind() == kind {
			return true
		}
	}
	return false
}

// importBindings returns the non-type binding names of an import statement.
func (s *importScan) importBindings(stmt *sitter.Node) []string {
	if hasChildKind(stmt, "type") {
		return nil
	}
	var names []string
	for _, clause := range children(stmt) {
		if clause.Kind() != "import_clause" {
			continue
		}
		for _, c := range children(clause) {
			switch c.Kind() {
			case "identifier":
				names = append(names, nodeText(s.src, c))
			case "namespace_import":
				for _, id := range children(c) {
					if id.Kind() == "identifier" {
						names = append(names, nodeText(s.src, id))
					}
				}
			case "named_imports":
				for _, spec := range children(c) {
					if spec.Kind() != "import_specifier" || hasChildKind(spec, "type") {
						continue
					}
					nameNode := field(spec, "alias")
					if nameNode == nil {
						nameNode = field(spec, "name")
					}
					if nameNode != nil {
						names = append(names, nodeText(s.src, nameNode))
					}
				}
			}
		}
	}
	return names
}

func (s *importScan) addCandidate(spec *sitter.Node, bindings []string) {
	if spec == nil {
		return
	}
	if text, ok := stringValue(s.src, spec); ok {
		s.cands = append(s.cands, candidate{spec: text, bindings: bindings})
	}
}

// importStatement records the specifier of `import … from 'x'` / `import 'x'`.
func (s *importScan) importStatement(stmt *sitter.Node) {
	if hasChildKind(stmt, "type") || hasChildKind(stmt, "import_require_clause") {
		return
	}
	var bindings []string
	if s.ts {
		if !hasChildKind(stmt, "import_clause") {
			s.addCandidate(field(stmt, "source"), nil) // side-effect import
			return
		}
		bindings = s.importBindings(stmt)
		if len(bindings) == 0 {
			return
		}
	}
	s.addCandidate(field(stmt, "source"), bindings)
}

// reExport records the specifier of `export … from 'x'`.
func (s *importScan) reExport(stmt, source *sitter.Node) {
	if hasChildKind(stmt, "type") || (s.ts && typeKeywordInError(stmt)) {
		return
	}
	if s.ts {
		if clause := firstChildKind(stmt, "export_clause"); clause != nil {
			valueSpec := false
			for _, spec := range children(clause) {
				if spec.Kind() == "export_specifier" && !hasChildKind(spec, "type") {
					valueSpec = true
				}
			}
			if !valueSpec {
				return
			}
		}
	}
	s.addCandidate(source, nil)
}

// typeKeywordInError reports whether the statement holds the `type` keyword inside an ERROR node.
// The pinned TypeScript grammar (0.23.2) cannot parse `export type * from 'x'` and
// `export type * as ns from 'x'` (TypeScript 5.0 syntax) and wraps the keyword in an ERROR child
// of the export statement; both forms are type-only and have no specifier.
func typeKeywordInError(stmt *sitter.Node) bool {
	for i := range stmt.ChildCount() {
		if c := stmt.Child(i); c != nil && c.Kind() == "ERROR" && hasChildKind(c, "type") {
			return true
		}
	}
	return false
}

func firstChildKind(n *sitter.Node, kind string) *sitter.Node {
	for i := range n.ChildCount() {
		if c := n.Child(i); c != nil && c.Kind() == kind {
			return c
		}
	}
	return nil
}

// callSpecifier records `import('x')`, `require('x')` and, in JavaScript files, the string
// elements of an AMD `define([...])` / `require([...])`.
func (s *importScan) callSpecifier(call *sitter.Node) {
	fn := field(call, "function")
	args := field(call, "arguments")
	if fn == nil || args == nil {
		return
	}
	var callee string
	switch fn.Kind() {
	case "import":
		callee = "import"
	case "identifier":
		callee = nodeText(s.src, fn)
	default:
		return
	}
	first := args.NamedChild(0)
	if first == nil {
		return
	}
	switch callee {
	case "import", "require":
		if first.Kind() == "string" || first.Kind() == "template_string" {
			s.addCandidate(first, nil)
			return
		}
	}
	if !s.ts && (callee == "define" || callee == "require") && first.Kind() == "array" {
		for _, el := range children(first) {
			if el.Kind() == "string" || el.Kind() == "template_string" {
				s.addCandidate(el, nil)
			}
		}
	}
}

// visit walks the tree in document order, collecting specifiers, declarations and value
// references.
func (s *importScan) visit(n *sitter.Node, inType bool) {
	kind := n.Kind()
	if s.ts && !inType {
		if _, ok := typeOnlyKinds[kind]; ok {
			inType = true
		}
	}
	switch kind {
	case "import_statement":
		if !inType {
			s.importStatement(n)
		}
		return
	case "export_statement":
		if source := field(n, "source"); source != nil {
			if !inType {
				s.reExport(n, source)
			}
			return
		}
		if s.ts && hasChildKind(n, "type") {
			inType = true
		}
	case "call_expression":
		if !inType {
			s.callSpecifier(n)
		}
	case "identifier", "shorthand_property_identifier":
		if s.ts && !inType {
			s.reference(n)
		}
		return
	}

	pushed := s.enter(n, kind)
	asType := kind == "as_expression" || kind == "satisfies_expression"
	for i, count := uint(0), n.ChildCount(); i < count; i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		s.visit(c, inType || (s.ts && asType && i > 0))
	}
	if pushed {
		s.stack = s.stack[:len(s.stack)-1]
	}
}

// enter registers the declarations a node makes and opens its scope; it reports whether a scope
// was pushed.
func (s *importScan) enter(n *sitter.Node, kind string) bool {
	if !s.ts || len(s.want) == 0 {
		return false
	}
	// Names declared in the enclosing scope.
	switch kind {
	case "function_declaration", "generator_function_declaration", "class_declaration",
		"abstract_class_declaration", "enum_declaration":
		s.bindName(field(n, "name"), s.top())
	}
	_, isFn := functionKinds[kind]
	_, isBlock := blockKinds[kind]
	if !isFn && !isBlock {
		if kind == "variable_declarator" {
			s.bindDeclarator(n)
		}
		return false
	}
	s.stack = append(s.stack, scopeEntry{id: n.Id(), fn: isFn})
	self := n.Id()
	switch kind {
	case "function_expression", "generator_function":
		s.bindName(field(n, "name"), self)
	case "catch_clause":
		s.bindPattern(field(n, "parameter"), self)
	case "for_in_statement":
		if field(n, "kind") != nil {
			s.bindPattern(field(n, "left"), self)
		}
	}
	if isFn {
		s.bindPattern(field(n, "parameters"), self)
		s.bindPattern(field(n, "parameter"), self)
	}
	return true
}

// top is the id of the innermost open scope, 0 at the top level of the file.
func (s *importScan) top() uintptr {
	if len(s.stack) == 0 {
		return 0
	}
	return s.stack[len(s.stack)-1].id
}

// nearestFunction is the id of the innermost open function scope, 0 at the top level.
func (s *importScan) nearestFunction() uintptr {
	for i := len(s.stack) - 1; i >= 0; i-- {
		if s.stack[i].fn {
			return s.stack[i].id
		}
	}
	return 0
}

// bindDeclarator registers the names of a variable declarator: var in the nearest function,
// let and const in the innermost scope.
func (s *importScan) bindDeclarator(decl *sitter.Node) {
	scope := s.top()
	if p := decl.Parent(); p != nil && p.Kind() == "variable_declaration" {
		scope = s.nearestFunction()
	}
	s.bindPattern(field(decl, "name"), scope)
}

// bindName registers a declared name (an identifier node) in a scope; scope 0 (the file's top
// level) is not tracked.
func (s *importScan) bindName(id *sitter.Node, scope uintptr) {
	if id == nil || scope == 0 {
		return
	}
	switch id.Kind() {
	case "identifier", "type_identifier", "shorthand_property_identifier_pattern":
	default:
		return
	}
	name := nodeText(s.src, id)
	if _, ok := s.want[name]; !ok {
		return
	}
	s.bound[id.Id()] = struct{}{}
	if s.decls[scope] == nil {
		s.decls[scope] = map[string]struct{}{}
	}
	s.decls[scope][name] = struct{}{}
}

// bindPattern registers every name a binding pattern (parameters, destructuring) declares.
// Default values and computed keys inside the pattern are expressions and are not touched.
func (s *importScan) bindPattern(n *sitter.Node, scope uintptr) {
	if n == nil || scope == 0 {
		return
	}
	switch n.Kind() {
	case "identifier", "shorthand_property_identifier_pattern":
		s.bindName(n, scope)
	case "required_parameter", "optional_parameter":
		s.bindPattern(field(n, "pattern"), scope)
	case "assignment_pattern", "object_assignment_pattern":
		s.bindPattern(field(n, "left"), scope)
	case "pair_pattern":
		s.bindPattern(field(n, "value"), scope)
	case "formal_parameters", "object_pattern", "array_pattern", "rest_pattern":
		for _, c := range children(n) {
			s.bindPattern(c, scope)
		}
	}
}

// reference records a value reference to a wanted name.
func (s *importScan) reference(id *sitter.Node) {
	if len(s.want) == 0 {
		return
	}
	name := nodeText(s.src, id)
	if _, ok := s.want[name]; !ok {
		return
	}
	if _, isBinding := s.bound[id.Id()]; isBinding {
		return
	}
	if p := id.Parent(); p != nil {
		switch p.Kind() {
		case "jsx_namespace_name":
			return
		case "export_specifier":
			// Only the exported local name refers to a binding of this file, not the alias.
			if nameNode := field(p, "name"); nameNode == nil || nameNode.Id() != id.Id() || hasChildKind(p, "type") {
				return
			}
		}
	}
	ids := make([]uintptr, len(s.stack))
	for i, e := range s.stack {
		ids[i] = e.id
	}
	s.refs = append(s.refs, valueRef{name: name, scope: ids})
}

// stringValue returns the value of a string literal or of a template literal without
// substitutions; ok is false for any other node.
func stringValue(src []byte, n *sitter.Node) (string, bool) {
	switch n.Kind() {
	case "string":
	case "template_string":
		if hasChildKind(n, "template_substitution") {
			return "", false
		}
	default:
		return "", false
	}
	text := src[n.StartByte():n.EndByte()]
	if len(text) < 2 || text[0] != text[len(text)-1] {
		return "", false
	}
	return unescape(string(text[1 : len(text)-1])), true
}

// unescape decodes the JavaScript escape sequences of a string literal body.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			i++
			continue
		}
		i++
		switch e := s[i]; e {
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
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		case 'x':
			if r, size, ok := hexEscape(s[i+1:], 2); ok {
				b.WriteRune(r)
				i += size
			} else {
				b.WriteByte('x')
			}
		case 'u':
			if strings.HasPrefix(s[i+1:], "{") {
				if end := strings.IndexByte(s[i+1:], '}'); end > 0 {
					if v, err := strconv.ParseUint(s[i+2:i+1+end], 16, 32); err == nil && v <= utf8.MaxRune {
						b.WriteRune(rune(v))
						i += end + 1
						break
					}
				}
				b.WriteByte('u')
			} else if r, size, ok := hexEscape(s[i+1:], 4); ok {
				b.WriteRune(r)
				i += size
			} else {
				b.WriteByte('u')
			}
		default:
			// Any other escaped character stands for itself (including a multi-byte one).
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteRune(r)
			i += size - 1
		}
		i++
	}
	return b.String()
}

// hexEscape parses exactly digits hex digits at the start of s.
func hexEscape(s string, digits int) (rune, int, bool) {
	if len(s) < digits {
		return 0, 0, false
	}
	v, err := strconv.ParseUint(s[:digits], 16, 32)
	if err != nil {
		return 0, 0, false
	}
	return rune(v), digits, true
}
