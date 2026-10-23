package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	agentsdb "dev-digest-go/backend/internal/agents/adapters/postgres"
	agentsapp "dev-digest-go/backend/internal/agents/app"
	agentshttp "dev-digest-go/backend/internal/agents/transport/http"
	blastpeers "dev-digest-go/backend/internal/blast/adapters/peers"
	blastpg "dev-digest-go/backend/internal/blast/adapters/postgres"
	blastapp "dev-digest-go/backend/internal/blast/app"
	blasthttp "dev-digest-go/backend/internal/blast/transport/http"
	briefpeers "dev-digest-go/backend/internal/brief/adapters/peers"
	briefpg "dev-digest-go/backend/internal/brief/adapters/postgres"
	briefapp "dev-digest-go/backend/internal/brief/app"
	briefhttp "dev-digest-go/backend/internal/brief/transport/http"
	cipeers "dev-digest-go/backend/internal/ci/adapters/peers"
	cipg "dev-digest-go/backend/internal/ci/adapters/postgres"
	"dev-digest-go/backend/internal/ci/adapters/runnerbin"
	"dev-digest-go/backend/internal/ci/adapters/zipentry"
	ciapp "dev-digest-go/backend/internal/ci/app"
	cihttp "dev-digest-go/backend/internal/ci/transport/http"
	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/conventions/adapters/clonefiles"
	conventionspg "dev-digest-go/backend/internal/conventions/adapters/postgres"
	conventionsapp "dev-digest-go/backend/internal/conventions/app"
	conventionshttp "dev-digest-go/backend/internal/conventions/transport/http"
	evalspeers "dev-digest-go/backend/internal/evals/adapters/peers"
	evalspg "dev-digest-go/backend/internal/evals/adapters/postgres"
	evalsapp "dev-digest-go/backend/internal/evals/app"
	evalsexec "dev-digest-go/backend/internal/evals/app/executor"
	evalshttp "dev-digest-go/backend/internal/evals/transport/http"
	"dev-digest-go/backend/internal/health"
	intentpeers "dev-digest-go/backend/internal/intent/adapters/peers"
	intentpg "dev-digest-go/backend/internal/intent/adapters/postgres"
	intentapp "dev-digest-go/backend/internal/intent/app"
	intenthttp "dev-digest-go/backend/internal/intent/transport/http"
	"dev-digest-go/backend/internal/jobs"
	jobspg "dev-digest-go/backend/internal/jobs/adapters/postgres"
	lbpg "dev-digest-go/backend/internal/leaderboard/adapters/postgres"
	lbapp "dev-digest-go/backend/internal/leaderboard/app"
	leaderboardhttp "dev-digest-go/backend/internal/leaderboard/transport/http"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/anthropic"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	onbpeers "dev-digest-go/backend/internal/onboarding/adapters/peers"
	onbpg "dev-digest-go/backend/internal/onboarding/adapters/postgres"
	onbapp "dev-digest-go/backend/internal/onboarding/app"
	onboardinghttp "dev-digest-go/backend/internal/onboarding/transport/http"
	perfpg "dev-digest-go/backend/internal/performance/adapters/postgres"
	perfapp "dev-digest-go/backend/internal/performance/app"
	performancehttp "dev-digest-go/backend/internal/performance/transport/http"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/inflight"
	"dev-digest-go/backend/internal/platform/logging"
	"dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/migrate"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/platform/runbus"
	"dev-digest-go/backend/internal/platform/secrets"
	pcclone "dev-digest-go/backend/internal/projectcontext/adapters/clonedocs"
	pcpg "dev-digest-go/backend/internal/projectcontext/adapters/postgres"
	pcapp "dev-digest-go/backend/internal/projectcontext/app"
	pchttp "dev-digest-go/backend/internal/projectcontext/transport/http"
	pullsgh "dev-digest-go/backend/internal/pulls/adapters/github"
	pullspg "dev-digest-go/backend/internal/pulls/adapters/postgres"
	pullsapp "dev-digest-go/backend/internal/pulls/app"
	pullshttp "dev-digest-go/backend/internal/pulls/transport/http"
	intelclone "dev-digest-go/backend/internal/repointel/adapters/clonetree"
	intelpg "dev-digest-go/backend/internal/repointel/adapters/postgres"
	inteltokens "dev-digest-go/backend/internal/repointel/adapters/tokenizer"
	inteltreesitter "dev-digest-go/backend/internal/repointel/adapters/treesitter"
	intelapp "dev-digest-go/backend/internal/repointel/app"
	inteldomain "dev-digest-go/backend/internal/repointel/domain"
	intelhttp "dev-digest-go/backend/internal/repointel/transport/http"
	reposdb "dev-digest-go/backend/internal/repos/adapters/postgres"
	reposapp "dev-digest-go/backend/internal/repos/app"
	reposhttp "dev-digest-go/backend/internal/repos/transport/http"
	"dev-digest-go/backend/internal/reviewer/adapters/llmcall"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	"dev-digest-go/backend/internal/reviews/adapters/peers"
	revpg "dev-digest-go/backend/internal/reviews/adapters/postgres"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/app/executor"
	reviewshttp "dev-digest-go/backend/internal/reviews/transport/http"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
	settingspg "dev-digest-go/backend/internal/settings/adapters/postgres"
	"dev-digest-go/backend/internal/settings/adapters/providers"
	settingsapp "dev-digest-go/backend/internal/settings/app"
	settingshttp "dev-digest-go/backend/internal/settings/transport/http"
	"dev-digest-go/backend/internal/skills/adapters/catalog"
	skillspg "dev-digest-go/backend/internal/skills/adapters/postgres"
	"dev-digest-go/backend/internal/skills/adapters/urlfetch"
	skillsapp "dev-digest-go/backend/internal/skills/app"
	skillshttp "dev-digest-go/backend/internal/skills/transport/http"
	smartdiffpg "dev-digest-go/backend/internal/smartdiff/adapters/postgres"
	smartdiffapp "dev-digest-go/backend/internal/smartdiff/app"
	smartdiffhttp "dev-digest-go/backend/internal/smartdiff/transport/http"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
	"dev-digest-go/backend/migrations"
)

