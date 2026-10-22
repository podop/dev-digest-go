package httpapi

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/httpx"
)

type want struct {
	code, msg string
	path      []any
}

func gotIssues(issues []httpx.Issue) []want {
	var out []want
	for _, i := range issues {
		p := i.Path
		if len(p) == 0 {
			p = nil
		}
		out = append(out, want{i.Code, i.Message, p})
	}
	return out
}

const repoUUID = "3f2b8c1e-7a4d-4e9b-8c1a-2d3e4f5a6b7c"

func TestParseAttachBody_Issues(t *testing.T) {
	tests := []struct {
		name, body string
		want       []want
	}{
		{"empty", ``, []want{{"invalid_type", "Required", nil}}},
		{"array", `[]`, []want{{"invalid_type", "Expected object, received array", nil}}},
		{"string", `"x"`, []want{{"invalid_type", "Expected object, received string", nil}}},
		{"null", `null`, []want{{"invalid_type", "Expected object, received null", nil}}},
		{"both missing", `{}`, []want{
			{"invalid_type", "Required", []any{"repo_id"}},
			{"invalid_type", "Required", []any{"paths"}},
		}},
		{"bad uuid and item", `{"repo_id":"nope","paths":["a",3]}`, []want{
			{"invalid_string", "Invalid uuid", []any{"repo_id"}},
			{"invalid_type", "Expected string, received number", []any{"paths", 1}},
		}},
		{"paths not an array", `{"repo_id":"` + repoUUID + `","paths":"a"}`, []want{
			{"invalid_type", "Expected array, received string", []any{"paths"}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, issues := parseAttachBody([]byte(tc.body))
			if diff := cmp.Diff(tc.want, gotIssues(issues), cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("parseAttachBody(%s) issues mismatch (-want +got):\n%s", tc.body, diff)
			}
		})
	}
}

func TestParseAttachBody_Valid(t *testing.T) {
	got, issues := parseAttachBody([]byte(`{"repo_id":"` + repoUUID + `","paths":[],"extra":1}`))
	if issues != nil || got.RepoID != repoUUID || got.Paths == nil || len(got.Paths) != 0 {
		t.Errorf("parseAttachBody() = %+v, %v, want the repo and an empty non-nil list", got, issues)
	}
}

func TestParseSaveBody(t *testing.T) {
	tests := []struct {
		name, body string
		want       []want
		version    int64
	}{
		{"ok", `{"content":"x","base_version":3}`, nil, 3},
		{"float", `{"content":"x","base_version":1.5}`, []want{{"invalid_type", "Expected integer, received float", []any{"base_version"}}}, 0},
		{"string version", `{"content":"x","base_version":"1"}`, []want{{"invalid_type", "Expected number, received string", []any{"base_version"}}}, 0},
		{"missing", `{}`, []want{
			{"invalid_type", "Required", []any{"content"}},
			{"invalid_type", "Required", []any{"base_version"}},
		}, 0},
		{"huge is clamped, not rejected", `{"content":"","base_version":1e30}`, nil, 1 << 40},
		{"negative", `{"content":"","base_version":-2}`, nil, -2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, issues := parseSaveBody([]byte(tc.body))
			if diff := cmp.Diff(tc.want, gotIssues(issues), cmp.AllowUnexported(want{})); diff != "" {
				t.Fatalf("parseSaveBody(%s) issues mismatch (-want +got):\n%s", tc.body, diff)
			}
			if tc.want == nil && got.BaseVersion != tc.version {
				t.Errorf("parseSaveBody(%s) version = %d, want %d", tc.body, got.BaseVersion, tc.version)
			}
		})
	}
}

func TestParseCreateBody(t *testing.T) {
	for _, body := range []string{``, `null`, ` null `, `{}`} {
		in, issues := parseCreateBody([]byte(body))
		if issues != nil || in.Path != nil || in.Content != nil || in.OnConflict != nil {
			t.Errorf("parseCreateBody(%q) = %+v, %v, want an empty input", body, in, issues)
		}
	}
	in, issues := parseCreateBody([]byte(`{"path":"p","content":"","on_conflict":"suffix"}`))
	if issues != nil || *in.Path != "p" || *in.Content != "" || *in.OnConflict != "suffix" {
		t.Errorf("parseCreateBody() = %+v, %v", in, issues)
	}
	_, issues = parseCreateBody([]byte(`{"path":null,"on_conflict":"x"}`))
	wantIssues := []want{
		{"invalid_type", "Expected string, received null", []any{"path"}},
		{"invalid_enum_value", "Invalid enum value. Expected 'fail' | 'suffix', received 'x'", []any{"on_conflict"}},
	}
	if diff := cmp.Diff(wantIssues, gotIssues(issues), cmp.AllowUnexported(want{})); diff != "" {
		t.Errorf("parseCreateBody() issues mismatch (-want +got):\n%s", diff)
	}
}

func TestParseRenameBody(t *testing.T) {
	got, issues := parseRenameBody([]byte(`{"path":"a","new_path":"b","base_version":2}`))
	if issues != nil || got != (renameBody{Path: "a", NewPath: "b", BaseVersion: 2}) {
		t.Errorf("parseRenameBody() = %+v, %v", got, issues)
	}
	_, issues = parseRenameBody([]byte(`{"path":1}`))
	if len(issues) != 3 {
		t.Errorf("parseRenameBody() issues = %v, want one per field", issues)
	}
}
