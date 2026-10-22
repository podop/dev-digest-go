package llm

import "testing"

func TestExtractJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain", ` {"x":1} `, `{"x":1}`, false},
		{"fenced json", "Here:\n```json\n{\"x\": 1}\n```\nbye", `{"x": 1}`, false},
		{"fenced plain", "```\n[1,2]\n```", `[1,2]`, false},
		{"prose around object", `Sure! {"a":"}"} hope that helps`, `{"a":"}"}`, false},
		{"prose around array", `result: [1,[2]] done`, `[1,[2]]`, false},
		{"not json", `not json`, "", true},
		{"unbalanced", `{"a":1`, "", true},
		{"empty", ``, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractJSON(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("extractJSON(%q) = %q, %v; want %q, error=%v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestUTF16(t *testing.T) {
	t.Parallel()
	if got := UTF16Len("a😀é"); got != 4 {
		t.Errorf("UTF16Len(a😀é) = %d, want 4", got)
	}
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"abcdef", 6, "abcdef"},
		{"abcdef", 3, "abc…"},
		{"ab😀cd", 3, "ab…"},
		{"ab😀cd", 4, "ab😀…"},
	}
	for _, tc := range tests {
		if got := truncateUTF16(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateUTF16(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
