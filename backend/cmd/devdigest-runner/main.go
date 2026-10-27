// Command devdigest-runner is the DevDigest CI runner: a static linux/amd64 executable that the
// exported GitHub Actions workflow or GitLab CI job runs in someone else's repository to review a
// pull or merge request with the backend's review engine. It reads secrets from the environment
// only, never links the secrets-file store, a database, Echo, the server's GitLab client or cgo
// (it runs the image's git program for the GitLab diff; see closure_test.go), and has no price book:
// the cost of a call is what the provider reports or, for an openai, anthropic or custom manifest,
// the manifest price.
package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	cidomain "dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/runner/adapters/env"
	"dev-digest-go/backend/internal/ci/runner/adapters/ghapi"
	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
	runnerdomain "dev-digest-go/backend/internal/ci/runner/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/anthropic"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/egress"
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
	OpenAI     string // API root including /v1
	Anthropic  string // API root including /v1
	GitHub     string // API root
}

func defaultEndpoints() endpoints {
	return endpoints{
		OpenRouter: openaicompat.DefaultOpenRouterBaseURL,
		OpenAI:     openaicompat.DefaultOpenAIBaseURL,
		Anthropic:  anthropic.DefaultBaseURL,
		GitHub:     ghapi.DefaultBaseURL,
	}
}

// envExtraCAFile names the optional PEM bundle a custom provider's HTTPS certificate may chain to
// (a gateway with a private CA, reached from a self-hosted runner).
const envExtraCAFile = "DEVDIGEST_EXTRA_CA_FILE"

// The runner limits of a built-in openai or anthropic manifest (FR36): the Phase 6 limits of the
// OpenRouter path, not the studio's own openai/anthropic defaults. The 360 s total budget of one
// structured call is the client's default.
var builtInPolicy = llm.Policy{
	RequestTimeout: 180 * time.Second, Retries: 2,
	ListTimeout: 15 * time.Second, ListRetries: 2,
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runProcess(ctx, os.LookupEnv, os.Stdout, os.Stderr, defaultEndpoints())
	stop()
	os.Exit(code)
}

