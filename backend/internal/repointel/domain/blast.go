package domain

import "time"

// Limits of the BlastRadius operation (Phase 5 spec 7.7).
const (
	// MaxCallersPerSymbol keeps at most this many caller rows per changed symbol (rank order) on
	// the persistent path.
	MaxCallersPerSymbol = 20
	// BlastScanTimeout ends the degraded clone scan; the call then answers its empty result.
	BlastScanTimeout = 30 * time.Second
	// MaxBlastScanSymbols bounds the changed symbol names the degraded scan looks for.
	MaxBlastScanSymbols = MaxCallerNames
	// MaxBlastScanCallers stops the degraded scan's collection at this many caller rows.
	MaxBlastScanCallers = 5000
)

// BlastSymbol is a symbol declared in a changed file.
type BlastSymbol struct {
	File string
	Name string
	// Kind is the symbol's kind as stored (function, class, method, interface, type, enum).
	Kind string
}

// BlastCaller is a caller of a changed symbol, labelled with its enclosing symbol.
type BlastCaller struct {
	// File is the caller file, slash-separated and relative to the clone.
	File string
	// Symbol is the enclosing symbol of the call site: the caller file's symbol without a "." in
	// its name with the largest line not after the reference line, else the file's base name.
	Symbol string
	// ViaSymbol is the changed symbol this caller reaches.
	ViaSymbol string
	// Line is the 1-based line of the reference.
	Line int
	// Rank is the file_rank.rank of the caller file; 0 on the degraded path.
	Rank float64
}

// BlastFacts are the endpoints and cron expressions a caller file declares (file_facts).
type BlastFacts struct {
	Endpoints []string
	Crons     []string
}

// BlastResult is the BlastRadius read port result (Node BlastResult). Both paths return a
// non-error result for an unusable index.
type BlastResult struct {
	// ChangedSymbols are the symbols declared in the changed files, deduplicated by (name, file).
	ChangedSymbols []BlastSymbol
	// Callers are in rank order, descending; at most MaxCallersPerSymbol per ViaSymbol on the
	// persistent path.
	Callers []BlastCaller
	// FactsByFile holds the facts of every caller file that has some; nil on the degraded path.
	FactsByFile map[string]BlastFacts
	// Degraded is true when the answer is not a complete persistent one; Reason then says why
	// (flag_off, no_data, index_failed, index_partial, repo_too_large) and is "" otherwise.
	Degraded bool
	Reason   DegradedReason
}
