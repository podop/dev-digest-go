package gitlab_test

import (
	"fmt"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/platform/gitlab"
)

func TestBinding_Conn(t *testing.T) {
	tests := []struct {
		name, baseURL, want string
	}{
		{"plain", "https://gitlab.example.com", "https://gitlab.example.com/api/v4"},
		{"port", "https://gitlab.example.com:8443", "https://gitlab.example.com:8443/api/v4"},
		{"prefix", "https://example.com/gitlab", "https://example.com/gitlab/api/v4"},
		{"stray slash", "https://example.com/gitlab/", "https://example.com/gitlab/api/v4"},
	}
	for _, tc := range tests {
		b := gitlab.Binding{ID: "1", Name: "n", BaseURL: tc.baseURL, Host: "h", Token: testToken}
		if got := b.Conn(); got.BaseURL != tc.want || got.Token != testToken {
			t.Errorf("Binding{%s}.Conn() = %q, token ok %v; want %q", tc.name, got.BaseURL, got.Token == testToken, tc.want)
		}
	}
}

func TestBinding_TextFormsHideTheToken(t *testing.T) {
	b := gitlab.Binding{ID: "1", Name: "Corp", BaseURL: "https://gitlab.example.com", Host: "gitlab.example.com", Token: testToken}
	for _, got := range []string{fmt.Sprintf("%v", b), fmt.Sprintf("%+v", b), fmt.Sprintf("%#v", b), fmt.Sprint(b), fmt.Sprintf("%s", b)} {
		if strings.Contains(got, testToken) || strings.Contains(got, testToken[:12]) {
			t.Errorf("formatted Binding %q shows the token", got)
		}
		if !strings.Contains(got, "Corp") {
			t.Errorf("formatted Binding %q lost its name", got)
		}
	}
	if got := fmt.Sprint(gitlab.Binding{}); !strings.Contains(got, `Token: ""`) {
		t.Errorf("formatted empty Binding = %q, want an empty token marker", got)
	}
}
