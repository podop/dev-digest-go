package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/health"
	"dev-digest-go/backend/internal/jobs"
	jobspg "dev-digest-go/backend/internal/jobs/adapters/postgres"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/logging"
	"dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/migrate"
	"dev-digest-go/backend/internal/platform/secrets"
	pullsgh "dev-digest-go/backend/internal/pulls/adapters/github"
	pullspg "dev-digest-go/backend/internal/pulls/adapters/postgres"
	pullsapp "dev-digest-go/backend/internal/pulls/app"
	pullshttp "dev-digest-go/backend/internal/pulls/transport/http"
	reposdb "dev-digest-go/backend/internal/repos/adapters/postgres"
	reposapp "dev-digest-go/backend/internal/repos/app"
	reposhttp "dev-digest-go/backend/internal/repos/transport/http"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
	settingspg "dev-digest-go/backend/internal/settings/adapters/postgres"
	"dev-digest-go/backend/internal/settings/adapters/providers"
	settingsapp "dev-digest-go/backend/internal/settings/app"
	settingshttp "dev-digest-go/backend/internal/settings/transport/http"
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
}

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
	e, err := wire(cfg, o, db, sqlDB, runner, log)
	if err != nil {
		return err
	}
	return listen(ctx, cfg, o, e, runner, log)
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

// wire builds the adapters, use cases, job handlers and routes.
func wire(cfg config.Config, o options, db *gorm.DB, sqlDB *sql.DB, runner *jobs.Runner, log *slog.Logger) (*echo.Echo, error) {
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
	resolver := tenancy.NewResolver(tenancypg.NewStore(db))

	settingsSvc := settingsapp.NewService(settingsapp.Deps{
		Store:     settingspg.NewStore(db),
		Secrets:   sec,
		GitHub:    gh,
		Providers: providers.New(o.providers),
	})
	reposStore := reposdb.NewStore(db)
	reposSvc := reposapp.NewService(reposStore, runner, git.Root())
	repoJobs := reposapp.NewJobs(reposStore, git, reposapp.JobsOptions{RemoteBase: o.remoteBase, Log: log})
	runner.Register(reposapp.KindClone, repoJobs.Clone)
	runner.Register(reposapp.KindRefresh, repoJobs.Refresh)
	pullsSvc := pullsapp.NewService(pullspg.NewStore(db), pullsgh.New(gh), log, nil)

	e := httpx.New(httpx.Options{
		Log:         log,
		CORSOrigins: cfg.CORSOrigins,
		RateLimit: httpx.RateLimit{
			Enabled: cfg.RateLimitEnabled,
			Buckets: settingshttp.RateLimitBuckets(),
		},
	})
	health.AddRoutes(e, sqlDB, log)
	settingshttp.AddRoutes(e, settingshttp.NewHandler(settingsSvc, resolver))
	reposhttp.AddRoutes(e, reposSvc, resolver)
	pullshttp.AddRoutes(e, pullsSvc, resolver)
	return e, nil
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
