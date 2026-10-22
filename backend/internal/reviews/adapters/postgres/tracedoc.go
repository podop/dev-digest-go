package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// The run trace document stored in run_traces.trace (spec 7.6, the RunTrace contract of
// frontend/src/vendor/shared/contracts/trace.ts). The domain Trace carries no tags; this file
// maps it to and from the document. memory_pulled ([]) and intent (null) are constants of the
// phase. Strings are made storable (U+0000 dropped, invalid UTF-8 replaced) on the way in; the
// way out checks the contract field by field and fails with domain.ErrTraceInvalid, naming the
// offending path and never quoting the document.

// ---- encoding ----

type configDoc struct {
	Agent    string `json:"agent"`
	Version  string `json:"version"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	PR       int    `json:"pr"`
	Source   string `json:"source"`
}

type statsDoc struct {
	DurationMS int      `json:"duration_ms"`
	TokensIn   int      `json:"tokens_in"`
	TokensOut  int      `json:"tokens_out"`
	CostUSD    *float64 `json:"cost_usd"`
	Findings   int      `json:"findings"`
	Grounding  string   `json:"grounding"`
}

// completedAssemblyDoc is the prompt_assembly of a completed run: every key present, null when
// empty.
type completedAssemblyDoc struct {
	System        string  `json:"system"`
	Skills        *string `json:"skills"`
	Memory        *string `json:"memory"`
	Specs         *string `json:"specs"`
	Callers       *string `json:"callers"`
	RepoMap       *string `json:"repo_map"`
	PRDescription *string `json:"pr_description"`
	Intent        *string `json:"intent"`
	User          string  `json:"user"`
}

// endedAssemblyDoc is the prompt_assembly of a run that ended abnormally: the callers, repo_map,
// pr_description and intent keys are absent.
type endedAssemblyDoc struct {
	System string  `json:"system"`
	Skills *string `json:"skills"`
	Memory *string `json:"memory"`
	Specs  *string `json:"specs"`
	User   string  `json:"user"`
}

type toolCallDoc struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
	Meta string `json:"meta"`
	MS   int    `json:"ms"`
}

type logDoc struct {
	T    string `json:"t"`
	Kind string `json:"kind"`
	Msg  string `json:"msg"`
}

type skillUsedDoc struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type originDoc struct {
	Kind      string  `json:"kind"`
	SkillID   *string `json:"skill_id,omitempty"`
	SkillName *string `json:"skill_name,omitempty"`
}

type contextDocDoc struct {
	Path    string    `json:"path"`
	DocType string    `json:"doc_type"`
	Origin  originDoc `json:"origin"`
	Tokens  int       `json:"tokens"`
	Status  string    `json:"status"`
	Source  string    `json:"source"`
	Text    *string   `json:"text,omitempty"`
}

type contextTraceDoc struct {
	BudgetTokens int             `json:"budget_tokens"`
	TokensTotal  int             `json:"tokens_total"`
	Docs         []contextDocDoc `json:"docs"`
}

type traceDoc struct {
	Config         configDoc        `json:"config"`
	Stats          statsDoc         `json:"stats"`
	PromptAssembly any              `json:"prompt_assembly"`
	ToolCalls      []toolCallDoc    `json:"tool_calls"`
	RawOutput      string           `json:"raw_output"`
	MemoryPulled   []struct{}       `json:"memory_pulled"`
	SpecsRead      []string         `json:"specs_read"`
	Log            []logDoc         `json:"log"`
	SkillsUsed     []skillUsedDoc   `json:"skills_used"`
	Intent         *struct{}        `json:"intent"`
	ProjectContext *contextTraceDoc `json:"project_context,omitempty"`
}

// EncodeTrace builds the stored document of a trace. It fails only on a value JSON cannot hold
// (a NaN or infinite cost).
func EncodeTrace(t domain.Trace) ([]byte, error) {
	a := t.PromptAssembly
	doc := traceDoc{
		Config: configDoc{
			Agent: clean(t.Config.Agent), Version: clean(t.Config.Version), Provider: clean(t.Config.Provider),
			Model: clean(t.Config.Model), PR: t.Config.PR, Source: orLocal(t.Config.Source),
		},
		Stats: statsDoc{
			DurationMS: t.Stats.DurationMS, TokensIn: t.Stats.TokensIn, TokensOut: t.Stats.TokensOut,
			CostUSD: t.Stats.CostUSD, Findings: t.Stats.Findings, Grounding: clean(t.Stats.Grounding),
		},
		ToolCalls:    make([]toolCallDoc, len(t.ToolCalls)),
		RawOutput:    clean(t.RawOutput),
		MemoryPulled: []struct{}{},
		SpecsRead:    make([]string, len(t.SpecsRead)),
		Log:          make([]logDoc, len(t.Log)),
		SkillsUsed:   make([]skillUsedDoc, len(t.SkillsUsed)),
	}
	if t.Ended {
		// The agent's own prompt and an empty user message; nothing else of the assembly.
		doc.PromptAssembly = endedAssemblyDoc{System: clean(a.System), User: clean(a.User)}
	} else {
		doc.PromptAssembly = completedAssemblyDoc{
			System: clean(a.System), Skills: cleanPtr(a.Skills), Specs: cleanPtr(a.Specs),
			Callers: cleanPtr(a.Callers), RepoMap: cleanPtr(a.RepoMap), PRDescription: cleanPtr(a.PRDescription),
			User: clean(a.User),
		}
	}
	for i, c := range t.ToolCalls {
		doc.ToolCalls[i] = toolCallDoc{Tool: clean(c.Tool), Args: clean(c.Args), Meta: clean(c.Meta), MS: c.MS}
	}
	for i, p := range t.SpecsRead {
		doc.SpecsRead[i] = clean(p)
	}
	for i, l := range t.Log {
		doc.Log[i] = logDoc{T: clean(l.T), Kind: string(l.Kind), Msg: clean(l.Msg)}
	}
	for i, s := range t.SkillsUsed {
		doc.SkillsUsed[i] = skillUsedDoc{ID: s.ID, Name: clean(s.Name), Version: s.Version}
	}
	if pc := t.ProjectContext; pc != nil {
		doc.ProjectContext = encodeContext(pc)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func orLocal(source string) string {
	if source == "" {
		return domain.SourceLocal
	}
	return source
}

func encodeContext(pc *domain.ContextTrace) *contextTraceDoc {
	out := &contextTraceDoc{BudgetTokens: pc.BudgetTokens, TokensTotal: pc.TokensTotal, Docs: make([]contextDocDoc, len(pc.Docs))}
	for i, d := range pc.Docs {
		doc := contextDocDoc{
			Path: clean(d.Path), DocType: clean(d.DocType), Tokens: d.Tokens, Status: string(d.Status),
			Source: d.Source, Text: cleanPtr(d.Text), Origin: originDoc{Kind: d.Origin.Kind},
		}
		if doc.Source == "" {
			doc.Source = "repo"
		}
		if d.Origin.Kind == "skill" {
			id, name := d.Origin.SkillID, clean(d.Origin.SkillName)
			doc.Origin.SkillID, doc.Origin.SkillName = &id, &name
		}
		out.Docs[i] = doc
	}
	return out
}

// ---- decoding ----

// jint is a JSON number that must be an integer value (1 and 1.0 both are, as in JavaScript).
// Decoding never fails: a value that is not such a number leaves ok false, so the caller reports
// the path of the field (an error returned from here would not carry it).
type jint struct {
	v  int
	ok bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (n *jint) UnmarshalJSON(b []byte) error {
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		*n = jint{}
		return nil
	}
	*n = jint{v: int(f), ok: true}
	return nil
}

// Pointer fields tell absent or null (nil) from a value; a required field must be non-nil.

type configIn struct {
	Agent    *string `json:"agent"`
	Version  *string `json:"version"`
	Provider *string `json:"provider"`
	Model    *string `json:"model"`
	PR       *jint   `json:"pr"`
	Source   *string `json:"source"`
}

type statsIn struct {
	DurationMS *jint    `json:"duration_ms"`
	TokensIn   *jint    `json:"tokens_in"`
	TokensOut  *jint    `json:"tokens_out"`
	CostUSD    *float64 `json:"cost_usd"`
	Findings   *jint    `json:"findings"`
	Grounding  *string  `json:"grounding"`
}

type toolCallIn struct {
	Tool *string `json:"tool"`
	Args *string `json:"args"`
	Meta *string `json:"meta"`
	MS   *jint   `json:"ms"`
}

type logIn struct {
	T    *string `json:"t"`
	Kind *string `json:"kind"`
	Msg  *string `json:"msg"`
}

type skillUsedIn struct {
	ID      *string `json:"id"`
	Name    *string `json:"name"`
	Version *jint   `json:"version"`
}

type originIn struct {
	Kind      *string `json:"kind"`
	SkillID   *string `json:"skill_id"`
	SkillName *string `json:"skill_name"`
}

type contextDocIn struct {
	Path    *string   `json:"path"`
	DocType *string   `json:"doc_type"`
	Origin  *originIn `json:"origin"`
	Tokens  *jint     `json:"tokens"`
	Status  *string   `json:"status"`
	Source  *string   `json:"source"`
	Text    *string   `json:"text"`
}

type contextTraceIn struct {
	BudgetTokens *jint            `json:"budget_tokens"`
	TokensTotal  *jint            `json:"tokens_total"`
	Docs         *[]*contextDocIn `json:"docs"`
}

type memoryIn struct {
	PR   *jint   `json:"pr"`
	Text *string `json:"text"`
}

type intentIn struct {
	Status *string `json:"status"`
}

type traceIn struct {
	Config         *configIn                  `json:"config"`
	Stats          *statsIn                   `json:"stats"`
	PromptAssembly map[string]json.RawMessage `json:"prompt_assembly"`
	ToolCalls      *[]*toolCallIn             `json:"tool_calls"`
	RawOutput      *string                    `json:"raw_output"`
	MemoryPulled   *[]*memoryIn               `json:"memory_pulled"`
	SpecsRead      *[]*string                 `json:"specs_read"`
	Log            *[]*logIn                  `json:"log"`
	SkillsUsed     *[]*skillUsedIn            `json:"skills_used"`
	Intent         *intentIn                  `json:"intent"`
	ProjectContext *contextTraceIn            `json:"project_context"`
}

// invalid is the error of a document that violates the contract at path.
func invalid(path string) error {
	return fmt.Errorf("%w: %s is missing or has the wrong type", domain.ErrTraceInvalid, path)
}

// pathCheck collects the first contract violation while a document is mapped.
type pathCheck struct{ err error }

func (p *pathCheck) fail(path string) {
	if p.err == nil {
		p.err = invalid(path)
	}
}

func (p *pathCheck) str(v *string, path string) string {
	if v == nil {
		p.fail(path)
		return ""
	}
	return *v
}

func (p *pathCheck) integer(v *jint, path string) int {
	if v == nil || !v.ok {
		p.fail(path)
		return 0
	}
	return v.v
}

// optInteger is integer for a key that may be absent or null.
func (p *pathCheck) optInteger(v *jint, path string) int {
	if v == nil {
		return 0
	}
	return p.integer(v, path)
}

func optString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// DecodeTrace reads a stored document into a Trace, enforcing the RunTrace contract: the required
// keys with their types, the closed enums, config.source defaulting to "local". Unknown keys are
// ignored. A violation is domain.ErrTraceInvalid.
//
// The result has Ended set for a document shaped like an ended run's (no callers, repo_map,
// pr_description or intent keys, nothing in skills, specs or user). SkillsUsed and ProjectContext
// are nil when the document has none.
func DecodeTrace(raw []byte) (domain.Trace, error) {
	var in traceIn
	if err := json.Unmarshal(raw, &in); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			path := typeErr.Field
			if path == "" {
				path = "document"
			}
			return domain.Trace{}, invalid(path)
		}
		return domain.Trace{}, fmt.Errorf("%w: document is not valid JSON", domain.ErrTraceInvalid)
	}
	p := &pathCheck{}
	t := domain.Trace{}

	if in.Config == nil {
		p.fail("config")
	} else {
		c := in.Config
		source := optString(c.Source)
		switch source {
		case "":
			source = domain.SourceLocal
		case "local", "ci":
		default:
			p.fail("config.source")
		}
		t.Config = domain.TraceConfig{
			Agent: p.str(c.Agent, "config.agent"), Version: optString(c.Version), Provider: optString(c.Provider),
			Model: p.str(c.Model, "config.model"), PR: p.optInteger(c.PR, "config.pr"), Source: source,
		}
	}
	if in.Stats == nil {
		p.fail("stats")
	} else {
		st := in.Stats
		t.Stats = domain.TraceStats{
			DurationMS: p.integer(st.DurationMS, "stats.duration_ms"), TokensIn: p.integer(st.TokensIn, "stats.tokens_in"),
			TokensOut: p.integer(st.TokensOut, "stats.tokens_out"), CostUSD: st.CostUSD,
			Findings: p.integer(st.Findings, "stats.findings"), Grounding: p.str(st.Grounding, "stats.grounding"),
		}
	}
	t.PromptAssembly, t.Ended = decodeAssembly(in.PromptAssembly, p)

	if in.ToolCalls == nil {
		p.fail("tool_calls")
	} else {
		t.ToolCalls = make([]domain.ToolCall, len(*in.ToolCalls))
		for i, c := range *in.ToolCalls {
			if c == nil {
				p.fail(fmt.Sprintf("tool_calls[%d]", i))
				continue
			}
			t.ToolCalls[i] = domain.ToolCall{
				Tool: p.str(c.Tool, "tool_calls.tool"), Args: p.str(c.Args, "tool_calls.args"),
				Meta: optString(c.Meta), MS: p.integer(c.MS, "tool_calls.ms"),
			}
		}
	}
	t.RawOutput = p.str(in.RawOutput, "raw_output")
	if in.MemoryPulled == nil {
		p.fail("memory_pulled")
	} else {
		for i, m := range *in.MemoryPulled {
			if m == nil || m.Text == nil {
				p.fail(fmt.Sprintf("memory_pulled[%d].text", i))
				continue
			}
			p.optInteger(m.PR, "memory_pulled.pr")
		}
	}
	if in.SpecsRead == nil {
		p.fail("specs_read")
	} else {
		t.SpecsRead = make([]string, len(*in.SpecsRead))
		for i, s := range *in.SpecsRead {
			t.SpecsRead[i] = p.str(s, fmt.Sprintf("specs_read[%d]", i))
		}
	}
	if in.Log == nil {
		p.fail("log")
	} else {
		t.Log = make([]domain.LogLine, len(*in.Log))
		for i, l := range *in.Log {
			if l == nil {
				p.fail(fmt.Sprintf("log[%d]", i))
				continue
			}
			kind := reviewer.EventKind(p.str(l.Kind, "log.kind"))
			switch kind {
			case reviewer.EventInfo, reviewer.EventTool, reviewer.EventResult, reviewer.EventError:
			default:
				p.fail("log.kind")
			}
			t.Log[i] = domain.LogLine{T: p.str(l.T, "log.t"), Kind: kind, Msg: p.str(l.Msg, "log.msg")}
		}
	}
	if in.SkillsUsed != nil {
		t.SkillsUsed = make([]domain.SkillUsed, len(*in.SkillsUsed))
		for i, s := range *in.SkillsUsed {
			if s == nil {
				p.fail(fmt.Sprintf("skills_used[%d]", i))
				continue
			}
			t.SkillsUsed[i] = domain.SkillUsed{
				ID: p.str(s.ID, "skills_used.id"), Name: p.str(s.Name, "skills_used.name"),
				Version: p.integer(s.Version, "skills_used.version"),
			}
		}
	}
	if in.Intent != nil {
		if in.Intent.Status == nil || (*in.Intent.Status != "used" && *in.Intent.Status != "unavailable") {
			p.fail("intent.status")
		}
	}
	if in.ProjectContext != nil {
		t.ProjectContext = decodeContext(in.ProjectContext, p)
	}
	if p.err != nil {
		return domain.Trace{}, p.err
	}
	return t, nil
}

// decodeAssembly maps prompt_assembly and reports whether it has the shape of an ended run's.
func decodeAssembly(m map[string]json.RawMessage, p *pathCheck) (reviewer.Assembly, bool) {
	if m == nil {
		p.fail("prompt_assembly")
		return reviewer.Assembly{}, false
	}
	var a reviewer.Assembly
	str := func(key string, required bool) *string {
		raw, ok := m[key]
		if !ok || string(raw) == "null" {
			if required {
				p.fail("prompt_assembly." + key)
			}
			return nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			p.fail("prompt_assembly." + key)
			return nil
		}
		return &s
	}
	if s := str("system", true); s != nil {
		a.System = *s
	}
	if s := str("user", true); s != nil {
		a.User = *s
	}
	a.Skills, a.Specs = str("skills", false), str("specs", false)
	a.Callers, a.RepoMap, a.PRDescription = str("callers", false), str("repo_map", false), str("pr_description", false)
	str("memory", false)
	if raw, ok := m["intent"]; ok && string(raw) != "null" {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			p.fail("prompt_assembly.intent")
		}
	}
	_, hasCallers := m["callers"]
	_, hasRepoMap := m["repo_map"]
	_, hasPRDescription := m["pr_description"]
	_, hasIntent := m["intent"]
	ended := !hasCallers && !hasRepoMap && !hasPRDescription && !hasIntent &&
		a.Skills == nil && a.Specs == nil && a.User == ""
	return a, ended
}

func decodeContext(in *contextTraceIn, p *pathCheck) *domain.ContextTrace {
	out := &domain.ContextTrace{
		BudgetTokens: p.integer(in.BudgetTokens, "project_context.budget_tokens"),
		TokensTotal:  p.integer(in.TokensTotal, "project_context.tokens_total"),
	}
	if in.Docs == nil {
		p.fail("project_context.docs")
		return out
	}
	out.Docs = make([]domain.ContextDoc, len(*in.Docs))
	for i, d := range *in.Docs {
		if d == nil {
			p.fail(fmt.Sprintf("project_context.docs[%d]", i))
			continue
		}
		doc := domain.ContextDoc{
			Path: p.str(d.Path, "project_context.docs.path"), DocType: p.str(d.DocType, "project_context.docs.doc_type"),
			Tokens: p.integer(d.Tokens, "project_context.docs.tokens"), Text: d.Text, Source: optString(d.Source),
		}
		switch domain.ContextStatus(p.str(d.Status, "project_context.docs.status")) {
		case domain.ContextIncluded, domain.ContextMissing, domain.ContextTooLarge, domain.ContextOverBudget, domain.ContextUnreadable:
			doc.Status = domain.ContextStatus(*d.Status)
		default:
			p.fail("project_context.docs.status")
		}
		switch doc.Source {
		case "":
			doc.Source = "repo"
		case "repo", "store":
		default:
			p.fail("project_context.docs.source")
		}
		if d.Origin == nil || d.Origin.Kind == nil {
			p.fail("project_context.docs.origin")
		} else {
			switch *d.Origin.Kind {
			case "agent":
				doc.Origin = domain.ContextOrigin{Kind: "agent"}
			case "skill":
				doc.Origin = domain.ContextOrigin{
					Kind:    "skill",
					SkillID: p.str(d.Origin.SkillID, "project_context.docs.origin.skill_id"), SkillName: p.str(d.Origin.SkillName, "project_context.docs.origin.skill_name"),
				}
			default:
				p.fail("project_context.docs.origin.kind")
			}
		}
		out.Docs[i] = doc
	}
	return out
}
