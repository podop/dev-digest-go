package parity_test

// Phase 6 parity of the CI export/ingest domain with the Node implementation (goldens/ci.json,
// goldens/runner.json): slug rule, trigger normalisation, workflow text (what stays identical to Node,
// plus spec 7.6.5 byte for byte), PR body, manifest and its YAML, result artifact parse and verification,
// GitHub error codes, the artifact ZIP read. Spec AC6036–AC6039, AC6046, FR6056, NFR6001; plan S32.
// The Go template differs from Node's on purpose (D6006, D6007, D6008): only what the spec keeps
// identical is compared. Disagreements that are decisions or library categories are listed with the
// reason in testdata/handwritten/ci_deviations.json; every test checks that list in both directions.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"

	"dev-digest-go/backend/internal/ci/adapters/zipentry"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
	rdomain "dev-digest-go/backend/internal/ci/runner/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ---------------------------------------------------------------------------
// hand-written deviation lists (shared with evals_test.go)
// ---------------------------------------------------------------------------

type deviation struct {
	Key      string `json:"key"`
	Category string `json:"category"`
	Node     any    `json:"node"`
	Go       any    `json:"go"`
	Reason   string `json:"reason"`
}

type deviationFile struct {
	HandWritten bool        `json:"handWritten"`
	Deviations  []deviation `json:"deviations"`
}

// deviations is a loaded list of known Node/Go disagreements; check() consumes it.
type deviations struct {
	listed map[string]deviation
	hit    map[string]bool
}

func loadDeviations(t testing.TB, name string) *deviations {
	t.Helper()
	var f deviationFile
	readJSON(t, filepath.Join("testdata", "handwritten", name+".json"), &f)
	if !f.HandWritten {
		t.Fatalf("%s.json must carry \"handWritten\": true", name)
	}
	d := &deviations{listed: map[string]deviation{}, hit: map[string]bool{}}
	for _, x := range f.Deviations {
		if x.Reason == "" || x.Category == "" {
			t.Errorf("%s: deviation %q needs a category and a reason", name, x.Key)
		}
		if _, dup := d.listed[x.Key]; dup {
			t.Errorf("%s: deviation %q is listed twice", name, x.Key)
		}
		d.listed[x.Key] = x
	}
	return d
}

// check compares the Node value with the Go value (both generic JSON values). A difference is an error
// unless the key is listed with exactly these two values.
func (d *deviations) check(t testing.TB, key string, node, got any, what string) {
	t.Helper()
	diff := cmp.Diff(node, got)
	dev, listed := d.listed[key]
	if diff == "" {
		if listed {
			t.Errorf("%s: %s agrees with Node but is listed as a deviation: remove it", key, what)
		}
		return
	}
	if !listed {
		t.Errorf("%s: %s mismatch (-node +go):\n%s", key, what, diff)
		return
	}
	d.hit[key] = true
	if diff := cmp.Diff(dev.Node, node); diff != "" {
		t.Errorf("%s: the listed Node value is out of date (-listed +actual):\n%s", key, diff)
	}
	if diff := cmp.Diff(dev.Go, got); diff != "" {
		t.Errorf("%s: the listed Go value is out of date (-listed +actual):\n%s", key, diff)
	}
}

// finish fails for every listed deviation whose case did not disagree during the run.
func (d *deviations) finish(t testing.TB, prefix string) {
	t.Helper()
	for k := range d.listed {
		if strings.HasPrefix(k, prefix) && !d.hit[k] {
			t.Errorf("deviation %q was never hit: it names no case or the case agrees", k)
		}
	}
}

// ---------------------------------------------------------------------------
// constants and paths
// ---------------------------------------------------------------------------

type ciGolden struct {
	Constants map[string]any `json:"constants"`
}

