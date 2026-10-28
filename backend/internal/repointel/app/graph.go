package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

// ImportGraph is the GraphBuilder: it reads each walked file through the SourceFS, takes its
// import specifiers from the Analyzer and resolves them with the importgraph rules (FR3023).
// A file that cannot be read or parsed contributes no specifier, a malformed tsconfig only
// switches alias resolution off (logged); an error is returned for a cancelled context and for
// a failure of the builder itself, a panic included.
type ImportGraph struct {
	files    domain.SourceFS
	analyzer Analyzer
	log      *slog.Logger
}

var _ GraphBuilder = (*ImportGraph)(nil)

// NewImportGraph builds the graph builder; a nil log discards warnings.
func NewImportGraph(files domain.SourceFS, analyzer Analyzer, log *slog.Logger) *ImportGraph {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ImportGraph{files: files, analyzer: analyzer, log: log}
}

// BuildGraph implements GraphBuilder.
func (g *ImportGraph) BuildGraph(ctx context.Context, root string, files []string) (graph domain.Graph, err error) {
	defer func() {
		if p := recover(); p != nil {
			graph, err = domain.Graph{}, fmt.Errorf("import graph panicked: %v", p)
		}
	}()
	graph, aliasErr, err := importgraph.Build(ctx, g.files, root, files, g.sourceImports(root))
	if err != nil {
		return domain.Graph{}, fmt.Errorf("build import graph: %w", err)
	}
	if aliasErr != nil {
		g.logAliasProblem(ctx, aliasErr)
	}
	return graph, nil
}

// logAliasProblem logs the problem categories and their count, nothing the clone's tsconfig said
// (NFR3005): the error text is for the caller of importgraph, not for the log.
func (g *ImportGraph) logAliasProblem(ctx context.Context, aliasErr error) {
	var ae *importgraph.AliasError
	if !errors.As(aliasErr, &ae) {
		g.log.WarnContext(ctx, "tsconfig aliases not used", "reason", "unknown")
		return
	}
	g.log.WarnContext(ctx, "tsconfig aliases not used",
		"reason", strings.Join(ae.Categories, ","),
		"problems", ae.Count,
		"disabled", ae.Disabled)
}

// sourceImports returns the SourceImports of the clone at root: each file is read and parsed
// under its own parse deadline (P10); any failure means no specifiers and no package (EC8).
func (g *ImportGraph) sourceImports(root string) importgraph.SourceImports {
	return func(ctx context.Context, file string) domain.FileImports {
		raw, err := g.files.ReadFile(root, file)
		if err != nil {
			return domain.FileImports{}
		}
		fctx, cancel := context.WithTimeout(ctx, domain.ParseTimeout)
		defer cancel()
		imports, err := g.analyzer.Imports(fctx, file, domain.DecodeText(raw))
		if err != nil || fctx.Err() != nil {
			return domain.FileImports{}
		}
		return imports
	}
}

// edgeBudgetReason is the graphFailed value of a graph over the edge budget
// (importgraph.ErrEdgeBudget): a fixed word, not the error text.
const edgeBudgetReason = "edge_budget"

// buildGraph runs the GraphBuilder for the indexer: the graph, or the message of a builder
// failure (graphFailed, empty graph; edgeBudgetReason for importgraph.ErrEdgeBudget). It returns
// an error only when ctx ended.
func (ix *Indexer) buildGraph(ctx context.Context, root string, files []string) (graph domain.Graph, graphFailed string, err error) {
	defer func() {
		if p := recover(); p != nil {
			graph, graphFailed, err = domain.Graph{}, errorText(ix.d.Redact, fmt.Sprintf("import graph panicked: %v", p)), nil
		}
	}()
	graph, gerr := ix.d.Graph.BuildGraph(ctx, root, files)
	if gerr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.Graph{}, "", fmt.Errorf("build import graph: %w", ctxErr)
		}
		if errors.Is(gerr, importgraph.ErrEdgeBudget) {
			return domain.Graph{}, edgeBudgetReason, nil
		}
		return domain.Graph{}, errorText(ix.d.Redact, gerr.Error()), nil
	}
	return graph, "", nil
}
