package postgres

// The stored brief: pr_brief.json holds the PrBrief document of the contract
// (frontend/src/vendor/shared/contracts/brief.ts) written in contract field order. A stored value
// is checked against that contract when it is read, like Node's PrBrief.safeParse (limits
// included, unknown keys ignored); one that fails reads as no brief. jsonb does not keep key
// order, which is why the shape lives in structs and never in the stored text.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf16"

	"dev-digest-go/backend/internal/brief/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// timeLayout is ISO-8601 UTC with milliseconds, as JavaScript's toISOString.
const timeLayout = "2006-01-02T15:04:05.000Z"

type briefDoc struct {
	Summary       string       `json:"summary"`
	Risks         risksDoc     `json:"risks"`
	ReviewFocus   []focusDoc   `json:"review_focus"`
	Intent        *intentDoc   `json:"intent"`
	Blast         *blastDoc    `json:"blast"`
	MissingInputs []missingDoc `json:"missing_inputs"`
	SpecsUsed     []string     `json:"specs_used"`
	HeadSHA       string       `json:"head_sha"`
	GeneratedAt   string       `json:"generated_at"`
	PromptVersion string       `json:"prompt_version"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	TokensIn      whole        `json:"tokens_in"`
	TokensOut     whole        `json:"tokens_out"`
	CostUSD       *float64     `json:"cost_usd"`
	ModelRequests whole        `json:"model_requests"`
}

type risksDoc struct {
	Risks []riskDoc `json:"risks"`
}

type riskDoc struct {
	Kind        string   `json:"kind"`
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Severity    string   `json:"severity"`
	FileRefs    []string `json:"file_refs"`
}

type focusDoc struct {
	File   string `json:"file"`
	Line   whole  `json:"line"`
	Reason string `json:"reason"`
}

type intentDoc struct {
	Intent      string      `json:"intent"`
	InScope     []string    `json:"in_scope"`
	OutOfScope  []string    `json:"out_of_scope"`
	ChangeType  *string     `json:"change_type"`
	Confidence  *string     `json:"confidence"`
	DerivedFrom *string     `json:"derived_from"`
	Sources     []sourceDoc `json:"sources"`
}

type sourceDoc struct {
	Kind   string  `json:"kind"`
	Ref    string  `json:"ref"`
	Status string  `json:"status"`
	Detail *string `json:"detail"`
}

type blastDoc struct {
	ChangedSymbols []symbolDoc     `json:"changed_symbols"`
	Downstream     []downstreamDoc `json:"downstream"`
	Summary        string          `json:"summary"`
	Degraded       *bool           `json:"degraded,omitempty"`
	Reason         *string         `json:"reason,omitempty"`
}

type symbolDoc struct {
	Name string `json:"name"`
	File string `json:"file"`
	Kind string `json:"kind"`
}

type downstreamDoc struct {
	Symbol            string      `json:"symbol"`
	Callers           []callerDoc `json:"callers"`
	EndpointsAffected []string    `json:"endpoints_affected"`
	CronsAffected     []string    `json:"crons_affected"`
}

type callerDoc struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line whole  `json:"line"`
}

type missingDoc struct {
	Input  string `json:"input"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// storable makes s storable in jsonb: U+0000, which jsonb rejects (SQLSTATE 22P05), and invalid
// UTF-8 become U+FFFD (INSIGHTS: clone-derived and PR text is sanitised before an upsert).
func storable(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "�")
	}
	return strings.ToValidUTF8(s, "�")
}

func storableAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = storable(s)
	}
	return out
}

func storablePtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := storable(*s)
	return &v
}

