package postgres_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/brief/adapters/postgres"
	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// fixture is a migrated database with two workspaces and a pull request of the first one.
type fixture struct {
	db    *gorm.DB
	store *postgres.Store
	ws    string
	other string
	repo  string
	pr    string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB)}
	f.ws = f.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = f.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.repo = f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', 'api', 'acme/api') RETURNING id`, f.ws)
	f.pr = f.pull(t, 1)
	return f
}

func (f fixture) insert(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := f.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (f fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := f.db.Exec(query, args...).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

func (f fixture) pull(t *testing.T, number int) string {
	t.Helper()
	return f.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, body)
		VALUES (?, ?, ?, 'Add rate limiting', 'dev', 'feat', 'main', 'a1b2c3d4e5f6', 'Fixes #12') RETURNING id`, f.ws, f.repo, number)
}

func (f fixture) agent(t *testing.T, name string) string {
	t.Helper()
	return f.insert(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
		VALUES (?, ?, 'openai', 'gpt', 'be careful') RETURNING id`, f.ws, name)
}

// review inserts a review; agent "" is NULL; createdAt is an RFC 3339 time.
func (f fixture) review(t *testing.T, agent, kind, createdAt string) string {
	t.Helper()
	var agentID *string
	if agent != "" {
		agentID = &agent
	}
	return f.insert(t, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, created_at)
		VALUES (?, ?, ?, ?, ?::timestamptz) RETURNING id`, f.ws, f.pr, agentID, kind, createdAt)
}

func (f fixture) finding(t *testing.T, review, file string, line int, title string, dismissed bool) {
	t.Helper()
	f.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence, dismissed_at)
		VALUES (?, ?, ?, ?, 'WARNING', 'bug', ?, 'r', 0.9, CASE WHEN ? THEN now() END)`, review, file, line, line+1, title, dismissed)
}

const unknownPR = "00000000-0000-4000-8000-000000000000"

func TestFindPull(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.FindPull(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatalf("FindPull() error = %v", err)
	}
	body := "Fixes #12"
	want := domain.Pull{
		ID: f.pr, WorkspaceID: f.ws, RepoID: f.repo, Owner: "acme", Name: "api", Provider: "github", Host: "github.com", Number: 1,
		Title: "Add rate limiting", Body: &body, Base: "main", HeadSHA: "a1b2c3d4e5f6",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindPull() mismatch (-want +got):\n%s", diff)
	}

	f.exec(t, `UPDATE pull_requests SET body = NULL WHERE id = ?`, f.pr)
	if got, _ := f.store.FindPull(t.Context(), f.ws, f.pr); got.Body != nil {
		t.Errorf("FindPull(NULL body).Body = %q, want nil", *got.Body)
	}
}

func TestFindPull_GitLabRepositoryReadsItsLocation(t *testing.T) { // FR29, FR18
	f := newFixture(t)
	f.exec(t, `UPDATE repos SET owner = 'acme/core', name = 'payments', full_name = 'acme/core/payments', provider = 'gitlab',
		host = 'git.example.com:8443', project_id = 42, web_url = 'https://git.example.com:8443/acme/core/payments' WHERE id = ?`, f.repo)
	got, err := f.store.FindPull(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatalf("FindPull() error = %v", err)
	}
	if got.Provider != "gitlab" || got.Host != "git.example.com:8443" || got.ProjectID != 42 || !got.IsGitLab() {
		t.Errorf("FindPull(GitLab) = %+v, want gitlab on git.example.com:8443 project 42", got)
	}
}

func TestFindPull_NotFound(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ name, ws, pr string }{
		{"other workspace", f.other, f.pr},
		{"unknown pull request", f.ws, unknownPR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.store.FindPull(t.Context(), tc.ws, tc.pr); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("FindPull(%s, %s) error = %v, want domain.ErrNotFound", tc.ws, tc.pr, err)
			}
		})
	}
}

