package postgres

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dev-digest-go/backend/internal/evals/domain"
)

// SQLSTATEs the store translates.
const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
)

// finishedStatuses are the statuses of a finished suite run.
var finishedStatuses = []string{string(domain.RunCompleted), string(domain.RunFailed)}

// hasState reports whether err carries the SQLSTATE, without importing the driver.
func hasState(err error, state string) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == state
}

// isUUID reports whether s has the canonical 8-4-4-4-12 hex form; any other id cannot name a row,
// and a query with it would fail (SQLSTATE 22P02) instead of finding nothing.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// clean makes a string storable in a Postgres text column: U+0000 and invalid UTF-8 (both
// rejected by the server, SQLSTATE 22021) become U+FFFD.
func clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "�")
	}
	return strings.ToValidUTF8(s, "�")
}

func cleanPtr(s *string) *string {
	if s == nil {
		return nil
	}
	c := clean(*s)
	return &c
}

// marshal encodes v as JSON that a jsonb column accepts: encoding/json writes U+0000 as the
// escape \u0000, which jsonb rejects (SQLSTATE 22P05), so it is rewritten to �. An escaped
// backslash followed by the text u0000 is left alone.
func marshal(v any) (jsonb, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	if !bytes.Contains(raw, []byte(`\u0000`)) {
		return raw, nil
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		if raw[i] != '\\' || i+1 >= len(raw) {
			out = append(out, raw[i])
			i++
			continue
		}
		if raw[i+1] == 'u' && i+6 <= len(raw) && string(raw[i+2:i+6]) == "0000" {
			out = append(out, `�`...)
			i += 6
			continue
		}
		out = append(out, raw[i], raw[i+1])
		i += 2
	}
	return out, nil
}

// jsonb maps a jsonb column to raw JSON text. GORM does not call Scan for a SQL NULL, so a
// nullable column reads as nil; Value writes nil as SQL NULL.
type jsonb []byte

// Scan implements sql.Scanner.
func (v *jsonb) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		*v = nil
	case []byte:
		*v = append((*v)[:0], s...)
	case string:
		*v = jsonb(s)
	default:
		return fmt.Errorf("scan jsonb: unsupported type %T", src)
	}
	return nil
}

// Value implements driver.Valuer; the text form is cast to jsonb by the server.
func (v jsonb) Value() (driver.Value, error) {
	if len(v) == 0 {
		return nil, nil
	}
	return string(v), nil
}

// ---- JSON shapes of the jsonb columns (the contract's snake_case) ----

type expectationJSON struct {
	Kind      string  `json:"kind"`
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Severity  *string `json:"severity"`
	Category  *string `json:"category"`
	Title     *string `json:"title"`
}

func encodeExpectation(e domain.Expectation) (jsonb, error) {
	return marshal(expectationJSON{
		Kind: string(e.Kind), File: e.File, StartLine: e.StartLine, EndLine: e.EndLine,
		Severity: e.Severity, Category: e.Category, Title: e.Title,
	})
}

func decodeExpectation(raw jsonb) (domain.Expectation, error) {
	var j expectationJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return domain.Expectation{}, fmt.Errorf("decode expected_output: %w", err)
	}
	return domain.Expectation{
		Kind: domain.ExpectationKind(j.Kind), File: j.File, StartLine: j.StartLine, EndLine: j.EndLine,
		Severity: j.Severity, Category: j.Category, Title: j.Title,
	}, nil
}

type metaJSON struct {
	PRTitle       string  `json:"pr_title"`
	PRDescription *string `json:"pr_description"`
	Repo          *string `json:"repo"`
	PRNumber      *int    `json:"pr_number"`
	HeadSHA       *string `json:"head_sha"`
}

func encodeMeta(m domain.InputMeta) (jsonb, error) {
	return marshal(metaJSON{
		PRTitle: m.PRTitle, PRDescription: m.PRDescription, Repo: m.Repo, PRNumber: m.PRNumber, HeadSHA: m.HeadSHA,
	})
}

func decodeMeta(raw jsonb) (domain.InputMeta, error) {
	var j metaJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return domain.InputMeta{}, fmt.Errorf("decode input_meta: %w", err)
	}
	return domain.InputMeta{
		PRTitle: j.PRTitle, PRDescription: j.PRDescription, Repo: j.Repo, PRNumber: j.PRNumber, HeadSHA: j.HeadSHA,
	}, nil
}