// httpGrace is how long in-flight requests may finish after a stop signal (FR43).
const httpGrace = 20 * time.Second

// generationDrain is how long the intent, brief and onboarding generations may take to return
// after their cancellation, at shutdown.
const generationDrain = 5 * time.Second

// options are the seams the component tests use to point serve at stubs. Production passes
// only the process environment and stdout.
type options struct {
	getenv        func(string) string
	logOut        io.Writer
	githubBaseURL string            // "" = api.github.com
	providers     providers.Options // zero = the real provider endpoints
	remoteBase    string            // "" = https://github.com; tests use a file:// directory
	gitBinary     string            // "" = git from PATH
	jobs          jobs.Options      // Logger is always set by serve
	onListen      func(net.Addr)    // called once the listener is up

	// llmURLs point the three provider adapters at stubs; "" = the real endpoint. Ignored with
	// LLM_PROVIDER_OVERRIDE=mock.
	llmURLs llmURLs
	// llmAdapters replaces the provider adapters outright (scripted providers that block or ignore
	// cancellation); nil = the adapters llmAdapters builds from llmURLs.
	llmAdapters map[llm.Provider]llm.Adapter
	// llm tunes the LLM client (retry backoff, budget, price book); Keys, Adapters and Logger are
	// always set by wire.
	llm llm.Options
	// urlfetch tunes the skill URL importer (resolver, dialer, TLS roots); zero = production.
	urlfetch urlfetch.Options
	// extractTimeout is the per-attempt limit of conventions.extract; 0 = conventionsapp.JobTimeout.
	extractTimeout time.Duration
	// noRepoIntelJobs leaves the three repo-intel job kinds unregistered, so a resync cannot be
	// enqueued (the degraded no_handler answer) and the clone and refresh triggers log a warning.
	noRepoIntelJobs bool
	// now is the clock of the Phase 5 services (derived_at, generated_at, the performance windows);
	// nil = time.Now.
	now func() time.Time
	// intentBudget, briefBudget and onboardingBudget bound one intent derivation, brief generation
	// and tour generation; 0 = each domain's own budget (120 s).
	intentBudget, briefBudget, onboardingBudget time.Duration
	// evalCaseBudget bounds one eval case; 0 = the domain's budget (120 s).
	evalCaseBudget time.Duration
	// runner is the CI runner the export ships; nil = runnerbin.Default(), the runner embedded by the
	// image build (a build without the devdigest_runner tag has none and answers runner_bundle_missing).
	runner ciapp.RunnerBundle
	// artifactClient fetches the storage leg of a CI artifact download; nil = the GitHub client's
	// default (tests point it at a TLS stub).
	artifactClient *http.Client
	// pollTick is how often the background PR poll checks which workspaces are due; 0 = the poller's
	// own default (1 min). Tests shorten it and move the clock (now) instead of waiting.
	pollTick time.Duration
}

