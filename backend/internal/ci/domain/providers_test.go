package domain_test

import (
	"regexp"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
)

func TestCustomSlug(t *testing.T) {
	tests := []struct {
		provider string
		want     string
		wantOK   bool
	}{
		{"custom:my-gw", "my-gw", true},
		{"custom:a", "a", true},
		{"custom:-", "-", true},
		{"custom:" + "a23456789012345678901234567890bc", "a23456789012345678901234567890bc", true},
		{"custom:" + "a23456789012345678901234567890bcd", "", false},
		{"custom:", "", false},
		{"custom:My-Gw", "", false},
		{"custom:a_b", "", false},
		{"custom:a\nb", "", false},
		{"custom:a b", "", false},
		{"openrouter", "", false},
		{"Custom:a", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, ok := domain.CustomSlug(tc.provider)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("CustomSlug(%q) = (%q, %v), want (%q, %v)", tc.provider, got, ok, tc.want, tc.wantOK)
		}
		if domain.IsCustomProvider(tc.provider) != tc.wantOK {
			t.Errorf("IsCustomProvider(%q) = %v, want %v", tc.provider, !tc.wantOK, tc.wantOK)
		}
	}
}

func TestEnvName(t *testing.T) {
	tests := []struct {
		slug   string
		want   string
		wantOK bool
	}{
		{"my-gw", "MY_GW", true},
		{"synapse", "SYNAPSE", true},
		{"a-b-c", "A_B_C", true},
		{"9-x", "9_X", true},
		{"-", "_", true},
		{"", "", false},
		{"My-gw", "", false},
		{"my_gw", "", false},
		{"my gw", "", false},
		{"a\n  evil: 1", "", false},
		{"a23456789012345678901234567890bcd", "", false},
	}
	for _, tc := range tests {
		got, ok := domain.EnvName(tc.slug)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("EnvName(%q) = (%q, %v), want (%q, %v)", tc.slug, got, ok, tc.want, tc.wantOK)
		}
	}
}

// EC26: slugs that differ only in "-" positions never collide, and every derived name has the
// shape of spec 7.
func TestDerivedNamesShapeAndInjective(t *testing.T) {
	shape := regexp.MustCompile(`^DEVDIGEST_[A-Z0-9_]{1,32}_(BASE_URL|API_KEY|HEADER_[1-5])$`)
	slugs := []string{"a", "a-b", "ab", "a-b-c", "a--b", "-a", "a-", "a23456789012345678901234567890bc", "my-gw"}
	seen := map[string]string{}
	for _, slug := range slugs {
		env, ok := domain.EnvName(slug)
		if !ok {
			t.Fatalf("EnvName(%q) not ok", slug)
		}
		if prev, dup := seen[env]; dup {
			t.Errorf("EnvName(%q) = EnvName(%q) = %q", slug, prev, env)
		}
		seen[env] = slug
		names := []string{domain.BaseURLVariable(env), domain.APIKeySecret(env)}
		for n := 1; n <= domain.MaxExtraHeaders; n++ {
			names = append(names, domain.HeaderSecret(env, n))
		}
		for _, name := range names {
			if !shape.MatchString(name) {
				t.Errorf("name %q for slug %q does not match %s", name, slug, shape)
			}
		}
	}
}

func TestSecretsAndVariables(t *testing.T) {
	gw := []string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2"}
	tests := []struct {
		name         string
		provider     string
		keyConfigure bool
		headers      int
		wantNames    []string
		wantSecrets  []string
		wantVars     []string
	}{
		{"openrouter", "openrouter", true, 0, []string{"OPENROUTER_API_KEY"}, []string{"OPENROUTER_API_KEY"}, []string{}},
		{"openai", "openai", false, 3, []string{"OPENAI_API_KEY"}, []string{"OPENAI_API_KEY"}, []string{}},
		{"anthropic", "anthropic", true, 0, []string{"ANTHROPIC_API_KEY"}, []string{"ANTHROPIC_API_KEY"}, []string{}},
		{
			"custom with key and two headers", "custom:my-gw", true, 2,
			append(append([]string{}, gw[0]), "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2", "DEVDIGEST_MY_GW_HEADER_3", "DEVDIGEST_MY_GW_HEADER_4", "DEVDIGEST_MY_GW_HEADER_5"),
			gw, []string{"DEVDIGEST_MY_GW_BASE_URL"},
		},
		{
			"custom without key", "custom:my-gw", false, 1,
			[]string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2", "DEVDIGEST_MY_GW_HEADER_3", "DEVDIGEST_MY_GW_HEADER_4", "DEVDIGEST_MY_GW_HEADER_5"},
			[]string{"DEVDIGEST_MY_GW_HEADER_1"}, []string{"DEVDIGEST_MY_GW_BASE_URL"},
		},
		{
			"custom keyless without headers", "custom:net", false, 0,
			[]string{"DEVDIGEST_NET_API_KEY", "DEVDIGEST_NET_HEADER_1", "DEVDIGEST_NET_HEADER_2", "DEVDIGEST_NET_HEADER_3", "DEVDIGEST_NET_HEADER_4", "DEVDIGEST_NET_HEADER_5"},
			[]string{}, []string{"DEVDIGEST_NET_BASE_URL"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			names, ok := domain.SecretNames(tc.provider)
			if !ok {
				t.Fatalf("SecretNames(%q) not ok", tc.provider)
			}
			if diff := cmp.Diff(tc.wantNames, names); diff != "" {
				t.Errorf("SecretNames(%q) mismatch (-want +got):\n%s", tc.provider, diff)
			}
			got := domain.RequiredSecrets(tc.provider, tc.keyConfigure, tc.headers)
			if got == nil {
				t.Errorf("RequiredSecrets(%q) = nil, want a non-nil list", tc.provider)
			}
			if diff := cmp.Diff(tc.wantSecrets, got); diff != "" {
				t.Errorf("RequiredSecrets(%q, %v, %d) mismatch (-want +got):\n%s", tc.provider, tc.keyConfigure, tc.headers, diff)
			}
			vars := domain.RequiredVariables(tc.provider)
			if vars == nil {
				t.Errorf("RequiredVariables(%q) = nil, want a non-nil list", tc.provider)
			}
			if diff := cmp.Diff(tc.wantVars, vars); diff != "" {
				t.Errorf("RequiredVariables(%q) mismatch (-want +got):\n%s", tc.provider, diff)
			}
		})
	}
}

