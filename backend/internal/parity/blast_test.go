package parity_test

// S30 (AC5038, AC5039, AC5042): the blast context against testdata/goldens/blast.json — the mapping
// of the repo-intel port answer to the BlastRadius read model, the prior-PR selection, and
// Reader.BlastRadius on both paths: the persistent one over index tables seeded into a real
// Postgres (the Node reader's SQL is emulated by the generator with the fixed orders of D5009) and
// the degraded clone scan over the corpus clone `shop`. The BlastService cases (logs, GitHub
// failures) belong to the service of lane N (S25).

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	blast "dev-digest-go/backend/internal/blast/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/repointel/adapters/clonetree"
	repointelpg "dev-digest-go/backend/internal/repointel/adapters/postgres"
	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// --- the port answer in the generator's JSON shape ---------------------------------------

type portSymbol struct {
	File string `json:"file"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type portCaller struct {
	File      string  `json:"file"`
	Symbol    string  `json:"symbol"`
	ViaSymbol string  `json:"viaSymbol"`
	Line      int     `json:"line"`
	Rank      float64 `json:"rank"`
}

type portFacts struct {
	Endpoints []string `json:"endpoints"`
	Crons     []string `json:"crons"`
}

// portWire is the Node BlastResult (without impactedEndpoints, which is never part of a response).
type portWire struct {
	ChangedSymbols []portSymbol         `json:"changedSymbols"`
	Callers        []portCaller         `json:"callers"`
	FactsByFile    map[string]portFacts `json:"factsByFile,omitempty"`
	Degraded       bool                 `json:"degraded"`
	Reason         string               `json:"reason,omitempty"`
}

// wireOfRepointel is the Go port answer of the repo-intel read in the golden's shape.
func wireOfRepointel(res domain.BlastResult) portWire {
	w := portWire{Degraded: res.Degraded, Reason: string(res.Reason)}
	for _, s := range res.ChangedSymbols {
		w.ChangedSymbols = append(w.ChangedSymbols, portSymbol{File: s.File, Name: s.Name, Kind: s.Kind})
	}
	for _, c := range res.Callers {
		w.Callers = append(w.Callers, portCaller{File: c.File, Symbol: c.Symbol, ViaSymbol: c.ViaSymbol, Line: c.Line, Rank: c.Rank})
	}
	for f, x := range res.FactsByFile {
		if w.FactsByFile == nil {
			w.FactsByFile = map[string]portFacts{}
		}
		w.FactsByFile[f] = portFacts{Endpoints: x.Endpoints, Crons: x.Crons}
	}
	return w
}

// blastPort is the port answer as the blast context reads it (the peers adapter of lane N maps
// the repo-intel result to it).
func (w portWire) blastPort() blast.PortResult {
	res := blast.PortResult{Degraded: w.Degraded, Reason: blast.Reason(w.Reason)}
	for _, s := range w.ChangedSymbols {
		res.ChangedSymbols = append(res.ChangedSymbols, blast.ChangedSymbol{Name: s.Name, File: s.File, Kind: s.Kind})
	}
	for _, c := range w.Callers {
		res.Callers = append(res.Callers, blast.Caller{File: c.File, Symbol: c.Symbol, ViaSymbol: c.ViaSymbol, Line: c.Line, Rank: c.Rank})
	}
	if w.FactsByFile != nil {
		res.FactsByFile = map[string]blast.Facts{}
		for f, x := range w.FactsByFile {
			res.FactsByFile[f] = blast.Facts{Endpoints: x.Endpoints, Crons: x.Crons}
		}
	}
	return res
}

// --- the BlastRadius read model in the wire shape ----------------------------------------

type wireBlast struct {
	ChangedSymbols []wireChanged    `json:"changed_symbols"`
	Downstream     []wireDownstream `json:"downstream"`
	Summary        string           `json:"summary"`
	Degraded       bool             `json:"degraded"`
	Reason         string           `json:"reason,omitempty"`
}

type wireChanged struct {
	Name string `json:"name"`
	File string `json:"file"`
	Kind string `json:"kind"`
}

type wireCaller struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type wireDownstream struct {
	Symbol            string       `json:"symbol"`
	Callers           []wireCaller `json:"callers"`
	EndpointsAffected []string     `json:"endpoints_affected"`
	CronsAffected     []string     `json:"crons_affected"`
}

// wireOfRadius renders the read model like the response of GET /pulls/:id/blast (spec 7.7);
// `reason` is written whenever the model carries one.
func wireOfRadius(r blast.BlastRadius) wireBlast {
	w := wireBlast{
		ChangedSymbols: []wireChanged{}, Downstream: []wireDownstream{}, Summary: r.Summary,
		Degraded: r.Degraded, Reason: string(r.Reason),
	}
	for _, s := range r.ChangedSymbols {
		w.ChangedSymbols = append(w.ChangedSymbols, wireChanged{Name: s.Name, File: s.File, Kind: s.Kind})
	}
	for _, d := range r.Downstream {
		wd := wireDownstream{Symbol: d.Symbol, Callers: []wireCaller{}, EndpointsAffected: d.EndpointsAffected, CronsAffected: d.CronsAffected}
		for _, c := range d.Callers {
			wd.Callers = append(wd.Callers, wireCaller{Name: c.Name, File: c.File, Line: c.Line})
		}
		w.Downstream = append(w.Downstream, wd)
	}
	return w
}

// checkBlast compares the read model built from the port answer with the golden's `blast`.
func checkBlast(t *testing.T, port portWire, want wireBlast) {
	t.Helper()
	got := wireOfRadius(blast.BuildRadius(port.blastPort()))
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("BuildRadius(%+v) mismatch (-want +got):\n%s", port, diff)
	}
}

// --- AC5042: mapping, history ------------------------------------------------------------

func TestBlastGolden_Mapping(t *testing.T) {
	var g struct {
		Mapping []struct {
			Name   string    `json:"name"`
			Result portWire  `json:"result"`
			Blast  wireBlast `json:"blast"`
		} `json:"mapping"`
	}
	loadGolden(t, "blast", &g)
	if len(g.Mapping) == 0 {
		t.Fatal("blast.json has no mapping cases")
	}
	for _, tc := range g.Mapping {
		t.Run(tc.Name, func(t *testing.T) { checkBlast(t, tc.Result, tc.Blast) })
	}
}

func TestBlastGolden_Summary(t *testing.T) {
	var g struct {
		Mapping []struct {
			Name  string    `json:"name"`
			Blast wireBlast `json:"blast"`
		} `json:"mapping"`
	}
	loadGolden(t, "blast", &g)
	for _, tc := range g.Mapping {
		var downstream []blast.Downstream
		for _, d := range tc.Blast.Downstream {
			bd := blast.Downstream{Symbol: d.Symbol, EndpointsAffected: d.EndpointsAffected, CronsAffected: d.CronsAffected}
			for _, c := range d.Callers {
				bd.Callers = append(bd.Callers, blast.CallerRef{Name: c.Name, File: c.File, Line: c.Line})
			}
			downstream = append(downstream, bd)
		}
		if got := blast.Summary(len(tc.Blast.ChangedSymbols), downstream); got != tc.Blast.Summary {
			t.Errorf("%s: Summary() = %q, want %q", tc.Name, got, tc.Blast.Summary)
		}
	}
}

func TestBlastGolden_History(t *testing.T) {
	var g struct {
		History []struct {
			Name    string `json:"name"`
			Current struct {
				Number int      `json:"number"`
				Files  []string `json:"files"`
			} `json:"current"`
			Candidates []struct {
				Number   int      `json:"number"`
				Title    string   `json:"title"`
				Author   string   `json:"author"`
				MergedAt string   `json:"merged_at"`
				Files    []string `json:"files"`
			} `json:"candidates"`
			Max    int `json:"max"`
			Result []struct {
				PRNumber     int      `json:"pr_number"`
				Title        string   `json:"title"`
				MergedAt     string   `json:"merged_at"`
				Author       string   `json:"author"`
				FilesOverlap []string `json:"files_overlap"`
				Notes        string   `json:"notes"`
			} `json:"result"`
		} `json:"history"`
	}
	loadGolden(t, "blast", &g)
	if len(g.History) == 0 {
		t.Fatal("blast.json has no history cases")
	}
	for _, tc := range g.History {
		t.Run(tc.Name, func(t *testing.T) {
			var candidates []blast.MergedPR
			for _, c := range tc.Candidates {
				candidates = append(candidates, blast.MergedPR{Number: c.Number, Title: c.Title, Author: c.Author, MergedAt: c.MergedAt, Files: c.Files})
			}
			got := blast.SelectPriorPRs(candidates, tc.Current.Number, tc.Current.Files)
			// The answer is capped at blast.HistoryMax (10); a golden with a smaller cap is the
			// same sorted list cut shorter.
			if tc.Max < blast.HistoryMax && len(got) > tc.Max {
				got = got[:tc.Max]
			} else if tc.Max != blast.HistoryMax && tc.Max > blast.HistoryMax {
				t.Fatalf("golden cap %d is above blast.HistoryMax", tc.Max)
			}
			type prior struct {
				PRNumber     int
				Title        string
				MergedAt     string
				Author       string
				FilesOverlap []string
			}
			var gotPriors, wantPriors []prior
			for _, p := range got {
				gotPriors = append(gotPriors, prior{p.PRNumber, p.Title, p.MergedAt, p.Author, p.FilesOverlap})
			}
			for _, p := range tc.Result {
				if p.Notes != "" {
					t.Errorf("golden notes = %q, want the constant empty string", p.Notes)
				}
				wantPriors = append(wantPriors, prior{p.PRNumber, p.Title, p.MergedAt, p.Author, p.FilesOverlap})
			}
			if diff := cmp.Diff(wantPriors, gotPriors, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("SelectPriorPRs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- AC5038 / AC5039: Reader.BlastRadius -------------------------------------------------

type goldenIndexState struct {
	Status         string         `json:"status"`
	FilesIndexed   int            `json:"files_indexed"`
	FilesSkipped   int            `json:"files_skipped"`
	LastIndexedSHA string         `json:"last_indexed_sha"`
	IndexerVersion int            `json:"indexer_version"`
	Stats          map[string]any `json:"stats"`
}

// blastEnv is a migrated database with one workspace; every case gets its own repository, so the
// index tables of two cases never meet.
type blastEnv struct {
	t     *testing.T
	db    *gorm.DB
	store *repointelpg.Store
	ws    string
}

func newBlastEnv(t *testing.T) *blastEnv {
	t.Helper()
	d := pgtest.Migrated(t)
	e := &blastEnv{t: t, db: d.DB, store: repointelpg.NewStore(d.DB)}
	if err := d.DB.Raw(`INSERT INTO workspaces (name) VALUES ('default') RETURNING id`).Scan(&e.ws).Error; err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return e
}

func (e *blastEnv) exec(query string, args ...any) {
	e.t.Helper()
	if err := e.db.Exec(query, args...).Error; err != nil {
		e.t.Fatalf("seed %q: %v", query, err)
	}
}

// repo inserts a repository (clonePath "" = no clone) and returns its id.
func (e *blastEnv) repo(name, owner, repoName, clonePath string) string {
	e.t.Helper()
	var path *string
	if clonePath != "" {
		path = &clonePath
	}
	var id string
	err := e.db.Raw(`INSERT INTO repos (workspace_id, owner, name, full_name, default_branch, clone_path)
		VALUES (?, ?, ?, ?, 'main', ?) RETURNING id`, e.ws, owner, repoName+"-"+name, owner+"/"+repoName+"-"+name, path).Scan(&id).Error
	if err != nil {
		e.t.Fatalf("seed repo: %v", err)
	}
	return id
}

func (e *blastEnv) state(repo string, st goldenIndexState) {
	e.t.Helper()
	stats, err := json.Marshal(st.Stats)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO repo_index_state (repo_id, last_indexed_sha, indexer_version, status, files_indexed, files_skipped, stats)
		VALUES (?, ?, ?, ?, ?, ?, ?::jsonb)`, repo, st.LastIndexedSHA, st.IndexerVersion, st.Status, st.FilesIndexed, st.FilesSkipped, string(stats))
}

