// Package postgres persists skills with GORM. Every method gets its handle from
// postgres.Conn, so it joins the transaction a use case opened with postgres.Tx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
)

// uniqueViolation is the SQLSTATE of a unique constraint violation.
const uniqueViolation = "23505"

// skillRow maps the skills table (evidence_files is always null on the wire and not mapped).
type skillRow struct {
	ID          string    `gorm:"column:id;primaryKey"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	Name        string    `gorm:"column:name"`
	Description string    `gorm:"column:description"`
	Type        string    `gorm:"column:type"`
	Source      string    `gorm:"column:source"`
	SourceRef   *string   `gorm:"column:source_ref"`
	Body        string    `gorm:"column:body"`
	Enabled     bool      `gorm:"column:enabled"`
	Version     int       `gorm:"column:version"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
	// UsedBy is only filled by the reads that select usedByColumn; it is never written.
	UsedBy int `gorm:"column:used_by;->"`
	// The owner plugin is only filled by the reads that select pluginColumns; it is never written
	// (plugin_id itself is not mapped, so no insert or update can touch it).
	PluginID      *string `gorm:"column:plugin_owner_id;->"`
	PluginName    *string `gorm:"column:plugin_owner_name;->"`
	PluginEnabled *bool   `gorm:"column:plugin_owner_enabled;->"`
}

func (skillRow) TableName() string { return "skills" }

func (r skillRow) toDomain() domain.Skill {
	sk := domain.Skill{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, Description: r.Description,
		Type: domain.SkillType(r.Type), Source: domain.Source(r.Source), SourceRef: r.SourceRef,
		Body: r.Body, Enabled: r.Enabled, Version: r.Version,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, UsedBy: r.UsedBy,
	}
	if r.PluginID != nil && r.PluginName != nil && r.PluginEnabled != nil {
		sk.Plugin = &domain.PluginRef{ID: *r.PluginID, Name: *r.PluginName, Enabled: *r.PluginEnabled}
	}
	return sk
}

// usedByColumn counts the agents linking the skill row it is selected with.
const usedByColumn = "(SELECT count(*) FROM agent_skills a WHERE a.skill_id = skills.id) AS used_by"

// pluginColumns reads the owner plugin of the skill row it is selected with. Correlated
// subqueries (not a join) keep the statement valid under FOR UPDATE; all three are NULL for a
// skill no plugin owns.
const pluginColumns = "(SELECT p.id FROM installed_plugins p WHERE p.id = skills.plugin_id) AS plugin_owner_id, " +
	"(SELECT p.name FROM installed_plugins p WHERE p.id = skills.plugin_id) AS plugin_owner_name, " +
	"(SELECT p.enabled FROM installed_plugins p WHERE p.id = skills.plugin_id) AS plugin_owner_enabled"

