package domain_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/plugins/domain"
)

// validBundle is fullJSON parsed; every test mutates a copy.
func validBundle(t testing.TB) domain.Bundle {
	t.Helper()
	return parseOK(t, fullJSON).Bundle
}

func TestValidate_AcceptsTheFullBundle(t *testing.T) {
	if err := domain.Validate(validBundle(t)); err != nil {
		t.Fatalf("Validate(full bundle) = %v, want nil", err)
	}
}

// emoji is two UTF-16 code units.
const emoji = "😀"

func TestValidate_Rules(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	tests := []struct {
		name string
		edit func(b *domain.Bundle)
		want string // "" = valid
	}{
		// manifest
		{"wrong format", func(b *domain.Bundle) { b.Manifest.Format = "devdigest-plugin/v2" }, `manifest.format must be "devdigest-plugin/v1" (found "devdigest-plugin/v2")`},
		{"empty format", func(b *domain.Bundle) { b.Manifest.Format = "" }, "manifest.format must be"},
		{"kind plugin", func(b *domain.Bundle) { b.Manifest.Kind = "plugin" }, ""},
		{"kind workspace", func(b *domain.Bundle) { b.Manifest.Kind = "workspace" }, ""},
		{"kind other", func(b *domain.Bundle) { b.Manifest.Kind = "team" }, "manifest.kind must be"},
		{"name 80 characters", func(b *domain.Bundle) { b.Manifest.Name = long(80) }, ""},
		{"name 81 characters", func(b *domain.Bundle) { b.Manifest.Name = long(81) }, "manifest.name must be 1 to 80 characters"},
		{"name 80 after trimming", func(b *domain.Bundle) { b.Manifest.Name = "  " + long(80) + " \t" }, ""},
		{"name empty after trimming", func(b *domain.Bundle) { b.Manifest.Name = "   " }, "manifest.name must be 1 to 80 characters"},
		{"name empty", func(b *domain.Bundle) { b.Manifest.Name = "" }, "manifest.name must be 1 to 80 characters"},
		{"name 80 code points of four bytes", func(b *domain.Bundle) { b.Manifest.Name = strings.Repeat(emoji, 80) }, ""},
		{"name 81 code points", func(b *domain.Bundle) { b.Manifest.Name = strings.Repeat(emoji, 81) }, "manifest.name must be 1 to 80"},
		{"name with a newline", func(b *domain.Bundle) { b.Manifest.Name = "a\nb" }, "manifest.name must be 1 to 80 characters without control characters"},
		{"name with DEL", func(b *domain.Bundle) { b.Manifest.Name = "a\x7fb" }, "manifest.name"},
		{"name with NUL", func(b *domain.Bundle) { b.Manifest.Name = "a\x00b" }, "manifest.name must not contain U+0000"},
		{"name with spaces and a dot", func(b *domain.Bundle) { b.Manifest.Name = "acme · payments v1.0" }, ""},
		{"version 64", func(b *domain.Bundle) { b.Manifest.Version = long(64) }, ""},
		{"version 65", func(b *domain.Bundle) { b.Manifest.Version = long(65) }, "manifest.version must be at most 64 characters"},
		{"version empty", func(b *domain.Bundle) { b.Manifest.Version = "" }, ""},
		{"version with NUL", func(b *domain.Bundle) { b.Manifest.Version = "1\x00" }, "manifest.version must not contain U+0000"},
		{"description with NUL", func(b *domain.Bundle) { b.Manifest.Description = ptr("a\x00") }, "manifest.description must not contain U+0000"},

		// agents
		{"agent name 200", func(b *domain.Bundle) { b.Agents[1].Name = long(200) }, ""},
		{"agent name 201", func(b *domain.Bundle) { b.Agents[0].Name = long(201) }, "agents[0].name must be 1 to 200 characters"},
		{"agent name empty", func(b *domain.Bundle) { b.Agents[0].Name = "" }, "agents[0].name must be 1 to 200 characters"},
		{"agent name 100 emoji", func(b *domain.Bundle) { b.Agents[1].Name = strings.Repeat(emoji, 100) }, ""},
		{"agent name 101 emoji", func(b *domain.Bundle) { b.Agents[0].Name = strings.Repeat(emoji, 101) }, "agents[0].name must be 1 to 200"},
		{"agent names repeat", func(b *domain.Bundle) { b.Agents[1].Name = b.Agents[0].Name }, `agents[1].name "Reviewer" is used by another agent`},
		{"agent model 200", func(b *domain.Bundle) { b.Agents[0].Model = long(200) }, ""},
		{"agent model 201", func(b *domain.Bundle) { b.Agents[0].Model = long(201) }, "agents[0].model must be 1 to 200"},
		{"agent model empty", func(b *domain.Bundle) { b.Agents[0].Model = "" }, "agents[0].model must be 1 to 200"},
		{"prompt 100000", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = long(100_000) }, ""},
		{"prompt 100001", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = long(100_001) }, "agents[0].system_prompt must be 1 to 100000"},
		{"prompt 50000 emoji is 100000 units", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = strings.Repeat(emoji, 50_000) }, ""},
		{"prompt 50001 emoji", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = strings.Repeat(emoji, 50_001) }, "agents[0].system_prompt"},
		{"prompt empty", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = "" }, "agents[0].system_prompt must be 1 to 100000"},
		{"prompt of zero-width characters only", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = "\u200B\u202E\uFEFF" }, "agents[0].system_prompt has no visible text after hidden text is removed (HTML comments, zero-width or bidi characters)"},
		{"prompt of a comment and blanks", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = " <!-- hi -->\n" }, "agents[0].system_prompt has no visible text after hidden text is removed"},
		{"prompt with a comment and text", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = "<!-- hi -->p" }, ""},
		{"prompt of blanks only keeps its old rule", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = "  " }, ""},
		{"prompt with NUL", func(b *domain.Bundle) { b.Agents[0].SystemPrompt = "a\x00" }, "agents[0].system_prompt must not contain U+0000"},
		{"agent description with NUL", func(b *domain.Bundle) { b.Agents[0].Description = "\x00" }, "agents[0].description must not contain U+0000"},
		{"provider unknown", func(b *domain.Bundle) { b.Agents[0].Provider = "gemini" }, `agents[0].provider must be one of openai, anthropic, openrouter, or custom:<slug> with 1 to 32 lowercase letters, digits or hyphens (found "gemini")`},
		{"provider custom", func(b *domain.Bundle) { b.Agents[0].Provider = "custom:synapse" }, ""},
		{"provider custom digits and hyphens", func(b *domain.Bundle) { b.Agents[0].Provider = "custom:0-a-9" }, ""},
		{"provider custom 32 characters", func(b *domain.Bundle) { b.Agents[0].Provider = "custom:" + long(32) }, ""},
		{"provider custom 33 characters", func(b *domain.Bundle) { b.Agents[0].Provider = "custom:" + long(33) }, "agents[0].provider must be one of"},
		{"provider custom empty slug", func(b *domain.Bundle) { b.Agents[0].Provider = "custom:" }, "agents[0].provider must be one of"},
		{"provider custom uppercase", func(b *domain.Bundle) { b.Agents[0].Provider = "custom:Bad!" }, `(found "custom:Bad!")`},
		{"provider custom underscore", func(b *domain.Bundle) { b.Agents[0].Provider = "custom:a_b" }, "agents[0].provider must be one of"},
		{"provider custom trailing newline", func(b *domain.Bundle) { b.Agents[0].Provider = "custom:ok\n" }, "agents[0].provider must be one of"},
		{"provider custom prefix in upper case", func(b *domain.Bundle) { b.Agents[0].Provider = "Custom:ok" }, "agents[0].provider must be one of"},
		{"provider custom with a space", func(b *domain.Bundle) { b.Agents[0].Provider = " custom:ok" }, "agents[0].provider must be one of"},
		{"provider anthropic", func(b *domain.Bundle) { b.Agents[0].Provider = "anthropic" }, ""},
		{"strategy unknown", func(b *domain.Bundle) { b.Agents[0].Strategy = "multi" }, "agents[0].strategy must be one of"},
		{"strategy empty", func(b *domain.Bundle) { b.Agents[0].Strategy = "" }, "agents[0].strategy must be one of"},
		{"ci_fail_on unknown", func(b *domain.Bundle) { b.Agents[0].CIFailOn = "major" }, "agents[0].ci_fail_on must be one of"},
		{"output schema scalar is allowed (agent create takes any JSON)", func(b *domain.Bundle) { b.Agents[0].OutputSchema = raw(`"x"`) }, ""},
		{"output schema invalid JSON", func(b *domain.Bundle) { b.Agents[0].OutputSchema = raw(`{`) }, "agents[0].output_schema is not valid JSON"},
		{"output schema with a lone surrogate", func(b *domain.Bundle) { b.Agents[0].OutputSchema = raw(`{"a":"\ud800"}`) }, "agents[0].output_schema must not contain an unpaired surrogate escape"},
		{"output schema with a surrogate pair", func(b *domain.Bundle) { b.Agents[0].OutputSchema = raw(`{"a":"\ud83d\ude00"}`) }, ""},
		{"output schema with NUL", func(b *domain.Bundle) { b.Agents[0].OutputSchema = raw(`{"a":"\u0000"}`) }, "agents[0].output_schema must not contain U+0000"},
		{"agent links an unknown skill", func(b *domain.Bundle) { b.Agents[0].Skills = []string{"style", "ghost"} }, `agents[0].skills[1] names the skill "ghost", which is not in the bundle`},
		{"agent links a skill twice", func(b *domain.Bundle) { b.Agents[0].Skills = []string{"style", "style"} }, `agents[0].skills lists "style" twice`},

		// skills
		{"skill name 64", func(b *domain.Bundle) { b.Skills[1].Name = long(64); b.Agents[0].Skills = []string{"style", long(64)} }, ""},
		{"skill name 65", func(b *domain.Bundle) { b.Skills[1].Name = long(65) }, "skills[1].name must be a kebab-case slug of 1 to 64"},
		{"skill name upper case", func(b *domain.Bundle) { b.Skills[1].Name = "Sec" }, "skills[1].name must be a kebab-case slug"},
		{"skill name with spaces", func(b *domain.Bundle) { b.Skills[1].Name = "a b" }, "skills[1].name must be a kebab-case slug"},
		{"skill name double dash", func(b *domain.Bundle) { b.Skills[1].Name = "a--b" }, "skills[1].name must be a kebab-case slug"},
		{"skill name empty", func(b *domain.Bundle) { b.Skills[1].Name = "" }, "skills[1].name must be a kebab-case slug"},
		{"skill names repeat", func(b *domain.Bundle) { b.Skills[1].Name = "style" }, `skills[1].name "style" is used by another skill`},
		{"skill description 300", func(b *domain.Bundle) { b.Skills[0].Description = long(300) }, ""},
		{"skill description 301", func(b *domain.Bundle) { b.Skills[0].Description = long(301) }, "skills[0].description must be at most 300 characters"},
		{"skill description 150 emoji", func(b *domain.Bundle) { b.Skills[0].Description = strings.Repeat(emoji, 150) }, ""},
		{"skill description 151 emoji", func(b *domain.Bundle) { b.Skills[0].Description = strings.Repeat(emoji, 151) }, "skills[0].description must be at most 300"},
		{"skill body 20000", func(b *domain.Bundle) { b.Skills[0].Body = long(20_000) }, ""},
		{"skill body 20001", func(b *domain.Bundle) { b.Skills[0].Body = long(20_001) }, "skills[0].body must be 1 to 20000"},
		{"skill body 10000 emoji", func(b *domain.Bundle) { b.Skills[0].Body = strings.Repeat(emoji, 10_000) }, ""},
		{"skill body 10001 emoji", func(b *domain.Bundle) { b.Skills[0].Body = strings.Repeat(emoji, 10_001) }, "skills[0].body"},
		{"skill body empty", func(b *domain.Bundle) { b.Skills[0].Body = "" }, "skills[0].body must be 1 to 20000"},
		{"skill body of a comment only", func(b *domain.Bundle) { b.Skills[0].Body = "<!-- only a comment -->" }, "skills[0].body has no visible text after hidden text is removed (HTML comments, zero-width or bidi characters)"},
		{"skill body of an unclosed comment", func(b *domain.Bundle) { b.Skills[0].Body = "<!-- never closed" }, "skills[0].body has no visible text"},
		{"skill body with a comment and text", func(b *domain.Bundle) { b.Skills[0].Body = "a<!-- x -->" }, ""},
		{"skill body over the limit is a length issue", func(b *domain.Bundle) { b.Skills[0].Body = strings.Repeat("\u200B", 20_001) }, "skills[0].body must be 1 to 20000"},
		{"skill body with NUL", func(b *domain.Bundle) { b.Skills[0].Body = "a\x00b" }, "skills[0].body must not contain U+0000"},
		{"skill type unknown", func(b *domain.Bundle) { b.Skills[0].Type = "tool" }, "skills[0].type must be one of rubric, convention, security, custom"},
		{"skill source unknown", func(b *domain.Bundle) { b.Skills[0].Source = "plugin" }, "skills[0].source must be one of"},
		{"skill source extracted", func(b *domain.Bundle) { b.Skills[0].Source = "extracted" }, ""},
		{"skill evidence file with NUL", func(b *domain.Bundle) { b.Skills[0].EvidenceFiles = []string{"a\x00"} }, "skills[0].evidence_files[0] must not contain U+0000"},

		// eval cases
		{"case name 120", func(b *domain.Bundle) { b.EvalCases[0].Name = long(120) }, ""},
		{"case name 121", func(b *domain.Bundle) { b.EvalCases[0].Name = long(121) }, "eval_cases[0].name must be 1 to 120"},
		{"case name empty", func(b *domain.Bundle) { b.EvalCases[0].Name = "" }, "eval_cases[0].name must be 1 to 120"},
		{"case owner kind unknown", func(b *domain.Bundle) { b.EvalCases[0].OwnerKind = "repo" }, "eval_cases[0].owner_kind must be one of skill, agent"},
		{"case agent owner missing", func(b *domain.Bundle) { b.EvalCases[0].OwnerRef = "Ghost" }, `eval_cases[0].owner_ref names the agent "Ghost", which is not in the bundle`},
		{"case skill owner missing", func(b *domain.Bundle) { b.EvalCases[1].OwnerRef = "ghost" }, `eval_cases[1].owner_ref names the skill "ghost", which is not in the bundle`},
		{"case names a skill as its agent", func(b *domain.Bundle) { b.EvalCases[0].OwnerRef = "style" }, "eval_cases[0].owner_ref names the agent"},
		{"case diff 200000", func(b *domain.Bundle) { b.EvalCases[0].InputDiff = ptr(long(200_000)) }, ""},
		{"case diff 200001", func(b *domain.Bundle) { b.EvalCases[0].InputDiff = ptr(long(200_001)) }, "eval_cases[0].input_diff must be at most 200000"},
		{"case diff with NUL", func(b *domain.Bundle) { b.EvalCases[0].InputDiff = ptr("\x00") }, "eval_cases[0].input_diff must not contain U+0000"},
		{"case notes with NUL", func(b *domain.Bundle) { b.EvalCases[0].Notes = ptr("\x00") }, "eval_cases[0].notes must not contain U+0000"},
		{"case JSON fields null", func(b *domain.Bundle) { b.EvalCases[0].InputFiles, b.EvalCases[0].ExpectedOutput = nil, nil }, ""},
		{"case JSON field object", func(b *domain.Bundle) { b.EvalCases[0].InputMeta = raw(` {"a":1}`) }, ""},
		{"case JSON field array", func(b *domain.Bundle) { b.EvalCases[0].InputFiles = raw(`[]`) }, ""},
		{"case JSON field string", func(b *domain.Bundle) { b.EvalCases[0].ExpectedOutput = raw(`"x"`) }, "eval_cases[0].expected_output must be null, an object or an array"},
		{"case JSON field number", func(b *domain.Bundle) { b.EvalCases[0].InputMeta = raw(`5`) }, "eval_cases[0].input_meta must be null, an object or an array"},
		{"case JSON field bool", func(b *domain.Bundle) { b.EvalCases[0].InputFiles = raw(`true`) }, "eval_cases[0].input_files must be null"},
		{"case JSON field invalid", func(b *domain.Bundle) { b.EvalCases[0].InputFiles = raw(`[`) }, "eval_cases[0].input_files is not valid JSON"},
		{"case JSON field with NUL escape", func(b *domain.Bundle) { b.EvalCases[0].ExpectedOutput = raw(`{"a":"\u0000"}`) }, "eval_cases[0].expected_output must not contain U+0000"},
		{"case JSON field with escaped backslash", func(b *domain.Bundle) { b.EvalCases[0].ExpectedOutput = raw(`{"a":"\\u0000"}`) }, ""},
		{"case JSON key with NUL escape", func(b *domain.Bundle) { b.EvalCases[0].InputMeta = raw(`{"\u0000":1}`) }, "eval_cases[0].input_meta must not contain U+0000"},

		// learnings
		{"learning content 20000", func(b *domain.Bundle) { b.Learnings[0].Content = long(20_000) }, ""},
		{"learning content 20001", func(b *domain.Bundle) { b.Learnings[0].Content = long(20_001) }, "learnings[0].content must be 1 to 20000"},
		{"learning content empty", func(b *domain.Bundle) { b.Learnings[0].Content = "" }, "learnings[0].content must be 1 to 20000"},
		{"learning content with NUL", func(b *domain.Bundle) { b.Learnings[0].Content = "a\x00" }, "learnings[0].content must not contain U+0000"},
		{"learning scope unknown", func(b *domain.Bundle) { b.Learnings[0].Scope = "org" }, "learnings[0].scope must be one of repo, global, team"},
		{"learning kind unknown", func(b *domain.Bundle) { b.Learnings[0].Kind = "rule" }, "learnings[0].kind must be one of decision, convention, preference, fact, learning"},
		{"confidence null", func(b *domain.Bundle) { b.Learnings[0].Confidence = nil }, ""},
		{"confidence 0", func(b *domain.Bundle) { b.Learnings[0].Confidence = ptr(0.0) }, ""},
		{"confidence 1", func(b *domain.Bundle) { b.Learnings[0].Confidence = ptr(1.0) }, ""},
		{"confidence above 1", func(b *domain.Bundle) { b.Learnings[0].Confidence = ptr(1.0000001) }, "learnings[0].confidence must be null or between 0 and 1"},
		{"confidence below 0", func(b *domain.Bundle) { b.Learnings[0].Confidence = ptr(-0.0000001) }, "learnings[0].confidence must be null or between 0 and 1"},
		{"learning repo is not checked", func(b *domain.Bundle) { b.Learnings[0].Repo = ptr("not/checked") }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := validBundle(t)
			tc.edit(&b)
			err := domain.Validate(b)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			wantInvalid(t, err, tc.want)
		})
	}
}

