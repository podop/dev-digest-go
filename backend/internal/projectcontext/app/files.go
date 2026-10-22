package app

import (
	"context"
	"errors"
	"fmt"
	"math"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/projectcontext/domain"
)

// maxSuffix is the highest "-N" tried when a created path is taken.
const maxSuffix = 1000

// OnConflict values of CreateInput.
const (
	OnConflictFail   = "fail"
	OnConflictSuffix = "suffix"
)

// CreateInput is a request to create a store file; nil fields take their defaults.
type CreateInput struct {
	Path       *string
	Content    *string
	OnConflict *string // OnConflictFail or OnConflictSuffix
}

// CreateFile creates a store file at version 1.
func (s *Service) CreateFile(ctx context.Context, workspaceID, repoID string, in CreateInput) (Preview, error) {
	repo, err := s.requireRepo(ctx, workspaceID, repoID)
	if err != nil {
		return Preview{}, err
	}
	wanted := domain.StoreRoot + "untitled.md"
	onConflict := OnConflictSuffix
	if in.Path != nil {
		wanted, onConflict = *in.Path, OnConflictFail
		if in.OnConflict != nil {
			onConflict = *in.OnConflict
		}
	}
	if !domain.CheckStorePath(wanted) {
		return Preview{}, errInvalidStorePath()
	}
	content := ""
	if in.Content != nil {
		content = *in.Content
	}
	size, err := checkContent(content)
	if err != nil {
		return Preview{}, err
	}

	var file File
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		// The repo row lock serialises the count, the suffix pick and the insert per repo.
		locked, err := s.store.LockRepo(ctx, workspaceID, repoID)
		if err != nil {
			return fmt.Errorf("lock repo: %w", err)
		}
		if !locked {
			return errRepoNotFound()
		}
		taken, err := s.store.ListFiles(ctx, repoID)
		if err != nil {
			return fmt.Errorf("list store files: %w", err)
		}
		if len(taken) >= domain.StoreMaxFiles {
			return apperr.Validation("A repository holds at most 500 files").WithCode("too_many_files")
		}
		inStore := make(map[string]struct{}, len(taken))
		for _, f := range taken {
			inStore[f.Path] = struct{}{}
		}
		free := func(p string) bool {
			if _, ok := inStore[p]; ok {
				return false
			}
			return !s.inClone(repo, p)
		}
		path := wanted
		if !free(path) {
			if onConflict != OnConflictSuffix {
				return errPathExists()
			}
			for n := 2; ; n++ {
				if n > maxSuffix {
					return errPathExists()
				}
				path = domain.WithSuffix(wanted, n)
				if !domain.CheckStorePath(path) {
					return errInvalidStorePath()
				}
				if free(path) {
					break
				}
			}
		}
		file, err = s.store.InsertFile(ctx, repoID, path, content, size)
		return mapInsertErr(err)
	})
	if err != nil {
		return Preview{}, err
	}
	s.logWrite(ctx, repoID, "create", file)
	return s.preview(ctx, repoID, file)
}

// SaveFile saves the content of a store file when its version equals baseVersion.
func (s *Service) SaveFile(ctx context.Context, workspaceID, repoID, path, content string, baseVersion int64) (Preview, error) {
	repo, err := s.requireRepo(ctx, workspaceID, repoID)
	if err != nil {
		return Preview{}, err
	}
	if err := s.requireStoreFile(ctx, repo, path); err != nil {
		return Preview{}, err
	}
	size, err := checkContent(content)
	if err != nil {
		return Preview{}, err
	}
	var saved File
	ok := false
	if base, fits := versionOf(baseVersion); fits {
		saved, ok, err = s.store.SaveFile(ctx, repoID, path, content, size, base)
		if err != nil {
			return Preview{}, fmt.Errorf("save store file: %w", err)
		}
	}
	if !ok {
		return Preview{}, s.notSaved(ctx, repoID, path)
	}
	s.logWrite(ctx, repoID, "save", saved)
	return s.preview(ctx, repoID, saved)
}

// RenameFile moves a store file to newPath when its version equals baseVersion; the
// attachments of the old path follow.
func (s *Service) RenameFile(ctx context.Context, workspaceID, repoID, path, newPath string, baseVersion int64) (Preview, error) {
	repo, err := s.requireRepo(ctx, workspaceID, repoID)
	if err != nil {
		return Preview{}, err
	}
	current, err := s.requireStoreFileRow(ctx, repo, path)
	if err != nil {
		return Preview{}, err
	}
	if !domain.CheckStorePath(newPath) {
		return Preview{}, errInvalidStorePath()
	}
	if newPath == path {
		return s.preview(ctx, repoID, current) // nothing to move
	}
	if s.inClone(repo, newPath) {
		return Preview{}, errPathExists()
	}

	var renamed File
	moved := false
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		locked, err := s.store.LockRepo(ctx, workspaceID, repoID)
		if err != nil {
			return fmt.Errorf("lock repo: %w", err)
		}
		if !locked {
			return errRepoNotFound()
		}
		if _, taken, err := s.store.FindFile(ctx, repoID, newPath); err != nil {
			return fmt.Errorf("find store file: %w", err)
		} else if taken {
			return errPathExists()
		}
		base, fits := versionOf(baseVersion)
		if !fits {
			return nil
		}
		renamed, moved, err = s.store.RenameFile(ctx, repoID, path, newPath, base)
		if err != nil {
			return mapInsertErr(err)
		}
		if !moved {
			return nil
		}
		if err := s.store.MoveAttachments(ctx, repoID, path, newPath); err != nil {
			return fmt.Errorf("move attachments: %w", err)
		}
		return nil
	})
	if err != nil {
		return Preview{}, err
	}
	if !moved {
		return Preview{}, s.notSaved(ctx, repoID, path)
	}
	s.logWrite(ctx, repoID, "rename", renamed)
	return s.preview(ctx, repoID, renamed)
}

