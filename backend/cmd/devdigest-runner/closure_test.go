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
	// The custom-provider server side: the runner links the custom-provider rules and adapters
	// (llm/custom, the adapters, platform/egress), never the store, the service or their transport.
	"dev-digest-go/backend/internal/customproviders",
	"dev-digest-go/backend/internal/settings",
	"dev-digest-go/backend/internal/agents",
}

// required lists the packages the runner must link now: the review engine, the three LLM adapters
// the manifests can name, the custom-provider rules and the guarded outbound client.
var required = []string{
	"dev-digest-go/backend/internal/reviewer/app",
	"dev-digest-go/backend/internal/llm/adapters/openaicompat",
	"dev-digest-go/backend/internal/llm/adapters/anthropic",
	"dev-digest-go/backend/internal/llm/custom",
	"dev-digest-go/backend/internal/platform/egress",
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
	seen := map[string]bool{}
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
		seen[path] = true
	}
	for _, want := range required {
		if !seen[want] {
			t.Errorf("runner closure lacks %s", want)
		}
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
