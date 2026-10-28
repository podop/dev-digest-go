package domain

import (
	"context"
	"sort"
	"strings"
)

// MaxReferenceNameBytes bounds the length of a symbol a ReferenceScanner matches; a longer one
// never references anything (no real identifier comes close, and the automaton holds one node
// per byte of every name).
const MaxReferenceNameBytes = 256

// scanTickUnits is how much work (bytes of lines plus occurrences examined) a Scan does between
// two looks at its context.
const scanTickUnits = 1 << 15

// ReferenceScanner finds the lines of a text that reference many symbols in one pass. It gives,
// per symbol, exactly the lines ReferenceLines gives (the reference rules are shared; the
// differential test pins the equality), but in time linear in the text instead of
// symbols x text: the occurrences of all names are found with one Aho-Corasick walk, and the
// comment / import test and the comment-and-string blanking of a line are done once per line,
// not once per name. A Scan looks at its context every few thousand units of work, so
// cancellation is honoured inside a file.
//
// A ReferenceScanner is immutable after NewReferenceScanner and safe for concurrent Scans.
type ReferenceScanner struct {
	names []string // reduced symbol per input index; "" when it can never reference
	lens  []int
	nodes []acNode
	root  [256]int32 // root transitions; 0 means none (node 0 is the root, never a child)
}

type acEdge struct {
	b  byte
	to int32
}

type acNode struct {
	edges []acEdge // sorted by byte; the root's are in ReferenceScanner.root
	fail  int32
	dict  int32   // nearest proper suffix node with output, 0 when none
	out   []int32 // input indices of the symbols that end here
}

// NewReferenceScanner prepares a scanner for symbols. Each symbol is reduced like in
// ReferenceLines (a '.' keeps only the last member); an empty one, or one longer than
// MaxReferenceNameBytes, never references. Indices in Scan's callbacks are indices of symbols.
func NewReferenceScanner(symbols []string) *ReferenceScanner {
	s := &ReferenceScanner{names: make([]string, len(symbols)), lens: make([]int, len(symbols)), nodes: make([]acNode, 1)}
	for i, sym := range symbols {
		name := sym
		if k := strings.LastIndexByte(sym, '.'); k >= 0 {
			name = sym[k+1:]
		}
		if name == "" || len(name) > MaxReferenceNameBytes {
			continue
		}
		s.names[i], s.lens[i] = name, len(name)
		s.insert(name, int32(i))
	}
	s.link()
	return s
}

func (s *ReferenceScanner) insert(name string, sym int32) {
	var cur int32
	for i := 0; i < len(name); i++ {
		c := name[i]
		next, ok := s.child(cur, c)
		if !ok {
			next = int32(len(s.nodes))
			s.nodes = append(s.nodes, acNode{})
			if cur == 0 {
				s.root[c] = next
			} else {
				e := s.nodes[cur].edges
				at := sort.Search(len(e), func(k int) bool { return e[k].b >= c })
				e = append(e, acEdge{})
				copy(e[at+1:], e[at:])
				e[at] = acEdge{b: c, to: next}
				s.nodes[cur].edges = e
			}
		}
		cur = next
	}
	s.nodes[cur].out = append(s.nodes[cur].out, sym)
}

func (s *ReferenceScanner) child(state int32, c byte) (int32, bool) {
	if state == 0 {
		to := s.root[c]
		return to, to != 0
	}
	e := s.nodes[state].edges
	for k := range e { // nodes have a handful of edges; a linear walk beats a search
		if e[k].b == c {
			return e[k].to, true
		}
		if e[k].b > c {
			break
		}
	}
	return 0, false
}

// step is the transition of the automaton: follow fail links until a child on c exists.
func (s *ReferenceScanner) step(state int32, c byte) int32 {
	for {
		if to, ok := s.child(state, c); ok {
			return to
		}
		if state == 0 {
			return 0
		}
		state = s.nodes[state].fail
	}
}

// link computes the fail and dictionary links breadth first.
func (s *ReferenceScanner) link() {
	queue := make([]int32, 0, len(s.nodes))
	for c := range s.root {
		if to := s.root[c]; to != 0 {
			queue = append(queue, to) // fail = 0, dict = 0
		}
	}
	for head := 0; head < len(queue); head++ {
		u := queue[head]
		for _, e := range s.nodes[u].edges {
			f := s.step(s.nodes[u].fail, e.b)
			v := &s.nodes[e.to]
			v.fail = f
			if len(s.nodes[f].out) > 0 {
				v.dict = f
			} else {
				v.dict = s.nodes[f].dict
			}
			queue = append(queue, e.to)
		}
	}
}

// walk calls fn(symbol, start) for every occurrence of every name in text (overlapping ones
// included), in order of end position, until fn returns false. It reports whether it ran to the
// end.
func (s *ReferenceScanner) walk(text string, fn func(sym int32, start int) bool) bool {
	var state int32
	for i := 0; i < len(text); i++ {
		state = s.step(state, text[i])
		t := state
		if len(s.nodes[t].out) == 0 {
			t = s.nodes[t].dict
		}
		for ; t != 0; t = s.nodes[t].dict {
			for _, sym := range s.nodes[t].out {
				if !fn(sym, i+1-s.lens[sym]) {
					return false
				}
			}
		}
	}
	return true
}

