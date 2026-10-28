package parity_test

// S31 (AC5050, AC5051, AC5052): the pure functions of the onboarding context against the Node
// golden onboarding.json: redaction, the command filter, the input excerpts and signals, the
// normalised tour, staleness, the prompt and its trimming. The service golden sections (flows,
// scenarios, stored documents, the bounded repository reads) are in onboarding_services_test.go.
//
// The domain package has its own Node differential (onboarding/domain/nodecases_test.go, testdata
// node_cases.json); this one runs the larger S21 corpus through the same functions.

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/onboarding/domain"
)

type pbTodo struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func pbTodos(in []pbTodo) []domain.TodoLine {
	var out []domain.TodoLine
	for _, l := range in {
		out = append(out, domain.TodoLine{Path: l.Path, Line: l.Line, Text: l.Text})
	}
	return out
}

func pbTodoShapes(in []domain.TodoLine) []pbTodo {
	out := []pbTodo{}
	for _, l := range in {
		out = append(out, pbTodo{l.Path, l.Line, l.Text})
	}
	return out
}

// pbTourInput is the golden's TourInput.
type pbTourInput struct {
	RepoName       string     `json:"repoName"`
	DefaultBranch  string     `json:"defaultBranch"`
	RepoMap        pbText     `json:"repoMap"`
	TopFiles       []string   `json:"topFiles"`
	Chains         [][]string `json:"chains"`
	Readme         pbText     `json:"readme"`
	PackageScripts pbText     `json:"packageScripts"`
	Compose        pbText     `json:"compose"`
	EnvNames       []string   `json:"envNames"`
	TodoLines      []pbTodo   `json:"todoLines"`
	UntestedFiles  []string   `json:"untestedFiles"`
}

func (p pbTourInput) domain(t testing.TB) domain.TourInput {
	return domain.TourInput{
		RepoName: p.RepoName, DefaultBranch: p.DefaultBranch, RepoMap: p.RepoMap.plain(t), TopFiles: p.TopFiles,
		Chains: p.Chains, Readme: p.Readme.plain(t), PackageScripts: p.PackageScripts.plain(t), Compose: p.Compose.plain(t),
		EnvNames: p.EnvNames, TodoLines: pbTodos(p.TodoLines), UntestedFiles: p.UntestedFiles,
	}
}