// toDocument maps a brief to its stored document: every list is a list (never null) and every
// text is storable.
func toDocument(b domain.Brief) briefDoc {
	doc := briefDoc{
		Summary:       storable(b.Summary),
		Risks:         risksDoc{Risks: make([]riskDoc, len(b.Risks))},
		ReviewFocus:   make([]focusDoc, len(b.ReviewFocus)),
		MissingInputs: make([]missingDoc, len(b.MissingInputs)),
		SpecsUsed:     storableAll(b.SpecsUsed),
		HeadSHA:       storable(b.HeadSHA),
		GeneratedAt:   b.GeneratedAt.UTC().Format(timeLayout),
		PromptVersion: storable(b.PromptVersion),
		Provider:      storable(b.Provider),
		Model:         storable(b.Model),
		TokensIn:      whole(b.TokensIn),
		TokensOut:     whole(b.TokensOut),
		CostUSD:       b.CostUSD,
		ModelRequests: whole(b.ModelRequests),
	}
	for i, r := range b.Risks {
		doc.Risks.Risks[i] = riskDoc{
			Kind: storable(r.Kind), Title: storable(r.Title), Explanation: storable(r.Explanation),
			Severity: string(r.Severity), FileRefs: storableAll(r.FileRefs),
		}
	}
	for i, f := range b.ReviewFocus {
		doc.ReviewFocus[i] = focusDoc{File: storable(f.File), Line: whole(f.Line), Reason: storable(f.Reason)}
	}
	for i, m := range b.MissingInputs {
		doc.MissingInputs[i] = missingDoc{Input: string(m.Input), Reason: string(m.Reason), Detail: storable(m.Detail)}
	}
	if in := b.Intent; in != nil {
		d := &intentDoc{
			Intent: storable(in.Text), InScope: storableAll(in.InScope), OutOfScope: storableAll(in.OutOfScope),
			ChangeType: storablePtr(in.ChangeType), Confidence: storablePtr(in.Confidence),
			Sources: make([]sourceDoc, len(in.Sources)),
		}
		if in.DerivedFrom != "" {
			d.DerivedFrom = ptr(storable(in.DerivedFrom))
		}
		for i, s := range in.Sources {
			d.Sources[i] = sourceDoc{Kind: storable(s.Kind), Ref: storable(s.Ref), Status: storable(s.Status), Detail: storablePtr(s.Detail)}
		}
		doc.Intent = d
	}
	if bl := b.Blast; bl != nil {
		d := &blastDoc{
			ChangedSymbols: make([]symbolDoc, len(bl.ChangedSymbols)),
			Downstream:     make([]downstreamDoc, len(bl.Downstream)),
			Summary:        storable(bl.Summary),
			Degraded:       ptr(bl.Degraded),
		}
		if bl.Reason != "" {
			d.Reason = ptr(storable(bl.Reason))
		}
		for i, s := range bl.ChangedSymbols {
			d.ChangedSymbols[i] = symbolDoc{Name: storable(s.Name), File: storable(s.File), Kind: storable(s.Kind)}
		}
		for i, g := range bl.Downstream {
			dg := downstreamDoc{
				Symbol: storable(g.Symbol), Callers: make([]callerDoc, len(g.Callers)),
				EndpointsAffected: storableAll(g.EndpointsAffected), CronsAffected: storableAll(g.CronsAffected),
			}
			for j, c := range g.Callers {
				dg.Callers[j] = callerDoc{Name: storable(c.Name), File: storable(c.File), Line: whole(c.Line)}
			}
			d.Downstream[i] = dg
		}
		doc.Blast = d
	}
	return doc
}

func ptr[T any](v T) *T { return &v }

// parseDocument returns the brief of a stored document; ok is false when the document does not
// satisfy the contract (an older shape, a corrupted value, a text that is not a timestamp).
func parseDocument(raw []byte) (domain.Brief, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil || !briefContract(tree) {
		return domain.Brief{}, false
	}
	var doc briefDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return domain.Brief{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, doc.GeneratedAt)
	if err != nil {
		return domain.Brief{}, false
	}
	return fromDocument(doc, at.UTC()), true
}

