package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/domain/wfrules"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Wire codes and texts of the GitLab CI export (GitLab C spec 7.2).
const (
	codeRepoHostNotConnected = "repo_host_not_connected"
	msgGitHubNeedsGitHub     = "GitHub Actions export needs a GitHub repository"
	msgGitLabNeedsGitLab     = "GitLab CI export needs a GitLab repository"
	msgGitLabUnavailable     = "GitLab is unavailable, try again later"

	// defaultRetryPause is the wait between the two attempts of a publish sequence.
	defaultRetryPause = 500 * time.Millisecond
)

// findRepo resolves the repository of the request (FR1): 404 repo_not_found for a repository the
// workspace does not have, 422 repo_ambiguous (details.repo_ids) for a name on several GitLab hosts.
func (s *ExportService) findRepo(ctx context.Context, workspaceID string, req ExportRequest) (ExportRepo, error) {
	repo, ok, err := s.d.Repos.Find(ctx, workspaceID, RepoQuery{ID: req.RepoID, FullName: req.Repo})
	var amb *AmbiguousRepoError
	if errors.As(err, &amb) {
		return ExportRepo{}, apperr.Validation(req.Repo + " exists on several hosts; choose the repository by id").
			WithCode(codeRepoAmbiguous).
			WithDetails(map[string]any{"repo_ids": amb.IDs})
	}
	if err != nil {
		return ExportRepo{}, fmt.Errorf("find repository: %w", err)
	}
	if !ok {
		return ExportRepo{}, apperr.NotFound("Repository is not imported in this workspace").WithCode(codeRepoNotFound)
	}
	return repo, nil
}

// targetFor is the target of the export: the requested one, or by the repository's host when the
// request names none (FR2). A target that does not fit the host is 422 unsupported_target.
func targetFor(requested string, repo ExportRepo) (string, error) {
	gitlab := repo.Provider == ProviderGitLab
	switch {
	case requested == "" && gitlab:
		return domain.TargetGitLabCI, nil
	case requested == "":
		return domain.TargetGHA, nil
	case requested == domain.TargetGHA && gitlab:
		return "", apperr.Validation(msgGitHubNeedsGitHub).WithCode(codeUnsupportedTarget)
	case requested == domain.TargetGitLabCI && !gitlab:
		return "", apperr.Validation(msgGitLabNeedsGitLab).WithCode(codeUnsupportedTarget)
	}
	return requested, nil
}

// exportGitLab is the gitlab_ci export after the repository and the target are known (FR3): host
// connection, slug, skill names, the job file and its rules, the runner, the manifest, then the
// files or the publish and the installation.
func (s *ExportService) exportGitLab(ctx context.Context, workspaceID string, agent domain.ExportAgent, repo ExportRepo, prov providerInfo, triggers []domain.Trigger, req ExportRequest) (domain.Export, error) {
	conn, err := s.connection(ctx, repo)
	if err != nil {
		return domain.Export{}, err
	}
	slug, err := s.gitLabSlug(ctx, workspaceID, agent, repo)
	if err != nil {
		return domain.Export{}, err
	}
	skills, err := skillFiles(agent.Skills)
	if err != nil {
		return domain.Export{}, err
	}
	job, err := gitLabJobText(req, slug, agent.Provider, prov.headerCount)
	if err != nil {
		return domain.Export{}, err
	}
	runner, ok := s.d.Runner.Runner()
	if !ok {
		return domain.Export{}, apperr.Internal(msgRunnerMissing).WithCode(codeRunnerMissing)
	}
	base, err := s.baseFiles(&agent, skills, slug, runner)
	if err != nil {
		return domain.Export{}, err
	}
	base = append(base, domain.File{Path: domain.GitLabJobPath(slug), Contents: job, Editable: true})

	out := domain.Export{
		RequiredSecrets:   domain.GitLabRequiredSecrets(agent.Provider, prov.keyConfigured, prov.headerCount, req.PostAs),
		RequiredVariables: domain.RequiredVariables(agent.Provider),
	}
	gl := &domain.GitLabExport{
		JobFile:        domain.GitLabJobPath(slug),
		IncludeSnippet: domain.IncludeSnippet(slug),
		TriggersNote:   domain.TriggersNote(triggers),
	}
	out.GitLab = gl

	if req.Action == domain.ActionFiles {
		gl.RootConfig, gl.ConfigPath = domain.RootConfigCreate, domain.GitLabRootConfigPath
		exists, err := s.cloneHasRootConfig(ctx, repo)
		if err != nil {
			return domain.Export{}, err
		}
		if exists {
			gl.RootConfig = domain.RootConfigKeepExisting
		}
		out.Files = withRootFile(base, slug, gl.RootConfig)
		return out, nil
	}

	if !conn.HasToken {
		return domain.Export{}, apperr.Config("GitLab connection " + conn.Name + " has no token")
	}
	if s.d.GitLab == nil {
		return domain.Export{}, apperr.Internal("GitLab publishing is not configured")
	}
	pub, err := s.publishGitLab(ctx, gitLabPublish{
		workspaceID: workspaceID, repo: repo, slug: slug, base: base, runner: runner,
		secrets: out.RequiredSecrets, variables: out.RequiredVariables, triggersNote: gl.TriggersNote,
	})
	if err != nil {
		return domain.Export{}, err
	}
	gl.RootConfig, gl.ConfigPath = pub.root, pub.configPath
	out.Files = pub.files
	repo = pub.repo

	err = s.d.Store.UpsertGitLabInstallation(ctx, NewGitLabInstallation{
		AgentID: agent.ID, Host: repo.Host, ProjectID: repo.ProjectID, Repo: repo.FullName,
		Slug: slug, ManifestVersion: agent.Version, Provider: agent.Provider,
	})
	if errors.Is(err, ErrSlugTaken) {
		return domain.Export{}, slugConflict(slug)
	}
	if err != nil {
		return domain.Export{}, fmt.Errorf("save installation: %w", err)
	}
	inst, found, err := s.d.Store.FindGitLabInstallation(ctx, workspaceID, agent.ID, repo.Host, repo.ProjectID)
	if err != nil {
		return domain.Export{}, fmt.Errorf("reload installation: %w", err)
	}
	if !found {
		return domain.Export{}, apperr.Internal("The CI installation was not saved")
	}
	out.Installation, out.PRURL = &inst, &pub.prURL
	return out, nil
}

