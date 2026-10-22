package onboardinghttp

import "dev-digest-go/backend/internal/onboarding/domain"

// The wire shapes of spec 7.3 (contract file onboarding.ts). Struct field order is the key order
// of the JSON; nullable fields are pointers without omitempty and slices are never nil, so an
// empty list is [] and not null. Timestamps are strings in the form of JavaScript's toISOString.

// timeLayout is JavaScript's toISOString: UTC with milliseconds.
const timeLayout = "2006-01-02T15:04:05.000Z"

type noneDTO struct {
	Status string `json:"status"`
}

type readyDTO struct {
	Status      string  `json:"status"`
	Stale       bool    `json:"stale"`
	StaleReason string  `json:"stale_reason,omitempty"`
	Tour        tourDTO `json:"tour"`
}

type tourDTO struct {
	RepoID        string          `json:"repo_id"`
	GeneratedAt   string          `json:"generated_at"`
	IndexedSHA    string          `json:"indexed_sha"`
	FilesIndexed  int             `json:"files_indexed"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	TokensIn      int             `json:"tokens_in"`
	TokensOut     int             `json:"tokens_out"`
	CostUSD       *float64        `json:"cost_usd"`
	PromptVersion int             `json:"prompt_version"`
	Language      string          `json:"language"`
	Architecture  architectureDTO `json:"architecture"`
	CriticalPaths []pathItemDTO   `json:"critical_paths"`
	RunSteps      []runStepDTO    `json:"run_steps"`
	ReadingPath   []pathItemDTO   `json:"reading_path"`
	FirstTasks    []firstTaskDTO  `json:"first_tasks"`
}

type architectureDTO struct {
	Summary string    `json:"summary"`
	Nodes   []nodeDTO `json:"nodes"`
	Edges   []edgeDTO `json:"edges"`
}

type nodeDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

type edgeDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type pathItemDTO struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// runStepDTO carries the comment only when there is one.
type runStepDTO struct {
	Command string `json:"command"`
	Comment string `json:"comment,omitempty"`
}

type firstTaskDTO struct {
	Title      string `json:"title"`
	Path       string `json:"path"`
	Complexity string `json:"complexity"`
}

// toState is the response body of a read or a generation: {"status":"none"} for no tour, else
// the ready shape.
func toState(s domain.State) any {
	if s.Tour == nil {
		return noneDTO{Status: "none"}
	}
	d := readyDTO{Status: "ready", Stale: s.Stale, Tour: toTour(*s.Tour)}
	if s.Stale {
		d.StaleReason = string(s.StaleReason)
	}
	return d
}

func toTour(t domain.Tour) tourDTO {
	d := tourDTO{
		RepoID: t.RepoID, GeneratedAt: t.GeneratedAt.UTC().Format(timeLayout), IndexedSHA: t.IndexedSHA,
		FilesIndexed: t.FilesIndexed, Provider: t.Provider, Model: t.Model, TokensIn: t.TokensIn,
		TokensOut: t.TokensOut, CostUSD: t.CostUSD, PromptVersion: t.PromptVersion, Language: t.Language,
		Architecture: architectureDTO{
			Summary: t.Architecture.Summary,
			Nodes:   make([]nodeDTO, len(t.Architecture.Nodes)),
			Edges:   make([]edgeDTO, len(t.Architecture.Edges)),
		},
		CriticalPaths: make([]pathItemDTO, len(t.CriticalPaths)),
		RunSteps:      make([]runStepDTO, len(t.RunSteps)),
		ReadingPath:   make([]pathItemDTO, len(t.ReadingPath)),
		FirstTasks:    make([]firstTaskDTO, len(t.FirstTasks)),
	}
	for i, n := range t.Architecture.Nodes {
		d.Architecture.Nodes[i] = nodeDTO{ID: n.ID, Label: n.Label, Kind: string(n.Kind)}
	}
	for i, e := range t.Architecture.Edges {
		d.Architecture.Edges[i] = edgeDTO{From: e.From, To: e.To}
	}
	for i, p := range t.CriticalPaths {
		d.CriticalPaths[i] = pathItemDTO{Path: p.Path, Reason: p.Reason}
	}
	for i, s := range t.RunSteps {
		d.RunSteps[i] = runStepDTO{Command: s.Command, Comment: s.Comment}
	}
	for i, p := range t.ReadingPath {
		d.ReadingPath[i] = pathItemDTO{Path: p.Path, Reason: p.Reason}
	}
	for i, f := range t.FirstTasks {
		d.FirstTasks[i] = firstTaskDTO{Title: f.Title, Path: f.Path, Complexity: string(f.Complexity)}
	}
	return d
}