func TestSecretsAndVariables_InvalidProvider(t *testing.T) {
	for _, p := range []string{"", "gemini", "custom:", "custom:UP", "custom:a_b"} {
		if names, ok := domain.SecretNames(p); ok || names != nil {
			t.Errorf("SecretNames(%q) = (%v, %v), want (nil, false)", p, names, ok)
		}
		if got := domain.RequiredSecrets(p, true, 2); got != nil {
			t.Errorf("RequiredSecrets(%q) = %v, want nil", p, got)
		}
		if got := domain.RequiredVariables(p); got != nil {
			t.Errorf("RequiredVariables(%q) = %v, want nil", p, got)
		}
	}
}

func TestRequiredSecrets_HeaderCountClamped(t *testing.T) {
	if got := domain.RequiredSecrets("custom:x", false, 9); len(got) != domain.MaxExtraHeaders {
		t.Errorf("RequiredSecrets(custom:x, false, 9) = %v, want %d headers", got, domain.MaxExtraHeaders)
	}
	if got := domain.RequiredSecrets("custom:x", false, -1); len(got) != 0 {
		t.Errorf("RequiredSecrets(custom:x, false, -1) = %v, want none", got)
	}
}

func TestRunner(t *testing.T) {
	tests := []struct {
		runner     domain.Runner
		wantRunsOn string
		wantOK     bool
	}{
		{"", "ubuntu-24.04", true},
		{domain.RunnerGitHubHosted, "ubuntu-24.04", true},
		{domain.RunnerSelfHosted, "[self-hosted, linux, x64]", true},
		{"self-hosted", "", false},
		{"ubuntu-latest\n  evil: 1", "", false},
	}
	for _, tc := range tests {
		got, ok := tc.runner.RunsOn()
		if got != tc.wantRunsOn || ok != tc.wantOK {
			t.Errorf("Runner(%q).RunsOn() = (%q, %v), want (%q, %v)", tc.runner, got, ok, tc.wantRunsOn, tc.wantOK)
		}
		if tc.runner.Valid() != tc.wantOK {
			t.Errorf("Runner(%q).Valid() = %v, want %v", tc.runner, !tc.wantOK, tc.wantOK)
		}
	}
}

func TestAllowedSecretNames_OnlyTheProvidersOwnHeaders(t *testing.T) {
	cases := []struct {
		provider string
		headers  int
		want     []string
	}{
		{"openrouter", 3, []string{"OPENROUTER_API_KEY"}},
		{"openai", 0, []string{"OPENAI_API_KEY"}},
		{"custom:my-gw", 0, []string{"DEVDIGEST_MY_GW_API_KEY"}},
		{"custom:my-gw", 2, []string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2"}},
		{"custom:my-gw", 9, []string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2", "DEVDIGEST_MY_GW_HEADER_3", "DEVDIGEST_MY_GW_HEADER_4", "DEVDIGEST_MY_GW_HEADER_5"}},
		{"custom:my-gw", -1, []string{"DEVDIGEST_MY_GW_API_KEY"}},
	}
	for _, c := range cases {
		got, ok := domain.AllowedSecretNames(c.provider, c.headers)
		if !ok || cmp.Diff(c.want, got) != "" {
			t.Errorf("AllowedSecretNames(%q, %d) = %v, %v; want %v, true", c.provider, c.headers, got, ok, c.want)
		}
	}
	if _, ok := domain.AllowedSecretNames("custom:Bad", 1); ok {
		t.Error("AllowedSecretNames accepted a malformed custom id")
	}
}
