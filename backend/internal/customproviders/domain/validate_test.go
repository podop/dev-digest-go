package domain_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm/custom"
)

func str(s string) *string { return &s }

// valid is the smallest accepted request; tests change one field of it.
func valid() domain.Input {
	return domain.Input{ID: "custom:synapse", Name: "Synapse", Protocol: custom.ProtocolOpenAI, BaseURL: "https://synapse.test/v1/"}
}

// paths returns the issue paths as "a.b.0.c" strings.
func paths(issues []domain.Issue) []string {
	var out []string
	for _, is := range issues {
		parts := make([]string, len(is.Path))
		for i, p := range is.Path {
			parts[i] = fmt.Sprint(p)
		}
		out = append(out, strings.Join(parts, "."))
	}
	return out
}

func TestValidate_Defaults(t *testing.T) {
	tests := []struct {
		name       string
		edit       func(*domain.Input)
		wantMode   custom.AuthMode
		wantHeader string
		wantOutput custom.Mode
	}{
		{name: "openai bearer strict", edit: func(*domain.Input) {}, wantMode: custom.AuthBearer, wantOutput: custom.ModeStrict},
		{name: "anthropic header x-api-key", edit: func(in *domain.Input) { in.Protocol = custom.ProtocolAnthropic },
			wantMode: custom.AuthHeader, wantHeader: "x-api-key", wantOutput: custom.ModeStrict},
		{name: "openai header custom name", edit: func(in *domain.Input) { in.AuthMode, in.AuthHeader = custom.AuthHeader, str("api-key") },
			wantMode: custom.AuthHeader, wantHeader: "api-key", wantOutput: custom.ModeStrict},
		{name: "bearer drops the header", edit: func(in *domain.Input) { in.AuthHeader = str("api-key") },
			wantMode: custom.AuthBearer, wantOutput: custom.ModeStrict},
		{name: "prompt only kept", edit: func(in *domain.Input) { in.StructuredOutput = custom.ModePromptOnly },
			wantMode: custom.AuthBearer, wantOutput: custom.ModePromptOnly},
		{name: "json object with openai", edit: func(in *domain.Input) { in.StructuredOutput = custom.ModeJSONObject },
			wantMode: custom.AuthBearer, wantOutput: custom.ModeJSONObject},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.edit(&in)
			d, issues := domain.Validate(in)
			if len(issues) > 0 {
				t.Fatalf("Validate(%+v) issues = %v, want none", in, issues)
			}
			p := d.Provider
			if p.AuthMode != tc.wantMode || p.AuthHeader != tc.wantHeader || p.StructuredOutput != tc.wantOutput {
				t.Errorf("Validate() auth = (%q, %q), output %q; want (%q, %q), %q",
					p.AuthMode, p.AuthHeader, p.StructuredOutput, tc.wantMode, tc.wantHeader, tc.wantOutput)
			}
		})
	}
}

func TestValidate_Normalizes(t *testing.T) {
	in := valid()
	in.Name = "   Synapse GW \t"
	in.Models = []domain.Model{{ID: "a", Label: str("")}, {ID: "b", Label: str("B"), Pricing: &domain.Pricing{PromptPerM: 2, CompletionPerM: 8}}}
	d, issues := domain.Validate(in)
	if len(issues) > 0 {
		t.Fatalf("Validate() issues = %v", issues)
	}
	if got, want := d.Provider.Name, "Synapse GW"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := d.Provider.BaseURL, "https://synapse.test/v1"; got != want {
		t.Errorf("BaseURL = %q, want %q", got, want)
	}
	if d.Provider.Models[0].Label != nil {
		t.Errorf("empty label = %q, want nil", *d.Provider.Models[0].Label)
	}
	if p := d.Provider.Models[1].Pricing; p == nil || p.PromptPerM != 2 || p.CompletionPerM != 8 {
		t.Errorf("pricing = %+v, want 2/8", d.Provider.Models[1].Pricing)
	}
}

