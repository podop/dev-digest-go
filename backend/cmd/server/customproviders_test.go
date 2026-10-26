package main

import (
	"bytes"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/config"
	cpdomain "dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/anthropic"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/secrets"
	"dev-digest-go/backend/internal/settings/adapters/providers"
	"dev-digest-go/backend/internal/skills/adapters/urlfetch"
)

// TestServe_BadExtraCAFile (FR14, EC27, AC14): a missing file and a file without a certificate stop
// the server before it touches the database, with one ERROR line that names the variable and the
// path and never the content of the file.
func TestServe_BadExtraCAFile(t *testing.T) {
	dir := t.TempDir()
	const content = "TOP-SECRET-NOT-A-CERTIFICATE"
	noCert := filepath.Join(dir, "no-cert.pem")
	if err := os.WriteFile(noCert, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
	}{
		{name: "missing file", path: filepath.Join(dir, "missing.pem")},
		{name: "file without a certificate", path: noCert},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			err := serve(t.Context(), config.Config{ExtraCAFile: tc.path}, withLogOutput(&logs))
			if err == nil || !strings.Contains(err.Error(), "DEVDIGEST_EXTRA_CA_FILE") || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("serve(ExtraCAFile=%q) error = %v, want one naming the variable and the path", tc.path, err)
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "ERROR") ||
				!strings.Contains(lines[0], "DEVDIGEST_EXTRA_CA_FILE") || !strings.Contains(lines[0], tc.path) {
				t.Errorf("serve(ExtraCAFile=%q) log = %q, want one ERROR line naming the variable and the path", tc.path, logs.String())
			}
			if strings.Contains(logs.String(), content) || strings.Contains(err.Error(), content) {
				t.Errorf("serve(ExtraCAFile=%q) leaked the file content", tc.path)
			}
		})
	}
}

func TestExtraCAClient(t *testing.T) {
	t.Parallel()
	if got := extraCAClient(nil); got != nil {
		t.Errorf("extraCAClient(nil) = %v, want nil (the client's own default stays)", got)
	}
	pool := x509.NewCertPool()
	if got := extraCAClient(pool); got == nil || got.Transport == nil {
		t.Errorf("extraCAClient(pool) = %v, want a client with a transport", got)
	}
	injected := providers.Options{HTTPClient: extraCAClient(pool)}
	if got := providersOptions(injected, x509.NewCertPool()); got.HTTPClient != injected.HTTPClient {
		t.Error("providersOptions replaced an injected client")
	}
	if got := providersOptions(providers.Options{}, nil); got.HTTPClient != nil {
		t.Error("providersOptions(nil pool) set a client")
	}
	if got := urlfetchOptions(urlfetch.Options{}, pool); got.TLSConfig == nil || got.TLSConfig.RootCAs != pool {
		t.Errorf("urlfetchOptions(pool).TLSConfig = %+v, want one with the pool", got.TLSConfig)
	}
	if got := urlfetchOptions(urlfetch.Options{}, nil); got.TLSConfig != nil {
		t.Error("urlfetchOptions(nil pool) set a TLS config")
	}
}

func TestCustomAdapters_Build(t *testing.T) {
	t.Parallel()
	a := newCustomAdapters(nil)
	sec := secrets.CustomSecret{Key: "k", Headers: []secrets.CustomHeader{{Name: "X-Team", Value: "v"}}}
	base := cpdomain.Provider{ID: "custom:gw", Name: "GW", BaseURL: "https://gw.example/v1", AuthMode: custom.AuthBearer}

	tests := []struct {
		name     string
		protocol custom.Protocol
		check    func(llm.Adapter) bool
		wantErr  bool
	}{
		{name: "openai", protocol: custom.ProtocolOpenAI, check: func(ad llm.Adapter) bool { _, ok := ad.(*openaicompat.CustomAdapter); return ok }},
		{name: "anthropic", protocol: custom.ProtocolAnthropic, check: func(ad llm.Adapter) bool { _, ok := ad.(*anthropic.CustomAdapter); return ok }},
		{name: "unknown protocol", protocol: "grpc", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Protocol = tc.protocol
			ad, err := a.Build(p, sec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Build(protocol %q) = %v, nil, want an error", tc.protocol, ad)
				}
				return
			}
			if err != nil || !tc.check(ad) {
				t.Fatalf("Build(protocol %q) = %T, %v, want the %s custom adapter", tc.protocol, ad, err, tc.name)
			}
			if ad.RequiresKey() {
				t.Errorf("Build(protocol %q).RequiresKey() = true, want false", tc.protocol)
			}
		})
	}
}

// TestMockCustomResolver (A4): under the mock override a well-formed custom id answers with the mock,
// a malformed one is not configured.
func TestMockCustomResolver(t *testing.T) {
	t.Parallel()
	r := mockCustomResolver{}
	call, err := r.Resolve(t.Context(), "ws", "custom:ghost")
	if err != nil || call.Adapter == nil || call.Name != "custom:ghost" || call.Mode != custom.ModeStrict {
		t.Errorf("Resolve(custom:ghost) = %+v, %v, want the mock adapter named custom:ghost in strict mode", call, err)
	}
	for _, bad := range []llm.Provider{"openai", "custom:", "custom:A B", ""} {
		if _, err := r.Resolve(t.Context(), "ws", bad); !errors.Is(err, llm.ErrCustomNotConfigured) {
			t.Errorf("Resolve(%q) error = %v, want ErrCustomNotConfigured", bad, err)
		}
	}
}

func TestCiPrices(t *testing.T) {
	t.Parallel()
	c := llm.New(llm.Options{Keys: noKeyReader{}})
	defer c.Close()
	p := ciPrices{book: c}
	if got, ok := p.PerM("openai", "gpt-5.4"); !ok || got.PromptPerM != 2.5 || got.CompletionPerM != 15 {
		t.Errorf("PerM(openai, gpt-5.4) = %+v, %v, want 2.5 / 15, true", got, ok)
	}
	if _, ok := p.PerM("openai", "no-such-model"); ok {
		t.Error("PerM(openai, no-such-model) ok = true, want false")
	}
}

type noKeyReader struct{}

func (noKeyReader) Get(string) string { return "" }
