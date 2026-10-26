package app_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/plugins/app"
	"dev-digest-go/backend/internal/plugins/domain"
)

const (
	ws   = "ws-1"
	user = "user-1"
	// marker appears in bundle text only (skill body, prompt, description); no log line may carry it.
	marker = "TOP-SECRET-BUNDLE-TEXT"
)

var (
	fixedNow = time.Date(2026, 10, 26, 23, 30, 0, 0, time.UTC)
	errBoom  = errors.New("boom")
)

// rig is a service over fakes.
type rig struct {
	svc     *app.Service
	reader  *fakeReader
	names   *fakeNames
	inst    *fakeInstaller
	plugins *fakePlugins
	last    *fakeLastExports
	logs    *bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		reader: &fakeReader{}, names: &fakeNames{}, inst: &fakeInstaller{}, plugins: &fakePlugins{},
		last: &fakeLastExports{}, logs: &bytes.Buffer{},
	}
	r.svc = app.NewService(app.Deps{
		Reader: r.reader, Names: r.names, Installer: r.inst, Plugins: r.plugins, LastExports: r.last,
		WorkspaceNames: fakeWorkspace{name: "Acme Corp", found: true},
		Clock:          func() time.Time { return fixedNow },
		Log:            slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Version:        "1.2.3",
	})
	return r
}

func sampleData() domain.ExportData {
	return domain.ExportData{
		Agents: []domain.AgentRecord{{
			ID: "a1", Name: "Reviewer", Description: "d " + marker, Provider: "openrouter", Model: "m",
			SystemPrompt: "prompt " + marker, Enabled: true, Strategy: "single-pass", CIFailOn: "critical",
			RepoIntel: true, SkillNames: []string{"style"},
		}},
		Skills: []domain.SkillRecord{{
			ID: "s1", Name: "style", Description: "desc", Type: "custom", Source: "manual",
			Body: "body " + marker, Enabled: true,
		}},
		EvalCases: []domain.EvalCaseRecord{{Name: "c1", OwnerKind: "agent", OwnerID: "a1"}},
		Learnings: []domain.LearningRecord{{Scope: "global", Kind: "learning", Content: "learn " + marker}},
	}
}

func importJSON(name string) []byte {
	return []byte(`{"manifest":{"name":"` + name + `","version":"1.0.0","format":"devdigest-plugin/v1"},` +
		`"agents":[{"name":"Rev","provider":"openrouter","model":"m","system_prompt":"p ` + marker + `","skills":["style"]}],` +
		`"skills":[{"name":"style","body":"body ` + marker + `"}],"eval_cases":[],"conventions":[]}`)
}

func jsonInput(name string) app.ImportInput {
	return app.ImportInput{Media: "application/json", Body: importJSON(name)}
}

func asApp(t *testing.T, err error) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v (%T), want *apperr.Error", err, err)
	}
	return ae
}

func wantApp(t *testing.T, err error, kind apperr.Kind, code string) *apperr.Error {
	t.Helper()
	ae := asApp(t, err)
	if ae.Kind != kind || ae.Code != code {
		t.Errorf("error = {kind %v, code %q}, want {kind %v, code %q}", ae.Kind, ae.Code, kind, code)
	}
	return ae
}

func unzipBundle(t *testing.T, archive []byte) domain.Bundle {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != domain.BundleFile {
		t.Fatalf("archive entries = %d, want only %s", len(zr.File), domain.BundleFile)
	}
	f, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("open entry: %v", err)
	}
	text, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close entry: %v", err)
	}
	var b domain.Bundle
	if err := json.Unmarshal(text, &b); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	return b
}

