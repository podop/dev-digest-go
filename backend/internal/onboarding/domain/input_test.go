package domain_test

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/onboarding/domain"
	rdomain "dev-digest-go/backend/internal/reviewer/domain"
)

func todosOf(in []todoWire) []domain.TodoLine {
	out := make([]domain.TodoLine, len(in))
	for i, t := range in {
		out[i] = domain.TodoLine{Path: t.Path, Line: t.Line, Text: t.Text}
	}
	return out
}

func TestExcerpts_EqualNode(t *testing.T) {
	for i, c := range loadCases(t).Excerpts {
		text := expandText(t, c.In)
		for _, k := range []struct {
			name string
			fn   func(string) (string, bool)
			want *string
		}{{"ReadmeExcerpt", domain.ReadmeExcerpt, c.Readme}, {"ComposeExcerpt", domain.ComposeExcerpt, c.Compose}} {
			got, ok := k.fn(text)
			switch {
			case k.want == nil && ok:
				t.Errorf("case %d: %s(%q) = %q, want none", i, k.name, shorten(text), shorten(got))
			case k.want != nil && !ok:
				t.Errorf("case %d: %s(%q) = none, want %q", i, k.name, shorten(text), shorten(*k.want))
			case k.want != nil && got != *k.want:
				t.Errorf("case %d: %s(%q): got len %d (%q), want len %d (%q)", i, k.name, shorten(text),
					rdomain.Len16(got), shorten(got), rdomain.Len16(*k.want), shorten(*k.want))
			}
		}
	}
}

func TestExcerpts_CutAndRedactBeforeTruncating(t *testing.T) {
	got, ok := domain.ReadmeExcerpt(strings.Repeat("a", 20_000))
	if !ok || rdomain.Len16(got) != 16*1024 {
		t.Errorf("ReadmeExcerpt(20000 units) has %d units (ok=%v), want %d", rdomain.Len16(got), ok, 16*1024)
	}
	if _, ok := domain.ReadmeExcerpt("   \n "); ok {
		t.Error("ReadmeExcerpt(blank) = some, want none")
	}
	// a secret that straddles the cut must be redacted whole, not half-cut (INSIGHTS: redact before truncate)
	text := strings.Repeat("a", 16*1024-8) + " sk-" + strings.Repeat("Z", 40)
	got, _ = domain.ReadmeExcerpt(text)
	if strings.Contains(got, "sk-") || strings.Contains(got, "ZZZ") {
		t.Errorf("ReadmeExcerpt kept part of a token: ...%q", got[len(got)-30:])
	}
	if got, _ := domain.ComposeExcerpt("POSTGRES_PASSWORD: s3cret\nname: x"); got != "POSTGRES_PASSWORD: <redacted>\nname: x" {
		t.Errorf("ComposeExcerpt = %q", got)
	}
}

func TestPackageScripts_EqualsNode(t *testing.T) {
	for i, c := range loadCases(t).Scripts {
		got, ok := domain.PackageScripts(c.In)
		switch {
		case c.Out == nil && ok:
			t.Errorf("case %d: PackageScripts(%q) = %q, want none", i, shorten(c.In), shorten(got))
		case c.Out != nil && !ok:
			t.Errorf("case %d: PackageScripts(%q) = none, want %q", i, shorten(c.In), shorten(*c.Out))
		case c.Out != nil && got != *c.Out:
			t.Errorf("case %d: PackageScripts(%q) = %q, want %q", i, shorten(c.In), shorten(got), shorten(*c.Out))
		}
	}
}

