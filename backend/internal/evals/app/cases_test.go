package app_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

const goodDiff = "diff --git a/src/a.ts b/src/a.ts\n--- a/src/a.ts\n+++ b/src/a.ts\n@@ -8,3 +8,5 @@\n const a = 1;\n+const b = 2;\n+const c = 3;\n const d = 4;\n const e = 5;"

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }

func decidedFinding() domain.FindingSource {
	now := epoch
	return domain.FindingSource{
		FindingID: "f-1", ReviewID: "r-1", PRID: "p-1", AgentID: sp("a-1"), AcceptedAt: &now,
		File: "src/a.ts", StartLine: 10, EndLine: 12, Severity: "WARNING", Category: "bug", Title: "Null dereference",
		Patch:   sp("@@ -8,3 +8,5 @@\n const a = 1;\n+const b = 2;\n+const c = 3;\n const d = 4;\n const e = 5;"),
		PRTitle: "Fix the parser", PRBody: sp("Closes the crash."), RepoFullName: "acme/api", PRNumber: 7, HeadSHA: "abc123",
	}
}

type caseEnv struct {
	svc      *app.CaseService
	store    *fakeCases
	findings fakeFindings
	logs     func() []map[string]any
}

func newCaseEnv(t *testing.T) *caseEnv {
	t.Helper()
	log, logs := logCapture(t)
	store := newFakeCases()
	store.addOwner(domain.OwnerAgent, "a-1")
	store.addOwner(domain.OwnerSkill, "s-1")
	findings := fakeFindings{"f-1": decidedFinding()}
	return &caseEnv{svc: app.NewCaseService(store, findings, fixedClock(), log), store: store, findings: findings, logs: logs}
}

func manualInput() domain.CreateInput {
	return domain.CreateInput{
		Name:        "Manual case",
		Expectation: domain.Expectation{Kind: domain.MustFind, File: "src/a.ts", StartLine: 10, EndLine: 12},
		InputDiff:   goodDiff,
	}
}

func TestFromFinding(t *testing.T) {
	env := newCaseEnv(t)
	got, err := env.svc.FromFinding(t.Context(), ws, "f-1")
	if err != nil {
		t.Fatalf("FromFinding() error = %v", err)
	}
	c := got.Case
	if c.OwnerKind != domain.OwnerAgent || c.OwnerID != "a-1" || c.Name != "Null dereference" || c.Expectation.Kind != domain.MustFind {
		t.Errorf("FromFinding() case = %+v, want an agent-owned must_find case named after the title", c)
	}
	if c.SourceFindingID == nil || *c.SourceFindingID != "f-1" || got.LastResult != nil {
		t.Errorf("FromFinding() sources/last result = %v/%v, want finding f-1 and no result", c.SourceFindingID, got.LastResult)
	}
	logs := env.logs()
	if len(logs) != 1 || logs[0]["msg"] != "eval case created" || logs[0]["caseId"] != c.ID ||
		logs[0]["ownerKind"] != "agent" || logs[0]["ownerId"] != "a-1" || logs[0]["diffLength"] != float64(len(c.InputDiff)) {
		t.Errorf("log lines = %v, want one 'eval case created' with ids and diffLength", logs)
	}
	for _, v := range logs[0] {
		if s, ok := v.(string); ok && strings.Contains(s, "const") {
			t.Errorf("log line %v leaks diff content", logs[0])
		}
	}
}

func TestFromFinding_Dismissed(t *testing.T) {
	env := newCaseEnv(t)
	src := decidedFinding()
	src.AcceptedAt, src.DismissedAt = nil, &epoch
	env.findings["f-1"] = src
	got, err := env.svc.FromFinding(t.Context(), ws, "f-1")
	if err != nil {
		t.Fatalf("FromFinding() error = %v", err)
	}
	if got.Case.Expectation.Kind != domain.MustNotFlag {
		t.Errorf("FromFinding() kind = %q, want must_not_flag", got.Case.Expectation.Kind)
	}
}

