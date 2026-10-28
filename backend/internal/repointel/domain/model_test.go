package domain

import "testing"

func TestSyntaxForPath_Go(t *testing.T) {
	tests := []struct {
		path   string
		want   Syntax
		wantOK bool
	}{
		{"a.go", SyntaxGo, true},
		{"A.GO", SyntaxGo, true},
		{"a.go.txt", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got, ok := SyntaxForPath(tc.path)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("SyntaxForPath(%q) = (%v, %v), want (%v, %v)", tc.path, got, ok, tc.want, tc.wantOK)
			}
			if ok && got.String() != "go" {
				t.Errorf("SyntaxForPath(%q).String() = %q, want %q", tc.path, got.String(), "go")
			}
		})
	}
}

func TestGoPackageKey(t *testing.T) {
	tests := []struct{ file, want string }{
		{"main.go", "go:."},
		{"internal/api/h.go", "go:internal/api"},
	}
	for _, tc := range tests {
		if got := GoPackageKey(tc.file); got != tc.want {
			t.Errorf("GoPackageKey(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
}
