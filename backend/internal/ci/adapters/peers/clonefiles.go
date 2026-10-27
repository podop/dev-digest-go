package peers

import (
	"context"
	"errors"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/platform/clonefs"
)

// CloneFiles implements app.CloneFiles over platform/clonefs: confined to the clone directory, no
// symlink is followed, nothing is read and nothing is fetched.
type CloneFiles struct{}

var _ app.CloneFiles = CloneFiles{}

// NewCloneFiles builds the adapter.
func NewCloneFiles() CloneFiles { return CloneFiles{} }

// Exists reports whether the regular file at rel exists in the clone at clonePath. An empty
// clonePath (no clone yet), a missing clone or file, a directory, a symlink or an invalid path is
// false, nil.
func (CloneFiles) Exists(_ context.Context, clonePath, rel string) (bool, error) {
	if clonePath == "" {
		return false, nil
	}
	_, err := clonefs.Stat(clonePath, rel)
	if errors.Is(err, clonefs.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