func TestPullFiles(t *testing.T) {
	f := newFixture(t)
	f.exec(t, `INSERT INTO pr_files (pr_id, path, additions, deletions, patch) VALUES
		(?, 'src/b.go', 3, 1, '@@ -1 +1 @@'), (?, 'README.md', 1, 0, NULL), (?, 'src/a.go', 5, 5, NULL)`, f.pr, f.pr, f.pr)
	other := f.pull(t, 2)
	f.exec(t, `INSERT INTO pr_files (pr_id, path) VALUES (?, 'unrelated.go')`, other)

	got, err := f.store.PullFiles(t.Context(), f.pr)
	if err != nil {
		t.Fatalf("PullFiles() error = %v", err)
	}
	want := []domain.StoredFile{
		{Path: "README.md", Additions: 1},
		{Path: "src/a.go", Additions: 5, Deletions: 5},
		{Path: "src/b.go", Additions: 3, Deletions: 1, Patch: "@@ -1 +1 @@"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PullFiles() mismatch (-want +got):\n%s", diff)
	}
	if got, err := f.store.PullFiles(t.Context(), unknownPR); err != nil || len(got) != 0 {
		t.Errorf("PullFiles(no files) = %v, %v, want empty", got, err)
	}
}

// The newest review of each agent counts (NULL agents one group), summaries and older runs do not,
// dismissed findings do; reviews come newest first, findings by file, start line, id.
func TestCurrentReviews(t *testing.T) {
	f := newFixture(t)
	a1, a2 := f.agent(t, "security"), f.agent(t, "style")

	old1 := f.review(t, a1, "review", "2026-01-01T10:00:00Z")
	f.finding(t, old1, "old.go", 1, "replaced by the re-run", false)
	new1 := f.review(t, a1, "review", "2026-01-01T11:00:00Z")
	f.finding(t, new1, "b.go", 5, "second", false)
	f.finding(t, new1, "a.go", 9, "dismissed counts", true)
	f.finding(t, new1, "a.go", 3, "first", false)
	f.review(t, a2, "summary", "2026-01-02T00:00:00Z") // not a review: ignored
	nullOld := f.review(t, "", "review", "2026-01-01T09:00:00Z")
	f.finding(t, nullOld, "n.go", 1, "hidden by the newer null-agent review", false)
	nullNew := f.review(t, "", "review", "2026-01-01T12:00:00Z")
	f.finding(t, nullNew, "n.go", 2, "from a deleted agent", false)
	only2 := f.review(t, a2, "review", "2026-01-01T08:00:00Z")

	got, err := f.store.CurrentReviews(t.Context(), f.pr)
	if err != nil {
		t.Fatalf("CurrentReviews() error = %v", err)
	}
	warn := func(file string, start int, title string) domain.FindingFact {
		return domain.FindingFact{Severity: reviewer.SeverityWarning, File: file, StartLine: start, EndLine: start + 1, Title: title}
	}
	want := []domain.ReviewFacts{
		{ReviewID: nullNew, Findings: []domain.FindingFact{warn("n.go", 2, "from a deleted agent")}},
		{ReviewID: new1, AgentID: &a1, Findings: []domain.FindingFact{warn("a.go", 3, "first"), warn("a.go", 9, "dismissed counts"), warn("b.go", 5, "second")}},
		{ReviewID: only2, AgentID: &a2},
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("CurrentReviews() mismatch (-want +got):\n%s", diff)
	}
}

func TestCurrentReviews_TiesGoToTheSmallerID(t *testing.T) {
	f := newFixture(t)
	a1 := f.agent(t, "security")
	first := f.review(t, a1, "review", "2026-01-01T10:00:00Z")
	second := f.review(t, a1, "review", "2026-01-01T10:00:00Z")
	want := min(first, second)
	got, err := f.store.CurrentReviews(t.Context(), f.pr)
	if err != nil || len(got) != 1 || got[0].ReviewID != want {
		t.Errorf("CurrentReviews(tie) = %v, %v, want only review %s", got, err, want)
	}
}

func TestCurrentReviews_NoReviews(t *testing.T) {
	f := newFixture(t)
	if got, err := f.store.CurrentReviews(t.Context(), f.pr); err != nil || len(got) != 0 {
		t.Errorf("CurrentReviews(none) = %v, %v, want empty", got, err)
	}
}

func fullBrief() domain.Brief {
	detail := "ticket not found"
	changeType, confidence := "feature", "high"
	return domain.Brief{
		Summary: "Adds a limiter.",
		Risks: []domain.Risk{{Kind: "security", Title: "Header trusted", Explanation: "Spoofable key.",
			Severity: domain.RiskHigh, FileRefs: []string{"src/limit.ts:6-9", "src/config.ts:12"}}},
		ReviewFocus: []domain.FocusItem{{File: "src/limit.ts", Line: 7, Reason: "Key from a header."}},
		Intent: &reviewer.Intent{
			Text: "Rate limit", InScope: []string{"a"}, OutOfScope: []string{}, ChangeType: &changeType, Confidence: &confidence,
			DerivedFrom: "explicit",
			Sources:     []reviewer.IntentSource{{Kind: "title", Ref: "title", Status: "used"}, {Kind: "ticket", Ref: "#12", Status: "failed", Detail: &detail}},
		},
		Blast: &domain.Blast{
			ChangedSymbols: []domain.BlastSymbol{{Name: "limit", File: "src/limit.ts", Kind: "function"}},
			Downstream: []domain.BlastGroup{{
				Symbol: "limit", Callers: []domain.BlastCaller{{Name: "register", File: "src/server.ts", Line: 22}},
				EndpointsAffected: []string{"GET /health"}, CronsAffected: []string{},
			}},
			Summary: "1 changed symbol",
		},
		MissingInputs: []domain.MissingInput{
			{Input: domain.InputSpecs, Reason: domain.ReasonUnreadable, Detail: "1 of 4"},
			{Input: domain.InputLinkedIssue, Reason: domain.ReasonNotLinked},
		},
		SpecsUsed:     []string{"docs/a.md"},
		HeadSHA:       "0123456789abcdef0123456789abcdef01234567",
		GeneratedAt:   time.Date(2026, 1, 1, 12, 0, 0, 123_000_000, time.UTC),
		PromptVersion: "v1",
		Provider:      "openai",
		Model:         "gpt-4.1",
		TokensIn:      1200,
		TokensOut:     180,
		CostUSD:       ptr(0.00042),
		ModelRequests: 2,
	}
}

func ptr[T any](v T) *T { return &v }

func TestSaveBrief_RoundTrip(t *testing.T) {
	f := newFixture(t)
	want := fullBrief()
	if err := f.store.SaveBrief(t.Context(), f.pr, want); err != nil {
		t.Fatalf("SaveBrief() error = %v", err)
	}
	got, ok, err := f.store.Brief(t.Context(), f.pr)
	if err != nil || !ok {
		t.Fatalf("Brief() = _, %v, %v, want the stored brief", ok, err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Brief() mismatch (-want +got):\n%s", diff)
	}

	var raw string
	f.db.Raw(`SELECT json::text FROM pr_brief WHERE pr_id = ?`, f.pr).Scan(&raw)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("stored document is not an object: %v", err)
	}
	for _, key := range []string{"summary", "risks", "review_focus", "intent", "blast", "missing_inputs", "specs_used", "head_sha",
		"generated_at", "prompt_version", "provider", "model", "tokens_in", "tokens_out", "cost_usd", "model_requests"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("stored document lacks %q", key)
		}
	}
	if _, ok := doc["history"]; ok {
		t.Error("stored document has history")
	}
	if !strings.Contains(string(doc["generated_at"]), `"2026-01-01T12:00:00.123Z"`) {
		t.Errorf("generated_at = %s, want ISO-8601 UTC with milliseconds", doc["generated_at"])
	}
}

