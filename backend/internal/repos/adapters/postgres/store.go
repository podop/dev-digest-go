// Package postgres persists repos with GORM.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/platform/codehost"
	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

// repoRow maps the repos table. created_at keeps its database default and is only used for
// ordering. provider and host keep their database defaults for a zero value, so rows written by
// code that does not name them stay GitHub on github.com.
type repoRow struct {
	ID            string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID   string     `gorm:"column:workspace_id"`
	Owner         string     `gorm:"column:owner"`
	Name          string     `gorm:"column:name"`
	FullName      string     `gorm:"column:full_name"`
	DefaultBranch string     `gorm:"column:default_branch"`
	ClonePath     *string    `gorm:"column:clone_path"`
	LastPolledAt  *time.Time `gorm:"column:last_polled_at"`
	CreatedBy     *string    `gorm:"column:created_by"`
	Provider      string     `gorm:"column:provider;default:github"`
	Host          string     `gorm:"column:host;default:github.com"`
	ProjectID     *int64     `gorm:"column:project_id"`
	WebURL        *string    `gorm:"column:web_url"`
}

func (repoRow) TableName() string { return "repos" }

// repoView is a repos row as read: the row plus the connection bound to its host (P4, FR16).
// The connection is looked up by host key at every read, never stored on the row (D26).
type repoView struct {
	Row            repoRow `gorm:"embedded"`
	ConnectionID   *string `gorm:"column:connection_id"`
	ConnectionBase *string `gorm:"column:connection_base_url"`
}

func (repoView) TableName() string { return "repos" }

// viewColumns and viewJoin read a repo with its connection. Only GitLab rows join; a GitHub
// repo has none.
const (
	viewColumns = "repos.*, gc.id AS connection_id, gc.base_url AS connection_base_url"
	viewJoin    = "LEFT JOIN gitlab_connections gc ON repos.provider = 'gitlab' AND gc.host = repos.host"
)

func (v repoView) toDomain() domain.Repo {
	r := v.Row
	repo := domain.Repo{
		ID:            r.ID,
		WorkspaceID:   r.WorkspaceID,
		Owner:         r.Owner,
		Name:          r.Name,
		FullName:      r.FullName,
		DefaultBranch: r.DefaultBranch,
		ClonePath:     r.ClonePath,
		LastPolledAt:  r.LastPolledAt,
		CreatedBy:     r.CreatedBy,
		Provider:      r.Provider,
		Host:          r.Host,
		ConnectionID:  v.ConnectionID,
	}
	if r.ProjectID != nil {
		repo.ProjectID = *r.ProjectID
	}
	if r.WebURL != nil {
		repo.WebURL = *r.WebURL
	}
	// A connected repo's address follows its connection (FR16); a not-connected one keeps the
	// address stored at its last successful project fetch.
	if v.ConnectionBase != nil {
		repo.WebURL = codehost.WebURL(*v.ConnectionBase, r.FullName)
	}
	return repo
}

// Store is the GORM-backed repo store. It implements the stores of the app package.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// conn is the handle for ctx: the open transaction when ctx came from InTx, else the pool.
func (s *Store) conn(ctx context.Context) *gorm.DB { return pg.Conn(ctx, s.db) }

// view starts a read of repos with their connection.
func (s *Store) view(ctx context.Context) *gorm.DB {
	return s.conn(ctx).Model(&repoView{}).Select(viewColumns).Joins(viewJoin)
}

// Add inserts the repo unless it exists and reselects the row. The insert reports whether this
// call created it, so of N concurrent adds of one repo exactly one sees created=true: a
// concurrent insert blocks on the unique index until the other transaction commits, then does
// nothing.
//
// A GitHub repo conflicts on (workspace_id, host, full_name). A GitLab repo may conflict on
// that index or on (workspace_id, host, project_id) (partial, provider = 'gitlab'); the insert
// names no conflict target, so either one makes it do nothing. The reselect finds the project
// first, then the path: a hit by path only is the stale path of another project and the caller
// tells it apart by ProjectID.
func (s *Store) Add(ctx context.Context, in app.NewRepo) (domain.Repo, bool, error) {
	db := s.conn(ctx)
	provider, host := in.Provider, in.Host
	if provider == "" {
		provider = codehost.GitHub
	}
	if host == "" {
		host = codehost.GitHubHost
	}
	branch := in.DefaultBranch
	if branch == "" {
		branch = domain.DefaultBranch
	}
	row := repoRow{
		WorkspaceID:   in.WorkspaceID,
		Owner:         in.Ref.Owner,
		Name:          in.Ref.Name,
		FullName:      in.Ref.FullName(),
		DefaultBranch: branch,
		Provider:      provider,
		Host:          host,
	}
	if in.CreatedBy != "" {
		row.CreatedBy = &in.CreatedBy
	}
	if provider == codehost.GitLab {
		row.ProjectID, row.WebURL = &in.ProjectID, &in.WebURL
	}
	conflict := clause.OnConflict{DoNothing: true}
	if provider == codehost.GitHub {
		conflict.Columns = []clause.Column{{Name: "workspace_id"}, {Name: "host"}, {Name: "full_name"}} // repos_ws_host_fullname_uq
	}
	res := db.Clauses(conflict).Omit("id").Create(&row)
	if res.Error != nil {
		return domain.Repo{}, false, fmt.Errorf("insert repo: %w", res.Error)
	}
	created := res.RowsAffected == 1

	var got repoView
	if provider == codehost.GitLab {
		err := s.view(ctx).Where("repos.workspace_id = ? AND repos.host = ? AND repos.provider = 'gitlab' AND repos.project_id = ?",
			in.WorkspaceID, host, in.ProjectID).Take(&got).Error
		if err == nil {
			return got.toDomain(), created, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Repo{}, false, fmt.Errorf("reselect repo: %w", err)
		}
	}
	if err := s.view(ctx).Where("repos.workspace_id = ? AND repos.host = ? AND repos.full_name = ?",
		in.WorkspaceID, host, row.FullName).Take(&got).Error; err != nil {
		return domain.Repo{}, false, fmt.Errorf("reselect repo: %w", err)
	}
	return got.toDomain(), created, nil
}

