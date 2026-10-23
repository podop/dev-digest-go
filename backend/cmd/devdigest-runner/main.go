// Command devdigest-runner is the DevDigest CI runner: a static linux/amd64 executable that the
// exported GitHub Actions workflow runs in someone else's repository to review a pull request
// with the backend's review engine. It reads secrets from the environment only, never links the
// secrets-file store, a database, Echo, git or cgo (see closure_test.go), and prices nothing.
//
// This file is the build smoke of the runner (spec 7.8.1): the LLM layer and the review engine
// are wired, the review itself is added by the runner steps.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

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
	return endpoints{OpenRouter: openaicompat.DefaultOpenRouterBaseURL, GitHub: "https://api.github.com"}
}

// The review engine and its LLM port are the ones local reviews and evals use.
var (
	_ reviewerapp.LLM = (*llmcall.Caller)(nil)
	_                 = reviewerapp.Run
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Getenv, os.Stderr, defaultEndpoints())
	stop()
	os.Exit(code)
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
		Logger: slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Prices: noPrices{},
	})
}

// run is the runner. It returns the process exit code.
func run(_ context.Context, getenv func(string) string, stderr io.Writer, ep endpoints) int {
	client := newClient(getenv, stderr, ep)
	defer client.Close()
	fmt.Fprintf(stderr, "[agent-runner] trace build=%s\n", buildID)
	if err := client.Ready(llm.OpenRouter); err != nil {
		fmt.Fprintf(stderr, "[agent-runner] FAILED: %s\n", err.Error())
		return 1
	}
	fmt.Fprintln(stderr, "[agent-runner] FAILED: the review is not implemented in this build")
	return 1
}
