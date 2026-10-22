package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dev-digest-go/backend/internal/repointel/domain"
)

// CallerRepos is the repo lookup of the caller scan; the index store implements it.
type CallerRepos interface {
	// GetRepo returns domain.ErrRepoNotFound for an unknown id.
	GetRepo(ctx context.Context, repoID string) (domain.Repo, error)
}

// CallerFiles is the read-only view of the clone the caller scan needs. Clone content is
// untrusted: implementations stay inside root, never follow a symbolic link and never write.
type CallerFiles interface {
	// ScanCallers lists the files to scan for references in path byte order; it wraps
	// domain.ErrCloneMissing when root is not a readable directory and returns ctx's error when
	// ctx ends.
	ScanCallers(ctx context.Context, root string) ([]string, error)
	// ReadCallerFile returns the bytes of a listed file.
	ReadCallerFile(root, rel string) ([]byte, error)
	// ReadFile returns the bytes of a changed file (capped at domain.MaxFileBytes).
	ReadFile(root, rel string) ([]byte, error)
}

// CallerScan configures Reader.CallerSignatures (WithCallerScan).
type CallerScan struct {
	Repos    CallerRepos
	Files    CallerFiles
	Analyzer Analyzer
	// Timeout bounds one call; zero means domain.CallerScanTimeout.
	Timeout time.Duration
}

// ReaderOption configures NewReader.
type ReaderOption func(*Reader)

// WithCallerScan enables CallerSignatures. Without it the operation returns an error.
func WithCallerScan(cs CallerScan) ReaderOption {
	return func(r *Reader) {
		if cs.Timeout <= 0 {
			cs.Timeout = domain.CallerScanTimeout
		}
		r.scan = &cs
	}
}

// errCallerScanOff is returned by CallerSignatures on a Reader built without WithCallerScan.
var errCallerScanOff = errors.New("caller signatures: caller scan is not configured")

// callerName is a callable name declared in a changed file and what the scan found for it.
type callerName struct {
	name     string
	declFile string
	rows     []domain.CallerSignature
	seen     map[callerKey]struct{}
}

type callerKey struct{ file, symbol string }

// CallerSignatures returns the callers of the callable symbols (function, method, class, no '.'
// in the name) the changed files declare, each labelled with its enclosing top-level symbol and
// that symbol's signature, at most limit rows (zero or less: domain.DefaultCallerLimit). It is
// Node's getCallerSignatures (spec section 7.11, D4004): the changed files are parsed in the
// clone, the clone is scanned with the reference rules of domain.ReferenceLines (one pass per file for all
// names, domain.ReferenceScanner), never the
// index tables except file_rank, which only orders the rows (percentile descending, stable).
// Rows come in name order (first declaring file wins), then path byte order, then line order.
//
// The answer is empty with the flag off, no changed file, an unknown repo, no clone path or a
// missing clone. A changed file that cannot be read, is over domain.MaxFileBytes, has no parser
// or fails to parse contributes no names. The whole call is bounded by the scan timeout (30 s)
// and by ctx; on expiry it returns an error wrapping the context error. Nothing outside the
// clone is read and nothing in it is run.
func (r *Reader) CallerSignatures(ctx context.Context, repoID string, changed []string, limit int) ([]domain.CallerSignature, error) {
	if !r.enabled || len(changed) == 0 {
		return []domain.CallerSignature{}, nil
	}
	if r.scan == nil {
		return nil, errCallerScanOff
	}
	if limit <= 0 {
		limit = domain.DefaultCallerLimit
	}
	ctx, cancel := context.WithTimeout(ctx, r.scan.Timeout)
	defer cancel()

	repo, err := r.scan.Repos.GetRepo(ctx, repoID)
	if errors.Is(err, domain.ErrRepoNotFound) {
		return []domain.CallerSignature{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("caller signatures: read repo: %w", err)
	}
	if repo.ClonePath == "" {
		return []domain.CallerSignature{}, nil
	}

	names, err := r.declaredNames(ctx, repo.ClonePath, changed)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return []domain.CallerSignature{}, nil
	}
	files, err := r.scan.Files.ScanCallers(ctx, repo.ClonePath)
	if errors.Is(err, domain.ErrCloneMissing) {
		return []domain.CallerSignature{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("caller signatures: %w", err)
	}
	if err := r.scanFiles(ctx, repo.ClonePath, files, names, limit); err != nil {
		return nil, err
	}

	rows := []domain.CallerSignature{}
	for i := range names {
		rows = append(rows, names[i].rows...)
		if len(rows) >= limit {
			rows = rows[:limit]
			break
		}
	}
	return r.rankRows(ctx, repoID, rows)
}

// declaredNames parses the changed files in input order and collects the callable names they
// declare; the first declaring file of a name wins.
func (r *Reader) declaredNames(ctx context.Context, root string, changed []string) ([]callerName, error) {
	var (
		names    []callerName
		known    = map[string]struct{}{}
		visited  = map[string]struct{}{}
		fullList bool
	)
	for _, file := range changed {
		if fullList {
			break
		}
		if _, dup := visited[file]; dup || !domain.IsSupported(file) {
			continue
		}
		visited[file] = struct{}{}
		raw, err := r.scan.Files.ReadFile(root, file)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("caller signatures: %w", ctx.Err())
			}
			continue
		}
		syms, err := r.symbolsOf(ctx, file, domain.DecodeText(raw))
		if err != nil {
			return nil, err
		}
		for _, s := range syms {
			if s.Kind != "function" && s.Kind != "method" && s.Kind != "class" || strings.Contains(s.Name, ".") || s.Name == "" {
				continue
			}
			if _, ok := known[s.Name]; ok {
				continue
			}
			known[s.Name] = struct{}{}
			names = append(names, callerName{name: s.Name, declFile: file, seen: map[callerKey]struct{}{}})
			if len(names) >= domain.MaxCallerNames {
				fullList = true
				break
			}
		}
	}
	return names, nil
}