func TestExport_PassesSelectionAndBuildsArchive(t *testing.T) {
	r := newRig(t)
	r.reader.data = sampleData()

	got, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{
		Name: "my-plugin", Description: "hello", AgentIDs: []string{"a1"}, SkillIDs: []string{"s1"},
		EvalCases: true, Learnings: true,
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	wantSel := []app.ExportSelection{{AgentIDs: []string{"a1"}, SkillIDs: []string{"s1"}, EvalCases: true, Learnings: true}}
	if diff := cmp.Diff(wantSel, r.reader.calls); diff != "" {
		t.Errorf("ReadExport selection mismatch (-want +got):\n%s", diff)
	}
	if want := "my-plugin-2026-10-26.zip"; got.FileName != want {
		t.Errorf("Export().FileName = %q, want %q", got.FileName, want)
	}
	b := unzipBundle(t, got.Archive)
	if b.Manifest.Name != "my-plugin" || b.Manifest.Version != "1.2.3" || b.Manifest.Description == nil || *b.Manifest.Description != "hello" {
		t.Errorf("manifest = %+v, want name my-plugin, version 1.2.3, description hello", b.Manifest)
	}
	if len(b.Agents) != 1 || len(b.Skills) != 1 || len(b.EvalCases) != 1 || len(b.Learnings) != 1 {
		t.Errorf("bundle counts = %d/%d/%d/%d, want 1/1/1/1", len(b.Agents), len(b.Skills), len(b.EvalCases), len(b.Learnings))
	}
	wantLast := domain.LastExport{
		FileName: "my-plugin-2026-10-26.zip", ExportedAt: "2026-10-26T23:30:00.000Z",
		Counts: domain.LastExportCounts{Agents: 1, Skills: 1, EvalCases: 1, Learnings: 1},
	}
	if diff := cmp.Diff(wantLast, got.Last); diff != "" {
		t.Errorf("Export().Last mismatch (-want +got):\n%s", diff)
	}
}

func TestExport_EmptySelectionMeansAll(t *testing.T) {
	r := newRig(t)
	r.reader.data = sampleData()
	if _, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "x"}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if diff := cmp.Diff([]app.ExportSelection{{}}, r.reader.calls, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("ReadExport selection mismatch (-want +got):\n%s", diff)
	}
}

func TestExport_DefaultName(t *testing.T) {
	tests := []struct {
		name string
		ws   fakeWorkspace
		want string
	}{
		{"from the workspace name", fakeWorkspace{name: "Acme Corp!", found: true}, "devdigest-acme-corp-2026-10-26.zip"},
		{"no stored name", fakeWorkspace{}, "devdigest-default-2026-10-26.zip"},
		{"empty stored name", fakeWorkspace{name: "", found: true}, "devdigest-default-2026-10-26.zip"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.reader.data = sampleData()
			svc := app.NewService(app.Deps{
				Reader: r.reader, Names: r.names, Installer: r.inst, Plugins: r.plugins, LastExports: r.last,
				WorkspaceNames: tc.ws, Clock: func() time.Time { return fixedNow },
			})
			got, err := svc.Export(ctx(t), ws, user, app.ExportInput{})
			if err != nil {
				t.Fatalf("Export() error = %v", err)
			}
			if got.FileName != tc.want {
				t.Errorf("Export(no name).FileName = %q, want %q", got.FileName, tc.want)
			}
		})
	}
}

func TestExport_WorkspaceNameReadFailure(t *testing.T) {
	r := newRig(t)
	svc := app.NewService(app.Deps{
		Reader: r.reader, Names: r.names, Installer: r.inst, Plugins: r.plugins, LastExports: r.last,
		WorkspaceNames: fakeWorkspace{err: errBoom},
	})
	_, err := svc.Export(ctx(t), ws, user, app.ExportInput{})
	if !errors.Is(err, errBoom) {
		t.Errorf("Export() error = %v, want it to wrap the port error", err)
	}
	if _, ok := apperr.As(err); ok {
		t.Errorf("Export() error is an apperr.Error, want a plain error (500)")
	}
}

func TestExport_InvalidInput(t *testing.T) {
	tests := []struct {
		name string
		in   app.ExportInput
		path string
	}{
		{"upper case", app.ExportInput{Name: "My-Plugin"}, "/name"},
		{"spaces", app.ExportInput{Name: "my plugin"}, "/name"},
		{"too long", app.ExportInput{Name: strings.Repeat("a", 65)}, "/name"},
		{"description too long", app.ExportInput{Name: "ok", Description: strings.Repeat("d", 301)}, "/description"},
		{"description in UTF-16 units", app.ExportInput{Name: "ok", Description: strings.Repeat("\U0001F600", 151)}, "/description"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.reader.data = sampleData()
			_, err := r.svc.Export(ctx(t), ws, user, tc.in)
			ae := wantApp(t, err, apperr.KindValidation, "validation_error")
			details, ok := ae.Details.([]any)
			if !ok || len(details) != 1 {
				t.Fatalf("details = %#v, want one issue", ae.Details)
			}
			if got := details[0].(map[string]any)["instancePath"]; got != tc.path {
				t.Errorf("details[0].instancePath = %v, want %s", got, tc.path)
			}
			if len(r.reader.calls) != 0 {
				t.Errorf("ReadExport called %d times, want 0 before the input is accepted", len(r.reader.calls))
			}
		})
	}
}

