package domain_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"dev-digest-go/backend/internal/plugins/domain"
)

// allocBound is the most a refused or maximal-legal parse may allocate in total (TotalAlloc,
// which counts garbage too). Reasoning: the body is at most 20 MiB and a parse legitimately
// copies each section a few times (the top-level map holds a copy of every section, each list
// item is copied once more, Validate and Preview only read), so 3 to 4 copies of 20 MiB is the
// honest ceiling of a legal bundle. 200 MiB is that ceiling plus headroom; the unbounded decode
// this guards against cost 1.5 GB (security review r1, M1), so a regression cannot hide in it.
const allocBound = 200 << 20

// refusedBound is the bound for a bundle that is refused: the pre-pass allocates nothing per
// value, so a refusal costs about the inflated entry (zip case) and nothing for a JSON body.
const refusedBound = 40 << 20

// allocated reports the bytes fn allocated in total.
func allocated(fn func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

const hostileHead = `{"manifest":{"name":"p","version":"1","format":"devdigest-plugin/v1"},`

// fill returns head + a JSON array-or-object body made of unit repeated up to the 20 MiB cap.
func fill(head, open, unit, sep, closing string) []byte {
	room := domain.MaxInflatedBytes - len(head) - len(open) - len(closing) - 64
	n := room / (len(unit) + len(sep))
	return []byte(head + open + strings.Repeat(unit+sep, n) + unit + closing)
}

type hostileCase struct {
	name string
	body func() []byte // built on demand: each body is ~20 MiB
	want string
}

// manyKeys is a run of distinct short keys ("k0":0,"k1":0,...) of about 20 MiB, built once.
var manyKeys = sync.OnceValue(func() string {
	var b strings.Builder
	for i := 0; b.Len() < domain.MaxInflatedBytes-1024; i++ {
		b.WriteString(`"k`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":0,`)
	}
	return b.String()
})

// hostile bundles, each under the 20 MiB cap and each built to maximise what a naive decode
// allocates: one tiny token per item, or one level per byte.
func hostileBundles() []hostileCase {
	keys := manyKeys
	deep := strings.Repeat("[", 9000) + strings.Repeat("]", 9000)
	return []hostileCase{
		{"conventions of zeros (the M1 probe)", func() []byte { return fill(hostileHead, `"conventions":[`, "0", ",", "]}") }, "conventions has"},
		{"context_files of zeros", func() []byte { return fill(hostileHead, `"context_files":[`, "0", ",", "]}") }, "context_files has"},
		{"agents of zeros", func() []byte { return fill(hostileHead, `"agents":[`, "0", ",", "]}") }, "agents has"},
		{"many tiny agents", func() []byte { return fill(hostileHead, `"agents":[`, "{}", ",", "]}") }, "agents has"},
		{"many tiny skills", func() []byte { return fill(hostileHead, `"skills":[`, "{}", ",", "]}") }, "skills has"},
		{"many tiny cases", func() []byte { return fill(hostileHead, `"eval_cases":[`, "{}", ",", "]}") }, "eval_cases has"},
		{"many tiny learnings", func() []byte { return fill(hostileHead, `"learnings":[`, "{}", ",", "]}") }, "learnings has"},
		{"settings with many keys", func() []byte { return []byte(hostileHead + `"settings":{` + keys() + `"z":0}}`) }, "settings has"},
		{"an agent with many keys", func() []byte { return []byte(hostileHead + `"agents":[{` + keys() + `"z":0}]}`) }, "agents[0] has"},
		{"manifest with many keys", func() []byte { return []byte(`{"manifest":{` + keys() + `"z":0}}`) }, "manifest has"},
		{"top level with many keys", func() []byte { return []byte(`{"manifest":{},` + keys() + `"z":0}`) }, "devdigest-plugin.json has"},
		{"skill versions of zeros", func() []byte { return fill(hostileHead+`"skills":[{"name":"s","versions":`, "[", "0", ",", "]}]}") }, "skills[0].versions has"},
		{"skill evidence_files of strings", func() []byte {
			return fill(hostileHead+`"skills":[{"name":"s","evidence_files":`, "[", `""`, ",", "]}]}")
		}, "skills[0].evidence_files has"},
		{"agent skills of strings", func() []byte { return fill(hostileHead+`"agents":[{"name":"a","skills":`, "[", `""`, ",", "]}]}") }, "agents[0].skills has"},
		{"manifest counts of zeros", func() []byte { return fill(`{"manifest":{"name":"p","counts":`, "[", "0", ",", "]}}") }, "values"},
		{"deep nesting in a raw section", func() []byte { return []byte(hostileHead + `"conventions":[` + deep + `]}`) }, "nested deeper"},
		{"deep nesting in manifest counts", func() []byte { return []byte(`{"manifest":{"counts":` + deep + `}}`) }, "nested deeper"},
		{"unknown section of zeros", func() []byte { return fill(hostileHead+`"extra":`, "[", "0", ",", "]}") }, "values"},
		{"empty strings as many values", func() []byte { return fill(hostileHead+`"extra":`, "[", `""`, ",", "]}") }, "values"},
		{"mixed-case key in a skill", func() []byte { return fill(hostileHead+`"skills":[{"name":"s","VERSIONS":`, "[", "0", ",", "]}]}") }, "skills[0].versions has"},
	}
}

func TestParseUpload_HostileBundlesAreRefusedWithBoundedAllocation(t *testing.T) {
	for _, tc := range hostileBundles() {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body()
			if len(body) > domain.MaxInflatedBytes {
				t.Fatalf("test body is %d bytes, want <= the cap %d", len(body), domain.MaxInflatedBytes)
			}
			var err error
			grown := allocated(func() { _, err = domain.ParseUpload("application/json", body) })
			t.Logf("body %d KiB, TotalAlloc grew by %d KiB", len(body)>>10, grown>>10)
			wantInvalid(t, err, tc.want)
			if grown > refusedBound {
				t.Errorf("ParseUpload allocated %d MiB, want < %d MiB", grown>>20, refusedBound>>20)
			}
		})
	}
}

