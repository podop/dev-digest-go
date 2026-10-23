package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"dev-digest-go/backend/internal/repointel/domain"
)

// blastSource is what BlastRadius reads from the index besides the state row and the symbols of
// files (readerSource).
type blastSource interface {
	// ResolvedCallers returns the references resolved to one of declFiles with a to_symbol in
	// names, joined with the file_rank of the referencing file, in rank order (descending, then
	// path, line, symbol; byte order).
	ResolvedCallers(ctx context.Context, repoID string, declFiles, names []string) ([]domain.ResolvedCaller, error)
	// FileFacts returns the file_facts rows of files.
	FileFacts(ctx context.Context, repoID string, files []string) ([]domain.FileFacts, error)
}

// BlastRadius is the blast radius read of spec section 7.7 (Node getBlastRadius): the symbols the
// changed files declare and their callers in other files.
//
// With the switch off, no changed file or an unusable index (no state row or an unreadable one,
// status failed or degraded) the answer comes from a bounded scan of the clone (blastScan) and is
// tagged degraded with the reason (flag_off, no_data, index_failed or the state's own degraded
// reason). With a usable index (full or partial) the answer is the index data: changed symbols
// from symbols, callers from the references resolved to a changed file with a file_rank row,
// each labelled with its enclosing symbol, at most MaxCallersPerSymbol per changed symbol in rank
// order, and facts per caller file; it is tagged degraded with index_partial or repo_too_large
// when the index is partial or its walk was bounded.
//
// Neither path fails for an unusable index; a database error on the persistent path and the end
// of ctx are returned. The scan answers its empty result when its own time limit ends.
func (r *Reader) BlastRadius(ctx context.Context, repoID string, changed []string) (domain.BlastResult, error) {
	if !r.enabled {
		return r.blastScan(ctx, repoID, changed, domain.ReasonFlagOff)
	}
	if len(changed) == 0 {
		return emptyBlast(domain.ReasonNoData), nil
	}
	// A state that cannot be read counts as none (Node tryGetIndexState).
	st, ok, err := r.src.GetState(ctx, repoID)
	if err != nil && ctx.Err() != nil {
		return domain.BlastResult{}, fmt.Errorf("blast radius: %w", ctx.Err())
	}
	switch {
	case err != nil || !ok:
		return r.blastScan(ctx, repoID, changed, domain.ReasonNoData)
	case !domain.UsableIndex(st):
		return r.blastScan(ctx, repoID, changed, domain.UnusableIndexReason(st))
	}
	degraded, reason := domain.UsableIndexFlags(st)
	res, err := r.persistentBlast(ctx, repoID, changed)
	if err != nil {
		return domain.BlastResult{}, err
	}
	res.Degraded, res.Reason = degraded, reason
	return res, nil
}

// emptyBlast is the degraded answer without data.
func emptyBlast(reason domain.DegradedReason) domain.BlastResult {
	return domain.BlastResult{
		ChangedSymbols: []domain.BlastSymbol{}, Callers: []domain.BlastCaller{}, Degraded: true, Reason: reason,
	}
}

// persistentBlast answers from the index tables.
func (r *Reader) persistentBlast(ctx context.Context, repoID string, changed []string) (domain.BlastResult, error) {
	declRows, err := r.src.SymbolsInFiles(ctx, repoID, changed)
	if err != nil {
		return domain.BlastResult{}, fmt.Errorf("blast radius: read changed symbols: %w", err)
	}
	symbols, names := domain.ChangedSymbols(declRows)
	res := domain.BlastResult{
		ChangedSymbols: nonNil(symbols), Callers: []domain.BlastCaller{}, FactsByFile: map[string]domain.BlastFacts{},
	}
	if len(names) == 0 {
		return res, nil
	}

	rows, err := r.src.ResolvedCallers(ctx, repoID, changed, names)
	if err != nil {
		return domain.BlastResult{}, fmt.Errorf("blast radius: read callers: %w", err)
	}
	if len(rows) == 0 {
		return res, nil
	}
	var callerFiles []string
	seen := map[string]struct{}{}
	for _, c := range rows {
		if _, dup := seen[c.File]; !dup {
			seen[c.File] = struct{}{}
			callerFiles = append(callerFiles, c.File)
		}
	}
	symRows, err := r.src.SymbolsInFiles(ctx, repoID, callerFiles)
	if err != nil {
		return domain.BlastResult{}, fmt.Errorf("blast radius: read caller symbols: %w", err)
	}
	byFile := make(map[string][]domain.LineName, len(callerFiles))
	for _, s := range symRows {
		byFile[s.File] = append(byFile[s.File], domain.LineName{Line: s.StartLine, Name: s.Name})
	}
	res.Callers = domain.AssembleCallers(rows, byFile)
	if len(res.Callers) == 0 {
		res.Callers = []domain.BlastCaller{}
		return res, nil
	}

	facts, err := r.src.FileFacts(ctx, repoID, domain.CallerFiles(res.Callers))
	if err != nil {
		return domain.BlastResult{}, fmt.Errorf("blast radius: read file facts: %w", err)
	}
	for _, f := range facts {
		res.FactsByFile[f.Path] = domain.BlastFacts{Endpoints: nonNil(f.Endpoints), Crons: nonNil(f.Crons)}
	}
	return res, nil
}

