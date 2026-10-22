package treesitter_test

import (
	"runtime"
	"testing"

	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
)

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestExtract_DoesNotGrowTheHeapPerParse: go-tree-sitter v0.25.0 saves the options of
// ParseWithOptions in a global table and never releases them (about 186 bytes per parse), so the
// analyzer cancels through the parser's cancellation flag instead. Live heap after N parses must
// not grow with N.
func TestExtract_DoesNotGrowTheHeapPerParse(t *testing.T) {
	const (
		warmup   = 500
		parses   = 5000
		perParse = 24 // bytes; the leak measured 186
	)
	a := treesitter.New()
	src := "export function f(a: number) { return g(a + 1); }\n"
	for range warmup {
		if _, err := a.Extract(t.Context(), "f.ts", src); err != nil {
			t.Fatalf("Extract() error = %v", err)
		}
	}
	before := liveHeap()
	for range parses {
		if _, err := a.Extract(t.Context(), "f.ts", src); err != nil {
			t.Fatalf("Extract() error = %v", err)
		}
	}
	after := liveHeap()
	growth := int64(after) - int64(before)
	t.Logf("live heap grew %d bytes over %d parses (%d B per parse)", growth, parses, growth/parses)
	if growth > int64(parses*perParse) {
		t.Errorf("live heap grew %d bytes over %d parses, want at most %d", growth, parses, parses*perParse)
	}
}