func TestValidate_Rejects(t *testing.T) {
	long := strings.Repeat("a", 33)
	tests := []struct {
		name      string
		edit      func(*domain.Input)
		wantPaths []string
	}{
		// EC2: ids.
		{name: "id upper case", edit: func(in *domain.Input) { in.ID = "custom:Syn" }, wantPaths: []string{"id"}},
		{name: "id empty slug", edit: func(in *domain.Input) { in.ID = "custom:" }, wantPaths: []string{"id"}},
		{name: "id 33 chars", edit: func(in *domain.Input) { in.ID = "custom:" + long }, wantPaths: []string{"id"}},
		{name: "id without prefix", edit: func(in *domain.Input) { in.ID = "synapse" }, wantPaths: []string{"id"}},
		{name: "id built-in", edit: func(in *domain.Input) { in.ID = "openai" }, wantPaths: []string{"id"}},
		{name: "id underscore", edit: func(in *domain.Input) { in.ID = "custom:a_b" }, wantPaths: []string{"id"}},
		// name.
		{name: "name blank", edit: func(in *domain.Input) { in.Name = " \t" }, wantPaths: []string{"name"}},
		{name: "name 81", edit: func(in *domain.Input) { in.Name = strings.Repeat("n", 81) }, wantPaths: []string{"name"}},
		{name: "name control", edit: func(in *domain.Input) { in.Name = "a\nb" }, wantPaths: []string{"name"}},
		{name: "name 41 emoji is 82 units", edit: func(in *domain.Input) { in.Name = strings.Repeat("\U0001F600", 41) }, wantPaths: []string{"name"}},
		// protocol.
		{name: "protocol unknown", edit: func(in *domain.Input) { in.Protocol = "grpc" }, wantPaths: []string{"protocol"}},
		{name: "protocol empty", edit: func(in *domain.Input) { in.Protocol = "" }, wantPaths: []string{"protocol"}},
		// EC3: literal base URLs.
		{name: "ftp", edit: func(in *domain.Input) { in.BaseURL = "ftp://x" }, wantPaths: []string{"base_url"}},
		{name: "userinfo", edit: func(in *domain.Input) { in.BaseURL = "https://u:p@x/v1" }, wantPaths: []string{"base_url"}},
		{name: "query", edit: func(in *domain.Input) { in.BaseURL = "https://x/v1?k=1" }, wantPaths: []string{"base_url"}},
		{name: "fragment", edit: func(in *domain.Input) { in.BaseURL = "https://x/v1#f" }, wantPaths: []string{"base_url"}},
		{name: "link local", edit: func(in *domain.Input) { in.BaseURL = "http://169.254.169.254" }, wantPaths: []string{"base_url"}},
		{name: "link local v6", edit: func(in *domain.Input) { in.BaseURL = "http://[fe80::1]" }, wantPaths: []string{"base_url"}},
		{name: "unspecified", edit: func(in *domain.Input) { in.BaseURL = "http://0.0.0.0" }, wantPaths: []string{"base_url"}},
		{name: "base url empty", edit: func(in *domain.Input) { in.BaseURL = "" }, wantPaths: []string{"base_url"}},
		// auth.
		{name: "auth mode unknown", edit: func(in *domain.Input) { in.AuthMode = "basic" }, wantPaths: []string{"auth_mode"}},
		{name: "openai header mode without header", edit: func(in *domain.Input) { in.AuthMode = custom.AuthHeader }, wantPaths: []string{"auth_header"}},
		{name: "auth header reserved", edit: func(in *domain.Input) { in.AuthMode, in.AuthHeader = custom.AuthHeader, str("Content-Type") }, wantPaths: []string{"auth_header"}},
		{name: "auth header not a token", edit: func(in *domain.Input) { in.AuthMode, in.AuthHeader = custom.AuthHeader, str("a b") }, wantPaths: []string{"auth_header"}},
		// EC30.
		{name: "json object with anthropic", edit: func(in *domain.Input) {
			in.Protocol, in.StructuredOutput = custom.ProtocolAnthropic, custom.ModeJSONObject
		}, wantPaths: []string{"structured_output"}},
		{name: "structured output loose", edit: func(in *domain.Input) { in.StructuredOutput = "loose" }, wantPaths: []string{"structured_output"}},
		// key.
		{name: "key too long", edit: func(in *domain.Input) {
			in.APIKey = domain.KeyChange{Op: domain.KeyReplace, Value: strings.Repeat("k", 4097)}
		}, wantPaths: []string{"api_key"}},
		{name: "key empty", edit: func(in *domain.Input) { in.APIKey = domain.KeyChange{Op: domain.KeyReplace} }, wantPaths: []string{"api_key"}},
		{name: "key newline", edit: func(in *domain.Input) { in.APIKey = domain.KeyChange{Op: domain.KeyReplace, Value: "sk-1\nx"} }, wantPaths: []string{"api_key"}},
		// EC11: extra headers.
		{name: "header content-type", edit: func(in *domain.Input) {
			in.ExtraHeaders = []domain.HeaderInput{{Name: "content-type", Value: str("x")}}
		}, wantPaths: []string{"extra_headers.0.name"}},
		{name: "header Authorization", edit: func(in *domain.Input) {
			in.ExtraHeaders = []domain.HeaderInput{{Name: "Authorization", Value: str("x")}}
		}, wantPaths: []string{"extra_headers.0.name"}},
		{name: "header equals auth header in other case", edit: func(in *domain.Input) {
			in.AuthMode, in.AuthHeader = custom.AuthHeader, str("api-key")
			in.ExtraHeaders = []domain.HeaderInput{{Name: "API-Key", Value: str("x")}}
		}, wantPaths: []string{"extra_headers.0.name"}},
		{name: "header duplicate in other case", edit: func(in *domain.Input) {
			in.ExtraHeaders = []domain.HeaderInput{{Name: "X-Tenant", Value: str("a")}, {Name: "x-tenant", Value: str("b")}}
		}, wantPaths: []string{"extra_headers.1.name"}},
		{name: "sixth header", edit: func(in *domain.Input) {
			for i := range 6 {
				in.ExtraHeaders = append(in.ExtraHeaders, domain.HeaderInput{Name: fmt.Sprintf("X-H%d", i), Value: str("v")})
			}
		}, wantPaths: []string{"extra_headers"}},
		{name: "header value CRLF", edit: func(in *domain.Input) { in.ExtraHeaders = []domain.HeaderInput{{Name: "X-A", Value: str("a\r\nb")}} }, wantPaths: []string{"extra_headers.0.value"}},
		{name: "header value empty", edit: func(in *domain.Input) { in.ExtraHeaders = []domain.HeaderInput{{Name: "X-A", Value: str("")}} }, wantPaths: []string{"extra_headers.0.value"}},
		// EC13: models.
		{name: "201 models", edit: func(in *domain.Input) {
			for i := range 201 {
				in.Models = append(in.Models, domain.Model{ID: fmt.Sprintf("m%d", i)})
			}
		}, wantPaths: []string{"models"}},
		{name: "duplicate model", edit: func(in *domain.Input) { in.Models = []domain.Model{{ID: "a"}, {ID: "b"}, {ID: "a"}} }, wantPaths: []string{"models.2.id"}},
		{name: "model id empty", edit: func(in *domain.Input) { in.Models = []domain.Model{{ID: ""}} }, wantPaths: []string{"models.0.id"}},
		{name: "model id 201", edit: func(in *domain.Input) { in.Models = []domain.Model{{ID: strings.Repeat("m", 201)}} }, wantPaths: []string{"models.0.id"}},
		{name: "label 201", edit: func(in *domain.Input) { in.Models = []domain.Model{{ID: "a", Label: str(strings.Repeat("l", 201))}} }, wantPaths: []string{"models.0.label"}},
		{name: "price negative", edit: func(in *domain.Input) {
			in.Models = []domain.Model{{ID: "a", Pricing: &domain.Pricing{PromptPerM: -1}}}
		}, wantPaths: []string{"models.0.pricing.promptPerM"}},
		{name: "price above 10000", edit: func(in *domain.Input) {
			in.Models = []domain.Model{{ID: "a", Pricing: &domain.Pricing{CompletionPerM: 10000.5}}}
		}, wantPaths: []string{"models.0.pricing.completionPerM"}},
		{name: "price 7 decimals", edit: func(in *domain.Input) {
			in.Models = []domain.Model{{ID: "a", Pricing: &domain.Pricing{PromptPerM: 0.1234567}}}
		}, wantPaths: []string{"models.0.pricing.promptPerM"}},
		{name: "two bad fields", edit: func(in *domain.Input) { in.Name, in.BaseURL = "", "ftp://x" }, wantPaths: []string{"name", "base_url"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.edit(&in)
			_, issues := domain.Validate(in)
			if diff := cmp.Diff(tc.wantPaths, paths(issues)); diff != "" {
				t.Errorf("Validate() issue paths mismatch (-want +got):\n%s\nissues: %+v", diff, issues)
			}
		})
	}
}

