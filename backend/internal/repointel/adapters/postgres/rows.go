package postgres

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// jsonb maps a jsonb column to raw JSON text. The columns using it are NOT NULL, so the "GORM
// skips Scan for NULL" quirk does not apply.
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

// symbolRow maps symbols for inserts. The id column keeps its database default.
type symbolRow struct {
	RepoID      string  `gorm:"column:repo_id"`
	Path        string  `gorm:"column:path"`
	Name        string  `gorm:"column:name"`
	Kind        string  `gorm:"column:kind"`
	Line        *int    `gorm:"column:line"`
	EndLine     *int    `gorm:"column:end_line"`
	Exported    bool    `gorm:"column:exported"`
	Signature   *string `gorm:"column:signature"`
	ContentHash *string `gorm:"column:content_hash"`
}

func (symbolRow) TableName() string { return "symbols" }

// referenceRow maps "references" for inserts; decl_file stays NULL until resolution.
type referenceRow struct {
	RepoID      string  `gorm:"column:repo_id"`
	FromPath    string  `gorm:"column:from_path"`
	ToSymbol    string  `gorm:"column:to_symbol"`
	Line        int     `gorm:"column:line"`
	ContentHash *string `gorm:"column:content_hash"`
}

func (referenceRow) TableName() string { return "references" }

// edgeRow maps file_edges.
type edgeRow struct {
	RepoID   string `gorm:"column:repo_id"`
	FromFile string `gorm:"column:from_file"`
	ToFile   string `gorm:"column:to_file"`
}

func (edgeRow) TableName() string { return "file_edges" }

// rankRow maps file_rank.
type rankRow struct {
	RepoID     string  `gorm:"column:repo_id"`
	FilePath   string  `gorm:"column:file_path"`
	PageRank   float64 `gorm:"column:pagerank"`
	Hotness    float64 `gorm:"column:hotness"`
	Rank       float64 `gorm:"column:rank"`
	Percentile int     `gorm:"column:percentile"`
}

func (rankRow) TableName() string { return "file_rank" }

// factsRow maps file_facts; endpoints and crons are JSON arrays of strings.
type factsRow struct {
	RepoID    string `gorm:"column:repo_id"`
	FilePath  string `gorm:"column:file_path"`
	Endpoints jsonb  `gorm:"column:endpoints;type:jsonb"`
	Crons     jsonb  `gorm:"column:crons;type:jsonb"`
}

func (factsRow) TableName() string { return "file_facts" }

// mapRow maps repo_map_cache for inserts; created_at keeps its default now().
type mapRow struct {
	RepoID      string `gorm:"column:repo_id;primaryKey"`
	CommitSHA   string `gorm:"column:commit_sha;primaryKey"`
	TokenBudget int    `gorm:"column:token_budget;primaryKey"`
	MapText     string `gorm:"column:map_text"`
	TokenCount  int    `gorm:"column:token_count"`
}

func (mapRow) TableName() string { return "repo_map_cache" }

// stateRow maps repo_index_state. updated_at is always written explicitly (the caller's clock),
// so GORM's automatic timestamp handling is off.
type stateRow struct {
	RepoID         string    `gorm:"column:repo_id;primaryKey"`
	LastIndexedSHA string    `gorm:"column:last_indexed_sha"`
	IndexerVersion int       `gorm:"column:indexer_version"`
	Status         string    `gorm:"column:status"`
	FilesIndexed   int       `gorm:"column:files_indexed"`
	FilesSkipped   int       `gorm:"column:files_skipped"`
	Stats          jsonb     `gorm:"column:stats;type:jsonb"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime:false;autoCreateTime:false"`
}

func (stateRow) TableName() string { return "repo_index_state" }

// repoRow is the part of repos the indexer reads.
type repoRow struct {
	ID            string  `gorm:"column:id"`
	WorkspaceID   string  `gorm:"column:workspace_id"`
	Owner         string  `gorm:"column:owner"`
	Name          string  `gorm:"column:name"`
	DefaultBranch string  `gorm:"column:default_branch"`
	ClonePath     *string `gorm:"column:clone_path"`
}

func (repoRow) TableName() string { return "repos" }

// marshalStrings encodes a string list as a JSON array; nil is [].
func marshalStrings(list []string) (jsonb, error) {
	if list == nil {
		list = []string{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return nil, fmt.Errorf("encode string list: %w", err)
	}
	return b, nil
}