type pbOnboardingGolden struct {
	Constants struct {
		SystemPrompt  string         `json:"systemPrompt"`
		SchemaName    string         `json:"schemaName"`
		PromptVersion int            `json:"promptVersion"`
		Language      string         `json:"language"`
		Limits        map[string]int `json:"limits"`
		Constants     map[string]any `json:"constants"`
	} `json:"constants"`
	Redact []struct {
		Name       string `json:"name"`
		Text       pbText `json:"text"`
		Output     pbText `json:"output"`
		Idempotent bool   `json:"idempotent"`
	} `json:"redact"`
	Command []struct {
		Command   string `json:"command"`
		Dangerous bool   `json:"dangerous"`
	} `json:"command"`
	Input struct {
		Readme []struct {
			Name   string  `json:"name"`
			Text   pbText  `json:"text"`
			Result *pbText `json:"result"`
		} `json:"readme"`
		Compose []struct {
			Name   string  `json:"name"`
			Text   pbText  `json:"text"`
			Result *pbText `json:"result"`
		} `json:"compose"`
		Scripts []struct {
			Name   string  `json:"name"`
			Text   string  `json:"text"`
			Result *string `json:"result"`
		} `json:"scripts"`
		Env []struct {
			Name   string   `json:"name"`
			Text   string   `json:"text"`
			Result []string `json:"result"`
		} `json:"env"`
		Todo []struct {
			Name   string   `json:"name"`
			Path   string   `json:"path"`
			Text   string   `json:"text"`
			Result []pbTodo `json:"result"`
		} `json:"todo"`
		CollectTodos []struct {
			Name  string `json:"name"`
			Files []struct {
				Path string `json:"path"`
				Text string `json:"text"`
			} `json:"files"`
			Result []pbTodo `json:"result"`
		} `json:"collectTodos"`
		Untested []struct {
			Name    string   `json:"name"`
			Ranked  []string `json:"ranked"`
			Indexed []string `json:"indexed"`
			Result  []string `json:"result"`
		} `json:"untested"`
		TestPaths []struct {
			Path   string `json:"path"`
			IsTest bool   `json:"isTest"`
		} `json:"testPaths"`
	} `json:"input"`
	Tour []struct {
		Name         string   `json:"name"`
		IndexedPaths []string `json:"indexedPaths"`
		Raw          struct {
			Architecture struct {
				Summary string `json:"summary"`
				Nodes   []struct {
					ID    string `json:"id"`
					Label string `json:"label"`
					Kind  string `json:"kind"`
				} `json:"nodes"`
				Edges []struct {
					From string `json:"from"`
					To   string `json:"to"`
				} `json:"edges"`
			} `json:"architecture"`
			CriticalPaths []struct{ Path, Reason string }     `json:"critical_paths"`
			RunSteps      []struct{ Command, Comment string } `json:"run_steps"`
			ReadingPath   []struct{ Path, Reason string }     `json:"reading_path"`
			FirstTasks    []struct {
				Title      string `json:"title"`
				Path       string `json:"path"`
				Complexity string `json:"complexity"`
			} `json:"first_tasks"`
		} `json:"raw"`
		Result map[string]any `json:"result"`
	} `json:"tour"`
	Stale []struct {
		Stored struct {
			IndexedSHA    string `json:"indexed_sha"`
			PromptVersion int    `json:"prompt_version"`
		} `json:"stored"`
		Current *string `json:"current"`
		Result  struct {
			Stale  bool   `json:"stale"`
			Reason string `json:"reason"`
		} `json:"result"`
	} `json:"stale"`
	Prompt []struct {
		Name      string      `json:"name"`
		MaxTokens int         `json:"maxTokens"`
		Input     pbTourInput `json:"input"`
		Estimate  int         `json:"estimate"`
		Trimmed   struct {
			Estimate      int      `json:"estimate"`
			Readme        *pbText  `json:"readme"`
			Compose       *pbText  `json:"compose"`
			TodoLines     []pbTodo `json:"todoLines"`
			RepoMapLength int      `json:"repoMapLength"`
			RepoMap       pbText   `json:"repoMap"`
		} `json:"trimmed"`
		Messages []struct {
			Role    string `json:"role"`
			Content pbText `json:"content"`
		} `json:"messages"`
		SystemIsTheNodeConstant bool `json:"systemIsTheNodeConstant"`
	} `json:"prompt"`
}

func pbOnboarding(t *testing.T) *pbOnboardingGolden {
	t.Helper()
	var g pbOnboardingGolden
	pbLoad(t, "onboarding", &g)
	return &g
}