type findingJSON struct {
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Severity  *string `json:"severity"`
	Category  *string `json:"category"`
	Title     *string `json:"title"`
	Match     string  `json:"match"`
}

func encodeFindings(fs []domain.ResultFinding) (jsonb, error) {
	out := make([]findingJSON, len(fs))
	for i, f := range fs {
		out[i] = findingJSON{
			File: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
			Severity: f.Severity, Category: f.Category, Title: f.Title, Match: string(f.Match),
		}
	}
	return marshal(out)
}

func decodeFindings(raw jsonb) ([]domain.ResultFinding, error) {
	out := []domain.ResultFinding{}
	if len(raw) == 0 {
		return out, nil
	}
	var js []findingJSON
	if err := json.Unmarshal(raw, &js); err != nil {
		return nil, fmt.Errorf("decode actual_output: %w", err)
	}
	for _, j := range js {
		out = append(out, domain.ResultFinding{
			File: j.File, StartLine: j.StartLine, EndLine: j.EndLine,
			Severity: j.Severity, Category: j.Category, Title: j.Title, Match: domain.FindingMatch(j.Match),
		})
	}
	return out, nil
}

type skillJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Version     int    `json:"version"`
}

type configJSON struct {
	Provider     string      `json:"provider"`
	Model        string      `json:"model"`
	SystemPrompt string      `json:"system_prompt"`
	Strategy     string      `json:"strategy"`
	Skills       []skillJSON `json:"skills"`
}

func encodeConfig(c domain.Config) (jsonb, error) {
	skills := make([]skillJSON, len(c.Skills))
	for i, s := range c.Skills {
		skills[i] = skillJSON{ID: s.ID, Name: s.Name, Description: s.Description, Body: s.Body, Version: s.Version}
	}
	return marshal(configJSON{
		Provider: c.Provider, Model: c.Model, SystemPrompt: c.SystemPrompt, Strategy: c.Strategy, Skills: skills,
	})
}

func decodeConfig(raw jsonb) (domain.Config, error) {
	var j configJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return domain.Config{}, fmt.Errorf("decode config: %w", err)
	}
	cfg := domain.Config{Provider: j.Provider, Model: j.Model, SystemPrompt: j.SystemPrompt, Strategy: j.Strategy}
	for _, s := range j.Skills {
		cfg.Skills = append(cfg.Skills, domain.SkillSnapshot{
			ID: s.ID, Name: s.Name, Description: s.Description, Body: s.Body, Version: s.Version,
		})
	}
	return cfg, nil
}

// ---- eval_cases ----