func TestSaveBrief_EmptyBriefStoresListsNotNulls(t *testing.T) {
	f := newFixture(t)
	b := domain.Brief{Summary: "S", HeadSHA: "abc", GeneratedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PromptVersion: "v1", Provider: "p", Model: "m", ModelRequests: 1}
	if err := f.store.SaveBrief(t.Context(), f.pr, b); err != nil {
		t.Fatalf("SaveBrief() error = %v", err)
	}
	var raw string
	f.db.Raw(`SELECT json::text FROM pr_brief WHERE pr_id = ?`, f.pr).Scan(&raw)
	for _, want := range []string{`"risks": {"risks": []}`, `"review_focus": []`, `"missing_inputs": []`, `"specs_used": []`, `"intent": null`, `"blast": null`, `"cost_usd": null`} {
		if !strings.Contains(raw, want) {
			t.Errorf("stored document = %s, want it to contain %s", raw, want)
		}
	}
	got, ok, err := f.store.Brief(t.Context(), f.pr)
	if err != nil || !ok {
		t.Fatalf("Brief() = %v, %v, want the brief", ok, err)
	}
	if got.Risks == nil || got.ReviewFocus == nil || got.MissingInputs == nil || got.SpecsUsed == nil {
		t.Errorf("Brief() lists = %+v, want non-nil empty lists", got)
	}
}

