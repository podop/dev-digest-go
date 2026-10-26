package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	agentsdomain "dev-digest-go/backend/internal/agents/domain"
	evalsdomain "dev-digest-go/backend/internal/evals/domain"
	customllm "dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pluginsdomain "dev-digest-go/backend/internal/plugins/domain"
	skillsdomain "dev-digest-go/backend/internal/skills/domain"
)

// Component tests of Settings slice D (plan S24) through the real server: the plugin export, the
// import preview and install, the read-back of an installed plugin through the agents and skills
// APIs, the enable/disable and remove routes, the hostile uploads, the raw-body rules, the rate
// buckets, the secrets leak check, the drift of the limits the plugins domain copies, and the
// goldens the UI contract test validates. The run gating (AC13, AC14, AC16) is
// plugin_gating_test.go.

// sdNow is the clock of these tests: the export instant and the date in the export file name.
var sdNow = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

const (
	sdZipMedia  = "application/zip"
	sdJSONMedia = "application/json"
	sdEvilOrg   = "http://evil.example"
)

// sdEnv is a migrated database with the default workspace and the system user, no seed.
type sdEnv struct{ *sbEnv }

func newSDEnv(t *testing.T) *sdEnv { return &sdEnv{newSBEnv(t)} }

// boot starts the server over the database with the fixed clock and the Host allow-list.
func (e *sdEnv) boot(t *testing.T, mutate ...func(*spec)) *stack {
	t.Helper()
	sp := spec{db: e.db, noSeed: true, now: func() time.Time { return sdNow }, allowedHosts: sbHosts}
	for _, m := range mutate {
		m(&sp)
	}
	return startStack(t, sp)
}

// ---- bundle and archive builders (plain maps: the wire format, independent of the domain types) ----

type sdDoc = map[string]any

// sdAgent is a bundle agent with every field; the fields given in over replace the defaults.
func sdAgent(name string, skills []string, over sdDoc) sdDoc {
	a := sdDoc{
		"name": name, "description": "", "provider": "openai", "model": "gpt-x", "system_prompt": "prompt of " + name,
		"output_schema": nil, "enabled": true, "skills": skills, "strategy": "single-pass",
		"ci_fail_on": "critical", "repo_intel": true,
	}
	if skills == nil {
		a["skills"] = []string{}
	}
	for k, v := range over {
		a[k] = v
	}
	return a
}

// sdSkill is a bundle skill.
func sdSkill(name string, over sdDoc) sdDoc {
	s := sdDoc{
		"name": name, "description": "about " + name, "type": "rubric", "source": "manual", "body": "body of " + name,
		"enabled": true, "evidence_files": nil,
	}
	for k, v := range over {
		s[k] = v
	}
	return s
}

// sdCase is a bundle eval case.
func sdCase(name, kind, ref string, over sdDoc) sdDoc {
	c := sdDoc{
		"name": name, "owner_kind": kind, "owner_ref": ref, "input_diff": nil, "input_files": nil,
		"input_meta": nil, "expected_output": nil, "notes": nil,
	}
	for k, v := range over {
		c[k] = v
	}
	return c
}

// sdLearning is a bundle learning.
func sdLearning(scope, kind, content string, confidence any, repo any) sdDoc {
	return sdDoc{"scope": scope, "kind": kind, "content": content, "confidence": confidence, "repo": repo}
}

// sdBundle is a plugin document; the manifest counts follow the lists.
func sdBundle(name string, agents, skills, cases, learnings []sdDoc) sdDoc {
	nz := func(l []sdDoc) []sdDoc {
		if l == nil {
			return []sdDoc{}
		}
		return l
	}
	agents, skills, cases, learnings = nz(agents), nz(skills), nz(cases), nz(learnings)
	return sdDoc{
		"manifest": sdDoc{
			"name": name, "version": "1.2.0", "format": "devdigest-plugin/v1", "kind": "plugin",
			"exported_at": "2026-10-20T08:00:00.000Z", "description": "A pack of " + name,
			"counts": sdDoc{
				"agents": len(agents), "skills": len(skills), "eval_cases": len(cases),
				"conventions": 0, "learnings": len(learnings),
			},
		},
		"agents": agents, "skills": skills, "eval_cases": cases, "conventions": []sdDoc{}, "learnings": learnings,
	}
}

// sdSmallBundle is the smallest valid bundle: one agent linking one skill.
func sdSmallBundle(name string) sdDoc {
	return sdBundle(name,
		[]sdDoc{sdAgent(name+"-agent", []string{name + "-skill"}, nil)},
		[]sdDoc{sdSkill(name+"-skill", nil)}, nil, nil)
}

func sdMarshal(t testing.TB, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// sdEntry is one archive entry.
type sdEntry struct {
	name string
	data []byte
}

// sdZip writes a Deflate archive with the entries in order.
func sdZip(t testing.TB, entries ...sdEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatalf("write zip entry %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sdZipOf is the archive of a plugin: the document as the one root entry devdigest-plugin.json.
func sdZipOf(t testing.TB, doc any) []byte {
	t.Helper()
	return sdZip(t, sdEntry{"devdigest-plugin.json", sdMarshal(t, doc)})
}

// sdUnzip lists the entry names of an archive and returns the content of each.
func sdUnzip(t testing.TB, archive []byte) ([]string, map[string][]byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("answer is not a zip archive: %v", err)
	}
	var names []string
	files := map[string][]byte{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %q: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read entry %q: %v", f.Name, err)
		}
		_ = rc.Close()
		files[f.Name] = data
	}
	return names, files
}

// sdExportBundle decodes the devdigest-plugin.json of an export answer.
func sdExportBundle(t testing.TB, got response) sdDoc {
	t.Helper()
	if got.Status != http.StatusOK {
		t.Fatalf("POST /plugins/export = %d %s, want 200", got.Status, got.Body)
	}
	names, files := sdUnzip(t, got.Body)
	if want := []string{"devdigest-plugin.json"}; !slices.Equal(names, want) {
		t.Fatalf("export archive entries = %v, want %v", names, want)
	}
	var doc sdDoc
	if err := json.Unmarshal(files["devdigest-plugin.json"], &doc); err != nil {
		t.Fatalf("devdigest-plugin.json is not JSON: %v", err)
	}
	return doc
}

// sdImport posts an upload to the import route; query is "" or "?dry_run=true&filename=…".
func sdImport(s *stack, media string, body []byte, query string, headers ...string) response {
	s.t.Helper()
	return s.do(http.MethodPost, "/plugins/import"+query, body, append([]string{"Content-Type", media}, headers...)...)
}

// sdPreview dry-runs the archive of doc.
func sdPreview(t testing.TB, s *stack, doc any) response {
	t.Helper()
	return sdImport(s, sdZipMedia, sdZipOf(t, doc), "?dry_run=true")
}

// sdInstall installs the archive of doc under a file name and requires a 201.
func sdInstall(t testing.TB, s *stack, doc any, fileName string) map[string]any {
	t.Helper()
	got := sdImport(s, sdZipMedia, sdZipOf(t, doc), "?filename="+url.QueryEscape(fileName))
	if got.Status != http.StatusCreated {
		t.Fatalf("POST /plugins/import = %d %s, want 201", got.Status, got.Body)
	}
	return got.Obj(t)
}

// sdObj returns the object at key.
func sdObj(t testing.TB, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %v (%T), want an object", key, m[key], m[key])
	}
	return v
}

// sdErr returns error.code and error.details of an error envelope.
func sdErr(t testing.TB, got response) (code string, details map[string]any) {
	t.Helper()
	e, _ := got.Obj(t)["error"].(map[string]any)
	code, _ = e["code"].(string)
	details, _ = e["details"].(map[string]any)
	return code, details
}

// sdByName indexes a JSON list of objects by name.
func sdByName(t testing.TB, got response) map[string]map[string]any {
	t.Helper()
	if got.Status != http.StatusOK {
		t.Fatalf("list = %d %s, want 200", got.Status, got.Body)
	}
	out := map[string]map[string]any{}
	for _, item := range got.JSON(t).([]any) {
		obj := item.(map[string]any)
		out[obj["name"].(string)] = obj
	}
	return out
}

// sdSortedKeys lists the keys of a map in order.
func sdSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- fixtures ----

// sdFixture are the ids of seedExportable.
type sdFixture struct {
	agentA, agentB, agentC string
	s1, s2, s3, s4         string
	repo                   string
}

// seedExportable stores: agent A (all non-default fields, disabled) linking s1 then s2 (s2
// disabled), agent B linking s3, agent C without skills, an unlinked skill s4; eval cases of A, of
// B and of s1; two learnings (one with a repository).
func (e *sdEnv) seedExportable(t *testing.T) sdFixture {
	t.Helper()
	f := sdFixture{}
	f.agentA = e.agent(t, "agent-a", false, 0)
	f.agentB = e.agent(t, "agent-b", true, 1)
	f.agentC = e.agent(t, "agent-c", true, 2)
	e.exec(t, `UPDATE agents SET description = 'checks a', provider = 'anthropic', model = 'claude-x',
		output_schema = '{"type":"object"}', strategy = 'map-reduce', ci_fail_on = 'warning', repo_intel = false
		WHERE id = ?`, f.agentA)
	f.s1 = e.skill(t, "style-guide", "manual")
	f.s2 = e.skill(t, "security-checks", "imported_file")
	f.s3 = e.skill(t, "perf-hints", "manual")
	f.s4 = e.skill(t, "unlinked-skill", "manual")
	e.exec(t, `UPDATE skills SET enabled = false, evidence_files = '["a.go"]' WHERE id = ?`, f.s2)
	// Inserted out of order: the bundle must follow the link order, not the insertion order.
	e.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, 1), (?, ?, 0), (?, ?, 0)`,
		f.agentA, f.s2, f.agentA, f.s1, f.agentB, f.s3)
	e.exec(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, input_diff, input_files, input_meta,
		expected_output, notes, created_at) VALUES
		(?, 'agent', ?, 'case-of-a', 'diff', '[{"path":"x"}]', '{"k":1}', '{"findings":[]}', 'note', '2026-01-01T00:00:00Z'),
		(?, 'agent', ?, 'case-of-b', NULL, NULL, NULL, NULL, NULL, '2026-01-01T00:00:01Z'),
		(?, 'skill', ?, 'case-of-s1', NULL, NULL, NULL, NULL, NULL, '2026-01-01T00:00:02Z')`,
		e.ws, f.agentA, e.ws, f.agentB, e.ws, f.s1)
	f.repo = e.repo(t, "org/x")
	e.exec(t, `INSERT INTO memory (workspace_id, repo_id, scope, kind, content, confidence, created_at) VALUES
		(?, ?, 'repo', 'decision', 'repo learning', 0.7, '2026-01-01T00:00:00Z'),
		(?, NULL, 'global', 'fact', 'global learning', NULL, '2026-01-01T00:00:01Z')`, e.ws, f.repo, e.ws)
	return f
}

// ---- export (AC4, AC5) ----

