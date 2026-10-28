package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"dev-digest-go/backend/internal/repointel/domain"
)

// defaultMaxHeldRows is the number of symbol and reference rows a full index may hold in memory
// before it stops starting files (see domain.MaxRows).
const defaultMaxHeldRows = domain.MaxRows

// parseTimeoutReason is the reason of a file whose parse ran past domain.ParseTimeout (FR3017).
const parseTimeoutReason = "parse timed out after 2000 ms"

type fileKind int

const (
	fileIndexed fileKind = iota + 1
	// fileUnsupported: an extension the analyzer does not parse; skipped without a reason.
	fileUnsupported
	// fileFailed: unreadable file, parser failure or timeout.
	fileFailed
)

// fileResult is the outcome of one file and the rows it contributes; rows exist only for an
// indexed file (FR3017).
type fileResult struct {
	kind    fileKind
	reason  string
	symbols []domain.SymbolRow
	refs    []domain.ReferenceRow
	facts   *domain.FileFacts
}

// parseFailure is an entry of stats.parseDegraded.
type parseFailure struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// parsed is the accumulated output of the parse phase, in walk order.
type parsed struct {
	symbols  []domain.SymbolRow
	refs     []domain.ReferenceRow
	facts    []domain.FileFacts
	indexed  int
	skipped  int
	degraded []parseFailure
	// rowBudget is true when parsing stopped because the symbols and references held in memory
	// reached the row budget; files not started are not indexed.
	rowBudget bool
}

// add folds one file's result into p; a failed file adds a parseDegraded entry until the list
// holds domain.MaxParseDegraded.
func (p *parsed) add(file string, r fileResult) {
	switch r.kind {
	case fileIndexed:
		p.indexed++
		p.symbols = append(p.symbols, r.symbols...)
		p.refs = append(p.refs, r.refs...)
		if r.facts != nil {
			p.facts = append(p.facts, *r.facts)
		}
	case fileUnsupported:
		p.skipped++
	case fileFailed:
		p.skipped++
		if len(p.degraded) < domain.MaxParseDegraded {
			p.degraded = append(p.degraded, parseFailure{File: file, Reason: r.reason})
		}
	}
}

// parseFile reads, decodes and analyses one file of the clone (FR3017). Outcomes are reported,
// never returned as errors; a panic of the analyzer fails the file.
func (ix *Indexer) parseFile(ctx context.Context, root, rel string) (res fileResult) {
	if !domain.IsSupported(rel) {
		return fileResult{kind: fileUnsupported}
	}
	defer func() {
		if p := recover(); p != nil {
			res = fileResult{kind: fileFailed, reason: errorText(ix.d.Redact, fmt.Sprintf("parser panicked: %v", p))}
		}
	}()
	raw, err := ix.d.Files.ReadFile(root, rel)
	if err != nil {
		return fileResult{kind: fileFailed, reason: errorText(ix.d.Redact, err.Error())}
	}
	text := domain.DecodeText(raw)
	hash := domain.ContentHash(text)

	// The analyzer does not enforce the per-file limit itself: every parse gets its own deadline.
	fctx, cancel := context.WithTimeout(ctx, domain.ParseTimeout)
	defer cancel()
	ex, err := ix.d.Analyzer.Extract(fctx, rel, text)
	if fctx.Err() != nil && ctx.Err() == nil {
		return fileResult{kind: fileFailed, reason: parseTimeoutReason}
	}
	if err != nil {
		return fileResult{kind: fileFailed, reason: errorText(ix.d.Redact, err.Error())}
	}

	out := fileResult{kind: fileIndexed}
	for _, s := range ex.Symbols {
		out.symbols = append(out.symbols, domain.SymbolRow{
			Path: rel, Name: s.Name, Kind: s.Kind, Line: s.Line, EndLine: s.EndLine,
			Exported: s.Exported, Signature: s.Signature, ContentHash: hash,
		})
	}
	for _, r := range ex.References {
		out.refs = append(out.refs, domain.ReferenceRow{FromPath: rel, ToSymbol: r.Name, Line: r.Line, ContentHash: hash})
	}
	if f, ok := domain.ExtractFacts(rel, text); ok {
		out.facts = &f
	}
	return out
}

// parseFiles parses files with at most ix.workers in parallel (FR3014) and folds the results in
// the order of files. stop, when not nil, is asked before each file is started; once it says
// true no further file is started and softStop is true. maxRows, when above zero, is the row
// budget: once the files parsed so far hold that many symbol and reference rows no further file
// is started and out.rowBudget is true. It returns ctx's error when ctx ends.
func (ix *Indexer) parseFiles(ctx context.Context, root string, files []string, stop func() bool, maxRows int) (out parsed, softStop bool, err error) {
	slots := make([]*fileResult, len(files))
	var (
		next        atomic.Int64
		rows        atomic.Int64
		softStopped atomic.Bool
		rowStopped  atomic.Bool
		wg          sync.WaitGroup
	)
	for range min(ix.workers, max(len(files), 1)) {
		wg.Go(func() {
			for ctx.Err() == nil && !softStopped.Load() && !rowStopped.Load() {
				idx := int(next.Add(1)) - 1
				if idx >= len(files) {
					return
				}
				if stop != nil && stop() {
					softStopped.Store(true)
					return
				}
				if maxRows > 0 && rows.Load() >= int64(maxRows) {
					rowStopped.Store(true)
					return
				}
				r := ix.parseFile(ctx, root, files[idx])
				rows.Add(int64(len(r.symbols) + len(r.refs)))
				slots[idx] = &r
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return parsed{}, false, fmt.Errorf("parse files: %w", err)
	}
	out.degraded = []parseFailure{}
	out.rowBudget = rowStopped.Load()
	for i, r := range slots {
		if r != nil {
			out.add(files[i], *r)
		}
	}
	return out, softStopped.Load(), nil
}