func TestExport_DescriptionBoundary(t *testing.T) {
	r := newRig(t)
	r.reader.data = sampleData()
	_, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "ok", Description: strings.Repeat("d", 300)})
	if err != nil {
		t.Errorf("Export(300-char description) error = %v, want nil", err)
	}
}

func TestExport_UnknownIDs(t *testing.T) {
	r := newRig(t)
	r.reader.missing = []string{"x1", "x2"}
	_, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "ok", AgentIDs: []string{"x1", "x2"}})
	ae := wantApp(t, err, apperr.KindNotFound, "not_found")
	if diff := cmp.Diff(map[string][]string{"ids": {"x1", "x2"}}, ae.Details); diff != "" {
		t.Errorf("details mismatch (-want +got):\n%s", diff)
	}
	if r.last.sets != 0 {
		t.Errorf("last export recorded %d times, want 0", r.last.sets)
	}
}

func TestExport_NothingToExport(t *testing.T) {
	r := newRig(t)
	r.reader.data = domain.ExportData{Learnings: []domain.LearningRecord{{Content: "only a learning"}}}
	_, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "ok", Learnings: true})
	wantApp(t, err, apperr.KindValidation, app.CodeNothingToExport)
	if r.last.sets != 0 {
		t.Errorf("last export recorded %d times, want 0", r.last.sets)
	}
}

func TestExport_ReadFailure(t *testing.T) {
	r := newRig(t)
	r.reader.err = errBoom
	_, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "ok"})
	if !errors.Is(err, errBoom) {
		t.Errorf("Export() error = %v, want it to wrap the reader error", err)
	}
}

func TestExport_TooLarge(t *testing.T) {
	r := newRig(t)
	data := sampleData()
	data.Skills[0].Body = strings.Repeat("a", domain.MaxInflatedBytes+1)
	r.reader.data = data
	_, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "ok"})
	ae := wantApp(t, err, apperr.KindValidation, app.CodeExportTooLarge)
	if !errors.Is(ae, domain.ErrExportTooLarge) {
		t.Errorf("error cause = %v, want domain.ErrExportTooLarge", ae.Unwrap())
	}
	if r.last.sets != 0 {
		t.Errorf("last export recorded %d times, want 0 for a refused export", r.last.sets)
	}
}

func TestExport_LastExportRecordedAndReadBack(t *testing.T) {
	r := newRig(t)
	r.reader.data = sampleData()

	before, err := r.svc.List(ctx(t), ws)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if before.LastExport != nil {
		t.Fatalf("List().LastExport before any export = %+v, want nil", before.LastExport)
	}

	got, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "my-plugin", EvalCases: true})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if r.last.sets != 1 || r.last.users[0] != user {
		t.Errorf("SetLastExport calls = %d for users %v, want 1 for %q", r.last.sets, r.last.users, user)
	}
	after, err := r.svc.List(ctx(t), ws)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if after.LastExport == nil {
		t.Fatal("List().LastExport after an export = nil")
	}
	if diff := cmp.Diff(got.Last, *after.LastExport); diff != "" {
		t.Errorf("List().LastExport mismatch (-export +list):\n%s", diff)
	}
}

func TestExport_LastExportFailureStillDownloads(t *testing.T) {
	r := newRig(t)
	r.reader.data = sampleData()
	r.last.setErr = errBoom

	got, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "my-plugin"})
	if err != nil {
		t.Fatalf("Export() error = %v, want the download to succeed", err)
	}
	if len(got.Archive) == 0 || got.FileName == "" {
		t.Errorf("Export() = %d bytes named %q, want an archive", len(got.Archive), got.FileName)
	}
	var warn map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(r.logs.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if rec["level"] == "WARN" {
			warn = rec
		}
	}
	if warn == nil {
		t.Fatalf("no WARN logged for the failed last-export write; logs:\n%s", r.logs.String())
	}
	if warn["plugin_name"] != "my-plugin" || warn["agents"] != float64(1) {
		t.Errorf("WARN = %v, want plugin_name my-plugin and agents 1", warn)
	}
	assertNoBundleText(t, r.logs.String())
}