func TestValidate_CountBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		fill  func(b *domain.Bundle, n int)
		limit int
		msg   string
	}{
		{"agents", func(b *domain.Bundle, n int) {
			for i := range n {
				b.Agents = append(b.Agents, domain.BundleAgent{Name: fmt.Sprintf("a%d", i), Provider: "openai", Model: "m", SystemPrompt: "p", Strategy: "auto", CIFailOn: "any", Skills: []string{}})
			}
		}, domain.MaxAgents, "agents has 101 entries; the limit is 100"},
		{"skills", func(b *domain.Bundle, n int) {
			for i := range n {
				b.Skills = append(b.Skills, domain.BundleSkill{Name: fmt.Sprintf("s%d", i), Type: "custom", Source: "manual", Body: "b"})
			}
		}, domain.MaxSkills, "skills has 201 entries; the limit is 200"},
		{"learnings", func(b *domain.Bundle, n int) {
			for range n {
				b.Learnings = append(b.Learnings, domain.BundleLearning{Scope: "global", Kind: "fact", Content: "c"})
			}
		}, domain.MaxLearnings, "learnings has 5001 entries; the limit is 5000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := domain.Bundle{Manifest: domain.Manifest{Name: "p", Format: domain.BundleFormat}}
			tc.fill(&base, tc.limit)
			if err := domain.Validate(base); err != nil {
				t.Fatalf("Validate(%d %s) = %v, want nil", tc.limit, tc.name, err)
			}
			over := domain.Bundle{Manifest: domain.Manifest{Name: "p", Format: domain.BundleFormat}}
			tc.fill(&over, tc.limit+1)
			wantInvalid(t, domain.Validate(over), tc.msg)
		})
	}
}

