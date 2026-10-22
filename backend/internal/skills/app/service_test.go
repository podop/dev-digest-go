package app_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
)

type env struct {
	w   *world
	svc *app.Service
}

func newEnv(t *testing.T) env {
	t.Helper()
	w := newWorld()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	return env{w: w, svc: app.NewService(&memStore{w: w}, &memTx{w: w}, func() time.Time { return now })}
}

// wantAppErr checks that err is an application error of the given kind, code and message.
func wantAppErr(t *testing.T, err error, kind apperr.Kind, code, message string) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v, want an apperr", err)
	}
	if ae.Kind != kind || ae.Code != code || ae.Message != message {
		t.Errorf("error = {%v %q %q}, want {%v %q %q}", ae.Kind, ae.Code, ae.Message, kind, code, message)
	}
	return ae
}

func str(s string) *string                     { return &s }
func flag(b bool) *bool                        { return &b }
func num(n int) *int                           { return &n }
func typ(t domain.SkillType) *domain.SkillType { return &t }

func (e env) create(t *testing.T, name string) domain.Skill {
	t.Helper()
	sk, err := e.svc.Create(t.Context(), wsID, app.CreateInput{Name: name, Description: "d", Type: domain.TypeCustom, Body: "b"})
	if err != nil {
		t.Fatalf("Create(%q) error = %v", name, err)
	}
	return sk
}

func versionMessages(t *testing.T, e env, id string) []string {
	t.Helper()
	vs, err := e.svc.ListVersions(t.Context(), wsID, id)
	if err != nil {
		t.Fatalf("ListVersions error = %v", err)
	}
	var out []string
	for _, v := range vs {
		out = append(out, *v.Message)
	}
	return out
}

