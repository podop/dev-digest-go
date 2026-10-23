// Command devdigest-runner is the DevDigest CI runner: a static linux/amd64 executable that the
// exported GitHub Actions workflow runs in someone else's repository to review a pull request
// with the backend's review engine. It reads secrets from the environment only, never links the
// secrets-file store, a database, Echo, git or cgo (see closure_test.go), and prices nothing.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"dev-digest-go/backend/internal/ci/runner/adapters/env"
	"dev-digest-go/backend/internal/ci/runner/adapters/ghapi"
	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/platform/keynames"
	"dev-digest-go/backend/internal/reviewer/adapters/llmcall"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
)

// buildID is the source commit (12 hex) the image build stamps with
// -ldflags "-X main.buildID=<id>"; "dev" for any other build.
var buildID = "dev"

// endpoints are the services the runner talks to. They are fixed in production; tests point them
// at httptest servers.
type endpoints struct {
	OpenRouter string // API root including /v1
	GitHub     string // API root
}

func defaultEndpoints() endpoints {
	return endpoints{OpenRouter: openaicompat.DefaultOpenRouterBaseURL, GitHub: ghapi.DefaultBaseURL}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := withCommandsStopped(os.Stdout, func() int {
		return run(ctx, os.LookupEnv, os.Stdout, os.Stderr, defaultEndpoints())
	})
	stop()
	os.Exit(code)
}

// withCommandsStopped runs body with workflow-command processing switched off for the job log
// (security r1 F2): the runner prints text it does not control (the model name from the manifest,
// warnings, failure texts), and GitHub Actions would act on a "##[...]" or "::..." command in it.
// The stop token is random and unknown to anything that feeds the runner, and the resume line is
// printed however body ends. The runner itself prints no workflow command.
func withCommandsStopped(stdout io.Writer, body func() int) int {
	token := rand.Text()
	_, _ = io.WriteString(stdout, "::stop-commands::"+token+"\n")
	defer func() { _, _ = io.WriteString(stdout, "::"+token+"::\n") }()
	return body()
}

// commandSafeWriter breaks "##[" and "::" in everything written through it. It guards the one
// stream the runner does not print itself: the model layer's log lines.
type commandSafeWriter struct{ w io.Writer }

func (c commandSafeWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(c.w, runnerapp.BreakCommands(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// envKeys reads the two names the runner knows from the environment; everything else is unset.
type envKeys struct{ getenv func(string) string }

func (k envKeys) Get(name string) string {
	switch name {
	case keynames.OpenRouterKey, keynames.GitHubToken:
		return k.getenv(name)
	}
	return ""
}

// noPrices is the runner's price book: none. The cost of a call is only what OpenRouter reports.
type noPrices struct{}

func (noPrices) Estimate(string, int, int) *float64 { return nil }

// newClient builds the LLM client of the runner: OpenRouter only, keys from the environment, no
// price book, warnings and errors on stderr.
func newClient(getenv func(string) string, stderr io.Writer, ep endpoints) *llm.Client {
	return llm.New(llm.Options{
		Keys: envKeys{getenv: getenv},
		Adapters: map[llm.Provider]llm.Adapter{
			llm.OpenRouter: openaicompat.NewOpenRouter(openaicompat.Options{BaseURL: ep.OpenRouter}),
		},
		Logger: slog.New(slog.NewTextHandler(commandSafeWriter{w: stderr}, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Prices: noPrices{},
	})
}

// reviewLLM binds the engine's LLM port to the client for one model: OpenRouter, no session id.
type reviewLLM struct{ client *llm.Client }

func (r reviewLLM) For(model string) reviewerapp.LLM {
	return llmcall.New(llmcall.Config{Client: r.client, Provider: llm.OpenRouter, Model: model, RunID: "ci-runner"})
}

// run is the runner. It returns the process exit code. The review is the one engine local reviews
// and evals run (reviewer/app.Run), over the OpenRouter client of newClient.
func run(ctx context.Context, lookup func(string) (string, bool), stdout, stderr io.Writer, ep endpoints) (code int) {
	getenv := func(name string) string { v, _ := lookup(name); return v }
	out := env.NewOut(stdout, stderr)
	defer func() {
		// A panic is a bug, not a review result; it still must not leave a key in the job log.
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			for _, name := range []string{keynames.OpenRouterKey, keynames.GitHubToken} {
				if v := getenv(name); len(v) >= 8 {
					msg = strings.ReplaceAll(msg, v, "[REDACTED]")
				}
			}
			out.Stderr(runnerapp.LogLine("[agent-runner] fatal error: ", msg))
			code = runnerapp.ExitFailed
		}
	}()

	client := newClient(getenv, stderr, ep)
	defer client.Close()
	return runnerapp.Run(ctx, runnerapp.Config{BuildID: buildID}, runnerapp.Deps{
		Env:    env.Process{LookupEnv: lookup},
		Files:  env.Files{},
		GitHub: ghapi.New(ghapi.Options{BaseURL: ep.GitHub}),
		LLM:    reviewLLM{client: client},
		Out:    out,
	})
}
