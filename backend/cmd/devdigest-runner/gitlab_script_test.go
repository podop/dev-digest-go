package main

// The script lines of the generated GitLab job file (spec GitLab C FR7, FR8, AC8): the lines run
// with a real sh and the tools of the job image (sh, base64, gzip, sha256sum) against the runner
// files the export commits. The fixture runner is a small shell program; the lines treat it as an
// opaque executable, so what they prove (decode, verify, run, exit code) holds for the real one.
// The helpers are prefixed gn.

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	cidomain "dev-digest-go/backend/internal/ci/domain"
)

// gnRunnerProgram is the executable the fixture "runner" is: it records where it ran and exits
// with GN_EXIT.
const gnRunnerProgram = "#!/bin/sh\nprintf 'cwd=%s platform=%s args=%s\\n' \"$(pwd -P)\" \"$DEVDIGEST_PLATFORM\" \"$#\" > \"$GN_MARKER\"\nexit \"${GN_EXIT:-0}\"\n"

// gnJobFile is the part of a job file these tests read.
type gnJobFile struct {
	Image struct {
		Name       string   `yaml:"name"`
		Entrypoint []string `yaml:"entrypoint"`
	} `yaml:"image"`
	Script []string `yaml:"script"`
}

func gnJobFileOf(t *testing.T, o cidomain.GitLabJobOptions) gnJobFile {
	t.Helper()
	text, err := cidomain.BuildGitLabJob(o)
	if err != nil {
		t.Fatalf("BuildGitLabJob(%+v) error = %v", o, err)
	}
	var doc map[string]gnJobFile
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("job file is not YAML: %v\n%s", err, text)
	}
	job, ok := doc[cidomain.GitLabJobName(o.Slug)]
	if !ok || len(job.Script) == 0 {
		t.Fatalf("job file has no %s job with a script:\n%s", cidomain.GitLabJobName(o.Slug), text)
	}
	return job
}

// gnRunnerFiles writes the export's runner files for exe into a project directory whose name holds
// a space (CI_PROJECT_DIR is quoted in every line) and returns it.
func gnRunnerFiles(t *testing.T, exe []byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project dir")
	encoded, err := cidomain.EncodeRunner(exe)
	if err != nil {
		t.Fatal(err)
	}
	gnWrite(t, dir, cidomain.RunnerPath, encoded)
	gnWrite(t, dir, cidomain.RunnerSumPath, cidomain.RunnerSumFile(cidomain.RunnerSHA256(exe)))
	return dir
}

// gnScriptResult is one run of the script lines.
type gnScriptResult struct {
	exit   int
	output string
	marker string // what the runner recorded; "" when it never ran
}

// gnRunScript runs the lines the way a GitLab shell executor does for an sh image: one shell with
// -e, started outside the project directory, with CI_PROJECT_DIR and CI_JOB_ID set.
func gnRunScript(t *testing.T, lines []string, projectDir string, runnerExit int) gnScriptResult {
	t.Helper()
	jobID := fmt.Sprintf("%d", 1_000_000_000+rand.Int64N(1_000_000_000))
	t.Cleanup(func() { _ = os.RemoveAll("/tmp/devdigest-" + jobID) })
	marker := filepath.Join(t.TempDir(), "runner.marker")
	cmd := exec.Command("sh", "-e", "-c", strings.Join(lines, "\n"))
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"PATH=" + gnOrigPath(), "HOME=" + cmd.Dir, "CI_PROJECT_DIR=" + projectDir, "CI_JOB_ID=" + jobID,
		"DEVDIGEST_PLATFORM=gitlab_ci", "GN_MARKER=" + marker, fmt.Sprintf("GN_EXIT=%d", runnerExit),
	}
	out, err := cmd.CombinedOutput()
	res := gnScriptResult{output: string(out)}
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("sh could not run: %v", err)
		}
		res.exit = ee.ExitCode()
	}
	if b, err := os.ReadFile(marker); err == nil {
		res.marker = string(b)
	}
	return res
}

