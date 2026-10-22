package importgraph_test

import (
	"testing"

	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

func TestIsBuiltin(t *testing.T) {
	tests := []struct {
		spec string
		want bool
	}{
		{"fs", true}, {"fs/promises", true}, {"node:fs", true}, {"node:test", true}, {"node:anything", true},
		{"path", true}, {"worker_threads", true}, {"_http_agent", true}, {"bun:sqlite", true},
		{"react", false}, {"fs-extra", false}, {"fs/", false}, {"test", false}, {"./fs", false},
		{"@scope/path", false}, {"", false}, {"Path", false},
	}
	for _, tc := range tests {
		if got := importgraph.IsBuiltin(tc.spec); got != tc.want {
			t.Errorf("IsBuiltin(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}
