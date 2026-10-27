package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/domain/wfrules"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Wire codes and texts of the export answers (spec 7.2, 7.6).
const (
	codeAgentNotFound     = "agent_not_found"
	codeUnsupportedTarget = "unsupported_target"
	// codeProviderNotConfigured is the 422 for an agent on an undefined custom provider (FR24). The
	// former unsupported_provider is never produced any more (D26).
	codeProviderNotConfigured = "provider_not_configured"
	codeInvalidTriggers       = "invalid_triggers"
	codeRepoNotFound          = "repo_not_found"
	codeRepoAmbiguous         = "repo_ambiguous"
	codeSlugConflict          = "ci_slug_conflict"
	codeSkillSlugConflict     = "skill_slug_conflict"
	codeWorkflowInsecure      = "workflow_insecure"
	codeRunnerMissing         = "runner_bundle_missing"
	codeManifestInvalid       = "manifest_invalid"
	// CodePluginDisabled is the code of the 409 for an agent of a disabled plugin (the same wire
	// code the plugins, reviews and evals contexts use; duplicated, not imported).
	CodePluginDisabled = "plugin_disabled"

	msgRunnerMissing = "The runner binary is missing: rebuild the backend image (docker compose build backend)"
	msgNoToken       = "GITHUB_TOKEN is not configured"
)

// ExportDeps are the ports of ExportService; every field except Log is required.
type ExportDeps struct {
	Agents AgentDirectory
	Repos  RepoDirectory
	Store  Store
	Runner RunnerBundle
	GitHub GitHub
	// Custom reads the workspace's custom providers; nil = every custom provider is undefined
	// (the export of such an agent answers 422 provider_not_configured).
	Custom CustomProviders
	// Prices is the price book for the `pricing` key of openai / anthropic manifests (FR35); nil =
	// no built-in model has a price.
	Prices ModelPrices
	// GitLabConns, Clones and GitLab serve a gitlab_ci export (GitLab C). A nil GitLabConns makes
	// every GitLab repository not connected (422 repo_host_not_connected); a nil Clones means no
	// repository has a clone; a nil GitLab fails an open_pr of a GitLab repository with 500.
	GitLabConns GitLabConns
	Clones      CloneFiles
	GitLab      GitLabPublisher
	// RetryPause waits between the two attempts of a GitLab publish sequence (A4); nil waits
	// 500 ms. It returns the context's error when ctx ends first.
	RetryPause func(ctx context.Context) error
	// Log receives the content-free publish failure line; nil discards it.
	Log *slog.Logger
}

// ExportService implements Exporter.
type ExportService struct {
	d   ExportDeps
	log *slog.Logger
}

var _ Exporter = (*ExportService)(nil)

