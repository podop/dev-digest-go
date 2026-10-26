package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// CountsOf counts the lists of b.
func CountsOf(b Bundle) ManifestCounts {
	return ManifestCounts{
		Agents: len(b.Agents), Skills: len(b.Skills), EvalCases: len(b.EvalCases),
		Conventions: len(b.Conventions), Learnings: len(b.Learnings), ContextFiles: len(b.ContextFiles),
	}
}

// PreviewDoc is the dry-run answer (spec section 7): what installing the upload would create.
// Manifest has the trimmed name, the kind (KindPlugin when absent) and the counted lists.
type PreviewDoc struct {
	Manifest  Manifest
	Kind      string
	Counts    ManifestCounts
	Agents    []PreviewAgent
	Skills    []PreviewSkill
	Conflicts Conflicts
	Ignored   []Ignored
	Warnings  []string
}

// PreviewAgent is an agent as the preview lists it; Skills are its skill names in link order.
type PreviewAgent struct {
	Name     string
	Provider string
	Model    string
	Enabled  bool
	Skills   []string
}

// PreviewSkill is a skill as the preview lists it.
type PreviewSkill struct {
	Name        string
	Type        string
	Description string
	Enabled     bool
}

// Preview describes a validated upload. found says what already exists in the workspace; the
// result's skill conflicts keep the bundle's skill order. Ignored lists the upload's ignored
// parts plus the sections a plugin never installs (conventions, skill versions, context files,
// settings) when the bundle holds them, each of which also gets a warning.
func Preview(up Upload, found Conflicts) PreviewDoc {
	return preview(up, found, sanitizeTexts(up.Bundle))
}

func preview(up Upload, found Conflicts, texts cleanTexts) PreviewDoc {
	b := up.Bundle
	name, _ := NormalizeName(b.Manifest.Name)
	kind := b.Manifest.Kind
	if kind == "" {
		kind = KindPlugin
	}
	manifest := b.Manifest
	manifest.Name = name
	manifest.Kind = kind
	manifest.Counts = CountsOf(b)

	doc := PreviewDoc{
		Manifest: manifest, Kind: kind, Counts: manifest.Counts,
		Agents:    make([]PreviewAgent, len(b.Agents)),
		Skills:    make([]PreviewSkill, len(b.Skills)),
		Conflicts: Conflicts{Plugin: found.Plugin, Skills: []string{}},
		Ignored:   slices.Clone(up.Ignored),
		Warnings:  []string{},
	}
	if doc.Ignored == nil {
		doc.Ignored = []Ignored{}
	}
	for i, a := range b.Agents {
		doc.Agents[i] = PreviewAgent{
			Name: a.Name, Provider: a.Provider, Model: a.Model, Enabled: a.Enabled,
			Skills: append([]string{}, a.Skills...),
		}
	}
	for i, s := range b.Skills {
		doc.Skills[i] = PreviewSkill{Name: s.Name, Type: s.Type, Description: texts.skillDescriptions[i], Enabled: s.Enabled}
		if slices.Contains(found.Skills, s.Name) {
			doc.Conflicts.Skills = append(doc.Conflicts.Skills, s.Name)
		}
	}

	unknown := 0
	for _, ig := range up.Ignored {
		if ig.Reason == ReasonUnknownKey {
			unknown++
		}
	}
	if unknown > 0 {
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("%d unknown key(s) were ignored", unknown))
	}
	skip := func(section string, n int, noun string) {
		if n == 0 {
			return
		}
		doc.Ignored = append(doc.Ignored, Ignored{PathOrSection: section, Reason: ReasonNeverInstalled})
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("%d %s ignored: they are never installed", n, noun))
	}
	skip("conventions", len(b.Conventions), "convention(s) are")
	versions := 0
	for _, s := range b.Skills {
		versions += len(s.Versions)
	}
	skip("skills[].versions", versions, "skill version(s) are")
	skip("context_files", len(b.ContextFiles), "context file(s) are")
	skip("settings", len(b.Settings), "setting(s) are")

	withRepo := 0
	for _, l := range b.Learnings {
		if l.Repo != nil {
			withRepo++
		}
	}
	if withRepo > 0 {
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("%d learning(s) name a repository; they are installed without one", withRepo))
	}
	doc.Warnings = append(doc.Warnings, texts.warnings...)
	return doc
}

// Installed skills take this source and the provenance "plugin:<name>" (A4).
const InstalledSkillSource = "imported_file"

// SkillSourceRef is the provenance of a skill installed by plugin name (A4).
func SkillSourceRef(plugin string) string { return "plugin:" + plugin }

// SkillVersionNote is the message of version 1 of a skill installed by plugin name (A4).
func SkillVersionNote(plugin string) string { return "Installed from plugin " + plugin }

// Install is the plan of one install: everything the store creates in one transaction, in
// creation order, all of it owned by the new plugin. It is built from a validated upload.
type Install struct {
	Name        string
	Version     string
	Description *string
	// Source is the upload's file name; empty when the request gave none.
	Source    string
	Agents    []InstallAgent
	Skills    []InstallSkill
	EvalCases []InstallCase
	Learnings []InstallLearning
}

// InstallAgent is an agent to create. Skills are bundle skill names to link, in order. OutputSchema
// nil is NULL.
type InstallAgent struct {
	Name         string
	Description  string
	Provider     string
	Model        string
	SystemPrompt string
	OutputSchema json.RawMessage
	Enabled      bool
	Strategy     string
	CIFailOn     string
	RepoIntel    bool
	Skills       []string
}

