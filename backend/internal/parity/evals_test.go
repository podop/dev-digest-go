package parity_test

// Phase 6 parity of the evals domain with the Node implementation (goldens/evals.json): rounding and
// deltas, the case rules (names, single-file diffs, normalisation, content checks, create from a finding,
// manual create, edit merge), scoring, aggregation, verdict, usage, error text, task line, skill blocks,
// the prompt line diff, compare, regression alert, sparkline, trend and period start. Spec AC6021,
// AC6066, FR6056, NFR6001; plan S32. The services (casesService, runsService, executor) and the Zod
// request validation are covered by the app and transport tests, not here. Differences that are
// decisions are listed in testdata/handwritten/evals_deviations.json (see ci_test.go, deviations).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ---------------------------------------------------------------------------
// golden access helpers
// ---------------------------------------------------------------------------

type evGolden map[string]json.RawMessage

func evLoad(t testing.TB) evGolden {
	t.Helper()
	var g evGolden
	loadGolden(t, "evals", &g)
	return g
}

// section decodes one top-level key into generic JSON values with the `$blob` digests rebuilt into their
// strings; entries holding a digest that cannot be rebuilt are dropped and counted.
func (g evGolden) list(t testing.TB, key string) (cases []map[string]any, unbuilt int) {
	t.Helper()
	raw, ok := g[key]
	if !ok {
		t.Fatalf("evals.json has no %q", key)
	}
	var all []map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	if len(all) < evMinCases[key] {
		t.Fatalf("evals.json %q has %d cases, want at least %d", key, len(all), evMinCases[key])
	}
	for _, c := range all {
		v, ok := unblob(c)
		if !ok {
			unbuilt++
			continue
		}
		cases = append(cases, v.(map[string]any))
	}
	return cases, unbuilt
}

// evMinCases are the sizes of the sections at the time the goldens were generated; a smaller section
// means a test would silently check less.
var evMinCases = map[string]int{
	"rounding": 30, "deltas": 17, "intersect": 14, "caseNames": 17, "singleFile": 5, "normalize": 15,
	"fromFinding": 27, "content": 50, "manual": 64, "taskLines": 10, "errorText": 18, "usage": 11, "kept": 3,
	"skillBlocks": 10, "score": 23, "aggregate": 68, "verdict": 9, "lineDiff": 40, "compare": 30, "alerts": 15,
	"series": 9, "periods": 10,
}

func (g evGolden) object(t testing.TB, key string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(g[key], &m); err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	return m
}

// rebuildBlob returns the string behind a `$blob` digest when it is a head, a run of one repeated rune and
// a tail (every long text of the evals goldens but the synthesised manyFilesLarge diff); it checks the
// result against the digest.
func rebuildBlob(m map[string]any) (string, bool) {
	raw, _ := json.Marshal(m)
	var b blob
	if json.Unmarshal(raw, &b) != nil {
		return "", false
	}
	mid := b.Len - utf16Len(b.Head) - utf16Len(b.Tail)
	if mid < 0 {
		return "", false
	}
	var candidates []rune
	if r := []rune(b.Head); len(r) > 0 {
		candidates = append(candidates, r[len(r)-1])
	}
	if r := []rune(b.Tail); len(r) > 0 {
		candidates = append(candidates, r[0])
	}
	for _, r := range candidates {
		w := utf16Len(string(r))
		if w == 0 || mid%w != 0 {
			continue
		}
		s := b.Head + strings.Repeat(string(r), mid/w) + b.Tail
		if (text{b: &b}).matches(s) {
			return s, true
		}
	}
	return "", false
}

// unblob rebuilds every `$blob` digest object inside v; ok is false when one cannot be rebuilt.
func unblob(v any) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		if b, _ := x["$blob"].(bool); b {
			s, ok := rebuildBlob(x)
			return s, ok
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, ok := unblob(e)
			if !ok {
				return nil, false
			}
			out[k] = r
		}
		return out, true
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, ok := unblob(e)
			if !ok {
				return nil, false
			}
			out[i] = r
		}
		return out, true
	}
	return v, true
}

func gObj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func gArr(v any) []any          { a, _ := v.([]any); return a }
func gStr(v any) string         { s, _ := v.(string); return s }
func gNum(v any) float64        { f, _ := v.(float64); return f }
func gInt(v any) int            { return int(gNum(v)) }