func TestFromFinding_Errors(t *testing.T) {
	long := strings.Repeat("x", domain.DiffMaxChars+1)
	tests := []struct {
		name    string
		mutate  func(*domain.FindingSource)
		finding string
		kind    apperr.Kind
		code    string
		message string
	}{
		{"unknown finding", nil, "nope", apperr.KindNotFound, "not_found", "Finding not found"},
		{"undecided", func(s *domain.FindingSource) { s.AcceptedAt = nil }, "f-1", apperr.KindValidation, "finding_undecided", "Accept or dismiss the finding first"},
		{"no agent", func(s *domain.FindingSource) { s.AgentID = nil }, "f-1", apperr.KindValidation, "finding_no_agent", "The review of this finding has no agent"},
		{"no patch", func(s *domain.FindingSource) { s.Patch = nil }, "f-1", apperr.KindValidation, "patch_unavailable", "The PR has no stored patch for this file"},
		{"blank patch", func(s *domain.FindingSource) { s.Patch = sp(" \n\t") }, "f-1", apperr.KindValidation, "patch_unavailable", "The PR has no stored patch for this file"},
		{"patch too large", func(s *domain.FindingSource) { s.Patch = &long }, "f-1", apperr.KindValidation, "patch_too_large", "The patch exceeds 200000 characters"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newCaseEnv(t)
			if tc.mutate != nil {
				src := decidedFinding()
				tc.mutate(&src)
				env.findings["f-1"] = src
			}
			_, err := env.svc.FromFinding(t.Context(), ws, tc.finding)
			wantApp(t, err, tc.kind, tc.code, tc.message)
			if len(env.store.cases) != 0 {
				t.Errorf("FromFinding() stored %d cases on failure, want none", len(env.store.cases))
			}
		})
	}
}

func TestFromFinding_ExistingCaseWinsOverTheDecision(t *testing.T) {
	env := newCaseEnv(t)
	env.store.add(domain.Case{ID: "case-existing", OwnerKind: domain.OwnerAgent, OwnerID: "a-1", SourceFindingID: sp("f-1")})
	src := decidedFinding()
	src.AcceptedAt = nil // undecided now
	env.findings["f-1"] = src
	_, err := env.svc.FromFinding(t.Context(), ws, "f-1")
	ae := wantApp(t, err, apperr.KindConflict, "eval_case_exists", "An eval case already exists for this finding")
	if got := detailsJSON(t, ae); got != `{"case_id":"case-existing"}` {
		t.Errorf("details = %s, want the existing case id", got)
	}
}

func TestFromFinding_Limit(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		wantErr bool
	}{{"one below the limit", 199, false}, {"at the limit", 200, true}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newCaseEnv(t)
			env.store.counts = map[ownerKey]int{{domain.OwnerAgent, "a-1"}: tc.count}
			_, err := env.svc.FromFinding(t.Context(), ws, "f-1")
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("FromFinding() error = %v", err)
				}
				return
			}
			wantApp(t, err, apperr.KindValidation, "eval_case_limit", "An agent or skill can have at most 200 eval cases")
		})
	}
}

func TestFromFinding_LostRace(t *testing.T) {
	env := newCaseEnv(t)
	env.store.raceWinner = "case-winner"
	_, err := env.svc.FromFinding(t.Context(), ws, "f-1")
	ae := wantApp(t, err, apperr.KindConflict, "eval_case_exists", "An eval case already exists for this finding")
	if got := detailsJSON(t, ae); got != `{"case_id":"case-winner"}` {
		t.Errorf("details = %s, want the winning case id", got)
	}
	if n := len(env.logs()); n != 0 {
		t.Errorf("log lines = %d, want none for a lost race", n)
	}
}

