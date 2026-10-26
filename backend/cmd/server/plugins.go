package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	pluginspg "dev-digest-go/backend/internal/plugins/adapters/postgres"
	pluginsapp "dev-digest-go/backend/internal/plugins/app"
	pluginsdomain "dev-digest-go/backend/internal/plugins/domain"
	settingsapp "dev-digest-go/backend/internal/settings/app"
)

// lastExports adapts the settings use case to the plugins LastExports port: the last export is the
// Settings row `plugin_last_export`. It is written here, through the settings use case, because
// PUT /settings refuses the key; the plugins context never imports settings.
type lastExports struct{ settings settingsRW }

// LastExport returns the stored value as raw JSON; found is false when no row exists.
func (l lastExports) LastExport(ctx context.Context, workspaceID string) (json.RawMessage, bool, error) {
	bag, err := l.settings.Get(ctx, workspaceID)
	if err != nil {
		return nil, false, fmt.Errorf("read settings: %w", err)
	}
	raw, ok := bag[pluginsdomain.LastExportKey]
	return raw, ok, nil
}

// SetLastExport upserts the row for the user the request acts as.
func (l lastExports) SetLastExport(ctx context.Context, workspaceID, userID string, raw json.RawMessage) error {
	if _, err := l.settings.Update(ctx, workspaceID, userID, settingsapp.Bag{pluginsdomain.LastExportKey: raw}); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

// newPluginsService builds the plugins use cases. One store implements the four database ports;
// the workspace display name is the same Settings row the workspace context reads (workspaceNames).
func newPluginsService(db *gorm.DB, settings settingsRW, custom pluginsapp.CustomProviders, now func() time.Time, log *slog.Logger, version string) *pluginsapp.Service {
	store := pluginspg.NewStore(db)
	return pluginsapp.NewService(pluginsapp.Deps{
		Reader:          store,
		Names:           store,
		Installer:       store,
		Plugins:         store,
		CustomProviders: custom,
		LastExports:     lastExports{settings: settings},
		WorkspaceNames:  workspaceNames{settings: settings},
		Clock:           pluginsapp.Clock(now),
		Log:             log,
		Version:         version,
	})
}