// DeleteFile deletes a store file. Its attachments stay.
func (s *Service) DeleteFile(ctx context.Context, workspaceID, repoID, path string) error {
	repo, err := s.requireRepo(ctx, workspaceID, repoID)
	if err != nil {
		return err
	}
	current, err := s.requireStoreFileRow(ctx, repo, path)
	if err != nil {
		return err
	}
	deleted, err := s.store.DeleteFile(ctx, repoID, path)
	if err != nil {
		return fmt.Errorf("delete store file: %w", err)
	}
	if !deleted {
		return errDocNotFound()
	}
	s.logWrite(ctx, repoID, "delete", current)
	return nil
}

// requireStoreFile succeeds when path is a store file of the repo.
func (s *Service) requireStoreFile(ctx context.Context, repo Repo, path string) error {
	_, err := s.requireStoreFileRow(ctx, repo, path)
	return err
}

// requireStoreFileRow returns the store file at path; a path that is not one is 403
// read_only when a clone document is there, else 404.
func (s *Service) requireStoreFileRow(ctx context.Context, repo Repo, path string) (File, error) {
	if domain.CheckStorePath(path) { // nothing else can be a store file; also keeps NUL away from SQL
		file, ok, err := s.store.FindFile(ctx, repo.ID, path)
		if err != nil {
			return File{}, fmt.Errorf("find store file: %w", err)
		}
		if ok {
			return file, nil
		}
	}
	if (domain.IsListablePath(path, s.globs) || domain.CheckStorePath(path)) && s.inClone(repo, path) {
		return File{}, apperr.Forbidden("Repository files are read-only").WithCode("read_only")
	}
	return File{}, errDocNotFound()
}

// notSaved explains a conditional write that matched no row: the file is gone (404) or its
// version moved on (409).
func (s *Service) notSaved(ctx context.Context, repoID, path string) error {
	now, ok, err := s.store.FindFile(ctx, repoID, path)
	if err != nil {
		return fmt.Errorf("find store file: %w", err)
	}
	if !ok {
		return errDocNotFound()
	}
	return apperr.Conflict(fmt.Sprintf("The file changed since you opened it (current is v%d)", now.Version)).
		WithCode("stale_version").
		WithDetails(map[string]int{"current_version": now.Version})
}

// inClone reports whether the clone holds a file at the (shape-valid) path. It only reads.
func (s *Service) inClone(repo Repo, path string) bool {
	if repo.ClonePath == nil || !domain.CheckPath(path) {
		return false
	}
	return s.clone.Exists(*repo.ClonePath, path)
}

func (s *Service) preview(ctx context.Context, repoID string, f File) (Preview, error) {
	rows, err := s.store.ListUsage(ctx, repoID)
	if err != nil {
		return Preview{}, fmt.Errorf("list usage: %w", err)
	}
	return storePreview(f, usedByPath(rows)[f.Path]), nil
}

func (s *Service) logWrite(ctx context.Context, repoID, operation string, f File) {
	s.log.InfoContext(ctx, "project context file",
		"repo_id", repoID, "operation", operation, "path", f.Path, "size", f.SizeBytes, "version", f.Version)
}

// checkContent is the gate of every write: no NUL (422), at most 256 KB of UTF-8 (413). It
// returns the byte size.
func checkContent(content string) (int, error) {
	if domain.HasNul(content) {
		return 0, apperr.Validation("The content holds a NUL character").WithCode("invalid_content")
	}
	size := domain.UTF8Bytes(content)
	if size > domain.MaxDocBytes {
		return 0, apperr.PayloadTooLarge("The file is larger than 256 KB").WithCode("doc_too_large")
	}
	return size, nil
}

// versionOf converts a client version to the database's integer; a value outside it matches nothing.
func versionOf(v int64) (int, bool) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, false
	}
	return int(v), true
}

func mapInsertErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrPathExists):
		return errPathExists()
	case errors.Is(err, ErrRepoGone):
		return errRepoNotFound()
	default:
		return fmt.Errorf("write store file: %w", err)
	}
}

func errInvalidStorePath() error {
	return apperr.Validation("Not a valid store file path").WithCode("invalid_path")
}

func errPathExists() error {
	return apperr.Conflict("A file already exists at this path").WithCode("path_exists")
}