func TestValidate_Accepts(t *testing.T) {
	tests := []struct {
		name string
		edit func(*domain.Input)
	}{
		{name: "slug 32 chars", edit: func(in *domain.Input) { in.ID = "custom:" + strings.Repeat("a", 32) }},
		{name: "name 80 chars", edit: func(in *domain.Input) { in.Name = strings.Repeat("n", 80) }},
		{name: "five headers", edit: func(in *domain.Input) {
			for i := range 5 {
				in.ExtraHeaders = append(in.ExtraHeaders, domain.HeaderInput{Name: fmt.Sprintf("X-H%d", i), Value: str("v")})
			}
		}},
		{name: "header without value passes validation", edit: func(in *domain.Input) { in.ExtraHeaders = []domain.HeaderInput{{Name: "X-A"}} }},
		{name: "200 models", edit: func(in *domain.Input) {
			for i := range 200 {
				in.Models = append(in.Models, domain.Model{ID: fmt.Sprintf("m%d", i)})
			}
		}},
		{name: "price 6 decimals and bounds", edit: func(in *domain.Input) {
			in.Models = []domain.Model{{ID: "a", Pricing: &domain.Pricing{PromptPerM: 0.000001, CompletionPerM: 10000}}, {ID: "b", Pricing: &domain.Pricing{}}}
		}},
		{name: "http loopback", edit: func(in *domain.Input) { in.BaseURL = "http://localhost:8080/v1" }},
		{name: "private http", edit: func(in *domain.Input) { in.BaseURL = "http://10.0.0.5/v1" }},
		{name: "key kept", edit: func(in *domain.Input) { in.APIKey = domain.KeyChange{Op: domain.KeyKeep} }},
		{name: "key removed", edit: func(in *domain.Input) { in.APIKey = domain.KeyChange{Op: domain.KeyRemove} }},
		{name: "key 4096", edit: func(in *domain.Input) {
			in.APIKey = domain.KeyChange{Op: domain.KeyReplace, Value: strings.Repeat("k", 4096)}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.edit(&in)
			if _, issues := domain.Validate(in); len(issues) > 0 {
				t.Errorf("Validate() issues = %+v, want none", issues)
			}
		})
	}
}

func TestValidate_KeepsHeaderOrderAndNames(t *testing.T) {
	in := valid()
	in.ExtraHeaders = []domain.HeaderInput{{Name: "X-Tenant", Value: str("t")}, {Name: "x-Other"}}
	d, issues := domain.Validate(in)
	if len(issues) > 0 {
		t.Fatalf("Validate() issues = %v", issues)
	}
	if diff := cmp.Diff([]string{"X-Tenant", "x-Other"}, d.Provider.ExtraHeaderNames); diff != "" {
		t.Errorf("ExtraHeaderNames mismatch (-want +got):\n%s", diff)
	}
	if d.Headers[1].Value != nil {
		t.Errorf("header without value = %q, want nil", *d.Headers[1].Value)
	}
}

func TestValidateKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool // acceptable
	}{{"sk-ok", true}, {"", false}, {"a\x00b", false}, {strings.Repeat("k", 4097), false}} {
		if got := domain.ValidateKey(tc.key) == ""; got != tc.want {
			t.Errorf("ValidateKey(len %d) acceptable = %v, want %v", len(tc.key), got, tc.want)
		}
	}
}
