package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gorm.io/gorm"

	ciapp "dev-digest-go/backend/internal/ci/app"
	cidomain "dev-digest-go/backend/internal/ci/domain"
	cppg "dev-digest-go/backend/internal/customproviders/adapters/postgres"
	cpapp "dev-digest-go/backend/internal/customproviders/app"
	cpdomain "dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/anthropic"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/secrets"
	"dev-digest-go/backend/internal/settings/adapters/providers"
	settingsapp "dev-digest-go/backend/internal/settings/app"
	"dev-digest-go/backend/internal/skills/adapters/urlfetch"
)

// Custom model providers (docs/plans/2026-10-26-custom-model-providers.md, S26): the composition
// root binds the customproviders context to the contexts that need to know which providers exist
// (settings, agents, plugins, ci) and to the LLM client. This file holds the adapters; the wiring
// is in wire (app.go).

// extraCAClient returns an HTTP client that trusts pool besides the system store, for the outbound
// clients that are not built by egress.Client (GitHub, the built-in LLM providers, the providers
// listing). A nil pool returns nil, which leaves the client's own default (and any test stub) alone.
func extraCAClient(pool *x509.CertPool) *http.Client {
	if pool == nil {
		return nil
	}
	return &http.Client{Transport: egress.WithRootCAs(nil, pool)}
}

// providersOptions gives the provider model listing (Settings) the extra CA unless a test injected
// its own client.
func providersOptions(opts providers.Options, pool *x509.CertPool) providers.Options {
	if opts.HTTPClient == nil {
		opts.HTTPClient = extraCAClient(pool)
	}
	return opts
}