func TestSaveBrief_UpsertReplaces(t *testing.T) {
	f := newFixture(t)
	first := fullBrief()
	second := fullBrief()
	second.Summary = "second"
	for _, b := range []domain.Brief{first, second} {
		if err := f.store.SaveBrief(t.Context(), f.pr, b); err != nil {
			t.Fatalf("SaveBrief() error = %v", err)
		}
	}
	var rows int64
	f.db.Raw(`SELECT count(*) FROM pr_brief WHERE pr_id = ?`, f.pr).Scan(&rows)
	got, _, _ := f.store.Brief(t.Context(), f.pr)
	if rows != 1 || got.Summary != "second" {
		t.Errorf("after two saves: %d rows, summary %q, want 1 row holding the second", rows, got.Summary)
	}
}

func TestSaveBrief_NULInTextIsSanitised(t *testing.T) {
	f := newFixture(t)
	b := fullBrief()
	b.Summary = "bad\x00summary"
	b.Risks[0].Title = "t\x00"
	b.Intent.Text = "intent\x00"
	b.Blast.Summary = "blast\x00"
	b.ReviewFocus[0].Reason = "r\xffx"
	if err := f.store.SaveBrief(t.Context(), f.pr, b); err != nil {
		t.Fatalf("SaveBrief(NUL) error = %v, want the text stored sanitised", err)
	}
	got, ok, err := f.store.Brief(t.Context(), f.pr)
	if err != nil || !ok {
		t.Fatalf("Brief() = %v, %v", ok, err)
	}
	if got.Summary != "bad�summary" || got.Risks[0].Title != "t�" || got.Intent.Text != "intent�" ||
		got.Blast.Summary != "blast�" || got.ReviewFocus[0].Reason != "r�x" {
		t.Errorf("Brief() = %q %q %q %q %q, want U+FFFD for the NUL and the invalid byte",
			got.Summary, got.Risks[0].Title, got.Intent.Text, got.Blast.Summary, got.ReviewFocus[0].Reason)
	}
}

func TestSaveBrief_UnknownPullRequestFails(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SaveBrief(t.Context(), unknownPR, fullBrief()); err == nil {
		t.Error("SaveBrief(unknown pull request) error = nil, want the foreign key failure")
	}
}

func TestBrief_Absent(t *testing.T) {
	f := newFixture(t)
	if _, ok, err := f.store.Brief(t.Context(), f.pr); ok || err != nil {
		t.Errorf("Brief(none) = _, %v, %v, want absent without an error", ok, err)
	}
}