func TestValidate_EvalCaseCounts(t *testing.T) {
	build := func(total, perOwnerSkills int) domain.Bundle {
		b := domain.Bundle{Manifest: domain.Manifest{Name: "p", Format: domain.BundleFormat}}
		for i := range perOwnerSkills {
			b.Skills = append(b.Skills, domain.BundleSkill{Name: fmt.Sprintf("s%d", i), Type: "custom", Source: "manual", Body: "b"})
		}
		for i := range total {
			b.EvalCases = append(b.EvalCases, domain.BundleCase{Name: "c", OwnerKind: "skill", OwnerRef: fmt.Sprintf("s%d", i%perOwnerSkills)})
		}
		return b
	}
	tests := []struct {
		name   string
		total  int
		owners int
		want   string
	}{
		{"1000 cases over 5 skills, 200 each", 1000, 5, ""},
		{"1001 cases over 6 skills", 1001, 6, "eval_cases has 1001 entries; the limit is 1000"},
		{"201 cases for one skill", 201, 1, "has more than 200 eval cases"},
		{"200 cases for one skill", 200, 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.Validate(build(tc.total, tc.owners))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			wantInvalid(t, err, tc.want)
		})
	}
}

func TestValidate_IssuesAreOrderedAndCapped(t *testing.T) {
	b := validBundle(t)
	b.Manifest.Format = "nope"
	b.Manifest.Name = ""
	for i := range 30 {
		b.Learnings = append(b.Learnings, domain.BundleLearning{Scope: "x", Kind: "fact", Content: fmt.Sprintf("c%d", i)})
	}
	err := domain.Validate(b)
	var ie *domain.InvalidError
	if !errors.As(err, &ie) {
		t.Fatalf("Validate() = %v, want *InvalidError", err)
	}
	if got := len(ie.Issues); got != domain.MaxIssues {
		t.Errorf("len(Issues) = %d, want %d", got, domain.MaxIssues)
	}
	if !strings.HasPrefix(ie.Message(), "manifest.format must be") {
		t.Errorf("Message() = %q, want the first broken rule (manifest.format)", ie.Message())
	}
	if ie.Error() != ie.Issues[0] {
		t.Errorf("Error() = %q, want Issues[0] = %q", ie.Error(), ie.Issues[0])
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Error("errors.Is(err, ErrInvalid) = false")
	}
}