// brokenState is a store whose index state cannot be read (Node tryGetIndexState: counts as none).
type brokenState struct{ *repointelpg.Store }

func (brokenState) GetState(context.Context, string) (domain.IndexState, bool, error) {
	return domain.IndexState{}, false, errors.New("relation repo_index_state is unreadable")
}

func (e *blastEnv) reader(enabled bool, broken bool) *app.Reader {
	scan := app.WithCallerScan(app.CallerScan{Repos: e.store, Files: clonetree.New(), Analyzer: treesitter.New()})
	if broken {
		return app.NewReader(brokenState{e.store}, enabled, scan)
	}
	return app.NewReader(e.store, enabled, scan)
}

type goldenTables struct {
	Symbols []struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Line    int    `json:"line"`
		EndLine int    `json:"end_line"`
	} `json:"symbols"`
	References []struct {
		FromPath string  `json:"from_path"`
		ToSymbol string  `json:"to_symbol"`
		Line     int     `json:"line"`
		DeclFile *string `json:"decl_file"`
	} `json:"references"`
	FileRank []struct {
		Path string  `json:"path"`
		Rank float64 `json:"rank"`
	} `json:"file_rank"`
	FileFacts []struct {
		Path      string   `json:"path"`
		Endpoints []string `json:"endpoints"`
		Crons     []string `json:"crons"`
	} `json:"file_facts"`
}