// caseRow maps eval_cases. input_files is never read or written.
type caseRow struct {
	ID              string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID     string     `gorm:"column:workspace_id"`
	OwnerKind       string     `gorm:"column:owner_kind"`
	OwnerID         string     `gorm:"column:owner_id"`
	Name            string     `gorm:"column:name"`
	InputDiff       *string    `gorm:"column:input_diff"`
	InputMeta       jsonb      `gorm:"column:input_meta;type:jsonb"`
	ExpectedOutput  jsonb      `gorm:"column:expected_output;type:jsonb"`
	Notes           *string    `gorm:"column:notes"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	SourceFindingID *string    `gorm:"column:source_finding_id"`
	SourceReviewID  *string    `gorm:"column:source_review_id"`
	SourcePRID      *string    `gorm:"column:source_pr_id"`
	ContentEditedAt *time.Time `gorm:"column:content_edited_at"`
}

func (caseRow) TableName() string { return "eval_cases" }

func newCaseRow(c domain.NewCase) (caseRow, error) {
	exp := c.Expectation
	exp.File = clean(exp.File)
	exp.Severity, exp.Category, exp.Title = cleanPtr(exp.Severity), cleanPtr(exp.Category), cleanPtr(exp.Title)
	expected, err := encodeExpectation(exp)
	if err != nil {
		return caseRow{}, err
	}
	meta, err := encodeMeta(c.InputMeta)
	if err != nil {
		return caseRow{}, err
	}
	diff := clean(c.InputDiff)
	return caseRow{
		WorkspaceID: c.WorkspaceID, OwnerKind: string(c.OwnerKind), OwnerID: c.OwnerID,
		Name: clean(c.Name), Notes: cleanPtr(c.Notes), InputDiff: &diff,
		InputMeta: meta, ExpectedOutput: expected,
		SourceFindingID: c.SourceFindingID, SourceReviewID: c.SourceReviewID, SourcePRID: c.SourcePRID,
	}, nil
}

func (r caseRow) toDomain() (domain.Case, error) {
	exp, err := decodeExpectation(r.ExpectedOutput)
	if err != nil {
		return domain.Case{}, fmt.Errorf("case %s: %w", r.ID, err)
	}
	meta, err := decodeMeta(r.InputMeta)
	if err != nil {
		return domain.Case{}, fmt.Errorf("case %s: %w", r.ID, err)
	}
	c := domain.Case{
		ID: r.ID, WorkspaceID: r.WorkspaceID, OwnerKind: domain.OwnerKind(r.OwnerKind), OwnerID: r.OwnerID,
		Name: r.Name, Notes: r.Notes, Expectation: exp, InputMeta: meta,
		SourceFindingID: r.SourceFindingID, SourceReviewID: r.SourceReviewID, SourcePRID: r.SourcePRID,
		ContentEditedAt: r.ContentEditedAt, CreatedAt: r.CreatedAt,
	}
	if r.InputDiff != nil {
		c.InputDiff = *r.InputDiff
	}
	return c, nil
}

// ---- eval_suite_runs and eval_runs ----

// runRow maps eval_suite_runs.
type runRow struct {
	ID                string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID       string     `gorm:"column:workspace_id"`
	AgentID           string     `gorm:"column:agent_id"`
	AgentVersion      int        `gorm:"column:agent_version"`
	Config            jsonb      `gorm:"column:config;type:jsonb"`
	CaseIDs           jsonb      `gorm:"column:case_ids;type:jsonb"`
	Status            string     `gorm:"column:status"`
	StartedAt         time.Time  `gorm:"column:started_at;default:now()"`
	FinishedAt        *time.Time `gorm:"column:finished_at"`
	CasesTotal        int        `gorm:"column:cases_total"`
	CasesFinished     int        `gorm:"column:cases_finished"`
	CasesPassed       int        `gorm:"column:cases_passed"`
	CasesErrored      int        `gorm:"column:cases_errored"`
	Recall            *float64   `gorm:"column:recall"`
	Precision         *float64   `gorm:"column:precision"`
	CitationAccuracy  *float64   `gorm:"column:citation_accuracy"`
	UnlabeledFindings int        `gorm:"column:unlabeled_findings"`
	CostUSD           *float64   `gorm:"column:cost_usd"`
	DurationMS        *int       `gorm:"column:duration_ms"`
	Error             *string    `gorm:"column:error"`
	AgentName         string     `gorm:"column:agent_name"`
	OwnerKind         string     `gorm:"column:owner_kind"`
	SkillID           *string    `gorm:"column:skill_id"`
	SkillVersion      *int       `gorm:"column:skill_version"`
}

func (runRow) TableName() string { return "eval_suite_runs" }

func newRunRow(r domain.NewRun) (runRow, error) {
	cfg := r.Config
	cfg.Provider, cfg.Model = clean(cfg.Provider), clean(cfg.Model)
	cfg.SystemPrompt, cfg.Strategy = clean(cfg.SystemPrompt), clean(cfg.Strategy)
	config, err := encodeConfig(cfg)
	if err != nil {
		return runRow{}, err
	}
	ids := r.CaseIDs
	if ids == nil {
		ids = []string{}
	}
	caseIDs, err := marshal(ids)
	if err != nil {
		return runRow{}, err
	}
	return runRow{
		WorkspaceID: r.WorkspaceID, AgentID: r.AgentID, AgentVersion: r.AgentVersion,
		Config: config, CaseIDs: caseIDs, Status: string(domain.RunRunning), CasesTotal: len(ids),
		AgentName: clean(r.AgentName), OwnerKind: string(r.OwnerKind), SkillID: r.SkillID, SkillVersion: r.SkillVersion,
	}, nil
}

func (r runRow) toDomain() (domain.Run, error) {
	cfg, err := decodeConfig(r.Config)
	if err != nil {
		return domain.Run{}, fmt.Errorf("run %s: %w", r.ID, err)
	}
	ids := []string{}
	if len(r.CaseIDs) > 0 {
		if err := json.Unmarshal(r.CaseIDs, &ids); err != nil {
			return domain.Run{}, fmt.Errorf("run %s: decode case_ids: %w", r.ID, err)
		}
	}
	return domain.Run{
		ID: r.ID, WorkspaceID: r.WorkspaceID, OwnerKind: domain.OwnerKind(r.OwnerKind),
		SkillID: r.SkillID, SkillVersion: r.SkillVersion,
		AgentID: r.AgentID, AgentVersion: r.AgentVersion, AgentName: r.AgentName,
		Config: cfg, CaseIDs: ids, Status: domain.RunStatus(r.Status),
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		CasesTotal: r.CasesTotal, CasesFinished: r.CasesFinished, CasesPassed: r.CasesPassed, CasesErrored: r.CasesErrored,
		Metrics: domain.Metrics{
			Recall: r.Recall, Precision: r.Precision, CitationAccuracy: r.CitationAccuracy,
			UnlabeledFindings: r.UnlabeledFindings, CostUSD: r.CostUSD,
		},
		DurationMS: r.DurationMS, Error: r.Error,
	}, nil
}

func runsToDomain(rows []runRow) ([]domain.Run, error) {
	out := make([]domain.Run, len(rows))
	for i, r := range rows {
		run, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out[i] = run
	}
	return out, nil
}

// resultRecord maps eval_runs for an insert (the legacy pass/recall/precision columns hold
// pass only, as Node writes them).
type resultRecord struct {
	ID             string   `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	RunID          string   `gorm:"column:run_id"`
	CaseID         string   `gorm:"column:case_id"`
	Status         string   `gorm:"column:status"`
	Pass           bool     `gorm:"column:pass"`
	ActualOutput   jsonb    `gorm:"column:actual_output;type:jsonb"`
	DroppedCount   int      `gorm:"column:dropped_count"`
	HitCount       int      `gorm:"column:hit_count"`
	ViolationCount int      `gorm:"column:violation_count"`
	UnlabeledCount int      `gorm:"column:unlabeled_count"`
	DurationMS     int      `gorm:"column:duration_ms"`
	CostUSD        *float64 `gorm:"column:cost_usd"`
	Error          *string  `gorm:"column:error"`
}