func fromDocument(doc briefDoc, generatedAt time.Time) domain.Brief {
	b := domain.Brief{
		Summary:       doc.Summary,
		Risks:         make([]domain.Risk, len(doc.Risks.Risks)),
		ReviewFocus:   make([]domain.FocusItem, len(doc.ReviewFocus)),
		MissingInputs: make([]domain.MissingInput, len(doc.MissingInputs)),
		SpecsUsed:     nonNil(doc.SpecsUsed),
		HeadSHA:       doc.HeadSHA,
		GeneratedAt:   generatedAt,
		PromptVersion: doc.PromptVersion,
		Provider:      doc.Provider,
		Model:         doc.Model,
		TokensIn:      int(doc.TokensIn),
		TokensOut:     int(doc.TokensOut),
		CostUSD:       doc.CostUSD,
		ModelRequests: int(doc.ModelRequests),
	}
	for i, r := range doc.Risks.Risks {
		b.Risks[i] = domain.Risk{Kind: r.Kind, Title: r.Title, Explanation: r.Explanation, Severity: domain.RiskSeverity(r.Severity), FileRefs: nonNil(r.FileRefs)}
	}
	for i, f := range doc.ReviewFocus {
		b.ReviewFocus[i] = domain.FocusItem{File: f.File, Line: int(f.Line), Reason: f.Reason}
	}
	for i, m := range doc.MissingInputs {
		b.MissingInputs[i] = domain.MissingInput{Input: domain.MissingInputName(m.Input), Reason: domain.MissingReason(m.Reason), Detail: m.Detail}
	}
	if in := doc.Intent; in != nil {
		it := &reviewer.Intent{
			Text: in.Intent, InScope: nonNil(in.InScope), OutOfScope: nonNil(in.OutOfScope),
			ChangeType: in.ChangeType, Confidence: in.Confidence, Sources: make([]reviewer.IntentSource, len(in.Sources)),
		}
		if in.DerivedFrom != nil {
			it.DerivedFrom = *in.DerivedFrom
		}
		for i, s := range in.Sources {
			it.Sources[i] = reviewer.IntentSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail}
		}
		b.Intent = it
	}
	if bl := doc.Blast; bl != nil {
		out := &domain.Blast{
			ChangedSymbols: make([]domain.BlastSymbol, len(bl.ChangedSymbols)),
			Downstream:     make([]domain.BlastGroup, len(bl.Downstream)),
			Summary:        bl.Summary,
			Degraded:       bl.Degraded != nil && *bl.Degraded,
		}
		if bl.Reason != nil {
			out.Reason = *bl.Reason
		}
		for i, s := range bl.ChangedSymbols {
			out.ChangedSymbols[i] = domain.BlastSymbol{Name: s.Name, File: s.File, Kind: s.Kind}
		}
		for i, g := range bl.Downstream {
			dg := domain.BlastGroup{
				Symbol: g.Symbol, Callers: make([]domain.BlastCaller, len(g.Callers)),
				EndpointsAffected: nonNil(g.EndpointsAffected), CronsAffected: nonNil(g.CronsAffected),
			}
			for j, c := range g.Callers {
				dg.Callers[j] = domain.BlastCaller{Name: c.Name, File: c.File, Line: int(c.Line)}
			}
			out.Downstream[i] = dg
		}
		b.Blast = out
	}
	return b
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// The contract, as checks over a decoded JSON tree (numbers are json.Number).

type check func(v any) bool

// field is a key of an object contract: Zod's plain (required), .optional() (optional), .nullable()
// (null) and .nullish() (both) keys.
type field struct {
	name string
	c    check
	// optional: the key may be absent; null: the value may be null.
	optional, null bool
}

func obj(fields ...field) check {
	return func(v any) bool {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for _, f := range fields {
			val, present := m[f.name]
			switch {
			case !present:
				if !f.optional {
					return false
				}
			case val == nil:
				if !f.null {
					return false
				}
			case !f.c(val):
				return false
			}
		}
		return true
	}
}

func str(v any) bool { _, ok := v.(string); return ok }

func boolean(v any) bool { _, ok := v.(bool); return ok }

// utf16Len is JavaScript's string length.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func strMax(max int) check {
	return func(v any) bool { s, ok := v.(string); return ok && utf16Len(s) <= max }
}

func oneOf(values ...string) check {
	return func(v any) bool {
		s, ok := v.(string)
		if !ok {
			return false
		}
		for _, want := range values {
			if s == want {
				return true
			}
		}
		return false
	}
}

func number(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	f, err := n.Float64()
	return err == nil && !math.IsInf(f, 0)
}

// maxWhole is the largest integer a JSON number is read as exactly (JavaScript's safe integer).
const maxWhole = 1<<53 - 1

// integer accepts what z.number().int() does: a number with no fractional part (1.0 is 1, 1e3 is
// 1000), up to maxWhole in magnitude (beyond it the value could not be held exactly).
func integer(v any) bool { return integerAtLeast(math.Inf(-1))(v) }

func integerAtLeast(min float64) check {
	return func(v any) bool {
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f) && math.Abs(f) <= maxWhole && f >= min
	}
}

// whole is an int that reads any JSON number with no fractional part (the contract's integers).
type whole int

