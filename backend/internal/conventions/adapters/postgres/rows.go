package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/conventions/domain"
)

// jsonb maps a jsonb column to raw JSON text. The columns using it are NOT NULL, so the
// "GORM skips Scan for NULL" quirk does not apply.
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
		return "null", nil
	}
	return string(v), nil
}

// scanRow maps convention_scans. Tokens are stored but never read.
type scanRow struct {
	ID           string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID  string     `gorm:"column:workspace_id"`
	RepoID       string     `gorm:"column:repo_id"`
	Status       string     `gorm:"column:status"`
	SampledFiles jsonb      `gorm:"column:sampled_files;type:jsonb"`
	Proposed     int        `gorm:"column:proposed"`
	Kept         int        `gorm:"column:kept"`
	Dropped      jsonb      `gorm:"column:dropped;type:jsonb"`
	Model        *string    `gorm:"column:model"`
	TokensIn     *int       `gorm:"column:tokens_in"`
	TokensOut    *int       `gorm:"column:tokens_out"`
	CostUSD      *float64   `gorm:"column:cost_usd"`
	Error        *string    `gorm:"column:error"`
	StartedAt    time.Time  `gorm:"column:started_at"`
	FinishedAt   *time.Time `gorm:"column:finished_at"`
}

func (scanRow) TableName() string { return "convention_scans" }

// droppedJSON is the stored shape of a dropped candidate (the wire shape, too).
type droppedJSON struct {
	Rule   string `json:"rule"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func (r scanRow) toDomain() (domain.Scan, error) {
	sampled := []string{}
	if len(r.SampledFiles) > 0 {
		if err := json.Unmarshal(r.SampledFiles, &sampled); err != nil {
			return domain.Scan{}, fmt.Errorf("decode sampled_files of scan %s: %w", r.ID, err)
		}
	}
	var stored []droppedJSON
	if len(r.Dropped) > 0 {
		if err := json.Unmarshal(r.Dropped, &stored); err != nil {
			return domain.Scan{}, fmt.Errorf("decode dropped of scan %s: %w", r.ID, err)
		}
	}
	dropped := make([]domain.Dropped, len(stored))
	for i, d := range stored {
		dropped[i] = domain.Dropped{Rule: d.Rule, Path: d.Path, Reason: domain.DropReason(d.Reason)}
	}
	return domain.Scan{
		ID: r.ID, WorkspaceID: r.WorkspaceID, RepoID: r.RepoID, Status: domain.ScanStatus(r.Status),
		SampledFiles: sampled, Proposed: r.Proposed, Kept: r.Kept, Dropped: dropped,
		Model: r.Model, CostUSD: r.CostUSD, Error: r.Error, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}, nil
}

// conventionRow maps conventions.
type conventionRow struct {
	ID          string    `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	RepoID      string    `gorm:"column:repo_id"`
	Rule        string    `gorm:"column:rule"`
	Confidence  *float64  `gorm:"column:confidence"`
	ScanID      *string   `gorm:"column:scan_id"`
	Category    string    `gorm:"column:category"`
	Evidence    jsonb     `gorm:"column:evidence;type:jsonb"`
	Status      string    `gorm:"column:status"`
	Edited      bool      `gorm:"column:edited"`
	SkillID     *string   `gorm:"column:skill_id"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (conventionRow) TableName() string { return "conventions" }

// evidenceJSON is the stored shape of an evidence item (the wire shape, too).
type evidenceJSON struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Snippet   string `json:"snippet"`
}

func (r conventionRow) toDomain() (domain.Convention, error) {
	var stored []evidenceJSON
	if len(r.Evidence) > 0 {
		if err := json.Unmarshal(r.Evidence, &stored); err != nil {
			return domain.Convention{}, fmt.Errorf("decode evidence of convention %s: %w", r.ID, err)
		}
	}
	evidence := make([]domain.Evidence, len(stored))
	for i, e := range stored {
		evidence[i] = domain.Evidence{Path: e.Path, StartLine: e.StartLine, EndLine: e.EndLine, Snippet: e.Snippet}
	}
	confidence := 0.0
	if r.Confidence != nil {
		confidence = *r.Confidence
	}
	return domain.Convention{
		ID: r.ID, WorkspaceID: r.WorkspaceID, RepoID: r.RepoID, ScanID: r.ScanID, Category: r.Category,
		Rule: r.Rule, Evidence: evidence, Confidence: confidence, Status: domain.Status(r.Status),
		Edited: r.Edited, SkillID: r.SkillID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

func marshalEvidence(list []domain.Evidence) (jsonb, error) {
	out := make([]evidenceJSON, len(list))
	for i, e := range list {
		out[i] = evidenceJSON{Path: e.Path, StartLine: e.StartLine, EndLine: e.EndLine, Snippet: e.Snippet}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode evidence: %w", err)
	}
	return b, nil
}

func marshalDropped(list []domain.Dropped) (jsonb, error) {
	out := make([]droppedJSON, len(list))
	for i, d := range list {
		out[i] = droppedJSON{Rule: d.Rule, Path: d.Path, Reason: string(d.Reason)}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode dropped: %w", err)
	}
	return b, nil
}

func marshalStrings(list []string) (jsonb, error) {
	if list == nil {
		list = []string{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return nil, fmt.Errorf("encode sampled files: %w", err)
	}
	return b, nil
}

// repoRow reads the repos table. The conventions context only looks repos up; it owns no write.
type repoRow struct {
	ID        string  `gorm:"column:id"`
	FullName  string  `gorm:"column:full_name"`
	ClonePath *string `gorm:"column:clone_path"`
}

func (repoRow) TableName() string { return "repos" }