func TestFromFinding_StoreFailureIsNotAnAppError(t *testing.T) {
	env := newCaseEnv(t)
	env.store.err = errBoom
	_, err := env.svc.FromFinding(t.Context(), ws, "f-1")
	if !errors.Is(err, errBoom) {
		t.Fatalf("FromFinding() error = %v, want it to wrap the store error", err)
	}
	if _, ok := apperr.As(err); ok {
		t.Errorf("FromFinding() error %v is an apperr, want a plain error (500 at the edge)", err)
	}
}

func TestCreate(t *testing.T) {
	tests := []struct {
		name string
		kind domain.OwnerKind
		id   string
	}{{"agent owner", domain.OwnerAgent, "a-1"}, {"skill owner", domain.OwnerSkill, "s-1"}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newCaseEnv(t)
			in := manualInput()
			in.PRTitle = sp("  ")
			in.PRDescription = sp("")
			got, err := env.svc.Create(t.Context(), ws, tc.kind, tc.id, in)
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			c := got.Case
			if c.OwnerKind != tc.kind || c.OwnerID != tc.id || c.Name != "Manual case" || c.InputMeta.PRTitle != "Manual case" ||
				c.InputMeta.PRDescription != nil || c.SourceFindingID != nil || c.InputDiff != goodDiff {
				t.Errorf("Create() case = %+v, want the manual case snapshot", c)
			}
			if logs := env.logs(); len(logs) != 1 || logs[0]["ownerKind"] != string(tc.kind) {
				t.Errorf("log lines = %v, want one 'eval case created' for %s", logs, tc.kind)
			}
		})
	}
}

func TestCreate_WrapsABareHunk(t *testing.T) {
	env := newCaseEnv(t)
	in := manualInput()
	in.InputDiff = "@@ -8,3 +8,5 @@\r\n const a = 1;\r\n+const b = 2;\r\n+const c = 3;\r\n const d = 4;\r\n const e = 5;"
	got, err := env.svc.Create(t.Context(), ws, domain.OwnerAgent, "a-1", in)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if want := domain.BuildSingleFileDiff("src/a.ts", strings.ReplaceAll(in.InputDiff, "\r\n", "\n")); got.Case.InputDiff != want {
		t.Errorf("Create() diff = %q, want %q", got.Case.InputDiff, want)
	}
}

func TestCreate_Order(t *testing.T) {
	t.Run("unknown owner before the content", func(t *testing.T) {
		env := newCaseEnv(t)
		in := manualInput()
		in.InputDiff = "garbage"
		_, err := env.svc.Create(t.Context(), ws, domain.OwnerAgent, "nope", in)
		wantApp(t, err, apperr.KindNotFound, "not_found", "Agent not found")
		_, err = env.svc.Create(t.Context(), ws, domain.OwnerSkill, "nope", in)
		wantApp(t, err, apperr.KindNotFound, "not_found", "Skill not found")
	})
	t.Run("content before the limit", func(t *testing.T) {
		env := newCaseEnv(t)
		env.store.counts = map[ownerKey]int{{domain.OwnerAgent, "a-1"}: 200}
		in := manualInput()
		in.InputDiff = "garbage"
		_, err := env.svc.Create(t.Context(), ws, domain.OwnerAgent, "a-1", in)
		wantApp(t, err, apperr.KindValidation, "diff_invalid", "The diff has no file with a hunk (@@ header)")
		_, err = env.svc.Create(t.Context(), ws, domain.OwnerAgent, "a-1", manualInput())
		wantApp(t, err, apperr.KindValidation, "eval_case_limit", "An agent or skill can have at most 200 eval cases")
	})
}

