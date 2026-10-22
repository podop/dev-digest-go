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
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	agentsdb "dev-digest-go/backend/internal/agents/adapters/postgres"
	agentsapp "dev-digest-go/backend/internal/agents/app"
	agentshttp "dev-digest-go/backend/internal/agents/transport/http"
	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/conventions/adapters/clonefiles"
	conventionspg "dev-digest-go/backend/internal/conventions/adapters/postgres"
	conventionsapp "dev-digest-go/backend/internal/conventions/app"
	conventionshttp "dev-digest-go/backend/internal/conventions/transport/http"
	"dev-digest-go/backend/internal/health"
	"dev-digest-go/backend/internal/jobs"
	jobspg "dev-digest-go/backend/internal/jobs/adapters/postgres"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/anthropic"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/logging"
	"dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/migrate"
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
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
	"dev-digest-go/backend/migrations"
)

// httpGrace is how long in-flight requests may finish after a stop signal (FR43).
const httpGrace = 20 * time.Second

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
}

// llmURLs are the base URLs of the provider adapters.
type llmURLs struct{ openAI, anthropic, openRouter string }

type option func(*options)

func withGetenv(getenv func(string) string) option { return func(o *options) { o.getenv = getenv } }
func withLogOutput(w io.Writer) option             { return func(o *options) { o.logOut = w } }

// serve runs the API until ctx ends: migrate, seed, start jobs, listen; on stop it drains HTTP
// (up to 20 s) in parallel with the job runner (up to 10 s), then closes the database.
func serve(ctx context.Context, cfg config.Config, opts ...option) error {
	o := options{getenv: func(string) string { return "" }, logOut: io.Discard}
	for _, opt := range opts {
		opt(&o)
	}
	log := logging.New(o.logOut, cfg.LogLevel)

	db, err := postgres.Open(ctx, cfg.DatabaseURL, log)
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
	// Repos cloned before repo-intel existed get one index job each (D3005). Like the reap above,
	// a failure is logged once and startup goes on (FR3008, Phase 1 AC33).
	if n, err := w.backfill.Run(ctx); err != nil {
		log.ErrorContext(ctx, "repo-intel backfill failed", "error", err)
	} else if n > 0 {
		log.InfoContext(ctx, "repo-intel backfill enqueued", "count", n)
	}
	return listen(ctx, cfg, o, w.echo, runner, log)
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
	gh := github.New(github.Options{
		BaseURL: o.githubBaseURL,
		Token:   func() string { return sec.Get(secrets.GitHubToken) },
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

	llmOpts := o.llm
	llmOpts.Keys = sec
	llmOpts.Adapters = llmAdapters(cfg, o.llmURLs, log)
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
	intelReader := intelapp.NewReader(intelStore, cfg.RepoIntelEnabled)

	reposStore := reposdb.NewStore(db)
	reposSvc := reposapp.NewService(reposStore, runner, git.Root())
	repoJobs := reposapp.NewJobs(reposStore, git, reposapp.JobsOptions{RemoteBase: o.remoteBase, Log: log, Index: intelJobs})
	pullsSvc := pullsapp.NewService(pullspg.NewStore(db), pullsgh.New(gh), log, nil)

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

	limits := skillshttp.BodyLimits()
	maps.Copy(limits, pchttp.BodyLimits())
	e := httpx.New(httpx.Options{
		Log:          log,
		CORSOrigins:  cfg.CORSOrigins,
		AllowedHosts: cfg.AllowedHosts,
		BodyLimits:   limits,
		RateLimit: httpx.RateLimit{
			Enabled: cfg.RateLimitEnabled,
			Buckets: settingshttp.RateLimitBuckets(),
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
	return &wired{echo: e, llm: llmClient, conventions: convSvc, backfill: intelapp.NewBackfill(intelStore, intelJobs)}, nil
}

// listen serves e until ctx ends, then shuts HTTP and the job runner down in parallel. A
// server that cannot drain within the grace period makes the process fail (FR43); jobs that
// do not settle in time keep their `running` rows and only produce a warning.
func listen(ctx context.Context, cfg config.Config, o options, e *echo.Echo, runner *jobs.Runner, log *slog.Logger) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Go(func() {
		<-runCtx.Done()
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
	startErr := sc.Start(runCtx, e)
	cancel()
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