func TestOnboardingParity_Constants(t *testing.T) {
	g := pbOnboarding(t)
	k := g.Constants
	if domain.SystemPrompt != k.SystemPrompt {
		t.Errorf("SystemPrompt differs from Node (-node +go):\n%s", pbClip(cmp.Diff(k.SystemPrompt, domain.SystemPrompt), 3000))
	}
	if domain.SchemaName != k.SchemaName || domain.PromptVersion != k.PromptVersion || domain.Language != k.Language {
		t.Errorf("schema/version/language = %q/%d/%q, want %q/%d/%q", domain.SchemaName, domain.PromptVersion, domain.Language, k.SchemaName, k.PromptVersion, k.Language)
	}
	for name, c := range map[string][2]int{
		"summaryMax": {domain.MaxSummary, k.Limits["summaryMax"]}, "nodesMin": {domain.MinNodes, k.Limits["nodesMin"]},
		"nodesMax": {domain.MaxNodes, k.Limits["nodesMax"]}, "edgesMax": {domain.MaxEdges, k.Limits["edgesMax"]},
		"nodeIdMax": {domain.MaxNodeID, k.Limits["nodeIdMax"]}, "nodeLabelMax": {domain.MaxNodeLabel, k.Limits["nodeLabelMax"]},
		"criticalPathsMin": {domain.MinCriticalPaths, k.Limits["criticalPathsMin"]}, "criticalPathsMax": {domain.MaxCriticalPaths, k.Limits["criticalPathsMax"]},
		"readingPathMin": {domain.MinReadingPath, k.Limits["readingPathMin"]}, "readingPathMax": {domain.MaxReadingPath, k.Limits["readingPathMax"]},
		"runStepsMin": {domain.MinRunSteps, k.Limits["runStepsMin"]}, "runStepsMax": {domain.MaxRunSteps, k.Limits["runStepsMax"]},
		"commandMax": {domain.MaxCommand, k.Limits["commandMax"]}, "commentMax": {domain.MaxComment, k.Limits["commentMax"]},
		"reasonMax": {domain.MaxReason, k.Limits["reasonMax"]}, "firstTasksMax": {domain.MaxFirstTasks, k.Limits["firstTasksMax"]},
		"taskTitleMax": {domain.MaxTaskTitle, k.Limits["taskTitleMax"]}, "pathMax": {domain.MaxPath, k.Limits["pathMax"]},
		"inputMaxTokens":      {domain.InputBudgetTokens, int(k.Constants["inputMaxTokens"].(float64))},
		"llmMaxOutputTokens":  {domain.MaxOutputTokens, int(k.Constants["llmMaxOutputTokens"].(float64))},
		"llmMaxRetries":       {domain.Reprompts, int(k.Constants["llmMaxRetries"].(float64))},
		"generationTimeoutMs": {int(domain.GenerationBudget.Milliseconds()) / 2, int(k.Constants["generationTimeoutMs"].(float64))}, // Go: twice Node's (2026-10-28)
		"topFilesCount":       {domain.TopFilesInPrompt, int(k.Constants["topFilesCount"].(float64))},
		"readmeMaxChars":      {domain.ReadmeMaxUnits, int(k.Constants["readmeMaxChars"].(float64))},
		"composeMaxChars":     {domain.ComposeMaxUnits, int(k.Constants["composeMaxChars"].(float64))},
		"scriptsMaxChars":     {domain.ScriptsMaxUnits, int(k.Constants["scriptsMaxChars"].(float64))},
		"envNamesMax":         {domain.EnvNamesMax, int(k.Constants["envNamesMax"].(float64))},
		"fileFetchMaxBytes":   {domain.MaxBlobBytes, int(k.Constants["fileFetchMaxBytes"].(float64))},
		"readConcurrency":     {domain.MaxBlobReaders, int(k.Constants["readConcurrency"].(float64))},
		"todoScanFiles":       {domain.TopFilesLimit, int(k.Constants["todoScanFiles"].(float64))},
		"todoMax":             {domain.TodoMax, int(k.Constants["todoMax"].(float64))},
		"todoLineMaxChars":    {domain.TodoLineMaxUnits, int(k.Constants["todoLineMaxChars"].(float64))},
		"untestedMax":         {domain.UntestedMax, int(k.Constants["untestedMax"].(float64))},
	} {
		if c[0] != c[1] {
			t.Errorf("constant %s = %d, want %d (Node)", name, c[0], c[1])
		}
	}
	// The root files read at the indexed commit, in the order of RootFilePaths.
	wantRoot := []string{}
	for _, key := range []string{"readmeCandidates"} {
		for _, p := range k.Constants[key].([]any) {
			wantRoot = append(wantRoot, p.(string))
		}
	}
	wantRoot = append(wantRoot, k.Constants["packageJsonPath"].(string))
	for _, p := range k.Constants["composeCandidates"].([]any) {
		wantRoot = append(wantRoot, p.(string))
	}
	wantRoot = append(wantRoot, k.Constants["envExamplePath"].(string))
	if diff := cmp.Diff(wantRoot, domain.RootFilePaths()); diff != "" {
		t.Errorf("RootFilePaths mismatch (-node +go):\n%s", diff)
	}
}

