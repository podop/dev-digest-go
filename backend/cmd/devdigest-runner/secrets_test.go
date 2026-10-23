package main

// Secrets and the GitHub trust boundary (spec AC6053, AC6054; NFR6004, NFR6005): the keys of the
// environment never reach the job log, the artifact or a posted body, a redirect is never
// followed, and a run that cannot post fails before it talks to anyone.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	keyMarker   = "sk-or-MARKER-0123456789"
	tokenMarker = "ghp_MARKER_0123456789"
)

// echoingOpenRouter answers every call with an error whose message echoes the whole request:
// the Authorization header and the body.
func echoingOpenRouter(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		echo := "request " + r.Method + " " + r.URL.Path + " Authorization: " + r.Header.Get("Authorization") + " body: " + string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": echo, "code": status}})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func assertNoMarkers(t *testing.T, where, text string) {
	t.Helper()
	for _, m := range []string{keyMarker, tokenMarker} {
		if strings.Contains(text, m) {
			t.Errorf("%s contains the secret %q:\n%s", where, m, text)
		}
	}
}

// AC6053, first half: the model layer fails with an error body that echoes the request.
func TestSecrets_ModelErrorEchoingTheRequest(t *testing.T) {
	for _, status := range []int{400, 401, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			w := newWorkspace(t, testManifest, map[string]string{"OPENROUTER_API_KEY": keyMarker, "GITHUB_TOKEN": tokenMarker})
			gh := gitHubOK(t)
			or, calls := echoingOpenRouter(t, status)

			var stdout, stderr bytes.Buffer
			code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})

			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if calls.Load() == 0 {
				t.Fatal("the model was never called: the echo path was not exercised")
			}
			assertNoMarkers(t, "stdout", stdout.String())
			assertNoMarkers(t, "stderr", stderr.String())
			if art, ok := w.artifact(t); ok {
				t.Errorf("an artifact was written for a failed run: %s", art)
			}
			for _, r := range gh.requests() {
				if r.Method == http.MethodPost {
					t.Errorf("a failed run posted %+v", r)
				}
			}
		})
	}
}

// AC6053, second half: GitHub answers 301 to another host; the redirect is not followed (the
// token would go with it), the answer is a failure, and the body that mentions the token is
// redacted.
func TestSecrets_GitHubRedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		_, _ = io.WriteString(w, "diff --git a/x b/x\n")
	}))
	t.Cleanup(other.Close)

	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.URL+"/stolen")
		w.WriteHeader(http.StatusMovedPermanently)
		_, _ = io.WriteString(w, "moved: "+r.Header.Get("Authorization"))
	}))
	t.Cleanup(redirecting.Close)

	w := newWorkspace(t, testManifest, map[string]string{"OPENROUTER_API_KEY": keyMarker, "GITHUB_TOKEN": tokenMarker})
	or := openRouterReply(t, nil)

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: redirecting.URL})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the redirect target received %d requests, want 0", n)
	}
	if !strings.Contains(stderr.String(), "GitHub API error fetching PR diff") || !strings.Contains(stderr.String(), "301 moved: Bearer [REDACTED]") {
		t.Errorf("stderr = %q, want the 301 error with the token redacted", stderr.String())
	}
	assertNoMarkers(t, "stdout", stdout.String())
	assertNoMarkers(t, "stderr", stderr.String())
	if art, ok := w.artifact(t); ok {
		t.Errorf("an artifact was written: %s", art)
	}
}