func TestList(t *testing.T) {
	r := newRig(t)
	t.Run("empty store is an empty list", func(t *testing.T) {
		got, err := r.svc.List(ctx(t), ws)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if got.Plugins == nil || len(got.Plugins) != 0 {
			t.Errorf("List().Plugins = %#v, want a non-nil empty slice", got.Plugins)
		}
	})
	t.Run("damaged last export is none", func(t *testing.T) {
		r.last.raw, r.last.found = json.RawMessage(`{"file_name":7}`), true
		got, err := r.svc.List(ctx(t), ws)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if got.LastExport != nil {
			t.Errorf("List().LastExport = %+v, want nil for a damaged row", got.LastExport)
		}
	})
	t.Run("plugin store failure", func(t *testing.T) {
		r.plugins.listErr = errBoom
		if _, err := r.svc.List(ctx(t), ws); !errors.Is(err, errBoom) {
			t.Errorf("List() error = %v, want it to wrap the store error", err)
		}
	})
	t.Run("last export read failure", func(t *testing.T) {
		r.plugins.listErr = nil
		r.last.getErr = errBoom
		if _, err := r.svc.List(ctx(t), ws); !errors.Is(err, errBoom) {
			t.Errorf("List() error = %v, want it to wrap the settings error", err)
		}
	})
}

func TestPreview_WritesNothing(t *testing.T) {
	r := newRig(t)
	r.names.found = domain.Conflicts{Plugin: true, Skills: []string{"style"}}

	doc, err := r.svc.Preview(ctx(t), ws, jsonInput("  acme  "))
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if doc.Manifest.Name != "acme" || len(doc.Agents) != 1 || len(doc.Skills) != 1 {
		t.Errorf("Preview() = name %q, %d agents, %d skills, want acme, 1, 1", doc.Manifest.Name, len(doc.Agents), len(doc.Skills))
	}
	if diff := cmp.Diff(r.names.found, doc.Conflicts, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Preview().Conflicts mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"acme"}, r.names.plugin); diff != "" {
		t.Errorf("Conflicts() plugin name (trimmed) mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([][]string{{"style"}}, r.names.skills); diff != "" {
		t.Errorf("Conflicts() skill names mismatch (-want +got):\n%s", diff)
	}
	if len(r.inst.plans) != 0 || r.plugins.removes != 0 || r.last.sets != 0 {
		t.Errorf("Preview wrote: installs %d, removes %d, last exports %d, want none", len(r.inst.plans), r.plugins.removes, r.last.sets)
	}
}

func TestPreviewAndInstall_Refusals(t *testing.T) {
	tests := []struct {
		name string
		in   app.ImportInput
		code string
		kind apperr.Kind
	}{
		{"bad file name", app.ImportInput{Media: "application/json", Body: importJSON("acme"), FileName: "a\nb.zip"}, "validation_error", apperr.KindValidation},
		{"unsupported media", app.ImportInput{Media: "text/plain", Body: importJSON("acme")}, app.CodeUnsupportedMediaType, apperr.KindInvalidInput},
		{"not json", app.ImportInput{Media: "application/json", Body: []byte("{")}, app.CodeInvalidPlugin, apperr.KindValidation},
		{"invalid bundle", jsonInput("Not Kebab Name " + strings.Repeat("x", 80)), app.CodeInvalidPlugin, apperr.KindValidation},
	}
	for _, op := range []string{"preview", "install"} {
		for _, tc := range tests {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				r := newRig(t)
				var err error
				if op == "preview" {
					_, err = r.svc.Preview(ctx(t), ws, tc.in)
				} else {
					_, err = r.svc.Install(ctx(t), ws, tc.in)
				}
				ae := wantApp(t, err, tc.kind, tc.code)
				if tc.code == app.CodeUnsupportedMediaType && !errors.Is(ae, domain.ErrUnsupportedMedia) {
					t.Errorf("unsupported media cause = %v, want domain.ErrUnsupportedMedia", ae.Unwrap())
				}
				if tc.code == app.CodeInvalidPlugin {
					d, ok := ae.Details.(map[string][]string)
					if !ok || len(d["issues"]) == 0 || ae.Message != d["issues"][0] {
						t.Errorf("details = %#v, message %q, want issues with the message first", ae.Details, ae.Message)
					}
				}
				if len(r.names.plugin) != 0 || len(r.inst.plans) != 0 {
					t.Errorf("refused upload reached names (%d) or installer (%d)", len(r.names.plugin), len(r.inst.plans))
				}
			})
		}
	}
}

