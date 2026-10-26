package domain

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// ExportData is what the store reads for an export: the selected records, each list in creation
// order. IDs only link records inside the bundle (an eval case's owner); BuildBundle never
// writes them out. JSON columns are raw JSON, nil for SQL NULL.
type ExportData struct {
	Agents    []AgentRecord
	Skills    []SkillRecord
	EvalCases []EvalCaseRecord
	Learnings []LearningRecord
}

// AgentRecord is one agent. SkillNames are the linked skills' names in link order.
type AgentRecord struct {
	ID           string
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
	SkillNames   []string
}

// SkillRecord is one skill with its current body. Whether a plugin owns it is not exported.
type SkillRecord struct {
	ID            string
	Name          string
	Description   string
	Type          string
	Source        string
	Body          string
	Enabled       bool
	EvidenceFiles []string
}

// EvalCaseRecord is one eval case of an agent or skill, by owner id.
type EvalCaseRecord struct {
	Name           string
	OwnerKind      string
	OwnerID        string
	InputDiff      *string
	InputFiles     json.RawMessage
	InputMeta      json.RawMessage
	ExpectedOutput json.RawMessage
	Notes          *string
}

// LearningRecord is one learning; Repo is the repository's full name or nil.
type LearningRecord struct {
	Scope      string
	Kind       string
	Content    string
	Confidence *float64
	Repo       *string
}

// Meta is what BuildBundle needs besides the data: the plugin name and description, the server
// build version ("dev" when empty) and the export instant.
type Meta struct {
	Name        string
	Description *string
	Version     string
	Now         time.Time
}

// BuildBundle turns the stored records into the plugin document. It is pure: lists keep the input
// order and are never nil; agent names are made unique (UniqueNames, FR4) and eval cases refer to
// them by that name; an agent links only skills that are in the data (so the bundle always
// re-imports); an eval case whose owner is not in the data is skipped; conventions are []; there
// are no versions, context files or settings. The manifest counts are the list lengths and its
// kind is "plugin".
func BuildBundle(data ExportData, meta Meta) Bundle {
	skillRef := make(map[string]string, len(data.Skills))
	skills := make([]BundleSkill, len(data.Skills))
	for i, s := range data.Skills {
		skillRef[s.ID] = s.Name
		skills[i] = BundleSkill{
			Name: s.Name, Description: s.Description, Type: s.Type, Source: s.Source, Body: s.Body,
			Enabled: s.Enabled, EvidenceFiles: s.EvidenceFiles,
		}
	}
	inBundle := make(map[string]struct{}, len(skills))
	for _, s := range skills {
		inBundle[s.Name] = struct{}{}
	}

	names := make([]string, len(data.Agents))
	for i, a := range data.Agents {
		names[i] = a.Name
	}
	names = UniqueNames(names)
	agentRef := make(map[string]string, len(data.Agents))
	agents := make([]BundleAgent, len(data.Agents))
	for i, a := range data.Agents {
		agentRef[a.ID] = names[i]
		linked := make([]string, 0, len(a.SkillNames))
		for _, n := range a.SkillNames {
			if _, ok := inBundle[n]; ok && !slices.Contains(linked, n) {
				linked = append(linked, n)
			}
		}
		agents[i] = BundleAgent{
			Name: names[i], Description: a.Description, Provider: a.Provider, Model: a.Model,
			SystemPrompt: a.SystemPrompt, OutputSchema: nullToNil(a.OutputSchema), Enabled: a.Enabled,
			Skills: linked, Strategy: a.Strategy, CIFailOn: a.CIFailOn, RepoIntel: a.RepoIntel,
		}
	}

	cases := make([]BundleCase, 0, len(data.EvalCases))
	for _, c := range data.EvalCases {
		refs := skillRef
		if c.OwnerKind == CaseOwnerAgent {
			refs = agentRef
		}
		ref, ok := refs[c.OwnerID]
		if !ok {
			continue
		}
		cases = append(cases, BundleCase{
			Name: c.Name, OwnerKind: c.OwnerKind, OwnerRef: ref, InputDiff: c.InputDiff,
			InputFiles: nullToNil(c.InputFiles), InputMeta: nullToNil(c.InputMeta),
			ExpectedOutput: nullToNil(c.ExpectedOutput), Notes: c.Notes,
		})
	}

	learnings := make([]BundleLearning, len(data.Learnings))
	for i, l := range data.Learnings {
		learnings[i] = BundleLearning{Scope: l.Scope, Kind: l.Kind, Content: l.Content, Confidence: l.Confidence, Repo: l.Repo}
	}

	version := meta.Version
	if version == "" {
		version = DefaultVersion
	}
	b := Bundle{
		Manifest: Manifest{
			Name: meta.Name, Version: version, Format: BundleFormat, Kind: KindPlugin,
			ExportedAt: meta.Now.UTC().Format(TimeLayout), Description: meta.Description,
		},
		Agents: agents, Skills: skills, EvalCases: cases,
		Conventions: []json.RawMessage{}, Learnings: learnings,
	}
	b.Manifest.Counts = CountsOf(b)
	return b
}

// EncodeArchive writes the bundle as a .zip with the one entry devdigest-plugin.json (Deflate,
// compact JSON). A bundle whose JSON is over MaxInflatedBytes is ErrExportTooLarge, so that every
// export re-imports within the import's own cap. The bytes are deterministic for a given bundle.
func EncodeArchive(b Bundle) ([]byte, error) {
	doc, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	if len(doc) > MaxInflatedBytes {
		return nil, ErrExportTooLarge
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: BundleFile, Method: zip.Deflate}
	if at, perr := time.Parse(TimeLayout, b.Manifest.ExportedAt); perr == nil {
		hdr.Modified = at
	}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return nil, fmt.Errorf("create archive entry: %w", err)
	}
	if _, err := w.Write(doc); err != nil {
		return nil, fmt.Errorf("write archive entry: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close archive: %w", err)
	}
	return buf.Bytes(), nil
}

// LastExport is the record of the last plugin export: the Settings row LastExportKey (FR5).
// ExportedAt is a TimeLayout timestamp.
type LastExport struct {
	FileName   string           `json:"file_name"`
	ExportedAt string           `json:"exported_at"`
	Counts     LastExportCounts `json:"counts"`
}

// LastExportCounts are what the export held.
type LastExportCounts struct {
	Agents    int `json:"agents"`
	Skills    int `json:"skills"`
	EvalCases int `json:"eval_cases"`
	Learnings int `json:"learnings"`
}

// NewLastExport records the export of b under fileName.
func NewLastExport(fileName string, b Bundle) LastExport {
	return LastExport{
		FileName: fileName, ExportedAt: b.Manifest.ExportedAt,
		Counts: LastExportCounts{
			Agents: len(b.Agents), Skills: len(b.Skills), EvalCases: len(b.EvalCases), Learnings: len(b.Learnings),
		},
	}
}

// EncodeLastExport is the JSON stored under LastExportKey.
func EncodeLastExport(l LastExport) (json.RawMessage, error) {
	raw, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("encode last export: %w", err)
	}
	return raw, nil
}

// DecodeLastExport reads the stored value. A null, malformed or empty-named value is "none"
// (ok false): a damaged row must not break GET /plugins.
func DecodeLastExport(raw json.RawMessage) (LastExport, bool) {
	if isNull(raw) {
		return LastExport{}, false
	}
	var l LastExport
	if err := json.Unmarshal(raw, &l); err != nil || l.FileName == "" {
		return LastExport{}, false
	}
	return l, true
}