func TestSettingsD_ExportOfAnAgentHoldsItsSkillsCasesAndLearnings(t *testing.T) { // AC4
	e := newSDEnv(t)
	e.seedExportable(t)
	s := e.boot(t)
	agentA := scalar[string](t, e.db.DB, `SELECT id::text FROM agents WHERE name = 'agent-a'`)

	got := s.post("/plugins/export", map[string]any{"agent_ids": []string{agentA}, "include_learnings": true})
	bundle := sdExportBundle(t, got)

	// The download headers: the zip, the file name of the default plugin name and the clock's day,
	// the header the UI on another origin reads, and no cache.
	if ct := got.Header.Get("Content-Type"); ct != sdZipMedia {
		t.Errorf("export Content-Type = %q, want %q", ct, sdZipMedia)
	}
	const wantDisposition = `attachment; filename="devdigest-default-2026-10-26.zip"`
	if cd := got.Header.Get("Content-Disposition"); cd != wantDisposition {
		t.Errorf("export Content-Disposition = %q, want %q", cd, wantDisposition)
	}
	if h := got.Header.Get("Access-Control-Expose-Headers"); !strings.Contains(h, "Content-Disposition") {
		t.Errorf("export Access-Control-Expose-Headers = %q, want it to expose Content-Disposition", h)
	}
	if cc := got.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("export Cache-Control = %q, want no-store", cc)
	}

	want := sdDoc{
		"manifest": sdDoc{
			"name": "devdigest-default", "version": "dev", "format": "devdigest-plugin/v1", "kind": "plugin",
			"exported_at": "2026-10-26T09:30:00.000Z", "description": nil,
			"counts": sdDoc{"agents": 1.0, "skills": 2.0, "eval_cases": 2.0, "conventions": 0.0, "learnings": 2.0},
		},
		"agents": []any{sdDoc{
			"name": "agent-a", "description": "checks a", "provider": "anthropic", "model": "claude-x",
			"system_prompt": "prompt of agent-a", "output_schema": sdDoc{"type": "object"}, "enabled": false,
			"skills": []any{"style-guide", "security-checks"}, "strategy": "map-reduce", "ci_fail_on": "warning",
			"repo_intel": false,
		}},
		"skills": []any{
			sdDoc{
				"name": "style-guide", "description": "d", "type": "rubric", "source": "manual", "body": "body",
				"enabled": true, "evidence_files": nil,
			},
			sdDoc{
				"name": "security-checks", "description": "d", "type": "rubric", "source": "imported_file", "body": "body",
				"enabled": false, "evidence_files": []any{"a.go"},
			},
		},
		"eval_cases": []any{
			sdDoc{
				"name": "case-of-a", "owner_kind": "agent", "owner_ref": "agent-a", "input_diff": "diff",
				"input_files": []any{sdDoc{"path": "x"}}, "input_meta": sdDoc{"k": 1.0},
				"expected_output": sdDoc{"findings": []any{}}, "notes": "note",
			},
			sdDoc{
				"name": "case-of-s1", "owner_kind": "skill", "owner_ref": "style-guide", "input_diff": nil,
				"input_files": nil, "input_meta": nil, "expected_output": nil, "notes": nil,
			},
		},
		"conventions": []any{},
		"learnings": []any{
			sdDoc{"scope": "repo", "kind": "decision", "content": "repo learning", "confidence": 0.7, "repo": "org/x"},
			sdDoc{"scope": "global", "kind": "fact", "content": "global learning", "confidence": nil, "repo": nil},
		},
	}
	if diff := cmp.Diff(want, bundle); diff != "" {
		t.Errorf("POST /plugins/export bundle mismatch (-want +got):\n%s", diff)
	}
	// A plugin export never carries the sections of a workspace export.
	for _, key := range []string{"versions", "context_files", "settings"} {
		if _, ok := bundle[key]; ok {
			t.Errorf("plugin export holds the key %q, want it left out", key)
		}
	}
	for _, sk := range bundle["skills"].([]any) {
		if _, ok := sk.(sdDoc)["versions"]; ok {
			t.Errorf("plugin export skill %v holds versions, want them left out", sk.(sdDoc)["name"])
		}
	}

	// FR5: the export is the last export of GET /plugins.
	list := s.get("/plugins").Obj(t)
	wantLast := sdDoc{
		"file_name": "devdigest-default-2026-10-26.zip", "exported_at": "2026-10-26T09:30:00.000Z",
		"counts": sdDoc{"agents": 1.0, "skills": 2.0, "eval_cases": 2.0, "learnings": 2.0},
	}
	if diff := cmp.Diff(wantLast, list["last_export"]); diff != "" {
		t.Errorf("GET /plugins last_export mismatch (-want +got):\n%s", diff)
	}
}

func TestSettingsD_ExportSelectionMatrix(t *testing.T) { // AC4, FR4
	e := newSDEnv(t)
	f := e.seedExportable(t)
	s := e.boot(t)

	type sel = map[string]any
	for _, tc := range []struct {
		name      string
		body      sel
		agents    []string
		skills    []string
		evalCases int
		learnings int
	}{
		{"no ids: everything", sel{}, []string{"agent-a", "agent-b", "agent-c"},
			[]string{"style-guide", "security-checks", "perf-hints", "unlinked-skill"}, 3, 0},
		{"no ids, learnings on", sel{"include_learnings": true}, []string{"agent-a", "agent-b", "agent-c"},
			[]string{"style-guide", "security-checks", "perf-hints", "unlinked-skill"}, 3, 2},
		{"no ids, eval cases off", sel{"include_eval_cases": false}, []string{"agent-a", "agent-b", "agent-c"},
			[]string{"style-guide", "security-checks", "perf-hints", "unlinked-skill"}, 0, 0},
		{"an agent pulls its linked skills", sel{"agent_ids": []string{f.agentA}}, []string{"agent-a"},
			[]string{"style-guide", "security-checks"}, 2, 0},
		{"an agent without skills", sel{"agent_ids": []string{f.agentC}}, []string{"agent-c"}, []string{}, 0, 0},
		{"a skill alone", sel{"skill_ids": []string{f.s1}}, []string{}, []string{"style-guide"}, 1, 0},
		{"an unlinked skill alone", sel{"skill_ids": []string{f.s4}}, []string{}, []string{"unlinked-skill"}, 0, 0},
		{"agents and skills: the union", sel{"agent_ids": []string{f.agentB}, "skill_ids": []string{f.s4}},
			[]string{"agent-b"}, []string{"perf-hints", "unlinked-skill"}, 1, 0},
		{"a skill that an agent already pulls", sel{"agent_ids": []string{f.agentA}, "skill_ids": []string{f.s1}},
			[]string{"agent-a"}, []string{"style-guide", "security-checks"}, 2, 0},
		{"the same id twice", sel{"agent_ids": []string{f.agentC, f.agentC}}, []string{"agent-c"}, []string{}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := sdExportBundle(t, s.post("/plugins/export", tc.body))
			names := func(key string) []string {
				out := []string{}
				for _, item := range bundle[key].([]any) {
					out = append(out, item.(map[string]any)["name"].(string))
				}
				return out
			}
			if diff := cmp.Diff(tc.agents, names("agents")); diff != "" {
				t.Errorf("exported agents mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.skills, names("skills")); diff != "" {
				t.Errorf("exported skills mismatch (-want +got):\n%s", diff)
			}
			if got := len(bundle["eval_cases"].([]any)); got != tc.evalCases {
				t.Errorf("exported eval cases = %d, want %d", got, tc.evalCases)
			}
			if got := len(bundle["learnings"].([]any)); got != tc.learnings {
				t.Errorf("exported learnings = %d, want %d", got, tc.learnings)
			}
		})
	}
}

func TestSettingsD_ExportNameAndDescription(t *testing.T) { // FR4
	e := newSDEnv(t)
	e.seedExportable(t)
	e.setting(t, "workspace_name", `"Acme · Payments"`)
	s := e.boot(t)

	// The default name follows the workspace display name.
	got := s.post("/plugins/export", sdDoc{})
	if cd := got.Header.Get("Content-Disposition"); cd != `attachment; filename="devdigest-acme-payments-2026-10-26.zip"` {
		t.Errorf("default export Content-Disposition = %q, want the kebab-cased workspace name", cd)
	}
	if name := sdObj(t, sdExportBundle(t, got), "manifest")["name"]; name != "devdigest-acme-payments" {
		t.Errorf("default export manifest.name = %v, want devdigest-acme-payments", name)
	}

	got = s.post("/plugins/export", sdDoc{"name": "my-pack", "description": "What it is"})
	manifest := sdObj(t, sdExportBundle(t, got), "manifest")
	if manifest["name"] != "my-pack" || manifest["description"] != "What it is" {
		t.Errorf("named export manifest = %v, want name my-pack and its description", manifest)
	}
	if cd := got.Header.Get("Content-Disposition"); cd != `attachment; filename="my-pack-2026-10-26.zip"` {
		t.Errorf("named export Content-Disposition = %q, want my-pack-2026-10-26.zip", cd)
	}
}

func TestSettingsD_ExportRefusals(t *testing.T) { // AC5
	e := newSDEnv(t)
	s := e.boot(t)

	// An empty workspace has nothing to export.
	got := s.post("/plugins/export", sdDoc{})
	if code, _ := sdErr(t, got); got.Status != http.StatusUnprocessableEntity || code != "nothing_to_export" {
		t.Errorf("export of an empty workspace = %d %s, want 422 nothing_to_export", got.Status, got.Body)
	}

	f := e.seedExportable(t)
	const unknown = "6f1c2b9e-0a53-4d7e-9c4f-2a8b1d3e5f70"
	for _, body := range []sdDoc{
		{"agent_ids": []string{unknown}},
		{"skill_ids": []string{unknown}},
		{"agent_ids": []string{f.agentA, unknown}, "skill_ids": []string{f.s1}},
	} {
		got := s.post("/plugins/export", body)
		code, details := sdErr(t, got)
		if got.Status != http.StatusNotFound || code != "not_found" {
			t.Errorf("export of %v = %d %s, want 404 not_found", body, got.Status, got.Body)
			continue
		}
		if diff := cmp.Diff(map[string]any{"ids": []any{unknown}}, details); diff != "" {
			t.Errorf("export of %v details mismatch (-want +got):\n%s", body, diff)
		}
	}

	for _, body := range []any{
		sdDoc{"name": "Not Kebab"}, sdDoc{"name": ""}, sdDoc{"name": strings.Repeat("a", 65)},
		sdDoc{"description": strings.Repeat("d", 301)}, sdDoc{"agent_ids": []string{}}, sdDoc{"agent_ids": []string{"nope"}},
		sdDoc{"skill_ids": "x"}, sdDoc{"include_eval_cases": "yes"}, sdDoc{"include_learnings": 1},
		sdDoc{"unknown_key": true}, []any{}, `"text"`,
	} {
		got := s.post("/plugins/export", body)
		if got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "validation_error" {
			t.Errorf("export body %v = %d %s, want 422 validation_error", body, got.Status, got.Body)
		}
	}
	if list := s.get("/plugins").Obj(t); list["last_export"] != nil {
		t.Errorf("last_export after only refused exports = %v, want null", list["last_export"])
	}
}