func (resultRecord) TableName() string { return "eval_runs" }

// resultRow is a result joined with its case, as the run detail and the compare read it.
type resultRow struct {
	CaseID         string   `gorm:"column:case_id"`
	Name           string   `gorm:"column:name"`
	ExpectedOutput jsonb    `gorm:"column:expected_output"`
	Status         *string  `gorm:"column:status"`
	ActualOutput   jsonb    `gorm:"column:actual_output"`
	DroppedCount   int      `gorm:"column:dropped_count"`
	DurationMS     *int     `gorm:"column:duration_ms"`
	CostUSD        *float64 `gorm:"column:cost_usd"`
	Error          *string  `gorm:"column:error"`
}

func (r resultRow) toDomain() (domain.CaseResult, error) {
	exp, err := decodeExpectation(r.ExpectedOutput)
	if err != nil {
		return domain.CaseResult{}, fmt.Errorf("case %s: %w", r.CaseID, err)
	}
	findings, err := decodeFindings(r.ActualOutput)
	if err != nil {
		return domain.CaseResult{}, fmt.Errorf("case %s: %w", r.CaseID, err)
	}
	res := domain.CaseResult{
		CaseID: r.CaseID, CaseName: r.Name, Expectation: exp, Status: domain.CaseError,
		Findings: findings, DroppedCount: r.DroppedCount, CostUSD: r.CostUSD, Error: r.Error,
	}
	// Rows of a suite run always have a status (only legacy single-case rows lack it).
	if r.Status != nil {
		res.Status = domain.CaseStatus(*r.Status)
	}
	if r.DurationMS != nil {
		res.DurationMS = *r.DurationMS
	}
	return res, nil
}