var gnImagePin = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// AC8, first clause: the job runs the one pinned image, by digest, with the entrypoint cleared.
func TestGitLabJobScript_ImageIsPinnedByDigest(t *testing.T) {
	job := gnJobFileOf(t, cidomain.GitLabJobOptions{Slug: gnSlug, PostAs: cidomain.PostAsGitHubReview})
	if job.Image.Name != cidomain.JobImage || !gnImagePin.MatchString(job.Image.Name) {
		t.Errorf("image = %q, want the build's JobImage %q pinned by @sha256:<64 hex>", job.Image.Name, cidomain.JobImage)
	}
	if len(job.Image.Entrypoint) != 1 || job.Image.Entrypoint[0] != "" {
		t.Errorf("image entrypoint = %q, want [\"\"]", job.Image.Entrypoint)
	}
}

// AC8, second clause: the script lines decode the runner, verify its checksum and reach it; the job
// then ends with the runner's own exit code (exit 1 is how the gate fails the job).
func TestGitLabJobScript_ReachesTheRunnerAndKeepsItsExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts cidomain.GitLabJobOptions
	}{
		{"openrouter on shared runners", cidomain.GitLabJobOptions{Slug: gnSlug, PostAs: cidomain.PostAsGitHubReview}},
		{"custom provider on a self-managed runner", cidomain.GitLabJobOptions{
			Slug: "my-gw", PostAs: cidomain.PostAsNone, Provider: "custom:my-gw", Runner: cidomain.RunnerSelfHosted, HeaderCount: 2,
		}},
	} {
		job := gnJobFileOf(t, tc.opts)
		for _, line := range job.Script {
			if strings.Contains(line, "|") || strings.Contains(line, ";") && !strings.Contains(line, "&&") {
				t.Errorf("%s: script line %q pipes or chains without &&; a failing stage would not stop the job under set -e", tc.name, line)
			}
		}
		for _, exit := range []int{0, 1, 3} {
			t.Run(fmt.Sprintf("%s/runner exits %d", tc.name, exit), func(t *testing.T) {
				dir := gnRunnerFiles(t, []byte(gnRunnerProgram))
				res := gnRunScript(t, job.Script, dir, exit)
				if res.exit != exit {
					t.Errorf("script exit = %d, want the runner's %d\n%s", res.exit, exit, res.output)
				}
				wantCwd, err := filepath.EvalSymlinks(dir)
				if err != nil {
					t.Fatal(err)
				}
				if want := "cwd=" + wantCwd + " platform=gitlab_ci args=0\n"; res.marker != want {
					t.Errorf("the runner recorded %q, want %q (run in CI_PROJECT_DIR, no arguments)", res.marker, want)
				}
				if _, err := os.Stat(filepath.Join(dir, cidomain.RunnerBinaryName)); err == nil {
					t.Errorf("the decoded runner was written into CI_PROJECT_DIR, want it in /tmp/devdigest-<job id>")
				}
			})
		}
	}
}

