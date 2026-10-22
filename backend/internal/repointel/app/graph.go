package app

import (
	"context"
	"fmt"
	"log/slog"

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

// BuildEdges implements GraphBuilder.
func (g *ImportGraph) BuildEdges(ctx context.Context, root string, files []string) (edges []domain.Edge, err error) {
	defer func() {
		if p := recover(); p != nil {
			edges, err = nil, fmt.Errorf("import graph panicked: %v", p)
		}
	}()
	edges, aliasErr, err := importgraph.Build(ctx, g.files, root, files, g.specifiers(root))
	if err != nil {
		return nil, fmt.Errorf("build import graph: %w", err)
	}
	if aliasErr != nil {
		// The text can quote a key of the clone's tsconfig, never source code.
		g.log.WarnContext(ctx, "tsconfig aliases not used", "error", domain.ClampUTF16(aliasErr.Error(), domain.MaxErrorChars))
	}
	return edges, nil
}

// specifiers returns the SpecifierSource of the clone at root: each file is read and parsed
// under its own parse deadline (P10); any failure means no specifiers.
func (g *ImportGraph) specifiers(root string) importgraph.SpecifierSource {
	return func(ctx context.Context, file string) []string {
		raw, err := g.files.ReadFile(root, file)
		if err != nil {
			return nil
		}
		fctx, cancel := context.WithTimeout(ctx, domain.ParseTimeout)
		defer cancel()
		specs, err := g.analyzer.Imports(fctx, file, domain.DecodeText(raw))
		if err != nil || fctx.Err() != nil {
			return nil
		}
		return specs
	}
}

// buildGraph runs the GraphBuilder for the indexer: edges, or the message of a builder failure
// (graphFailed, no edges). It returns an error only when ctx ended.
func (ix *Indexer) buildGraph(ctx context.Context, root string, files []string) (edges []domain.Edge, graphFailed string, err error) {
	defer func() {
		if p := recover(); p != nil {
			edges, graphFailed, err = nil, errorText(ix.d.Redact, fmt.Sprintf("import graph panicked: %v", p)), nil
		}
	}()
	edges, gerr := ix.d.Graph.BuildEdges(ctx, root, files)
	if gerr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, "", fmt.Errorf("build import graph: %w", ctxErr)
		}
		return nil, errorText(ix.d.Redact, gerr.Error()), nil
	}
	return edges, "", nil
}
