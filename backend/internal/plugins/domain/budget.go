package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// checkBudget is the pre-pass of decodeBundle (security review r1, M1). encoding/json builds a
// slice, a map or an interface tree of the whole value before any count is checked, so a 20 MiB
// body of "0," items costs more than a gigabyte. This pass walks the text once, allocates
// nothing per value, and refuses a bundle that is over a budget before anything is decoded:
//
//   - at most MaxJSONValues values and keys in all, nested at most MaxJSONDepth levels;
//   - the list limits of the bundle (agents, skills, eval cases, learnings, and the sections an
//     import ignores: conventions, context files, skill versions and evidence files, and an
//     agent's skill names);
//   - at most MaxObjectKeys keys in the top object, the manifest, the settings and a list item.
//
// It is not a validator: a syntax error ends the pass quietly and the decoder, which validates
// the whole text before it allocates, reports it. Keys are matched the way the decoder matches
// them: unescaped, and case-insensitively inside list items (struct fields).
func checkBudget(text []byte) error {
	s := scanner{b: text}
	s.value(roleTop, 1, -1)
	if s.err != nil {
		return s.err
	}
	return nil
}

// role says what a JSON value is in the bundle, which decides the limits that apply to it.
type role uint8

const (
	roleOpaque role = iota // any value that is only bytes to the decoder
	roleTop
	roleManifest
	roleSettings
	roleAgents
	roleSkills
	roleCases
	roleLearnings
	roleConventions
	roleContextFiles
	roleAgentItem
	roleSkillItem
	roleCaseItem
	roleLearningItem
	roleAgentSkills
	roleVersions
	roleEvidenceFiles
)

// listLimit is the item limit of a list role; 0 means the role is no list.
func listLimit(r role) int {
	switch r {
	case roleAgents:
		return MaxAgents
	case roleSkills:
		return MaxSkills
	case roleCases:
		return MaxEvalCases
	case roleLearnings:
		return MaxLearnings
	case roleConventions:
		return MaxConventions
	case roleContextFiles:
		return MaxContextFiles
	case roleAgentSkills:
		return MaxSkills
	case roleVersions:
		return MaxSkillVersions
	case roleEvidenceFiles:
		return MaxEvidenceFiles
	}
	return 0
}

// listName names a list role in a message; owner is the index of the agent or skill that holds
// a nested list.
func listName(r role, owner int) string {
	switch r {
	case roleAgents:
		return "agents"
	case roleSkills:
		return "skills"
	case roleCases:
		return "eval_cases"
	case roleLearnings:
		return "learnings"
	case roleConventions:
		return "conventions"
	case roleContextFiles:
		return "context_files"
	case roleAgentSkills:
		return fmt.Sprintf("agents[%d].skills", owner)
	case roleVersions:
		return fmt.Sprintf("skills[%d].versions", owner)
	}
	return fmt.Sprintf("skills[%d].evidence_files", owner)
}

// itemRole is the role of the items of a list role.
func itemRole(r role) role {
	switch r {
	case roleAgents:
		return roleAgentItem
	case roleSkills:
		return roleSkillItem
	case roleCases:
		return roleCaseItem
	case roleLearnings:
		return roleLearningItem
	}
	return roleOpaque
}

// memberRole is the role of the value under key in an object of role r.
func memberRole(r role, key []byte) role {
	switch r {
	case roleTop:
		switch string(key) {
		case "manifest":
			return roleManifest
		case "settings":
			return roleSettings
		case "agents":
			return roleAgents
		case "skills":
			return roleSkills
		case "eval_cases":
			return roleCases
		case "learnings":
			return roleLearnings
		case "conventions":
			return roleConventions
		case "context_files":
			return roleContextFiles
		}
	case roleAgentItem:
		if bytes.EqualFold(key, []byte("skills")) {
			return roleAgentSkills
		}
	case roleSkillItem:
		switch {
		case bytes.EqualFold(key, []byte("versions")):
			return roleVersions
		case bytes.EqualFold(key, []byte("evidence_files")):
			return roleEvidenceFiles
		}
	}
	return roleOpaque
}

// objectName names an object role in a key-limit message.
func objectName(r role, idx int) string {
	switch r {
	case roleTop:
		return BundleFile
	case roleManifest:
		return "manifest"
	case roleSettings:
		return "settings"
	case roleAgentItem:
		return fmt.Sprintf("agents[%d]", idx)
	case roleSkillItem:
		return fmt.Sprintf("skills[%d]", idx)
	case roleCaseItem:
		return fmt.Sprintf("eval_cases[%d]", idx)
	}
	return fmt.Sprintf("learnings[%d]", idx)
}

// keyedObject reports whether an object of role r has the MaxObjectKeys limit.
func keyedObject(r role) bool {
	switch r {
	case roleTop, roleManifest, roleSettings, roleAgentItem, roleSkillItem, roleCaseItem, roleLearningItem:
		return true
	}
	return false
}

// needsKeys reports whether the members of an object of role r get roles of their own.
func needsKeys(r role) bool {
	return r == roleTop || r == roleAgentItem || r == roleSkillItem
}

type scanner struct {
	b      []byte
	i      int
	values int
	err    *InvalidError
	// over is the outermost list that has passed its item limit and is still being walked.
	over *overList
	// stopped is set when the pass ends: a violation (err set) or a syntax error (err nil).
	stopped bool
}

