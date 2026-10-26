package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
)

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "abc", 3},
		{"bmp non-ascii", "é中", 2},
		{"emoji is a surrogate pair", "😀", 2},
		{"ten emoji", strings.Repeat("😀", 10), 20},
		{"mixed", "a😀b", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := httpx.UTF16Len(tc.in); got != tc.want {
				t.Errorf("UTF16Len(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestJSTrim(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"spaces", "  a b \t\n", "a b"},
		{"nbsp and ideographic space", " 　x　 ", "x"},
		{"bom", "\xef\xbb\xbfx\xef\xbb\xbf", "x"},
		{"next line is not trimmed", "\u0085x", "\u0085x"},
		{"line separator", " x ", "x"},
		{"only white space", " \t ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := httpx.JSTrim(tc.in); got != tc.want {
				t.Errorf("JSTrim(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const slugMessage = "Use a kebab-case slug: lowercase letters, digits and single dashes"

func TestStringRuleCheck(t *testing.T) {
	name := httpx.StringRule{Min: 1, Max: 64, Pattern: slugPattern, PatternMessage: slugMessage}
	desc := httpx.StringRule{Max: 300}
	rule := httpx.StringRule{Trim: true, Min: 1, Max: 500}

	tests := []struct {
		name      string
		rule      httpx.StringRule
		in        string
		wantValue string
		want      []httpx.Issue
	}{
		{name: "name 64 chars", rule: name, in: strings.Repeat("a", 64), wantValue: strings.Repeat("a", 64)},
		{name: "name 65 chars", rule: name, in: strings.Repeat("a", 65), wantValue: strings.Repeat("a", 65), want: []httpx.Issue{
			{Code: "too_big", Path: []any{"name"}, Message: "String must contain at most 64 character(s)"},
		}},
		{name: "name empty reports min and pattern", rule: name, in: "", want: []httpx.Issue{
			{Code: "too_small", Path: []any{"name"}, Message: "String must contain at least 1 character(s)"},
			{Code: "invalid_string", Path: []any{"name"}, Message: slugMessage},
		}},
		{name: "name double dash", rule: name, in: "a--b", wantValue: "a--b", want: []httpx.Issue{
			{Code: "invalid_string", Path: []any{"name"}, Message: slugMessage},
		}},
		{name: "description 300 units with 10 emoji", rule: desc, in: strings.Repeat("a", 280) + strings.Repeat("😀", 10), wantValue: strings.Repeat("a", 280) + strings.Repeat("😀", 10)},
		{name: "description 301 units", rule: desc, in: strings.Repeat("a", 281) + strings.Repeat("😀", 10), wantValue: strings.Repeat("a", 281) + strings.Repeat("😀", 10), want: []httpx.Issue{
			{Code: "too_big", Path: []any{"name"}, Message: "String must contain at most 300 character(s)"},
		}},
		{name: "default pattern message", rule: httpx.StringRule{Pattern: slugPattern}, in: "A", wantValue: "A", want: []httpx.Issue{
			{Code: "invalid_string", Path: []any{"name"}, Message: "Invalid"},
		}},
		{name: "trim then length", rule: rule, in: "  rule  ", wantValue: "rule"},
		{name: "blank after trim", rule: rule, in: " \t ", want: []httpx.Issue{
			{Code: "too_small", Path: []any{"name"}, Message: "String must contain at least 1 character(s)"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotValue, got := tc.rule.Check(tc.in, "name")
			if gotValue != tc.wantValue {
				t.Errorf("Check(%q) value = %q, want %q", tc.in, gotValue, tc.wantValue)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Check(%q) issues mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

// fieldsEcho serves the helpers under test.
func fieldsEcho(t *testing.T) *echo.Echo {
	t.Helper()
	e := newEcho(t, httpx.Options{})
	e.GET("/versions/:version", func(c *echo.Context) error {
		v, err := httpx.PathVersion(c, "version")
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]int{"version": v})
	})
	e.GET("/providers/:id/models", func(c *echo.Context) error {
		v, err := httpx.PathEnum(c, "id", []string{"openai", "anthropic", "openrouter"})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"id": v})
	})
	e.GET("/search", func(c *echo.Context) error {
		q, issues := httpx.QueryString(c, "q", httpx.StringRule{Max: 200})
		if issues != nil {
			return httpx.NewValidationError(issues...)
		}
		return c.JSON(http.StatusOK, map[string]string{"q": q})
	})
	e.POST("/refine", func(c *echo.Context) error {
		return httpx.NewValidationError(httpx.RefineIssue("Provide either a or b"))
	})
	return e
}

func TestPathVersion(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		wantStatus int
		wantMsg    string
	}{
		{"one", "1", http.StatusOK, ""},
		{"max int32", "2147483647", http.StatusOK, ""},
		{"leading zeros", "007", http.StatusOK, ""},
		{"zero", "0", http.StatusUnprocessableEntity, "Number must be greater than 0"},
		{"above int32", "2147483648", http.StatusUnprocessableEntity, "Number must be less than or equal to 2147483647"},
		{"huge", "99999999999999999999999", http.StatusUnprocessableEntity, "Number must be less than or equal to 2147483647"},
		{"float", "1.5", http.StatusUnprocessableEntity, "Expected integer, received float"},
		{"exponent", "1e2", http.StatusUnprocessableEntity, "Expected number, received nan"},
		{"hex", "0x10", http.StatusUnprocessableEntity, "Expected number, received nan"},
		{"negative", "-1", http.StatusUnprocessableEntity, "Expected number, received nan"},
		{"plus", "+1", http.StatusUnprocessableEntity, "Expected number, received nan"},
		{"letters", "abc", http.StatusUnprocessableEntity, "Expected number, received nan"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			fieldsEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/versions/"+url.PathEscape(tc.version), nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET /versions/%s status = %d, want %d (%.200s)", tc.version, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantMsg == "" {
				return
			}
			d := decodeDetails(t, rec)
			if len(d) != 1 || d[0].InstancePath != "/version" || d[0].Message != tc.wantMsg {
				t.Errorf("GET /versions/%s details = %+v, want one issue at /version %q", tc.version, d, tc.wantMsg)
			}
		})
	}
}

func TestPathEnum(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{"openai", "openai", http.StatusOK},
		{"anthropic", "anthropic", http.StatusOK},
		{"openrouter", "openrouter", http.StatusOK},
		{"unknown", "foo", http.StatusUnprocessableEntity},
		{"wrong case", "OpenAI", http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			fieldsEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/providers/"+tc.id+"/models", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET /providers/%s/models status = %d, want %d", tc.id, rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK {
				return
			}
			d := decodeDetails(t, rec)
			wantMsg := "Invalid enum value. Expected 'openai' | 'anthropic' | 'openrouter', received '" + tc.id + "'"
			if len(d) != 1 || d[0].Keyword != "invalid_enum_value" || d[0].InstancePath != "/id" || d[0].Message != wantMsg {
				t.Errorf("details = %+v, want one invalid_enum_value at /id %q", d, wantMsg)
			}
		})
	}
}

func TestQueryString(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantQ      string
	}{
		{"absent", "", http.StatusOK, ""},
		{"present", "q=react", http.StatusOK, "react"},
		{"empty value", "q=", http.StatusOK, ""},
		{"200 units", "q=" + strings.Repeat("a", 200), http.StatusOK, strings.Repeat("a", 200)},
		{"201 units", "q=" + strings.Repeat("a", 201), http.StatusUnprocessableEntity, ""},
		{"100 emoji are 200 units", "q=" + url.QueryEscape(strings.Repeat("😀", 100)), http.StatusOK, strings.Repeat("😀", 100)},
		{"101 emoji are 202 units", "q=" + url.QueryEscape(strings.Repeat("😀", 101)), http.StatusUnprocessableEntity, ""},
		{"unknown keys ignored", "other=1&q=x", http.StatusOK, "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			fieldsEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?"+tc.query, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET /search?%.60s status = %d, want %d", tc.query, rec.Code, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				d := decodeDetails(t, rec)
				if len(d) != 1 || d[0].InstancePath != "/q" || d[0].Keyword != "too_big" {
					t.Errorf("details = %+v, want one too_big at /q", d)
				}
				return
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if got["q"] != tc.wantQ {
				t.Errorf("q = %q, want %q", got["q"], tc.wantQ)
			}
		})
	}
}

func TestRefineIssueIsAtRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refine", nil)
	fieldsEcho(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /refine status = %d, want 422", rec.Code)
	}
	d := decodeDetails(t, rec)
	if len(d) != 1 || d[0].InstancePath != "/" || d[0].Message != "Provide either a or b" || d[0].Keyword != "custom" {
		t.Errorf("details = %+v, want one custom issue at instancePath / with the refine message", d)
	}
	if got := d[0].Params.Issue.Path; len(got) != 0 {
		t.Errorf("params.issue.path = %v, want empty", got)
	}
}

