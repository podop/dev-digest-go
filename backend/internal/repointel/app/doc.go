// The use cases of the package: Indexer runs the full and the incremental index, ImportGraph
// builds the import edges, Jobs holds the three job handlers (repo-intel-index, -refresh,
// -resync) and the triggers other contexts use to enqueue them, Backfill enqueues the startup
// index jobs, Service answers the two HTTP routes (index state, resync) and Reader is the
// in-process read port.
//
// # Read port
//
// Reader implements the operations of spec section 7.3 marked Phase 3: RepoMap, FileRank,
// SymbolsInFiles, TopFilesByRank, ConventionSamples, CriticalPaths and ListIndexedFiles
// (IndexState lives on Service), plus CallerSignatures of Phase 4 (section 7.11). With
// REPO_INTEL_ENABLED off they return empty results (RepoMap: reason flag_off). A missing or
// degraded index is never an error; a database error is returned to the caller.
//
// CallerSignatures does not read the index tables except file_rank, which only orders the rows:
// it parses the changed files in the clone and scans the clone with Node's reference rules
// (domain.ReferenceLines; one pass per file for all names: domain.ReferenceScanner), through the
// CallerScan ports given to NewReader by WithCallerScan. The scan has its own walk (clonetree.ScanCallers), never follows a symbolic link, bounds the files,
// the names and the rows, honours ctx and ends after 30 s with an error.
//
// The remaining operations of the contract are later-phase work and have no implementation here:
//
//   - BlastRadius(repoID, changedFiles) returns the Node BlastResult: changed symbols, resolved
//     callers (at most 20 per changed symbol, rank descending), impacted endpoints, facts per
//     caller file, degraded and reason (Phase 5).
//   - UnresolvedReferences(repoID, files) returns the references of the files whose decl_file is
//     still NULL (no consumer yet).
//
// The phase that implements one of them extends Reader with the method of that name.
package app
