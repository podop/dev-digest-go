package domain

import (
	"encoding/json"
	"time"
)

// BundleFormat is the envelope format shared with the plugin bundle.
const BundleFormat = "devdigest-plugin/v1"

// BundleKind marks the bundle as a workspace export (the plugin bundle has no kind).
const BundleKind = "workspace"

// DefaultVersion is the manifest version when the server build has none.
const DefaultVersion = "dev"

// TimeLayout is the wire timestamp layout: UTC with milliseconds.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Meta is what BuildBundle needs besides the data: the workspace name, the server build version
// ("dev" when empty) and the export instant.
type Meta struct {
	Name    string
	Version string
	Now     time.Time
}

// Bundle is the exported workspace document (spec section 7): the plugin envelope with
// kind "workspace". It holds no id, path, secret, embedding or run-history record.
type Bundle struct {
	Manifest     Manifest                   `json:"manifest"`
	Agents       []BundleAgent              `json:"agents"`
	Skills       []BundleSkill              `json:"skills"`
	EvalCases    []BundleCase               `json:"eval_cases"`
	Conventions  []BundleRule               `json:"conventions"`
	Learnings    []BundleMemory             `json:"learnings"`
	ContextFiles []BundleContext            `json:"context_files"`
	Settings     map[string]json.RawMessage `json:"settings"`
}

// Manifest describes the bundle.
type Manifest struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Format      string         `json:"format"`
	Kind        string         `json:"kind"`
	ExportedAt  string         `json:"exported_at"`
	Description *string        `json:"description"`
	Counts      ManifestCounts `json:"counts"`
}

// ManifestCounts are the sizes of the bundle's lists.
type ManifestCounts struct {
	Agents       int `json:"agents"`
	Skills       int `json:"skills"`
	EvalCases    int `json:"eval_cases"`
	Conventions  int `json:"conventions"`
	Learnings    int `json:"learnings"`
	ContextFiles int `json:"context_files"`
}

// BundleAgent is an exported agent; Name is unique within the bundle and Skills name bundle skills.
type BundleAgent struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Enabled      bool            `json:"enabled"`
	Skills       []string        `json:"skills"`
	Strategy     string          `json:"strategy"`
	CIFailOn     string          `json:"ci_fail_on"`
	RepoIntel    bool            `json:"repo_intel"`
}

// BundleSkill is an exported skill with all its versions.
type BundleSkill struct {
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	Type          string               `json:"type"`
	Source        string               `json:"source"`
	Body          string               `json:"body"`
	Enabled       bool                 `json:"enabled"`
	EvidenceFiles json.RawMessage      `json:"evidence_files"`
	Versions      []BundleSkillVersion `json:"versions"`
}

// BundleSkillVersion is one exported skill version.
type BundleSkillVersion struct {
	Version     int     `json:"version"`
	Body        string  `json:"body"`
	Description *string `json:"description"`
	Message     *string `json:"message"`
	CreatedAt   string  `json:"created_at"`
}

// BundleCase is an exported eval case; OwnerRef is the owning agent's bundle name or skill name.
type BundleCase struct {
	Name           string          `json:"name"`
	OwnerKind      string          `json:"owner_kind"`
	OwnerRef       string          `json:"owner_ref"`
	InputDiff      *string         `json:"input_diff"`
	InputFiles     json.RawMessage `json:"input_files"`
	InputMeta      json.RawMessage `json:"input_meta"`
	ExpectedOutput json.RawMessage `json:"expected_output"`
	Notes          *string         `json:"notes"`
}

// BundleRule is an exported convention.
type BundleRule struct {
	Rule            string   `json:"rule"`
	EvidencePath    *string  `json:"evidence_path"`
	EvidenceSnippet *string  `json:"evidence_snippet"`
	Confidence      *float64 `json:"confidence"`
	Accepted        bool     `json:"accepted"`
	Repo            string   `json:"repo"`
	Category        string   `json:"category"`
	Status          string   `json:"status"`
}

// BundleMemory is an exported learning.
type BundleMemory struct {
	Scope      string   `json:"scope"`
	Kind       string   `json:"kind"`
	Content    string   `json:"content"`
	Confidence *float64 `json:"confidence"`
	Repo       *string  `json:"repo"`
}