// runProcess is main without the process: GitHub mode runs with workflow commands stopped, GitLab
// mode (DEVDIGEST_PLATFORM=gitlab_ci) without the stop token. A GitLab job log does not interpret
// GitHub workflow commands, and every line printed there has already passed domain.GitLabLogText
// (FR29), so a "::stop-commands::" line would only be noise in the log.
func runProcess(ctx context.Context, lookup func(string) (string, bool), stdout, stderr io.Writer, ep endpoints) int {
	if runnerdomain.GitLabModeRequested(lookup) {
		return run(ctx, lookup, stdout, stderr, ep)
	}
	return withCommandsStopped(stdout, func() int { return run(ctx, lookup, stdout, stderr, ep) })
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

// envKeys reads the two names the OpenRouter client knows from the environment; everything else is
// unset. Only the OpenRouter path uses it: an openai, anthropic or custom manifest gets its own
// client (providers) whose keys hold the one credential of that provider, so the OpenRouter key
// never reaches it (A10, EC29).
type envKeys struct{ getenv func(string) string }

func (k envKeys) Get(name string) string {
	switch name {
	case keynames.OpenRouterKey, keynames.GitHubToken:
		return k.getenv(name)
	}
	return ""
}

// gitLabLogWriter passes the model layer's log lines (one slog record per Write) through line
// before they reach the GitLab job log: redaction, FR29 sanitising and the cap. The record's
// closing line break is kept; the text itself holds none (slog quotes them).
type gitLabLogWriter struct {
	w    io.Writer
	line func(text string) string
}

func (g gitLabLogWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(g.w, g.line(strings.TrimSuffix(string(p), "\n"))+"\n"); err != nil {
		return 0, err
	}
	return len(p), nil
}

// noPrices is the runner's price book for OpenRouter: none. The cost of a call is only what
// OpenRouter reports.
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

// soleKey is the key reader of a built-in provider's client: the one key of the manifest's
// provider under its own name, nothing else.
type soleKey struct{ name, key string }

func (k soleKey) Get(name string) string {
	if name == k.name {
		return k.key
	}
	return ""
}

// limited gives a built-in adapter the runner's limits instead of its own.
type limited struct {
	llm.Adapter
	policy llm.Policy
}

func (l limited) Policy() llm.Policy { return l.policy }

// fixedCustom resolves the one custom provider of the run, whatever the workspace.
type fixedCustom struct {
	id   llm.Provider
	call llm.CustomCall
}

func (f fixedCustom) Resolve(_ context.Context, _ string, p llm.Provider) (llm.CustomCall, error) {
	if p != f.id {
		return llm.CustomCall{}, llm.ErrCustomNotConfigured
	}
	return f.call, nil
}

// providers builds the engine's LLM port for an openai, anthropic or custom manifest (FR30, FR36).
// Each selection gets its own client: no price book (the manifest price prices the tokens), one
// key, the secrets of the selection redacted from every error.
type providers struct {
	ep     endpoints
	stderr io.Writer
	// rootCAs is the trust store for a custom provider's HTTPS; nil = the system store.
	rootCAs *x509.CertPool
}

var _ runnerapp.Providers = providers{}

func (p providers) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(commandSafeWriter{w: p.stderr}, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func (p providers) For(sel runnerapp.ProviderSelection) (reviewerapp.LLM, error) {
	provider := llm.Provider(sel.Provider)
	opts := llm.Options{
		Logger: p.logger(),
		// A non-nil Pricer, so the client never starts the live price book.
		Prices: runnerapp.ManifestPricer{Pricing: sel.Price},
		// A response without usage carries estimated tokens, which a manifest price never prices (SCR7).
		PriceReportedUsageOnly: true,
	}
	switch {
	case provider == llm.OpenAI:
		opts.Keys = soleKey{name: provider.KeyName(), key: sel.Key}
		opts.Adapters = map[llm.Provider]llm.Adapter{
			llm.OpenAI: limited{Adapter: openaicompat.NewOpenAI(openaicompat.Options{BaseURL: p.ep.OpenAI}), policy: builtInPolicy},
		}
	case provider == llm.Anthropic:
		opts.Keys = soleKey{name: provider.KeyName(), key: sel.Key}
		opts.Adapters = map[llm.Provider]llm.Adapter{
			llm.Anthropic: limited{Adapter: anthropic.New(anthropic.Options{BaseURL: p.ep.Anthropic}), policy: builtInPolicy},
		}
	case provider.IsCustom() && sel.Custom != nil:
		opts.Keys = soleKey{}
		opts.Custom = fixedCustom{id: provider, call: p.customCall(provider, sel)}
	default:
		return nil, errors.New("unsupported provider " + sel.Provider)
	}
	client := llm.New(opts)
	return llmcall.New(llmcall.Config{Client: client, Provider: provider, Model: sel.Model, RunID: "ci-runner"}), nil
}

// customCall is the call description of a custom provider: its adapter over the egress client
// (no redirects, no proxy, the address guard), the manifest price and the values to redact.
func (p providers) customCall(id llm.Provider, sel runnerapp.ProviderSelection) llm.CustomCall {
	ep := sel.Custom
	hc := egress.Client{Guard: custom.ForbiddenAddr, RootCAs: p.rootCAs}.HTTPClient()
	headers := make(map[string]string, len(ep.Headers))
	for _, h := range ep.Headers {
		headers[h.Name] = h.Value
	}
	var adapter llm.Adapter
	switch ep.Protocol {
	case custom.ProtocolAnthropic:
		adapter = anthropic.NewCustom(anthropic.CustomOptions{
			ID: string(id), Name: string(id), BaseURL: ep.BaseURL, AuthMode: ep.AuthMode, AuthHeader: ep.AuthHeader,
			Key: sel.Key, Headers: headers, HTTPClient: hc,
		})
	default:
		adapter = openaicompat.NewCustom(openaicompat.CustomOptions{
			ID: string(id), Name: string(id), BaseURL: ep.BaseURL, AuthMode: ep.AuthMode, AuthHeader: ep.AuthHeader,
			Key: sel.Key, Headers: headers, HTTPClient: hc,
		})
	}
	return llm.CustomCall{
		Adapter: adapter, Name: string(id), Protocol: ep.Protocol, Mode: ep.Mode,
		Price: runnerapp.ManifestPricer{Pricing: sel.Price}, Secrets: sel.Secrets(),
	}
}

// secretNames are the environment variables whose values never reach the job log (NFR7, A10):
// every key and token the runner knows, whichever provider the manifest names.
func secretNames() []string {
	names := []string{cidomain.SecretOpenRouter, cidomain.SecretOpenAI, cidomain.SecretAnthropic, keynames.GitHubToken, cidomain.GitLabTokenVariable, "DEVDIGEST_LLM_API_KEY"}
	for n := 1; n <= custom.MaxHeaders; n++ {
		names = append(names, "DEVDIGEST_LLM_HEADER_"+strconv.Itoa(n))
	}
	return names
}

// minRedactLen is the shortest value the panic redactor scrubs, as in the use case.
const minRedactLen = 8

// run is the runner. It returns the process exit code. The review is the one engine local reviews
// and evals run (reviewer/app.Run): over the OpenRouter client of newClient for an openrouter
// manifest, over the client of providers for any other.
func run(ctx context.Context, lookup func(string) (string, bool), stdout, stderr io.Writer, ep endpoints) (code int) {
	getenv := func(name string) string { v, _ := lookup(name); return v }
	out := env.NewOut(stdout, stderr)
	gitLab := false // set below, once the panic handler is in place
	// logLine is a line printed outside the use case: in GitLab mode it passes the §7.5 redaction,
	// the FR29 sanitiser and the 1,200-byte cap (security r1 L2); in GitHub mode it is LogLine.
	logLine := func(prefix, text string) string {
		if gitLab {
			return runnerapp.GitLabLine(env.Process{LookupEnv: lookup}, prefix, text)
		}
		return runnerapp.LogLine(prefix, text)
	}
	defer func() {
		// A panic is a bug, not a review result; it still must not leave a key in the job log.
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			for _, name := range secretNames() {
				if v := getenv(name); len(v) >= minRedactLen {
					msg = strings.ReplaceAll(msg, v, "[REDACTED]")
				}
			}
			out.Stderr(logLine("[agent-runner] fatal error: ", msg))
			code = runnerapp.ExitFailed
		}
	}()

	gitLab = runnerdomain.GitLabModeRequested(lookup)
	logOut := stderr
	if gitLab {
		logOut = gitLabLogWriter{w: stderr, line: func(text string) string { return logLine("", text) }}
	}

	rootCAs, err := egress.LoadExtraCA(getenv(envExtraCAFile))
	if err != nil {
		out.Stderr(logLine("[agent-runner] FAILED: ", envExtraCAFile+": "+err.Error()))
		return runnerapp.ExitFailed
	}

	client := newClient(getenv, logOut, ep)
	defer client.Close()
	deps := runnerapp.Deps{
		Env:       env.Process{LookupEnv: lookup},
		Files:     env.Files{},
		GitHub:    ghapi.New(ghapi.Options{BaseURL: ep.GitHub}),
		LLM:       reviewLLM{client: client},
		Providers: providers{ep: ep, stderr: logOut, rootCAs: rootCAs},
		Out:       out,
	}
	if gitLab {
		deps.GitLab, deps.GitDiff = gitLabDeps(lookup)
	}
	return runnerapp.Run(ctx, runnerapp.Config{BuildID: buildID}, deps)
}