// InstallSkill is a skill to create at version 1 (message VersionNote). Enabled is the bundle's
// flag: the plugin, not the skill, is the trust gate (D6). EvidenceFiles nil is NULL.
type InstallSkill struct {
	Name          string
	Description   string
	Type          string
	Source        string
	SourceRef     string
	Body          string
	Enabled       bool
	EvidenceFiles []string
	VersionNote   string
}

// InstallCase is an eval case to create, owned by the agent or skill OwnerRef names. The JSON
// fields are nil for NULL.
type InstallCase struct {
	Name           string
	OwnerKind      string
	OwnerRef       string
	InputDiff      *string
	InputFiles     json.RawMessage
	InputMeta      json.RawMessage
	ExpectedOutput json.RawMessage
	Notes          *string
}

// InstallLearning is a learning to create without a repository (D13).
type InstallLearning struct {
	Scope      string
	Kind       string
	Content    string
	Confidence *float64
}

// PlanInstall builds the install plan of a bundle that passed Validate. source is the upload's
// file name ("" when none). The plugin name is the trimmed manifest name. Skill bodies and agent
// system prompts are stored without hidden text (SanitizeHiddenText); Preview warns about it.
func PlanInstall(b Bundle, source string) Install { return planInstall(b, source, sanitizeTexts(b)) }

func planInstall(b Bundle, source string, texts cleanTexts) Install {
	name, _ := NormalizeName(b.Manifest.Name)
	in := Install{
		Name: name, Version: b.Manifest.Version, Description: b.Manifest.Description, Source: source,
		Agents:    make([]InstallAgent, len(b.Agents)),
		Skills:    make([]InstallSkill, len(b.Skills)),
		EvalCases: make([]InstallCase, len(b.EvalCases)),
		Learnings: make([]InstallLearning, len(b.Learnings)),
	}
	for i, a := range b.Agents {
		in.Agents[i] = InstallAgent{
			Name: a.Name, Description: a.Description, Provider: a.Provider, Model: a.Model,
			SystemPrompt: texts.agentPrompts[i], OutputSchema: a.OutputSchema, Enabled: a.Enabled,
			Strategy: a.Strategy, CIFailOn: a.CIFailOn, RepoIntel: a.RepoIntel, Skills: slices.Clone(a.Skills),
		}
	}
	for i, s := range b.Skills {
		in.Skills[i] = InstallSkill{
			Name: s.Name, Description: texts.skillDescriptions[i], Type: s.Type, Source: InstalledSkillSource,
			SourceRef: SkillSourceRef(name), Body: texts.skillBodies[i], Enabled: s.Enabled,
			EvidenceFiles: slices.Clone(s.EvidenceFiles), VersionNote: SkillVersionNote(name),
		}
	}
	for i, c := range b.EvalCases {
		in.EvalCases[i] = InstallCase{
			Name: c.Name, OwnerKind: c.OwnerKind, OwnerRef: c.OwnerRef, InputDiff: c.InputDiff,
			InputFiles: c.InputFiles, InputMeta: c.InputMeta, ExpectedOutput: c.ExpectedOutput, Notes: c.Notes,
		}
	}
	for i, l := range b.Learnings {
		in.Learnings[i] = InstallLearning{Scope: l.Scope, Kind: l.Kind, Content: l.Content, Confidence: l.Confidence}
	}
	return in
}

// Prepared is an upload that passed Validate, with its install plan, for one request: the hidden
// text of the bundle is removed once and shared by the validation, the plan and the preview
// (security review r2, N1).
type Prepared struct {
	Upload Upload
	// Plan is what an install stores.
	Plan  Install
	texts cleanTexts
}

// Prepare validates the upload's bundle and plans its install (see PlanInstall for source). It
// returns the error of Validate, nil or an *InvalidError.
func Prepare(up Upload, source string) (Prepared, error) {
	texts := sanitizeTexts(up.Bundle)
	if err := validate(up.Bundle, texts); err != nil {
		return Prepared{}, err
	}
	return Prepared{Upload: up, Plan: planInstall(up.Bundle, source, texts), texts: texts}, nil
}

// Preview describes the prepared upload like the function Preview does.
func (p Prepared) Preview(found Conflicts) PreviewDoc { return preview(p.Upload, found, p.texts) }

// SkillNames lists the plan's skill names in bundle order, for the conflict check.
func (in Install) SkillNames() []string {
	names := make([]string, len(in.Skills))
	for i, s := range in.Skills {
		names[i] = s.Name
	}
	return names
}

// Created are the counts an install reports (Conventions is always 0: they are never installed).
type Created struct {
	Agents      int
	Skills      int
	EvalCases   int
	Conventions int
	Learnings   int
}

// Created is what the plan creates.
func (in Install) Created() Created {
	return Created{Agents: len(in.Agents), Skills: len(in.Skills), EvalCases: len(in.EvalCases), Learnings: len(in.Learnings)}
}

// PluginCounts are what an installed plugin owns.
type PluginCounts struct {
	Agents    int
	Skills    int
	EvalCases int
	Learnings int
}

// Plugin is an installed plugin as GET /plugins lists it. Version, Description and Source are nil
// for NULL columns.
type Plugin struct {
	ID          string
	Name        string
	Version     *string
	Description *string
	Source      *string
	InstalledAt time.Time
	Enabled     bool
	Counts      PluginCounts
}

// Installed is the answer of a successful install: the new (disabled) plugin and what was created.
type Installed struct {
	Plugin  Plugin
	Created Created
}

// Removed are the counts a removal deleted. CIInstallations are the CI installations of the
// deleted agents.
type Removed struct {
	Agents          int
	Skills          int
	EvalCases       int
	Learnings       int
	CIInstallations int
}
