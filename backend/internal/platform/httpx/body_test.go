package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/httpx"
)

const bodyLimit = 1 << 20

// jsonPadded returns a valid JSON object of exactly n bytes with one long string value.
func jsonPadded(n int) string {
	const prefix, suffix = `{"pad":"`, `"}`
	return prefix + strings.Repeat("a", n-len(prefix)-len(suffix)) + suffix
}

func TestBodyRules(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		noBody      bool // send a nil body (no Content-Length)
		wantStatus  int
		wantCode    string // empty for success
		wantMsg     string
	}{
		{
			name: "over limit by one byte", method: http.MethodPut, path: "/settings", contentType: "application/json",
			body: jsonPadded(bodyLimit + 1), wantStatus: http.StatusRequestEntityTooLarge,
			wantCode: "internal_error", wantMsg: "Request body is too large",
		},
		{
			name: "exactly at limit", method: http.MethodPut, path: "/settings", contentType: "application/json",
			body: jsonPadded(bodyLimit), wantStatus: http.StatusOK,
		},
		{
			name: "invalid json", method: http.MethodPut, path: "/settings", contentType: "application/json",
			body: "{bad", wantStatus: http.StatusBadRequest,
			wantCode: "internal_error", wantMsg: "Body is not valid JSON but content-type is set to 'application/json'",
		},
		{
			name: "empty json body", method: http.MethodPut, path: "/settings", contentType: "application/json",
			body: "", wantStatus: http.StatusBadRequest,
			wantCode: "internal_error", wantMsg: "Body cannot be empty when content-type is set to 'application/json'",
		},
		{
			name: "json with charset", method: http.MethodPut, path: "/settings", contentType: "application/json; charset=utf-8",
			body: `{"a":1}`, wantStatus: http.StatusOK,
		},
		{
			name: "form content type", method: http.MethodPut, path: "/settings", contentType: "application/x-www-form-urlencoded",
			body: "a=1", wantStatus: http.StatusUnsupportedMediaType,
			wantCode: "internal_error", wantMsg: "Unsupported Media Type",
		},
		{
			name: "unsupported type even when empty", method: http.MethodPost, path: "/echo", contentType: "application/xml",
			body: "", wantStatus: http.StatusUnsupportedMediaType,
			wantCode: "internal_error", wantMsg: "Unsupported Media Type",
		},
		{
			name: "non-empty body without content type", method: http.MethodPost, path: "/echo",
			body: `{"a":1}`, wantStatus: http.StatusUnsupportedMediaType,
			wantCode: "internal_error", wantMsg: "Unsupported Media Type",
		},
		{
			name: "text plain accepted", method: http.MethodPost, path: "/echo", contentType: "text/plain",
			body: "hello", wantStatus: http.StatusOK,
		},
		{
			name: "text plain empty accepted", method: http.MethodPost, path: "/echo", contentType: "text/plain",
			body: "", wantStatus: http.StatusOK,
		},
		{
			name: "text plain over limit", method: http.MethodPost, path: "/echo", contentType: "text/plain",
			body: strings.Repeat("a", bodyLimit+1), wantStatus: http.StatusRequestEntityTooLarge,
			wantCode: "internal_error", wantMsg: "Request body is too large",
		},
		{
			name: "POST without body or content type", method: http.MethodPost, path: "/echo",
			noBody: true, wantStatus: http.StatusOK,
		},
		{
			name: "DELETE without body or content type", method: http.MethodDelete, path: "/echo",
			noBody: true, wantStatus: http.StatusOK,
		},
		{
			name: "unmatched route is a 404 whatever the body", method: http.MethodPost, path: "/nope",
			contentType: "application/x-www-form-urlencoded", body: "a=1", wantStatus: http.StatusNotFound,
			wantCode: "not_found", wantMsg: "Route POST /nope not found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body *strings.Reader
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if !tc.noBody {
				body = strings.NewReader(tc.body)
				req = httptest.NewRequest(tc.method, tc.path, body)
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			newEcho(t, httpx.Options{}).ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s status = %d, want %d (body %.200s)", tc.method, tc.path, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode == "" {
				return
			}
			var got httpx.ErrorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body %q: %v", rec.Body.String(), err)
			}
			want := httpx.ErrorDetail{Code: tc.wantCode, Message: tc.wantMsg}
			if diff := cmp.Diff(want, got.Error); diff != "" {
				t.Errorf("%s %s error mismatch (-want +got):\n%s", tc.method, tc.path, diff)
			}
		})
	}
}

func TestBodyReachesHandlerIntact(t *testing.T) {
	const payload = `{"a": [1, 2, 3], "b": "é"}`
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newEcho(t, httpx.Options{}).ServeHTTP(rec, req)

	var got struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if got.Body != payload {
		t.Errorf("handler saw body %q, want %q", got.Body, payload)
	}
}

func TestBodyIgnoredOnGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ok", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newEcho(t, httpx.Options{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /ok with an invalid JSON body status = %d, want 200", rec.Code)
	}
}
