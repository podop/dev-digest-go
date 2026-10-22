package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// hostileEnv is a repo whose changed file src/a.ts declares the given names and whose only other
// file, src/z.ts, is the given text with one symbol, h, on line 1.
func hostileEnv(names []string, z string, timeout time.Duration) *app.Reader {
	store := newFakeStore()
	store.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	var decl strings.Builder
	var syms []domain.Symbol
	for i, n := range names {
		fmt.Fprintf(&decl, "function %s() {}\n", n)
		syms = append(syms, sym(n, "function", i+1, "function "+n+"()"))
	}
	files := &callerFiles{files: map[string]string{"src/a.ts": decl.String(), "src/z.ts": z}}
	an := &fakeAnalyzer{ex: map[string]domain.Extraction{
		"src/a.ts": {Symbols: syms},
		"src/z.ts": {Symbols: []domain.Symbol{sym("h", "function", 1, "function h()")}},
	}}
	return app.NewReader(store, true, app.WithCallerScan(app.CallerScan{Repos: store, Files: files, Analyzer: an, Timeout: timeout}))
}

// TestCallerSignatures_ManyNamesOnOneHugeFile is the shape of the security probe of Phase 4
// (F1, NFR4010): 1,000 declared names and a ~2 MB caller file whose lines each call all of them.
// The per-name scan took 17 to 90 s; one pass over the file takes milliseconds.
func TestCallerSignatures_ManyNamesOnOneHugeFile(t *testing.T) {
	const al = "abcdefghijklmnopqrstuvwxyz"
	names := make([]string, domain.MaxCallerNames)
	var call strings.Builder
	for i := range names {
		names[i] = fmt.Sprintf("%c%c%c", al[i/676%26], al[i/26%26], al[i%26])
		call.WriteString(names[i] + "();")
	}
	for _, tail := range []string{"", " '"} { // the quote makes every line go through the string blanking
		t.Run(fmt.Sprintf("tail %q", tail), func(t *testing.T) {
			line := call.String() + tail + "\n"
			var z strings.Builder
			z.WriteString("function h() {\n")
			for z.Len()+len(line)+2 < domain.MaxCallerFileBytes-1 {
				z.WriteString(line)
			}
			z.WriteString("}\n")

			r := hostileEnv(names, z.String(), 8*time.Second)
			start := time.Now()
			got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 20)
			if err != nil {
				t.Fatalf("CallerSignatures() after %v error = %v, want the scan to finish within the limit", time.Since(start), err)
			}
			want := make([]domain.CallerSignature, 20)
			for i := range want {
				want[i] = domain.CallerSignature{File: "src/z.ts", Symbol: "h", Signature: "function h()"}
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("CallerSignatures() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCallerSignatures_TimeoutAndCancelInsideOneFile: names that are prefixes of each other make
// the work on one 2 MB line enormous (every position ends many names); the call must still end at
// the scan timeout or at the cancel, not when the file is done.
func TestCallerSignatures_TimeoutAndCancelInsideOneFile(t *testing.T) {
	names := make([]string, 200)
	for i := range names {
		names[i] = strings.Repeat("a", i+1)
	}
	z := strings.Repeat("a", 1_990_000) + "\n"

	t.Run("deadline", func(t *testing.T) {
		r := hostileEnv(names, z, 200*time.Millisecond)
		start := time.Now()
		_, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 20)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("CallerSignatures() error = %v, want context.DeadlineExceeded", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("CallerSignatures() with a 200 ms limit took %v, want it to end within the limit plus 1.8 s", d)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		r := hostileEnv(names, z, 30*time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(100*time.Millisecond, cancel)
		start := time.Now()
		_, err := r.CallerSignatures(ctx, repoID, []string{"src/a.ts"}, 20)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("CallerSignatures() error = %v, want context.Canceled", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("CallerSignatures() cancelled after 100 ms took %v, want it to end within 1.9 s of the cancel", d)
		}
	})
}

// TestCallerSignatures_RowsOfManyNamesInOneFile: names in collection order, a name stops at the
// limit, later names are dropped once earlier ones hold the limit between them.
func TestCallerSignatures_RowsOfManyNamesInOneFile(t *testing.T) {
	store := newFakeStore()
	store.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	files := &callerFiles{files: map[string]string{
		"src/a.ts": "function one() {}\nfunction two() {}\nfunction three() {}\n",
		"src/u.ts": "function f1() {\n two(); one();\n}\nfunction f2() {\n one(); three();\n}\nfunction f3() {\n two();\n}\n",
		"src/v.ts": "function g1() {\n three(); one();\n}\n",
	}}
	an := &fakeAnalyzer{ex: map[string]domain.Extraction{
		"src/a.ts": {Symbols: []domain.Symbol{sym("one", "function", 1, "function one()"), sym("two", "function", 2, "function two()"), sym("three", "function", 3, "function three()")}},
		"src/u.ts": {Symbols: []domain.Symbol{sym("f1", "function", 1, "function f1()"), sym("f2", "function", 4, "function f2()"), sym("f3", "function", 7, "function f3()")}},
		"src/v.ts": {Symbols: []domain.Symbol{sym("g1", "function", 1, "function g1()")}},
	}}
	r := app.NewReader(store, true, app.WithCallerScan(app.CallerScan{Repos: store, Files: files, Analyzer: an}))
	row := func(file, symbol string) domain.CallerSignature {
		return domain.CallerSignature{File: file, Symbol: symbol, Signature: "function " + symbol + "()"}
	}
	tests := []struct {
		limit int
		want  []domain.CallerSignature
	}{
		// one: u.ts f1, f2, v.ts g1; two: u.ts f1, f3; three: u.ts f2, v.ts g1.
		{limit: 20, want: []domain.CallerSignature{
			row("src/u.ts", "f1"), row("src/u.ts", "f2"), row("src/v.ts", "g1"),
			row("src/u.ts", "f1"), row("src/u.ts", "f3"),
			row("src/u.ts", "f2"), row("src/v.ts", "g1"),
		}},
		{limit: 2, want: []domain.CallerSignature{row("src/u.ts", "f1"), row("src/u.ts", "f2")}},
		{limit: 4, want: []domain.CallerSignature{row("src/u.ts", "f1"), row("src/u.ts", "f2"), row("src/v.ts", "g1"), row("src/u.ts", "f1")}},
	}
	for _, tc := range tests {
		got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, tc.limit)
		if err != nil {
			t.Fatalf("CallerSignatures(limit %d) error = %v", tc.limit, err)
		}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("CallerSignatures(limit %d) mismatch (-want +got):\n%s", tc.limit, diff)
		}
	}
}
