package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrArtifactInvalid is returned by ParseResultArtifact for bytes that are not a valid
// devdigest-result.json: not UTF-8, not JSON, not an object, or breaking the CiResultArtifact
// schema.
var ErrArtifactInvalid = errors.New("invalid result artifact")

// CiResultArtifact is the parsed devdigest-result.json (the shared CiResultArtifact schema):
// counts and metadata only, never finding text. A nil pointer is an absent or null field. Keys the
// schema does not know are ignored.
type CiResultArtifact struct {
	FindingsCount int
	Critical      *int
	Warning       *int
	Suggestion    *int
	// CostUSD is required to be present, but null when the cost is unknown.
	CostUSD    *float64
	DurationMS *int
	Agent      string
	Version    *string
	PRNumber   *int

	// Provenance of artifact version 2, verified against the run by VerifyArtifact.
	CommitSHA       *string
	Repository      *string
	RepositoryID    *int64
	RunID           *int64
	RunAttempt      *int
	Model           *string
	ManifestVersion *int
	Skills          []string
	Blockers        *int
}

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER.
const maxSafeInteger = 1<<53 - 1

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// ParseResultArtifact parses the bytes of devdigest-result.json the way the Node studio does:
// strict UTF-8 (a leading byte order mark is dropped, as TextDecoder does), JSON, then the
// CiResultArtifact schema. Integer fields take any number JavaScript calls an integer (1, 1.0,
// 1e2) up to 2^53-1. The size limits are enforced before this, by the use case and the ZIP reader.
func ParseResultArtifact(data []byte) (CiResultArtifact, error) {
	if !utf8.Valid(data) {
		return CiResultArtifact{}, ErrArtifactInvalid
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if t := bytes.TrimLeft(data, " \t\r\n"); len(t) == 0 || t[0] != '{' {
		return CiResultArtifact{}, ErrArtifactInvalid
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return CiResultArtifact{}, ErrArtifactInvalid
	}
	var a CiResultArtifact
	d := fieldDecoder{obj: obj}

	if v, ok := d.integer("findings_count", true); ok {
		a.FindingsCount = int(*v)
	}
	a.Critical = d.optInt("critical")
	a.Warning = d.optInt("warning")
	a.Suggestion = d.optInt("suggestion")
	a.CostUSD = d.cost("cost_usd")
	a.DurationMS = d.optInt("duration_ms")
	if s := d.str("agent", true); s != nil {
		a.Agent = *s
	}
	a.Version = d.str("version", false)
	a.PRNumber = d.optInt("pr_number")
	a.CommitSHA = d.str("commit_sha", false)
	a.Repository = d.str("repository", false)
	a.RepositoryID = d.optInt64("repository_id")
	a.RunID = d.optInt64("run_id")
	a.RunAttempt = d.optInt("run_attempt")
	a.Model = d.str("model", false)
	a.ManifestVersion = d.optInt("manifest_version")
	a.Skills = d.strings("skills")
	a.Blockers = d.optInt("blockers")
	if d.bad {
		return CiResultArtifact{}, ErrArtifactInvalid
	}
	return a, nil
}

// fieldDecoder reads typed fields out of a JSON object and remembers whether any was wrong.
type fieldDecoder struct {
	obj map[string]json.RawMessage
	bad bool
}

// raw returns the field's raw value; present is false for an absent key; null is true for JSON
// null.
func (d *fieldDecoder) raw(key string) (v json.RawMessage, present, null bool) {
	v, present = d.obj[key]
	if !present {
		return nil, false, false
	}
	return v, true, string(bytes.TrimSpace(v)) == "null"
}

// number parses a JSON number the way JavaScript's JSON.parse does: as a float64. Any other kind
// of value, and a number that overflows to infinity, is wrong.
func (d *fieldDecoder) number(v json.RawMessage) (float64, bool) {
	t := strings.TrimSpace(string(v))
	if t == "" || (t[0] != '-' && (t[0] < '0' || t[0] > '9')) {
		return 0, false
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// integer reads an integer field. A missing or null required field is wrong; an optional one
// yields nil, false without marking the artifact wrong.
func (d *fieldDecoder) integer(key string, required bool) (*int64, bool) {
	v, present, null := d.raw(key)
	if !present || null {
		if required {
			d.bad = true
		}
		return nil, false
	}
	f, ok := d.number(v)
	if !ok || f != math.Trunc(f) || math.Abs(f) > maxSafeInteger {
		d.bad = true
		return nil, false
	}
	n := int64(f)
	return &n, true
}

func (d *fieldDecoder) optInt64(key string) *int64 {
	n, _ := d.integer(key, false)
	return n
}

func (d *fieldDecoder) optInt(key string) *int {
	n, ok := d.integer(key, false)
	if !ok {
		return nil
	}
	i := int(*n)
	return &i
}

// cost reads a field that must be present and is a number or null.
func (d *fieldDecoder) cost(key string) *float64 {
	v, present, null := d.raw(key)
	if !present {
		d.bad = true
		return nil
	}
	if null {
		return nil
	}
	f, ok := d.number(v)
	if !ok {
		d.bad = true
		return nil
	}
	return &f
}

// str reads a string field; absent or null is wrong when required, else nil.
func (d *fieldDecoder) str(key string, required bool) *string {
	v, present, null := d.raw(key)
	if !present || null {
		if required {
			d.bad = true
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		d.bad = true
		return nil
	}
	return &s
}

// strings reads an optional array of strings.
func (d *fieldDecoder) strings(key string) []string {
	v, present, null := d.raw(key)
	if !present || null {
		return nil
	}
	var list []string
	if t := bytes.TrimSpace(v); len(t) == 0 || t[0] != '[' || json.Unmarshal(v, &list) != nil {
		d.bad = true
		return nil
	}
	return list
}

// RunFacts are the GitHub facts about a workflow run that an artifact is verified against.
type RunFacts struct {
	HeadSHA      string
	RepositoryID int64
	// Path is the workflow file path as GitHub reports it; it may carry an "@ref" suffix.
	Path string
}

// FactsOf takes the verification facts out of a workflow run.
func FactsOf(r WorkflowRun) RunFacts {
	return RunFacts{HeadSHA: r.HeadSHA, RepositoryID: r.RepositoryID, Path: r.Path}
}

// VerifyArtifact checks that the artifact's own claims match what GitHub says about the run:
// commit_sha equals the head SHA and repository_id the repository id (both must be present), and
// the run comes from the installation's workflow file.
func VerifyArtifact(a CiResultArtifact, run RunFacts, workflowPath string) bool {
	if a.CommitSHA == nil || a.RepositoryID == nil {
		return false
	}
	path, _, _ := strings.Cut(run.Path, "@")
	return *a.CommitSHA == run.HeadSHA && *a.RepositoryID == run.RepositoryID && path == workflowPath
}