// overList is a list past its limit: its items are counted, not decoded, to give the exact count
// in the message. If the value budget runs out first, the list is the reason.
type overList struct {
	r     role
	idx   int
	limit int
	depth int
}

// violation ends the pass with a refusal.
func (s *scanner) violation(err *InvalidError) {
	s.stopped = true
	s.err = err
}

// syntax ends the pass on text that is not JSON; the decoder reports that, with its own message.
func (s *scanner) syntax() { s.stopped = true }

func (s *scanner) skipSpace() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\r', '\n':
			s.i++
		default:
			return
		}
	}
}

// count books one value or key against the budget.
func (s *scanner) count() {
	s.values++
	if s.values > MaxJSONValues {
		if o := s.over; o != nil {
			s.violation(invalid("%s has more than %d entries; the limit is %d", listName(o.r, o.idx), o.limit, o.limit))
			return
		}
		s.violation(invalid("%s holds more than %d JSON values", BundleFile, MaxJSONValues))
	}
}

// value scans one value of role r at the given depth. idx is the position of the enclosing list
// item (or of the item that owns this value) for messages.
func (s *scanner) value(r role, depth, idx int) {
	if s.stopped {
		return
	}
	s.skipSpace()
	if s.i >= len(s.b) {
		s.syntax()
		return
	}
	s.count()
	if s.stopped {
		return
	}
	switch s.b[s.i] {
	case '{':
		s.object(r, depth, idx)
	case '[':
		s.array(r, depth, idx)
	case '"':
		s.str(false)
	default:
		s.literal()
	}
}

func (s *scanner) deeper(depth int) bool {
	if depth > MaxJSONDepth {
		s.violation(invalid("%s is nested deeper than %d levels", BundleFile, MaxJSONDepth))
		return true
	}
	return false
}

func (s *scanner) array(r role, depth, idx int) {
	if s.deeper(depth) {
		return
	}
	s.i++ // [
	limit := listLimit(r)
	child := itemRole(r)
	n := 0
	s.skipSpace()
	if s.i < len(s.b) && s.b[s.i] == ']' {
		s.i++
		return
	}
	for !s.stopped {
		s.value(child, depth+1, n)
		n++
		if limit > 0 && n > limit && s.over == nil && !s.stopped {
			s.over = &overList{r: r, idx: idx, limit: limit, depth: depth}
		}
		s.skipSpace()
		if s.stopped || s.i >= len(s.b) {
			s.syntax()
			return
		}
		c := s.b[s.i]
		s.i++
		if c == ']' {
			break
		}
		if c != ',' {
			s.syntax()
			return
		}
	}
	if s.over != nil && s.over.depth == depth {
		s.over = nil
	}
	// The count is exact, so the message is the one the decoder gives; items past the limit were
	// only counted, never decoded, and the global value budget still bounds the walk.
	if limit > 0 && n > limit && !s.stopped {
		s.violation(invalid("%s has %d entries; the limit is %d", listName(r, idx), n, limit))
	}
}

func (s *scanner) object(r role, depth, idx int) {
	if s.deeper(depth) {
		return
	}
	s.i++ // {
	n := 0
	s.skipSpace()
	if s.i < len(s.b) && s.b[s.i] == '}' {
		s.i++
		return
	}
	for !s.stopped {
		s.skipSpace()
		if s.i >= len(s.b) || s.b[s.i] != '"' {
			s.syntax()
			return
		}
		s.count()
		if s.stopped {
			return
		}
		key := s.str(needsKeys(r))
		n++
		if keyedObject(r) && n > MaxObjectKeys {
			s.violation(invalid("%s has more than %d keys", objectName(r, idx), MaxObjectKeys))
			return
		}
		s.skipSpace()
		if s.stopped || s.i >= len(s.b) || s.b[s.i] != ':' {
			s.syntax()
			return
		}
		s.i++
		member := roleOpaque
		if needsKeys(r) {
			member = memberRole(r, key)
		}
		s.value(member, depth+1, idx)
		s.skipSpace()
		if s.stopped || s.i >= len(s.b) {
			s.syntax()
			return
		}
		c := s.b[s.i]
		s.i++
		if c == '}' {
			return
		}
		if c != ',' {
			s.syntax()
			return
		}
	}
}

// str skips a string and returns its bytes between the quotes. With unescape set, escapes are
// resolved (which allocates); only the keys of objects whose members have roles ask for that, and
// those objects are bounded by MaxObjectKeys keys each.
func (s *scanner) str(unescape bool) []byte {
	start := s.i
	s.i++ // opening quote
	escaped := false
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case '"':
			s.i++
			raw := s.b[start+1 : s.i-1]
			if !escaped || !unescape {
				return raw
			}
			var out string
			if err := json.Unmarshal(s.b[start:s.i], &out); err != nil {
				return raw
			}
			return []byte(out)
		case '\\':
			escaped = true
			s.i += 2
		default:
			s.i++
		}
	}
	s.syntax()
	return nil
}

// literal skips a number, true, false or null.
func (s *scanner) literal() {
	start := s.i
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ',', ']', '}', ' ', '\t', '\r', '\n':
			if s.i == start {
				s.syntax()
			}
			return
		}
		s.i++
	}
}