func TestSettingsD_ExportNamesDuplicateAgentsInCreationOrder(t *testing.T) { // AC5
	e := newSDEnv(t)
	later := e.agent(t, "Reviewer", true, 10) // inserted first, created second
	earlier := e.agent(t, "Reviewer", true, 5)
	e.exec(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'agent', ?, 'case-of-later'), (?, 'agent', ?, 'case-of-earlier')`,
		e.ws, later, e.ws, earlier)
	s := e.boot(t)

	bundle := sdExportBundle(t, s.post("/plugins/export", sdDoc{}))
	var agents []string
	for _, a := range bundle["agents"].([]any) {
		agents = append(agents, a.(map[string]any)["name"].(string))
	}
	if want := []string{"Reviewer", "Reviewer (2)"}; !slices.Equal(agents, want) {
		t.Errorf("exported agent names = %v, want %v in creation order", agents, want)
	}
	owners := map[string]string{}
	for _, c := range bundle["eval_cases"].([]any) {
		m := c.(map[string]any)
		owners[m["name"].(string)] = m["owner_ref"].(string)
	}
	if want := map[string]string{"case-of-earlier": "Reviewer", "case-of-later": "Reviewer (2)"}; !cmp.Equal(owners, want) {
		t.Errorf("eval case owner_ref = %v, want %v", owners, want)
	}

	// The bundle re-imports into another workspace: both agents are created.
	archive := sdZipOf(t, bundle)
	other := newSDEnv(t)
	otherStack := other.boot(t)
	if got := sdImport(otherStack, sdZipMedia, archive, "?dry_run=true"); got.Status != http.StatusOK {
		t.Errorf("dry-run import of the export = %d %s, want 200", got.Status, got.Body)
	}
}

// ---- last export (P7) ----

func TestSettingsD_LastExportIsNotASettingAUserCanWrite(t *testing.T) { // P7
	e := newSDEnv(t)
	e.seedExportable(t)
	s := e.boot(t)

	value := sdDoc{"file_name": "forged.zip", "exported_at": "2026-01-01T00:00:00.000Z",
		"counts": sdDoc{"agents": 1, "skills": 1, "eval_cases": 1, "learnings": 1}}
	put := putSettings(s, "plugin_last_export", value)
	if put.Status != http.StatusUnprocessableEntity || put.errorCode(t) != "validation_error" {
		t.Fatalf("PUT /settings plugin_last_export = %d %s, want 422 validation_error", put.Status, put.Body)
	}
	if paths := issuePaths(t, put); !slices.Contains(paths, "[plugin_last_export]") {
		t.Errorf("PUT /settings plugin_last_export issue paths = %v, want one at plugin_last_export", paths)
	}
	if list := s.get("/plugins").Obj(t); list["last_export"] != nil {
		t.Fatalf("GET /plugins last_export after the refused PUT = %v, want null", list["last_export"])
	}

	if got := s.post("/plugins/export", sdDoc{}); got.Status != http.StatusOK {
		t.Fatalf("POST /plugins/export = %d %s, want 200", got.Status, got.Body)
	}
	last := sdObj(t, s.get("/plugins").Obj(t), "last_export")
	if last["file_name"] != "devdigest-default-2026-10-26.zip" {
		t.Errorf("GET /plugins last_export = %v, want the file of the export", last)
	}

	// The workspace export (Slice B) never holds the plugin setting: not as a key, not as text.
	ws := s.get("/workspace/export")
	if ws.Status != http.StatusOK {
		t.Fatalf("GET /workspace/export = %d %s, want 200", ws.Status, ws.Body)
	}
	if strings.Contains(string(ws.Body), "plugin_last_export") || strings.Contains(string(ws.Body), "devdigest-default-2026") {
		t.Errorf("workspace export holds the last plugin export: %.300s", ws.Body)
	}
	settings := sdObj(t, ws.Obj(t), "settings")
	if _, ok := settings["plugin_last_export"]; ok {
		t.Errorf("workspace export settings hold plugin_last_export: %v", sdSortedKeys(settings))
	}
}

// ---- secrets (AC6) ----

// sdSecretValues are recognisable secrets of the secrets file; sdSecretNote is more file content.
var sdSecretValues = map[string]string{
	"OPENAI_API_KEY":     "sk-sdleak-openai-7Hq2ZxK9",
	"ANTHROPIC_API_KEY":  "sk-ant-sdleak-9Lp4VcW1",
	"OPENROUTER_API_KEY": "sk-or-v1-sdleak-3Mn8BtR5",
	"GITHUB_TOKEN":       "ghp_sdleak0Rt5Wy1KdX2",
	"SD_EXTRA_NOTE":      "sdleak-extra-file-content-4Jh6",
}

func TestSettingsD_NoSecretReachesAnExportAnAnswerOrTheLog(t *testing.T) { // AC6
	e := newSDEnv(t)
	e.seedExportable(t)
	raw := sdMarshal(t, sdSecretValues)
	s := e.boot(t, func(sp *spec) { sp.secretFile = string(raw) })

	// The secrets are in use: the settings report them as configured.
	status := s.get("/settings/secrets-status")
	if status.Status != http.StatusOK || !strings.Contains(string(status.Body), "true") {
		t.Fatalf("GET /settings/secrets-status = %d %s, want the secrets reported as configured", status.Status, status.Body)
	}

	secrets := []string{string(raw)}
	for _, v := range sdSecretValues {
		secrets = append(secrets, v)
	}
	var bodies []string
	collect := func(what string, got response, wantStatus int) response {
		t.Helper()
		if got.Status != wantStatus {
			t.Fatalf("%s = %d %s, want %d", what, got.Status, got.Body, wantStatus)
		}
		bodies = append(bodies, string(got.Body))
		for k, v := range got.Header {
			bodies = append(bodies, k+": "+strings.Join(v, ","))
		}
		return got
	}

	exp := collect("POST /plugins/export", s.post("/plugins/export", sdDoc{"include_learnings": true}), http.StatusOK)
	_, files := sdUnzip(t, exp.Body)
	inflated := string(files["devdigest-plugin.json"])
	for _, secret := range secrets {
		assertNoSecret(t, "export archive bytes", string(exp.Body), secret)
		assertNoSecret(t, "export bundle", inflated, secret)
	}
	if strings.Contains(inflated, "secrets.json") {
		t.Errorf("export bundle names the secrets file")
	}

	// The same bundle through the preview, the install, the list and the agent and skill reads.
	archive := exp.Body
	other := sdMarshal(t, sdSmallBundle("leak-check"))
	collect("dry-run import of the export", sdImport(s, sdZipMedia, archive, "?dry_run=true"), http.StatusOK)
	collect("dry-run import of a bundle", sdImport(s, sdJSONMedia, other, "?dry_run=true"), http.StatusOK)
	inst := collect("install", sdImport(s, sdJSONMedia, other, "?filename=leak.json"), http.StatusCreated)
	id := sdObj(t, inst.Obj(t), "installed")["id"].(string)
	collect("GET /plugins", s.get("/plugins"), http.StatusOK)
	collect("PATCH /plugins/:id", s.do(http.MethodPatch, "/plugins/"+id, sdDoc{"enabled": true}), http.StatusOK)
	collect("GET /agents", s.get("/agents"), http.StatusOK)
	collect("GET /skills", s.get("/skills"), http.StatusOK)
	collect("a refused import", sdImport(s, sdZipMedia, []byte("not a zip"), ""), http.StatusUnprocessableEntity)
	collect("DELETE /plugins/:id", s.do(http.MethodDelete, "/plugins/"+id, sdDoc{"confirm": "leak-check"}), http.StatusOK)

	for i, body := range bodies {
		for _, secret := range secrets {
			assertNoSecret(t, fmt.Sprintf("response %d", i), body, secret)
		}
	}
	for _, secret := range secrets {
		assertNoSecret(t, "server log", s.logs.String(), secret)
	}
	if lines := logLines(t, s.logs, "plugin exported"); len(lines) != 1 {
		t.Errorf("'plugin exported' log lines = %d, want 1", len(lines))
	}
}

// ---- preview and conflicts (AC7) ----

func TestSettingsD_PreviewListsConflictsAndStoresNothing(t *testing.T) { // AC7, FR6, FR8
	e := newSDEnv(t)
	s := e.boot(t)
	sdInstall(t, s, sdSmallBundle("taken"), "taken.zip")
	e.skill(t, "shared-skill", "manual")
	before := sbTableCounts(t, e.db.DB)

	doc := sdBundle("taken",
		[]sdDoc{
			sdAgent("alpha", []string{"shared-skill", "fresh-skill"}, nil),
			sdAgent("beta", nil, sdDoc{"enabled": false, "provider": "anthropic"}),
		},
		[]sdDoc{sdSkill("shared-skill", nil), sdSkill("fresh-skill", sdDoc{"type": "security", "enabled": false})},
		[]sdDoc{sdCase("c1", "agent", "alpha", nil)},
		[]sdDoc{sdLearning("global", "fact", "x", nil, nil)})

	want := sdDoc{
		"manifest": sdDoc{
			"name": "taken", "version": "1.2.0", "format": "devdigest-plugin/v1", "kind": "plugin",
			"exported_at": "2026-10-20T08:00:00.000Z", "description": "A pack of taken",
			"counts": sdDoc{"agents": 2.0, "skills": 2.0, "eval_cases": 1.0, "conventions": 0.0, "learnings": 1.0},
		},
		"kind":   "plugin",
		"counts": sdDoc{"agents": 2.0, "skills": 2.0, "eval_cases": 1.0, "conventions": 0.0, "learnings": 1.0},
		"agents": []any{
			sdDoc{"name": "alpha", "provider": "openai", "model": "gpt-x", "enabled": true, "skills": []any{"shared-skill", "fresh-skill"}},
			sdDoc{"name": "beta", "provider": "anthropic", "model": "gpt-x", "enabled": false, "skills": []any{}},
		},
		"skills": []any{
			sdDoc{"name": "shared-skill", "type": "rubric", "description": "about shared-skill", "enabled": true},
			sdDoc{"name": "fresh-skill", "type": "security", "description": "about fresh-skill", "enabled": false},
		},
		"conflicts": sdDoc{"plugin": true, "skills": []any{"shared-skill"}},
		"ignored":   []any{},
		"warnings":  []any{},
	}
	for _, c := range []struct{ name, media string }{{"zip", sdZipMedia}, {"json", sdJSONMedia}} {
		t.Run(c.name, func(t *testing.T) {
			body := sdZipOf(t, doc)
			if c.media == sdJSONMedia {
				body = sdMarshal(t, doc)
			}
			got := sdImport(s, c.media, body, "?dry_run=true")
			if got.Status != http.StatusOK {
				t.Fatalf("dry-run import = %d %s, want 200", got.Status, got.Body)
			}
			if diff := cmp.Diff(want, sdDoc(got.Obj(t))); diff != "" {
				t.Errorf("dry-run import mismatch (-want +got):\n%s", diff)
			}
			assertCountsUnchanged(t, "dry run", e.db.DB, before)

			// Installing the same upload is refused with the same details, and stores nothing.
			inst := sdImport(s, c.media, body, "")
			code, details := sdErr(t, inst)
			if inst.Status != http.StatusConflict || code != "plugin_conflict" {
				t.Fatalf("install of the conflicting upload = %d %s, want 409 plugin_conflict", inst.Status, inst.Body)
			}
			if diff := cmp.Diff(map[string]any{"plugin": true, "skills": []any{"shared-skill"}}, details); diff != "" {
				t.Errorf("409 details mismatch (-want +got):\n%s", diff)
			}
			assertCountsUnchanged(t, "refused install", e.db.DB, before)
		})
	}

	// A skill name alone is a conflict too, and a clean bundle has none.
	solo := sdBundle("fresh-name", nil, []sdDoc{sdSkill("shared-skill", nil)}, nil, nil)
	got := sdPreview(t, s, solo).Obj(t)
	if diff := cmp.Diff(sdDoc{"plugin": false, "skills": []any{"shared-skill"}}, sdDoc(sdObj(t, got, "conflicts"))); diff != "" {
		t.Errorf("skill-only conflict mismatch (-want +got):\n%s", diff)
	}
	clean := sdPreview(t, s, sdSmallBundle("clean")).Obj(t)
	if diff := cmp.Diff(sdDoc{"plugin": false, "skills": []any{}}, sdDoc(sdObj(t, clean, "conflicts"))); diff != "" {
		t.Errorf("clean conflicts mismatch (-want +got):\n%s", diff)
	}
}

func TestSettingsD_PreviewListsIgnoredPartsOfTheArchive(t *testing.T) { // FR6, EC
	e := newSDEnv(t)
	s := e.boot(t)
	doc := sdSmallBundle("with-extras")
	doc["unknown_section"] = sdDoc{"x": 1}
	archive := sdZip(t,
		sdEntry{"pack/devdigest-plugin.json", sdMarshal(t, doc)},
		sdEntry{"pack/README.md", []byte("readme")},
		sdEntry{"pack/scripts/run.sh", []byte("#!/bin/sh")})
	got := sdImport(s, sdZipMedia, archive, "?dry_run=true")
	if got.Status != http.StatusOK {
		t.Fatalf("dry-run import of a folder archive = %d %s, want 200", got.Status, got.Body)
	}
	body := got.Obj(t)
	var paths []string
	for _, ig := range body["ignored"].([]any) {
		paths = append(paths, ig.(map[string]any)["path_or_section"].(string))
	}
	for _, want := range []string{"pack/README.md", "pack/scripts/run.sh"} {
		if !slices.Contains(paths, want) {
			t.Errorf("ignored = %v, want it to list %s", paths, want)
		}
	}
	if warnings := body["warnings"].([]any); len(warnings) == 0 {
		t.Errorf("warnings = [], want one for the unknown key")
	}
}

// ---- install and read-back (P2, AC8, AC17) ----

// sdPackDoc is the plugin of the read-back tests: two agents (one linking two skills in a fixed
// order), three skills, two eval cases and two learnings. The skill names start with prefix.
func sdPackDoc(prefix string) sdDoc {
	style, security, unused := prefix+"style-guide", prefix+"security-checks", prefix+"unused-skill"
	return sdBundle("tools-pack",
		[]sdDoc{
			sdAgent("planner", []string{style, security}, sdDoc{
				"description": "plans work", "provider": "anthropic", "model": "claude-x", "system_prompt": "You plan.",
				"output_schema": sdDoc{"type": "object"}, "strategy": "map-reduce", "ci_fail_on": "warning", "repo_intel": false,
			}),
			sdAgent("solo", nil, sdDoc{"enabled": false, "provider": "openrouter", "model": "a/b"}),
		},
		[]sdDoc{
			sdSkill(style, sdDoc{"evidence_files": []string{"a.go", "b.go"}, "body": "Use tabs.", "description": "style"}),
			sdSkill(security, sdDoc{"type": "security", "enabled": false, "body": "No secrets."}),
			sdSkill(unused, sdDoc{"type": "custom"}),
		},
		[]sdDoc{
			sdCase("plan-case", "agent", "planner", sdDoc{
				"input_diff": "diff --git", "input_files": []sdDoc{{"path": "x"}}, "input_meta": sdDoc{"k": 1},
				"expected_output": sdDoc{"findings": []sdDoc{}}, "notes": "n",
			}),
			sdCase("style-case", "skill", style, nil),
		},
		[]sdDoc{
			sdLearning("repo", "decision", "prefer tabs", 0.8, "org/x"),
			sdLearning("global", "fact", "go is fast", nil, nil),
		})
}

func TestSettingsD_InstalledPluginReadsBackThroughTheAgentAndSkillAPIs(t *testing.T) { // P2, AC8, AC17, FR7, FR14
	e := newSDEnv(t)
	userAgent := e.agent(t, "mine", true, 0)
	userSkill := e.skill(t, "my-skill", "manual")
	s := e.boot(t)

	doc := sdPackDoc("")
	got := sdInstall(t, s, doc, "tools.zip")

	pluginID := sdObj(t, got, "installed")["id"].(string)
	wantInstalled := sdDoc{
		"id": pluginID, "name": "tools-pack", "version": "1.2.0", "source": "tools.zip",
		"installed_at": sdObj(t, got, "installed")["installed_at"], "enabled": false, "description": "A pack of tools-pack",
		"counts": sdDoc{"agents": 2.0, "skills": 3.0, "eval_cases": 2.0, "learnings": 2.0},
	}
	if diff := cmp.Diff(wantInstalled, sdDoc(sdObj(t, got, "installed"))); diff != "" {
		t.Errorf("install answer installed mismatch (-want +got):\n%s", diff)
	}
	wantCreated := sdDoc{"agents": 2.0, "skills": 3.0, "eval_cases": 2.0, "conventions": 0.0, "learnings": 2.0}
	if diff := cmp.Diff(wantCreated, sdDoc(sdObj(t, got, "created"))); diff != "" {
		t.Errorf("install answer created mismatch (-want +got):\n%s", diff)
	}
	wantRef := sdDoc{"id": pluginID, "name": "tools-pack", "enabled": false}

	skills := sdByName(t, s.get("/skills"))
	agents := sdByName(t, s.get("/agents"))

	t.Run("skills", func(t *testing.T) {
		for _, sk := range doc["skills"].([]sdDoc) {
			name := sk["name"].(string)
			summary, ok := skills[name]
			if !ok {
				t.Errorf("GET /skills lacks %s", name)
				continue
			}
			one := s.get("/skills/" + summary["id"].(string))
			if one.Status != http.StatusOK {
				t.Fatalf("GET /skills/%s = %d %s, want 200", name, one.Status, one.Body)
			}
			skill := one.Obj(t)
			want := sdDoc{
				"name": name, "description": sk["description"], "type": sk["type"], "body": sk["body"],
				"enabled": sk["enabled"], "version": 1.0, "source": "imported_file", "source_ref": "plugin:tools-pack",
				"plugin": wantRef,
			}
			// The skills API never shows the evidence files (null on the wire); the table keeps them.
			var stored []string
			if err := json.Unmarshal([]byte(scalar[string](t, e.db.DB, `SELECT coalesce(evidence_files::text, 'null') FROM skills WHERE id = ?`, summary["id"])), &stored); err != nil {
				t.Fatal(err)
			}
			wantStored, _ := sk["evidence_files"].([]string)
			if diff := cmp.Diff(wantStored, stored); diff != "" {
				t.Errorf("skill %s stored evidence_files mismatch (-want +got):\n%s", name, diff)
			}
			for key, w := range want {
				if diff := cmp.Diff(w, skill[key]); diff != "" {
					t.Errorf("GET /skills/%s %s mismatch (-want +got):\n%s", name, key, diff)
				}
			}
			// Version 1 is the install: the body and description of the bundle, the plugin as the note.
			versions := s.get("/skills/" + summary["id"].(string) + "/versions")
			if versions.Status != http.StatusOK {
				t.Fatalf("GET /skills/%s/versions = %d %s, want 200", name, versions.Status, versions.Body)
			}
			list := versions.JSON(t).([]any)
			if len(list) != 1 {
				t.Fatalf("skill %s versions = %d, want exactly version 1", name, len(list))
			}
			v1 := list[0].(map[string]any)
			wantV1 := sdDoc{"version": 1.0, "body": sk["body"], "description": sk["description"], "message": "Installed from plugin tools-pack"}
			for key, w := range wantV1 {
				if diff := cmp.Diff(w, v1[key]); diff != "" {
					t.Errorf("skill %s version 1 %s mismatch (-want +got):\n%s", name, key, diff)
				}
			}
		}
	})

	t.Run("agents", func(t *testing.T) {
		for _, a := range doc["agents"].([]sdDoc) {
			name := a["name"].(string)
			summary, ok := agents[name]
			if !ok {
				t.Errorf("GET /agents lacks %s", name)
				continue
			}
			id := summary["id"].(string)
			one := s.get("/agents/" + id)
			if one.Status != http.StatusOK {
				t.Fatalf("GET /agents/%s = %d %s, want 200", name, one.Status, one.Body)
			}
			agent := one.Obj(t)
			for _, key := range []string{"name", "description", "provider", "model", "system_prompt", "output_schema", "enabled", "strategy", "ci_fail_on", "repo_intel"} {
				if diff := cmp.Diff(jsonRoundTrip(t, a[key]), agent[key]); diff != "" {
					t.Errorf("GET /agents/%s %s mismatch (-want +got):\n%s", name, key, diff)
				}
			}
			if diff := cmp.Diff(any(wantRef), agent["plugin"]); diff != "" {
				t.Errorf("GET /agents/%s plugin mismatch (-want +got):\n%s", name, diff)
			}

			// The links keep the bundle's order and name the installed skills.
			links := s.get("/agents/" + id + "/skills")
			if links.Status != http.StatusOK {
				t.Fatalf("GET /agents/%s/skills = %d %s, want 200", name, links.Status, links.Body)
			}
			var linked []string
			ordered := links.JSON(t).([]any)
			sort.SliceStable(ordered, func(i, j int) bool {
				return ordered[i].(map[string]any)["order"].(float64) < ordered[j].(map[string]any)["order"].(float64)
			})
			var linkedIDs []any
			for i, l := range ordered {
				link := l.(map[string]any)
				if link["order"] != float64(i) {
					t.Errorf("agent %s link %d has order %v, want %d", name, i, link["order"], i)
				}
				for skillName, sk := range skills {
					if sk["id"] == link["skill_id"] {
						linked = append(linked, skillName)
					}
				}
				linkedIDs = append(linkedIDs, link["skill_id"])
			}
			if want := a["skills"].([]string); !slices.Equal(linked, want) {
				t.Errorf("agent %s linked skills = %v, want %v in link order", name, linked, want)
			}

			// The stored versions: 1 without links, and 2 with them when the agent links skills.
			v1 := s.get("/agents/" + id + "/versions/1")
			if v1.Status != http.StatusOK {
				t.Fatalf("GET /agents/%s/versions/1 = %d %s, want 200", name, v1.Status, v1.Body)
			}
			cfg1 := sdObj(t, v1.Obj(t), "config")
			if diff := cmp.Diff(any(a["system_prompt"]), cfg1["system_prompt"]); diff != "" {
				t.Errorf("agent %s version 1 system_prompt mismatch (-want +got):\n%s", name, diff)
			}
			if len(linkedIDs) > 0 {
				v2 := s.get("/agents/" + id + "/versions/2")
				if v2.Status != http.StatusOK {
					t.Fatalf("GET /agents/%s/versions/2 = %d %s, want 200", name, v2.Status, v2.Body)
				}
				if diff := cmp.Diff(linkedIDs, sdObj(t, v2.Obj(t), "config")["skills"]); diff != "" {
					t.Errorf("agent %s version 2 skills mismatch (-want +got):\n%s", name, diff)
				}
				if agent["version"] != 2.0 {
					t.Errorf("agent %s version = %v, want 2", name, agent["version"])
				}
			} else if agent["version"] != 1.0 {
				t.Errorf("agent %s version = %v, want 1", name, agent["version"])
			}
		}
	})

	t.Run("eval cases and learnings", func(t *testing.T) {
		type caseRow struct {
			Name, OwnerKind, OwnerID, InputDiff, Notes string
			InputFiles, InputMeta, ExpectedOutput      string
		}
		var cases []caseRow
		if err := e.db.DB.Raw(`SELECT name, owner_kind, owner_id::text AS owner_id, coalesce(input_diff, '') AS input_diff,
			coalesce(notes, '') AS notes, coalesce(input_files::text, '') AS input_files, coalesce(input_meta::text, '') AS input_meta,
			coalesce(expected_output::text, '') AS expected_output, false AS plugin_owned
			FROM eval_cases WHERE workspace_id = ? ORDER BY name`, e.ws).Scan(&cases).Error; err != nil {
			t.Fatal(err)
		}
		wantCases := []caseRow{
			{Name: "plan-case", OwnerKind: "agent", OwnerID: agents["planner"]["id"].(string), InputDiff: "diff --git", Notes: "n",
				InputFiles: `[{"path": "x"}]`, InputMeta: `{"k": 1}`, ExpectedOutput: `{"findings": []}`},
			{Name: "style-case", OwnerKind: "skill", OwnerID: skills["style-guide"]["id"].(string)},
		}
		if diff := cmp.Diff(wantCases, cases); diff != "" {
			t.Errorf("installed eval cases mismatch (-want +got):\n%s", diff)
		}

		type learning struct {
			Scope, Kind, Content string
			Confidence           *float64
			NoRepo               bool
			Plugin               string
		}
		var learnings []learning
		if err := e.db.DB.Raw(`SELECT scope, kind, content, confidence, repo_id IS NULL AS no_repo, coalesce(plugin_id::text, '') AS plugin
			FROM memory WHERE workspace_id = ? ORDER BY content`, e.ws).Scan(&learnings).Error; err != nil {
			t.Fatal(err)
		}
		conf := 0.8
		wantLearnings := []learning{
			{Scope: "global", Kind: "fact", Content: "go is fast", NoRepo: true, Plugin: pluginID},
			{Scope: "repo", Kind: "decision", Content: "prefer tabs", Confidence: &conf, NoRepo: true, Plugin: pluginID},
		}
		if diff := cmp.Diff(wantLearnings, learnings); diff != "" {
			t.Errorf("installed learnings mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("the user's own items carry no plugin", func(t *testing.T) { // AC17
		if agents["mine"]["plugin"] != nil || skills["my-skill"]["plugin"] != nil {
			t.Errorf("list plugin of user items = %v and %v, want null", agents["mine"]["plugin"], skills["my-skill"]["plugin"])
		}
		for path, plugin := range map[string]any{"/agents/" + userAgent: nil, "/skills/" + userSkill: nil} {
			one := s.get(path).Obj(t)
			if v, ok := one["plugin"]; !ok || v != plugin {
				t.Errorf("GET %s plugin = %v (present %v), want an explicit null", path, v, ok)
			}
		}
		for name, item := range agents {
			if name == "mine" {
				continue
			}
			if diff := cmp.Diff(any(wantRef), item["plugin"]); diff != "" {
				t.Errorf("GET /agents %s plugin mismatch (-want +got):\n%s", name, diff)
			}
		}
		for name, item := range skills {
			if name == "my-skill" {
				continue
			}
			if diff := cmp.Diff(any(wantRef), item["plugin"]); diff != "" {
				t.Errorf("GET /skills %s plugin mismatch (-want +got):\n%s", name, diff)
			}
		}
	})

	t.Run("GET /plugins shows the counts", func(t *testing.T) {
		list := s.get("/plugins").Obj(t)
		plugins := list["plugins"].([]any)
		if len(plugins) != 1 {
			t.Fatalf("GET /plugins plugins = %v, want the one installed plugin", plugins)
		}
		if diff := cmp.Diff(any(wantInstalled), plugins[0]); diff != "" {
			t.Errorf("GET /plugins row mismatch (-want +got):\n%s", diff)
		}
	})
}

// jsonRoundTrip turns a Go value into what encoding/json decodes it to, to compare with a response.
func jsonRoundTrip(t testing.TB, v any) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(sdMarshal(t, v), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSettingsD_InstallIsAllOrNothingOnAConflictingSkill(t *testing.T) { // AC8, EC6 (the forced DB failure is plugins/adapters/postgres)
	e := newSDEnv(t)
	e.skill(t, "taken-name", "manual")
	s := e.boot(t)
	before := sbTableCounts(t, e.db.DB)

	doc := sdBundle("rollback-check", []sdDoc{sdAgent("a", []string{"taken-name"}, nil)}, []sdDoc{sdSkill("taken-name", nil)}, nil, nil)
	got := sdImport(s, sdZipMedia, sdZipOf(t, doc), "")
	if code, _ := sdErr(t, got); got.Status != http.StatusConflict || code != "plugin_conflict" {
		t.Fatalf("install over an existing skill = %d %s, want 409 plugin_conflict", got.Status, got.Body)
	}
	assertCountsUnchanged(t, "refused install", e.db.DB, before)
}

// ---- enable, disable, remove (AC15) ----

func TestSettingsD_EnableAndDisableAPlugin(t *testing.T) { // AC15, FR11
	e := newSDEnv(t)
	s := e.boot(t)
	id := sdObj(t, sdInstall(t, s, sdSmallBundle("toggle"), "toggle.zip"), "installed")["id"].(string)

	for _, enabled := range []bool{true, false, false, true} {
		got := s.do(http.MethodPatch, "/plugins/"+id, sdDoc{"enabled": enabled})
		if got.Status != http.StatusOK {
			t.Fatalf("PATCH /plugins/:id enabled=%v = %d %s, want 200", enabled, got.Status, got.Body)
		}
		row := got.Obj(t)
		if row["enabled"] != enabled || row["id"] != id || row["name"] != "toggle" {
			t.Errorf("PATCH enabled=%v answered %v, want the row with that flag", enabled, row)
		}
		// The flag shows in the list and on the plugin's agents.
		list := s.get("/plugins").Obj(t)["plugins"].([]any)
		if list[0].(map[string]any)["enabled"] != enabled {
			t.Errorf("GET /plugins enabled after PATCH %v = %v", enabled, list[0].(map[string]any)["enabled"])
		}
		agent := sdByName(t, s.get("/agents"))["toggle-agent"]
		if plugin := agent["plugin"].(map[string]any); plugin["enabled"] != enabled {
			t.Errorf("agent plugin.enabled after PATCH %v = %v", enabled, plugin["enabled"])
		}
	}

	const unknown = "6f1c2b9e-0a53-4d7e-9c4f-2a8b1d3e5f70"
	if got := s.do(http.MethodPatch, "/plugins/"+unknown, sdDoc{"enabled": true}); got.Status != http.StatusNotFound || got.errorCode(t) != "not_found" {
		t.Errorf("PATCH of an unknown plugin = %d %s, want 404 not_found", got.Status, got.Body)
	}
	for _, body := range []any{sdDoc{"enabled": "yes"}, sdDoc{"enabled": 1}, sdDoc{"enabled": nil}, sdDoc{}, sdDoc{"enabled": true, "name": "x"}, []any{}} {
		if got := s.do(http.MethodPatch, "/plugins/"+id, body); got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "validation_error" {
			t.Errorf("PATCH with %v = %d %s, want 422 validation_error", body, got.Status, got.Body)
		}
	}
	if got := s.do(http.MethodPatch, "/plugins/not-a-uuid", sdDoc{"enabled": true}); got.Status != http.StatusUnprocessableEntity {
		t.Errorf("PATCH with a malformed id = %d %s, want 422", got.Status, got.Body)
	}
	if row := s.get("/plugins").Obj(t)["plugins"].([]any)[0].(map[string]any); row["enabled"] != true {
		t.Errorf("plugin enabled after the refused PATCHes = %v, want the last accepted value true", row["enabled"])
	}
}

func TestSettingsD_RemoveNeedsTheExactName(t *testing.T) { // FR12 (the full cascade is plugin_gating_test.go)
	e := newSDEnv(t)
	s := e.boot(t)
	id := sdObj(t, sdInstall(t, s, sdPackDoc(""), "tools.zip"), "installed")["id"].(string)
	before := sbTableCounts(t, e.db.DB)

	for _, confirm := range []string{"", "tools", "Tools-Pack", "tools-pack ", " tools-pack"} {
		got := s.do(http.MethodDelete, "/plugins/"+id, sdDoc{"confirm": confirm})
		if got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "confirmation_mismatch" {
			t.Errorf("DELETE with confirm %q = %d %s, want 422 confirmation_mismatch", confirm, got.Status, got.Body)
		}
	}
	for _, body := range []any{sdDoc{}, sdDoc{"confirm": 1}, sdDoc{"confirm": "tools-pack", "x": 1}} {
		if got := s.do(http.MethodDelete, "/plugins/"+id, body); got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "validation_error" {
			t.Errorf("DELETE with %v = %d %s, want 422 validation_error", body, got.Status, got.Body)
		}
	}
	assertCountsUnchanged(t, "refused removals", e.db.DB, before)

	got := s.do(http.MethodDelete, "/plugins/"+id, sdDoc{"confirm": "tools-pack"})
	if got.Status != http.StatusOK {
		t.Fatalf("DELETE with the exact name = %d %s, want 200", got.Status, got.Body)
	}
	want := sdDoc{"ok": true, "removed": sdDoc{"agents": 2.0, "skills": 3.0, "eval_cases": 2.0, "learnings": 2.0, "ci_installations": 0.0}}
	if diff := cmp.Diff(want, sdDoc(got.Obj(t))); diff != "" {
		t.Errorf("DELETE answer mismatch (-want +got):\n%s", diff)
	}
	if got := s.do(http.MethodDelete, "/plugins/"+id, sdDoc{"confirm": "tools-pack"}); got.Status != http.StatusNotFound {
		t.Errorf("second DELETE = %d %s, want 404", got.Status, got.Body)
	}
}

// ---- hostile uploads (AC9) ----

// sdLieAboutSize sets the uncompressed size that the central directory of an archive declares for
// every entry, as an attacker's archive does to look small.
func sdLieAboutSize(t testing.TB, archive []byte, size uint32) []byte {
	t.Helper()
	out := bytes.Clone(archive)
	found := 0
	for i := 0; i+46 <= len(out); i++ {
		if bytes.Equal(out[i:i+4], []byte("PK\x01\x02")) {
			binary.LittleEndian.PutUint32(out[i+24:], size)
			found++
		}
	}
	if found == 0 {
		t.Fatal("no central directory header found")
	}
	return out
}

// sdPeakHeap runs f and returns the heap in use before it (after a GC) and the highest value a
// sampler saw while it ran.
func sdPeakHeap(f func()) (base, peak uint64) {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base, peak = ms.HeapAlloc, ms.HeapAlloc
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
	}()
	f()
	close(stop)
	<-done
	return base, peak
}

func TestSettingsD_HostileArchivesAreRefusedAndStoreNothing(t *testing.T) { // AC9, EC1-EC4, NFR1
	e := newSDEnv(t)
	s := e.boot(t)
	valid := sdMarshal(t, sdSmallBundle("hostile"))
	before := sbTableCounts(t, e.db.DB)

	withFormat := sdSmallBundle("hostile")
	withFormat["manifest"].(sdDoc)["format"] = "devdigest-plugin/v2"
	missingSkill := sdBundle("hostile", []sdDoc{sdAgent("a", []string{"not-there"}, nil)}, nil, nil, nil)
	manyEntries := make([]sdEntry, 0, 501)
	manyEntries = append(manyEntries, sdEntry{"devdigest-plugin.json", valid})
	for i := 0; i < 500; i++ {
		manyEntries = append(manyEntries, sdEntry{fmt.Sprintf("f%03d.txt", i), nil})
	}

	cases := []struct {
		name    string
		archive []byte
		want    string // a part of the message that names the rule
	}{
		{"inflates past 20 MiB", sdZip(t, sdEntry{"devdigest-plugin.json", bytes.Repeat([]byte(" "), 25<<20)}), "20"},
		{"inflates past 20 MiB under a header that lies", sdLieAboutSize(t,
			sdZip(t, sdEntry{"devdigest-plugin.json", bytes.Repeat([]byte(" "), 25<<20)}), 100), "20"},
		{"a parent directory path", sdZip(t, sdEntry{"devdigest-plugin.json", valid}, sdEntry{"../x", []byte("x")}), "../x"},
		{"an absolute path", sdZip(t, sdEntry{"devdigest-plugin.json", valid}, sdEntry{"/etc/passwd", []byte("x")}), "/etc/passwd"},
		{"a backslash path", sdZip(t, sdEntry{"devdigest-plugin.json", valid}, sdEntry{`a\b`, []byte("x")}), `a\\b`},
		{"a drive letter path", sdZip(t, sdEntry{"devdigest-plugin.json", valid}, sdEntry{"C:/x", []byte("x")}), "C:/x"},
		{"no bundle file", sdZip(t, sdEntry{"readme.txt", []byte("x")}), "devdigest-plugin.json"},
		{"two bundle files", sdZip(t, sdEntry{"devdigest-plugin.json", valid}, sdEntry{"pack/devdigest-plugin.json", valid}), "devdigest-plugin.json"},
		{"invalid JSON", sdZip(t, sdEntry{"devdigest-plugin.json", []byte(`{"manifest": `)}), "JSON"},
		{"a bundle that is not an object", sdZip(t, sdEntry{"devdigest-plugin.json", []byte(`[1,2]`)}), ""},
		{"another format", sdZipOf(t, withFormat), "devdigest-plugin/v1"},
		{"a missing skill", sdZipOf(t, missingSkill), "not-there"},
		{"more than 500 entries", sdZip(t, manyEntries...), "500"},
		{"not an archive", []byte("PK\x03\x04 and then nothing"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, query := range []string{"?dry_run=true", ""} {
				var got response
				base, peak := sdPeakHeap(func() { got = sdImport(s, sdZipMedia, tc.archive, query) })
				code, details := sdErr(t, got)
				if got.Status != http.StatusUnprocessableEntity || code != "invalid_plugin" {
					t.Fatalf("import%s = %d %s, want 422 invalid_plugin", query, got.Status, got.Body)
				}
				e, _ := got.Obj(t)["error"].(map[string]any)
				if msg, _ := e["message"].(string); msg == "" || !strings.Contains(msg, tc.want) {
					t.Errorf("import%s message = %q, want it to name %q", query, msg, tc.want)
				}
				if issues, _ := details["issues"].([]any); len(issues) == 0 || len(issues) > 20 {
					t.Errorf("import%s details.issues = %v, want 1 to 20 issues", query, details["issues"])
				}
				if strings.HasPrefix(tc.name, "inflates past 20 MiB") {
					t.Logf("import%s: the heap peaked %d MiB above its start", query, (max(peak, base)-base)>>20)
					if peak > base && peak-base > 64<<20 {
						t.Errorf("heap grew by %d MiB while refusing the bomb, want < 64 MiB", (peak-base)>>20)
					}
				}
			}
			assertCountsUnchanged(t, tc.name, e.db.DB, before)
		})
	}

	t.Run("a bomb that is a JSON body", func(t *testing.T) {
		doc := `{"manifest":{"name":"x"},"pad":"` + strings.Repeat("a", 1<<20) + `"}`
		got := sdImport(s, sdJSONMedia, []byte(doc), "?dry_run=true")
		if code, _ := sdErr(t, got); got.Status != http.StatusUnprocessableEntity || code != "invalid_plugin" {
			t.Errorf("import of an incomplete JSON bundle = %d %.200s, want 422 invalid_plugin", got.Status, got.Body)
		}
	})
}

// ---- the raw body rules of the import route (AC10, D16) ----

func TestSettingsD_ImportBodyRules(t *testing.T) { // AC10, FR6, NFR3
	e := newSDEnv(t)
	s := e.boot(t)
	valid := sdMarshal(t, sdSmallBundle("rules"))
	before := sbTableCounts(t, e.db.DB)

	t.Run("a zip over 5 MiB is 413", func(t *testing.T) {
		got := sdImport(s, sdZipMedia, bytes.Repeat([]byte{0}, 6<<20), "")
		if got.Status != http.StatusRequestEntityTooLarge || got.errorCode(t) != "payload_too_large" {
			t.Errorf("6 MiB zip = %d %.200s, want 413 payload_too_large", got.Status, got.Body)
		}
	})
	t.Run("a JSON body over 20 MiB is 413", func(t *testing.T) {
		got := sdImport(s, sdJSONMedia, bytes.Repeat([]byte(" "), 21<<20), "")
		if got.Status != http.StatusRequestEntityTooLarge || got.errorCode(t) != "payload_too_large" {
			t.Errorf("21 MiB JSON = %d %.200s, want 413 payload_too_large", got.Status, got.Body)
		}
	})
	t.Run("a JSON body of 6 MiB is allowed and judged as a bundle", func(t *testing.T) {
		doc := sdSmallBundle("big")
		doc["agents"].([]sdDoc)[0]["description"] = strings.Repeat("d", 6<<20)
		got := sdImport(s, sdJSONMedia, sdMarshal(t, doc), "?dry_run=true")
		if got.Status != http.StatusOK {
			t.Errorf("6 MiB JSON bundle = %d %.200s, want 200", got.Status, got.Body)
		}
	})
	for _, media := range []string{"text/plain", "multipart/form-data; boundary=x", "application/octet-stream", "application/x-www-form-urlencoded"} {
		t.Run("media type "+media, func(t *testing.T) {
			got := sdImport(s, media, valid, "?dry_run=true")
			if got.Status != http.StatusUnsupportedMediaType || got.errorCode(t) != "unsupported_media_type" {
				t.Errorf("import as %s = %d %.200s, want 415 unsupported_media_type", media, got.Status, got.Body)
			}
		})
	}
	t.Run("no content type", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.base+"/plugins/import?dry_run=true", bytes.NewReader(valid))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("import without a content type = %d, want 415", resp.StatusCode)
		}
	})
	t.Run("the 415 of the other routes is unchanged", func(t *testing.T) {
		// A text/plain body on a JSON route is judged by its handler (422), not refused as media.
		got := s.do(http.MethodPost, "/plugins/export", `{"name":"x"}`, "Content-Type", "text/plain")
		if got.Status != http.StatusUnprocessableEntity {
			t.Errorf("export as text/plain = %d %s, want 422 like every JSON route", got.Status, got.Body)
		}
		got = s.do(http.MethodPost, "/plugins/export", valid, "Content-Type", sdZipMedia)
		if got.Status != http.StatusUnsupportedMediaType {
			t.Errorf("export as application/zip = %d %s, want 415: only the import route takes a zip", got.Status, got.Body)
		}
	})
	t.Run("a bad query", func(t *testing.T) {
		if got := sdImport(s, sdJSONMedia, valid, "?dry_run=maybe"); got.Status != http.StatusBadRequest {
			t.Errorf("dry_run=maybe = %d %s, want 400", got.Status, got.Body)
		}
		for _, name := range []string{"", strings.Repeat("n", 256), "bad\x01name"} {
			got := sdImport(s, sdJSONMedia, valid, "?dry_run=true&filename="+url.QueryEscape(name))
			if got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "validation_error" {
				t.Errorf("filename %q = %d %.200s, want 422 validation_error", name, got.Status, got.Body)
			}
		}
	})
	assertCountsUnchanged(t, "the refused imports", e.db.DB, before)
}

func TestSettingsD_ForeignOriginAndHostAreRefusedOnEveryPluginRoute(t *testing.T) { // AC10, D16
	e := newSDEnv(t)
	e.seedExportable(t)
	s := e.boot(t)
	id := sdObj(t, sdInstall(t, s, sdSmallBundle("guarded"), "g.zip"), "installed")["id"].(string)
	before := sbTableCounts(t, e.db.DB)
	valid := sdMarshal(t, sdSmallBundle("other"))

	wantOrigin := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Origin not allowed"}}
	for _, req := range []struct {
		method, path, media string
		body                any
	}{
		{http.MethodPost, "/plugins/export", sdJSONMedia, sdDoc{}},
		{http.MethodPost, "/plugins/import?dry_run=true", sdZipMedia, sdZipOf(t, sdSmallBundle("other"))},
		{http.MethodPost, "/plugins/import", sdJSONMedia, valid},
		{http.MethodPost, "/plugins/import", "text/plain", valid},
		{http.MethodPatch, "/plugins/" + id, sdJSONMedia, sdDoc{"enabled": true}},
		{http.MethodDelete, "/plugins/" + id, sdJSONMedia, sdDoc{"confirm": "guarded"}},
	} {
		got := s.do(req.method, req.path, req.body, "Content-Type", req.media, "Origin", sdEvilOrg)
		if got.Status != http.StatusForbidden {
			t.Errorf("%s %s with a foreign Origin = %d %.200s, want 403", req.method, req.path, got.Status, got.Body)
			continue
		}
		if diff := cmp.Diff(wantOrigin, got.JSON(t)); diff != "" {
			t.Errorf("%s %s foreign Origin body mismatch (-want +got):\n%s", req.method, req.path, diff)
		}
	}
	assertCountsUnchanged(t, "foreign Origin", e.db.DB, before)

	wantHost := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Host not allowed"}}
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/plugins"}, {http.MethodPost, "/plugins/export"}, {http.MethodPost, "/plugins/import"},
		{http.MethodGet, "/ci/installations"},
	} {
		got := s.do(req.method, req.path, nil, "Host", "evil.example")
		if got.Status != http.StatusForbidden {
			t.Errorf("%s %s with a foreign Host = %d %.200s, want 403", req.method, req.path, got.Status, got.Body)
			continue
		}
		if diff := cmp.Diff(wantHost, got.JSON(t)); diff != "" {
			t.Errorf("%s %s foreign Host body mismatch (-want +got):\n%s", req.method, req.path, diff)
		}
	}

	// The UI's own origin works.
	ok := s.do(http.MethodPost, "/plugins/export", sdDoc{}, "Origin", "http://localhost:3100")
	if ok.Status != http.StatusOK {
		t.Errorf("export from the UI origin = %d %.200s, want 200", ok.Status, ok.Body)
	}
}

func TestSettingsD_ImportAndExportHaveTenRequestsAMinute(t *testing.T) { // AC10, AC15, NFR3
	e := newSDEnv(t)
	e.seedExportable(t)
	s := e.boot(t, func(sp *spec) { sp.rateLimit = true })
	valid := sdMarshal(t, sdSmallBundle("rate"))

	for i := 1; i <= 10; i++ {
		if got := sdImport(s, sdJSONMedia, valid, "?dry_run=true"); got.Status != http.StatusOK {
			t.Fatalf("import %d = %d %s, want 200", i, got.Status, got.Body)
		}
	}
	got := sdImport(s, sdJSONMedia, valid, "?dry_run=true")
	if got.Status != http.StatusTooManyRequests {
		t.Fatalf("import 11 = %d %s, want 429", got.Status, got.Body)
	}
	if got.Header.Get("Retry-After") == "" {
		t.Errorf("429 lacks Retry-After")
	}

	// Export has its own counter: the spent import bucket does not touch it.
	for i := 1; i <= 10; i++ {
		if got := s.post("/plugins/export", sdDoc{}); got.Status != http.StatusOK {
			t.Fatalf("export %d = %d %.200s, want 200", i, got.Status, got.Body)
		}
	}
	if got := s.post("/plugins/export", sdDoc{}); got.Status != http.StatusTooManyRequests {
		t.Fatalf("export 11 = %d %.200s, want 429", got.Status, got.Body)
	}

	// The other plugin routes are not limited by these buckets.
	for i := 0; i < 15; i++ {
		if got := s.get("/plugins"); got.Status != http.StatusOK {
			t.Fatalf("GET /plugins %d with spent buckets = %d %s, want 200", i, got.Status, got.Body)
		}
	}
}

// ---- Slice B's workspace export, imported as a plugin (AC11) ----

// seedWorkspaceExport fills a workspace with every kind of record a workspace export holds, with
// skill names an import accepts.
func (e *sdEnv) seedWorkspaceExport(t *testing.T) {
	t.Helper()
	agent := e.agent(t, "Reviewer", true, 1)
	e.agent(t, "Reviewer", false, 2)
	skill := e.skill(t, "imported-rules", "imported_file")
	other := e.skill(t, "style-rules", "manual")
	e.exec(t, `UPDATE skills SET body = 'current body', version = 3, evidence_files = '["a.go"]' WHERE id = ?`, skill)
	e.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, 0), (?, ?, 1)`, agent, skill, agent, other)
	for _, v := range []int{1, 2, 3} {
		e.exec(t, `INSERT INTO skill_versions (skill_id, version, body, message) VALUES (?, ?, ?, 'msg')`, skill, v, fmt.Sprintf("body v%d", v))
	}
	e.exec(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, input_diff) VALUES
		(?, 'agent', ?, 'agent-case', 'diff'), (?, 'skill', ?, 'skill-case', NULL)`, e.ws, agent, e.ws, skill)
	repo := e.repo(t, "org/x")
	e.exec(t, `INSERT INTO conventions (workspace_id, repo_id, rule, confidence, category, evidence, status) VALUES
		(?, ?, 'rule-one', 0.9, 'naming', '[{"path":"a.go","snippet":"s"}]', 'accepted')`, e.ws, repo)
	e.exec(t, `INSERT INTO memory (workspace_id, repo_id, scope, kind, content, confidence) VALUES
		(?, ?, 'repo', 'decision', 'repo learning', 0.7), (?, NULL, 'global', 'fact', 'global learning', NULL)`, e.ws, repo, e.ws)
	e.exec(t, `INSERT INTO context_files (repo_id, path, content, size_bytes) VALUES (?, 'docs/a.md', 'A', 1)`, repo)
	e.setting(t, "theme", `"dark"`)
	e.setting(t, "workspace_name", `"acme · payments"`)
}

func TestSettingsD_AWorkspaceExportInstallsAsAPlugin(t *testing.T) { // AC11, FR9
	source := newSDEnv(t)
	source.seedWorkspaceExport(t)
	exportStack := source.boot(t)
	export := exportStack.get("/workspace/export")
	if export.Status != http.StatusOK {
		t.Fatalf("GET /workspace/export = %d %s, want 200", export.Status, export.Body)
	}

	target := newSDEnv(t)
	s := target.boot(t)
	got := sdImport(s, sdJSONMedia, export.Body, "?dry_run=true&filename=workspace.json")
	if got.Status != http.StatusOK {
		t.Fatalf("dry-run of the workspace export = %d %s, want 200", got.Status, got.Body)
	}
	preview := got.Obj(t)
	if preview["kind"] != "workspace" || sdObj(t, preview, "manifest")["name"] != "acme · payments" {
		t.Errorf("preview kind and name = %v, %v, want workspace and the workspace name", preview["kind"], sdObj(t, preview, "manifest")["name"])
	}
	wantCounts := sdDoc{"agents": 2.0, "skills": 2.0, "eval_cases": 2.0, "conventions": 1.0, "learnings": 2.0}
	if diff := cmp.Diff(wantCounts, sdDoc(sdObj(t, preview, "counts"))); diff != "" {
		t.Errorf("preview counts mismatch (-want +got):\n%s", diff)
	}
	var ignored []string
	for _, ig := range preview["ignored"].([]any) {
		ignored = append(ignored, ig.(map[string]any)["path_or_section"].(string))
	}
	sort.Strings(ignored)
	if want := []string{"context_files", "conventions", "settings", "skills[].versions"}; !slices.Equal(ignored, want) {
		t.Errorf("preview ignored = %v, want %v", ignored, want)
	}
	if warnings := preview["warnings"].([]any); len(warnings) < 4 {
		t.Errorf("preview warnings = %v, want one per ignored section and one for the learning's repository", warnings)
	}
	if diff := cmp.Diff(sdDoc{"plugin": false, "skills": []any{}}, sdDoc(sdObj(t, preview, "conflicts"))); diff != "" {
		t.Errorf("preview conflicts mismatch (-want +got):\n%s", diff)
	}
	if list := s.get("/plugins").Obj(t)["plugins"].([]any); len(list) != 0 {
		t.Fatalf("the dry run installed %v", list)
	}

	inst := sdImport(s, sdJSONMedia, export.Body, "?filename=workspace.json")
	if inst.Status != http.StatusCreated {
		t.Fatalf("install of the workspace export = %d %s, want 201", inst.Status, inst.Body)
	}
	body := inst.Obj(t)
	installed := sdObj(t, body, "installed")
	if installed["name"] != "acme · payments" || installed["enabled"] != false || installed["source"] != "workspace.json" {
		t.Errorf("installed = %v, want the workspace name, disabled, from workspace.json", installed)
	}
	wantCreated := sdDoc{"agents": 2.0, "skills": 2.0, "eval_cases": 2.0, "conventions": 0.0, "learnings": 2.0}
	if diff := cmp.Diff(wantCreated, sdDoc(sdObj(t, body, "created"))); diff != "" {
		t.Errorf("created mismatch (-want +got):\n%s", diff)
	}

	// Current skill bodies, no old versions, learnings without a repository, no conventions.
	skills := sdByName(t, s.get("/skills"))
	if skills["imported-rules"]["body"] != "current body" {
		t.Errorf("imported-rules body = %v, want the exported current body", skills["imported-rules"]["body"])
	}
	versions := s.get("/skills/" + skills["imported-rules"]["id"].(string) + "/versions").JSON(t).([]any)
	if len(versions) != 1 {
		t.Errorf("imported-rules versions = %d, want only the install's version 1", len(versions))
	}
	if n := scalar[int](t, target.db.DB, `SELECT count(*) FROM memory WHERE repo_id IS NOT NULL`); n != 0 {
		t.Errorf("learnings with a repository after the install = %d, want 0", n)
	}
	if n := scalar[int](t, target.db.DB, `SELECT count(*) FROM conventions`); n != 0 {
		t.Errorf("conventions after the install = %d, want 0", n)
	}
	agents := s.get("/agents").JSON(t).([]any)
	if len(agents) != 2 {
		t.Fatalf("agents after the install = %d, want 2", len(agents))
	}
	var names []string
	for _, a := range agents {
		names = append(names, a.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	if want := []string{"Reviewer", "Reviewer (2)"}; !slices.Equal(names, want) {
		t.Errorf("installed agent names = %v, want %v", names, want)
	}
}

// ---- GET /ci/installations (AC1) ----

func TestSettingsD_CIInstallationsByRepo(t *testing.T) { // AC1, FR1, EC12
	e := newSDEnv(t)
	a1 := e.agent(t, "zeta-agent", true, 0)
	a2 := e.agent(t, "alpha-agent", true, 1)
	a3 := e.agent(t, "web-agent", true, 2)
	a4 := e.agent(t, "circle-agent", true, 3)
	other := scalar[string](t, e.db.DB, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id::text`)
	foreign := scalar[string](t, e.db.DB, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, 'foreign', 'openai', 'm', 'p') RETURNING id::text`, other)

	inst := func(agent, repo, target, slug string, at string) string {
		return e.id(t, `INSERT INTO ci_installations (agent_id, repo, target_type, slug, installed_at, provider)
			VALUES (?, ?, ?, NULLIF(?, ''), ?::timestamptz, 'openrouter') RETURNING id::text`, agent, repo, target, slug, at)
	}
	x1 := inst(a1, "acme/api", "gha", "zeta", "2026-10-01T10:00:00Z")
	x2 := inst(a2, "acme/api", "gha", "", "2026-10-01T11:00:00Z")
	inst(a3, "acme/web", "gha", "web", "2026-10-02T10:00:00Z")
	inst(a4, "acme/legacy", "circle", "", "2026-10-03T10:00:00Z")
	inst(a1, "Zed/lib", "gha", "", "2026-10-04T10:00:00Z")
	inst(foreign, "foreign/repo", "gha", "", "2026-10-05T10:00:00Z")
	// A CI run belongs to an agent run of the workspace.
	run := func(agent string) string {
		return e.id(t, `INSERT INTO agent_runs (workspace_id, agent_id, status, source) VALUES (?, ?, 'done', 'ci') RETURNING id::text`, e.ws, agent)
	}
	e.exec(t, `INSERT INTO ci_runs (ci_installation_id, agent_run_id, ran_at, status, github_url) VALUES
		(?, ?, '2026-10-10T08:00:00Z', 'failed', 'https://github.com/acme/api/actions/runs/1'),
		(?, ?, '2026-10-12T09:15:30.250Z', 'succeeded', 'https://github.com/acme/api/actions/runs/2'),
		(?, ?, NULL, 'running', NULL)`, x1, run(a1), x2, run(a2), x1, run(a1))
	s := e.boot(t)

	got := s.get("/ci/installations")
	if got.Status != http.StatusOK {
		t.Fatalf("GET /ci/installations = %d %s, want 200", got.Status, got.Body)
	}
	repos := got.Obj(t)["repos"].([]any)
	var order []string
	byRepo := map[string]map[string]any{}
	for _, r := range repos {
		m := r.(map[string]any)
		order = append(order, m["repo"].(string))
		byRepo[m["repo"].(string)] = m
	}
	// Byte order of the names: upper case before lower case; the circle installation and the other
	// workspace's agent are not listed.
	if want := []string{"Zed/lib", "acme/api", "acme/web"}; !slices.Equal(order, want) {
		t.Fatalf("repos = %v, want %v", order, want)
	}

	api := byRepo["acme/api"]
	var agents, slugs []any
	for _, i := range api["installations"].([]any) {
		m := i.(map[string]any)
		agents = append(agents, m["agent_name"])
		slugs = append(slugs, m["slug"])
		if m["id"] == nil || m["agent_id"] == nil || m["installed_at"] == nil {
			t.Errorf("installation %v lacks id, agent_id or installed_at", m)
		}
	}
	if want := []any{"alpha-agent", "zeta-agent"}; !cmp.Equal(agents, want) {
		t.Errorf("acme/api installations = %v, want %v in agent name order", agents, want)
	}
	if want := []any{nil, "zeta"}; !cmp.Equal(slugs, want) {
		t.Errorf("acme/api slugs = %v, want %v", slugs, want)
	}
	wantLast := sdDoc{"status": "succeeded", "ran_at": "2026-10-12T09:15:30.250Z", "github_url": "https://github.com/acme/api/actions/runs/2", "agent_id": a2}
	if diff := cmp.Diff(any(wantLast), api["last_run"]); diff != "" {
		t.Errorf("acme/api last_run mismatch (-want +got):\n%s", diff)
	}
	if byRepo["acme/web"]["last_run"] != nil || byRepo["Zed/lib"]["last_run"] != nil {
		t.Errorf("last_run of repositories without runs = %v and %v, want null", byRepo["acme/web"]["last_run"], byRepo["Zed/lib"]["last_run"])
	}

	// An empty workspace lists no repositories.
	empty := newSDEnv(t)
	if got := empty.boot(t).get("/ci/installations"); got.Status != http.StatusOK || string(bytes.TrimSpace(got.Body)) != `{"repos":[]}` {
		t.Errorf("GET /ci/installations of an empty workspace = %d %s, want 200 {\"repos\":[]}", got.Status, got.Body)
	}
}

// ---- drift of the limits the plugins domain copies (P3) ----

// sdCheckDef returns the definition of a CHECK constraint.
func sdCheckDef(t testing.TB, db *pgtest.Database, table, constraint string) string {
	t.Helper()
	return scalar[string](t, db.DB, `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
		JOIN pg_class r ON r.oid = c.conrelid WHERE r.relname = ? AND c.conname = ?`, table, constraint)
}

// sdCheckValues returns the literal values of a CHECK constraint `col = ANY (ARRAY['a'::text, …])`.
// A regular expression of the constraint (`col ~ '^…$'::text`) is not a value: sdCheckPatterns
// returns it.
func sdCheckValues(t testing.TB, db *pgtest.Database, table, constraint string) []string {
	t.Helper()
	def := sdCheckDef(t, db, table, constraint)
	var out []string
	for _, m := range regexp.MustCompile(`'([^']+)'::text`).FindAllStringSubmatch(def, -1) {
		if strings.HasPrefix(m[1], "^") {
			continue
		}
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("constraint %s.%s = %q holds no values", table, constraint, def)
	}
	return out
}