func TestValidate_NoNULAnywhereInTheJSONPath(t *testing.T) {
	// The same text arrives as JSON escapes; decoding must surface them as U+0000 for Validate.
	for _, field := range []string{
		`"name":"a\u0000b"`,
	} {
		text := `{"manifest":{"name":"p","format":"devdigest-plugin/v1"},"skills":[{"name":"s","body":"b","description":"d\u0000"}],"agents":[{` + field + `,"provider":"openai","model":"m","system_prompt":"p"}]}`
		up := parseOK(t, text)
		err := domain.Validate(up.Bundle)
		wantInvalid(t, err, "must not contain U+0000")
		var ie *domain.InvalidError
		_ = errors.As(err, &ie)
		if len(ie.Issues) != 2 {
			t.Errorf("issues = %v, want both the skill description and the agent name", ie.Issues)
		}
	}
}

// TestPreviewAndInstallRefuseAnUnpairedSurrogate is the L1 regression: json.Valid accepts "\ud800",
// Postgres jsonb does not, so the gate both the dry run and the install share (ParseUpload, then
// Validate) must answer invalid_plugin instead of letting the install fail with a 500.
func TestPreviewAndInstallRefuseAnUnpairedSurrogate(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
		want  string
	}{
		{"agent output_schema", "agents.0.output_schema", `{"x":"\ud800"}`, "agents[0].output_schema must not contain an unpaired surrogate"},
		{"case input_files", "eval_cases.0.input_files", `[{"p":"\udfff"}]`, "eval_cases[0].input_files must not contain an unpaired surrogate"},
		{"case input_meta", "eval_cases.0.input_meta", `{"k":"a\ud800b"}`, "eval_cases[0].input_meta must not contain an unpaired surrogate"},
		{"case expected_output", "eval_cases.0.expected_output", `{"\ud800":1}`, "eval_cases[0].expected_output must not contain an unpaired surrogate"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]json.RawMessage
			if err := json.Unmarshal([]byte(fullJSON), &doc); err != nil {
				t.Fatalf("decode fullJSON: %v", err)
			}
			text := setJSONField(t, doc, tc.field, tc.value)
			up, err := domain.ParseUpload("application/json", []byte(text))
			if err != nil {
				t.Fatalf("ParseUpload() = %v, want the bundle to decode (the problem is for Validate)", err)
			}
			wantInvalid(t, domain.Validate(up.Bundle), tc.want)
		})
	}
}

// setJSONField returns doc with the raw value at path (section.index.field) replaced; the value
// text is spliced in unchanged, which a decode and re-encode of an escape would not keep.
func setJSONField(t testing.TB, doc map[string]json.RawMessage, path, value string) string {
	t.Helper()
	parts := strings.Split(path, ".")
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(doc[parts[0]], &list); err != nil {
		t.Fatalf("decode %s: %v", parts[0], err)
	}
	list[0][parts[2]] = json.RawMessage(value)
	section, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("encode %s: %v", parts[0], err)
	}
	doc[parts[0]] = section
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(out)
}
