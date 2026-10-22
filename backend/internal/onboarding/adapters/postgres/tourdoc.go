package postgres

// The stored tour document (onboarding.json): its encoding and its contract check on read. The
// document is the wire shape of spec 7.3 (the vendored OnboardingTour contract); a stored
// document that no longer satisfies that contract, limits included, reads as absent.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"dev-digest-go/backend/internal/onboarding/domain"
)

// timeLayout is JavaScript's toISOString: UTC with milliseconds.
const timeLayout = "2006-01-02T15:04:05.000Z"

// maxSafeInt bounds the integers a JavaScript number holds exactly.
const maxSafeInt = 1 << 53

// storedTour is the document as it is written: field order is key order, nullable fields are
// always present, a run step's comment only when set.
type storedTour struct {
	RepoID        string       `json:"repo_id"`
	GeneratedAt   string       `json:"generated_at"`
	IndexedSHA    string       `json:"indexed_sha"`
	FilesIndexed  int          `json:"files_indexed"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	TokensIn      int          `json:"tokens_in"`
	TokensOut     int          `json:"tokens_out"`
	CostUSD       *float64     `json:"cost_usd"`
	PromptVersion int          `json:"prompt_version"`
	Language      string       `json:"language"`
	Architecture  storedArch   `json:"architecture"`
	CriticalPaths []storedPath `json:"critical_paths"`
	RunSteps      []storedStep `json:"run_steps"`
	ReadingPath   []storedPath `json:"reading_path"`
	FirstTasks    []storedTask `json:"first_tasks"`
}

type storedArch struct {
	Summary string       `json:"summary"`
	Nodes   []storedNode `json:"nodes"`
	Edges   []storedEdge `json:"edges"`
}

type storedNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

type storedEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type storedPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type storedStep struct {
	Command string `json:"command"`
	Comment string `json:"comment,omitempty"`
}

type storedTask struct {
	Title      string `json:"title"`
	Path       string `json:"path"`
	Complexity string `json:"complexity"`
}

// EncodeTour is the document written to onboarding.json. Text is made storable first: jsonb
// rejects U+0000 and invalid UTF-8.
func EncodeTour(t domain.Tour) ([]byte, error) {
	d := storedTour{
		RepoID: clean(t.RepoID), GeneratedAt: t.GeneratedAt.UTC().Format(timeLayout), IndexedSHA: clean(t.IndexedSHA),
		FilesIndexed: t.FilesIndexed, Provider: clean(t.Provider), Model: clean(t.Model),
		TokensIn: t.TokensIn, TokensOut: t.TokensOut, CostUSD: t.CostUSD, PromptVersion: t.PromptVersion,
		Language: t.Language,
		Architecture: storedArch{
			Summary: clean(t.Architecture.Summary),
			Nodes:   make([]storedNode, len(t.Architecture.Nodes)),
			Edges:   make([]storedEdge, len(t.Architecture.Edges)),
		},
		CriticalPaths: make([]storedPath, len(t.CriticalPaths)),
		RunSteps:      make([]storedStep, len(t.RunSteps)),
		ReadingPath:   make([]storedPath, len(t.ReadingPath)),
		FirstTasks:    make([]storedTask, len(t.FirstTasks)),
	}
	for i, n := range t.Architecture.Nodes {
		d.Architecture.Nodes[i] = storedNode{ID: clean(n.ID), Label: clean(n.Label), Kind: string(n.Kind)}
	}
	for i, e := range t.Architecture.Edges {
		d.Architecture.Edges[i] = storedEdge{From: clean(e.From), To: clean(e.To)}
	}
	for i, p := range t.CriticalPaths {
		d.CriticalPaths[i] = storedPath{Path: clean(p.Path), Reason: clean(p.Reason)}
	}
	for i, s := range t.RunSteps {
		d.RunSteps[i] = storedStep{Command: clean(s.Command), Comment: clean(s.Comment)}
	}
	for i, p := range t.ReadingPath {
		d.ReadingPath[i] = storedPath{Path: clean(p.Path), Reason: clean(p.Reason)}
	}
	for i, f := range t.FirstTasks {
		d.FirstTasks[i] = storedTask{Title: clean(f.Title), Path: clean(f.Path), Complexity: string(f.Complexity)}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("encode tour: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// clean makes a string storable in jsonb: U+0000 and invalid UTF-8 become U+FFFD.
func clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "�")
	}
	return strings.ToValidUTF8(s, "�")
}

// DecodeTour parses a stored document and checks it against the OnboardingTour contract (the
// Zod object of the vendored shared contract: required keys, string lengths in UTF-16 units,
// integers, the node-kind and complexity enums, the list maximums; unknown keys are ignored). ok
// is false for a document that fails any of it, and for a generated_at that is not an ISO-8601
// instant (the contract only asks for a string; every document this backend or Node wrote holds
// an instant).
func DecodeTour(doc []byte) (t domain.Tour, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return domain.Tour{}, false
	}
	root, isObj := v.(map[string]any)
	if !isObj {
		return domain.Tour{}, false
	}
	r := reader{}
	t.RepoID = r.str(root, "repo_id", 0, math.MaxInt)
	generatedAt := r.str(root, "generated_at", 0, math.MaxInt)
	t.IndexedSHA = r.str(root, "indexed_sha", 0, math.MaxInt)
	t.FilesIndexed = r.integer(root, "files_indexed")
	t.Provider = r.str(root, "provider", 0, math.MaxInt)
	t.Model = r.str(root, "model", 0, math.MaxInt)
	t.TokensIn = r.integer(root, "tokens_in")
	t.TokensOut = r.integer(root, "tokens_out")
	t.CostUSD = r.nullableNumber(root, "cost_usd")
	t.PromptVersion = r.integer(root, "prompt_version")
	if r.str(root, "language", 0, math.MaxInt) != domain.Language {
		r.fail()
	}

	arch := r.object(root, "architecture")
	t.Architecture = domain.Architecture{
		Summary: r.str(arch, "summary", 0, domain.MaxSummary),
		Nodes:   []domain.Node{},
		Edges:   []domain.Edge{},
	}
	for _, n := range r.list(arch, "nodes", domain.MaxNodes) {
		no := r.element(n)
		kind := r.str(no, "kind", 0, math.MaxInt)
		if kind != string(domain.KindEntry) && kind != string(domain.KindModule) &&
			kind != string(domain.KindStore) && kind != string(domain.KindExternal) {
			r.fail()
		}
		t.Architecture.Nodes = append(t.Architecture.Nodes, domain.Node{
			ID:    r.str(no, "id", 1, domain.MaxNodeID),
			Label: r.str(no, "label", 1, domain.MaxNodeLabel),
			Kind:  domain.NodeKind(kind),
		})
	}
	for _, e := range r.list(arch, "edges", domain.MaxEdges) {
		eo := r.element(e)
		t.Architecture.Edges = append(t.Architecture.Edges, domain.Edge{
			From: r.str(eo, "from", 1, domain.MaxNodeID),
			To:   r.str(eo, "to", 1, domain.MaxNodeID),
		})
	}
	t.CriticalPaths = r.pathItems(root, "critical_paths", domain.MaxCriticalPaths)
	t.ReadingPath = r.pathItems(root, "reading_path", domain.MaxReadingPath)
	t.RunSteps = []domain.RunStep{}
	for _, s := range r.list(root, "run_steps", domain.MaxRunSteps) {
		so := r.element(s)
		step := domain.RunStep{Command: r.str(so, "command", 1, domain.MaxCommand)}
		if _, present := so["comment"]; present {
			step.Comment = r.str(so, "comment", 0, domain.MaxComment)
		}
		t.RunSteps = append(t.RunSteps, step)
	}
	t.FirstTasks = []domain.FirstTask{}
	for _, f := range r.list(root, "first_tasks", domain.MaxFirstTasks) {
		fo := r.element(f)
		c := r.str(fo, "complexity", 0, math.MaxInt)
		if c != string(domain.ComplexityLow) && c != string(domain.ComplexityMedium) && c != string(domain.ComplexityHigh) {
			r.fail()
		}
		t.FirstTasks = append(t.FirstTasks, domain.FirstTask{
			Title:      r.str(fo, "title", 1, domain.MaxTaskTitle),
			Path:       r.str(fo, "path", 1, domain.MaxPath),
			Complexity: domain.Complexity(c),
		})
	}
	if r.failed {
		return domain.Tour{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, generatedAt)
	if err != nil {
		return domain.Tour{}, false
	}
	t.GeneratedAt = at.UTC()
	t.Language = domain.Language
	return t, true
}

// reader pulls typed values out of a decoded JSON document and remembers the first violation.
// After a violation every accessor answers its zero value.
type reader struct{ failed bool }

func (r *reader) fail() { r.failed = true }

func (r *reader) object(m map[string]any, key string) map[string]any {
	o, ok := m[key].(map[string]any)
	if !ok {
		r.fail()
	}
	return o
}

func (r *reader) element(v any) map[string]any {
	o, ok := v.(map[string]any)
	if !ok {
		r.fail()
	}
	return o
}

func (r *reader) list(m map[string]any, key string, limit int) []any {
	l, ok := m[key].([]any)
	if !ok || len(l) > limit {
		r.fail()
		return nil
	}
	return l
}

func (r *reader) str(m map[string]any, key string, atLeast, atMost int) string {
	s, ok := m[key].(string)
	if !ok {
		r.fail()
		return ""
	}
	if n := utf16Units(s); n < atLeast || n > atMost {
		r.fail()
	}
	return s
}

// integer reads a JSON number that JavaScript holds as an integer.
func (r *reader) integer(m map[string]any, key string) int {
	n, ok := m[key].(json.Number)
	if !ok {
		r.fail()
		return 0
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > maxSafeInt {
		r.fail()
		return 0
	}
	return int(f)
}

// nullableNumber reads z.number().nullable(): the key must be present; null is nil.
func (r *reader) nullableNumber(m map[string]any, key string) *float64 {
	raw, present := m[key]
	if !present {
		r.fail()
		return nil
	}
	if raw == nil {
		return nil
	}
	n, ok := raw.(json.Number)
	if !ok {
		r.fail()
		return nil
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		r.fail()
		return nil
	}
	return &f
}

func (r *reader) pathItems(m map[string]any, key string, limit int) []domain.PathItem {
	out := []domain.PathItem{}
	for _, p := range r.list(m, key, limit) {
		po := r.element(p)
		out = append(out, domain.PathItem{
			Path:   r.str(po, "path", 1, domain.MaxPath),
			Reason: r.str(po, "reason", 0, domain.MaxReason),
		})
	}
	return out
}

// utf16Units is JavaScript's string length.
func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