func TestInstall_RefusesConflictBeforeWriting(t *testing.T) {
	tests := []struct {
		name  string
		found domain.Conflicts
		want  map[string]any
	}{
		{"plugin name", domain.Conflicts{Plugin: true}, map[string]any{"plugin": true, "skills": []string{}}},
		{"skill name", domain.Conflicts{Skills: []string{"style"}}, map[string]any{"plugin": false, "skills": []string{"style"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.names.found = tc.found
			_, err := r.svc.Install(ctx(t), ws, jsonInput("acme"))
			ae := wantApp(t, err, apperr.KindConflict, app.CodePluginConflict)
			if diff := cmp.Diff(tc.want, ae.Details); diff != "" {
				t.Errorf("details mismatch (-want +got):\n%s", diff)
			}
			if len(r.inst.plans) != 0 {
				t.Errorf("Installer.Install called %d times, want 0 before a conflict is resolved", len(r.inst.plans))
			}
		})
	}
}

func TestInstall_RaceIsConflict(t *testing.T) {
	r := newRig(t)
	r.inst.err = &domain.ConflictError{Conflicts: domain.Conflicts{Plugin: true}}
	_, err := r.svc.Install(ctx(t), ws, jsonInput("acme"))
	ae := wantApp(t, err, apperr.KindConflict, app.CodePluginConflict)
	if diff := cmp.Diff(map[string]any{"plugin": true, "skills": []string{}}, ae.Details); diff != "" {
		t.Errorf("details mismatch (-want +got):\n%s", diff)
	}
}

func TestInstall_StoreFailure(t *testing.T) {
	r := newRig(t)
	r.inst.err = errBoom
	_, err := r.svc.Install(ctx(t), ws, jsonInput("acme"))
	if !errors.Is(err, errBoom) {
		t.Errorf("Install() error = %v, want it to wrap the store error", err)
	}
	if _, ok := apperr.As(err); ok {
		t.Errorf("Install() error is an apperr.Error, want a plain error (500)")
	}
}

func TestInstall_PlansTheTrimmedUpload(t *testing.T) {
	r := newRig(t)
	in := jsonInput("  acme  ")
	in.FileName = "acme.json"

	got, err := r.svc.Install(ctx(t), ws, in)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(r.inst.plans) != 1 {
		t.Fatalf("Installer.Install calls = %d, want 1", len(r.inst.plans))
	}
	plan := r.inst.plans[0]
	if plan.Name != "acme" || plan.Source != "acme.json" {
		t.Errorf("plan = name %q, source %q, want acme, acme.json", plan.Name, plan.Source)
	}
	if len(plan.Agents) != 1 || len(plan.Skills) != 1 || plan.Skills[0].SourceRef != "plugin:acme" {
		t.Errorf("plan agents/skills = %d/%d (source ref %q), want 1/1 with plugin:acme", len(plan.Agents), len(plan.Skills), plan.Skills[0].SourceRef)
	}
	if diff := cmp.Diff([]string{"acme"}, r.names.plugin); diff != "" {
		t.Errorf("conflict check name mismatch (-want +got):\n%s", diff)
	}
	if got.Plugin.Name != "acme" || got.Created.Agents != 1 || got.Created.Skills != 1 {
		t.Errorf("Install() = %+v, want the installed plugin with 1 agent and 1 skill", got)
	}
}

func TestInstall_ZipUpload(t *testing.T) {
	r := newRig(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(domain.BundleFile)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(importJSON("zipped")); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	got, err := r.svc.Install(ctx(t), ws, app.ImportInput{Media: "application/zip", Body: buf.Bytes(), FileName: "zipped.zip"})
	if err != nil {
		t.Fatalf("Install(zip) error = %v", err)
	}
	if got.Plugin.Name != "zipped" {
		t.Errorf("Install(zip).Plugin.Name = %q, want zipped", got.Plugin.Name)
	}
}

func TestSetEnabled(t *testing.T) {
	r := newRig(t)
	r.plugins.rows = []domain.Plugin{{ID: "p1", Name: "acme", Counts: domain.PluginCounts{Agents: 2, Skills: 3}}}

	got, err := r.svc.SetEnabled(ctx(t), ws, "p1", true)
	if err != nil {
		t.Fatalf("SetEnabled(true) error = %v", err)
	}
	if !got.Enabled {
		t.Errorf("SetEnabled(true).Enabled = false")
	}
	got, err = r.svc.SetEnabled(ctx(t), ws, "p1", false)
	if err != nil || got.Enabled {
		t.Errorf("SetEnabled(false) = %+v, %v, want a disabled plugin", got, err)
	}
	if !strings.Contains(r.logs.String(), "plugin enabled") || !strings.Contains(r.logs.String(), "plugin disabled") {
		t.Errorf("logs lack the enable and disable events:\n%s", r.logs.String())
	}

	_, err = r.svc.SetEnabled(ctx(t), ws, "nope", true)
	wantApp(t, err, apperr.KindNotFound, "not_found")
}

func TestRemove_ExactConfirmation(t *testing.T) {
	tests := []struct {
		name    string
		confirm string
		ok      bool
	}{
		{"exact", "Acme Plugin", true},
		{"other case", "acme plugin", false},
		{"leading space", " Acme Plugin", false},
		{"trailing space", "Acme Plugin ", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.plugins.rows = []domain.Plugin{{ID: "p1", Name: "Acme Plugin"}}
			r.plugins.removed = domain.Removed{Agents: 1, Skills: 2, EvalCases: 3, Learnings: 4, CIInstallations: 5}

			got, err := r.svc.Remove(ctx(t), ws, "p1", tc.confirm)
			if tc.ok {
				if err != nil {
					t.Fatalf("Remove(%q) error = %v", tc.confirm, err)
				}
				if diff := cmp.Diff(r.plugins.removed, got); diff != "" {
					t.Errorf("Remove(%q) mismatch (-want +got):\n%s", tc.confirm, diff)
				}
				if len(r.plugins.rows) != 0 {
					t.Errorf("plugin still stored after a confirmed removal")
				}
				return
			}
			wantApp(t, err, apperr.KindValidation, app.CodeConfirmationMismatch)
			if len(r.plugins.rows) != 1 {
				t.Errorf("plugin removed on a wrong confirmation %q", tc.confirm)
			}
		})
	}
}

func TestRemove_NotFound(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Remove(ctx(t), ws, "nope", "x")
	wantApp(t, err, apperr.KindNotFound, "not_found")
}

func TestLogs_NeverCarryBundleText(t *testing.T) {
	r := newRig(t)
	r.reader.data = sampleData()
	r.plugins.rows = []domain.Plugin{{ID: "p1", Name: "acme"}}

	if _, err := r.svc.Export(ctx(t), ws, user, app.ExportInput{Name: "my-plugin", Description: "x " + marker, EvalCases: true, Learnings: true}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if _, err := r.svc.Preview(ctx(t), ws, jsonInput("acme")); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if _, err := r.svc.Install(ctx(t), ws, jsonInput("acme2")); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := r.svc.Install(ctx(t), ws, app.ImportInput{Media: "application/json", Body: []byte(`{"manifest":"` + marker + `"`)}); err == nil {
		t.Fatal("Install(malformed) error = nil")
	}
	r.inst.err = errBoom
	if _, err := r.svc.Install(ctx(t), ws, jsonInput("acme3")); err == nil {
		t.Fatal("Install(store failure) error = nil")
	}
	if _, err := r.svc.SetEnabled(ctx(t), ws, "p1", true); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	if _, err := r.svc.Remove(ctx(t), ws, "p1", "acme"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	logs := r.logs.String()
	for _, want := range []string{"plugin exported", "plugin installed", "plugin enabled", "plugin removed", "my-plugin", "acme2"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
	assertNoBundleText(t, logs)
}

func assertNoBundleText(t *testing.T, logs string) {
	t.Helper()
	if strings.Contains(logs, marker) {
		t.Errorf("logs carry bundle text (%q):\n%s", marker, logs)
	}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	return t.Context()
}
