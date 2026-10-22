package treesitter_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/domain"
)

// hostile returns the shapes of the largest file the walk admits that were measured to pin a
// worker for minutes before the extraction passes honoured the context (security review F1).
func hostile() map[string]string {
	const size = 409_600 // domain.MaxFileBytes
	return map[string]string{
		"open_parens":      strings.Repeat("(", size),
		"broken_tokens":    strings.Repeat("function ( { [ < } ", size/19),
		"calls":            strings.Repeat("a();\n", size/5),
		"arrow_consts":     strings.Repeat("const a=()=>1;\n", size/15),
		"imports":          strings.Repeat("import 'p';\n", size/12),
		"deep_parens":      strings.Repeat("(", size/2) + strings.Repeat(")", size/2),
		"deep_member":      "x" + strings.Repeat(".a", size/2-1) + "();",
		"jsx_nested":       strings.Repeat("<A>", size/3),
		"bare_identifiers": "import {a} from 'x';\n" + strings.Repeat("a ", size/2-10),
	}
}

// run calls Extract or Imports and returns the error and the time it took.
func run(ctx context.Context, a *treesitter.Analyzer, op, file, src string) (time.Duration, error) {
	start := time.Now()
	var err error
	if op == "Extract" {
		_, err = a.Extract(ctx, file, src)
	} else {
		_, err = a.Imports(ctx, file, src)
	}
	return time.Since(start), err
}

// TestHostileFile_TwoSecondLimit: the per-file limit of domain.ParseTimeout bounds the whole
// extraction, not only the parse. The two shapes of the review (409,600 open parentheses, and
// the broken-token file) used to take 27 and 3 minutes; now each call returns within the limit
// plus slack, with a result or an error wrapping context.DeadlineExceeded.
//
// The slack is a regression bound, not a latency target: the C parser polls its cancel flag at
// intervals, and under -race with other packages running (the full gate) a call was measured
// 1.2 s past the limit. A run-away extraction takes minutes, so a few seconds of slack still
// separates the two.
func TestHostileFile_TwoSecondLimit(t *testing.T) {
	const slack = 8 * time.Second
	a := treesitter.New()
	for _, name := range []string{"open_parens", "broken_tokens"} {
		src := hostile()[name]
		for _, op := range []string{"Extract", "Imports"} {
			t.Run(name+"/"+op, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), domain.ParseTimeout)
				defer cancel()
				elapsed, err := run(ctx, a, op, "f.ts", src)
				if elapsed > domain.ParseTimeout+slack {
					t.Errorf("%s(%s) took %v, want at most %v", op, name, elapsed, domain.ParseTimeout+slack)
				}
				if err != nil && !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("%s(%s) error = %v, want nil or context.DeadlineExceeded", op, name, err)
				}
			})
		}
	}
}

// TestHostileFiles_StopAtTheDeadline: every hostile shape, in every grammar, stops soon after a
// short deadline, with an error that wraps context.DeadlineExceeded. The slack is generous for the
// reason given at TestHostileFile_TwoSecondLimit; the old behaviour overran by minutes.
func TestHostileFiles_StopAtTheDeadline(t *testing.T) {
	const (
		deadline = 100 * time.Millisecond
		slack    = 5 * time.Second
	)
	a := treesitter.New()
	for name, src := range hostile() {
		for _, file := range []string{"f.ts", "f.tsx", "f.js"} {
			for _, op := range []string{"Extract", "Imports"} {
				t.Run(name+"/"+file+"/"+op, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), deadline)
					defer cancel()
					elapsed, err := run(ctx, a, op, file, src)
					if elapsed > deadline+slack {
						t.Errorf("%s(%s %s) took %v, want at most %v", op, name, file, elapsed, deadline+slack)
					}
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("%s(%s %s) error = %v, want context.DeadlineExceeded", op, name, file, err)
					}
				})
			}
		}
	}
}

// TestExtract_CancelledMidParse: cancelling the context while the parse runs stops it.
func TestExtract_CancelledMidParse(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	elapsed, err := run(ctx, treesitter.New(), "Extract", "f.ts", hostile()["open_parens"])
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Extract(open_parens) error = %v, want context.Canceled", err)
	}
	if elapsed > 5*time.Second { // see TestHostileFile_TwoSecondLimit on the slack
		t.Errorf("Extract(open_parens) took %v after cancel, want under 5s", elapsed)
	}
}
