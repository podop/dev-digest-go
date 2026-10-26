package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/plugins/domain"
)

// Remove deletes the plugin and everything it owns in one transaction. It locks the plugin row
// first (so a concurrent enable, disable or second removal waits), asks check with the plugin's
// name, counts the CI installations of the plugin's agents, then deletes the eval cases of its
// agents and skills (an eval case has no foreign key to its owner), its learnings, agents and
// skills, and last the plugin. Everything that hangs on an agent or skill by foreign key goes the
// way the agents and skills contexts' own deletes send it: links, versions, CI installations and
// eval suite runs cascade; reviews and agent runs keep their records with no agent (EC7); a user
// agent that linked a plugin skill keeps itself and loses the link (EC8). check's error is returned
// unchanged and nothing is deleted. domain.ErrNotFound for an unknown or malformed id.
func (s *Store) Remove(ctx context.Context, workspaceID, id string, check func(name string) error) (domain.Removed, error) {
	if !isUUID(id) {
		return domain.Removed{}, domain.ErrNotFound
	}
	var removed domain.Removed
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		db := pg.Conn(ctx, s.db)

		var locked pluginRow
		err := db.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id", "name").Where("workspace_id = ? AND id = ?", workspaceID, id).Take(&locked).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock plugin: %w", err)
		}
		if err := check(locked.Name); err != nil {
			return err
		}

		owned := sql.Named("plugin", id)
		var ci int64
		err = db.Raw(`SELECT count(*) FROM ci_installations
			WHERE agent_id IN (SELECT id FROM agents WHERE plugin_id = @plugin)`, owned).Scan(&ci).Error
		if err != nil {
			return fmt.Errorf("count ci installations: %w", err)
		}
		removed.CIInstallations = int(ci)

		res := db.Exec(`DELETE FROM eval_cases WHERE workspace_id = @ws AND (
				(owner_kind = 'agent' AND owner_id IN (SELECT id FROM agents WHERE plugin_id = @plugin))
				OR (owner_kind = 'skill' AND owner_id IN (SELECT id FROM skills WHERE plugin_id = @plugin)))`,
			sql.Named("ws", workspaceID), owned)
		if res.Error != nil {
			return fmt.Errorf("delete eval cases: %w", res.Error)
		}
		removed.EvalCases = int(res.RowsAffected)

		for _, step := range []struct {
			what string
			sql  string
			n    *int
		}{
			{"learnings", `DELETE FROM memory WHERE plugin_id = @plugin`, &removed.Learnings},
			{"agents", `DELETE FROM agents WHERE plugin_id = @plugin`, &removed.Agents},
			{"skills", `DELETE FROM skills WHERE plugin_id = @plugin`, &removed.Skills},
		} {
			res := db.Exec(step.sql, owned)
			if res.Error != nil {
				return fmt.Errorf("delete %s: %w", step.what, res.Error)
			}
			*step.n = int(res.RowsAffected)
		}

		res = db.Where("workspace_id = ? AND id = ?", workspaceID, id).Delete(&pluginRow{})
		if res.Error != nil {
			return fmt.Errorf("delete plugin: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return domain.Removed{}, err
	}
	return removed, nil
}