// UpdatePath sets full_name, owner, name and web_url of the workspace's GitLab repo (FR47).
// When the new full name is held by another repo of the workspace and host, nothing changes and
// that repo's id is returned; the unique index decides a race. domain.ErrNotFound means no
// GitLab repo with that id in the workspace.
func (s *Store) UpdatePath(ctx context.Context, workspaceID, id string, p app.ProjectPath) (string, error) {
	db := s.conn(ctx)
	update := db.Model(&repoRow{}).
		Where(`workspace_id = ? AND id = ? AND provider = 'gitlab'
			AND NOT EXISTS (SELECT 1 FROM repos o WHERE o.workspace_id = repos.workspace_id
				AND o.host = repos.host AND o.full_name = ? AND o.id <> repos.id)`,
			workspaceID, id, p.FullName).
		Updates(map[string]any{"full_name": p.FullName, "owner": p.Owner, "name": p.Name, "web_url": p.WebURL})
	if update.Error != nil && !isUniqueViolation(update.Error) {
		return "", fmt.Errorf("update repo path: %w", update.Error)
	}
	if update.Error == nil && update.RowsAffected == 1 {
		return "", nil
	}
	var other struct{ ID string }
	err := db.Table("repos o").Select("o.id").
		Joins("JOIN repos r ON r.workspace_id = o.workspace_id AND r.host = o.host AND r.id = ?", id).
		Where("o.workspace_id = ? AND o.full_name = ? AND o.id <> r.id AND r.provider = 'gitlab'", workspaceID, p.FullName).
		Take(&other).Error
	switch {
	case err == nil:
		return other.ID, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return "", fmt.Errorf("find repo holding %s: %w", p.FullName, err)
	}
	if update.Error != nil {
		return "", fmt.Errorf("update repo path: %w", update.Error)
	}
	return "", domain.ErrNotFound
}

// List returns the workspace's repos by creation time ascending, each with the id of the
// connection bound to its host (nil when there is none, FR16).
func (s *Store) List(ctx context.Context, workspaceID string) ([]domain.Repo, error) {
	var rows []repoView
	if err := s.view(ctx).Where("repos.workspace_id = ?", workspaceID).
		Order("repos.created_at, repos.id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select repos: %w", err)
	}
	repos := make([]domain.Repo, len(rows))
	for i, r := range rows {
		repos[i] = r.toDomain()
	}
	return repos, nil
}

// Get returns the repo of the workspace, or domain.ErrNotFound.
func (s *Store) Get(ctx context.Context, workspaceID, id string) (domain.Repo, error) {
	var row repoView
	err := s.view(ctx).Where("repos.workspace_id = ? AND repos.id = ?", workspaceID, id).Take(&row).Error
	return rowOrNotFound(row, err)
}

// GetByID returns the repo regardless of workspace, or domain.ErrNotFound. It is for job
// handlers, whose payload comes from our own queue.
func (s *Store) GetByID(ctx context.Context, id string) (domain.Repo, error) {
	var row repoView
	err := s.view(ctx).Where("repos.id = ?", id).Take(&row).Error
	return rowOrNotFound(row, err)
}

// Delete removes the repo of the workspace; foreign keys cascade to its dependents.
func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	res := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, id).Delete(&repoRow{})
	return affected(res, "delete repo")
}

// SetCloned stores the clone directory and the poll time.
func (s *Store) SetCloned(ctx context.Context, id, clonePath string, at time.Time) error {
	res := s.conn(ctx).Model(&repoRow{}).Where("id = ?", id).
		Updates(map[string]any{"clone_path": clonePath, "last_polled_at": at})
	return affected(res, "set clone path")
}

// SetPolled stores the poll time.
func (s *Store) SetPolled(ctx context.Context, id string, at time.Time) error {
	res := s.conn(ctx).Model(&repoRow{}).Where("id = ?", id).Update("last_polled_at", at)
	return affected(res, "set last polled")
}

// isUniqueViolation reports whether err carries SQLSTATE 23505, without importing the driver.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == "23505"
}

func rowOrNotFound(row repoView, err error) (domain.Repo, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Repo{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("select repo: %w", err)
	}
	return row.toDomain(), nil
}

func affected(res *gorm.DB, what string) error {
	if res.Error != nil {
		return fmt.Errorf("%s: %w", what, res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}