func gStrP(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func gFloatP(v any) *float64 {
	if f, ok := v.(float64); ok {
		return &f
	}
	return nil
}

func gIntP(v any) *int {
	if f, ok := v.(float64); ok {
		i := int(f)
		return &i
	}
	return nil
}

// ---------------------------------------------------------------------------
// Go value -> golden shape
// ---------------------------------------------------------------------------

func ptrVal[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func evExpectation(m map[string]any) domain.Expectation {
	return domain.Expectation{
		Kind: domain.ExpectationKind(gStr(m["kind"])), File: gStr(m["file"]),
		StartLine: gInt(m["start_line"]), EndLine: gInt(m["end_line"]),
		Severity: gStrP(m["severity"]), Category: gStrP(m["category"]), Title: gStrP(m["title"]),
	}
}

func expWire(e domain.Expectation) map[string]any {
	return map[string]any{
		"kind": string(e.Kind), "file": e.File, "start_line": float64(e.StartLine), "end_line": float64(e.EndLine),
		"severity": ptrVal(e.Severity), "category": ptrVal(e.Category), "title": ptrVal(e.Title),
	}
}

func metaWire(m domain.InputMeta) map[string]any {
	var pr any
	if m.PRNumber != nil {
		pr = float64(*m.PRNumber)
	}
	return map[string]any{
		"pr_title": m.PRTitle, "pr_description": ptrVal(m.PRDescription), "repo": ptrVal(m.Repo),
		"pr_number": pr, "head_sha": ptrVal(m.HeadSHA),
	}
}

func evMeta(m map[string]any) domain.InputMeta {
	return domain.InputMeta{
		PRTitle: gStr(m["pr_title"]), PRDescription: gStrP(m["pr_description"]), Repo: gStrP(m["repo"]),
		PRNumber: gIntP(m["pr_number"]), HeadSHA: gStrP(m["head_sha"]),
	}
}

// ruleErrWire is the Node error shape of a *RuleError: the application error name, kind, code, message
// and details (LineRange as {start, end}).
func ruleErrWire(err error) any {
	var re *domain.RuleError
	if !errors.As(err, &re) {
		return map[string]any{"notARuleError": fmt.Sprintf("%T %v", err, err)}
	}
	name, kind := "ValidationError", "validation"
	if re.Kind == domain.RuleBadRequest {
		name, kind = "InvalidInputError", "invalid_input"
	}
	out := map[string]any{"name": name, "kind": kind, "code": re.Code, "message": re.Message}
	if re.Details != nil {
		d := map[string]any{}
		for k, v := range re.Details {
			switch x := v.(type) {
			case []domain.LineRange:
				rs := make([]any, len(x))
				for i, r := range x {
					rs[i] = map[string]any{"start": float64(r.Start), "end": float64(r.End)}
				}
				d[k] = rs
			case []string:
				ss := make([]any, len(x))
				for i, s := range x {
					ss[i] = s
				}
				d[k] = ss
			default:
				d[k] = v
			}
		}
		out["details"] = d
	}
	return out
}

func newCaseWire(c domain.NewCase) map[string]any {
	return map[string]any{
		"workspaceId": c.WorkspaceID, "ownerKind": string(c.OwnerKind), "ownerId": c.OwnerID, "name": c.Name,
		"notes": ptrVal(c.Notes), "expectation": expWire(c.Expectation), "inputDiff": c.InputDiff,
		"inputMeta": metaWire(c.InputMeta), "sourceFindingId": ptrVal(c.SourceFindingID),
		"sourceReviewId": ptrVal(c.SourceReviewID), "sourcePrId": ptrVal(c.SourcePRID),
	}
}

// nodeOutcome is the golden's result or error of a case in one comparable value.
func nodeOutcome(c map[string]any) any {
	if e, ok := c["error"]; ok {
		return e
	}
	return c["result"]
}

// evDeviation checks a value pair through the deviation list under section/name.
func evCompare(t testing.TB, dev *deviations, section, name string, want, got any) {
	t.Helper()
	dev.check(t, section+"/"+name, toValue(t, want), toValue(t, got), section)
}

// ---------------------------------------------------------------------------
// constants, rounding, deltas, intersect
// ---------------------------------------------------------------------------

func TestEvalsConstantsParity(t *testing.T) {
	g := evLoad(t)
	c := g.object(t, "constants")
	sh := gObj(c["shared"])
	for _, tc := range []struct {
		name      string
		want, got float64
	}{
		{"REGRESSION_THRESHOLD", gNum(c["REGRESSION_THRESHOLD"]), -domain.RegressionThreshold},
		{"METRIC_DECIMALS", gNum(c["METRIC_DECIMALS"]), domain.MetricDecimals},
		{"COST_DECIMALS", gNum(c["COST_DECIMALS"]), domain.CostDecimals},
		{"SPARKLINE_POINTS", gNum(c["SPARKLINE_POINTS"]), domain.SparklineMax},
		{"TREND_POINTS", gNum(c["TREND_POINTS"]), domain.LatestWindow},
		{"RECENT_RUNS_LIMIT", gNum(c["RECENT_RUNS_LIMIT"]), domain.RecentRunsMax},
		{"EVAL_CASE_CONCURRENCY", gNum(c["EVAL_CASE_CONCURRENCY"]), domain.CaseConcurrency},
		// Deliberate divergence (2026-10-28): the Go case budget is 6× Node's 120 s (one review
		// call's total budget, llm.DefaultTotalBudget).
		{"EVAL_CASE_TIMEOUT_MS", gNum(c["EVAL_CASE_TIMEOUT_MS"]), float64(domain.CaseBudget.Milliseconds()) / 6},
		{"ERROR_MESSAGE_MAX_CHARS", gNum(c["ERROR_MESSAGE_MAX_CHARS"]), domain.ErrorTextMax},
		{"EVAL_SHUTDOWN_WAIT_MS", gNum(c["EVAL_SHUTDOWN_WAIT_MS"]), float64(domain.ShutdownDrain.Milliseconds())},
		{"EVAL_CASE_NAME_MAX", gNum(sh["EVAL_CASE_NAME_MAX"]), domain.CaseNameMax},
		{"EVAL_INPUT_DIFF_MAX_CHARS", gNum(sh["EVAL_INPUT_DIFF_MAX_CHARS"]), domain.DiffMaxChars},
		{"EVAL_CASES_PER_AGENT_MAX", gNum(sh["EVAL_CASES_PER_AGENT_MAX"]), domain.CasesPerOwnerMax},
		{"EVAL_DIFF_FILES_DETAIL_MAX", gNum(sh["EVAL_DIFF_FILES_DETAIL_MAX"]), domain.DiffFilesDetailMax},
		{"EVAL_PERIOD_TREND_MAX", gNum(sh["EVAL_PERIOD_TREND_MAX"]), domain.TrendMax},
		{"EVAL_EXPECTATION_FILE_MAX", gNum(sh["EVAL_EXPECTATION_FILE_MAX"]), domain.ExpectationFileMax},
		{"EVAL_EXPECTATION_TITLE_MAX", gNum(sh["EVAL_EXPECTATION_TITLE_MAX"]), domain.ExpectationTitleMax},
		{"EVAL_PR_TITLE_MAX", gNum(sh["EVAL_PR_TITLE_MAX"]), domain.PRTitleMax},
		{"EVAL_PR_DESCRIPTION_MAX", gNum(sh["EVAL_PR_DESCRIPTION_MAX"]), domain.PRDescriptionMax},
	} {
		if tc.want != tc.got {
			t.Errorf("%s = %v, Node %v", tc.name, tc.got, tc.want)
		}
	}
	if c["INTERRUPTED_ERROR"] != domain.InterruptedError {
		t.Errorf("InterruptedError = %q, Node %v", domain.InterruptedError, c["INTERRUPTED_ERROR"])
	}
	var periods []string
	for _, p := range []string{"7d", "30d", "90d", "all", "1d", "", "7D"} {
		if _, ok := domain.ParsePeriod(p); ok {
			periods = append(periods, p)
		}
	}
	if diff := cmp.Diff(toValue(t, periods), sh["EVAL_PERIODS"]); diff != "" {
		t.Errorf("accepted periods (-node +go):\n%s", diff)
	}
}

func TestEvalsRoundingParity(t *testing.T) {
	g := evLoad(t)
	cases, _ := g.list(t, "rounding")
	for _, c := range cases {
		v, d := gNum(c["value"]), gInt(c["decimals"])
		if got, want := domain.RoundTo(v, d), gNum(c["result"]); got != want {
			t.Errorf("RoundTo(%v, %d) = %v, want %v", v, d, got, want)
		}
	}
	deltas, _ := g.list(t, "deltas")
	for _, c := range deltas {
		dec := domain.MetricDecimals
		if d, ok := c["decimals"]; ok {
			dec = gInt(d)
		}
		got := domain.MetricChange(gFloatP(c["prev"]), gFloatP(c["next"]), dec)
		want := gFloatP(c["result"])
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("MetricChange(%v, %v, %d) = %v, want %v", c["prev"], c["next"], dec, ptrVal(got), ptrVal(want))
		}
	}
	inter, _ := g.list(t, "intersect")
	for _, c := range inter {
		a, b := gArr(c["a"]), gArr(c["b"])
		got := domain.RangesIntersect(gInt(a[0]), gInt(a[1]), gInt(b[0]), gInt(b[1]))
		if want, _ := c["result"].(bool); got != want {
			t.Errorf("RangesIntersect(%v, %v) = %v, want %v", a, b, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// case rules
// ---------------------------------------------------------------------------

func TestEvalsCaseTextParity(t *testing.T) {
	g := evLoad(t)
	names, _ := g.list(t, "caseNames")
	for _, c := range names {
		if got, want := domain.CaseName(gStr(c["title"]), gStr(c["file"]), gInt(c["startLine"])), gStr(c["name"]); got != want {
			t.Errorf("CaseName(%q, %q, %v) = %q, want %q", c["title"], c["file"], c["startLine"], got, want)
		}
	}
	single, _ := g.list(t, "singleFile")
	for _, c := range single {
		if got, want := domain.BuildSingleFileDiff(gStr(c["path"]), gStr(c["patch"])), gStr(c["diff"]); got != want {
			t.Errorf("BuildSingleFileDiff(%q, %q) = %q, want %q", c["path"], c["patch"], got, want)
		}
	}
	norm, _ := g.list(t, "normalize")
	for _, c := range norm {
		if got, want := domain.NormalizeCaseDiff(gStr(c["raw"]), gStr(c["file"])), gStr(c["result"]); got != want {
			t.Errorf("%s: NormalizeCaseDiff(%q, %q) = %q, want %q", c["name"], c["raw"], c["file"], got, want)
		}
	}
	tl, _ := g.list(t, "taskLines")
	for _, c := range tl {
		m := gObj(c["meta"])
		if got, want := domain.TaskLine(gIntP(m["pr_number"]), gStr(m["pr_title"])), gStr(c["line"]); got != want {
			t.Errorf("TaskLine(%v, %q) = %q, want %q", m["pr_number"], m["pr_title"], got, want)
		}
	}
	sb, _ := g.list(t, "skillBlocks")
	for _, c := range sb {
		if got, want := reviewer.SkillBlock(gStr(c["name"]), gStr(c["description"]), gStr(c["body"])), gStr(c["block"]); got != want {
			t.Errorf("SkillBlock(%q) = %q, want %q", c["name"], got, want)
		}
	}
}

func TestEvalsErrorTextAndUsageParity(t *testing.T) {
	g := evLoad(t)
	et, _ := g.list(t, "errorText")
	n := 0
	for _, c := range et {
		switch gStr(c["kind"]) {
		case "error", "string", "number": // the Go callers pass err.Error(): the message
			n++
			if got, want := domain.ErrorText(gStr(c["message"]), nil), gStr(c["result"]); got != want {
				t.Errorf("ErrorText(%q) = %q, want %q", c["message"], got, want)
			}
		}
	}
	if n < 14 {
		t.Errorf("only %d error text cases were compared", n)
	}
	// redaction runs before the whitespace collapse and the cut
	if got := domain.ErrorText("key sk-1  bad", func(s string) string { return strings.ReplaceAll(s, "sk-1", "[redacted]") }); got != "key [redacted] bad" {
		t.Errorf("ErrorText with redaction = %q", got)
	}
	us, _ := g.list(t, "usage")
	for _, c := range us {
		u := domain.NewCaseUsage()
		for _, x := range gArr(c["costs"]) {
			u.Add(gFloatP(x))
		}
		got, want := u.Cost(), gFloatP(c["rounded"])
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%s: Cost() = %v, want %v", c["name"], ptrVal(got), ptrVal(want))
		}
	}
	kept, _ := g.list(t, "kept")
	for _, c := range kept {
		var fs []reviewer.Finding
		for _, x := range gArr(c["findings"]) {
			f := gObj(x)
			fs = append(fs, reviewer.Finding{
				ID: gStr(f["id"]), Severity: reviewer.Severity(gStr(f["severity"])), Category: reviewer.Category(gStr(f["category"])),
				Title: gStr(f["title"]), File: gStr(f["file"]), StartLine: gInt(f["start_line"]), EndLine: gInt(f["end_line"]),
				Rationale: gStr(f["rationale"]),
			})
		}
		got := []any{}
		for _, k := range domain.ToKeptFindings(fs) {
			got = append(got, map[string]any{
				"file": k.File, "start_line": float64(k.StartLine), "end_line": float64(k.EndLine),
				"severity": ptrVal(k.Severity), "category": ptrVal(k.Category), "title": ptrVal(k.Title),
			})
		}
		if diff := cmp.Diff(c["result"], toValue(t, got)); diff != "" {
			t.Errorf("%s: ToKeptFindings (-node +go):\n%s", c["name"], diff)
		}
	}
}

func evNewCaseFromSource(src map[string]any) domain.FindingSource {
	fs := domain.FindingSource{
		FindingID: gStr(src["findingId"]), ReviewID: gStr(src["reviewId"]), PRID: gStr(src["prId"]),
		AgentID: gStrP(src["agentId"]), File: gStr(src["file"]), StartLine: gInt(src["startLine"]), EndLine: gInt(src["endLine"]),
		Severity: gStr(src["severity"]), Category: gStr(src["category"]), Title: gStr(src["title"]),
		Patch: gStrP(src["patch"]), PRTitle: gStr(src["prTitle"]), PRBody: gStrP(src["prBody"]),
		RepoFullName: gStr(src["repoFullName"]), PRNumber: gInt(src["prNumber"]), HeadSHA: gStr(src["headSha"]),
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	switch gStr(src["decision"]) {
	case "accepted":
		fs.AcceptedAt = &at
	case "dismissed":
		fs.DismissedAt = &at
	}
	return fs
}

func TestEvalsFromFindingParity(t *testing.T) {
	g := evLoad(t)
	dev := loadDeviations(t, "evals_deviations")
	cases, unbuilt := g.list(t, "fromFinding")
	for _, c := range cases {
		nc, err := domain.BuildFromFinding("ws-1", evNewCaseFromSource(gObj(c["source"])))
		var got any
		if err != nil {
			got = ruleErrWire(err)
		} else {
			got = newCaseWire(nc)
		}
		evCompare(t, dev, "fromFinding", gStr(c["name"]), nodeOutcome(c), got)
	}
	if unbuilt != 0 {
		t.Errorf("%d fromFinding cases could not be rebuilt from their digests", unbuilt)
	}
	dev.finish(t, "fromFinding/")
}

func TestEvalsContentParity(t *testing.T) {
	g := evLoad(t)
	dev := loadDeviations(t, "evals_deviations")
	rawDiffs := g.object(t, "diffs")
	diffs := map[string]string{}
	unbuiltDiffs := map[string]bool{}
	var corpus struct {
		Diffs map[string]struct {
			Text any `json:"text"`
		} `json:"diffs"`
	}
	loadCorpus(t, "evals", "content", &corpus)
	for k, v := range rawDiffs {
		if r, ok := unblob(v); ok {
			diffs[k] = r.(string)
			continue
		}
		// A digest that is not a head, a run and a tail: expand the corpus text it came from.
		if src := corpus.Diffs[k].Text; src != nil {
			var b blob
			raw, _ := json.Marshal(v)
			if json.Unmarshal(raw, &b) == nil {
				if s := expandStrings(t, src); (text{b: &b}).matches(s) {
					diffs[k] = s
					continue
				}
			}
		}
		unbuiltDiffs[k] = true
	}
	if len(unbuiltDiffs) != 1 || !unbuiltDiffs["manyFilesLarge"] {
		t.Errorf("unbuildable diffs = %v, want only manyFilesLarge", unbuiltDiffs)
	}
	cases, _ := g.list(t, "content")
	skipped := 0
	for _, c := range cases {
		d, ok := diffs[gStr(c["diff"])]
		if !ok {
			skipped++
			continue
		}
		e := gObj(c["expectation"])
		err := domain.CheckCaseContent(d, domain.Expectation{File: gStr(e["file"]), StartLine: gInt(e["start_line"]), EndLine: gInt(e["end_line"])})
		var got any = map[string]any{"ok": true}
		if err != nil {
			got = ruleErrWire(err)
		}
		evCompare(t, dev, "content", gStr(c["name"]), nodeOutcome(c), got)
	}
	if skipped != 0 {
		t.Errorf("%d content cases use a diff that could not be rebuilt", skipped)
	}
	dev.finish(t, "content/")
}

func evCreateInput(d map[string]any) domain.CreateInput {
	meta := gObj(d["input_meta"])
	return domain.CreateInput{
		Name: gStr(d["name"]), Notes: gStrP(d["notes"]), Expectation: evExpectation(gObj(d["expectation"])),
		InputDiff: gStr(d["input_diff"]), PRTitle: gStrP(meta["pr_title"]), PRDescription: gStrP(meta["pr_description"]),
	}
}

func TestEvalsManualCaseParity(t *testing.T) {
	g := evLoad(t)
	dev := loadDeviations(t, "evals_deviations")
	cases, unbuilt := g.list(t, "manual")
	compared := 0
	for _, c := range cases {
		parse := gObj(c["parse"])
		if ok, _ := parse["ok"].(bool); !ok {
			continue // a Zod issue list: the request schema belongs to the transport tests
		}
		compared++
		in := evCreateInput(gObj(parse["data"]))
		owner := gObj(c["owner"])
		diff := domain.NormalizeCaseDiff(in.InputDiff, in.Expectation.File)
		var got any
		if err := domain.CheckCaseContent(diff, in.Expectation); err != nil {
			got = ruleErrWire(err)
		} else {
			got = newCaseWire(domain.BuildManualCase("ws-1", domain.OwnerKind(gStr(owner["kind"])), gStr(owner["id"]), in, diff))
		}
		evCompare(t, dev, "manual", gStr(c["name"]), nodeOutcome(c), got)
	}
	if compared < 30 {
		t.Errorf("only %d manual cases were compared", compared)
	}
	if unbuilt != 0 {
		t.Errorf("%d manual cases could not be rebuilt from their digests", unbuilt)
	}
	dev.finish(t, "manual/")
}

func evUpdateInput(d map[string]any) domain.UpdateInput {
	field := func(key string) domain.Field[string] {
		v, ok := d[key]
		if !ok {
			return domain.Field[string]{}
		}
		return domain.Field[string]{Set: true, Value: gStrP(v)}
	}
	u := domain.UpdateInput{
		Name: gStrP(d["name"]), File: gStrP(d["file"]), StartLine: gIntP(d["start_line"]), EndLine: gIntP(d["end_line"]),
		InputDiff: gStrP(d["input_diff"]),
		Notes:     field("notes"), Severity: field("severity"), Category: field("category"), Title: field("title"),
		PRTitle: field("pr_title"), PRDescription: field("pr_description"),
	}
	if k := gStrP(d["kind"]); k != nil {
		kind := domain.ExpectationKind(*k)
		u.Kind = &kind
	}
	return u
}

func evStoredCase(m map[string]any) domain.Case {
	return domain.Case{
		ID: "case-1", WorkspaceID: "ws-1", OwnerKind: domain.OwnerAgent, OwnerID: "agent-1",
		Name: gStr(m["name"]), Notes: gStrP(m["notes"]), Expectation: evExpectation(gObj(m["expectation"])),
		InputDiff: gStr(m["inputDiff"]), InputMeta: evMeta(gObj(m["inputMeta"])),
	}
}

func TestEvalsEditParity(t *testing.T) {
	g := evLoad(t)
	dev := loadDeviations(t, "evals_deviations")
	edit := g.object(t, "edit")
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	compared := 0
	for _, group := range []struct{ stored, cases string }{{"stored", "cases"}, {"manualStored", "manualCases"}} {
		stored := evStoredCase(gObj(edit[group.stored]))
		for _, raw := range gArr(edit[group.cases]) {
			cv, ok := unblob(raw)
			if !ok {
				t.Errorf("an edit case could not be rebuilt from its digests")
				continue
			}
			c := cv.(map[string]any)
			parse := gObj(c["parse"])
			if ok, _ := parse["ok"].(bool); !ok {
				continue
			}
			compared++
			name := group.cases + "/" + gStr(c["name"])
			m, err := domain.MergeEdit(stored, evUpdateInput(gObj(parse["data"])), now)
			if err != nil {
				evCompare(t, dev, "edit", name, c["error"], ruleErrWire(err))
				continue
			}
			want := gObj(c["result"])
			if want == nil {
				evCompare(t, dev, "edit", name, c["error"], "no error")
				continue
			}
			vals := gObj(want["values"])
			got := map[string]any{}
			// A field the patch wrote is in values; one it did not write must still equal the stored one.
			pick := func(key string, written, stored any) {
				if _, w := vals[key]; w {
					got[key] = written
				} else if cmp.Diff(toValue(t, written), toValue(t, stored)) != "" {
					got[key] = written
					got[key+"-changed-but-not-written"] = true
				}
			}
			mc := m.Case
			pick("name", mc.Name, stored.Name)
			pick("notes", ptrVal(mc.Notes), ptrVal(stored.Notes))
			pick("expectation", expWire(mc.Expectation), expWire(stored.Expectation))
			pick("inputDiff", mc.InputDiff, stored.InputDiff)
			pick("inputMeta", metaWire(mc.InputMeta), metaWire(stored.InputMeta))
			if mc.ContentEditedAt != nil {
				got["markContentEdited"] = true
			}
			result := map[string]any{"values": got, "recheck": m.Recheck, "contentChanged": m.ContentChanged}
			if m.Recheck {
				var rr any = map[string]any{"result": map[string]any{"ok": true}}
				if err := domain.CheckCaseContent(mc.InputDiff, mc.Expectation); err != nil {
					rr = map[string]any{"error": ruleErrWire(err)}
				}
				result["recheckResult"] = rr
			}
			evCompare(t, dev, "edit", name, want, result)
		}
	}
	if compared < 30 {
		t.Errorf("only %d edit cases were compared", compared)
	}
	dev.finish(t, "edit/")
}

// ---------------------------------------------------------------------------
// scoring, aggregate, verdict (AC6021)
// ---------------------------------------------------------------------------

func TestEvalsScoringParity(t *testing.T) {
	g := evLoad(t)
	cases, _ := g.list(t, "score")
	for _, c := range cases {
		var kept []domain.KeptFinding
		for _, x := range gArr(c["kept"]) {
			f := gObj(x)
			kept = append(kept, domain.KeptFinding{
				File: gStr(f["file"]), StartLine: gInt(f["start_line"]), EndLine: gInt(f["end_line"]),
				Severity: gStrP(f["severity"]), Category: gStrP(f["category"]), Title: gStrP(f["title"]),
			})
		}
		s := domain.ScoreCase(evExpectation(gObj(c["expectation"])), kept)
		fs := []any{}
		for _, f := range s.Findings {
			fs = append(fs, map[string]any{
				"file": f.File, "start_line": float64(f.StartLine), "end_line": float64(f.EndLine),
				"severity": ptrVal(f.Severity), "category": ptrVal(f.Category), "title": ptrVal(f.Title), "match": string(f.Match),
			})
		}
		got := map[string]any{
			"status": string(s.Status), "findings": fs, "hit_count": float64(s.HitCount),
			"violation_count": float64(s.ViolationCount), "unlabeled_count": float64(s.UnlabeledCount),
		}
		if diff := cmp.Diff(c["result"], toValue(t, got)); diff != "" {
			t.Errorf("%s: ScoreCase (-node +go):\n%s", c["name"], diff)
		}
	}
}

func evOutcomes(list []any) []domain.CaseOutcome {
	var out []domain.CaseOutcome
	for _, x := range list {
		o := gObj(x)
		out = append(out, domain.CaseOutcome{
			Kind: domain.ExpectationKind(gStr(o["kind"])), Status: domain.CaseStatus(gStr(o["status"])),
			HitCount: gInt(o["hit_count"]), ViolationCount: gInt(o["violation_count"]), UnlabeledCount: gInt(o["unlabeled_count"]),
			DroppedCount: gInt(o["dropped_count"]), CostUSD: gFloatP(o["cost_usd"]), Error: gStrP(o["error"]),
		})
	}
	return out
}

func TestEvalsAggregateAndVerdictParity(t *testing.T) {
	g := evLoad(t)
	agg, _ := g.list(t, "aggregate")
	if len(agg) < 60 {
		t.Fatalf("golden has %d aggregate cases", len(agg))
	}
	for _, c := range agg {
		r := domain.AggregateRun(evOutcomes(gArr(c["outcomes"])))
		got := map[string]any{
			"recall": ptrVal(r.Metrics.Recall), "precision": ptrVal(r.Metrics.Precision),
			"citation_accuracy": ptrVal(r.Metrics.CitationAccuracy), "cases_passed": float64(r.CasesPassed),
			"cases_errored": float64(r.CasesErrored), "unlabeled_findings": float64(r.Metrics.UnlabeledFindings),
			"cost_usd": ptrVal(r.Metrics.CostUSD),
		}
		if diff := cmp.Diff(c["result"], toValue(t, got)); diff != "" {
			t.Errorf("%s: AggregateRun (-node +go):\n%s", c["name"], diff)
		}
	}
	vs, _ := g.list(t, "verdict")
	for _, c := range vs {
		v := domain.Verdict(evOutcomes(gArr(c["outcomes"])))
		got := map[string]any{"status": string(v.Status), "error": ptrVal(v.Error)}
		if diff := cmp.Diff(c["result"], toValue(t, got)); diff != "" {
			t.Errorf("%s: Verdict (-node +go):\n%s", c["name"], diff)
		}
	}
}

// ---------------------------------------------------------------------------
// line diff (AC6066)
// ---------------------------------------------------------------------------

func diffWire(lines []domain.DiffLine) []any {
	out := make([]any, len(lines))
	for i, l := range lines {
		out[i] = map[string]any{"op": string(l.Op), "line": l.Line}
	}
	return out
}

// evGenLines is the generator's text of a `gen` line diff case.
func evGenLines(spec map[string]any) (before, after string) {
	rng := func(n int, f func(i int) string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = f(i + 1)
		}
		return out
	}
	if gStr(spec["kind"]) == "alternate" {
		b := rng(gInt(spec["before"]), func(i int) string { return "l-" + strconv.Itoa(i) })
		a := rng(gInt(spec["after"]), func(i int) string {
			if i%2 == 0 {
				return "l-" + strconv.Itoa(i)
			}
			return "m-" + strconv.Itoa(i)
		})
		return strings.Join(b, "\n"), strings.Join(a, "\n")
	}
	head := rng(gInt(spec["prefix"]), func(i int) string { return "head-" + strconv.Itoa(i) })
	tail := rng(gInt(spec["suffix"]), func(i int) string { return "tail-" + strconv.Itoa(i) })
	b := slices.Concat(head, rng(gInt(spec["before"]), func(i int) string { return "a-" + strconv.Itoa(i) }), tail)
	a := slices.Concat(head, rng(gInt(spec["after"]), func(i int) string { return "b-" + strconv.Itoa(i) }), tail)
	return strings.Join(b, "\n"), strings.Join(a, "\n")
}

func TestEvalsLineDiffParity(t *testing.T) {
	g := evLoad(t)
	cases, _ := g.list(t, "lineDiff")
	summaries := 0
	for _, c := range cases {
		before, after := gStr(c["before"]), gStr(c["after"])
		if gen := gObj(c["gen"]); gen != nil {
			before, after = evGenLines(gen)
		}
		got := domain.LineDiff(before, after)
		if sum := gObj(c["summary"]); sum != nil {
			summaries++
			h := sha256.New()
			var ops []any
			for _, l := range got {
				fmt.Fprintf(h, "%s\t%s\n", l.Op, l.Line)
				if n := len(ops); n > 0 && gStr(ops[n-1].([]any)[0]) == string(l.Op) {
					ops[n-1].([]any)[1] = ops[n-1].([]any)[1].(float64) + 1
				} else {
					ops = append(ops, []any{string(l.Op), float64(1)})
				}
			}
			gotSum := map[string]any{"count": float64(len(got)), "ops": ops, "sha256": hex.EncodeToString(h.Sum(nil))}
			if diff := cmp.Diff(sum, toValue(t, gotSum)); diff != "" {
				t.Errorf("%s: LineDiff summary (-node +go):\n%s", c["name"], diff)
			}
			continue
		}
		if diff := cmp.Diff(c["result"], toValue(t, diffWire(got))); diff != "" {
			t.Errorf("%s: LineDiff (-node +go):\n%s", c["name"], diff)
		}
	}
	if summaries < 8 {
		t.Errorf("only %d summarised line diffs were compared, want 8", summaries)
	}
	// AC6066: a prompt above the table limit is all deletions then all additions.
	b, a := evGenLines(map[string]any{"kind": "different", "before": 3000.0, "after": 3000.0})
	got := domain.LineDiff(b, a)
	if len(got) != 6000 {
		t.Fatalf("3000 x 3000 line diff has %d lines, want 6000", len(got))
	}
	for i, l := range got {
		if want := domain.OpDel; i >= 3000 {
			if l.Op != domain.OpAdd {
				t.Fatalf("line %d op = %s, want add", i, l.Op)
			}
		} else if l.Op != want {
			t.Fatalf("line %d op = %s, want del", i, l.Op)
		}
	}
}

// ---------------------------------------------------------------------------
// compare, alerts, series, periods (AC6066)
// ---------------------------------------------------------------------------

func evRun(m map[string]any, cfg map[string]any) domain.Run {
	r := domain.Run{
		ID: gStr(m["id"]), OwnerKind: domain.OwnerKind(gStr(m["owner_kind"])), SkillID: gStrP(m["skill_id"]),
		SkillVersion: gIntP(m["skill_version"]), AgentID: gStr(m["agent_id"]), AgentVersion: gInt(m["agent_version"]),
		AgentName: gStr(m["agent_name"]), Status: domain.RunStatus(gStr(m["status"])),
		Metrics: domain.Metrics{
			Recall: gFloatP(m["recall"]), Precision: gFloatP(m["precision"]), CitationAccuracy: gFloatP(m["citation_accuracy"]),
			UnlabeledFindings: gInt(m["unlabeled_findings"]), CostUSD: gFloatP(m["cost_usd"]),
		},
	}
	if cfg != nil {
		r.Config = domain.Config{
			Provider: gStr(cfg["provider"]), Model: gStr(cfg["model"]), SystemPrompt: gStr(cfg["system_prompt"]), Strategy: gStr(cfg["strategy"]),
		}
		for _, s := range gArr(cfg["skills"]) {
			sk := gObj(s)
			r.Config.Skills = append(r.Config.Skills, domain.SkillSnapshot{
				ID: gStr(sk["id"]), Name: gStr(sk["name"]), Description: gStr(sk["description"]), Body: gStr(sk["body"]), Version: gInt(sk["version"]),
			})
		}
	}
	return r
}

func evSide(m map[string]any) domain.CompareSide {
	s := domain.CompareSide{Run: evRun(gObj(m["run"]), gObj(m["config"]))}
	for _, x := range gArr(m["results"]) {
		r := gObj(x)
		s.Results = append(s.Results, domain.CaseResult{CaseID: gStr(r["case_id"]), CaseName: gStr(r["case_name"]), Status: domain.CaseStatus(gStr(r["status"]))})
	}
	return s
}

func compareWire(c domain.Compare) map[string]any {
	cases := []any{}
	for _, x := range c.Cases {
		cases = append(cases, map[string]any{"case_id": x.CaseID, "case_name": x.CaseName, "a": string(x.A), "b": string(x.B), "flipped": x.Flipped})
	}
	change := func(v *domain.ValueChange) any {
		if v == nil {
			return nil
		}
		return map[string]any{"a": v.A, "b": v.B}
	}
	strs := func(s []string) []any {
		out := []any{}
		for _, x := range s {
			out = append(out, x)
		}
		return out
	}
	return map[string]any{
		"delta": map[string]any{
			"recall": ptrVal(c.Delta.Recall), "precision": ptrVal(c.Delta.Precision),
			"citation_accuracy": ptrVal(c.Delta.CitationAccuracy), "cost_usd": ptrVal(c.Delta.CostUSD),
		},
		"cases": cases,
		"config_diff": map[string]any{
			"system_prompt": diffWire(c.ConfigDiff.SystemPrompt), "model": change(c.ConfigDiff.Model),
			"strategy": change(c.ConfigDiff.Strategy), "skills_added": strs(c.ConfigDiff.SkillsAdded),
			"skills_removed": strs(c.ConfigDiff.SkillsRemoved),
		},
	}
}

func TestEvalsCompareParity(t *testing.T) {
	g := evLoad(t)
	cases, _ := g.list(t, "compare")
	if len(cases) != 30 {
		t.Fatalf("golden has %d compare cases, want 30", len(cases))
	}
	for _, c := range cases {
		a, b := evSide(gObj(c["a"])), evSide(gObj(c["b"]))
		res, err := domain.CompareRuns(a, b)
		if e, failed := c["error"]; failed {
			if diff := cmp.Diff(e, toValue(t, ruleErrWire(err))); diff != "" {
				t.Errorf("%s: CompareRuns error (-node +go):\n%s", c["name"], diff)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: CompareRuns: %v", c["name"], err)
			continue
		}
		want := gObj(c["result"])
		got := compareWire(res)
		got["a_id"], got["b_id"] = res.A.ID, res.B.ID
		wantCmp := map[string]any{
			"delta": want["delta"], "cases": want["cases"], "config_diff": want["config_diff"],
			"a_id": gObj(want["a"])["id"], "b_id": gObj(want["b"])["id"],
		}
		if diff := cmp.Diff(wantCmp, toValue(t, got)); diff != "" {
			t.Errorf("%s: CompareRuns (-node +go):\n%s", c["name"], diff)
		}
	}
}

func TestEvalsAlertsSeriesPeriodsParity(t *testing.T) {
	g := evLoad(t)
	alerts, _ := g.list(t, "alerts")
	metricsRun := func(v any) *domain.Run {
		m := gObj(v)
		if m == nil {
			return nil
		}
		r := evRun(map[string]any{"recall": m["recall"], "precision": m["precision"], "citation_accuracy": m["citation_accuracy"]}, nil)
		return &r
	}
	for _, c := range alerts {
		got := domain.RegressionAlert(metricsRun(c["latest"]), metricsRun(c["previous"]))
		var gotWire any
		if got != nil {
			ms := []any{}
			for _, d := range got {
				ms = append(ms, map[string]any{"metric": string(d.Metric), "delta": d.Delta})
			}
			gotWire = map[string]any{"metrics": ms}
		}
		if diff := cmp.Diff(c["result"], toValue(t, gotWire)); diff != "" {
			t.Errorf("%s: RegressionAlert (-node +go):\n%s", c["name"], diff)
		}
	}

	series, _ := g.list(t, "series")
	ids := func(runs []domain.Run) []any {
		out := []any{}
		for _, r := range runs {
			out = append(out, r.ID)
		}
		return out
	}
	for _, c := range series {
		var runs []domain.Run
		for _, x := range gArr(c["runsNewestFirst"]) {
			runs = append(runs, evRun(gObj(x), nil))
		}
		spark := []any{}
		for _, p := range domain.Sparkline(runs) {
			spark = append(spark, map[string]any{"run_id": p.RunID, "recall": ptrVal(p.Recall), "precision": ptrVal(p.Precision), "citation_accuracy": ptrVal(p.CitationAccuracy)})
		}
		if diff := cmp.Diff(c["sparkline"], toValue(t, spark)); diff != "" {
			t.Errorf("%s: Sparkline (-node +go):\n%s", c["name"], diff)
		}
		if diff := cmp.Diff(c["trendDefault"], toValue(t, ids(domain.Trend(runs, domain.LatestWindow)))); diff != "" {
			t.Errorf("%s: Trend(default) (-node +go):\n%s", c["name"], diff)
		}
		for _, tr := range gArr(c["trends"]) {
			m := gObj(tr)
			if diff := cmp.Diff(m["ids"], toValue(t, ids(domain.Trend(runs, gInt(m["max"]))))); diff != "" {
				t.Errorf("%s: Trend(%v) (-node +go):\n%s", c["name"], m["max"], diff)
			}
		}
	}

	periods, _ := g.list(t, "periods")
	for _, c := range periods {
		now, err := time.Parse(time.RFC3339, gStr(c["now"]))
		if err != nil {
			t.Fatal(err)
		}
		p, ok := domain.ParsePeriod(gStr(c["period"]))
		if !ok {
			t.Errorf("ParsePeriod(%q) refused a golden period", c["period"])
			continue
		}
		var got any
		if s := domain.PeriodStart(p, now); s != nil {
			got = s.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		if got != c["result"] {
			t.Errorf("PeriodStart(%q, %s) = %v, want %v", c["period"], c["now"], got, c["result"])
		}
	}
}