// connection is the connection of the repository's host (FR2): none is 422 repo_host_not_connected
// with details {host, repo_id}, for both actions and before any later check.
func (s *ExportService) connection(ctx context.Context, repo ExportRepo) (GitLabConn, error) {
	var (
		conn GitLabConn
		ok   bool
		err  error
	)
	if s.d.GitLabConns != nil {
		conn, ok, err = s.d.GitLabConns.Resolve(ctx, repo.Host)
	}
	if err != nil {
		return GitLabConn{}, fmt.Errorf("resolve gitlab connection: %w", err)
	}
	if !ok {
		return GitLabConn{}, apperr.Validation(fmt.Sprintf("No GitLab connection for %s. Add one in Settings → GitLab Integration.", repo.Host)).
			WithCode(codeRepoHostNotConnected).
			WithDetails(map[string]string{"host": repo.Host, "repo_id": repo.ID})
	}
	return conn, nil
}

// gitLabSlug is the slug of a GitLab export (Phase 6 7.6.2 on the repository identity host +
// project id): the one stored for (agent, project), else the one derived from the name; another
// agent owning it in the project is a conflict.
func (s *ExportService) gitLabSlug(ctx context.Context, workspaceID string, agent domain.ExportAgent, repo ExportRepo) (string, error) {
	existing, found, err := s.d.Store.FindGitLabInstallation(ctx, workspaceID, agent.ID, repo.Host, repo.ProjectID)
	if err != nil {
		return "", fmt.Errorf("find installation: %w", err)
	}
	slug := domain.AgentSlug(agent.Name)
	if found && existing.Slug != nil && *existing.Slug != "" {
		slug = *existing.Slug
	}
	owner, taken, err := s.d.Store.GitLabSlugOwner(ctx, repo.Host, repo.ProjectID, slug)
	if err != nil {
		return "", fmt.Errorf("find slug owner: %w", err)
	}
	if taken && owner != agent.ID {
		return "", slugConflict(slug)
	}
	return slug, nil
}

// gitLabJobText is the job file of the export: the edited text of the request (an empty string is
// text, checked and refused as invalid_yaml) or the generated one. Both pass the GitLab rules
// (FR14): 422 workflow_insecure with details.rule.
func gitLabJobText(req ExportRequest, slug, provider string, headerCount int) (string, error) {
	var text string
	if req.Workflow != nil {
		text = *req.Workflow
	} else {
		generated, err := domain.BuildGitLabJob(domain.GitLabJobOptions{
			Slug: slug, PostAs: req.PostAs, Provider: provider, Runner: req.Runner, HeaderCount: headerCount,
		})
		if err != nil {
			return "", fmt.Errorf("build job file: %w", err)
		}
		text = generated
	}
	rules := wfrules.GitLabRules{Slug: slug, HeaderCount: headerCount}
	if cs, ok := domain.CustomSlug(provider); ok {
		rules.CustomEnv, _ = domain.EnvName(cs)
	}
	if res := wfrules.CheckGitLab(text, rules); !res.OK {
		return "", apperr.Validation("The workflow breaks a security rule").
			WithCode(codeWorkflowInsecure).
			WithDetails(map[string]string{"rule": string(res.Rule)})
	}
	return text, nil
}