func decodeDetails(t *testing.T, rec *httptest.ResponseRecorder) []httpx.IssueDetail {
	t.Helper()
	var body struct {
		Error struct {
			Details []httpx.IssueDetail `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Details
}

func TestPathParam(t *testing.T) {
	e := newEcho(t, httpx.Options{})
	e.GET("/providers/:id/models", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"id": httpx.PathParam(c, "id")})
	})
	tests := []struct {
		name, path, want string
	}{
		{"literal colon", "/providers/custom:synapse/models", "custom:synapse"},
		{"encoded colon", "/providers/custom%3Asynapse/models", "custom:synapse"},
		{"encoded colon lowercase hex", "/providers/custom%3asynapse/models", "custom:synapse"},
		{"plain", "/providers/openai/models", "openai"},
		{"decoded once", "/providers/custom%253Asynapse/models", "custom%3Asynapse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET %s: status %d, body %q: %v", tc.path, rec.Code, rec.Body.String(), err)
			}
			if got := body["id"]; got != tc.want {
				t.Errorf("PathParam for GET %s = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestPathParam_MalformedEscapeStaysRaw(t *testing.T) {
	e := newEcho(t, httpx.Options{})
	e.GET("/providers/:id/models", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"id": httpx.PathParam(c, "id")})
	})
	req := httptest.NewRequest(http.MethodGet, "/providers/x/models", nil)
	req.URL.Path = "/providers/custom%zz/models" // httptest refuses to parse a bad escape
	req.URL.RawPath = "/providers/custom%zz/models"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status %d, body %q: %v", rec.Code, rec.Body.String(), err)
	}
	if got, want := body["id"], "custom%zz"; got != want {
		t.Errorf("PathParam(malformed escape) = %q, want %q", got, want)
	}
}
