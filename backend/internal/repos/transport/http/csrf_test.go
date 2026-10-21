package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"dev-digest-go/backend/internal/platform/httpx"
)

const (
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
	// attackAdd is what a cross-site page can post without a CORS preflight (text/plain).
	attackAdd = `{"url":"https://github.com/attacker/x"}`
)

func (v *env) assertUntouched(t *testing.T) {
	t.Helper()
	if n := v.count(t, "repos"); n != 0 {
		t.Errorf("repo rows = %d, want 0", n)
	}
	if len(v.queue.jobs) != 0 {
		t.Errorf("jobs = %v, want none", v.queue.kinds())
	}
}

func TestAdd_PlainTextFromForeignOriginIsRefused(t *testing.T) {
	v := newEnv(t)
	res := v.do(t, http.MethodPost, "/repos", attackAdd, "Content-Type", "text/plain;charset=UTF-8", "Origin", foreignOrigin)
	if res.status != http.StatusForbidden {
		t.Errorf("POST /repos status = %d (%s), want 403", res.status, res.body)
	}
	v.assertUntouched(t)
}

func TestAdd_PlainTextIsAStringNotAnObject(t *testing.T) {
	for _, headers := range [][]string{nil, {"Origin", uiOrigin}} {
		v := newEnv(t)
		res := v.do(t, http.MethodPost, "/repos", attackAdd, append([]string{"Content-Type", "text/plain"}, headers...)...)
		if res.status != http.StatusUnprocessableEntity {
			t.Fatalf("POST /repos (headers %v) status = %d (%s), want 422", headers, res.status, res.body)
		}
		var got httpx.ErrorBody
		if err := json.Unmarshal([]byte(res.body), &got); err != nil {
			t.Fatal(err)
		}
		details, _ := got.Error.Details.([]any)
		if len(details) != 1 || details[0].(map[string]any)["message"] != "Expected object, received string" {
			t.Errorf("details = %v, want one issue \"Expected object, received string\"", got.Error.Details)
		}
		v.assertUntouched(t)
	}
}

func TestAdd_JSONFromOwnOriginWorks(t *testing.T) {
	for _, headers := range [][]string{nil, {"Origin", uiOrigin}} {
		v := newEnv(t)
		res := v.do(t, http.MethodPost, "/repos", attackAdd, append([]string{"Content-Type", "application/json; charset=utf-8"}, headers...)...)
		if res.status != http.StatusCreated {
			t.Errorf("POST /repos (headers %v) status = %d (%s), want 201", headers, res.status, res.body)
		}
	}
}

// The routes without a body must not run for a foreign page either, whatever Content-Type it
// picks; the UI's own origin and body-less callers keep working.
func TestBodylessRoutes_ForeignOriginIsRefused(t *testing.T) {
	v := newEnv(t)
	id := decode[map[string]any](t, post(t, v, "https://github.com/o/r"))["id"].(string)
	v.queue.reset()

	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/repos/" + id + "/refresh"},
		{http.MethodDelete, "/repos/" + id},
	} {
		res := v.do(t, req.method, req.path, "", "Content-Type", "text/plain", "Origin", foreignOrigin)
		if res.status != http.StatusForbidden {
			t.Errorf("%s %s from a foreign origin = %d (%s), want 403", req.method, req.path, res.status, res.body)
		}
	}
	if n := v.count(t, "repos"); n != 1 {
		t.Errorf("repo rows = %d, want 1 (delete refused)", n)
	}
	if len(v.queue.jobs) != 0 {
		t.Errorf("jobs = %v, want none", v.queue.kinds())
	}

	if res := v.do(t, http.MethodPost, "/repos/"+id+"/refresh", "", "Origin", uiOrigin); res.status != http.StatusOK {
		t.Errorf("refresh from the UI origin = %d (%s), want 200", res.status, res.body)
	}
	if res := v.do(t, http.MethodDelete, "/repos/"+id, ""); res.status != http.StatusOK {
		t.Errorf("delete without Origin = %d (%s), want 200", res.status, res.body)
	}
}