// llmURLs are the base URLs of the provider adapters.
type llmURLs struct{ openAI, anthropic, openRouter string }

type option func(*options)

func withGetenv(getenv func(string) string) option { return func(o *options) { o.getenv = getenv } }
func withLogOutput(w io.Writer) option             { return func(o *options) { o.logOut = w } }

// serve runs the API until ctx ends: migrate, seed, start jobs, listen; on stop it cancels the live
// review and eval runs (up to 5 s to record themselves) in parallel with the job runner (up to 10 s), then
// drains HTTP (up to 20 s) and closes the database.
func serve(ctx context.Context, cfg config.Config, opts ...option) error {
	o := options{getenv: func(string) string { return "" }, logOut: io.Discard}
	for _, opt := range opts {
		opt(&o)
	}
	log := logging.New(o.logOut, cfg.LogLevel)

	db, err := postgres.Open(ctx, cfg.DatabaseURL, log, postgres.WithMaxOpenConns(cfg.DBMaxOpenConns))
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := startupDB(ctx, cfg, db, sqlDB, log); err != nil {
		return err
	}

	o.jobs.Logger = log
	runner := jobs.New(jobspg.NewStore(db), o.jobs)
	w, err := wire(cfg, o, db, sqlDB, runner, log)
	if err != nil {
		return err
	}
	defer w.llm.Close()

	// Scans left running by a crash or a hard stop fail before the port accepts connections
	// (AC264). A failing reap does not stop the API: with DB_AUTO_MIGRATE=false on an unmigrated
	// database it still has to serve /health (Phase 1 AC33).
	if failed, err := w.conventions.FailInterrupted(ctx); err != nil {
		log.ErrorContext(ctx, "interrupted convention scans not failed", "error", err)
	} else if failed > 0 {
		log.WarnContext(ctx, "interrupted convention scans failed", "count", failed)
	}
	// Review runs left running by a crash or a hard stop fail the same way; the use case logs the
	// count. Non-fatal for the same reason (Phase 1 AC33, EC4034).
	if _, err := w.reviews.FailInterrupted(ctx); err != nil {
		log.ErrorContext(ctx, "interrupted review runs not failed", "error", err)
	}
	// Eval runs left running by a crash fail as "interrupted"; the use case logs the count. Non-fatal
	// for the same reason.
	if _, err := w.evalRuns.FailInterrupted(ctx); err != nil {
		log.ErrorContext(ctx, "interrupted eval runs not failed", "error", err)
	}
	// Repos cloned before repo-intel existed get one index job each (D3005). Like the reap above,
	// a failure is logged once and startup goes on (FR3008, Phase 1 AC33).
	if n, err := w.backfill.Run(ctx); err != nil {
		log.ErrorContext(ctx, "repo-intel backfill failed", "error", err)
	} else if n > 0 {
		log.InfoContext(ctx, "repo-intel backfill enqueued", "count", n)
	}
	return listen(ctx, cfg, o, w, runner, log)
}

// startupDB applies pending migrations and then the seed, each unless switched off (FR18).
func startupDB(ctx context.Context, cfg config.Config, db *gorm.DB, sqlDB *sql.DB, log *slog.Logger) error {
	if cfg.AutoMigrate {
		m, err := migrate.New(sqlDB, migrations.FS, log)
		if err != nil {
			log.ErrorContext(ctx, "startup failed", "step", "migrate", "error", err)
			return fmt.Errorf("migrate: %w", err)
		}
		if _, err := m.Up(ctx); err != nil {
			log.ErrorContext(ctx, "startup failed", "step", "migrate", "error", err)
			return err
		}
	}
	if cfg.AutoSeed {
		if _, err := seed.New(seedpg.NewStore(db), log).Run(ctx); err != nil {
			log.ErrorContext(ctx, "startup failed", "step", "seed", "error", err)
			return err
		}
	}
	return nil
}

// wired is what wire hands back to serve.
type wired struct {
	echo        *echo.Echo
	llm         *llm.Client // closed by serve on exit
	conventions *conventionsapp.Service
	backfill    *intelapp.Backfill
	reviews     reviewsapp.Lifecycle
	executor    *executor.Executor  // shut down by listen before HTTP drains
	evalRuns    evalsapp.Runs       // reaps the eval runs a crash left running
	evalExec    *evalsexec.Executor // shut down by listen next to the review executor
	// intent, brief and onboarding run generations on detached contexts; listen stops them before
	// HTTP drains, because Echo never cancels the context of a request in flight (P2).
	intent     intentapp.Stopper
	brief      briefapp.Lifecycle
	onboarding onbapp.Lifecycle
	// poller is the background PR poll; nil when PR_POLL_ENABLED=false. listen runs it on the stop
	// context and waits for it (≤ 5 s) before serve closes the database.
	poller *pullsapp.Poller
}