// Scan reads content once and calls hit(i, line) for each 1-based line that references
// symbols[i], lines ascending per symbol (across symbols the order is by line). A symbol is
// ignored when skip(i) is true (skip may be nil) and is not reported again once hit returned
// false for it; Scan returns as soon as no symbol is left. A hit error ends the Scan and is
// returned as it is. Scan returns ctx's error when ctx ends; it looks at it every few thousand
// units of work, so a single huge line cannot hold it past the deadline for long.
func (s *ReferenceScanner) Scan(ctx context.Context, content string, skip func(i int) bool, hit func(i, line int) (more bool, err error)) error {
	return s.scan(ctx, declNode, content, skip, hit)
}

// ScanFile is Scan of the content of file with the declaration rules of its language on top of
// Node's (spec R-I1): in a file ending in .go a line whose code is func name( or
// func (receiver) name( declares name; in a file ending in .java an occurrence name( preceded,
// after whitespace, by a type or modifier token (a word other than return, throw, new, else, case,
// yield, assert or do; a ']'; a '>' closing a '<' of the line) declares it. A line that declares
// a name is no reference to it, as in Node. Any other file scans exactly like Scan.
func (s *ReferenceScanner) ScanFile(ctx context.Context, file, content string, skip func(i int) bool, hit func(i, line int) (more bool, err error)) error {
	return s.scan(ctx, declRuleFor(file), content, skip, hit)
}

func (s *ReferenceScanner) scan(ctx context.Context, rule declRule, content string, skip func(i int) bool, hit func(i, line int) (more bool, err error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var (
		active    = make([]bool, len(s.names))
		remaining int
	)
	for i := range active {
		if s.names[i] != "" && (skip == nil || !skip(i)) {
			active[i] = true
			remaining++
		}
	}
	if remaining == 0 {
		return nil
	}

	var (
		work    int
		ctxErr  error
		rawMark = make([]int32, len(s.names)) // line number of the last raw line containing the symbol
		touch   = make([]int32, len(s.names)) // line number of the last line that examined the symbol
		isDecl  = make([]bool, len(s.names))
		isRef   = make([]bool, len(s.names))
		touched []int32
		// per-line state read by the closures below
		line, raw string
		lineNo    int32
		sameLine  bool
		ld        lineDecl
	)
	tick := func(units int) bool {
		work += units
		if work < scanTickUnits {
			return true
		}
		work = 0
		ctxErr = ctx.Err()
		return ctxErr == nil
	}
	found := false
	probe := func(sym int32, _ int) bool {
		if active[sym] {
			found = true
			return false
		}
		return tick(1)
	}
	mark := func(sym int32, _ int) bool {
		if active[sym] {
			rawMark[sym] = lineNo
		}
		return tick(1)
	}
	examine := func(sym int32, p int) bool {
		if !active[sym] || (!sameLine && rawMark[sym] != lineNo) {
			return tick(1)
		}
		if touch[sym] != lineNo {
			touch[sym], isDecl[sym], isRef[sym] = lineNo, false, false
			touched = append(touched, sym)
		}
		if isDecl[sym] {
			return tick(1)
		}
		name, before, after := s.names[sym], line[:p], line[p+s.lens[sym]:]
		if declares(name, before, after) || ld.declares(line, name, p) {
			isDecl[sym] = true
		} else if !isRef[sym] && references(name, before, after) {
			isRef[sym] = true
		}
		return tick(1)
	}

	for start, n := 0, 1; start <= len(content); n++ {
		end := strings.IndexByte(content[start:], '\n')
		if end < 0 {
			end = len(content) - start
		}
		raw = content[start : start+end]
		start += end + 1
		if !tick(len(raw) + 1) {
			return ctxErr
		}
		if isCommentLine(raw) || isImportLine(raw) {
			continue
		}

		found = false
		if s.walk(raw, probe); ctxErr != nil {
			return ctxErr
		}
		if !found {
			continue
		}
		line = sanitizeLine(raw)
		sameLine = len(line) == len(raw) && line == raw
		lineNo = int32(n)
		ld = newLineDecl(rule, line)
		if !sameLine {
			if s.walk(raw, mark); ctxErr != nil {
				return ctxErr
			}
		}
		touched = touched[:0]
		if s.walk(line, examine); ctxErr != nil {
			return ctxErr
		}
		for _, sym := range touched {
			if !active[sym] || isDecl[sym] || !isRef[sym] {
				continue
			}
			more, err := hit(int(sym), n)
			if err != nil {
				return err
			}
			if !more {
				active[sym] = false
				if remaining--; remaining == 0 {
					return nil
				}
			}
		}
	}
	return nil
}
