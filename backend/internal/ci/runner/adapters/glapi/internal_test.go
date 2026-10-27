package glapi

import (
	"net/url"
	"testing"
	"time"
)

func TestClient_PerCallTimeoutIs30sAndRedirectsAreOff(t *testing.T) {
	c, err := New(Options{APIURL: "https://gitlab.example/api/v4"})
	if err != nil {
		t.Fatal(err)
	}
	if c.http.Timeout != 30*time.Second {
		t.Errorf("per-call timeout = %v, want 30s", c.http.Timeout)
	}
	if c.http.CheckRedirect == nil || c.http.CheckRedirect(nil, nil) == nil {
		t.Error("CheckRedirect must refuse every redirect")
	}
}

func TestClient_ConfinedToOriginAndPrefix(t *testing.T) {
	c, err := New(Options{APIURL: "https://gitlab.example:8443/gl/api/v4"})
	if err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]bool{
		"https://gitlab.example:8443/gl/api/v4/projects/1":    true,
		"https://gitlab.example:8443/gl/api/v4":               true,
		"https://gitlab.example:8443/gl/api/v42/projects/1":   false,
		"https://gitlab.example:8443/other/projects/1":        false,
		"https://gitlab.example/gl/api/v4/projects/1":         false,
		"http://gitlab.example:8443/gl/api/v4/projects/1":     false,
		"https://evil.example:8443/gl/api/v4/projects/1":      false,
		"https://gitlab.example:8443@evil.example/gl/api/v4/": false,
	} {
		u := mustParse(t, target)
		if got := c.confined(u); got != want {
			t.Errorf("confined(%s) = %v, want %v", target, got, want)
		}
	}
}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