func (w *whole) UnmarshalJSON(b []byte) error {
	f, err := json.Number(b).Float64()
	if err != nil || f != math.Trunc(f) || math.Abs(f) > maxWhole {
		return fmt.Errorf("not a whole number: %s", b)
	}
	*w = whole(f)
	return nil
}

func list(item check, max int) check {
	return func(v any) bool {
		a, ok := v.([]any)
		if !ok || (max >= 0 && len(a) > max) {
			return false
		}
		for _, x := range a {
			if !item(x) {
				return false
			}
		}
		return true
	}
}

var (
	strList = list(str, -1)

	intentContract = obj(
		field{name: "intent", c: str},
		field{name: "in_scope", c: strList},
		field{name: "out_of_scope", c: strList},
		field{name: "change_type", c: oneOf("feature", "bugfix", "refactor", "docs", "test", "chore", "security", "perf", "mixed"), optional: true, null: true},
		field{name: "confidence", c: oneOf("high", "medium", "low"), optional: true, null: true},
		field{name: "derived_from", c: oneOf("explicit", "inferred"), optional: true, null: true},
		field{name: "sources", optional: true, null: true, c: list(obj(
			field{name: "kind", c: oneOf("title", "body", "ticket", "doc", "commits", "branch", "diff")},
			field{name: "ref", c: str},
			field{name: "status", c: oneOf("used", "truncated", "skipped", "failed")},
			field{name: "detail", c: str, optional: true, null: true},
		), -1)},
	)

	blastContract = obj(
		field{name: "changed_symbols", c: list(obj(field{name: "name", c: str}, field{name: "file", c: str}, field{name: "kind", c: str}), -1)},
		field{name: "downstream", c: list(obj(
			field{name: "symbol", c: str},
			field{name: "callers", c: list(obj(field{name: "name", c: str}, field{name: "file", c: str}, field{name: "line", c: integer}), -1)},
			field{name: "endpoints_affected", c: strList},
			field{name: "crons_affected", c: strList},
		), -1)},
		field{name: "summary", c: str},
		field{name: "degraded", c: boolean, optional: true},
		field{name: "reason", c: oneOf("flag_off", "index_failed", "index_partial", "repo_too_large", "no_data"), optional: true},
	)

	// historyContract: a stored "history" is not part of the brief and is dropped, but a bad one
	// still fails the document, as in the contract.
	historyContract = obj(field{name: "history", c: list(obj(
		field{name: "pr_number", c: integer},
		field{name: "title", c: str},
		field{name: "merged_at", c: str},
		field{name: "author", c: str},
		field{name: "files_overlap", c: strList},
		field{name: "notes", c: str},
	), -1)})

	briefContract = obj(
		field{name: "summary", c: strMax(domain.MaxSummary)},
		field{name: "risks", c: obj(field{name: "risks", c: list(obj(
			field{name: "kind", c: str},
			field{name: "title", c: str},
			field{name: "explanation", c: str},
			field{name: "severity", c: oneOf("high", "medium", "low")},
			field{name: "file_refs", c: strList},
		), -1)})},
		field{name: "review_focus", c: list(obj(
			field{name: "file", c: str},
			field{name: "line", c: integerAtLeast(1)},
			field{name: "reason", c: strMax(domain.MaxFocusReason)},
		), domain.MaxFocus)},
		field{name: "intent", c: intentContract, null: true},
		field{name: "blast", c: blastContract, null: true},
		field{name: "missing_inputs", c: list(obj(
			field{name: "input", c: oneOf("intent", "blast", "specs", "linked_issue", "description")},
			field{name: "reason", c: oneOf("not_derived", "degraded", "no_review_run", "none_attached", "over_budget", "unreadable", "not_linked", "fetch_failed", "empty")},
			field{name: "detail", c: str, optional: true},
		), -1)},
		field{name: "history", c: historyContract, optional: true},
		field{name: "specs_used", c: strList},
		field{name: "head_sha", c: str},
		field{name: "generated_at", c: str},
		field{name: "prompt_version", c: str},
		field{name: "provider", c: str},
		field{name: "model", c: str},
		field{name: "tokens_in", c: integer},
		field{name: "tokens_out", c: integer},
		field{name: "cost_usd", c: number, null: true},
		field{name: "model_requests", c: oneOf1or2},
	)
)

func oneOf1or2(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	f, err := n.Float64()
	return err == nil && (f == 1 || f == 2)
}