// maxConcurrentBlastScans is how many degraded clone scans run at once in one Reader (one per
// process). A scan is CPU-bound on one core for up to the scan timeout, so two keep a core free on
// a small host. What a scan keeps for its whole length is bounded by constants: at most
// domain.MaxBlastScanSymbols symbols (names copied) and domain.MaxBlastScanCallers rows. Beyond
// that it holds only the file it is on (at most about 2 MB: the text, its decoded copy and the
// extracted symbols, which for a file that declares a name on every line is the larger part, a few
// MB), so all scans together stay at tens of MB however many files a clone has. Callers of the same
// repo and changed set share one scan (blastFlights), so the limit only meets different repos or
// changed sets.
const maxConcurrentBlastScans = 2

// blastScan is the degraded path: Node's clone scan with the regex symbol extractor, bounded for
// a hostile clone. It answers the empty degraded result (with reason) for an unknown repo, a repo
// without a clone path, a missing clone, no changed file, a Reader built without WithCallerScan,
// and when the scan's own time limit (the CallerScan timeout, 30 s, counted from the moment the
// call is accepted, a wait for a free scan slot included) ends. The end of ctx itself is an error.
//
// Concurrent calls with the same repo, reason and set of changed files share one scan, and at
// most maxConcurrentBlastScans scans run at a time. A waiting caller stops waiting at once when
// its ctx ends; the scan goes on for the others, and ends when every caller has left.
func (r *Reader) blastScan(ctx context.Context, repoID string, changed []string, reason domain.DegradedReason) (domain.BlastResult, error) {
	if r.scan == nil || len(changed) == 0 {
		return emptyBlast(reason), nil
	}
	res, err := r.blastFlights.Do(ctx, blastScanKey(repoID, reason, changed), nil,
		func(fctx context.Context, _ func(struct{})) (domain.BlastResult, error) {
			return r.runBlastScan(fctx, repoID, changed, reason)
		})
	if err != nil {
		if ctx.Err() != nil { // the caller gave up
			return domain.BlastResult{}, fmt.Errorf("blast radius: %w", ctx.Err())
		}
		return domain.BlastResult{}, err
	}
	return cloneBlast(res), nil
}

// blastScanKey identifies a scan: the changed files are a set (the walk, not their order,
// orders the answer).
func blastScanKey(repoID string, reason domain.DegradedReason, changed []string) string {
	set := slices.Clone(changed)
	slices.Sort(set)
	set = slices.Compact(set)
	return repoID + "\x00" + string(reason) + "\x00" + strings.Join(set, "\x00")
}

// cloneBlast gives each caller of a shared scan its own slices.
func cloneBlast(r domain.BlastResult) domain.BlastResult {
	r.ChangedSymbols = slices.Clone(r.ChangedSymbols)
	r.Callers = slices.Clone(r.Callers)
	if r.FactsByFile != nil {
		m := make(map[string]domain.BlastFacts, len(r.FactsByFile))
		for k, v := range r.FactsByFile {
			m[k] = v
		}
		r.FactsByFile = m
	}
	return r
}

// runBlastScan is one shared scan: it waits for a scan slot and scans, both within the scan
// time limit. ctx is the flight's, which ends when every caller has left.
func (r *Reader) runBlastScan(ctx context.Context, repoID string, changed []string, reason domain.DegradedReason) (domain.BlastResult, error) {
	empty := emptyBlast(reason)
	sctx, cancel := context.WithTimeout(ctx, r.scan.Timeout)
	defer cancel()

	select {
	case r.blastSlots <- struct{}{}:
		defer func() { <-r.blastSlots }()
	case <-sctx.Done():
		if ctx.Err() != nil {
			return domain.BlastResult{}, fmt.Errorf("blast radius: %w", ctx.Err())
		}
		return empty, nil // no free slot within the time limit
	}

	res, err := r.scanClone(sctx, repoID, changed, reason)
	if err == nil {
		return res, nil
	}
	switch {
	case ctx.Err() != nil: // every caller gave up
		return domain.BlastResult{}, fmt.Errorf("blast radius: %w", ctx.Err())
	case sctx.Err() != nil: // our time limit
		return empty, nil
	}
	return domain.BlastResult{}, err
}

