package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// forbidden lists the import-path prefixes the runner's closure must not contain (spec AC6048):
// the HTTP framework, the ORM and database drivers, the git adapter, the secrets-file store and
// tree-sitter.
var forbidden = []string{
	"github.com/labstack/echo",
	"gorm.io/",
	"github.com/jackc/",
	"github.com/lib/pq",
	"github.com/tree-sitter/",
	"database/sql",
	"dev-digest-go/backend/internal/platform/gitcli",
	"dev-digest-go/backend/internal/platform/secrets",
	"dev-digest-go/backend/internal/platform/postgres",
	"dev-digest-go/backend/internal/repointel",
}

func goTool(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return runtime.GOROOT() + "/bin/go"
}

func goCmd(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), goTool(t), args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRunnerClosure(t *testing.T) {
	// cgo on, so a package of the closure that needs it shows its CgoFiles; the standard
	// library's own cgo files (net, os/user) are not the runner's concern.
	out, err := goCmd(t, []string{"CGO_ENABLED=1", "GOOS=linux", "GOARCH=amd64"},
		"list", "-deps", "-f", "{{.ImportPath}}\t{{.Standard}}\t{{len .CgoFiles}}", ".")
	if err != nil {
		t.Fatalf("go list -deps . = %v\n%s", err, out)
	}
	var sawEngine, sawLLM bool
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			t.Fatalf("go list line %q: want 3 fields", line)
		}
		path, std, cgo := f[0], f[1] == "true", f[2] != "0"
		for _, bad := range forbidden {
			if strings.HasPrefix(path, bad) {
				t.Errorf("runner closure contains %s (forbidden prefix %s)", path, bad)
			}
		}
		if cgo && !std {
			t.Errorf("runner closure contains cgo package %s", path)
		}
		sawEngine = sawEngine || path == "dev-digest-go/backend/internal/reviewer/app"
		sawLLM = sawLLM || path == "dev-digest-go/backend/internal/llm/adapters/openaicompat"
	}
	if !sawEngine || !sawLLM {
		t.Errorf("runner closure lacks the review engine (%v) or the OpenRouter adapter (%v)", sawEngine, sawLLM)
	}
}

func TestRunnerBuildsStatic(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the runner for linux/amd64")
	}
	out, err := goCmd(t, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"},
		"build", "-trimpath", "-buildvcs=false", "-o", os.DevNull, ".")
	if err != nil {
		t.Fatalf("CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build . = %v\n%s", err, out)
	}
}