// The M1 probe from the review, packed as the attack ships: a ~21 KB archive.
func TestParseUpload_TokenBombInAnArchive(t *testing.T) {
	body := fill(hostileHead, `"conventions":[`, "0", ",", "]}")
	z := buildZip(t, entry{name: domain.BundleFile, data: body})
	if len(z) > 1<<20 {
		t.Fatalf("test archive is %d bytes, want a small archive", len(z))
	}
	var err error
	grown := allocated(func() { _, err = domain.ParseUpload(zipMedia, z) })
	t.Logf("archive %d KiB, TotalAlloc grew by %d KiB", len(z)>>10, grown>>10)
	wantInvalid(t, err, "conventions has")
	if grown > refusedBound {
		t.Errorf("ParseUpload(zip) allocated %d MiB, want < %d MiB", grown>>20, refusedBound>>20)
	}
}

// maximalLegalBundle fills every list to its limit, with the ignored sections of a workspace
// export at their limits, and pads the text with a long (legal) skill body.
func maximalLegalBundle() string {
	var b strings.Builder
	b.WriteString(`{"manifest":{"name":"big","version":"1","format":"devdigest-plugin/v1","kind":"workspace","counts":{"agents":100}},`)

	b.WriteString(`"agents":[`)
	for i := range domain.MaxAgents {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"agent-%d","description":"d","provider":"openai","model":"m","system_prompt":"p","output_schema":{"type":"object","properties":{"a":{"type":"string"}}},"enabled":true,"skills":["skill-0","skill-1"],"strategy":"auto","ci_fail_on":"any","repo_intel":true}`, i)
	}
	b.WriteString(`],"skills":[`)
	for i := range domain.MaxSkills {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"skill-%d","description":"d","type":"custom","source":"manual","body":"%s","enabled":true,"evidence_files":[%s],"versions":[%s]}`,
			i, strings.Repeat("x", 4000), strings.TrimSuffix(strings.Repeat(`"a/b.go",`, domain.MaxEvidenceFiles), ","),
			strings.TrimSuffix(strings.Repeat(`{"version":1,"body":"b"},`, domain.MaxSkillVersions), ","))
	}
	b.WriteString(`],"eval_cases":[`)
	for i := range domain.MaxEvalCases {
		if i > 0 {
			b.WriteByte(',')
		}
		// 200 cases per owner at most: spread over 5 skills.
		fmt.Fprintf(&b, `{"name":"c%d","owner_kind":"skill","owner_ref":"skill-%d","input_diff":"diff","input_files":[{"path":"a","content":"c"}],"input_meta":{"k":1},"expected_output":{"findings":[]},"notes":"n"}`, i, i%5)
	}
	b.WriteString(`],"learnings":[`)
	for i := range domain.MaxLearnings {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"scope":"global","kind":"fact","content":"learning %d","confidence":0.5,"repo":null}`, i)
	}
	b.WriteString(`],"conventions":[`)
	b.WriteString(strings.TrimSuffix(strings.Repeat(`{"rule":"r","repo":"a/b","evidence":[{"path":"p","start_line":1}]},`, domain.MaxConventions), ","))
	b.WriteString(`],"context_files":[`)
	b.WriteString(strings.TrimSuffix(strings.Repeat(`{"repo":"a/b","path":"CLAUDE.md","content":"c"},`, domain.MaxContextFiles), ","))
	b.WriteString(`],"settings":{"theme":"dark","density":"compact"}}`)
	return b.String()
}

func TestParseUpload_MaximalLegalBundleStillParses(t *testing.T) {
	text := maximalLegalBundle()
	if len(text) > domain.MaxInflatedBytes {
		t.Fatalf("maximal bundle is %d bytes, want <= the cap %d", len(text), domain.MaxInflatedBytes)
	}
	// The scanner counts one value per scalar, container and key; a Decoder token is one per
	// scalar, key, opening and closing delimiter, so tokens are at least the values. Expect
	// real headroom under the budget.
	dec := json.NewDecoder(strings.NewReader(text))
	tokens := 0
	for {
		if _, err := dec.Token(); err != nil {
			break
		}
		tokens++
	}
	t.Logf("maximal bundle holds %d JSON tokens (budget %d values)", tokens, domain.MaxJSONValues)
	if tokens > domain.MaxJSONValues {
		t.Errorf("maximal legal bundle holds %d tokens, want <= the %d value budget", tokens, domain.MaxJSONValues)
	}
	var (
		up  domain.Upload
		err error
	)
	grown := allocated(func() {
		up, err = domain.ParseUpload("application/json", []byte(text))
		if err == nil {
			err = domain.Validate(up.Bundle)
		}
		if err == nil {
			_ = domain.Preview(up, domain.Conflicts{})
		}
	})
	if err != nil {
		t.Fatalf("ParseUpload+Validate(maximal legal bundle of %d KiB) = %v, want nil", len(text)>>10, err)
	}
	b := up.Bundle
	if len(b.Agents) != domain.MaxAgents || len(b.Skills) != domain.MaxSkills || len(b.EvalCases) != domain.MaxEvalCases ||
		len(b.Learnings) != domain.MaxLearnings || len(b.Conventions) != domain.MaxConventions || len(b.ContextFiles) != domain.MaxContextFiles {
		t.Errorf("counts = %+v, want every list at its limit", domain.CountsOf(b))
	}
	t.Logf("maximal bundle %d KiB, TotalAlloc grew by %d KiB", len(text)>>10, grown>>10)
	if grown > allocBound {
		t.Errorf("parse of a maximal legal bundle allocated %d MiB, want < %d MiB", grown>>20, allocBound>>20)
	}
}

func TestParseUpload_ListLimitsOfIgnoredAndNestedSections(t *testing.T) {
	zeros := func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat("0,", n), ",") + "]" }
	tests := []struct {
		name  string
		build func(n int) string
		limit int
		want  string
	}{
		{"conventions", func(n int) string { return `{"manifest":{},"conventions":` + zeros(n) + `}` }, domain.MaxConventions, "conventions has %d entries; the limit is %d"},
		{"context_files", func(n int) string { return `{"manifest":{},"context_files":` + zeros(n) + `}` }, domain.MaxContextFiles, "context_files has %d entries; the limit is %d"},
		{"versions", func(n int) string { return `{"manifest":{},"skills":[{},{"versions":` + zeros(n) + `}]}` }, domain.MaxSkillVersions, "skills[1].versions has %d entries; the limit is %d"},
		{"evidence_files", func(n int) string { return `{"manifest":{},"skills":[{"Evidence_Files":` + zeros(n) + `}]}` }, domain.MaxEvidenceFiles, "skills[0].evidence_files has %d entries; the limit is %d"},
		{"agent skills", func(n int) string { return `{"manifest":{},"agents":[{"skills":` + zeros(n) + `}]}` }, domain.MaxSkills, "agents[0].skills has %d entries; the limit is %d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.ParseUpload("application/json", []byte(tc.build(tc.limit))); err != nil {
				var ie *domain.InvalidError
				if !errors.As(err, &ie) || strings.Contains(ie.Message(), "entries; the limit") {
					t.Errorf("%d entries: error = %v, want no limit error", tc.limit, err)
				}
			}
			_, err := domain.ParseUpload("application/json", []byte(tc.build(tc.limit+1)))
			wantInvalid(t, err, fmt.Sprintf(tc.want, tc.limit+1, tc.limit))
		})
	}
}

func TestParseUpload_ScannerHandlesEscapedKeysAndStrings(t *testing.T) {
	// A key spelled with escapes is the same key to the decoder; so is a different case in an
	// item. The limits must not be skipped by spelling.
	over := strings.TrimSuffix(strings.Repeat("0,", domain.MaxConventions+1), ",")
	text := `{"manifest":{},"conv\u0065ntions":[` + over + `]}`
	_, err := domain.ParseUpload("application/json", []byte(text))
	wantInvalid(t, err, "conventions has")

	// Brackets, quotes and escaped backslashes inside strings must not confuse the count.
	ok := `{"manifest":{"name":"p [ \" ] \\"},"conventions":["]","\"]","\\", "}{"]}`
	if _, err := domain.ParseUpload("application/json", []byte(ok)); err != nil {
		t.Errorf("ParseUpload(strings holding brackets) = %v, want nil", err)
	}
}

func TestParseUpload_JSONValueAndDepthBudgets(t *testing.T) {
	deep := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	text := func(inner string) []byte { return []byte(`{"manifest":{},"conventions":[` + inner + `]}`) }

	// conventions[] is level 2; the item's own nesting adds to it.
	if _, err := domain.ParseUpload("application/json", text(deep(domain.MaxJSONDepth-2))); err != nil {
		t.Errorf("nesting at the limit: error = %v, want nil", err)
	}
	_, err := domain.ParseUpload("application/json", text(deep(domain.MaxJSONDepth-1)))
	wantInvalid(t, err, "nested deeper than")

	// A value count over the budget in a section without its own limit.
	n := domain.MaxJSONValues + 1
	_, err = domain.ParseUpload("application/json", []byte(`{"manifest":{},"extra":[`+strings.Repeat("0,", n)+`0]}`))
	wantInvalid(t, err, "more than")
}

// TestParseUpload_DensestBundlesUnderTheBudgetStayBounded builds the worst shapes the budgets
// still admit: just under MaxJSONValues values, spread so that every per-item map, present-map
// and raw copy is as large as the limits allow. The measured ceiling justifies allocBound.
func TestParseUpload_DensestBundlesUnderTheBudgetStayBounded(t *testing.T) {
	perItem := (domain.MaxJSONValues/domain.MaxLearnings - 12) / 2 // unknown keys per learning: key + value each, 12 for the rest of the item
	if perItem > domain.MaxObjectKeys-5 {
		perItem = domain.MaxObjectKeys - 5
	}
	var keys strings.Builder
	for i := range perItem {
		fmt.Fprintf(&keys, `"k%d":0,`, i)
	}
	learning := `{"scope":"global","kind":"fact","content":"c",` + keys.String() + `"z":0}`
	tests := []struct {
		name string
		body string
	}{
		{"5000 learnings with the most unknown keys", hostileHead + `"learnings":[` + strings.TrimSuffix(strings.Repeat(learning+",", domain.MaxLearnings), ",") + `]}`},
		{"one output_schema holding the whole value budget", hostileHead + `"agents":[{"name":"a","provider":"openai","model":"m","system_prompt":"p","strategy":"auto","ci_fail_on":"any","output_schema":[` +
			strings.Repeat("0,", domain.MaxJSONValues-100) + `0]}]}`},
		{"20000 conventions of 40 values", hostileHead + `"conventions":[` + strings.TrimSuffix(strings.Repeat(`{`+strings.Repeat(`"a":0,`, 19)+`"z":0},`, domain.MaxConventions), ",") + `]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				up  domain.Upload
				err error
			)
			grown := allocated(func() {
				up, err = domain.ParseUpload("application/json", []byte(tc.body))
				if err == nil {
					err = domain.Validate(up.Bundle)
				}
				if err == nil {
					_ = domain.Preview(up, domain.Conflicts{})
				}
			})
			t.Logf("body %d KiB, TotalAlloc grew by %d KiB, err = %v", len(tc.body)>>10, grown>>10, err)
			if grown > allocBound {
				t.Errorf("allocated %d MiB, want < %d MiB", grown>>20, allocBound>>20)
			}
		})
	}
}