func TestCIConstantsParity(t *testing.T) {
	var g ciGolden
	loadGolden(t, "ci", &g)
	c := g.Constants
	str := func(k string) string { s, _ := c[k].(string); return s }
	num := func(k string) float64 { f, _ := c[k].(float64); return f }
	for _, tc := range []struct{ key, got string }{
		{"AGENTS_DIR", domain.AgentsDir}, {"SKILLS_DIR", domain.SkillsDir}, {"MEMORY_FILE_PATH", domain.MemoryPath},
		{"WORKFLOWS_DIR", domain.WorkflowsDir}, {"CI_BRANCH", domain.Branch}, {"CI_PR_TITLE", domain.PRTitle},
		{"CI_COMMIT_MESSAGE", domain.CommitMessage}, {"RESULT_FILE_NAME", domain.ResultFileName},
		{"AGENT_SLUG_FALLBACK", domain.AgentSlugFallback}, {"SKILL_SLUG_FALLBACK", domain.SkillSlugFallback},
		{"RUNNER_OS", domain.RunnerOS}, {"FORK_GUARD_EXPR", domain.ForkGuardExpr},
	} {
		if tc.got != str(tc.key) {
			t.Errorf("%s = %q, Node %q", tc.key, tc.got, str(tc.key))
		}
	}
	for _, tc := range []struct {
		key string
		got float64
	}{
		{"SLUG_MAX_LENGTH", domain.SlugMaxLength}, {"MAX_WORKFLOW_BYTES", domain.MaxWorkflowBytes},
		{"MAX_ARTIFACT_ZIP_BYTES", domain.MaxArtifactZipBytes}, {"MAX_ARTIFACT_JSON_BYTES", domain.MaxArtifactJSONBytes},
		{"RUNS_PER_INSTALLATION", domain.RunsPerInstallation}, {"REFRESH_BUDGET_MS", float64(domain.RefreshBudget.Milliseconds())},
	} {
		if tc.got != num(tc.key) {
			t.Errorf("%s = %v, Node %v", tc.key, tc.got, num(tc.key))
		}
	}
	var triggers []string
	for _, tr := range domain.Triggers() {
		triggers = append(triggers, string(tr))
	}
	if diff := cmp.Diff(c["ALLOWED_TRIGGERS"], toValue(t, triggers)); diff != "" {
		t.Errorf("ALLOWED_TRIGGERS (-node +go):\n%s", diff)
	}
	postAs := toValue(t, []domain.PostAs{domain.PostAsGitHubReview, domain.PostAsPRComment, domain.PostAsNone})
	if diff := cmp.Diff(c["POST_AS_VALUES"], postAs); diff != "" {
		t.Errorf("POST_AS_VALUES (-node +go):\n%s", diff)
	}
	pins, _ := c["ACTION_PINS"].(map[string]any)
	if pins["checkout"] != domain.ActionCheckout || pins["uploadArtifact"] != domain.ActionUploadArtifact {
		t.Errorf("action pins = %q, %q; Node %v", domain.ActionCheckout, domain.ActionUploadArtifact, pins)
	}
	if !domain.ValidSlug("a") || domain.ValidSlug("A") || domain.ValidSlug("-a") || domain.ValidSlug(strings.Repeat("a", 41)) || !domain.ValidSlug(strings.Repeat("a", 40)) {
		t.Errorf("ValidSlug disagrees with SLUG_PATTERN %v", c["SLUG_PATTERN"])
	}
	paths, _ := c["paths"].(map[string]any)
	for _, tc := range []struct{ key, got string }{
		{"manifestPathFor", domain.ManifestPath("sec")}, {"skillPathFor", domain.SkillPath("api-safety")},
		{"workflowPathFor", domain.WorkflowPath("sec")}, {"checkNameFor", domain.CheckName("sec")},
		{"artifactNameFor", domain.ArtifactName("sec")},
	} {
		if paths[tc.key] != tc.got {
			t.Errorf("%s = %q, Node %v", tc.key, tc.got, paths[tc.key])
		}
	}
}

// ---------------------------------------------------------------------------
// slugs, triggers (AC6036)
// ---------------------------------------------------------------------------

func TestCISlugParity(t *testing.T) {
	var g struct {
		Slugs []struct {
			Name     string `json:"name"`
			Fallback string `json:"fallback"`
			Slug     string `json:"slug"`
		} `json:"slugs"`
	}
	loadGolden(t, "ci", &g)
	if len(g.Slugs) < 50 {
		t.Fatalf("golden has %d slug cases, want at least 50", len(g.Slugs))
	}
	for i, c := range g.Slugs {
		fb := c.Fallback
		if fb == "" {
			fb = domain.AgentSlugFallback
		}
		if got := domain.Slugify(c.Name, fb); got != c.Slug {
			t.Errorf("slug case %d Slugify(%q, %q) = %q, want %q", i, c.Name, fb, got, c.Slug)
		}
		if c.Fallback == "" {
			if got := domain.AgentSlug(c.Name); got != c.Slug {
				t.Errorf("AgentSlug(%q) = %q, want %q", c.Name, got, c.Slug)
			}
		}
		if c.Fallback == domain.SkillSlugFallback {
			if got := domain.SkillSlug(c.Name); got != c.Slug {
				t.Errorf("SkillSlug(%q) = %q, want %q", c.Name, got, c.Slug)
			}
		}
	}
}

func TestCITriggersParity(t *testing.T) {
	var g struct {
		Triggers []struct {
			Input  []string  `json:"input"`
			Result *[]string `json:"result"`
		} `json:"triggers"`
	}
	loadGolden(t, "ci", &g)
	for _, c := range g.Triggers {
		got, ok := domain.NormalizeTriggers(c.Input)
		if c.Result == nil {
			if ok {
				t.Errorf("NormalizeTriggers(%q) = %v, want refused", c.Input, got)
			}
			continue
		}
		var names []string
		for _, tr := range got {
			names = append(names, string(tr))
		}
		if !ok || !slices.Equal(names, *c.Result) {
			t.Errorf("NormalizeTriggers(%q) = %v, %v; want %v", c.Input, names, ok, *c.Result)
		}
	}
}

// ---------------------------------------------------------------------------
// workflow text (AC6039)
// ---------------------------------------------------------------------------