// symbolsOf parses one file with its own parse deadline. A parse failure, timeout or panic gives
// no symbols and no error; only the end of ctx is an error.
func (r *Reader) symbolsOf(ctx context.Context, file, text string) (syms []domain.Symbol, err error) {
	defer func() {
		if p := recover(); p != nil {
			syms, err = nil, nil
		}
	}()
	fctx, cancel := context.WithTimeout(ctx, domain.ParseTimeout)
	defer cancel()
	ex, perr := r.scan.Analyzer.Extract(fctx, file, text)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("caller signatures: %w", ctx.Err())
	}
	if perr != nil {
		return nil, nil
	}
	return ex.Symbols, nil
}

// scanFiles reads each listed file once and collects, for every name still wanted, the callers
// found in it. The result equals Node's name-major loop: rows of a name appear in file order and
// line order, a name stops collecting at limit rows, and once the names up to some index hold
// limit rows between them the later names are dropped (their rows could never be returned).
// A file is scanned for all names in one pass (domain.ReferenceScanner), which looks at ctx every
// few thousand units of work, so the scan timeout and a cancel are honoured inside a file.
func (r *Reader) scanFiles(ctx context.Context, root string, files []string, names []callerName, limit int) error {
	symbols := make([]string, len(names))
	for i := range names {
		symbols[i] = names[i].name
	}
	scanner := domain.NewReferenceScanner(symbols)
	wanted := len(names)
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("caller signatures: %w", err)
		}
		if wanted == 0 || allFull(names[:wanted], limit) {
			return nil
		}
		raw, err := r.scan.Files.ReadCallerFile(root, rel)
		if err != nil {
			continue
		}
		text := domain.DecodeText(raw)

		var (
			encl   enclosingIndex
			parsed bool
			hitErr error
		)
		skip := func(i int) bool {
			return i >= wanted || len(names[i].rows) >= limit || names[i].declFile == rel
		}
		hit := func(i, line int) (bool, error) {
			n := &names[i]
			if !parsed {
				parsed = true
				syms, err := r.symbolsOf(ctx, rel, text)
				if err != nil {
					hitErr = err
					return false, err
				}
				encl = newEnclosingIndex(syms)
			}
			if enc := encl.at(line); enc != nil && enc.Signature != "" {
				key := callerKey{file: rel, symbol: enc.Name}
				if _, dup := n.seen[key]; !dup {
					n.seen[key] = struct{}{}
					n.rows = append(n.rows, domain.CallerSignature{File: rel, Symbol: enc.Name, Signature: enc.Signature})
				}
			}
			return len(n.rows) < limit, nil
		}
		if err := scanner.Scan(ctx, text, skip, hit); err != nil {
			if hitErr != nil {
				return hitErr
			}
			return fmt.Errorf("caller signatures: %w", err)
		}
		total := 0
		for i := range names[:wanted] {
			total += len(names[i].rows)
			if total >= limit {
				wanted = i + 1
				break
			}
		}
	}
	return nil
}

func allFull(names []callerName, limit int) bool {
	for i := range names {
		if len(names[i].rows) < limit {
			return false
		}
	}
	return true
}

// enclosingIndex answers "which symbol does a hit at line belong to" in O(log n): the symbol
// with the largest start line at or before it whose name has no '.'; of equal lines the first
// extracted.
type enclosingIndex struct{ syms []*domain.Symbol }

func newEnclosingIndex(syms []domain.Symbol) enclosingIndex {
	cand := make([]*domain.Symbol, 0, len(syms))
	for i := range syms {
		if !strings.Contains(syms[i].Name, ".") {
			cand = append(cand, &syms[i])
		}
	}
	slices.SortStableFunc(cand, func(a, b *domain.Symbol) int { return a.Line - b.Line })
	out := cand[:0]
	for _, s := range cand {
		if len(out) == 0 || out[len(out)-1].Line != s.Line {
			out = append(out, s)
		}
	}
	return enclosingIndex{syms: out}
}

func (e enclosingIndex) at(line int) *domain.Symbol {
	i, _ := slices.BinarySearchFunc(e.syms, line, func(s *domain.Symbol, line int) int {
		if s.Line <= line {
			return -1
		}
		return 1
	})
	if i == 0 {
		return nil
	}
	return e.syms[i-1]
}

// rankRows sets Rank from file_rank and, when the repo has any rank for the files, sorts by it
// (descending, stable).
func (r *Reader) rankRows(ctx context.Context, repoID string, rows []domain.CallerSignature) ([]domain.CallerSignature, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	var paths []string
	for _, row := range rows {
		if !slices.Contains(paths, row.File) {
			paths = append(paths, row.File)
		}
	}
	ranks, err := r.src.FileRanks(ctx, repoID, paths)
	if err != nil {
		return nil, fmt.Errorf("caller signatures: read file ranks: %w", err)
	}
	if len(ranks) == 0 {
		return rows, nil
	}
	byFile := make(map[string]int, len(ranks))
	for _, p := range ranks {
		byFile[p.Path] = p.Percentile
	}
	for i := range rows {
		rows[i].Rank = byFile[rows[i].File]
	}
	slices.SortStableFunc(rows, func(a, b domain.CallerSignature) int { return b.Rank - a.Rank })
	return rows, nil
}
