package domain

// Turns the model's loose output into the stored tour content (spec 7.10.5, Node's
// onboarding/domain/tour.ts). The model only PROPOSES: every path is checked against the indexed
// files, text is clamped to the stored limits (so a stored tour always passes the contract), and a
// section below its minimum is emptied, not failed.

import (
	"strings"

	rdomain "dev-digest-go/backend/internal/reviewer/domain"
)

// TourContent is the five sections of a tour. Every list is non-nil.
type TourContent struct {
	Architecture  Architecture
	CriticalPaths []PathItem
	RunSteps      []RunStep
	ReadingPath   []PathItem
	FirstTasks    []FirstTask
}

// Dropped counts the items removed per section (counts only, never repository text; they go to
// the outcome log line).
type Dropped struct {
	Nodes, Edges, CriticalPaths, RunSteps, ReadingPath, FirstTasks int
}

// NormalizePath turns "./src/a.ts" and "/src/a.ts" into "src/a.ts" and removes trailing slashes
// (a folder): trim, leading "./" repeatedly, leading "/", trailing "/".
func NormalizePath(raw string) string {
	s := rdomain.JSTrim(raw)
	for strings.HasPrefix(s, "./") {
		s = s[2:]
	}
	return strings.TrimRight(strings.TrimLeft(s, "/"), "/")
}

// folderSet holds every ancestor folder of every file ("a/b/c.ts" gives "a" and "a/b"; a path
// starting with "/" has none, as in Node's loop).
func folderSet(files map[string]bool) map[string]bool {
	folders := map[string]bool{}
	for f := range files {
		for i := strings.IndexByte(f, '/'); i > 0; {
			folders[f[:i]] = true
			j := strings.IndexByte(f[i+1:], '/')
			if j < 0 {
				break
			}
			i += 1 + j
		}
	}
	return folders
}

// pathChecker accepts a proposed path once: normalised, non-empty, at most MaxPath units, not
// seen before, a known file (or, with allowFolders, a folder with files under it).
type pathChecker struct {
	files, folders map[string]bool
	allowFolders   bool
	seen           map[string]bool
}

func (c *pathChecker) check(raw string) (string, bool) {
	p := NormalizePath(raw)
	if p == "" || rdomain.Len16(p) > MaxPath || c.seen[p] {
		return "", false
	}
	if !c.files[p] && !(c.allowFolders && c.folders[p]) {
		return "", false
	}
	c.seen[p] = true
	return p, true
}

func pathSection(items []LLMPath, files, folders map[string]bool, min, max int) []PathItem {
	c := &pathChecker{files: files, folders: folders, seen: map[string]bool{}}
	out := []PathItem{}
	for _, it := range items {
		if len(out) == max {
			break
		}
		if p, ok := c.check(it.Path); ok {
			out = append(out, PathItem{Path: p, Reason: rdomain.Cut(it.Reason, MaxReason)})
		}
	}
	if len(out) < min {
		return []PathItem{}
	}
	return out
}

func clampID(id string) string { return rdomain.PrefixUnits(rdomain.JSTrim(id), MaxNodeID) }

func architectureOf(raw LLMArchitecture) (arch Architecture, droppedNodes, droppedEdges int) {
	nodes := []Node{}
	ids := map[string]bool{}
	for _, n := range raw.Nodes {
		id := clampID(n.ID)
		label := rdomain.Cut(n.Label, MaxNodeLabel)
		if id == "" || label == "" || ids[id] {
			continue
		}
		ids[id] = true
		nodes = append(nodes, Node{ID: id, Label: label, Kind: NodeKind(n.Kind)})
	}
	if len(nodes) > MaxNodes {
		nodes = nodes[:MaxNodes]
	}
	summary := rdomain.Cut(raw.Summary, MaxSummary)
	if len(nodes) < MinNodes {
		return Architecture{Summary: summary, Nodes: []Node{}, Edges: []Edge{}}, len(raw.Nodes), len(raw.Edges)
	}
	known := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		known[n.ID] = true
	}
	edges := []Edge{}
	seen := map[Edge]bool{}
	for _, e := range raw.Edges {
		if len(edges) == MaxEdges {
			break
		}
		clamped := Edge{From: clampID(e.From), To: clampID(e.To)}
		if !known[clamped.From] || !known[clamped.To] || seen[clamped] {
			continue
		}
		seen[clamped] = true
		edges = append(edges, clamped)
	}
	return Architecture{Summary: summary, Nodes: nodes, Edges: edges}, len(raw.Nodes) - len(nodes), len(raw.Edges) - len(edges)
}

func runStepsOf(raw []LLMRunStep) []RunStep {
	steps := []RunStep{}
	for _, s := range raw {
		if len(steps) == MaxRunSteps {
			break
		}
		command := rdomain.JSTrim(s.Command)
		if command == "" || rdomain.Len16(command) > MaxCommand || IsDangerousCommand(command) {
			continue
		}
		steps = append(steps, RunStep{Command: command, Comment: rdomain.Cut(s.Comment, MaxComment)})
	}
	if len(steps) < MinRunSteps {
		return []RunStep{}
	}
	return steps
}

func firstTasksOf(raw []LLMTask, files, folders map[string]bool) []FirstTask {
	c := &pathChecker{files: files, folders: folders, allowFolders: true, seen: map[string]bool{}}
	out := []FirstTask{}
	for _, t := range raw {
		if len(out) == MaxFirstTasks {
			break
		}
		// the path is checked (and remembered) before the title is looked at: a task with an
		// empty title still blocks a later task on the same path
		p, ok := c.check(t.Path)
		if !ok {
			continue
		}
		if title := rdomain.Cut(t.Title, MaxTaskTitle); title != "" {
			out = append(out, FirstTask{Title: title, Path: p, Complexity: Complexity(t.Complexity)})
		}
	}
	return out
}

// NormalizeTour validates and clamps the model's output. indexedPaths are all the indexed files
// of the repository: a critical or reading path must be one of them exactly; a first-task path
// may also be a folder with indexed files under it. A run step the command filter flags is
// dropped (and counted).
func NormalizeTour(raw LLMOutput, indexedPaths []string) (TourContent, Dropped) {
	files := make(map[string]bool, len(indexedPaths))
	for _, p := range indexedPaths {
		files[p] = true
	}
	folders := folderSet(files)
	arch, droppedNodes, droppedEdges := architectureOf(raw.Architecture)
	critical := pathSection(raw.CriticalPaths, files, folders, MinCriticalPaths, MaxCriticalPaths)
	reading := pathSection(raw.ReadingPath, files, folders, MinReadingPath, MaxReadingPath)
	runSteps := runStepsOf(raw.RunSteps)
	tasks := firstTasksOf(raw.FirstTasks, files, folders)
	return TourContent{
		Architecture: arch, CriticalPaths: critical, RunSteps: runSteps, ReadingPath: reading, FirstTasks: tasks,
	}, Dropped{
		Nodes: droppedNodes, Edges: droppedEdges,
		CriticalPaths: len(raw.CriticalPaths) - len(critical),
		RunSteps:      len(raw.RunSteps) - len(runSteps),
		ReadingPath:   len(raw.ReadingPath) - len(reading),
		FirstTasks:    len(raw.FirstTasks) - len(tasks),
	}
}