// intelHandlers are the three repo-intel job handlers.
type intelHandlers struct{ index, refresh, resync jobs.Handler }

// registerJobs binds the job kinds. conventions.extract gets its own per-attempt limit (D208);
// clone and refresh keep the runner's default (AC303); the repo-intel kinds run under 120 s
// (FR3005). A nil intel leaves the repo-intel kinds unregistered.
func registerJobs(r *jobs.Runner, clone, refresh, extract jobs.Handler, extractTimeout time.Duration, intel *intelHandlers) {
	r.Register(reposapp.KindClone, clone)
	r.Register(reposapp.KindRefresh, refresh)
	r.Register(conventionsapp.KindExtract, extract, jobs.WithTimeout(extractTimeout))
	if intel == nil {
		return
	}
	r.Register(inteldomain.KindIndex, intel.index, jobs.WithTimeout(inteldomain.JobTimeout))
	r.Register(inteldomain.KindRefresh, intel.refresh, jobs.WithTimeout(inteldomain.JobTimeout))
	r.Register(inteldomain.KindResync, intel.resync, jobs.WithTimeout(inteldomain.JobTimeout))
}

// llmAdapters builds the provider adapters; with LLM_PROVIDER_OVERRIDE=mock every provider is a
// deterministic mock and startup logs one warning (D214).
func llmAdapters(cfg config.Config, urls llmURLs, log *slog.Logger) map[llm.Provider]llm.Adapter {
	ads := map[llm.Provider]llm.Adapter{
		llm.OpenAI:     openaicompat.NewOpenAI(openaicompat.Options{BaseURL: urls.openAI}),
		llm.Anthropic:  anthropic.New(anthropic.Options{BaseURL: urls.anthropic}),
		llm.OpenRouter: openaicompat.NewOpenRouter(openaicompat.Options{BaseURL: urls.openRouter}),
	}
	if cfg.LLMProviderOverride == config.LLMProviderMock {
		for p := range ads {
			ads[p] = mock.New(p, mock.Options{Delay: cfg.LLMMockDelay})
		}
		log.Warn("LLM_PROVIDER_OVERRIDE=mock: every LLM provider is replaced by a deterministic mock",
			"mock_delay_ms", cfg.LLMMockDelay.Milliseconds())
	}
	return ads
}