// The stored cases of the Node golden (PrBrief.safeParse of stored documents): a document that
// fails the contract reads as absent, one that passes keeps its values. generated_at is replaced
// by a real timestamp first: domain.Brief holds a time, so a non-timestamp string is absent here
// whatever else the document says (TestBrief_NonTimestampIsAbsent); this keeps every case about
// its own rule.
func TestBrief_ContractGolden(t *testing.T) {
	raw, err := os.ReadFile("../../../parity/testdata/goldens/brief.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden struct {
		Stored []struct {
			Name   string          `json:"name"`
			Doc    json.RawMessage `json:"doc"`
			OK     bool            `json:"ok"`
			Parsed json.RawMessage `json:"parsed"`
		} `json:"stored"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(golden.Stored) < 10 {
		t.Fatalf("golden holds %d stored cases, want the whole list", len(golden.Stored))
	}
	f := newFixture(t)
	for _, tc := range golden.Stored {
		t.Run(tc.Name, func(t *testing.T) {
			doc := withTimestamp(t, tc.Doc)
			f.exec(t, `INSERT INTO pr_brief (pr_id, json) VALUES (?, ?::jsonb) ON CONFLICT (pr_id) DO UPDATE SET json = EXCLUDED.json`, f.pr, string(doc))
			got, ok, err := f.store.Brief(t.Context(), f.pr)
			if err != nil {
				t.Fatalf("Brief() error = %v, want none: a stored shape never errors", err)
			}
			if ok != tc.OK {
				t.Fatalf("Brief() ok = %v, want %v", ok, tc.OK)
			}
			if !ok {
				return
			}
			var want map[string]any
			if err := json.Unmarshal(withTimestamp(t, tc.Parsed), &want); err != nil {
				t.Fatalf("decode parsed: %v", err)
			}
			// History is not part of the Go brief; the other keys are the contract's.
			delete(want, "history")
			if diff := cmp.Diff(want, wire(t, got), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Brief() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// withTimestamp sets generated_at of an object document to a valid timestamp; other documents
// stay as they are.
func withTimestamp(t *testing.T, doc json.RawMessage) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if json.Unmarshal(doc, &m) != nil || m == nil {
		return doc
	}
	if _, ok := m["generated_at"]; ok {
		m["generated_at"] = json.RawMessage(`"2026-01-01T00:00:00.000Z"`)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("encode document: %v", err)
	}
	return out
}

// wire renders a brief the way the contract spells it, for comparing with the golden.
func wire(t *testing.T, b domain.Brief) map[string]any {
	t.Helper()
	f := newFixture(t) // a store round trip yields the stored document back, key by key
	if err := f.store.SaveBrief(t.Context(), f.pr, b); err != nil {
		t.Fatalf("SaveBrief() error = %v", err)
	}
	var raw string
	f.db.Raw(`SELECT json::text FROM pr_brief WHERE pr_id = ?`, f.pr).Scan(&raw)
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode stored: %v", err)
	}
	return out
}

func TestBrief_NonTimestampIsAbsent(t *testing.T) {
	f := newFixture(t)
	doc := `{"summary":"S","risks":{"risks":[]},"review_focus":[],"intent":null,"blast":null,"missing_inputs":[],"specs_used":[],"head_sha":"abc","generated_at":"yesterday","prompt_version":"v1","provider":"p","model":"m","tokens_in":1,"tokens_out":2,"cost_usd":null,"model_requests":1}`
	f.exec(t, `INSERT INTO pr_brief (pr_id, json) VALUES (?, ?::jsonb)`, f.pr, doc)
	if _, ok, err := f.store.Brief(t.Context(), f.pr); ok || err != nil {
		t.Errorf("Brief(generated_at \"yesterday\") = _, %v, %v, want absent", ok, err)
	}
}

// Whole numbers written the JavaScript way (1.0, 1e3) satisfy the contract's integers.
func TestBrief_IntegersWrittenAsFloatsPass(t *testing.T) {
	f := newFixture(t)
	doc := `{"summary":"S","risks":{"risks":[]},"review_focus":[{"file":"a","line":3.0,"reason":"r"}],"intent":null,"blast":null,"missing_inputs":[],"specs_used":[],"head_sha":"abc","generated_at":"2026-01-01T00:00:00.000Z","prompt_version":"v1","provider":"p","model":"m","tokens_in":1e3,"tokens_out":2.0,"cost_usd":0.5,"model_requests":2.0}`
	f.exec(t, `INSERT INTO pr_brief (pr_id, json) VALUES (?, ?::jsonb)`, f.pr, doc)
	got, ok, err := f.store.Brief(t.Context(), f.pr)
	if err != nil || !ok {
		t.Fatalf("Brief() = %v, %v, want the brief", ok, err)
	}
	if got.TokensIn != 1000 || got.TokensOut != 2 || got.ModelRequests != 2 || got.ReviewFocus[0].Line != 3 {
		t.Errorf("Brief() = %+v, want tokens 1000/2, 2 requests, line 3", got)
	}
}
