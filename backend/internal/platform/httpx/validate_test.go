package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/httpx"
)

func TestPathUUID(t *testing.T) {
	const valid = "3f2b8c1e-7a4d-4e9b-8c1a-2d3e4f5a6b7c"
	tests := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{"lowercase v4", valid, http.StatusOK},
		{"uppercase", "3F2B8C1E-7A4D-4E9B-8C1A-2D3E4F5A6B7C", http.StatusOK},
		{"version 1", "3f2b8c1e-7a4d-1e9b-8c1a-2d3e4f5a6b7c", http.StatusOK},
		{"nil uuid", "00000000-0000-0000-0000-000000000000", http.StatusOK},
		{"not a uuid", "abc", http.StatusUnprocessableEntity},
		{"missing dashes", "3f2b8c1e7a4d4e9b8c1a2d3e4f5a6b7c", http.StatusUnprocessableEntity},
		{"trailing char", valid + "0", http.StatusUnprocessableEntity},
		{"non-hex", "3f2b8c1e-7a4d-4e9b-8c1a-2d3e4f5a6b7z", http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			newTestEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pulls/"+tc.id, nil))
			if rec.Code != tc.wantStatus {
				t.Errorf("GET /pulls/%s status = %d, want %d", tc.id, rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestPathUUIDValidationBody(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pulls/abc", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	want := map[string]any{"error": map[string]any{
		"code":    "validation_error",
		"message": "Request validation failed",
		"details": []any{map[string]any{
			"keyword":      "invalid_string",
			"instancePath": "/id",
			"schemaPath":   "#/id/invalid_string",
			"message":      "Invalid uuid",
			"params": map[string]any{"issue": map[string]any{
				"code": "invalid_string", "path": []any{"id"}, "message": "Invalid uuid",
			}},
		}},
	}}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("GET /pulls/abc status = %d, want 422", rec.Code)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GET /pulls/abc body mismatch (-want +got):\n%s", diff)
	}
}

func TestNewValidationError(t *testing.T) {
	err := httpx.NewValidationError(
		httpx.Issue{Code: "invalid_type", Path: nil, Message: "Expected object, received null"},
		httpx.Issue{Code: "too_small", Path: []any{"items", 0, "name"}, Message: "Too small"},
	)
	if err.Message != "Request validation failed" || err.Code != "validation_error" {
		t.Errorf("NewValidationError = %q/%q, want validation_error/Request validation failed", err.Code, err.Message)
	}
	b, mErr := json.Marshal(err.Details)
	if mErr != nil {
		t.Fatalf("Marshal(details): %v", mErr)
	}
	var got []map[string]any
	if uErr := json.Unmarshal(b, &got); uErr != nil {
		t.Fatalf("Unmarshal(details): %v", uErr)
	}
	paths := [][2]any{{got[0]["instancePath"], got[0]["schemaPath"]}, {got[1]["instancePath"], got[1]["schemaPath"]}}
	want := [][2]any{{"/", "#//invalid_type"}, {"/items/0/name", "#/items/0/name/too_small"}}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("paths mismatch (-want +got):\n%s", diff)
	}
	issuePath := got[1]["params"].(map[string]any)["issue"].(map[string]any)["path"]
	if diff := cmp.Diff([]any{"items", float64(0), "name"}, issuePath); diff != "" {
		t.Errorf("params.issue.path mismatch (-want +got):\n%s", diff)
	}
	whole := got[0]["params"].(map[string]any)["issue"].(map[string]any)["path"]
	if diff := cmp.Diff([]any{}, whole); diff != "" {
		t.Errorf("params.issue.path of whole-body issue mismatch (-want +got):\n%s", diff)
	}
}
