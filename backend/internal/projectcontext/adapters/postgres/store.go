// Package postgres persists the project-context store files and attachments with GORM. It
// reads repos, agents and skills through its own row structs and writes only the three
// context tables; it never touches an agent's or skill's version.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/projectcontext/app"
)

// PostgreSQL error codes this store translates.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

type repoRow struct {
	ID          string  `gorm:"column:id;primaryKey"`
	WorkspaceID string  `gorm:"column:workspace_id"`
	ClonePath   *string `gorm:"column:clone_path"`
}

func (repoRow) TableName() string { return "repos" }

type agentRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id"`
}

func (agentRow) TableName() string { return "agents" }

type skillRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id"`
}

func (skillRow) TableName() string { return "skills" }

type fileRow struct {
	ID        string    `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	RepoID    string    `gorm:"column:repo_id"`
	Path      string    `gorm:"column:path"`
	Content   string    `gorm:"column:content"`
	SizeBytes int       `gorm:"column:size_bytes"`
	Version   int       `gorm:"column:version"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (fileRow) TableName() string { return "context_files" }

func (r fileRow) toFile() app.File {
	return app.File{Path: r.Path, Content: r.Content, SizeBytes: r.SizeBytes, Version: r.Version, UpdatedAt: r.UpdatedAt}
}

type fileInfoRow struct {
	Path      string    `gorm:"column:path"`
	SizeBytes int       `gorm:"column:size_bytes"`
	Chars     int       `gorm:"column:chars"`
	Version   int       `gorm:"column:version"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type agentDocRow struct {
	AgentID  string `gorm:"column:agent_id;primaryKey"`
	RepoID   string `gorm:"column:repo_id;primaryKey"`
	Path     string `gorm:"column:path;primaryKey"`
	Position int    `gorm:"column:position"`
}

func (agentDocRow) TableName() string { return "agent_context_docs" }

type skillDocRow struct {
	SkillID  string `gorm:"column:skill_id;primaryKey"`
	RepoID   string `gorm:"column:repo_id;primaryKey"`
	Path     string `gorm:"column:path;primaryKey"`
	Position int    `gorm:"column:position"`
}

func (skillDocRow) TableName() string { return "skill_context_docs" }

// usageRow is one joined attachment of a repo.
type usageRow struct {
	Path      string  `gorm:"column:path"`
	AgentID   string  `gorm:"column:agent_id"`
	AgentName string  `gorm:"column:agent_name"`
	SkillName *string `gorm:"column:skill_name"`
}

// Store is the GORM-backed project-context store. Its methods join the transaction carried by
// the context (postgres.InTx).
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return postgres.Conn(ctx, s.db) }

// FindRepo returns the repo of the workspace.
func (s *Store) FindRepo(ctx context.Context, workspaceID, repoID string) (app.Repo, bool, error) {
	var row repoRow
	res := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, repoID).Limit(1).Find(&row)
	if res.Error != nil {
		return app.Repo{}, false, fmt.Errorf("select repo: %w", res.Error)
	}
	return app.Repo{ID: row.ID, ClonePath: row.ClonePath}, res.RowsAffected == 1, nil
}

// LockRepo locks the repo row (FOR UPDATE) until the transaction ends.
func (s *Store) LockRepo(ctx context.Context, workspaceID, repoID string) (bool, error) {
	var row repoRow
	res := s.conn(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("workspace_id = ? AND id = ?", workspaceID, repoID).Limit(1).Find(&row)
	if res.Error != nil {
		return false, fmt.Errorf("lock repo: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// AgentExists reports whether the workspace has the agent.
func (s *Store) AgentExists(ctx context.Context, workspaceID, agentID string) (bool, error) {
	res := s.conn(ctx).Select("id").Where("workspace_id = ? AND id = ?", workspaceID, agentID).Limit(1).Find(&agentRow{})
	if res.Error != nil {
		return false, fmt.Errorf("select agent: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// SkillExists reports whether the workspace has the skill.
func (s *Store) SkillExists(ctx context.Context, workspaceID, skillID string) (bool, error) {
	res := s.conn(ctx).Select("id").Where("workspace_id = ? AND id = ?", workspaceID, skillID).Limit(1).Find(&skillRow{})
	if res.Error != nil {
		return false, fmt.Errorf("select skill: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ListUsage returns the direct attachments, then the attachments through linked skills (every
// linked skill counts, enabled or not).
func (s *Store) ListUsage(ctx context.Context, repoID string) ([]app.UsageRow, error) {
	db := s.conn(ctx)
	var direct []usageRow
	err := db.Table("agent_context_docs AS d").
		Select("d.path AS path, a.id AS agent_id, a.name AS agent_name").
		Joins("JOIN agents a ON a.id = d.agent_id").
		Where("d.repo_id = ?", repoID).
		Order(`a.name COLLATE "C", a.id`).
		Scan(&direct).Error
	if err != nil {
		return nil, fmt.Errorf("select direct usage: %w", err)
	}
	var viaSkill []usageRow
	err = db.Table("skill_context_docs AS d").
		Select("d.path AS path, a.id AS agent_id, a.name AS agent_name, s.name AS skill_name").
		Joins("JOIN agent_skills l ON l.skill_id = d.skill_id").
		Joins("JOIN agents a ON a.id = l.agent_id").
		Joins("JOIN skills s ON s.id = d.skill_id").
		Where("d.repo_id = ?", repoID).
		Order(`a.name COLLATE "C", a.id, s.name COLLATE "C"`).
		Scan(&viaSkill).Error
	if err != nil {
		return nil, fmt.Errorf("select skill usage: %w", err)
	}
	out := make([]app.UsageRow, 0, len(direct)+len(viaSkill))
	for _, r := range direct {
		out = append(out, app.UsageRow{Path: r.Path, AgentID: r.AgentID, AgentName: r.AgentName, Via: app.ViaDirect})
	}
	for _, r := range viaSkill {
		row := app.UsageRow{Path: r.Path, AgentID: r.AgentID, AgentName: r.AgentName, Via: app.ViaSkill}
		if r.SkillName != nil {
			row.SkillName = *r.SkillName
		}
		out = append(out, row)
	}
	return out, nil
}

// AgentPaths returns the agent's attached paths in one repo by position.
func (s *Store) AgentPaths(ctx context.Context, agentID, repoID string) ([]string, error) {
	var rows []agentDocRow
	err := s.conn(ctx).Where("agent_id = ? AND repo_id = ?", agentID, repoID).Order(`"position"`).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select agent paths: %w", err)
	}
	paths := make([]string, len(rows))
	for i, r := range rows {
		paths[i] = r.Path
	}
	return paths, nil
}

// SkillPaths returns the skill's attached paths in one repo by position.
func (s *Store) SkillPaths(ctx context.Context, skillID, repoID string) ([]string, error) {
	var rows []skillDocRow
	err := s.conn(ctx).Where("skill_id = ? AND repo_id = ?", skillID, repoID).Order(`"position"`).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select skill paths: %w", err)
	}
	paths := make([]string, len(rows))
	for i, r := range rows {
		paths[i] = r.Path
	}
	return paths, nil
}

// ReplaceAgentPaths replaces the list. Run it in a transaction: the agent row is locked first
// so two PUTs for the same agent serialise instead of colliding on the primary key.
func (s *Store) ReplaceAgentPaths(ctx context.Context, agentID, repoID string, paths []string) error {
	db := s.conn(ctx)
	if err := db.Select("id").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", agentID).Limit(1).Find(&agentRow{}).Error; err != nil {
		return fmt.Errorf("lock agent: %w", err)
	}
	if err := db.Where("agent_id = ? AND repo_id = ?", agentID, repoID).Delete(&agentDocRow{}).Error; err != nil {
		return fmt.Errorf("delete agent paths: %w", err)
	}
	if len(paths) == 0 {
		return nil
	}
	rows := make([]agentDocRow, len(paths))
	for i, p := range paths {
		rows[i] = agentDocRow{AgentID: agentID, RepoID: repoID, Path: p, Position: i}
	}
	return translateInsert(db.Create(&rows).Error, app.ErrOwnerGone, "insert agent paths")
}

// ReplaceSkillPaths replaces the list; see ReplaceAgentPaths.
func (s *Store) ReplaceSkillPaths(ctx context.Context, skillID, repoID string, paths []string) error {
	db := s.conn(ctx)
	if err := db.Select("id").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", skillID).Limit(1).Find(&skillRow{}).Error; err != nil {
		return fmt.Errorf("lock skill: %w", err)
	}
	if err := db.Where("skill_id = ? AND repo_id = ?", skillID, repoID).Delete(&skillDocRow{}).Error; err != nil {
		return fmt.Errorf("delete skill paths: %w", err)
	}
	if len(paths) == 0 {
		return nil
	}
	rows := make([]skillDocRow, len(paths))
	for i, p := range paths {
		rows[i] = skillDocRow{SkillID: skillID, RepoID: repoID, Path: p, Position: i}
	}
	return translateInsert(db.Create(&rows).Error, app.ErrOwnerGone, "insert skill paths")
}

// ListFiles returns the repo's store files in byte order of the path, without content.
func (s *Store) ListFiles(ctx context.Context, repoID string) ([]app.FileInfo, error) {
	var rows []fileInfoRow
	err := s.conn(ctx).Table("context_files").
		Select("path, size_bytes, length(content) AS chars, version, updated_at").
		Where("repo_id = ?", repoID).
		Order(`path COLLATE "C"`).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select store files: %w", err)
	}
	out := make([]app.FileInfo, len(rows))
	for i, r := range rows {
		out[i] = app.FileInfo{Path: r.Path, SizeBytes: r.SizeBytes, Chars: r.Chars, Version: r.Version, UpdatedAt: r.UpdatedAt}
	}
	return out, nil
}

// FindFile returns the store file at path.
func (s *Store) FindFile(ctx context.Context, repoID, path string) (app.File, bool, error) {
	var row fileRow
	res := s.conn(ctx).Where("repo_id = ? AND path = ?", repoID, path).Limit(1).Find(&row)
	if res.Error != nil {
		return app.File{}, false, fmt.Errorf("select store file: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return app.File{}, false, nil
	}
	return row.toFile(), true, nil
}

// InsertFile inserts a store file at version 1.
func (s *Store) InsertFile(ctx context.Context, repoID, path, content string, sizeBytes int) (app.File, error) {
	row := fileRow{RepoID: repoID, Path: path, Content: content, SizeBytes: sizeBytes}
	err := s.conn(ctx).Omit("id", "version", "updated_at").Clauses(clause.Returning{}).Create(&row).Error
	if err != nil {
		switch pgCode(err) {
		case pgUniqueViolation:
			return app.File{}, app.ErrPathExists
		case pgForeignKeyViolation:
			return app.File{}, app.ErrRepoGone
		}
		return app.File{}, fmt.Errorf("insert store file: %w", err)
	}
	return row.toFile(), nil
}

// SaveFile updates the content when the version equals base, bumping it.
func (s *Store) SaveFile(ctx context.Context, repoID, path, content string, sizeBytes, base int) (app.File, bool, error) {
	var row fileRow
	res := s.conn(ctx).Model(&row).Clauses(clause.Returning{}).
		Where("repo_id = ? AND path = ? AND version = ?", repoID, path, base).
		Updates(map[string]any{
			"content":    content,
			"size_bytes": sizeBytes,
			"version":    gorm.Expr("version + 1"),
			"updated_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		return app.File{}, false, fmt.Errorf("update store file: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return app.File{}, false, nil
	}
	return row.toFile(), true, nil
}

// RenameFile moves the file when the version equals base, bumping it.
func (s *Store) RenameFile(ctx context.Context, repoID, path, newPath string, base int) (app.File, bool, error) {
	var row fileRow
	res := s.conn(ctx).Model(&row).Clauses(clause.Returning{}).
		Where("repo_id = ? AND path = ? AND version = ?", repoID, path, base).
		Updates(map[string]any{
			"path":       newPath,
			"version":    gorm.Expr("version + 1"),
			"updated_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		if pgCode(res.Error) == pgUniqueViolation {
			return app.File{}, false, app.ErrPathExists
		}
		return app.File{}, false, fmt.Errorf("rename store file: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return app.File{}, false, nil
	}
	return row.toFile(), true, nil
}

// MoveAttachments repoints the attachments of from to to for agents and skills. An owner that
// already attaches to keeps one row: the old to row is dropped and the moved row keeps its position.
func (s *Store) MoveAttachments(ctx context.Context, repoID, from, to string) error {
	db := s.conn(ctx)
	for _, t := range []struct{ table, owner string }{
		{"agent_context_docs", "agent_id"},
		{"skill_context_docs", "skill_id"},
	} {
		err := db.Exec(`DELETE FROM `+t.table+` WHERE repo_id = ? AND path = ? AND `+t.owner+` IN (
			SELECT `+t.owner+` FROM `+t.table+` WHERE repo_id = ? AND path = ?)`, repoID, to, repoID, from).Error
		if err != nil {
			return fmt.Errorf("drop existing %s of %s: %w", t.table, to, err)
		}
		err = db.Exec(`UPDATE `+t.table+` SET path = ? WHERE repo_id = ? AND path = ?`, to, repoID, from).Error
		if err != nil {
			return fmt.Errorf("repoint %s: %w", t.table, err)
		}
	}
	return nil
}

// DeleteFile deletes the store file; attachments stay.
func (s *Store) DeleteFile(ctx context.Context, repoID, path string) (bool, error) {
	res := s.conn(ctx).Where("repo_id = ? AND path = ?", repoID, path).Delete(&fileRow{})
	if res.Error != nil {
		return false, fmt.Errorf("delete store file: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

func pgCode(err error) string {
	var withState interface{ SQLState() string } // *pgconn.PgError
	if errors.As(err, &withState) {
		return withState.SQLState()
	}
	return ""
}

// translateInsert maps a foreign-key violation (the owner or repo was deleted after the
// existence check) to gone.
func translateInsert(err, gone error, what string) error {
	if err == nil {
		return nil
	}
	if pgCode(err) == pgForeignKeyViolation {
		return gone
	}
	return fmt.Errorf("%s: %w", what, err)
}