// sdCheckPatterns returns the regular expressions of a CHECK constraint (`col ~ '^…$'::text`).
func sdCheckPatterns(t testing.TB, db *pgtest.Database, table, constraint string) []string {
	t.Helper()
	def := sdCheckDef(t, db, table, constraint)
	var out []string
	for _, m := range regexp.MustCompile(`~ '([^']+)'::text`).FindAllStringSubmatch(def, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestSettingsD_PluginDomainLimitsMatchTheirOrigins(t *testing.T) { // P3
	db := pgtest.Migrated(t)

	t.Run("limits", func(t *testing.T) {
		for _, c := range []struct {
			name      string
			got, want int
		}{
			{"SkillNameMax", pluginsdomain.SkillNameMax, skillsdomain.SkillNameMax},
			{"SkillDescriptionMax", pluginsdomain.SkillDescriptionMax, skillsdomain.SkillDescriptionMax},
			{"SkillBodyMax", pluginsdomain.SkillBodyMax, skillsdomain.SkillBodyMax},
			{"CaseNameMax", pluginsdomain.CaseNameMax, evalsdomain.CaseNameMax},
			{"CasesPerOwnerMax", pluginsdomain.CasesPerOwnerMax, evalsdomain.CasesPerOwnerMax},
			{"CaseDiffMax", pluginsdomain.CaseDiffMax, evalsdomain.DiffMaxChars},
		} {
			if c.got != c.want {
				t.Errorf("plugins domain %s = %d, its origin has %d", c.name, c.got, c.want)
			}
		}
		// The skill name limit is also a CHECK of the table.
		def := scalar[string](t, db.DB, `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c WHERE c.conname = 'skills_name_len_chk'`)
		if !strings.Contains(def, fmt.Sprintf("<= %d)", pluginsdomain.SkillNameMax)) {
			t.Errorf("skills_name_len_chk = %q, want the limit %d", def, pluginsdomain.SkillNameMax)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		if pluginsdomain.DefaultStrategy != agentsdomain.DefaultStrategy {
			t.Errorf("plugins DefaultStrategy = %q, agents has %q", pluginsdomain.DefaultStrategy, agentsdomain.DefaultStrategy)
		}
		if pluginsdomain.DefaultCIFailOn != agentsdomain.DefaultCIFailOn {
			t.Errorf("plugins DefaultCIFailOn = %q, agents has %q", pluginsdomain.DefaultCIFailOn, agentsdomain.DefaultCIFailOn)
		}
	})

	// Custom provider ids are a pattern, not a list (spec FR15): the built-in list above is compared
	// exactly, and the CHECK of agents.provider must additionally carry the custom pattern, which is
	// llm/custom's id rule that the bundle validation copies.
	t.Run("AgentProviders custom pattern", func(t *testing.T) {
		const wantPattern = `^custom:[a-z0-9-]{1,32}$`
		if diff := cmp.Diff([]string{wantPattern}, sdCheckPatterns(t, db, "agents", "agents_provider_chk")); diff != "" {
			t.Errorf("agents_provider_chk patterns mismatch (-want +got):\n%s", diff)
		}
		pattern := regexp.MustCompile(wantPattern)
		for _, id := range []string{
			"custom:a", "custom:synapse", "custom:a-b-9", "custom:" + strings.Repeat("a", customllm.MaxSlugLen),
			"custom:" + strings.Repeat("a", customllm.MaxSlugLen+1), "custom:", "custom:Bad!", "custom:a_b", "Custom:a",
			"custom:a\n", " custom:a", "openai", "",
		} {
			_, parsed := customllm.ParseID(id)
			if got := pluginsdomain.IsCustomProvider(id); got != parsed || got != pattern.MatchString(id) {
				t.Errorf("custom id %q: plugins IsCustomProvider = %v, llm/custom ParseID = %v, CHECK pattern = %v; want all equal",
					id, got, parsed, pattern.MatchString(id))
			}
		}
		for _, name := range pluginsdomain.AgentProviders() {
			if pluginsdomain.IsCustomProvider(name) {
				t.Errorf("built-in provider %q is a custom id", name)
			}
		}
	})

	// The enums: against the origin package where it exports the list, and always against the
	// CHECK constraint of the column the value is stored in. The order is the contract's order.
	skillSources := []string{
		string(skillsdomain.SourceManual), string(skillsdomain.SourceImportedFile), string(skillsdomain.SourceImportedURL),
		string(skillsdomain.SourceExtracted), string(skillsdomain.SourceCommunity),
	}
	ownerKinds := []string{string(evalsdomain.OwnerSkill), string(evalsdomain.OwnerAgent)}
	for _, c := range []struct {
		name   string
		got    []string
		origin []string // nil: the origin package exports no list
		table  string
		check  string
	}{
		{"AgentProviders", pluginsdomain.AgentProviders(), agentsdomain.Providers(), "agents", "agents_provider_chk"},
		{"AgentStrategies", pluginsdomain.AgentStrategies(), agentsdomain.Strategies(), "agents", "agents_strategy_chk"},
		{"AgentCIFailOns", pluginsdomain.AgentCIFailOns(), agentsdomain.CIFailOns(), "agents", "agents_ci_fail_on_chk"},
		{"SkillTypes", pluginsdomain.SkillTypes(), skillsdomain.SkillTypeNames(), "skills", "skills_type_chk"},
		{"SkillSources", pluginsdomain.SkillSources(), skillSources, "skills", "skills_source_chk"},
		{"CaseOwnerKinds", pluginsdomain.CaseOwnerKinds(), ownerKinds, "eval_cases", "eval_cases_owner_kind_chk"},
		{"LearningScopes", pluginsdomain.LearningScopes(), nil, "memory", "memory_scope_chk"},
		{"LearningKinds", pluginsdomain.LearningKinds(), nil, "memory", "memory_kind_chk"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.origin != nil {
				if diff := cmp.Diff(c.origin, c.got); diff != "" {
					t.Errorf("%s differs from the origin package (-origin +plugins):\n%s", c.name, diff)
				}
			}
			stored := sdCheckValues(t, db, c.table, c.check)
			if diff := cmp.Diff(slices.Sorted(slices.Values(stored)), slices.Sorted(slices.Values(c.got))); diff != "" {
				t.Errorf("%s differs from %s (-%s +plugins):\n%s", c.name, c.check, c.check, diff)
			}
		})
	}
}

// ---- the contract goldens (NFR9) ----

// TestContracts_SettingsD records the answers the UI contract test validates. The ids and times are
// normalised by the goldens helper; the clock is fixed for the export.
func TestContracts_SettingsD(t *testing.T) {
	e := newSDEnv(t)
	f := e.seedExportable(t)
	s := e.boot(t)
	g := sbGoldens(t, s)

	// A preview with a plugin-name-free conflict, an ignored archive entry and a warning.
	conflicting := sdSmallBundle("preview-pack")
	conflicting["skills"].([]sdDoc)[0]["name"] = "style-guide"
	conflicting["agents"].([]sdDoc)[0]["skills"] = []string{"style-guide"}
	archive := sdZip(t, sdEntry{"devdigest-plugin.json", sdMarshal(t, conflicting)}, sdEntry{"README.md", []byte("readme")})
	g.check("plugins_import_preview", sdImport(s, sdZipMedia, archive, "?dry_run=true&filename=preview-pack.zip"), http.StatusOK)

	// An export first, so that the list carries a last export.
	if got := s.post("/plugins/export", sdDoc{"agent_ids": []string{f.agentA}, "include_learnings": true}); got.Status != http.StatusOK {
		t.Fatalf("POST /plugins/export = %d %s, want 200", got.Status, got.Body)
	}
	g.check("plugins_import_installed", sdImport(s, sdZipMedia, sdZipOf(t, sdPackDoc("gold-")), "?filename=tools.zip"), http.StatusCreated)
	id := scalar[string](t, e.db.DB, `SELECT id::text FROM installed_plugins WHERE name = 'tools-pack'`)
	planner := scalar[string](t, e.db.DB, `SELECT id::text FROM agents WHERE name = 'planner'`)

	// The plugin installs disabled: naming its agent in a review is refused.
	repo := e.repo(t, "acme/golden")
	pull := e.pull(t, repo, 7, nil)
	g.check("plugin_disabled", s.post("/pulls/"+pull+"/review", sdDoc{"agentId": planner}), http.StatusConflict)

	g.check("plugins_list", s.get("/plugins"), http.StatusOK)
	// The owner shows on the agent and skill answers (FR14).
	skill := scalar[string](t, e.db.DB, `SELECT id::text FROM skills WHERE name = 'gold-style-guide'`)
	g.check("agent_get_plugin", s.get("/agents/"+planner), http.StatusOK)
	g.check("skill_get_plugin", s.get("/skills/"+skill), http.StatusOK)
	g.check("plugins_patch", s.do(http.MethodPatch, "/plugins/"+id, sdDoc{"enabled": true}), http.StatusOK)
	g.check("plugins_delete", s.do(http.MethodDelete, "/plugins/"+id, sdDoc{"confirm": "tools-pack"}), http.StatusOK)

	for _, agent := range []string{f.agentA, f.agentB} {
		repoName := map[string]string{f.agentA: "acme/api", f.agentB: "acme/web"}[agent]
		inst := e.id(t, `INSERT INTO ci_installations (agent_id, repo, target_type, slug, provider) VALUES (?, ?, 'gha', 'slug', 'openrouter') RETURNING id::text`, agent, repoName)
		if agent == f.agentA {
			run := e.id(t, `INSERT INTO agent_runs (workspace_id, agent_id, status, source) VALUES (?, ?, 'done', 'ci') RETURNING id::text`, e.ws, agent)
			e.exec(t, `INSERT INTO ci_runs (ci_installation_id, agent_run_id, ran_at, status, github_url) VALUES (?, ?, '2026-10-12T09:15:30Z', 'succeeded', 'https://github.com/acme/api/actions/runs/2')`, inst, run)
		}
	}
	g.check("ci_installations_by_repo", s.get("/ci/installations"), http.StatusOK)
}