// cloneHasRootConfig decides the files preview from the local clone (FR5): no clone, or no
// `.gitlab-ci.yml` in it, is false. It makes no GitLab request.
func (s *ExportService) cloneHasRootConfig(ctx context.Context, repo ExportRepo) (bool, error) {
	if s.d.Clones == nil || repo.ClonePath == "" {
		return false, nil
	}
	ok, err := s.d.Clones.Exists(ctx, repo.ClonePath, domain.GitLabRootConfigPath)
	if err != nil {
		return false, fmt.Errorf("check clone for %s: %w", domain.GitLabRootConfigPath, err)
	}
	return ok, nil
}

// withRootFile appends the root include file to base when the export creates it. It returns a new
// slice; base is not changed.
func withRootFile(base []domain.File, slug string, root domain.RootConfig) []domain.File {
	files := append([]domain.File(nil), base...)
	if root == domain.RootConfigCreate {
		files = append(files, domain.File{Path: domain.GitLabRootConfigPath, Contents: domain.BuildRootInclude(slug)})
	}
	return files
}

// gitLabPublish is the input of one publish.
type gitLabPublish struct {
	workspaceID string
	repo        ExportRepo
	slug        string
	// base are the files of the export without the root include file.
	base         []domain.File
	runner       RunnerArtifact
	secrets      []string
	variables    []string
	triggersNote string
}

// gitLabPublished is what a publish decided and did.
type gitLabPublished struct {
	// repo is the repository after the project's path was followed.
	repo       ExportRepo
	root       domain.RootConfig
	configPath string
	// files are the files of the export as decided against GitLab: the root include file only for
	// RootConfigCreate. Files identical to the parent's are in the list but not committed.
	files []domain.File
	prURL string
}

// publishGitLab runs the publish sequence of FR11 and FR12 and maps its failures (§7.2). The whole
// sequence is retried once on a transient failure, never on 429 (A4); every write is one attempt.
// The sequence is idempotent: a commit that landed before its answer was lost is found identical
// by the retry, and an open merge request is reused.
func (s *ExportService) publishGitLab(ctx context.Context, in gitLabPublish) (gitLabPublished, error) {
	out, err := s.publishGitLabOnce(ctx, in)
	if err != nil && ctx.Err() == nil && transientGitLab(err) {
		if perr := s.pause(ctx); perr == nil {
			out, err = s.publishGitLabOnce(ctx, in)
		}
	}
	if err == nil {
		return out, nil
	}
	var ge *GitLabError
	if !errors.As(err, &ge) {
		return gitLabPublished{}, err
	}
	attrs := []any{"repo_id", in.repo.ID, "host", in.repo.Host}
	if ge.Status != 0 {
		attrs = append(attrs, "status", ge.Status)
	}
	if ge.Step != "" {
		attrs = append(attrs, "step", ge.Step)
	}
	s.log.ErrorContext(ctx, "ci export: GitLab write failed", attrs...)
	switch ge.Status {
	case 401, 403, 404:
		return gitLabPublished{}, apperr.Validation(fmt.Sprintf(
			"GitLab refused the write. The connection token needs the api scope and at least the Developer role in %s.",
			in.repo.FullName)).
			WithCode(string(domain.CodeGitLabPermissionDenied)).WithCause(err)
	}
	return gitLabPublished{}, apperr.ExternalService(msgGitLabUnavailable).
		WithCode(string(domain.CodeGitLabUnavailable)).WithCause(err)
}

// transientGitLab reports a failure worth one more sequence: a transport failure or a 5xx. A 429
// is not retried (the limit would only be hit again).
func transientGitLab(err error) bool {
	var ge *GitLabError
	if !errors.As(err, &ge) {
		return false
	}
	return ge.Transport || ge.Status >= 500
}