// AC5038: the persistent corpus over the real tables.
func TestBlastGolden_PersistentPath(t *testing.T) {
	var g struct {
		Persistent []struct {
			Name         string           `json:"name"`
			Enabled      bool             `json:"enabled"`
			ChangedFiles []string         `json:"changedFiles"`
			IndexState   goldenIndexState `json:"index_state"`
			Tables       goldenTables     `json:"tables"`
			Port         portWire         `json:"port"`
			Blast        wireBlast        `json:"blast"`
		} `json:"persistent"`
	}
	loadGolden(t, "blast", &g)
	if len(g.Persistent) == 0 {
		t.Fatal("blast.json has no persistent cases")
	}
	e := newBlastEnv(t)
	for _, tc := range g.Persistent {
		t.Run(tc.Name, func(t *testing.T) {
			e.t = t
			repo := e.repo(tc.Name, "acme", "shop", "")
			e.state(repo, tc.IndexState)
			for _, s := range tc.Tables.Symbols {
				e.exec(`INSERT INTO symbols (repo_id, path, name, kind, line, end_line) VALUES (?, ?, ?, ?, ?, ?)`, repo, s.Path, s.Name, s.Kind, s.Line, s.EndLine)
			}
			for _, r := range tc.Tables.References {
				e.exec(`INSERT INTO "references" (repo_id, from_path, to_symbol, line, decl_file) VALUES (?, ?, ?, ?, ?)`, repo, r.FromPath, r.ToSymbol, r.Line, r.DeclFile)
			}
			for _, r := range tc.Tables.FileRank {
				e.exec(`INSERT INTO file_rank (repo_id, file_path, pagerank, hotness, rank, percentile) VALUES (?, ?, 0, 0, ?, 50)`, repo, r.Path, r.Rank)
			}
			for _, f := range tc.Tables.FileFacts {
				endpoints, _ := json.Marshal(nonNilStrings(f.Endpoints))
				crons, _ := json.Marshal(nonNilStrings(f.Crons))
				e.exec(`INSERT INTO file_facts (repo_id, file_path, endpoints, crons) VALUES (?, ?, ?::jsonb, ?::jsonb)`, repo, f.Path, string(endpoints), string(crons))
			}

			res, err := e.reader(tc.Enabled, false).BlastRadius(t.Context(), repo, tc.ChangedFiles)
			if err != nil {
				t.Fatalf("BlastRadius(%v) error = %v", tc.ChangedFiles, err)
			}
			got := wireOfRepointel(res)
			if diff := cmp.Diff(tc.Port, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("BlastRadius(%v) port result mismatch (-want +got):\n%s", tc.ChangedFiles, diff)
			}
			checkBlast(t, got, tc.Blast)
		})
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// materialize copies the corpus clone `fixture` into a temp directory and writes extraFiles.
func materialize(t *testing.T, fixture string, extra []goldenExtraFile) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("testdata", "corpus", "blast", "clones", fixture)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture %s: %v", fixture, err)
	}
	for _, f := range extra {
		full := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		switch {
		case f.Link != "":
			if err := os.Symlink(f.Link, full); err != nil {
				t.Fatalf("symlink %s: %v", f.Path, err)
			}
		case f.PadTo > 0:
			// content, then "//" and "p" up to padTo bytes (the generator's padding).
			body := f.Content + "//" + strings.Repeat("p", f.PadTo-len(f.Content)-2)
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.WriteFile(full, []byte(f.Content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

type goldenExtraFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	PadTo   int    `json:"padTo"`
	Link    string `json:"link"`
}

// AC5039: the degraded clone scan (switch off, no index row, a failed or degraded index, no
// clone, no repository).
func TestBlastGolden_CloneScan(t *testing.T) {
	var g struct {
		Clone []struct {
			Name         string            `json:"name"`
			Fixture      string            `json:"fixture"`
			Enabled      bool              `json:"enabled"`
			NoClone      bool              `json:"noClone"`
			NoRepo       bool              `json:"noRepo"`
			IndexState   json.RawMessage   `json:"index_state"`
			ChangedFiles []string          `json:"changedFiles"`
			ExtraFiles   []goldenExtraFile `json:"extraFiles"`
			ExtraSizes   map[string]int64  `json:"extraFileSizes"`
			Port         portWire          `json:"port"`
			Blast        wireBlast         `json:"blast"`
		} `json:"clone"`
	}
	loadGolden(t, "blast", &g)
	if len(g.Clone) == 0 {
		t.Fatal("blast.json has no clone cases")
	}
	e := newBlastEnv(t)
	for _, tc := range g.Clone {
		t.Run(tc.Name, func(t *testing.T) {
			e.t = t
			root := materialize(t, tc.Fixture, tc.ExtraFiles)
			for p, size := range tc.ExtraSizes {
				st, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
				if err != nil || st.Size() != size {
					t.Fatalf("fixture file %s: size %v, %v, want %d", p, st, err, size)
				}
			}
			clone := root
			if tc.NoClone {
				clone = ""
			}
			repo := e.repo(tc.Name, "acme", "shop", clone)
			if tc.NoRepo {
				repo = "00000000-0000-4000-8000-000000000404"
			}
			broken := string(tc.IndexState) == `"error"`
			if len(tc.IndexState) > 0 && string(tc.IndexState) != "null" && !broken {
				var st goldenIndexState
				if err := json.Unmarshal(tc.IndexState, &st); err != nil {
					t.Fatalf("decode index_state: %v", err)
				}
				e.state(repo, st)
			}

			res, err := e.reader(tc.Enabled, broken).BlastRadius(t.Context(), repo, tc.ChangedFiles)
			if err != nil {
				t.Fatalf("BlastRadius(%v) error = %v", tc.ChangedFiles, err)
			}
			got := wireOfRepointel(res)
			if diff := cmp.Diff(tc.Port, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("BlastRadius(%v) port result mismatch (-want +got):\n%s", tc.ChangedFiles, diff)
			}
			checkBlast(t, got, tc.Blast)
		})
	}
}