// wire builds the adapters, use cases, job handlers and routes.
func wire(cfg config.Config, o options, db *gorm.DB, sqlDB *sql.DB, runner *jobs.Runner, log *slog.Logger) (*wired, error) {
	sec := secrets.New(filepath.Join(cfg.DataDir, secrets.FileName), o.getenv)
	githubToken := func() string { return sec.Get(secrets.GitHubToken) }
	gh := github.New(github.Options{
		BaseURL:        o.githubBaseURL,
		Token:          githubToken,
		ArtifactClient: o.artifactClient,
	})
	git, err := gitcli.New(gitcli.Options{
		Binary:    o.gitBinary,
		CloneRoot: cfg.CloneDir,
		Token:     func(context.Context) (string, error) { return sec.Get(secrets.GitHubToken), nil },
	})
	if err != nil {
		return nil, fmt.Errorf("git: %w", err)
	}
	// The constructors that can fail come before the LLM client, so an error never leaks its
	// price-refresh goroutine.
	cat, err := catalog.New()
	if err != nil {
		return nil, fmt.Errorf("skill catalog: %w", err)
	}
	pcStore := pcpg.NewStore(db)
	tx := postgres.NewTx(db)
	pcSvc, err := pcapp.NewService(pcStore, pcclone.Docs{}, tx, cfg.ProjectContextGlobs, log)
	if err != nil {
		return nil, fmt.Errorf("project context: %w", err)
	}
	resolver := tenancy.NewResolver(tenancypg.NewStore(db))
	now := o.now
	if now == nil {
		now = time.Now
	}

	llmOpts := o.llm
	llmOpts.Keys = sec
	llmOpts.Adapters = o.llmAdapters
	if llmOpts.Adapters == nil {
		llmOpts.Adapters = llmAdapters(cfg, o.llmURLs, log)
	}
	llmOpts.Logger = log
	llmClient := llm.New(llmOpts)

	settingsSvc := settingsapp.NewService(settingsapp.Deps{
		Store:     settingspg.NewStore(db),
		Secrets:   sec,
		GitHub:    gh,
		Providers: providers.New(o.providers),
	})
	intelStore := intelpg.NewStore(db)
	intelFiles := intelclone.New()
	intelAnalyzer := inteltreesitter.New()
	intelJobs := intelapp.NewJobs(intelapp.JobsDeps{
		Indexer: intelapp.NewIndexer(intelapp.IndexerDeps{
			Reader:   intelStore,
			Writer:   intelStore,
			Tx:       tx,
			Git:      git,
			Files:    intelFiles,
			Analyzer: intelAnalyzer,
			Graph:    intelapp.NewImportGraph(intelFiles, intelAnalyzer, log),
			Tokens:   inteltokens.New(log),
			Redact:   llmClient,
		}, intelapp.IndexerOptions{Log: log}),
		Repos:  intelStore,
		Git:    git,
		Queue:  runner,
		Redact: llmClient,
	}, intelapp.JobsOptions{Log: log})
	intelSvc := intelapp.NewService(intelStore, runner, log)
	// The caller scan is what the review prompt's callers digest runs on; the conventions ranker
	// uses the same reader.
	intelReader := intelapp.NewReader(intelStore, cfg.RepoIntelEnabled,
		intelapp.WithCallerScan(intelapp.CallerScan{Repos: intelStore, Files: intelFiles, Analyzer: intelAnalyzer}))

	reposStore := reposdb.NewStore(db)
	reposSvc := reposapp.NewService(reposStore, runner, git.Root())
	repoJobs := reposapp.NewJobs(reposStore, git, reposapp.JobsOptions{RemoteBase: o.remoteBase, Log: log, Index: intelJobs})
	pullsStore := pullspg.NewStore(db)
	pullsSvc := pullsapp.NewService(pullsStore, pullsgh.New(gh), log, nil)

	agentsSvc := agentsapp.NewService(agentsdb.NewStore(db), tx, llmClient, log)

	skillsStore := skillspg.NewStore(db)
	skillsSvc := skillsapp.NewService(skillsStore, tx, time.Now)
	importer := skillsapp.NewImporter(urlfetch.New(o.urlfetch), cat)

	convStore := conventionspg.NewStore(db)
	convSvc := conventionsapp.NewService(convStore, convStore, runner, conventionsapp.ServiceOptions{Redact: llmClient})
	extractTimeout := o.extractTimeout
	if extractTimeout <= 0 {
		extractTimeout = conventionsapp.JobTimeout
	}
	convJobs := conventionsapp.NewJobs(conventionsapp.JobsDeps{
		Store:    convStore,
		Repos:    convStore,
		Files:    clonefiles.New(),
		LLM:      llmClient,
		Features: settingsSvc,
		Ranker:   intelReader,
		Redact:   llmClient,
	}, conventionsapp.JobsOptions{Timeout: extractTimeout, Log: log})
	var intel *intelHandlers
	if !o.noRepoIntelJobs {
		intel = &intelHandlers{index: intelJobs.Index, refresh: intelJobs.Refresh, resync: intelJobs.Resync}
	}
	registerJobs(runner, repoJobs.Clone, repoJobs.Refresh, convJobs.Extract, extractTimeout, intel)

	merger := skillsapp.NewMerger(skillsapp.MergerDeps{
		Tx:          tx,
		Skills:      skillsSvc,
		Lookup:      skillsStore,
		Agents:      agentsSvc,
		Conventions: convSvc,
	})

	// One bus serves the executor (publishing) and the service (replay, cancel, subscribe).
	runs := peers.NewBus(runbus.New(runbus.Options{}))
	reviewsStore := revpg.NewStore(db)
	// The intent service also serves GET/POST /pulls/:id/intent[/refresh]. The review hook is wired
	// only while REVIEW_INTENT_ENABLED holds: a nil interface is the switch off (P5, D5001).
	intentStore := intentpg.NewStore(db)
	intentSvc := intentapp.NewService(intentapp.Deps{
		Pulls:    intentStore,
		Settings: settingsSvc,
		LLM:      llmClient,
		Docs:     intentpeers.NewDocs(git, gh),
		Tickets:  intentpeers.NewTickets(gh, llmClient.Redact),
		Store:    intentStore,
		Clock:    now,
		Log:      log,
	}, intentapp.Options{Budget: o.intentBudget})
	execDeps := executor.Deps{
		Store:          reviewsStore,
		Tx:             tx,
		Bus:            runs,
		Providers:      peers.NewProviders(llmClient, newLLMCaller(llmClient)),
		RepoIntel:      peers.NewRepoIntel(intelReader),
		ProjectContext: peers.NewProjectContext(pcSvc.WithGit(git)),
		Engine:         reviewerapp.Run,
		Log:            log,
	}
	if cfg.ReviewIntentEnabled {
		execDeps.Intent = peers.NewIntent(intentSvc)
	}
	reviewsExec := executor.New(execDeps, executor.Options{AgentConcurrency: cfg.ReviewAgentConcurrency, MapConcurrency: cfg.ReviewMapConcurrency})
	reviewsSvc := reviewsapp.NewService(reviewsapp.Deps{
		Store: reviewsStore, Tx: tx, Executor: reviewsExec, Bus: runs, Log: log,
	})

	blastSvc := blastapp.NewService(blastpg.NewStore(db), blastpeers.NewRepoIntel(intelReader),
		blastpeers.NewGitHub(gh), blastapp.Options{Log: log, Redact: llmClient.Redact})
	briefSvc := briefapp.NewService(briefapp.Deps{
		Store:     briefpg.NewStore(db),
		Intents:   intentSvc,
		Blast:     briefpeers.NewBlast(blastSvc),
		Documents: briefpeers.NewDocuments(reviewsStore, peers.NewProjectContext(pcSvc.WithGit(git))),
		Issues:    briefpeers.NewIssues(gh),
		Roles:     briefpeers.Roles{},
		LLM:       llmClient,
		Models:    settingsSvc,
		Clock:     now,
		Log:       log,
		Budget:    o.briefBudget,
	})
	onbSvc := onbapp.NewService(onbapp.Deps{
		Store:       onbpg.NewStore(db),
		IndexStates: onbpeers.NewIndexStates(intelSvc),
		Index:       onbpeers.NewIndex(intelReader),
		Blobs:       onbpeers.NewBlobs(git),
		LLM:         llmClient,
		Models:      settingsSvc,
		Now:         now,
		Log:         log,
		Budget:      o.onboardingBudget,
	})

	// Evals: the executor is the launcher of the run service; its engine and LLM port are the
	// review engine's, bound to the run's own provider and model.
	evalsStore := evalspg.NewStore(db)
	evalExec := evalsexec.New(evalsexec.Deps{
		Store:     evalsStore,
		Providers: evalspeers.NewProviders(llmClient, newEvalLLMCaller(llmClient)),
		Engine:    reviewerapp.Run,
		Clock:     now,
		Log:       log,
	}, evalsexec.Options{CaseBudget: o.evalCaseBudget})
	evalCases := evalsapp.NewCaseService(evalsStore, evalsStore, now, log)
	evalRuns := evalsapp.NewRunService(evalsapp.RunDeps{
		Cases: evalsStore, Runs: evalsStore, Snapshots: evalsStore, Launcher: evalExec, Now: now, Log: log,
	})

	// CI export and ingest: the directories read agents, skills and repos through their use cases.
	ciAgents := cipeers.NewAgents(agentsSvc, skillsSvc)
	ciStore := cipg.NewStore(db)
	ciGitHub := cipeers.NewGitHub(gh, githubToken)
	runnerBundle := o.runner
	if runnerBundle == nil {
		runnerBundle = runnerbin.Default()
	}
	ciExport := ciapp.NewExportService(ciapp.ExportDeps{
		Agents: ciAgents, Repos: cipeers.NewRepos(reposSvc), Store: ciStore, Runner: runnerBundle, GitHub: ciGitHub, Log: log,
	})
	ciIngest := ciapp.NewIngestService(ciapp.IngestDeps{
		Agents: ciAgents, Store: ciStore, GitHub: ciGitHub, Zip: zipentry.New(), Clock: now, Log: log,
	})

	limits := skillshttp.BodyLimits()
	maps.Copy(limits, pchttp.BodyLimits())
	e := httpx.New(httpx.Options{
		Log:          log,
		CORSOrigins:  cfg.CORSOrigins,
		AllowedHosts: cfg.AllowedHosts,
		BodyLimits:   limits,
		RateLimit: httpx.RateLimit{
			Enabled: cfg.RateLimitEnabled,
			Buckets: slices.Concat(settingshttp.RateLimitBuckets(),
				[]httpx.Bucket{
					{Method: http.MethodPost, Path: "/pulls/:id/review", Limit: 10},
					{Method: http.MethodPost, Path: intenthttp.RefreshPath, Limit: 10},
				},
				// The two eval run starts share one counter per workspace; run-all counts per IP.
				evalshttp.RateLimitBuckets()),
			// A live run stream is long-lived and reconnects on its own: it never counts.
			Exempt: []httpx.Route{{Method: http.MethodGet, Path: "/runs/:id/events"}},
		},
	})
	health.AddRoutes(e, sqlDB, log)
	settingshttp.AddRoutes(e, settingshttp.NewHandler(settingsSvc, resolver))
	reposhttp.AddRoutes(e, reposSvc, resolver)
	pullshttp.AddRoutes(e, pullsSvc, resolver)
	agentshttp.AddRoutes(e, agentsSvc, resolver)
	// Also registers POST /repos/:id/conventions/skill; conventionshttp does not.
	skillshttp.AddRoutes(e, skillsSvc, importer, merger, resolver)
	conventionshttp.AddRoutes(e, convSvc, resolver)
	pchttp.AddRoutes(e, pcSvc, resolver)
	intelhttp.AddRoutes(e, intelSvc, resolver)
	reviewshttp.AddRoutes(e, reviewsSvc, resolver)
	intenthttp.AddRoutes(e, intentSvc, resolver)
	smartdiffhttp.AddRoutes(e, smartdiffapp.NewService(smartdiffpg.NewStore(db)), resolver)
	blasthttp.AddRoutes(e, blastSvc, resolver)
	briefhttp.AddRoutes(e, briefSvc, resolver)
	onboardinghttp.AddRoutes(e, onbSvc, resolver)
	// GET /agents/performance is a static route, so it wins over GET /agents/:id.
	performancehttp.AddRoutes(e, perfapp.NewService(perfpg.NewStore(db), now), resolver)
	evalshttp.AddRoutes(e, evalCases, evalRuns, resolver)
	cihttp.AddRoutes(e, ciExport, ciIngest, resolver)
	leaderboardhttp.AddRoutes(e, lbapp.NewService(lbpg.NewStore(db), now), resolver)

	// The background PR poll: its GitHub client is its own and never retries, so a repo poll stays
	// within its page bound (the shared client retries transient failures). The token is read from
	// the same source as the shared client's, and the interval from each workspace's Settings.
	var poller *pullsapp.Poller
	if cfg.PRPollEnabled {
		pages := github.New(github.Options{BaseURL: o.githubBaseURL, Token: githubToken, Retry: &retry.Options{}})
		pollGH := pullsgh.New(pages)
		poller = pullsapp.NewPoller(pullsapp.PollerDeps{
			Targets:   pullsStore,
			Intervals: settingsIntervals{settings: settingsSvc},
			Pages:     pollGH,
			Stats:     pollGH,
			Store:     pullsStore,
			Log:       log,
			Now:       now,
		}, pullsapp.PollerOptions{Tick: o.pollTick})
	}
	return &wired{
		echo: e, llm: llmClient, conventions: convSvc, backfill: intelapp.NewBackfill(intelStore, intelJobs),
		reviews: reviewsSvc, executor: reviewsExec, evalRuns: evalRuns, evalExec: evalExec, intent: intentSvc, brief: briefSvc, onboarding: onbSvc,
		poller: poller,
	}, nil
}