// TestOnboardingParity_Redact (AC5050): the output of RedactSecrets equals Node's for every
// corpus text, and redacting the output again changes nothing where Node's is idempotent.
func TestOnboardingParity_Redact(t *testing.T) {
	g := pbOnboarding(t)
	if len(g.Redact) < 60 {
		t.Fatalf("golden has %d redaction cases, a generator change shrank it", len(g.Redact))
	}
	for _, c := range g.Redact {
		t.Run(c.Name, func(t *testing.T) {
			got := domain.RedactSecrets(c.Text.plain(t))
			pbCheckText(t, "RedactSecrets", c.Output, got)
			if c.Idempotent {
				if again := domain.RedactSecrets(got); again != got {
					t.Errorf("RedactSecrets is not idempotent here (Node's is): second pass changed %q to %q", pbClip(got, 200), pbClip(again, 200))
				}
			}
		})
	}
}

// TestOnboardingParity_Command (AC5051): every verdict of the command filter equals Node's.
func TestOnboardingParity_Command(t *testing.T) {
	g := pbOnboarding(t)
	if len(g.Command) < 400 {
		t.Fatalf("golden has %d command cases, a generator change shrank it", len(g.Command))
	}
	for i, c := range g.Command {
		if got := domain.IsDangerousCommand(c.Command); got != c.Dangerous {
			t.Errorf("case %d: IsDangerousCommand(%q) = %v, want %v", i, c.Command, got, c.Dangerous)
		}
	}
}

func TestOnboardingParity_Input(t *testing.T) {
	g := pbOnboarding(t).Input
	excerpt := func(t *testing.T, kind string, f func(string) (string, bool), name string, text pbText, want *pbText) {
		got, ok := f(text.plain(t))
		switch {
		case want == nil && ok:
			t.Errorf("%s %s: excerpt = %q, want none", kind, name, pbClip(got, 100))
		case want != nil && !ok:
			t.Errorf("%s %s: no excerpt, want one", kind, name)
		case want != nil:
			pbCheckText(t, kind+" "+name, *want, got)
		}
	}
	t.Run("readme", func(t *testing.T) {
		for _, c := range g.Readme {
			excerpt(t, "ReadmeExcerpt", domain.ReadmeExcerpt, c.Name, c.Text, c.Result)
		}
	})
	t.Run("compose", func(t *testing.T) {
		for _, c := range g.Compose {
			excerpt(t, "ComposeExcerpt", domain.ComposeExcerpt, c.Name, c.Text, c.Result)
		}
	})
	t.Run("scripts", func(t *testing.T) {
		for _, c := range g.Scripts {
			got, ok := domain.PackageScripts(c.Text)
			switch {
			case c.Result == nil && ok:
				t.Errorf("%s: PackageScripts = %q, want none", c.Name, got)
			case c.Result != nil && (!ok || got != *c.Result):
				t.Errorf("%s: PackageScripts(%q) = (%q, %v), want %q", c.Name, pbClip(c.Text, 80), pbClip(got, 100), ok, pbClip(*c.Result, 100))
			}
		}
	})
	t.Run("env", func(t *testing.T) {
		for _, c := range g.Env {
			if diff := cmp.Diff(c.Result, domain.EnvVariableNames(c.Text), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: EnvVariableNames mismatch (-node +go):\n%s", c.Name, diff)
			}
		}
	})
	t.Run("todo", func(t *testing.T) {
		for _, c := range g.Todo {
			if diff := cmp.Diff(c.Result, pbTodoShapes(domain.FindTodoLines(c.Path, c.Text, 0)), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: FindTodoLines mismatch (-node +go):\n%s", c.Name, pbClip(diff, 2000))
			}
		}
	})
	t.Run("collectTodos", func(t *testing.T) {
		for _, c := range g.CollectTodos {
			var perFile [][]domain.TodoLine
			for _, f := range c.Files {
				perFile = append(perFile, domain.FindTodoLines(f.Path, f.Text, domain.TodoMax))
			}
			if diff := cmp.Diff(c.Result, pbTodoShapes(domain.CollectTodoLines(perFile)), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: CollectTodoLines mismatch (-node +go):\n%s", c.Name, pbClip(diff, 2000))
			}
		}
	})
	t.Run("untested", func(t *testing.T) {
		for _, c := range g.Untested {
			if diff := cmp.Diff(c.Result, domain.FindUntestedFiles(c.Ranked, c.Indexed), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: FindUntestedFiles mismatch (-node +go):\n%s", c.Name, diff)
			}
		}
	})
	t.Run("testPaths", func(t *testing.T) {
		if len(g.TestPaths) < 30 {
			t.Fatalf("golden has %d test-path cases, a generator change shrank it", len(g.TestPaths))
		}
		for _, c := range g.TestPaths {
			if got := domain.IsTestPath(c.Path); got != c.IsTest {
				t.Errorf("IsTestPath(%q) = %v, want %v", c.Path, got, c.IsTest)
			}
		}
	})
}