// AC8, third clause: a runner file with one changed byte stops at the checksum line, and so does
// every other way the files can be wrong: the runner never runs.
func TestGitLabJobScript_StopsBeforeTheRunnerWhenTheFilesAreWrong(t *testing.T) {
	job := gnJobFileOf(t, cidomain.GitLabJobOptions{Slug: gnSlug, PostAs: cidomain.PostAsGitHubReview})
	exe := []byte(gnRunnerProgram)
	goodSum := cidomain.RunnerSumFile(cidomain.RunnerSHA256(exe))

	tampered := append([]byte(nil), exe...)
	tampered[len(tampered)-3] ^= 0x01 // one byte of the executable, then a valid encoding of it
	tamperedEnc, err := cidomain.EncodeRunner(tampered)
	if err != nil {
		t.Fatal(err)
	}
	goodEnc, err := cidomain.EncodeRunner(exe)
	if err != nil {
		t.Fatal(err)
	}
	otherSum := cidomain.RunnerSumFile(strings.Repeat("0", 64))

	for _, tc := range []struct {
		name     string
		encoded  *string // content of the runner file; nil = file absent
		sum      *string // content of the checksum file; nil = file absent
		wantInfo string  // part of the tool output that shows which line stopped the job
	}{
		{"one changed byte of the runner", &tamperedEnc, &goodSum, "FAILED"},
		{"a checksum of another file", &goodEnc, &otherSum, "FAILED"},
		{"a checksum line with a changed digit", &goodEnc, gnPtr(strings.Replace(goodSum, goodSum[:1], gnFlipHex(goodSum[:1]), 1)), "FAILED"},
		{"an empty checksum file", &goodEnc, gnPtr(""), "no properly formatted"},
		{"a checksum file for another name", &goodEnc, gnPtr(strings.Replace(goodSum, cidomain.RunnerBinaryName, "other-runner", 1)), "other-runner"},
		{"a checksum file that is not a checksum file", &goodEnc, gnPtr("hello\n"), "no properly formatted"},
		{"a missing checksum file", &goodEnc, nil, "sha256"},
		{"a missing runner file", nil, &goodSum, "devdigest-runner-linux-amd64.gz.b64"},
		{"a runner file that is not base64", gnPtr("!!! not base64 !!!\n"), &goodSum, "base64"},
		{"a runner file that is base64 of something that is not gzip", gnPtr("aGVsbG8gd29ybGQK\n"), &goodSum, "gzip"},
		{"a truncated runner file", gnPtr(goodEnc[:len(goodEnc)/2] + "\n"), &goodSum, ""},
		{"an empty runner file", gnPtr(""), &goodSum, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "project dir")
			if tc.encoded != nil {
				gnWrite(t, dir, cidomain.RunnerPath, *tc.encoded)
			}
			if tc.sum != nil {
				gnWrite(t, dir, cidomain.RunnerSumPath, *tc.sum)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			res := gnRunScript(t, job.Script, dir, 0)
			if res.exit == 0 {
				t.Errorf("script exit = 0, want a failure before the runner\n%s", res.output)
			}
			if res.marker != "" {
				t.Errorf("the runner ran (%q), want the job stopped before it\n%s", res.marker, res.output)
			}
			if tc.wantInfo != "" && !strings.Contains(res.output, tc.wantInfo) {
				t.Errorf("tool output = %q, want it to show %q", res.output, tc.wantInfo)
			}
		})
	}
}

// the changed-byte case must stop at the checksum line itself, not earlier: decoding the tampered
// file succeeds and the tool names the mismatch.
func TestGitLabJobScript_OneChangedByteIsCaughtByTheChecksum(t *testing.T) {
	job := gnJobFileOf(t, cidomain.GitLabJobOptions{Slug: gnSlug, PostAs: cidomain.PostAsGitHubReview})
	exe := []byte(gnRunnerProgram)
	tampered := append([]byte(nil), exe...)
	tampered[len(tampered)-3] ^= 0x01
	dir := gnRunnerFiles(t, exe)
	enc, err := cidomain.EncodeRunner(tampered)
	if err != nil {
		t.Fatal(err)
	}
	gnWrite(t, dir, cidomain.RunnerPath, enc)
	res := gnRunScript(t, job.Script, dir, 0)
	if res.exit == 0 || res.marker != "" {
		t.Fatalf("exit %d, runner ran %q; want a failure and no run\n%s", res.exit, res.marker, res.output)
	}
	if !strings.Contains(res.output, cidomain.RunnerBinaryName+": FAILED") || !strings.Contains(res.output, "did NOT match") {
		t.Errorf("tool output = %q, want sha256sum's mismatch for %s", res.output, cidomain.RunnerBinaryName)
	}
}

func gnPtr[T any](v T) *T { return &v }

// flipHex returns another hex digit.
func gnFlipHex(d string) string {
	if d == "0" {
		return "1"
	}
	return "0"
}