// listen serves w.echo until ctx ends. On stop the review executor shuts down first (every live
// run is cancelled, up to 5 s to record itself, then every run bus completes so the SSE streams
// end) in parallel with the eval executor (the same drain), the intent, brief and onboarding
// generations (cancelled, up to 5 s to return) and the job runner (10 s); only then does Echo get its stop signal and drain
// the remaining requests (20 s grace, FR43, FR4042). The background PR poll (when enabled) runs on
// the stop context and returns within 5 s of it, next to the job runner; serve closes the database
// only after listen has waited for it. Echo runs on its own context for that
// reason: the signal context would stop it before the runs could finish. A server that cannot
// drain within the grace period makes the process fail; runs and jobs that do not settle in time
// keep their `running` rows (reaped at the next start) and only produce a warning.
func listen(ctx context.Context, cfg config.Config, o options, w *wired, runner *jobs.Runner, log *slog.Logger) error {
	// stopCtx ends on the stop signal, or when the server stopped by itself (port in use).
	stopCtx, stop := context.WithCancel(ctx)
	defer stop()
	// The values of ctx stay; its cancellation does not reach Echo.
	echoCtx, stopEcho := context.WithCancel(context.WithoutCancel(ctx))
	defer stopEcho()

	var wg sync.WaitGroup
	wg.Go(func() {
		<-stopCtx.Done()
		// Echo never cancels the context of a request in flight, so the generations of the
		// intent, brief and onboarding requests are stopped here, together with the review runs
		// and before Echo gets its stop signal (P2, NFR5010).
		var stopping sync.WaitGroup
		stopping.Go(func() {
			if err := w.executor.Shutdown(context.WithoutCancel(ctx)); err != nil {
				if errors.Is(err, executor.ErrNotDrained) {
					log.Warn("review runs did not finish recording; the next start reaps them", "error", err)
				} else {
					log.Error("review executor shutdown", "error", err)
				}
			}
		})
		stopping.Go(func() {
			if err := w.evalExec.Shutdown(context.WithoutCancel(ctx)); err != nil {
				if errors.Is(err, evalsexec.ErrNotDrained) {
					log.Warn("eval runs did not finish recording; the next start reaps them", "error", err)
				} else {
					log.Error("eval executor shutdown", "error", err)
				}
			}
		})
		for name, stopper := range map[string]func(context.Context) error{
			"intent derivations":     w.intent.Shutdown,
			"brief generations":      w.brief.Shutdown,
			"onboarding generations": w.onboarding.Shutdown,
		} {
			stopping.Go(func() {
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), generationDrain)
				defer cancel()
				if err := stopper(sctx); err != nil {
					if errors.Is(err, inflight.ErrNotDrained) {
						log.Warn("generations did not return before the deadline", "service", name, "error", err)
					} else {
						log.Error("generations shutdown", "service", name, "error", err)
					}
				}
			})
		}
		stopping.Wait()
		stopEcho()
	})
	if w.poller != nil {
		log.Info("pull request poller started")
		wg.Go(func() { w.poller.Run(stopCtx) })
	} else {
		log.Info("pull request poller disabled (PR_POLL_ENABLED=false)")
	}
	wg.Go(func() {
		<-stopCtx.Done()
		if err := runner.Shutdown(jobs.DefaultShutdownTimeout); err != nil {
			log.Warn("job runner did not settle", "error", err)
		}
	})

	var drainErr error
	sc := echo.StartConfig{
		Address:         cfg.Addr,
		HideBanner:      true,
		HidePort:        true,
		GracefulTimeout: httpGrace,
		ListenerAddrFunc: func(addr net.Addr) {
			log.InfoContext(ctx, "api listening", "addr", addr.String())
			if o.onListen != nil {
				o.onListen(addr)
			}
		},
		OnShutdownError: func(err error) { drainErr = err },
		BeforeServeFunc: func(s *http.Server) error {
			s.ReadHeaderTimeout = 5 * time.Second
			s.IdleTimeout = 120 * time.Second
			return nil
		},
	}
	startErr := sc.Start(echoCtx, w.echo)
	stop()
	wg.Wait()

	switch {
	case startErr != nil && !errors.Is(startErr, http.ErrServerClosed):
		return fmt.Errorf("serve: %w", startErr)
	case drainErr != nil:
		return fmt.Errorf("shutdown: %w", drainErr)
	}
	log.Info("api stopped")
	return nil
}

// newLLMCaller binds the review engine's LLM adapter to the LLM client for the reviews context's
// provider resolver: peers does not import the reviewer adapters, this composition root does.
func newLLMCaller(client llmcall.Structured) peers.CallerFactory {
	return func(c peers.CallerConfig) reviewerapp.LLM {
		return llmcall.New(llmcall.Config{
			Client: client, Provider: c.Provider, Model: c.Model, RunID: c.RunID, SessionID: c.SessionID, OnUsage: c.OnUsage,
		})
	}
}

// newEvalLLMCaller binds the review engine's LLM adapter to the LLM client for the evals context's
// provider resolver, like newLLMCaller does for reviews; a case has no session id.
func newEvalLLMCaller(client llmcall.Structured) evalspeers.CallerFactory {
	return func(c evalspeers.CallerConfig) reviewerapp.LLM {
		return llmcall.New(llmcall.Config{
			Client: client, Provider: c.Provider, Model: c.Model, RunID: c.RunID, OnUsage: c.OnUsage,
		})
	}
}
