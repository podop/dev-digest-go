package main

// The lines the runner prints outside the use case in GitLab mode (security r1 L2): a panic, an
// unusable extra-CA bundle and the model layer's log records pass the §7.5 redaction, the FR29
// sanitiser and the 1,200-byte cap; GitHub mode keeps its own LogLine.

import (
	"bytes"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/ci/runner/adapters/env"
	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
)

const (
	// shortToken is below the 8-byte floor of the panic redactor: only the GitLab path redacts it.
	shortToken = "glpat-1"
	hostile    = "\x1b[31mred\x1b[0m section_start:1:x\r\nsecond line \u0085 end"
)

func assertGitLabLine(t *testing.T, stderr string) {
	t.Helper()
	line := strings.TrimSuffix(stderr, "\n")
	if strings.ContainsAny(line, "\x1b\r\n\u0085") {
		t.Errorf("stderr = %q, want one line without control characters", stderr)
	}
	for _, bad := range []string{shortToken, "section_start:", "section_end:"} {
		if strings.Contains(stderr, bad) {
			t.Errorf("stderr = %q, want no %q", stderr, bad)
		}
	}
	if len(line) > runnerapp.MaxFailureBytes {
		t.Errorf("stderr line is %d bytes, want at most %d", len(line), runnerapp.MaxFailureBytes)
	}
}

func TestRun_GitLabPanicLineIsRedactedAndSanitised(t *testing.T) {
	vars := map[string]string{"DEVDIGEST_PLATFORM": "gitlab_ci", "DEVDIGEST_GITLAB_TOKEN": shortToken}
	lookup := func(k string) (string, bool) {
		if k == envExtraCAFile {
			panic("boom " + shortToken + " " + hostile + " " + strings.Repeat("x", 3000))
		}
		v, ok := vars[k]
		return v, ok
	}
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), lookup, &stdout, &stderr, defaultEndpoints()); code != runnerapp.ExitFailed {
		t.Errorf("run() = %d, want %d", code, runnerapp.ExitFailed)
	}
	if !strings.HasPrefix(stderr.String(), "[agent-runner] fatal error: boom [REDACTED] ") {
		t.Fatalf("stderr = %q, want the fatal error line with the token redacted", stderr.String())
	}
	assertGitLabLine(t, stderr.String())
}

func TestRun_GitLabExtraCAFailureLineIsRedactedAndSanitised(t *testing.T) {
	vars := map[string]string{
		"DEVDIGEST_PLATFORM": "gitlab_ci", "DEVDIGEST_GITLAB_TOKEN": shortToken,
		envExtraCAFile: "/missing/section_start:1:x/" + shortToken + "/" + strings.Repeat("d/", 1000) + "ca.pem",
	}
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), envOf(vars), &stdout, &stderr, defaultEndpoints()); code != runnerapp.ExitFailed {
		t.Errorf("run() = %d, want %d", code, runnerapp.ExitFailed)
	}
	if !strings.HasPrefix(stderr.String(), "[agent-runner] FAILED: "+envExtraCAFile+": ") {
		t.Fatalf("stderr = %q, want the extra-CA failure line", stderr.String())
	}
	assertGitLabLine(t, stderr.String())
}

// GitHub mode is unchanged: the same failure keeps its text, cut by LogLine only.
func TestRun_GitHubExtraCAFailureLineIsUnchanged(t *testing.T) {
	path := "/missing/section_start:1:x/ca.pem"
	var stdout, stderr bytes.Buffer
	run(t.Context(), envOf(map[string]string{envExtraCAFile: path}), &stdout, &stderr, defaultEndpoints())
	want := runnerapp.LogLine("[agent-runner] FAILED: ", envExtraCAFile+": read extra CA file \""+path+"\": no such file or directory") + "\n"
	if got := stderr.String(); got != want {
		t.Errorf("GitHub mode stderr = %q, want %q", got, want)
	}
}

func TestGitLabLogWriter(t *testing.T) {
	vars := map[string]string{"DEVDIGEST_PLATFORM": "gitlab_ci", "DEVDIGEST_GITLAB_TOKEN": shortToken}
	var buf bytes.Buffer
	w := gitLabLogWriter{w: &buf, line: func(text string) string {
		return runnerapp.GitLabLine(env.Process{LookupEnv: envOf(vars)}, "", text)
	}}
	record := "level=WARN msg=\"x " + shortToken + " \x1b[2K section_end:1:y\"\n"
	n, err := w.Write([]byte(record))
	if err != nil || n != len(record) {
		t.Fatalf("Write() = %d, %v, want %d, nil", n, err, len(record))
	}
	want := "level=WARN msg=\"x [REDACTED]  1:y\"\n"
	if got := buf.String(); got != want {
		t.Errorf("Write() wrote %q, want %q", got, want)
	}
}