// urlfetchOptions gives the skill URL importer the extra CA unless a test injected its own TLS
// configuration. The importer builds its own transport, so it takes a tls.Config, not a client.
func urlfetchOptions(opts urlfetch.Options, pool *x509.CertPool) urlfetch.Options {
	if opts.TLSConfig == nil && pool != nil {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	return opts
}

// customAdapters builds the HTTP adapter of a custom provider (cpapp.Adapters): by protocol, with
// the egress client (no proxy, no redirect, connect-time address guard) and the extra CA. One
// client serves every adapter, so connections to a gateway are reused across calls.
type customAdapters struct{ hc *http.Client }

func newCustomAdapters(rootCAs *x509.CertPool) customAdapters {
	return customAdapters{hc: egress.Client{Guard: custom.ForbiddenAddr, RootCAs: rootCAs}.HTTPClient()}
}

var _ cpapp.Adapters = customAdapters{}

// Build returns the adapter of p carrying sec's key and header values.
func (a customAdapters) Build(p cpdomain.Provider, sec secrets.CustomSecret) (llm.Adapter, error) {
	var headers map[string]string
	if len(sec.Headers) > 0 {
		headers = make(map[string]string, len(sec.Headers))
		for _, h := range sec.Headers {
			headers[h.Name] = h.Value
		}
	}
	switch p.Protocol {
	case custom.ProtocolOpenAI:
		return openaicompat.NewCustom(openaicompat.CustomOptions{
			ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, AuthMode: p.AuthMode, AuthHeader: p.AuthHeader,
			Key: sec.Key, Headers: headers, HTTPClient: a.hc,
		}), nil
	case custom.ProtocolAnthropic:
		return anthropic.NewCustom(anthropic.CustomOptions{
			ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, AuthMode: p.AuthMode, AuthHeader: p.AuthHeader,
			Key: sec.Key, Headers: headers, HTTPClient: a.hc,
		}), nil
	}
	return nil, fmt.Errorf("custom provider %s: unknown protocol %q", p.ID, p.Protocol)
}

// newCustomProviders builds the customproviders use cases over the database and the secrets file.
func newCustomProviders(db *gorm.DB, sec *secrets.Store, rootCAs *x509.CertPool, now func() time.Time, log *slog.Logger) *cpapp.Service {
	return cpapp.NewService(cpapp.Deps{
		Store:      cppg.NewStore(db),
		Secrets:    sec,
		References: cppg.NewReferences(db),
		Adapters:   newCustomAdapters(rootCAs),
		Clock:      cpapp.Clock(now),
		Log:        log,
	})
}

// mockCustomResolver is the custom resolver under LLM_PROVIDER_OVERRIDE=mock (A4): every
// well-formed custom id answers with the deterministic mock and no key, whether or not the
// workspace defines it, like the built-in providers. Definitions, validation and the tests of a
// connection still go through the real customproviders service.
type mockCustomResolver struct{ delay time.Duration }

var _ llm.CustomResolver = mockCustomResolver{}

func (m mockCustomResolver) Resolve(_ context.Context, _ string, p llm.Provider) (llm.CustomCall, error) {
	if !p.IsCustom() {
		return llm.CustomCall{}, llm.ErrCustomNotConfigured
	}
	return llm.CustomCall{
		Adapter: mock.New(p, mock.Options{Delay: m.delay}),
		Name:    string(p),
		Mode:    custom.ModeStrict,
	}, nil
}

// settingsCustom adapts the customproviders service to the settings port: it answers which custom
// ids exist and runs the connection test of a defined one.
type settingsCustom struct{ svc *cpapp.Service }

func (s settingsCustom) Defined(ctx context.Context, workspaceID, id string) bool {
	return s.svc.Defined(ctx, workspaceID, id)
}

// TestConnection maps the test result of the context to the settings one; an undefined provider is
// a 404 custom_provider_not_found and nothing is saved (the service checks before it writes).
func (s settingsCustom) TestConnection(ctx context.Context, workspaceID, id, key string) (settingsapp.ConnTestResult, error) {
	res, err := s.svc.TestConnection(ctx, workspaceID, id, key)
	if err != nil {
		return settingsapp.ConnTestResult{}, err
	}
	return settingsapp.ConnTestResult{
		Provider:       settingsapp.Provider(id),
		OK:             res.OK,
		Message:        res.Message,
		LatencyMS:      res.LatencyMS,
		Model:          res.Model,
		ModelReachable: res.ModelReachable,
		Warning:        res.Warning,
	}, nil
}

// agentsCustom adapts the customproviders service to the agents port: existence (plain and locked
// FOR SHARE inside the agent write's transaction) and the live and manual models of a provider.
type agentsCustom struct{ svc *cpapp.Service }

func (a agentsCustom) Defined(ctx context.Context, workspaceID, id string) (bool, error) {
	return a.svc.IsDefined(ctx, workspaceID, id)
}

func (a agentsCustom) LockDefined(ctx context.Context, workspaceID, id string) (bool, error) {
	return a.svc.LockDefined(ctx, workspaceID, id)
}

func (a agentsCustom) Models(ctx context.Context, workspaceID, id string) ([]llm.ModelInfo, error) {
	return a.svc.Models(ctx, workspaceID, id)
}

// pluginsCustom adapts the customproviders service to the plugins port: which of the ids a bundle
// names the workspace defines (for the dry-run warning).
type pluginsCustom struct{ svc *cpapp.Service }

func (p pluginsCustom) CustomDefined(ctx context.Context, workspaceID string, ids []string) ([]string, error) {
	var defined []string
	for _, id := range ids {
		ok, err := p.svc.IsDefined(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if ok {
			defined = append(defined, id)
		}
	}
	return defined, nil
}

// ciCustom adapts the customproviders service to the ci export port: the definition without the
// base URL, key or header values, whether a key is stored, and the manual price of the model.
type ciCustom struct{ svc *cpapp.Service }

var _ ciapp.CustomProviders = ciCustom{}

func (c ciCustom) ForExport(ctx context.Context, workspaceID, providerID, model string) (ciapp.CustomExport, bool, error) {
	v, err := c.svc.Get(ctx, workspaceID, providerID)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Kind == apperr.KindNotFound {
			return ciapp.CustomExport{}, false, nil
		}
		return ciapp.CustomExport{}, false, err
	}
	p := v.Provider
	cfg := cidomain.CustomProviderConfig{
		Protocol:         string(p.Protocol),
		AuthMode:         string(p.AuthMode),
		ExtraHeaders:     append([]string(nil), p.ExtraHeaderNames...),
		StructuredOutput: string(p.StructuredOutput),
	}
	if p.AuthMode == custom.AuthHeader {
		header := p.AuthHeader
		if header == "" {
			header = custom.DefaultAuthHeader
		}
		cfg.AuthHeader = &header
	}
	out := ciapp.CustomExport{Config: cfg, KeyConfigured: v.KeyConfigured}
	for _, m := range p.Models {
		if m.ID == model && m.Pricing != nil {
			out.Pricing = &cidomain.Pricing{PromptPerM: m.Pricing.PromptPerM, CompletionPerM: m.Pricing.CompletionPerM}
			break
		}
	}
	return out, true, nil
}

// ciPrices adapts the LLM client's price book to the ci export port (FR35).
type ciPrices struct {
	book interface {
		PerM(model string) (promptPerM, completionPerM float64, ok bool)
	}
}

var _ ciapp.ModelPrices = ciPrices{}

func (c ciPrices) PerM(_, model string) (cidomain.Pricing, bool) {
	in, out, ok := c.book.PerM(model)
	if !ok {
		return cidomain.Pricing{}, false
	}
	return cidomain.Pricing{PromptPerM: in, CompletionPerM: out}, true
}
