package pullshttp_test

import (
	"net/http"
	"testing"
)

const (
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
	attackComment = `{"path":"a.go","line":1,"body":"pwned"}`
)

func TestPostComment_PlainTextFromForeignOriginIsRefused(t *testing.T) {
	ev := newEnv(t, "tok")
	resp := ev.do(http.MethodPost, "/pulls/"+ev.prID+"/comments", attackComment,
		"Content-Type", "text/plain;charset=UTF-8", "Origin", foreignOrigin)
	if resp.Status != http.StatusForbidden {
		t.Errorf("status = %d (%s), want 403", resp.Status, resp.Raw)
	}
	if n := len(ev.stub.requests(".*")); n != 0 {
		t.Errorf("GitHub requests = %d, want 0", n)
	}
}

func TestPostComment_PlainTextIsAStringNotAnObject(t *testing.T) {
	for _, headers := range [][]string{nil, {"Origin", uiOrigin}} {
		ev := newEnv(t, "tok")
		resp := ev.do(http.MethodPost, "/pulls/"+ev.prID+"/comments", attackComment,
			append([]string{"Content-Type", "text/plain"}, headers...)...)
		resp.wantError(t, http.StatusUnprocessableEntity, "validation_error", "Request validation failed")
		var body struct {
			Error struct {
				Details []struct {
					Message string `json:"message"`
				} `json:"details"`
			} `json:"error"`
		}
		resp.decode(t, &body)
		if len(body.Error.Details) != 1 || body.Error.Details[0].Message != "Expected object, received string" {
			t.Errorf("details = %+v, want one issue \"Expected object, received string\"", body.Error.Details)
		}
		if n := len(ev.stub.requests(".*")); n != 0 {
			t.Errorf("GitHub requests = %d, want 0", n)
		}
	}
}

func TestPostComment_JSONFromOwnOriginReachesTheService(t *testing.T) {
	for _, headers := range [][]string{nil, {"Origin", uiOrigin}} {
		ev := newEnv(t, "tok")
		resp := ev.do(http.MethodPost, "/pulls/"+ev.prID+"/comments", attackComment, headers...)
		// The stub has no comment endpoint; reaching GitHub is what matters here.
		if resp.Status == http.StatusForbidden || resp.Status == http.StatusUnprocessableEntity {
			t.Errorf("headers %v: status = %d (%s), want the request to reach the service", headers, resp.Status, resp.Raw)
		}
		if n := len(ev.stub.requests(".*")); n == 0 {
			t.Errorf("headers %v: GitHub requests = 0, want the comment to be sent", headers)
		}
	}
}

func TestPoll_ForeignOriginIsRefused(t *testing.T) {
	ev := newEnv(t, "tok")
	resp := ev.do(http.MethodPost, "/repos/"+ev.repoID+"/poll", "", "Content-Type", "text/plain", "Origin", foreignOrigin)
	if resp.Status != http.StatusForbidden {
		t.Errorf("status = %d (%s), want 403", resp.Status, resp.Raw)
	}
	if n := len(ev.stub.requests(".*")); n != 0 {
		t.Errorf("GitHub requests = %d, want 0", n)
	}
	assertNotPolled(t, ev)
	if resp := ev.do(http.MethodPost, "/repos/"+ev.repoID+"/poll", "", "Origin", uiOrigin); resp.Status == http.StatusForbidden {
		t.Errorf("poll from the UI origin = 403, want it served")
	}
}