// BundleContext is an exported project-context file.
type BundleContext struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ConventionAccepted is the convention status that the bundle's accepted flag stands for.
const ConventionAccepted = "accepted"

// BuildBundle turns the stored records into the export document. It is pure: lists keep the
// input order, all lists are non-nil, agent names are made unique (UniqueNames) and eval cases
// refer to them by that name, an eval case whose owner is not in the data is skipped (A6), and
// settings pass through ExportSettings. The manifest counts are the lengths of the output lists.
func BuildBundle(data ExportData, meta Meta) Bundle {
	agentNames := make([]string, len(data.Agents))
	for i, a := range data.Agents {
		agentNames[i] = a.Name
	}
	agentNames = UniqueNames(agentNames)
	agentRef := make(map[string]string, len(data.Agents))
	agents := make([]BundleAgent, len(data.Agents))
	for i, a := range data.Agents {
		agentRef[a.ID] = agentNames[i]
		agents[i] = BundleAgent{
			Name: agentNames[i], Description: a.Description, Provider: a.Provider, Model: a.Model,
			SystemPrompt: a.SystemPrompt, OutputSchema: a.OutputSchema, Enabled: a.Enabled,
			Skills:   nonNilStrings(a.SkillNames),
			Strategy: a.Strategy, CIFailOn: a.CIFailOn, RepoIntel: a.RepoIntel,
		}
	}

	skillRef := make(map[string]string, len(data.Skills))
	skills := make([]BundleSkill, len(data.Skills))
	for i, s := range data.Skills {
		skillRef[s.ID] = s.Name
		versions := make([]BundleSkillVersion, len(s.Versions))
		for j, v := range s.Versions {
			versions[j] = BundleSkillVersion{
				Version: v.Version, Body: v.Body, Description: v.Description, Message: v.Message,
				CreatedAt: v.CreatedAt.UTC().Format(TimeLayout),
			}
		}
		skills[i] = BundleSkill{
			Name: s.Name, Description: s.Description, Type: s.Type, Source: s.Source, Body: s.Body,
			Enabled: s.Enabled, EvidenceFiles: s.EvidenceFiles, Versions: versions,
		}
	}

	cases := make([]BundleCase, 0, len(data.EvalCases))
	for _, c := range data.EvalCases {
		refs := skillRef
		if c.OwnerKind == OwnerAgent {
			refs = agentRef
		}
		ref, ok := refs[c.OwnerID]
		if !ok {
			continue
		}
		cases = append(cases, BundleCase{
			Name: c.Name, OwnerKind: c.OwnerKind, OwnerRef: ref, InputDiff: c.InputDiff,
			InputFiles: c.InputFiles, InputMeta: c.InputMeta, ExpectedOutput: c.ExpectedOutput, Notes: c.Notes,
		})
	}

	rules := make([]BundleRule, len(data.Conventions))
	for i, c := range data.Conventions {
		rules[i] = BundleRule{
			Rule: c.Rule, EvidencePath: c.EvidencePath, EvidenceSnippet: c.EvidenceSnippet,
			Confidence: c.Confidence, Accepted: c.Status == ConventionAccepted, Repo: c.Repo,
			Category: c.Category, Status: c.Status,
		}
	}

	learnings := make([]BundleMemory, len(data.Learnings))
	for i, l := range data.Learnings {
		learnings[i] = BundleMemory{Scope: l.Scope, Kind: l.Kind, Content: l.Content, Confidence: l.Confidence, Repo: l.Repo}
	}

	files := make([]BundleContext, len(data.ContextFiles))
	for i, f := range data.ContextFiles {
		files[i] = BundleContext{Repo: f.Repo, Path: f.Path, Content: f.Content}
	}

	version := meta.Version
	if version == "" {
		version = DefaultVersion
	}
	return Bundle{
		Manifest: Manifest{
			Name: meta.Name, Version: version, Format: BundleFormat, Kind: BundleKind,
			ExportedAt: meta.Now.UTC().Format(TimeLayout),
			Counts: ManifestCounts{
				Agents: len(agents), Skills: len(skills), EvalCases: len(cases), Conventions: len(rules),
				Learnings: len(learnings), ContextFiles: len(files),
			},
		},
		Agents: agents, Skills: skills, EvalCases: cases, Conventions: rules,
		Learnings: learnings, ContextFiles: files,
		Settings: ExportSettings(data.Settings),
	}
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