// NewExportService builds the service. A missing dependency is a wiring bug and panics at startup.
func NewExportService(d ExportDeps) *ExportService {
	if d.Agents == nil || d.Repos == nil || d.Store == nil || d.Runner == nil || d.GitHub == nil {
		panic("ci/app: NewExportService needs every dependency")
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ExportService{d: d, log: log}
}

// Export runs the checks of spec 7.6.1 in order, builds the files and, for open_pr, publishes them.
func (s *ExportService) Export(ctx context.Context, workspaceID, agentID string, req ExportRequest) (domain.Export, error) {
	agent, ok, err := s.d.Agents.Get(ctx, workspaceID, agentID)
	if err != nil {
		return domain.Export{}, fmt.Errorf("load agent: %w", err)
	}
	if !ok {
		return domain.Export{}, apperr.NotFound("Agent not found").WithCode(codeAgentNotFound)
	}
	// An agent of a disabled plugin is not exportable (its own enabled flag is not checked: a
	// disabled agent can still be exported on purpose). Nothing is read or written before this.
	if p := agent.Plugin; p != nil && !p.Enabled {
		return domain.Export{}, apperr.Conflict(fmt.Sprintf("Agent %q belongs to the disabled plugin %q", agent.Name, p.Name)).
			WithCode(CodePluginDisabled)
	}
	if req.Target != "" && req.Target != domain.TargetGHA && req.Target != domain.TargetGitLabCI {
		return domain.Export{}, apperr.Validation("Only GitHub Actions can be exported").WithCode(codeUnsupportedTarget)
	}
	prov, err := s.resolveProvider(ctx, workspaceID, &agent)
	if err != nil {
		return domain.Export{}, err
	}
	triggers, ok := domain.NormalizeTriggers(req.Triggers)
	if !ok {
		return domain.Export{}, apperr.Validation("Select at least one valid pull_request trigger").WithCode(codeInvalidTriggers)
	}
	located, err := s.findRepo(ctx, workspaceID, req)
	if err != nil {
		return domain.Export{}, err
	}
	target, err := targetFor(req.Target, located)
	if err != nil {
		return domain.Export{}, err
	}
	if target == domain.TargetGitLabCI {
		return s.exportGitLab(ctx, workspaceID, agent, located, prov, triggers, req)
	}
	repo := domain.ImportedRepo{Owner: located.Owner, Name: located.Name, FullName: located.FullName, DefaultBranch: located.DefaultBranch}

	slug, err := s.slugFor(ctx, workspaceID, agent, repo)
	if err != nil {
		return domain.Export{}, err
	}
	skills, err := skillFiles(agent.Skills)
	if err != nil {
		return domain.Export{}, err
	}

	workflow, err := workflowText(req, slug, triggers, agent.Provider, prov.headerCount)
	if err != nil {
		return domain.Export{}, err
	}
	runner, ok := s.d.Runner.Runner()
	if !ok {
		return domain.Export{}, apperr.Internal(msgRunnerMissing).WithCode(codeRunnerMissing)
	}

	files, err := s.baseFiles(&agent, skills, slug, runner)
	if err != nil {
		return domain.Export{}, err
	}
	files = append(files, domain.File{Path: domain.WorkflowPath(slug), Contents: workflow, Editable: true})
	required := domain.Export{
		Files:             files,
		RequiredSecrets:   domain.RequiredSecrets(agent.Provider, prov.keyConfigured, prov.headerCount),
		RequiredVariables: domain.RequiredVariables(agent.Provider),
	}
	if req.Action == domain.ActionFiles {
		return required, nil
	}

	if !s.d.GitHub.Configured() {
		return domain.Export{}, apperr.Config(msgNoToken)
	}
	prURL, err := s.publish(ctx, repo, slug, runner, required)
	if err != nil {
		return domain.Export{}, err
	}
	err = s.d.Store.UpsertInstallation(ctx, domain.NewInstallation{
		AgentID: agent.ID, Repo: repo.FullName, Slug: slug, ManifestVersion: agent.Version,
		Provider: agent.Provider,
	})
	if errors.Is(err, ErrSlugTaken) {
		return domain.Export{}, slugConflict(slug)
	}
	if err != nil {
		return domain.Export{}, fmt.Errorf("save installation: %w", err)
	}
	inst, found, err := s.d.Store.FindInstallation(ctx, workspaceID, agent.ID, repo.FullName)
	if err != nil {
		return domain.Export{}, fmt.Errorf("reload installation: %w", err)
	}
	if !found {
		return domain.Export{}, apperr.Internal("The CI installation was not saved")
	}
	required.Installation, required.PRURL = &inst, &prURL
	return required, nil
}

// baseFiles are the files both targets share, in order: the manifest, the skills, the memory and
// the runner with its checksum. It prices a built-in model first (FR35) and refuses an agent that
// cannot be a manifest (422 manifest_invalid).
func (s *ExportService) baseFiles(agent *domain.ExportAgent, skills []skillFile, slug string, runner RunnerArtifact) ([]domain.File, error) {
	slugs := make([]string, len(skills))
	for i, sk := range skills {
		slugs[i] = sk.slug
	}
	s.priceBuiltIn(agent)
	manifest, err := domain.BuildManifest(*agent, slugs)
	if err != nil {
		return nil, manifestError(err)
	}
	files := make([]domain.File, 0, len(skills)+6)
	files = append(files, domain.File{Path: domain.ManifestPath(slug), Contents: manifest.YAML()})
	for _, sk := range skills {
		files = append(files, domain.File{Path: domain.SkillPath(sk.slug), Contents: sk.body})
	}
	return append(files,
		domain.File{Path: domain.MemoryPath},
		domain.File{Path: domain.RunnerPath, Contents: runner.Encoded},
		domain.File{Path: domain.RunnerSumPath, Contents: domain.RunnerSumFile(runner.SHA256)},
	), nil
}

// providerInfo is what the provider step learned for the rest of the export.
type providerInfo struct {
	// headerCount is the number of extra headers of a custom provider, 0 for a built-in.
	headerCount int
	// keyConfigured is true when a custom provider has a stored key; unused for a built-in.
	keyConfigured bool
}

// resolveProvider is the provider step of the checks (spec 7.6.1, FR24): openrouter, openai and
// anthropic are exportable as they are; a custom id must be defined in the workspace, and its
// definition and manual price are copied into the agent for the manifest. An undefined custom id
// is 422 provider_not_configured. A value that is neither can only come from a corrupted row (the
// database CHECK and the agent validation refuse it): it is reported as the manifest problem it
// would be, never as the retired unsupported_provider.
func (s *ExportService) resolveProvider(ctx context.Context, workspaceID string, agent *domain.ExportAgent) (providerInfo, error) {
	switch agent.Provider {
	case domain.ProviderOpenRouter, domain.ProviderOpenAI, domain.ProviderAnthropic:
		return providerInfo{}, nil
	}
	if !domain.IsCustomProvider(agent.Provider) {
		return providerInfo{}, manifestError(&domain.ManifestError{Issues: []domain.ManifestIssue{
			{Path: "provider", Message: "Invalid provider"},
		}})
	}
	notConfigured := apperr.Validation(fmt.Sprintf("Custom provider %s is not configured", agent.Provider)).
		WithCode(codeProviderNotConfigured)
	if s.d.Custom == nil {
		return providerInfo{}, notConfigured
	}
	ce, ok, err := s.d.Custom.ForExport(ctx, workspaceID, agent.Provider, agent.Model)
	if err != nil {
		return providerInfo{}, fmt.Errorf("load custom provider: %w", err)
	}
	if !ok {
		return providerInfo{}, notConfigured
	}
	cfg := ce.Config
	cfg.ExtraHeaders = slices.Clone(cfg.ExtraHeaders)
	agent.Custom = &cfg
	agent.Pricing = ce.Pricing
	return providerInfo{headerCount: len(cfg.ExtraHeaders), keyConfigured: ce.KeyConfigured}, nil
}

// priceBuiltIn sets the price book's price of an openai / anthropic model on the agent (FR35,
// D29); no price, no port or another provider leaves it unset, and the export never fails for it.
func (s *ExportService) priceBuiltIn(agent *domain.ExportAgent) {
	if s.d.Prices == nil || (agent.Provider != domain.ProviderOpenAI && agent.Provider != domain.ProviderAnthropic) {
		return
	}
	if p, ok := s.d.Prices.PerM(agent.Provider, agent.Model); ok {
		agent.Pricing = &p
	}
}

// ListInstallations returns the agent's installations, newest first, each with its last run.
func (s *ExportService) ListInstallations(ctx context.Context, workspaceID, agentID string) ([]domain.Installation, error) {
	ok, err := s.d.Agents.Exists(ctx, workspaceID, agentID)
	if err != nil {
		return nil, fmt.Errorf("check agent: %w", err)
	}
	if !ok {
		return nil, apperr.NotFound("Agent not found").WithCode(codeAgentNotFound)
	}
	list, err := s.d.Store.ListInstallations(ctx, workspaceID, InstallationFilter{AgentID: agentID})
	if err != nil {
		return nil, fmt.Errorf("list installations: %w", err)
	}
	return list, nil
}

// slugFor is the slug of an export (spec 7.6.2): the one stored for (agent, repository), else the
// one derived from the name; another agent owning it in the repository is a conflict.
func (s *ExportService) slugFor(ctx context.Context, workspaceID string, agent domain.ExportAgent, repo domain.ImportedRepo) (string, error) {
	existing, found, err := s.d.Store.FindInstallation(ctx, workspaceID, agent.ID, repo.FullName)
	if err != nil {
		return "", fmt.Errorf("find installation: %w", err)
	}
	slug := domain.AgentSlug(agent.Name)
	if found && existing.Slug != nil && *existing.Slug != "" {
		slug = *existing.Slug
	}
	owner, taken, err := s.d.Store.SlugOwner(ctx, repo.FullName, slug)
	if err != nil {
		return "", fmt.Errorf("find slug owner: %w", err)
	}
	if taken && owner != agent.ID {
		return "", slugConflict(slug)
	}
	return slug, nil
}

func slugConflict(slug string) error {
	return apperr.Conflict(fmt.Sprintf("Another agent already uses the CI name %q in this repository", slug)).
		WithCode(codeSlugConflict)
}

// skillFile is a skill file before it becomes a File.
type skillFile struct{ slug, body string }

// skillFiles derives the file names of the enabled skills and refuses two skills with one name.
func skillFiles(skills []domain.ExportSkill) ([]skillFile, error) {
	out := make([]skillFile, len(skills))
	seen := make(map[string]bool, len(skills))
	for i, sk := range skills {
		slug := domain.SkillSlug(sk.Name)
		if seen[slug] {
			return nil, apperr.Validation("Two attached skills have the same file name").WithCode(codeSkillSlugConflict)
		}
		seen[slug] = true
		out[i] = skillFile{slug: slug, body: sk.Body}
	}
	return out, nil
}

// workflowText is the workflow of the export: the edited text of the request (an empty string is
// text, checked and refused as invalid_yaml, as Node does with `??`), else the generated one. Both
// pass the workflow rules (spec 7.6.6).
func workflowText(req ExportRequest, slug string, triggers []domain.Trigger, provider string, headerCount int) (string, error) {
	var text string
	if req.Workflow != nil {
		text = *req.Workflow
	} else {
		generated, err := domain.BuildWorkflow(domain.WorkflowOptions{
			Slug: slug, Triggers: triggers, PostAs: req.PostAs,
			Provider: provider, Runner: req.Runner, HeaderCount: headerCount,
		})
		if err != nil {
			return "", fmt.Errorf("build workflow: %w", err)
		}
		text = generated
	}
	names, _ := domain.AllowedSecretNames(provider, headerCount) // resolveProvider admitted only providers that have names
	if res := wfrules.CheckWith(text, names); !res.OK {
		return "", apperr.Validation("The workflow breaks a security rule").
			WithCode(codeWorkflowInsecure).
			WithDetails(map[string]string{"rule": string(res.Rule)})
	}
	return text, nil
}

// manifestError maps a BuildManifest failure to 422 manifest_invalid with details.issues.
func manifestError(err error) error {
	var me *domain.ManifestError
	if !errors.As(err, &me) {
		return fmt.Errorf("build manifest: %w", err)
	}
	issues := make([]map[string]string, len(me.Issues))
	for i, is := range me.Issues {
		issues[i] = map[string]string{"path": is.Path, "message": is.Message}
	}
	return apperr.Validation("The agent cannot be serialized as a CI manifest").
		WithCode(codeManifestInvalid).
		WithDetails(map[string]any{"issues": issues})
}

// publish commits the files to the CI branch and returns the URL of the open pull request of that
// branch (reused) or of a new one (spec 7.6.7). Every GitHub failure becomes a 422
// github_permission_denied or a 502 github_unavailable; the log line carries the status only.
func (s *ExportService) publish(ctx context.Context, repo domain.ImportedRepo, slug string, runner RunnerArtifact, ex domain.Export) (string, error) {
	ref := RepoRef{Owner: repo.Owner, Name: repo.Name}
	url, err := s.commitAndOpen(ctx, ref, repo.DefaultBranch, slug, runner, ex)
	if err == nil {
		return url, nil
	}
	attrs := []any{"repo", repo.FullName}
	status := 0
	var ge *GitHubError
	if errors.As(err, &ge) {
		status = ge.Status
	}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	s.log.ErrorContext(ctx, "ci export: GitHub write failed", attrs...)
	if domain.PublishErrorCode(status) == domain.CodePermissionDenied {
		return "", apperr.Validation(domain.PermissionDeniedMessage).WithCode(string(domain.CodePermissionDenied)).WithCause(err)
	}
	return "", apperr.ExternalService(domain.UnavailableMessage).WithCode(string(domain.CodeUnavailable)).WithCause(err)
}

func (s *ExportService) commitAndOpen(ctx context.Context, ref RepoRef, base, slug string, runner RunnerArtifact, ex domain.Export) (string, error) {
	commit := make([]CommitFile, len(ex.Files))
	paths := make([]string, len(ex.Files))
	for i, f := range ex.Files {
		commit[i] = CommitFile{Path: f.Path, Contents: f.Contents, AsBlob: f.Path == domain.RunnerPath}
		paths[i] = f.Path
	}
	err := s.d.GitHub.CommitFiles(ctx, ref, CommitFilesInput{
		Branch: domain.Branch, Base: base, Message: domain.CommitMessage, Files: commit,
	})
	if err != nil {
		return "", err
	}
	open, found, err := s.d.GitHub.FindOpenPR(ctx, ref, domain.Branch)
	if err != nil {
		return "", err
	}
	if found {
		return open, nil
	}
	return s.d.GitHub.OpenPullRequest(ctx, ref, OpenPullRequestInput{
		Title: domain.PRTitle,
		Head:  domain.Branch,
		Base:  base,
		Body:  domain.BuildPRBodyFor(slug, paths, runner.SHA256, runner.BuildID, ex.RequiredSecrets, ex.RequiredVariables),
	})
}