// scanClone walks the clone: changed symbols come from the changed files, callers from one
// reference scan of every other file for all the changed names.
func (r *Reader) scanClone(ctx context.Context, repoID string, changed []string, reason domain.DegradedReason) (domain.BlastResult, error) {
	empty := emptyBlast(reason)
	repo, err := r.scan.Repos.GetRepo(ctx, repoID)
	if errors.Is(err, domain.ErrRepoNotFound) {
		return empty, nil
	}
	if err != nil {
		return domain.BlastResult{}, fmt.Errorf("blast radius: read repo: %w", err)
	}
	if repo.ClonePath == "" {
		return empty, nil
	}
	files, err := r.scan.Files.ScanCallers(ctx, repo.ClonePath)
	if errors.Is(err, domain.ErrCloneMissing) {
		return empty, nil
	}
	if err != nil {
		return domain.BlastResult{}, fmt.Errorf("blast radius: %w", err)
	}

	sc := &blastScanner{reader: r, root: repo.ClonePath, reason: reason}
	if err := sc.collectSymbols(ctx, files, changed); err != nil {
		return domain.BlastResult{}, err
	}
	if len(sc.symbols) == 0 {
		return empty, nil
	}
	if err := sc.collectCallers(ctx, files); err != nil {
		return domain.BlastResult{}, err
	}
	return sc.result(), nil
}

// blastScanner holds the state of one degraded scan.
type blastScanner struct {
	reader *Reader
	root   string
	reason domain.DegradedReason

	// symbols are the changed symbols in walk then extraction order; the scan looks for
	// symbols[i].Name in every other file.
	symbols []domain.BlastSymbol
	// The enclosing-symbol index of a file is never kept: it lives only while that file is
	// scanned for callers (enclosingFor). An index kept per file would hold the file's text
	// reachable (names and labels are substrings of it) and, for a file that declares one name
	// on every line, one entry per line while the symbol cap counts the name once.

	// rows[i] are the callers of symbols[i] in scan order; limit[i] is how many of them can still
	// matter and wanted the number of leading symbols that can matter at all (see prune).
	rows       [][]domain.BlastCaller
	limit      []int
	wanted     int
	sinceCheck int
	seen       map[callerRowKey]struct{}
}

type callerRowKey struct{ file, enclosing, name string }

// pruneEvery is how many new caller rows may be collected between two prunes.
const pruneEvery = 256

// collectSymbols extracts the symbols of the changed files that the walk lists, in walk order,
// keeping at most domain.MaxBlastScanSymbols of them (deduplicated by name and file).
func (s *blastScanner) collectSymbols(ctx context.Context, walked, changed []string) error {
	changedSet := make(map[string]struct{}, len(changed))
	for _, c := range changed {
		changedSet[c] = struct{}{}
	}
	type key struct{ name, file string }
	dedup := map[key]struct{}{}
	for _, rel := range walked {
		if _, ok := changedSet[rel]; !ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("blast radius: %w", err)
		}
		raw, err := s.reader.scan.Files.ReadCallerFile(s.root, rel)
		if err != nil {
			continue // Node reads an unreadable file as empty
		}
		syms, err := domain.ExtractSymbols(ctx, domain.DecodeText(raw))
		if err != nil {
			return fmt.Errorf("blast radius: %w", err)
		}
		// The names are substrings of the file text and the symbols outlive it, so each name is
		// copied.
		for i := range syms {
			syms[i].Name = strings.Clone(syms[i].Name)
		}
		for _, x := range syms {
			if len(s.symbols) >= domain.MaxBlastScanSymbols {
				return nil
			}
			k := key{x.Name, rel}
			if _, dup := dedup[k]; dup {
				continue
			}
			dedup[k] = struct{}{}
			s.symbols = append(s.symbols, domain.BlastSymbol{File: rel, Name: x.Name, Kind: x.Kind})
		}
	}
	return nil
}

func enclosingOf(syms []domain.ExtractedSymbol) domain.Enclosing {
	ln := make([]domain.LineName, len(syms))
	for i, x := range syms {
		ln[i] = domain.LineName{Line: x.Line, Name: x.Name}
	}
	return domain.NewEnclosing(ln)
}

