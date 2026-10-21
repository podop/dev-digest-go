package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHeadMirrorsGet(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"registered GET route", "/repos", http.StatusOK},
		{"unknown route", "/nope", http.StatusNotFound},
		{"GET route that fails", "/apperr/conflict", http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			newTestEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Errorf("HEAD %s status = %d, want %d", tc.path, rec.Code, tc.wantStatus)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("HEAD %s body = %q, want empty", tc.path, rec.Body.String())
			}
		})
	}
}