func TestCreate_ContentErrors(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*domain.CreateInput)
		code        string
		message     string
		wantDetails string
	}{
		{"too large", func(in *domain.CreateInput) { in.InputDiff = strings.Repeat("x", domain.DiffMaxChars+1) },
			"patch_too_large", "The diff exceeds 200000 characters", ""},
		{"no hunk", func(in *domain.CreateInput) { in.InputDiff = "diff --git a/x b/x\n--- a/x\n+++ b/x\n" },
			"diff_invalid", "The diff has no file with a hunk (@@ header)", ""},
		{"file not in the diff", func(in *domain.CreateInput) { in.Expectation.File = "other.ts" },
			"expectation_file_not_in_diff", "The expectation file is not among the diff files", `{"files":["src/a.ts"]}`},
		{"lines not in the diff", func(in *domain.CreateInput) { in.Expectation.StartLine, in.Expectation.EndLine = 100, 101 },
			"expectation_lines_not_in_diff", "The expectation lines do not intersect a changed or context line of the file",
			`{"ranges":[{"start":8,"end":12}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newCaseEnv(t)
			in := manualInput()
			tc.mutate(&in)
			_, err := env.svc.Create(t.Context(), ws, domain.OwnerAgent, "a-1", in)
			ae := wantApp(t, err, apperr.KindValidation, tc.code, tc.message)
			if tc.wantDetails == "" {
				if ae.Details != nil {
					t.Errorf("details = %v, want none", ae.Details)
				}
			} else if got := detailsJSON(t, ae); got != tc.wantDetails {
				t.Errorf("details = %s, want %s", got, tc.wantDetails)
			}
		})
	}
}

func TestList(t *testing.T) {
	env := newCaseEnv(t)
	got, err := env.svc.List(t.Context(), ws, domain.OwnerAgent, "a-1")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("List(empty) = %v, %v, want an empty non-nil list", got, err)
	}
	env.store.add(domain.Case{OwnerKind: domain.OwnerSkill, OwnerID: "s-1", Name: "skill case"})
	got, err = env.svc.List(t.Context(), ws, domain.OwnerSkill, "s-1")
	if err != nil || len(got) != 1 || got[0].Case.Name != "skill case" {
		t.Fatalf("List(skill) = %v, %v, want the skill's case", got, err)
	}
	_, err = env.svc.List(t.Context(), ws, domain.OwnerAgent, "nope")
	wantApp(t, err, apperr.KindNotFound, "not_found", "Agent not found")
	_, err = env.svc.List(t.Context(), ws, domain.OwnerSkill, "nope")
	wantApp(t, err, apperr.KindNotFound, "not_found", "Skill not found")
}

func storedCase(env *caseEnv) domain.Case {
	return env.store.add(domain.Case{
		OwnerKind: domain.OwnerAgent, OwnerID: "a-1", Name: "Stored", Notes: sp("note"),
		Expectation: domain.Expectation{Kind: domain.MustFind, File: "src/a.ts", StartLine: 10, EndLine: 12, Severity: sp("WARNING")},
		InputDiff:   goodDiff, InputMeta: domain.InputMeta{PRTitle: "PR", PRDescription: sp("desc")}, CreatedAt: epoch,
	})
}

func TestUpdate_UnknownCase(t *testing.T) {
	env := newCaseEnv(t)
	_, err := env.svc.Update(t.Context(), ws, "nope", domain.UpdateInput{})
	wantApp(t, err, apperr.KindNotFound, "not_found", "Eval case not found")
}

func TestUpdate_NameOnlyIsNotAContentEdit(t *testing.T) {
	env := newCaseEnv(t)
	c := storedCase(env)
	env.store.last[c.ID] = &domain.LastResult{RunID: "run-9", Status: domain.CasePassed, RanAt: epoch}
	// A stored diff that would fail the re-check proves that a metadata edit does not run it.
	env.store.cases[0].InputDiff = "garbage"
	got, err := env.svc.Update(t.Context(), ws, c.ID, domain.UpdateInput{Name: sp("Renamed")})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got.Case.Name != "Renamed" || got.Case.ContentEditedAt != nil {
		t.Errorf("Update() name/content_edited_at = %q/%v, want Renamed/nil", got.Case.Name, got.Case.ContentEditedAt)
	}
	if got.LastResult == nil || got.LastResult.RunID != "run-9" {
		t.Errorf("Update() last result = %v, want the case's last result", got.LastResult)
	}
}

func TestUpdate_EmptyPatchChangesNothing(t *testing.T) {
	env := newCaseEnv(t)
	c := storedCase(env)
	got, err := env.svc.Update(t.Context(), ws, c.ID, domain.UpdateInput{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if env.store.updates != 1 || env.store.writes != 0 {
		t.Errorf("store updates/writes = %d/%d, want 1/0", env.store.updates, env.store.writes)
	}
	if diff := cmp.Diff(c, got.Case); diff != "" {
		t.Errorf("Update({}) case mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdate_ContentEditPassesRecheck(t *testing.T) {
	env := newCaseEnv(t)
	c := storedCase(env)
	got, err := env.svc.Update(t.Context(), ws, c.ID, domain.UpdateInput{StartLine: ip(9), EndLine: ip(11), Severity: domain.Field[string]{Set: true}})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	e := got.Case.Expectation
	if e.StartLine != 9 || e.EndLine != 11 || e.Severity != nil {
		t.Errorf("Update() expectation = %+v, want lines 9-11 and severity cleared", e)
	}
	if got.Case.ContentEditedAt == nil || !got.Case.ContentEditedAt.Equal(epoch) {
		t.Errorf("Update() content_edited_at = %v, want %v", got.Case.ContentEditedAt, epoch)
	}
}

func TestUpdate_FailedRecheckWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		in   domain.UpdateInput
		code string
	}{
		{"file not in the diff", domain.UpdateInput{File: sp("other.ts")}, "expectation_file_not_in_diff"},
		{"lines not in the diff", domain.UpdateInput{StartLine: ip(100), EndLine: ip(101)}, "expectation_lines_not_in_diff"},
		{"new diff without a hunk", domain.UpdateInput{InputDiff: sp("nothing here")}, "diff_invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newCaseEnv(t)
			c := storedCase(env)
			_, err := env.svc.Update(t.Context(), ws, c.ID, tc.in)
			ae, ok := apperr.As(err)
			if !ok || ae.Kind != apperr.KindValidation || ae.Code != tc.code {
				t.Fatalf("Update() error = %v, want a 422 %s", err, tc.code)
			}
			if env.store.writes != 0 {
				t.Errorf("store writes = %d, want 0", env.store.writes)
			}
		})
	}
}

func TestUpdate_ReversedLinesAreBadRequest(t *testing.T) {
	env := newCaseEnv(t)
	c := storedCase(env)
	// Only one bound changes: the merged values are validated.
	_, err := env.svc.Update(t.Context(), ws, c.ID, domain.UpdateInput{EndLine: ip(5)})
	wantApp(t, err, apperr.KindInvalidInput, "validation_error", "end_line must be >= start_line")
}

func TestUpdate_NullClearsAndBlankTitleFallsBackToName(t *testing.T) {
	env := newCaseEnv(t)
	c := storedCase(env)
	got, err := env.svc.Update(t.Context(), ws, c.ID, domain.UpdateInput{
		Notes:         domain.Field[string]{Set: true},
		PRTitle:       domain.Field[string]{Set: true},
		PRDescription: domain.Field[string]{Set: true},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got.Case.Notes != nil || got.Case.InputMeta.PRTitle != "Stored" || got.Case.InputMeta.PRDescription != nil {
		t.Errorf("Update() = notes %v, pr_title %q, pr_description %v, want nil/Stored/nil",
			got.Case.Notes, got.Case.InputMeta.PRTitle, got.Case.InputMeta.PRDescription)
	}
}

func TestDelete(t *testing.T) {
	env := newCaseEnv(t)
	c := storedCase(env)
	if err := env.svc.Delete(t.Context(), ws, c.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(env.store.cases) != 0 {
		t.Errorf("Delete() left %d cases", len(env.store.cases))
	}
	wantApp(t, env.svc.Delete(t.Context(), ws, c.ID), apperr.KindNotFound, "not_found", "Eval case not found")
}