// collectCallers reads each walked file once and collects, for every changed symbol, the callers
// found in the files other than its own. The result equals Node's symbol-major loop (rows of a
// symbol in file then line order, a (file, enclosing symbol, name) once) up to the bound of
// domain.MaxBlastScanCallers rows, which keeps the first rows of that order; see prune.
func (s *blastScanner) collectCallers(ctx context.Context, files []string) error {
	n := len(s.symbols)
	names := make([]string, n)
	for i, sym := range s.symbols {
		names[i] = sym.Name
	}
	scanner := domain.NewReferenceScanner(names)
	s.rows = make([][]domain.BlastCaller, n)
	s.limit = make([]int, n)
	for i := range s.limit {
		s.limit[i] = domain.MaxBlastScanCallers
	}
	s.wanted = n
	s.seen = map[callerRowKey]struct{}{}

	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("blast radius: %w", err)
		}
		if s.finished() {
			return nil
		}
		raw, err := s.reader.scan.Files.ReadCallerFile(s.root, rel)
		if err != nil {
			continue
		}
		text := domain.DecodeText(raw)

		var (
			hitErr error
			enc    domain.Enclosing
			haveEn bool
		)
		skip := func(i int) bool {
			return i >= s.wanted || len(s.rows[i]) >= s.limit[i] || s.symbols[i].File == rel
		}
		hit := func(i, line int) (bool, error) {
			if i >= s.wanted {
				return false, nil
			}
			if !haveEn {
				var err error
				if enc, err = s.enclosingFor(ctx, text); err != nil {
					hitErr = err
					return false, err
				}
				haveEn = true
			}
			label := domain.CallerLabel(enc, rel, line)
			k := callerRowKey{rel, label, s.symbols[i].Name}
			if _, dup := s.seen[k]; !dup {
				// The label can be a substring of this file's text; the row and the seen set
				// outlive it.
				label = strings.Clone(label)
				k.enclosing = label
				s.seen[k] = struct{}{}
				s.rows[i] = append(s.rows[i], domain.BlastCaller{File: rel, Symbol: label, ViaSymbol: s.symbols[i].Name, Line: line})
				if s.sinceCheck++; s.sinceCheck >= pruneEvery {
					s.prune()
				}
			}
			return len(s.rows[i]) < s.limit[i], nil
		}
		if err := scanner.Scan(ctx, text, skip, hit); err != nil {
			if hitErr != nil {
				return hitErr
			}
			return fmt.Errorf("blast radius: %w", err)
		}
		s.prune()
	}
	return nil
}

// enclosingFor extracts the enclosing-symbol index of the file being scanned from its text, for the
// caller to use while it scans that file. A changed file that is also scanned as a caller file
// (for the other changed symbols) is extracted a second time rather than kept from
// collectSymbols; the text is the same, so is the index.
func (s *blastScanner) enclosingFor(ctx context.Context, text string) (domain.Enclosing, error) {
	syms, err := domain.ExtractSymbols(ctx, text)
	if err != nil {
		return domain.Enclosing{}, fmt.Errorf("blast radius: %w", err)
	}
	return enclosingOf(syms), nil
}

// prune applies the row bound. The answer is the first domain.MaxBlastScanCallers rows in
// symbol-major order, and the scan finds rows file-major, so a row can only be dropped when the
// rows of the symbols before it already fill the bound: the later symbols are dropped, and the
// symbol the bound falls in is cut to its share. Rows of a dropped symbol are freed.
func (s *blastScanner) prune() {
	s.sinceCheck = 0
	sum := 0
	for j := 0; j < s.wanted; j++ {
		s.limit[j] = domain.MaxBlastScanCallers - sum
		if n := len(s.rows[j]); sum+n >= domain.MaxBlastScanCallers {
			s.rows[j] = s.rows[j][:domain.MaxBlastScanCallers-sum]
			for k := j + 1; k < s.wanted; k++ {
				s.rows[k] = nil
			}
			s.wanted = j + 1
			return
		}
		sum += len(s.rows[j])
	}
}

// finished reports that no further file can change the answer: every symbol that still counts
// holds as many rows as it may.
func (s *blastScanner) finished() bool {
	for j := 0; j < s.wanted; j++ {
		if len(s.rows[j]) < s.limit[j] {
			return false
		}
	}
	return true
}

func (s *blastScanner) result() domain.BlastResult {
	callers := []domain.BlastCaller{}
	for j := 0; j < s.wanted; j++ {
		callers = append(callers, s.rows[j]...)
	}
	if len(callers) > domain.MaxBlastScanCallers {
		callers = callers[:domain.MaxBlastScanCallers]
	}
	return domain.BlastResult{ChangedSymbols: s.symbols, Callers: callers, Degraded: true, Reason: s.reason}
}