// ciWithoutNodeSteps removes what the Go template changes on purpose (D6007): the "Set up Node.js"
// step and the body of the run step between its name line and its env block.
func ciWithoutNodeSteps(text string) string {
	const (
		setupNode = "      - name: Set up Node.js"
		runStep   = "      - name: Run the DevDigest review"
	)
	var out []string
	skipping := ""
	for _, line := range strings.Split(text, "\n") {
		switch {
		case line == setupNode:
			skipping = "setup"
			continue
		case line == runStep:
			skipping = "run"
			out = append(out, line)
			continue
		case skipping == "setup" && strings.HasPrefix(line, "      - name:"):
			skipping = ""
		case skipping == "run" && strings.HasPrefix(line, "        env:"):
			skipping = ""
		}
		if skipping != "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestCIWorkflowParity(t *testing.T) {
	var g struct {
		Workflows []struct {
			Slug     string   `json:"slug"`
			Triggers []string `json:"triggers"`
			PostAs   string   `json:"postAs"`
			Result   *string  `json:"result"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"workflows"`
	}
	loadGolden(t, "ci", &g)
	if len(g.Workflows) < 20 {
		t.Fatalf("golden has %d workflow cases, want 20", len(g.Workflows))
	}
	for _, c := range g.Workflows {
		var trig []domain.Trigger
		for _, s := range c.Triggers {
			trig = append(trig, domain.Trigger(s))
		}
		got, err := domain.BuildWorkflow(domain.WorkflowOptions{Slug: c.Slug, Triggers: trig, PostAs: domain.PostAs(c.PostAs)})
		label := fmt.Sprintf("BuildWorkflow(%q, %v, %q)", c.Slug, c.Triggers, c.PostAs)
		if c.Error != nil {
			if !errors.Is(err, domain.ErrInvalidWorkflowOptions) {
				t.Errorf("%s: error = %v (text %d bytes), want ErrInvalidWorkflowOptions (Node: %q)", label, err, len(got), c.Error.Message)
			} else if !strings.Contains(err.Error(), strings.TrimPrefix(c.Error.Message, "invalid ")) {
				t.Errorf("%s: error %q does not name the same field as Node's %q", label, err, c.Error.Message)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", label, err)
			continue
		}
		// What stays identical to Node: everything but the Node setup step and the run step.
		if diff := cmp.Diff(ciWithoutNodeSteps(*c.Result), ciWithoutNodeSteps(got)); diff != "" {
			t.Errorf("%s differs from Node outside the removed setup step and the run step (-node +go):\n%s", label, diff)
		}
		if strings.Contains(got, "Set up Node.js") || strings.Contains(got, "node .devdigest") {
			t.Errorf("%s still has the Node setup or run step:\n%s", label, got)
		}
		if !strings.Contains(*c.Result, "Set up Node.js") || !strings.Contains(*c.Result, "node .devdigest/runner/index.js") {
			t.Errorf("%s: the Node golden lost the steps the Go text replaces", label)
		}
	}
}

// TestCIWorkflowSpecText: for slug security-reviewer, triggers [synchronize] and post_as none the text
// is spec 7.6.5 byte for byte (AC6039). The spec text of the corpus is the all-triggers github_review
// variant, so those two values are substituted in it.
func TestCIWorkflowSpecText(t *testing.T) {
	var c struct {
		Go string `json:"go"`
	}
	loadCorpus(t, "wfrules", "cases", &c)
	want := strings.Replace(c.Go, "types: [opened, synchronize, reopened]", "types: [synchronize]", 1)
	want = strings.Replace(want, "DEVDIGEST_POST_AS: github_review", "DEVDIGEST_POST_AS: none", 1)
	if want == c.Go {
		t.Fatal("the substitutions changed nothing")
	}
	got, err := domain.BuildWorkflow(domain.WorkflowOptions{
		Slug: "security-reviewer", Triggers: []domain.Trigger{domain.TriggerSynchronize}, PostAs: domain.PostAsNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildWorkflow(security-reviewer, [synchronize], none) differs from spec 7.6.5 (-want +got):\n%s", diff)
	}
}

func TestCIPRBodyParity(t *testing.T) {
	var g struct {
		PRBodies []struct {
			Slug  string   `json:"slug"`
			Paths []string `json:"paths"`
			Body  string   `json:"body"`
		} `json:"prBodies"`
	}
	loadGolden(t, "ci", &g)
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range g.PRBodies {
		got := domain.BuildPRBody(c.Slug, c.Paths, sha, "build-1")
		start := strings.Index(got, "### Runner\n")
		end := strings.Index(got, "Branch: `")
		if start < 0 || end < start {
			t.Errorf("BuildPRBody(%q) has no runner paragraph:\n%s", c.Slug, got)
			continue
		}
		para := got[start:end]
		if !strings.Contains(para, sha) || !strings.Contains(para, "build-1") {
			t.Errorf("BuildPRBody(%q) runner paragraph lacks the digest or build id: %q", c.Slug, para)
		}
		// Node has no runner paragraph (D6008): everything else is identical.
		if diff := cmp.Diff(c.Body, got[:start]+got[end:]); diff != "" {
			t.Errorf("BuildPRBody(%q) without the runner paragraph (-node +go):\n%s", c.Slug, diff)
		}
	}
}

// ---------------------------------------------------------------------------
// manifest and its YAML (AC6037)
// ---------------------------------------------------------------------------

type ciManifestJSON struct {
	Name         string   `json:"name" yaml:"name"`
	Provider     string   `json:"provider" yaml:"provider"`
	Model        string   `json:"model" yaml:"model"`
	SystemPrompt string   `json:"system_prompt" yaml:"system_prompt"`
	Skills       []string `json:"skills" yaml:"skills"`
	Strategy     string   `json:"strategy" yaml:"strategy"`
	CIFailOn     string   `json:"ci_fail_on" yaml:"ci_fail_on"`
	Version      int      `json:"version" yaml:"version"`
}

// manifestKeys is the key order of spec 7.6.4.
var manifestKeys = []string{"name", "provider", "model", "system_prompt", "skills", "strategy", "ci_fail_on", "version"}

// ciParseManifestYAML parses the YAML a Manifest wrote the way the runner reads it (yaml.v3, exactly one
// document, unknown keys refused) and returns the keys in file order.
func ciParseManifestYAML(text string) (ciManifestJSON, []string, error) {
	var node yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(text))
	if err := dec.Decode(&node); err != nil {
		return ciManifestJSON{}, nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return ciManifestJSON{}, nil, errors.New("second document")
	}
	var keys []string
	if len(node.Content) == 1 && node.Content[0].Kind == yaml.MappingNode {
		m := node.Content[0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			keys = append(keys, m.Content[i].Value)
		}
	}
	var out ciManifestJSON
	dd := yaml.NewDecoder(strings.NewReader(text))
	dd.KnownFields(true)
	if err := dd.Decode(&out); err != nil {
		return ciManifestJSON{}, nil, err
	}
	return out, keys, nil
}

func ciWire(m domain.Manifest) ciManifestJSON {
	return ciManifestJSON{
		Name: m.Name, Provider: m.Provider, Model: m.Model, SystemPrompt: m.SystemPrompt,
		Skills: m.Skills, Strategy: m.Strategy, CIFailOn: m.CIFailOn, Version: m.Version,
	}
}

// ciTypedAgent reads the agent object of a golden case; typed is false when a field has a type the
// Go struct cannot hold (a fractional or non-numeric version, a non-string text).
func ciTypedAgent(m map[string]any) (a domain.ExportAgent, typed bool) {
	str := func(k string) string { s, _ := m[k].(string); return s }
	for _, k := range []string{"name", "provider", "model", "system_prompt", "strategy", "ci_fail_on"} {
		if _, ok := m[k].(string); !ok {
			return a, false
		}
	}
	v, ok := m["version"].(float64)
	if !ok || v != math.Trunc(v) {
		return a, false
	}
	return domain.ExportAgent{
		Name: str("name"), Provider: str("provider"), Model: str("model"), SystemPrompt: str("system_prompt"),
		Strategy: str("strategy"), CIFailOn: str("ci_fail_on"), Version: int(v),
	}, true
}

func TestCIManifestParity(t *testing.T) {
	var g struct {
		Manifests []struct {
			Name   string          `json:"name"`
			Agent  map[string]any  `json:"agent"`
			Skills []string        `json:"skills"`
			Result *ciManifestJSON `json:"result"`
			Error  *struct {
				Details struct {
					Issues []struct {
						Path    string `json:"path"`
						Message string `json:"message"`
					} `json:"issues"`
				} `json:"details"`
			} `json:"error"`
		} `json:"manifests"`
	}
	loadGolden(t, "ci", &g)
	skipped := 0
	for _, c := range g.Manifests {
		agent, typed := ciTypedAgent(c.Agent)
		if !typed {
			// A fraction or a string where an integer or a string is required cannot be said in Go's
			// typed ExportAgent: the type system refuses it before BuildManifest runs.
			skipped++
			continue
		}
		m, err := domain.BuildManifest(agent, c.Skills)
		if c.Error != nil {
			var me *domain.ManifestError
			if !errors.As(err, &me) {
				t.Errorf("%s: BuildManifest error = %v, want *ManifestError", c.Name, err)
				continue
			}
			type issue struct{ Path, Message string }
			var want, got []issue
			for _, is := range c.Error.Details.Issues {
				want = append(want, issue{is.Path, is.Message})
			}
			for _, is := range me.Issues {
				got = append(got, issue{is.Path, is.Message})
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("%s: manifest issues (-node +go):\n%s", c.Name, diff)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: BuildManifest: %v", c.Name, err)
			continue
		}
		want := *c.Result
		if want.Skills == nil {
			want.Skills = []string{}
		}
		got := ciWire(m)
		if got.Skills == nil {
			got.Skills = []string{}
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s: manifest (-node +go):\n%s", c.Name, diff)
		}
		// The serialised file reads back to exactly these values, keys in the order of spec 7.6.4.
		text := m.YAML()
		back, keys, err := ciParseManifestYAML(text)
		if err != nil {
			t.Errorf("%s: the runner's YAML parser refuses %q: %v", c.Name, text, err)
			continue
		}
		if back.Skills == nil {
			back.Skills = []string{}
		}
		if diff := cmp.Diff(want, back); diff != "" {
			t.Errorf("%s: YAML round trip (-node +go):\n%s", c.Name, diff)
		}
		if !slices.Equal(keys, manifestKeys) {
			t.Errorf("%s: YAML keys = %v, want %v", c.Name, keys, manifestKeys)
		}
		if len(want.Skills) == 0 && !strings.Contains(text, "\nskills: []\n") {
			t.Errorf("%s: no skills must be written `skills: []`:\n%s", c.Name, text)
		}
	}
	if skipped != 3 {
		t.Errorf("%d manifest cases were skipped as untypable in Go, want 3 (version fraction, version string, prompt number)", skipped)
	}
}

// ciFiles is a one-file runner.Files over a manifest text.
type ciFiles struct{ text string }

func (f ciFiles) ReadFile(string, int64) ([]byte, error) { return []byte(f.text), nil }
func (ciFiles) ReadDir(string) ([]string, error)         { return nil, errors.New("no directory") }
func (ciFiles) WriteFile(string, []byte) error           { return errors.New("read only") }

// ciRunnerParse reads a manifest text with the CI runner's own loader (ci/runner/app).
func ciRunnerParse(text string) (ciManifestJSON, error) {
	m, err := runnerapp.LoadAgentManifest(ciFiles{text}, "agent.yaml")
	if err != nil {
		return ciManifestJSON{}, err
	}
	out := ciManifestJSON{
		Name: m.Name, Provider: m.Provider, Model: m.Model, SystemPrompt: m.SystemPrompt, Skills: m.Skills,
		Strategy: string(m.Strategy), CIFailOn: string(m.CIFailOn),
	}
	if m.Version != nil {
		out.Version = int(*m.Version)
	}
	return out, nil
}

// TestCIManifestYAMLValues: the ~95 strings of the Node corpus (leading spaces, `#`, `: `, quotes, tabs,
// CRLF, emoji, YAML 1.1 words, ...) as name and system_prompt survive the Go emitter through yaml.v3 and
// through the CI runner's manifest loader, with the keys in the order of spec 7.6.4. Plus every single
// rune of the low BMP range and the YAML-hostile code points.
func TestCIManifestYAMLValues(t *testing.T) {
	var g struct {
		YAMLValues []struct {
			Value    string         `json:"value"`
			Manifest ciManifestJSON `json:"manifest"`
		} `json:"yamlValues"`
	}
	loadGolden(t, "ci", &g)
	if len(g.YAMLValues) < 90 {
		t.Fatalf("golden has %d yaml value cases, want about 95", len(g.YAMLValues))
	}
	roundTrip := func(label string, m domain.Manifest) {
		t.Helper()
		text := m.YAML()
		back, keys, err := ciParseManifestYAML(text)
		if err != nil {
			t.Errorf("%s: parse %q: %v", label, text, err)
			return
		}
		want := ciWire(m)
		if back.Skills == nil {
			back.Skills = []string{}
		}
		if want.Skills == nil {
			want.Skills = []string{}
		}
		if diff := cmp.Diff(want, back); diff != "" {
			t.Errorf("%s: round trip of %q (-want +got):\n%s", label, text, diff)
		}
		if !slices.Equal(keys, manifestKeys) {
			t.Errorf("%s: keys = %v", label, keys)
		}
		// the runner's loader reads the same values (it validates, so only complete manifests)
		viaRunner, err := ciRunnerParse(text)
		if err != nil {
			t.Errorf("%s: the runner's loader refuses %q: %v", label, text, err)
			return
		}
		if viaRunner.Skills == nil {
			viaRunner.Skills = []string{}
		}
		if diff := cmp.Diff(want, viaRunner); diff != "" {
			t.Errorf("%s: runner loader round trip of %q (-want +got):\n%s", label, text, diff)
		}
	}
	for i, c := range g.YAMLValues {
		m := domain.Manifest{
			Name: c.Manifest.Name, Provider: c.Manifest.Provider, Model: c.Manifest.Model,
			SystemPrompt: c.Manifest.SystemPrompt, Skills: c.Manifest.Skills, Strategy: c.Manifest.Strategy,
			CIFailOn: c.Manifest.CIFailOn, Version: c.Manifest.Version,
		}
		roundTrip(fmt.Sprintf("value %d %q", i, c.Value), m)
	}
	// skills with hostile entries, and an agent without skills
	roundTrip("skills", domain.Manifest{Name: "n", Provider: "openrouter", Model: "m", SystemPrompt: "p", Skills: []string{"a", "b: c", "- d", "#e", ""}, Strategy: "auto", CIFailOn: "never", Version: 1})
	roundTrip("no skills", domain.Manifest{Name: "n", Provider: "openrouter", Model: "m", SystemPrompt: "p", Strategy: "auto", CIFailOn: "never", Version: 1})
	// every code point the emitter has to escape or may fold, alone and between letters
	var points []rune
	for r := rune(0); r < 0x300; r++ {
		points = append(points, r)
	}
	points = append(points, 0x2028, 0x2029, 0xFEFF, 0xFFFD, 0xFFFE, 0xFFFF, 0x1F600, 0x10FFFF, 0xD7FF, 0xE000)
	for _, r := range points {
		for _, v := range []string{string(r), "a" + string(r) + "b", string(r) + "\n" + string(r)} {
			roundTrip(fmt.Sprintf("U+%04X", r), domain.Manifest{
				Name: v, Provider: "openrouter", Model: "m", SystemPrompt: v, Skills: []string{v}, Strategy: "auto", CIFailOn: "never", Version: 2,
			})
		}
	}
}

// ---------------------------------------------------------------------------
// result artifact (AC6046 inputs), verification, GitHub error codes
// ---------------------------------------------------------------------------

// ciArtifactWire is the JSON object of a parsed artifact: optional keys only when set, cost_usd always.
func ciArtifactWire(t testing.TB, a domain.CiResultArtifact) map[string]any {
	t.Helper()
	m := map[string]any{"findings_count": a.FindingsCount, "cost_usd": a.CostUSD, "agent": a.Agent}
	put := func(k string, set bool, v any) {
		if set {
			m[k] = v
		}
	}
	put("critical", a.Critical != nil, a.Critical)
	put("warning", a.Warning != nil, a.Warning)
	put("suggestion", a.Suggestion != nil, a.Suggestion)
	put("duration_ms", a.DurationMS != nil, a.DurationMS)
	put("version", a.Version != nil, a.Version)
	put("pr_number", a.PRNumber != nil, a.PRNumber)
	put("commit_sha", a.CommitSHA != nil, a.CommitSHA)
	put("repository", a.Repository != nil, a.Repository)
	put("repository_id", a.RepositoryID != nil, a.RepositoryID)
	put("run_id", a.RunID != nil, a.RunID)
	put("run_attempt", a.RunAttempt != nil, a.RunAttempt)
	put("model", a.Model != nil, a.Model)
	put("manifest_version", a.ManifestVersion != nil, a.ManifestVersion)
	put("skills", a.Skills != nil, a.Skills)
	put("blockers", a.Blockers != nil, a.Blockers)
	return toValue(t, m).(map[string]any)
}

// ciWithoutNulls drops the null optional keys of a Node artifact (cost_usd is required and stays).
func ciWithoutNulls(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if v == nil && k != "cost_usd" {
			continue
		}
		out[k] = v
	}
	return out
}

type ciArtifactCase struct {
	Name     string                        `json:"name"`
	JSON     map[string]any                `json:"json"`
	Text     *string                       `json:"text"`
	Parts    []struct{ Text, Hex *string } `json:"parts"`
	BytesLen int                           `json:"bytesLen"`
	Result   struct {
		OK       bool           `json:"ok"`
		Reason   string         `json:"reason"`
		Artifact map[string]any `json:"artifact"`
	} `json:"result"`
}

func (c ciArtifactCase) bytes(t testing.TB) []byte {
	t.Helper()
	switch {
	case c.JSON != nil:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(c.JSON); err != nil {
			t.Fatal(err)
		}
		return bytes.TrimRight(buf.Bytes(), "\n")
	case c.Text != nil:
		return []byte(*c.Text)
	}
	var b []byte
	for _, p := range c.Parts {
		if p.Text != nil {
			b = append(b, *p.Text...)
		}
		if p.Hex != nil {
			raw, err := hex.DecodeString(*p.Hex)
			if err != nil {
				t.Fatal(err)
			}
			b = append(b, raw...)
		}
	}
	return b
}

func ciOutcome(a domain.CiResultArtifact, err error, t testing.TB) any {
	if err != nil {
		if !errors.Is(err, domain.ErrArtifactInvalid) {
			t.Errorf("ParseResultArtifact error = %v, want ErrArtifactInvalid", err)
		}
		return map[string]any{"ok": false, "reason": "artifact_invalid"}
	}
	return map[string]any{"ok": true, "artifact": ciArtifactWire(t, a)}
}

func TestCIArtifactParity(t *testing.T) {
	var g struct {
		Artifacts []ciArtifactCase `json:"artifacts"`
	}
	loadGolden(t, "ci", &g)
	dev := loadDeviations(t, "ci_deviations")
	for _, c := range g.Artifacts {
		data := c.bytes(t)
		if c.JSON == nil && len(data) != c.BytesLen {
			t.Errorf("%s: input has %d bytes, golden says %d", c.Name, len(data), c.BytesLen)
		}
		a, err := domain.ParseResultArtifact(data)
		got := ciOutcome(a, err, t)
		var want any
		if c.Result.OK {
			want = map[string]any{"ok": true, "artifact": ciWithoutNulls(c.Result.Artifact)}
		} else {
			want = map[string]any{"ok": false, "reason": c.Result.Reason}
		}
		dev.check(t, "artifacts/"+c.Name, want, got, "ParseResultArtifact")
	}
	dev.finish(t, "artifacts/")
}

func TestCIVerifyParity(t *testing.T) {
	var g struct {
		Verify []struct {
			Name     string         `json:"name"`
			Artifact map[string]any `json:"artifact"`
			Run      struct {
				HeadSHA      string `json:"head_sha"`
				RepositoryID int64  `json:"repository_id"`
				Path         string `json:"path"`
			} `json:"run"`
			WorkflowPath string `json:"workflowPath"`
			Result       bool   `json:"result"`
		} `json:"verify"`
	}
	loadGolden(t, "ci", &g)
	for _, c := range g.Verify {
		raw, err := json.Marshal(c.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		a, err := domain.ParseResultArtifact(raw)
		if err != nil {
			t.Errorf("%s: artifact does not parse: %v", c.Name, err)
			continue
		}
		facts := domain.RunFacts{HeadSHA: c.Run.HeadSHA, RepositoryID: c.Run.RepositoryID, Path: c.Run.Path}
		if got := domain.VerifyArtifact(a, facts, c.WorkflowPath); got != c.Result {
			t.Errorf("%s: VerifyArtifact = %v, want %v", c.Name, got, c.Result)
		}
	}
}

func TestCIGitHubErrorCodeParity(t *testing.T) {
	var g struct {
		GitHubErrors []struct {
			Name string          `json:"name"`
			Err  json.RawMessage `json:"err"`
			Code string          `json:"code"`
		} `json:"githubErrors"`
	}
	loadGolden(t, "ci", &g)
	for _, c := range g.GitHubErrors {
		// The inputs of the Go function: an integer status (anything else is "no status") and whether
		// the lower-case header said 0 (the string "0" or the number 0), as Node reads it.
		status, limited := 0, false
		var e struct {
			Status   any `json:"status"`
			Response struct {
				Headers map[string]any `json:"headers"`
			} `json:"response"`
		}
		if json.Unmarshal(c.Err, &e) == nil {
			if f, ok := e.Status.(float64); ok && f == math.Trunc(f) {
				status = int(f)
			}
			switch v := e.Response.Headers["x-ratelimit-remaining"].(type) {
			case string:
				limited = v == "0"
			case float64:
				limited = v == 0
			}
		}
		if got := domain.GitHubErrorCode(status, limited); string(got) != c.Code {
			t.Errorf("%s: GitHubErrorCode(%d, %v) = %q, want %q", c.Name, status, limited, got, c.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// artifact ZIP (zipentry + the size rules of the use case)
// ---------------------------------------------------------------------------

// ciReadResultArtifact is the read of one downloaded artifact as spec 7.7.4 says: a ZIP over
// MaxArtifactZipBytes is too large, then the one named entry is read under MaxArtifactJSONBytes, then
// parsed. It returns the skip reason ("" = the artifact parsed).
func ciReadResultArtifact(zipData []byte) (domain.CiResultArtifact, string, error) {
	if len(zipData) > domain.MaxArtifactZipBytes {
		return domain.CiResultArtifact{}, string(domain.SkipTooLarge), nil
	}
	raw, err := zipentry.New().Entry(zipData, domain.ResultFileName, domain.MaxArtifactJSONBytes)
	switch {
	case errors.Is(err, app.ErrEntryTooLarge):
		return domain.CiResultArtifact{}, string(domain.SkipTooLarge), nil
	case err != nil:
		return domain.CiResultArtifact{}, string(domain.SkipInvalid), err
	}
	a, err := domain.ParseResultArtifact(raw)
	if err != nil {
		return domain.CiResultArtifact{}, string(domain.SkipInvalid), err
	}
	return a, "", nil
}

// ciPaddedZip builds a ZIP of exactly size bytes holding the result entry, by adding a stored filler
// entry (other entries are ignored by the reader).
func ciPaddedZip(t testing.TB, entry []byte, size int) []byte {
	t.Helper()
	build := func(filler int) []byte {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		f, err := w.Create(domain.ResultFileName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(entry); err != nil {
			t.Fatal(err)
		}
		h := &zip.FileHeader{Name: "padding.bin", Method: zip.Store}
		pf, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pf.Write(make([]byte, filler)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	base := len(build(0))
	out := build(size - base)
	if len(out) != size {
		t.Fatalf("padded zip has %d bytes, want %d", len(out), size)
	}
	return out
}

func TestCIZipParity(t *testing.T) {
	var g struct {
		Zips []struct {
			Name string `json:"name"`
			Spec struct {
				PadToSize int `json:"padToSize"`
			} `json:"spec"`
			ZipLen    int    `json:"zipLen"`
			ZipBase64 string `json:"zipBase64"`
			Entry     struct {
				Kind string `json:"kind"`
				Text text   `json:"text"`
			} `json:"entry"`
			Result struct {
				OK       bool           `json:"ok"`
				Reason   string         `json:"reason"`
				Artifact map[string]any `json:"artifact"`
			} `json:"result"`
		} `json:"zips"`
	}
	loadGolden(t, "ci", &g)
	dev := loadDeviations(t, "ci_deviations")
	for _, c := range g.Zips {
		var data []byte
		switch {
		case c.Spec.PadToSize > 0:
			data = ciPaddedZip(t, []byte(`{"findings_count":0,"cost_usd":null,"agent":"A"}`), c.Spec.PadToSize)
		default:
			var err error
			if data, err = base64.StdEncoding.DecodeString(c.ZipBase64); err != nil {
				t.Errorf("%s: bad zipBase64: %v", c.Name, err)
				continue
			}
		}
		if len(data) != c.ZipLen {
			t.Errorf("%s: zip has %d bytes, golden says %d", c.Name, len(data), c.ZipLen)
			continue
		}

		// the entry read alone (skipped for the two zips whose size alone decides)
		if c.Spec.PadToSize == 0 {
			raw, err := zipentry.New().Entry(data, domain.ResultFileName, domain.MaxArtifactJSONBytes)
			var got any
			switch {
			case err == nil:
				got = map[string]any{"kind": "entry", "len": float64(len(raw)), "sha": fmt.Sprintf("%x", rawSum(raw))}
			case errors.Is(err, app.ErrEntryTooLarge):
				got = map[string]any{"kind": "too_large"}
			default:
				got = map[string]any{"kind": "invalid_or_missing"}
			}
			var want any
			switch c.Entry.Kind {
			case "entry":
				want = map[string]any{"kind": "entry", "len": float64(len(c.Entry.Text.s)), "sha": fmt.Sprintf("%x", rawSum([]byte(c.Entry.Text.s)))}
				if c.Entry.Text.b != nil {
					want = map[string]any{"kind": "entry", "len": float64(c.Entry.Text.b.Len), "sha": c.Entry.Text.b.SHA256}
				}
			case "too_large":
				want = map[string]any{"kind": "too_large"}
			default: // missing, invalid
				want = map[string]any{"kind": "invalid_or_missing"}
			}
			dev.check(t, "zips-entry/"+c.Name, want, got, "zipentry.Entry")
		}

		// the artifact outcome
		a, reason, _ := ciReadResultArtifact(data)
		var got any = map[string]any{"ok": true, "artifact": ciArtifactWire(t, a)}
		if reason != "" {
			got = map[string]any{"ok": false, "reason": reason}
		}
		var want any = map[string]any{"ok": false, "reason": c.Result.Reason}
		if c.Result.OK {
			want = map[string]any{"ok": true, "artifact": ciWithoutNulls(c.Result.Artifact)}
		}
		dev.check(t, "zips/"+c.Name, resolveBlobs(want, got), got, "artifact read from the ZIP")
	}
	dev.finish(t, "zips")
}

// ---------------------------------------------------------------------------
// AC6046: the artifact of the Node runner and the one of the Go runner for the same inputs
// ---------------------------------------------------------------------------

func TestCIRunnerArtifactsIngestEqually(t *testing.T) {
	var g struct {
		Artifacts []struct {
			Name  string `json:"name"`
			Input struct {
				Findings        []struct{ Severity string } `json:"findings"`
				CostUSD         *float64                    `json:"costUsd"`
				DurationMS      float64                     `json:"durationMs"`
				Agent           string                      `json:"agent"`
				PRNumber        float64                     `json:"prNumber"`
				CommitSHA       *string                     `json:"commitSha"`
				Repository      string                      `json:"repository"`
				RepositoryID    *int64                      `json:"repositoryId"`
				RunID           *int64                      `json:"runId"`
				RunAttempt      *int64                      `json:"runAttempt"`
				Model           string                      `json:"model"`
				ManifestVersion *int64                      `json:"manifestVersion"`
				Skills          []string                    `json:"skills"`
				Blockers        int                         `json:"blockers"`
			} `json:"input"`
			Result *struct {
				JSON string `json:"json"`
			} `json:"result"`
		} `json:"artifacts"`
	}
	loadGolden(t, "runner", &g)
	inst := domain.Installation{ID: "inst-1", AgentID: "agent-1", Repo: "acme/widgets"}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	checked := 0
	for _, c := range g.Artifacts {
		if c.Result == nil {
			continue
		}
		in := c.Input
		var findings []reviewer.Finding
		for _, f := range in.Findings {
			findings = append(findings, reviewer.Finding{Severity: reviewer.Severity(f.Severity)})
		}
		built, err := rdomain.BuildArtifact(rdomain.ArtifactInput{
			Findings: findings, CostUSD: in.CostUSD, DurationMS: int64(in.DurationMS), Agent: in.Agent, PRNumber: int(in.PRNumber),
			CommitSHA: in.CommitSHA, Repository: in.Repository, RepositoryID: in.RepositoryID, RunID: in.RunID,
			RunAttempt: in.RunAttempt, Model: in.Model, ManifestVersion: in.ManifestVersion, Skills: in.Skills, Blockers: in.Blockers,
		})
		if err != nil {
			t.Errorf("%s: BuildArtifact: %v", c.Name, err)
			continue
		}
		goJSON := built.JSON()
		nodeArtifact, nodeErr := domain.ParseResultArtifact([]byte(c.Result.JSON))
		goArtifact, goErr := domain.ParseResultArtifact([]byte(goJSON))
		if (nodeErr == nil) != (goErr == nil) {
			t.Errorf("%s: the Node artifact parses = %v, the Go artifact parses = %v\nnode: %s\ngo: %s", c.Name, nodeErr == nil, goErr == nil, c.Result.JSON, goJSON)
			continue
		}
		if nodeErr != nil {
			continue // both refused (an input outside the schema); nothing is ingested
		}
		if diff := cmp.Diff(ciArtifactWire(t, nodeArtifact), ciArtifactWire(t, goArtifact)); diff != "" {
			t.Errorf("%s: parsed artifacts differ (-node +go):\n%s", c.Name, diff)
		}
		// Both verify against the run they claim to come from, and store equal ci_runs values.
		run := domain.WorkflowRun{
			ID: 1001, RunAttempt: 1, HTMLURL: "https://github.com/acme/widgets/actions/runs/1001",
			CreatedAt: "2026-03-01T10:00:00Z", PRNumbers: []int{int(in.PRNumber)},
		}
		if in.CommitSHA != nil {
			run.HeadSHA = *in.CommitSHA
		}
		if in.RepositoryID != nil {
			run.RepositoryID = *in.RepositoryID
		}
		run.Path = domain.WorkflowPath("sec")
		nv := domain.VerifyArtifact(nodeArtifact, domain.FactsOf(run), domain.WorkflowPath("sec"))
		gv := domain.VerifyArtifact(goArtifact, domain.FactsOf(run), domain.WorkflowPath("sec"))
		wantVerified := in.CommitSHA != nil && in.RepositoryID != nil
		if nv != wantVerified || gv != wantVerified {
			t.Errorf("%s: VerifyArtifact node=%v go=%v, want %v", c.Name, nv, gv, wantVerified)
		}
		nodeRun := domain.NewRunFromArtifact("ws-1", inst, run, nodeArtifact, now)
		goRun := domain.NewRunFromArtifact("ws-1", inst, run, goArtifact, now)
		// cost_usd and duration_ms are the runner's measurements and are compared as parsed values above.
		nodeRun.CostUSD, goRun.CostUSD = nil, nil
		nodeRun.DurationMS, goRun.DurationMS = nil, nil
		if diff := cmp.Diff(nodeRun, goRun); diff != "" {
			t.Errorf("%s: stored ci_runs values differ (-node +go):\n%s", c.Name, diff)
		}
		checked++
	}
	if checked < 10 {
		t.Errorf("only %d runner artifacts were compared, want at least 10", checked)
	}
}

func rawSum(b []byte) [32]byte { return sha256.Sum256(b) }

// resolveBlobs replaces, in the golden value want, each `$blob` digest object by the Go string at the same
// place when that string has the digest's length and SHA-256 (so the comparison of a long text is exact
// without the golden carrying it); a digest that does not match is left in place and shows in the diff.
func resolveBlobs(want, got any) any {
	switch w := want.(type) {
	case map[string]any:
		if b, ok := w["$blob"].(bool); ok && b {
			gs, isStr := got.(string)
			raw, _ := json.Marshal(w)
			var bl struct {
				blob
			}
			if json.Unmarshal(raw, &bl.blob) == nil && isStr && (text{b: &bl.blob}).matches(gs) {
				return gs
			}
			return w
		}
		g, _ := got.(map[string]any)
		out := make(map[string]any, len(w))
		for k, v := range w {
			out[k] = resolveBlobs(v, g[k])
		}
		return out
	case []any:
		g, _ := got.([]any)
		out := make([]any, len(w))
		for i, v := range w {
			var gv any
			if i < len(g) {
				gv = g[i]
			}
			out[i] = resolveBlobs(v, gv)
		}
		return out
	}
	return want
}