func (s *ExportService) pause(ctx context.Context) error {
	if s.d.RetryPause != nil {
		return s.d.RetryPause(ctx)
	}
	t := time.NewTimer(defaultRetryPause)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// publishGitLabOnce is one pass of the sequence: project, root decision, parent, file metas, the
// commit when an action remains, then the merge request.
func (s *ExportService) publishGitLabOnce(ctx context.Context, in gitLabPublish) (gitLabPublished, error) {
	gl := s.d.GitLab
	ref := GitLabRef{Host: in.repo.Host, ProjectID: in.repo.ProjectID}

	// 1. The project: current path and web address are stored, default branch and CI config read.
	proj, err := gl.Project(ctx, ref)
	if err != nil {
		return gitLabPublished{}, err
	}
	repo, err := s.d.Repos.FollowGitLabPath(ctx, in.workspaceID, in.repo, proj.PathWithNamespace)
	if err != nil {
		return gitLabPublished{}, fmt.Errorf("follow gitlab path: %w", err)
	}
	if repo.WebURL == "" {
		return gitLabPublished{}, fmt.Errorf("repository %s has no web address", repo.ID)
	}
	defaultBranch := proj.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = repo.DefaultBranch
	}
	if defaultBranch == "" {
		return gitLabPublished{}, errors.New("project has no default branch")
	}

	metas := map[string]fileProbe{} // keyed by ref + "\x00" + path
	probe := func(gitRef, path string) (fileProbe, error) {
		key := gitRef + "\x00" + path
		if p, ok := metas[key]; ok {
			return p, nil
		}
		sum, found, err := gl.FileSHA256(ctx, ref, path, gitRef)
		if err != nil {
			return fileProbe{}, err
		}
		metas[key] = fileProbe{sha: sum, found: found}
		return metas[key], nil
	}

	// 2. Root decision.
	root, configPath := domain.RootConfigCustomPath, proj.CIConfigPath
	if configPath == "" || configPath == domain.GitLabRootConfigPath {
		configPath = domain.GitLabRootConfigPath
		p, err := probe(defaultBranch, domain.GitLabRootConfigPath)
		if err != nil {
			return gitLabPublished{}, err
		}
		root = domain.RootConfigCreate
		if p.found {
			root = domain.RootConfigKeepExisting
		}
	}
	files := withRootFile(in.base, in.slug, root)

	// 3. Parent: the head of the CI branch when it exists, else the default branch.
	br, branchExists, err := gl.Branch(ctx, ref, domain.Branch)
	if err != nil {
		return gitLabPublished{}, err
	}
	parent := defaultBranch
	if branchExists {
		parent = br.HeadSHA
	}

	// 4. Actions: absent = create, other content = update, identical = left out.
	var actions []GitLabCommitAction
	for _, f := range files {
		p, err := probe(parent, f.Path)
		if err != nil {
			return gitLabPublished{}, err
		}
		switch {
		case !p.found:
			actions = append(actions, GitLabCommitAction{Action: "create", Path: f.Path, Content: f.Contents})
		case p.sha != sha256Hex(f.Contents):
			actions = append(actions, GitLabCommitAction{Action: "update", Path: f.Path, Content: f.Contents})
		}
	}

	// 5. One commit when anything is left.
	if len(actions) > 0 {
		commit := GitLabCommit{Branch: domain.Branch, Message: domain.CommitMessage, Actions: actions}
		if !branchExists {
			commit.StartBranch = defaultBranch
		}
		if err := gl.Commit(ctx, ref, commit); err != nil {
			return gitLabPublished{}, err
		}
	}

	// FR12: the open merge request is reused, else one is created.
	iid, found, err := gl.OpenMR(ctx, ref, domain.Branch, defaultBranch)
	if err != nil {
		return gitLabPublished{}, err
	}
	if !found {
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.Path
		}
		iid, err = gl.CreateMR(ctx, ref, GitLabNewMR{
			SourceBranch: domain.Branch, TargetBranch: defaultBranch, Title: domain.PRTitle,
			RemoveSourceBranch: true,
			Description: domain.BuildMRDescription(domain.MRDescInput{
				Slug: in.slug, Paths: paths, RunnerSHA256: in.runner.SHA256, BuildID: in.runner.BuildID,
				Secrets: in.secrets, Variables: in.variables, RootConfig: root, ConfigPath: configPath,
				TriggersNote: in.triggersNote,
			}),
		})
		var ge *GitLabError
		if errors.As(err, &ge) && ge.Status == 409 {
			// An MR for the branches was opened meanwhile: list again and reuse it.
			var again bool
			iid, again, err = gl.OpenMR(ctx, ref, domain.Branch, defaultBranch)
			if err == nil && !again {
				err = &GitLabError{Status: 409, Step: StepCreateMR, Cause: ge}
			}
		}
		if err != nil {
			return gitLabPublished{}, err
		}
	}
	return gitLabPublished{
		repo: repo, root: root, configPath: configPath, files: files,
		prURL: strings.TrimRight(repo.WebURL, "/") + "/-/merge_requests/" + strconv.Itoa(iid),
	}, nil
}

// fileProbe is the answer of one file-meta lookup.
type fileProbe struct {
	sha   string
	found bool
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