// TestOnboardingParity_NormalizeTour (AC5052): the normalised tour and the dropped counts equal
// Node's; a run step's empty comment is absent in the stored shape.
func TestOnboardingParity_NormalizeTour(t *testing.T) {
	g := pbOnboarding(t)
	for _, c := range g.Tour {
		t.Run(c.Name, func(t *testing.T) {
			var raw domain.LLMOutput
			raw.Architecture.Summary = c.Raw.Architecture.Summary
			for _, n := range c.Raw.Architecture.Nodes {
				raw.Architecture.Nodes = append(raw.Architecture.Nodes, domain.LLMNode{ID: n.ID, Label: n.Label, Kind: n.Kind})
			}
			for _, e := range c.Raw.Architecture.Edges {
				raw.Architecture.Edges = append(raw.Architecture.Edges, domain.Edge{From: e.From, To: e.To})
			}
			for _, p := range c.Raw.CriticalPaths {
				raw.CriticalPaths = append(raw.CriticalPaths, domain.LLMPath{Path: p.Path, Reason: p.Reason})
			}
			for _, s := range c.Raw.RunSteps {
				raw.RunSteps = append(raw.RunSteps, domain.LLMRunStep{Command: s.Command, Comment: s.Comment})
			}
			for _, p := range c.Raw.ReadingPath {
				raw.ReadingPath = append(raw.ReadingPath, domain.LLMPath{Path: p.Path, Reason: p.Reason})
			}
			for _, f := range c.Raw.FirstTasks {
				raw.FirstTasks = append(raw.FirstTasks, domain.LLMTask{Title: f.Title, Path: f.Path, Complexity: f.Complexity})
			}
			tour, dropped := domain.NormalizeTour(raw, c.IndexedPaths)

			nodes := []map[string]any{}
			for _, n := range tour.Architecture.Nodes {
				nodes = append(nodes, map[string]any{"id": n.ID, "label": n.Label, "kind": string(n.Kind)})
			}
			edges := []map[string]any{}
			for _, e := range tour.Architecture.Edges {
				edges = append(edges, map[string]any{"from": e.From, "to": e.To})
			}
			paths := func(ps []domain.PathItem) []map[string]any {
				out := []map[string]any{}
				for _, p := range ps {
					out = append(out, map[string]any{"path": p.Path, "reason": p.Reason})
				}
				return out
			}
			steps := []map[string]any{}
			for _, s := range tour.RunSteps {
				m := map[string]any{"command": s.Command}
				if s.Comment != "" {
					m["comment"] = s.Comment
				}
				steps = append(steps, m)
			}
			tasks := []map[string]any{}
			for _, f := range tour.FirstTasks {
				tasks = append(tasks, map[string]any{"title": f.Title, "path": f.Path, "complexity": string(f.Complexity)})
			}
			got := pbGeneric(t, map[string]any{
				"tour": map[string]any{
					"architecture":   map[string]any{"summary": tour.Architecture.Summary, "nodes": nodes, "edges": edges},
					"critical_paths": paths(tour.CriticalPaths), "run_steps": steps, "reading_path": paths(tour.ReadingPath), "first_tasks": tasks,
				},
				"dropped": map[string]any{
					"nodes": dropped.Nodes, "edges": dropped.Edges, "critical_paths": dropped.CriticalPaths,
					"run_steps": dropped.RunSteps, "reading_path": dropped.ReadingPath, "first_tasks": dropped.FirstTasks,
				},
			})
			pbDiff(t, "NormalizeTour("+c.Name+")", pbGeneric(t, c.Result), got)
		})
	}
}