// A run that succeeds leaves no secret in the artifact, the log or the posted review either.
func TestSecrets_SuccessfulRunLeavesNone(t *testing.T) {
	w := newWorkspace(t, testManifest, map[string]string{
		"OPENROUTER_API_KEY": keyMarker, "GITHUB_TOKEN": tokenMarker, "GITHUB_RUN_ID": "77", "PR_HEAD_SHA": "abc123",
	})
	gh := gitHubOK(t)
	or := openRouterReply(t, []map[string]any{{
		"id": "f1", "severity": "WARNING", "category": "bug", "title": "Edge", "file": "src/config.ts",
		"start_line": 10, "end_line": 10, "rationale": "Consider the edge.", "confidence": 0.8, "kind": "finding",
	}})

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, &stderr)
	}
	assertNoMarkers(t, "stdout", stdout.String())
	assertNoMarkers(t, "stderr", stderr.String())
	art, ok := w.artifact(t)
	if !ok {
		t.Fatal("no artifact")
	}
	assertNoMarkers(t, "artifact", art)
	for _, r := range gh.requests() {
		body, _ := json.Marshal(r.Body)
		assertNoMarkers(t, "posted body", string(body))
		if r.Headers["Authorization"] != "Bearer "+tokenMarker {
			t.Errorf("request %s %s authorization = %q: the token must travel as a bearer", r.Method, r.URL, r.Headers["Authorization"])
		}
	}
}

// The failure line scrubs a secret even when it comes from somewhere no client scrubbed: a
// manifest whose skill slug is the key is printed in the "not a valid file name" message.
func TestSecrets_FailureLineScrubsEnvironmentValues(t *testing.T) {
	manifest := "name: A\nmodel: m\nsystem_prompt: p\nskills: [" + keyMarker + "]\n"
	w := newWorkspace(t, manifest, map[string]string{"OPENROUTER_API_KEY": keyMarker, "GITHUB_TOKEN": tokenMarker})
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), w.lookup, &stdout, &stderr, defaultEndpoints())
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Skill slug '[REDACTED]'") {
		t.Errorf("stderr = %q, want the slug redacted", stderr.String())
	}
	assertNoMarkers(t, "stderr", stderr.String())
}

// A failure line is one line: a value from the repository cannot start a second one.
func TestFailureLineIsOneLine(t *testing.T) {
	manifest := "name: A\nmodel: m\nsystem_prompt: p\nstrategy: \"x\\n::error::injected\"\n"
	w := newWorkspace(t, manifest, nil)
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), w.lookup, &stdout, &stderr, defaultEndpoints()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n"); len(lines) != 1 {
		t.Errorf("stderr has %d lines, want 1: %q", len(lines), stderr.String())
	}
}

// AC6054: a run that has to post but has no token fails before any request and writes nothing.
func TestSecrets_NoTokenFailsBeforeAnyRequest(t *testing.T) {
	for _, mode := range []string{"github_review", "pr_comment", ""} {
		t.Run("post as "+mode, func(t *testing.T) {
			env := map[string]string{"GITHUB_TOKEN": ""}
			if mode != "" {
				env["DEVDIGEST_POST_AS"] = mode
			}
			w := newWorkspace(t, testManifest, env)
			delete(w.env, "GITHUB_TOKEN") // unset, not empty
			gh := gitHubOK(t)
			or, calls := echoingOpenRouter(t, 500)

			var stdout, stderr bytes.Buffer
			code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})

			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			wantMode := mode
			if wantMode == "" {
				wantMode = "github_review"
			}
			if want := "[agent-runner] FAILED: GITHUB_TOKEN is required to post as '" + wantMode + "'"; !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr = %q, want %q", stderr.String(), want)
			}
			if n := len(gh.requests()); n != 0 {
				t.Errorf("GitHub requests = %d, want 0", n)
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("model requests = %d, want 0", n)
			}
			if _, err := os.Stat(w.env["DEVDIGEST_RESULT_PATH"]); !os.IsNotExist(err) {
				t.Errorf("artifact exists (stat error %v), want none", err)
			}
		})
	}
}

// With posting off the token is not needed: the diff is fetched anyway (Node sent an empty
// bearer) and the exit code follows the gate.
func TestSecrets_NoTokenWithPostingOff(t *testing.T) {
	w := newWorkspace(t, testManifest, map[string]string{"DEVDIGEST_POST_AS": "none"})
	delete(w.env, "GITHUB_TOKEN")
	gh := gitHubOK(t)
	or := openRouterReply(t, nil)
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, &stderr)
	}
	if reqs := gh.requests(); len(reqs) != 1 || reqs[0].Method != http.MethodGet {
		t.Errorf("GitHub requests = %+v, want only the diff fetch", reqs)
	}
}