func TestService_Create(t *testing.T) {
	tests := []struct {
		name        string
		in          app.CreateInput
		wantEnabled bool
		wantSource  domain.Source
		wantRef     *string
		wantMessage string
	}{
		{
			name: "manual defaults", in: app.CreateInput{Name: "x", Type: domain.TypeCustom, Body: "b"},
			wantEnabled: true, wantSource: domain.SourceManual, wantMessage: "Created",
		},
		{
			name: "manual keeps enabled false", in: app.CreateInput{Name: "x", Type: domain.TypeCustom, Body: "b", Enabled: flag(false)},
			wantEnabled: false, wantSource: domain.SourceManual, wantMessage: "Created",
		},
		{
			name:        "manual drops source_ref",
			in:          app.CreateInput{Name: "x", Type: domain.TypeCustom, Body: "b", SourceRef: str("https://h/x.md")},
			wantEnabled: true, wantSource: domain.SourceManual, wantMessage: "Created",
		},
		{
			name: "imported url is disabled whatever was sent",
			in: app.CreateInput{
				Name: "x", Type: domain.TypeCustom, Body: "b", Enabled: flag(true), Source: domain.SourceImportedURL, SourceRef: str("https://h/x.md"),
			},
			wantEnabled: false, wantSource: domain.SourceImportedURL, wantRef: str("https://h/x.md"), wantMessage: "Imported from https://h/x.md",
		},
		{
			name:        "imported without a ref",
			in:          app.CreateInput{Name: "x", Type: domain.TypeCustom, Body: "b", Source: domain.SourceCommunity},
			wantEnabled: false, wantSource: domain.SourceCommunity, wantMessage: "Created",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			got, err := e.svc.Create(t.Context(), wsID, tc.in)
			if err != nil {
				t.Fatalf("Create error = %v", err)
			}
			if got.Enabled != tc.wantEnabled || got.Source != tc.wantSource || got.Version != 1 || got.UsedBy != 0 {
				t.Errorf("Create = enabled %v source %q version %d used_by %d, want %v %q 1 0",
					got.Enabled, got.Source, got.Version, got.UsedBy, tc.wantEnabled, tc.wantSource)
			}
			if diff := cmp.Diff(tc.wantRef, got.SourceRef); diff != "" {
				t.Errorf("Create source_ref mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff([]string{tc.wantMessage}, versionMessages(t, e, got.ID)); diff != "" {
				t.Errorf("Create snapshot messages mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestService_CreateDuplicateNameIsConflict(t *testing.T) {
	e := newEnv(t)
	e.create(t, "x")
	_, err := e.svc.Create(t.Context(), wsID, app.CreateInput{Name: "x", Type: domain.TypeCustom, Body: "b"})
	wantAppErr(t, err, apperr.KindConflict, "conflict", `A skill named "x" already exists in this workspace`)
	// The same name in another workspace is fine.
	if _, err := e.svc.Create(t.Context(), otherWS, app.CreateInput{Name: "x", Type: domain.TypeCustom, Body: "b"}); err != nil {
		t.Errorf("Create in another workspace error = %v, want nil", err)
	}
}

func TestService_CreateRollsBackWhenTheSnapshotFails(t *testing.T) {
	e := newEnv(t)
	e.w.failInsertVersion = errBoom
	if _, err := e.svc.Create(t.Context(), wsID, app.CreateInput{Name: "x", Type: domain.TypeCustom, Body: "b"}); err == nil {
		t.Fatal("Create error = nil, want the snapshot failure")
	}
	if got, _ := e.svc.List(t.Context(), wsID); len(got) != 0 {
		t.Errorf("List after failed Create = %d skills, want 0", len(got))
	}
}

func TestService_GetIsWorkspaceScoped(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	_, err := e.svc.Get(t.Context(), otherWS, sk.ID)
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill not found")
}

func TestService_UpdateVersioning(t *testing.T) {
	tests := []struct {
		name        string
		patch       domain.Patch
		wantVersion int
		wantMessage string // "" = no new snapshot
	}{
		{"body and description", domain.Patch{Body: str("b2"), Description: str("d2")}, 2, "Edited body and description"},
		{"body", domain.Patch{Body: str("b2")}, 2, "Edited body"},
		{"description", domain.Patch{Description: str("d2")}, 2, "Edited description"},
		{"same body", domain.Patch{Body: str("b")}, 1, ""},
		{"same body and description", domain.Patch{Body: str("b"), Description: str("d")}, 1, ""},
		{"name type enabled", domain.Patch{Name: str("y"), Type: typ(domain.TypeRubric), Enabled: flag(false)}, 1, ""},
		{"empty patch", domain.Patch{}, 1, ""},
		{"matching base_version", domain.Patch{Body: str("b2"), BaseVersion: num(1)}, 2, "Edited body"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			sk := e.create(t, "x")
			got, err := e.svc.Update(t.Context(), wsID, sk.ID, tc.patch)
			if err != nil {
				t.Fatalf("Update error = %v", err)
			}
			if got.Version != tc.wantVersion {
				t.Errorf("Update version = %d, want %d", got.Version, tc.wantVersion)
			}
			want := []string{"Created"}
			if tc.wantMessage != "" {
				want = []string{tc.wantMessage, "Created"}
			}
			if diff := cmp.Diff(want, versionMessages(t, e, sk.ID)); diff != "" {
				t.Errorf("Update snapshots mismatch (-want +got):\n%s", diff)
			}
			if !got.UpdatedAt.After(sk.UpdatedAt) {
				t.Errorf("Update left updated_at %v, want later than %v", got.UpdatedAt, sk.UpdatedAt)
			}
		})
	}
}

func TestService_UpdateAppliesFields(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	got, err := e.svc.Update(t.Context(), wsID, sk.ID, domain.Patch{Name: str("y"), Type: typ(domain.TypeSecurity), Enabled: flag(false), Body: str("nb")})
	if err != nil {
		t.Fatalf("Update error = %v", err)
	}
	if got.Name != "y" || got.Type != domain.TypeSecurity || got.Enabled || got.Body != "nb" || got.Description != "d" {
		t.Errorf("Update = %+v, want name y, type security, disabled, body nb, description kept", got)
	}
}

func TestService_UpdateStaleVersion(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	for _, body := range []string{"b2", "b3"} {
		if _, err := e.svc.Update(t.Context(), wsID, sk.ID, domain.Patch{Body: str(body)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, patch := range []domain.Patch{
		{Body: str("new"), BaseVersion: num(2)},
		{BaseVersion: num(2)}, // also an empty patch
	} {
		_, err := e.svc.Update(t.Context(), wsID, sk.ID, patch)
		ae := wantAppErr(t, err, apperr.KindConflict, "stale_version", "The skill changed since you opened it (you edited v2, current is v3)")
		if diff := cmp.Diff(map[string]int{"current_version": 3}, ae.Details); diff != "" {
			t.Errorf("stale_version details mismatch (-want +got):\n%s", diff)
		}
	}
	got, _ := e.svc.Get(t.Context(), wsID, sk.ID)
	if got.Version != 3 || got.Body != "b3" {
		t.Errorf("skill after stale updates = v%d %q, want v3 b3 (nothing written)", got.Version, got.Body)
	}
}

func TestService_UpdateChecksInOrder(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	e.create(t, "taken")

	_, err := e.svc.Update(t.Context(), wsID, "missing", domain.Patch{Name: str("taken"), BaseVersion: num(9)})
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill not found")

	// A stale base_version wins over a duplicate name.
	_, err = e.svc.Update(t.Context(), wsID, sk.ID, domain.Patch{Name: str("taken"), BaseVersion: num(9)})
	wantAppErr(t, err, apperr.KindConflict, "stale_version", "The skill changed since you opened it (you edited v9, current is v1)")

	_, err = e.svc.Update(t.Context(), wsID, sk.ID, domain.Patch{Name: str("taken")})
	wantAppErr(t, err, apperr.KindConflict, "conflict", `A skill named "taken" already exists in this workspace`)
}

func TestService_UpdateDuplicateNameLeavesNoSnapshot(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	e.create(t, "taken")
	if _, err := e.svc.Update(t.Context(), wsID, sk.ID, domain.Patch{Name: str("taken"), Body: str("b2")}); err == nil {
		t.Fatal("Update error = nil, want conflict")
	}
	if diff := cmp.Diff([]string{"Created"}, versionMessages(t, e, sk.ID)); diff != "" {
		t.Errorf("snapshots after failed Update mismatch (-want +got):\n%s", diff)
	}
}

func TestService_Restore(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x") // v1: body b, description d
	for _, p := range []domain.Patch{{Body: str("b2"), Description: str("d2")}, {Body: str("b3")}} {
		if _, err := e.svc.Update(t.Context(), wsID, sk.ID, p); err != nil {
			t.Fatal(err)
		}
	}

	got, err := e.svc.Restore(t.Context(), wsID, sk.ID, 1)
	if err != nil {
		t.Fatalf("Restore error = %v", err)
	}
	if got.Version != 4 || got.Body != "b" || got.Description != "d" {
		t.Errorf("Restore = v%d %q %q, want v4 b d", got.Version, got.Body, got.Description)
	}
	want := []string{"Restored from v1", "Edited body", "Edited body and description", "Created"}
	if diff := cmp.Diff(want, versionMessages(t, e, sk.ID)); diff != "" {
		t.Errorf("snapshots after Restore mismatch (-want +got):\n%s", diff)
	}

	// Restoring the current version still writes a new one.
	got, err = e.svc.Restore(t.Context(), wsID, sk.ID, 4)
	if err != nil || got.Version != 5 {
		t.Errorf("Restore(current) = v%d, %v, want v5", got.Version, err)
	}
}

func TestService_RestoreKeepsDescriptionWhenSnapshotHasNone(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	// A snapshot with a null description, as imported data may have.
	e.w.versions[0].Description = nil
	if _, err := e.svc.Update(t.Context(), wsID, sk.ID, domain.Patch{Description: str("d2")}); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Restore(t.Context(), wsID, sk.ID, 1)
	if err != nil || got.Description != "d2" {
		t.Errorf("Restore = %q, %v, want description d2 kept", got.Description, err)
	}
}

func TestService_RestoreUnknownVersionOrSkill(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	_, err := e.svc.Restore(t.Context(), wsID, sk.ID, 9)
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill version not found")
	_, err = e.svc.Restore(t.Context(), wsID, "missing", 1)
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill not found")
	if got, _ := e.svc.Get(t.Context(), wsID, sk.ID); got.Version != 1 {
		t.Errorf("version after failed Restore = %d, want 1", got.Version)
	}
}

func TestService_Delete(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	if err := e.svc.Delete(t.Context(), wsID, sk.ID); err != nil {
		t.Fatalf("Delete error = %v", err)
	}
	if diff := cmp.Diff([]string{sk.ID}, e.w.evalGone); diff != "" {
		t.Errorf("eval cases deleted for mismatch (-want +got):\n%s", diff)
	}
	wantAppErr(t, e.svc.Delete(t.Context(), wsID, sk.ID), apperr.KindNotFound, "not_found", "Skill not found")
	if len(e.w.evalGone) != 1 {
		t.Errorf("eval cases deleted for an unknown skill: %v", e.w.evalGone)
	}
}

func TestService_VersionReads(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")

	v, err := e.svc.GetVersion(t.Context(), wsID, sk.ID, 1)
	if err != nil || v.Version != 1 {
		t.Errorf("GetVersion(1) = %+v, %v, want version 1", v, err)
	}
	_, err = e.svc.GetVersion(t.Context(), wsID, sk.ID, 9)
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill version not found")
	_, err = e.svc.GetVersion(t.Context(), wsID, "missing", 1)
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill version not found")
	_, err = e.svc.ListVersions(t.Context(), wsID, "missing")
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill not found")
	_, err = e.svc.Agents(t.Context(), wsID, "missing")
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill not found")
	_, err = e.svc.Stats(t.Context(), wsID, "missing", 30)
	wantAppErr(t, err, apperr.KindNotFound, "not_found", "Skill not found")
}

func TestService_Stats(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	e.w.agents["a1"] = domain.AgentRef{ID: "a1", Name: "alpha", Enabled: true}
	e.w.links["a1"] = []string{sk.ID}
	e.w.counters[sk.ID] = domain.Counters{RunsAttached: 2, RunsCited: 1, Findings: 3, Accepted: 1, Dismissed: 1}
	e.w.breakdown = domain.Breakdown{ByCategory: []domain.CountBy{{Key: "bug", Count: 2}, {Key: "security", Count: 1}}}

	got, err := e.svc.Stats(t.Context(), wsID, sk.ID, 7)
	if err != nil {
		t.Fatalf("Stats error = %v", err)
	}
	half := 0.5
	want := domain.Stats{
		SkillID: sk.ID, WindowDays: 7, RunsAttached: 2, RunsCited: 1, PullRate: &half, Findings: 3, Accepted: 1, Dismissed: 1, AcceptRate: &half,
		ByCategory: []domain.CountBy{{Key: "bug", Count: 2}, {Key: "security", Count: 1}},
		BySeverity: []domain.CountBy{},
		UsedBy:     []domain.AgentRef{{ID: "a1", Name: "alpha", Enabled: true}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Stats mismatch (-want +got):\n%s", diff)
	}
}

func TestService_StatsWithoutData(t *testing.T) {
	e := newEnv(t)
	sk := e.create(t, "x")
	got, err := e.svc.Stats(t.Context(), wsID, sk.ID, 30)
	if err != nil {
		t.Fatalf("Stats error = %v", err)
	}
	want := domain.Stats{
		SkillID: sk.ID, WindowDays: 30, ByCategory: []domain.CountBy{}, BySeverity: []domain.CountBy{}, UsedBy: []domain.AgentRef{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Stats mismatch (-want +got):\n%s", diff)
	}
}

func TestService_StatsSummaryFollowsListOrder(t *testing.T) {
	e := newEnv(t)
	b, a := e.create(t, "b"), e.create(t, "a")
	e.w.counters[b.ID] = domain.Counters{Findings: 4, Accepted: 3, Dismissed: 1}

	got, err := e.svc.StatsSummary(t.Context(), wsID, 30)
	if err != nil {
		t.Fatalf("StatsSummary error = %v", err)
	}
	rate := 0.75
	want := []domain.StatsSummary{{SkillID: a.ID}, {SkillID: b.ID, AcceptRate: &rate, Findings: 4}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("StatsSummary mismatch (-want +got):\n%s", diff)
	}
}