func TestPackageScripts_Behaviour(t *testing.T) {
	tests := []struct {
		name, in, want string
		ok             bool
	}{
		{"strings only", `{"scripts":{"dev":"next dev","n":3}}`, "{\n  \"dev\": \"next dev\"\n}", true},
		{"numeric keys first", `{"scripts":{"b":"1","2":"x","a":"2","10":"y"}}`, "{\n  \"2\": \"x\",\n  \"10\": \"y\",\n  \"b\": \"1\",\n  \"a\": \"2\"\n}", true},
		{"duplicate key keeps first position, last value", `{"scripts":{"a":"1","b":"2","a":"3"}}`, "{\n  \"a\": \"3\",\n  \"b\": \"2\"\n}", true},
		{"not json", `{ not json`, "", false},
		{"no scripts", `{"name":"x"}`, "", false},
		{"scripts array", `{"scripts":["a"]}`, "", false},
		{"no string value", `{"scripts":{"a":1}}`, "", false},
		{"trailing garbage", `{"scripts":{"a":"b"}} x`, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.PackageScripts(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("PackageScripts(%s) = %q, %v, want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
	// a block nested a million levels deep parses (JSON.parse has no limit) and is not an object
	deep := `{"x":` + strings.Repeat("[", 1_000_000) + strings.Repeat("]", 1_000_000) + `,"scripts":{"a":"b"}}`
	if got, ok := domain.PackageScripts(deep); !ok || got != "{\n  \"a\": \"b\"\n}" {
		t.Errorf("PackageScripts(deeply nested) = %q, %v", shorten(got), ok)
	}
}

func TestEnvVariableNames_EqualsNodeAndKeepsNoValues(t *testing.T) {
	for i, c := range loadCases(t).EnvNames {
		got := domain.EnvVariableNames(c.In)
		if diff := cmp.Diff(c.Out, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d: EnvVariableNames(%q) mismatch (-want +got):\n%s", i, shorten(c.In), diff)
		}
	}
	got := domain.EnvVariableNames("# c\nAPI_KEY=sk-live-secret\nexport DB_URL = postgres://u:p@h\nAPI_KEY=again\n\nnot a var")
	if diff := cmp.Diff([]string{"API_KEY", "DB_URL"}, got); diff != "" {
		t.Errorf("EnvVariableNames mismatch (-want +got):\n%s", diff)
	}
}

func TestFindTodoLines_EqualsNode(t *testing.T) {
	for i, c := range loadCases(t).Todo {
		got := domain.FindTodoLines(c.Path, c.In, 0)
		if diff := cmp.Diff(todosOf(c.Out), got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d: FindTodoLines(%q, %q) mismatch (-want +got):\n%s", i, c.Path, shorten(c.In), diff)
		}
		// the limit keeps a prefix
		if lim := domain.FindTodoLines(c.Path, c.In, 3); len(c.Out) > 3 {
			if diff := cmp.Diff(todosOf(c.Out[:3]), lim); diff != "" {
				t.Errorf("case %d: FindTodoLines(limit 3) mismatch (-want +got):\n%s", i, diff)
			}
		}
	}
}

func TestCollectTodoLines_EqualsNode(t *testing.T) {
	for i, c := range loadCases(t).Collect {
		perFile := make([][]domain.TodoLine, len(c.PerFile))
		for j, f := range c.PerFile {
			perFile[j] = todosOf(f)
		}
		got := domain.CollectTodoLines(perFile)
		if diff := cmp.Diff(todosOf(c.Out), got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d: CollectTodoLines mismatch (-want +got):\n%s", i, diff)
		}
		if len(got) != domain.TodoMax {
			t.Errorf("case %d: CollectTodoLines returned %d lines, want %d", i, len(got), domain.TodoMax)
		}
	}
}

// P11: a TODO line must not keep the 512 KiB blob it was cut from alive.
func TestFindTodoLines_DoesNotRetainTheBlob(t *testing.T) {
	text := strings.Repeat("x", 1<<19) + "\n// TODO fix this\n" + strings.Repeat("y", 1<<10)
	start := uintptr(unsafe.Pointer(unsafe.StringData(text)))
	end := start + uintptr(len(text))
	lines := domain.FindTodoLines("a.go", text, 0)
	if len(lines) != 1 || lines[0].Line != 2 {
		t.Fatalf("FindTodoLines = %+v, want one line at 2", lines)
	}
	if p := uintptr(unsafe.Pointer(unsafe.StringData(lines[0].Text))); p >= start && p < end {
		t.Error("TodoLine.Text points into the scanned blob")
	}
}

func TestFindTodoLines_Edges(t *testing.T) {
	tests := []struct {
		name, in string
		want     []int
	}{
		{"CRLF", "a\r\nTODO b\r\nFIXME c", []int{2, 3}},
		{"lone CR is not a break", "a\rTODO b\nFIXME", []int{1, 2}},
		{"word boundary", "TODOs\nmyTODO\nTODO:\n_TODO\ntodo", []int{3}},
		{"empty", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			for _, l := range domain.FindTodoLines("f", tc.in, 0) {
				got = append(got, l.Line)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FindTodoLines(%q) lines mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestFindUntestedFiles_EqualsNode(t *testing.T) {
	for i, c := range loadCases(t).Untested {
		got := domain.FindUntestedFiles(c.Ranked, c.Indexed)
		if diff := cmp.Diff(c.Out, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d: FindUntestedFiles(%q, %q) mismatch (-want +got):\n%s", i, c.Ranked, c.Indexed, diff)
		}
	}
}

func TestIsTestPath_EqualsNode(t *testing.T) {
	for _, c := range loadCases(t).TestPath {
		if got := domain.IsTestPath(c.In); got != c.Out {
			t.Errorf("IsTestPath(%q) = %v, want %v", c.In, got, c.Out)
		}
	}
}

func TestPickRootFiles(t *testing.T) {
	str := func(s string) *string { return &s }
	paths := domain.RootFilePaths()
	if len(paths) != 9 || paths[3] != "package.json" || paths[8] != ".env.example" {
		t.Fatalf("RootFilePaths() = %q", paths)
	}
	texts := make([]*string, len(paths))
	texts[1], texts[2] = str(""), str("readme.md") // README exists but is empty: it still wins
	texts[3] = str("{}")
	texts[5], texts[6] = str("yaml"), str("compose.yml") // docker-compose.yaml comes first
	texts[8] = str("A=1")
	got := domain.PickRootFiles(texts)
	if got.Readme == nil || *got.Readme != "" || got.PackageJSON == nil || got.Compose == nil || *got.Compose != "yaml" || got.EnvExample == nil {
		t.Errorf("PickRootFiles = %+v", got)
	}
	if none := domain.PickRootFiles(make([]*string, len(paths))); none != (domain.RootTexts{}) {
		t.Errorf("PickRootFiles(all absent) = %+v", none)
	}
}