func TestOnboardingParity_Staleness(t *testing.T) {
	for i, c := range pbOnboarding(t).Stale {
		current := ""
		if c.Current != nil {
			current = *c.Current
		}
		reason := domain.Staleness(c.Stored.IndexedSHA, c.Stored.PromptVersion, current)
		if stale := reason != domain.StaleNone; stale != c.Result.Stale || string(reason) != c.Result.Reason {
			t.Errorf("case %d: Staleness(%q, %d, %q) = %q, want stale=%v reason %q", i, c.Stored.IndexedSHA, c.Stored.PromptVersion, current, reason, c.Result.Stale, c.Result.Reason)
		}
	}
}

func TestOnboardingParity_Prompt(t *testing.T) {
	g := pbOnboarding(t)
	for _, c := range g.Prompt {
		t.Run(c.Name, func(t *testing.T) {
			if !c.SystemIsTheNodeConstant {
				t.Fatal("golden does not vouch for the system prompt")
			}
			in := c.Input.domain(t)
			if got := domain.EstimateInputTokens(in); got != c.Estimate {
				t.Errorf("EstimateInputTokens = %d, want %d", got, c.Estimate)
			}

			tr := domain.TrimToBudget(in, c.MaxTokens)
			want := c.Trimmed
			if got := domain.EstimateInputTokens(tr); got != want.Estimate {
				t.Errorf("estimate of the trimmed input (max %d) = %d, want %d", c.MaxTokens, got, want.Estimate)
			}
			for _, f := range []struct {
				name string
				want *pbText
				got  string
			}{{"readme", want.Readme, tr.Readme}, {"compose", want.Compose, tr.Compose}} {
				switch {
				case f.want == nil && f.got != "":
					t.Errorf("trimmed %s = %q, want it removed", f.name, pbClip(f.got, 80))
				case f.want != nil:
					pbCheckText(t, "trimmed "+f.name, *f.want, f.got)
				}
			}
			if diff := cmp.Diff(want.TodoLines, pbTodoShapes(tr.TodoLines), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("trimmed TODO lines mismatch (-node +go):\n%s", diff)
			}
			if n := pbUnits(tr.RepoMap); n != want.RepoMapLength {
				t.Errorf("trimmed repo map has %d UTF-16 units, want %d", n, want.RepoMapLength)
			}
			pbCheckText(t, "trimmed repo map", want.RepoMap, tr.RepoMap)

			msgs := domain.BuildMessages(in)
			if len(msgs) != len(c.Messages) {
				t.Fatalf("BuildMessages returned %d messages, want %d", len(msgs), len(c.Messages))
			}
			for i, m := range msgs {
				w := c.Messages[i]
				if m.Role != w.Role {
					t.Errorf("message %d role = %q, want %q", i, m.Role, w.Role)
				}
				if w.Content.b == nil && w.Content.s == "$ONBOARDING_SYSTEM_PROMPT" {
					w.Content = pbText{s: g.Constants.SystemPrompt}
				}
				pbCheckText(t, fmt.Sprintf("message %d (%s) content", i, m.Role), w.Content, m.Content)
			}
		})
	}
}
