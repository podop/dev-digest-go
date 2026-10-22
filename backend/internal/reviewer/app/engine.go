package app

import (
	"context"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/reviewer/domain"
)

// chunkResult is one finished call: the answer and the prompt it was made with.
type chunkResult struct {
	res      CallResult
	assembly domain.Assembly
}

// Run reviews a parsed diff (spec 7.5 step 7, 7.7). It picks single pass or map-reduce from the
// agent's strategy, splits oversize chunks, calls the model once per chunk (at most
// opts.MapConcurrency in flight, results kept in chunk order), reduces the partial reviews,
// drops the findings that do not cite a changed line, and scores the rest from their severities.
// The progress lines go to ev in the order of the spec.
//
// On the first failed call no further chunk starts, calls in flight finish and the first error is
// returned: ErrCancelled when opts.Cancelled stopped it, else the LLM port's error unchanged.
func Run(ctx context.Context, in Input, llm LLM, ev Events, opts Options) (domain.Outcome, error) {
	emit := func(kind domain.EventKind, msg string, data any) {
		if ev != nil {
			ev.Emit(kind, msg, data)
		}
	}
	parts := domain.PromptParts{
		System: in.SystemPrompt, Skills: in.Skills, Specs: in.Specs, RepoMap: in.RepoMap,
		Callers: in.Callers, PRDescription: in.PRDescription, Task: in.Task,
	}
	// The whole-diff prompt is the trace's default; a single pass replaces it with its call's.
	whole := parts
	whole.Diff = in.Diff.Raw
	assembly := domain.AssemblePrompt(whole).Assembly

	mode := domain.SelectMode(in.Strategy, in.Diff, domain.MapThresholdLines)
	files := strconv.Itoa(len(in.Diff.Files))
	if mode == domain.ModeMapReduce {
		emit(domain.EventInfo, "Large diff → map-reduce over "+files+" files", nil)
	} else {
		emit(domain.EventInfo, "Reviewing "+files+" changed file(s) in one pass", nil)
	}

	chunks, notes := domain.GuardChunkSize(domain.ChunksFor(mode, in.Diff), domain.MaxChunkUnits)
	for _, n := range notes {
		emit(domain.EventInfo, n.Message(), OversizeWarning{
			Warning: "diff_oversize", File: n.Label, Parts: n.Parts, Truncated: n.Truncated,
		})
	}
	effective := domain.EffectiveMode(mode, len(chunks))

	concurrency := opts.MapConcurrency
	if concurrency <= 0 {
		concurrency = domain.DefaultMapConcurrency
	}
	results, err := mapOrdered(chunks, concurrency, func(chunk domain.Chunk, index int) (chunkResult, error) {
		if opts.Cancelled != nil && opts.Cancelled() {
			return chunkResult{}, ErrCancelled
		}
		if effective == domain.ModeMapReduce {
			emit(domain.EventTool, "map: reviewing "+chunk.Label, FileData{File: chunk.Label})
		} else {
			emit(domain.EventTool, "Reviewing "+chunk.Label+" in one pass", FileData{File: chunk.Label})
		}
		p := parts
		p.Diff = chunk.DiffText
		a := domain.AssemblePrompt(p)
		res, err := llm.Review(ctx, Call{
			Messages: a.Messages,
			Sections: a.Sections,
			Chunk:    domain.ChunkRef{Index: index, Total: len(chunks)},
		})
		if err != nil {
			return chunkResult{}, err
		}
		emit(domain.EventResult, chunk.Label+": "+strconv.Itoa(len(res.Review.Findings))+" candidate finding(s)", nil)
		return chunkResult{res: res, assembly: a.Assembly}, nil
	})
	if err != nil {
		return domain.Outcome{}, err
	}

	if effective == domain.ModeSinglePass && len(results) > 0 {
		assembly = results[0].assembly
	}
	var usage domain.Usage
	zero := 0.0
	usage.CostUSD = &zero
	partials := make([]domain.Review, len(results))
	raws := make([]string, len(results))
	labels := make([]string, len(chunks))
	for i, r := range results {
		usage.TokensIn += r.res.Usage.TokensIn
		usage.TokensOut += r.res.Usage.TokensOut
		usage.CostUSD = addCost(usage.CostUSD, r.res.Usage.CostUSD)
		partials[i] = r.res.Review
		raws[i] = r.res.Raw
		labels[i] = chunks[i].Label
	}

	merged := domain.ReduceReviews(partials)
	emit(domain.EventResult, "Reduced to "+strconv.Itoa(len(merged.Findings))+" finding(s); verdict="+
		string(merged.Verdict)+", score="+strconv.Itoa(merged.Score), nil)

	ground := domain.GroundFindings(merged.Findings, in.Diff)
	for _, d := range ground.Dropped {
		emit(domain.EventInfo, "grounding dropped \""+d.Finding.Title+"\": "+d.Reason, nil)
	}
	summary := ground.Summary()
	emit(domain.EventResult, "Citation grounding: "+summary, nil)

	scoped := domain.ApplyScopePolicy(ground.Kept, false)
	merged.Findings = scoped.Findings
	merged.Score = domain.ScoreFromFindings(scoped.Findings)
	return domain.Outcome{
		Review:      merged,
		Grounding:   summary,
		Dropped:     ground.Dropped,
		Mode:        effective,
		Assembly:    assembly,
		ChunkLabels: labels,
		Usage:       usage,
		Raw:         strings.Join(raws, "\n---\n"),
	}, nil
}

// addCost adds two costs; a missing (unpriced) cost makes the sum missing.
func addCost(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	s := *a + *b
	return &s
}