// versionRow maps the skill_versions table.
type versionRow struct {
	SkillID     string    `gorm:"column:skill_id;primaryKey"`
	Version     int       `gorm:"column:version;primaryKey"`
	Body        string    `gorm:"column:body"`
	Description *string   `gorm:"column:description"`
	Message     *string   `gorm:"column:message"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (versionRow) TableName() string { return "skill_versions" }

func (r versionRow) toDomain() domain.Version {
	return domain.Version{
		SkillID: r.SkillID, Version: r.Version, Body: r.Body, Description: r.Description,
		Message: r.Message, CreatedAt: r.CreatedAt,
	}
}

// agentRow is the read-only view of the agents table the skills context needs: id, name and
// enabled. The agents context owns the table.
type agentRow struct {
	ID      string `gorm:"column:id"`
	Name    string `gorm:"column:name"`
	Enabled bool   `gorm:"column:enabled"`
}

func (agentRow) TableName() string { return "agents" }

func agentRefs(rows []agentRow) []domain.AgentRef {
	out := make([]domain.AgentRef, len(rows))
	for i, r := range rows {
		out[i] = domain.AgentRef{ID: r.ID, Name: r.Name, Enabled: r.Enabled}
	}
	return out
}

// repoRow is the read-only view of the repos table the merge needs.
type repoRow struct {
	ID          string `gorm:"column:id"`
	WorkspaceID string `gorm:"column:workspace_id"`
	FullName    string `gorm:"column:full_name"`
}

func (repoRow) TableName() string { return "repos" }

// evalCaseRow is the eval_cases table, of which the skills context only deletes the rows it owns.
type evalCaseRow struct {
	ID string `gorm:"column:id;primaryKey"`
}

func (evalCaseRow) TableName() string { return "eval_cases" }

// Store is the GORM-backed skills store.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return pg.Conn(ctx, s.db) }

// isUniqueViolation reports whether err is a PostgreSQL unique violation.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == uniqueViolation
}

// withUsage selects skills rows together with their used_by and owner plugin.
func (s *Store) withUsage(ctx context.Context) *gorm.DB {
	return s.conn(ctx).Table("skills").Select("skills.*, " + usedByColumn + ", " + pluginColumns)
}

// List returns the workspace's skills by name in byte order.
func (s *Store) List(ctx context.Context, workspaceID string) ([]domain.Skill, error) {
	var rows []skillRow
	if err := s.withUsage(ctx).Where("skills.workspace_id = ?", workspaceID).
		Order(`skills.name COLLATE "C", skills.id`).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("select skills: %w", err)
	}
	out := make([]domain.Skill, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// Get returns the skill, or domain.ErrNotFound.
func (s *Store) Get(ctx context.Context, workspaceID, id string) (domain.Skill, error) {
	var rows []skillRow
	if err := s.withUsage(ctx).Where("skills.workspace_id = ? AND skills.id = ?", workspaceID, id).
		Limit(1).Scan(&rows).Error; err != nil {
		return domain.Skill{}, fmt.Errorf("select skill: %w", err)
	}
	if len(rows) == 0 {
		return domain.Skill{}, domain.ErrNotFound
	}
	return rows[0].toDomain(), nil
}

// Lock selects the skill FOR UPDATE; the lock lasts until the surrounding transaction ends.
func (s *Store) Lock(ctx context.Context, workspaceID, id string) (domain.Skill, error) {
	var rows []skillRow
	// FOR UPDATE OF skills: the owner plugin comes from a subquery in the select list, so the
	// lock stays on the skill row only.
	err := s.conn(ctx).Table("skills").Select("skills.*, "+pluginColumns).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "skills"}}).
		Where("skills.workspace_id = ? AND skills.id = ?", workspaceID, id).Limit(1).Scan(&rows).Error
	if err != nil {
		return domain.Skill{}, fmt.Errorf("lock skill: %w", err)
	}
	if len(rows) == 0 {
		return domain.Skill{}, domain.ErrNotFound
	}
	return rows[0].toDomain(), nil
}

// Insert stores the skill as version 1.
func (s *Store) Insert(ctx context.Context, in domain.NewSkill) (domain.Skill, error) {
	row := skillRow{
		WorkspaceID: in.WorkspaceID, Name: in.Name, Description: in.Description, Type: string(in.Type),
		Source: string(in.Source), SourceRef: in.SourceRef, Body: in.Body, Enabled: in.Enabled, Version: 1,
	}
	// The database fills id, created_at and updated_at; RETURNING * brings them back.
	err := s.conn(ctx).Clauses(clause.Returning{}).Omit("id", "created_at", "updated_at").Create(&row).Error
	if isUniqueViolation(err) {
		return domain.Skill{}, domain.ErrDuplicateName
	}
	if err != nil {
		return domain.Skill{}, fmt.Errorf("insert skill: %w", err)
	}
	return row.toDomain(), nil
}

// Update writes the non-nil fields and updated_at, then re-reads the skill.
func (s *Store) Update(ctx context.Context, workspaceID, id string, w domain.Write) (domain.Skill, error) {
	set := map[string]any{"updated_at": gorm.Expr("now()")}
	if w.Name != nil {
		set["name"] = *w.Name
	}
	if w.Description != nil {
		set["description"] = *w.Description
	}
	if w.Type != nil {
		set["type"] = string(*w.Type)
	}
	if w.Body != nil {
		set["body"] = *w.Body
	}
	if w.Enabled != nil {
		set["enabled"] = *w.Enabled
	}
	if w.Version != nil {
		set["version"] = *w.Version
	}
	res := s.conn(ctx).Model(&skillRow{}).Where("workspace_id = ? AND id = ?", workspaceID, id).Updates(set)
	if isUniqueViolation(res.Error) {
		return domain.Skill{}, domain.ErrDuplicateName
	}
	if res.Error != nil {
		return domain.Skill{}, fmt.Errorf("update skill: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.Skill{}, domain.ErrNotFound
	}
	return s.Get(ctx, workspaceID, id)
}

// InsertVersion stores a snapshot.
func (s *Store) InsertVersion(ctx context.Context, in domain.NewVersion) error {
	row := versionRow{SkillID: in.SkillID, Version: in.Version, Body: in.Body, Description: &in.Description, Message: &in.Message}
	if err := s.conn(ctx).Omit("created_at").Create(&row).Error; err != nil {
		return fmt.Errorf("insert skill version: %w", err)
	}
	return nil
}

// ListVersions returns the snapshots newest first.
func (s *Store) ListVersions(ctx context.Context, skillID string) ([]domain.Version, error) {
	var rows []versionRow
	if err := s.conn(ctx).Where("skill_id = ?", skillID).Order("version DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select skill versions: %w", err)
	}
	out := make([]domain.Version, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// GetVersion returns one snapshot, or domain.ErrVersionNotFound.
func (s *Store) GetVersion(ctx context.Context, skillID string, version int) (domain.Version, error) {
	var row versionRow
	err := s.conn(ctx).Where("skill_id = ? AND version = ?", skillID, version).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Version{}, domain.ErrVersionNotFound
	}
	if err != nil {
		return domain.Version{}, fmt.Errorf("select skill version: %w", err)
	}
	return row.toDomain(), nil
}

// Delete removes the skill; links, snapshots and run links go by cascade, findings and
// conventions are set to no skill by the foreign keys.
func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	res := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, id).Delete(&skillRow{})
	if res.Error != nil {
		return fmt.Errorf("delete skill: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteEvalCases removes the eval cases owned by the skill.
func (s *Store) DeleteEvalCases(ctx context.Context, workspaceID, skillID string) error {
	err := s.conn(ctx).Where("workspace_id = ? AND owner_kind = 'skill' AND owner_id = ?", workspaceID, skillID).
		Delete(&evalCaseRow{}).Error
	if err != nil {
		return fmt.Errorf("delete skill eval cases: %w", err)
	}
	return nil
}

// Agents returns the agents linking the skill, by name in the database's collation, then id.
func (s *Store) Agents(ctx context.Context, skillID string) ([]domain.AgentRef, error) {
	var rows []agentRow
	err := s.conn(ctx).Table("agent_skills AS s").Select("a.id, a.name, a.enabled").
		Joins("JOIN agents a ON a.id = s.agent_id").Where("s.skill_id = ?", skillID).
		Order("a.name, a.id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select skill agents: %w", err)
	}
	return agentRefs(rows), nil
}

// FindRepo returns the repo of the workspace, or domain.ErrRepoNotFound.
func (s *Store) FindRepo(ctx context.Context, workspaceID, repoID string) (app.RepoRef, error) {
	var rows []repoRow
	err := s.conn(ctx).Select("id, workspace_id, full_name").
		Where("workspace_id = ? AND id = ?", workspaceID, repoID).Limit(1).Find(&rows).Error
	if err != nil {
		return app.RepoRef{}, fmt.Errorf("select repo: %w", err)
	}
	if len(rows) == 0 {
		return app.RepoRef{}, domain.ErrRepoNotFound
	}
	return app.RepoRef{ID: rows[0].ID, FullName: rows[0].FullName}, nil
}

// FindAgents returns the agents of the workspace among ids.
func (s *Store) FindAgents(ctx context.Context, workspaceID string, ids []string) ([]domain.AgentRef, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []agentRow
	if err := s.conn(ctx).Select("id, name, enabled").
		Where("workspace_id = ? AND id IN ?", workspaceID, ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select agents: %w", err)
	}
	return agentRefs(rows), nil
}
