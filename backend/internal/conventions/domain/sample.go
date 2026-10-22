package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Sampling limits (FR243). Lengths are UTF-16 code units, like the Node server.
const (
	SampleTopN          = 12
	ConfigMaxFiles      = 8
	SourceFileMaxChars  = 6_000
	ConfigFileMaxChars  = 3_000
	SampleTotalMaxChars = 90_000
	FileReadMaxBytes    = 512 * 1024
	WalkMaxFiles        = 5_000
	WalkMaxDepth        = 8
)

// SampleKind says why a file is in the sample.
type SampleKind string

// Sample kinds.
const (
	KindConfig SampleKind = "config"
	KindSource SampleKind = "source"
)

// SampledFile is a file read from the clone for the sample.
type SampledFile struct {
	Path    string
	Kind    SampleKind
	Content string
}

// Sample is the rendered sample and the paths that made it in.
type Sample struct {
	Text     string
	Included []string
}

var (
	configBasenames = []string{
		"package.json", "biome.json", "biome.jsonc", ".editorconfig", "pyproject.toml", "ruff.toml",
		"setup.cfg", ".golangci.yml", ".golangci.yaml", "rustfmt.toml", ".rubocop.yml",
		"CONTRIBUTING.md", "AGENTS.md", "CLAUDE.md", "STYLEGUIDE.md",
	}
	configPrefixes   = []string{"tsconfig", ".eslintrc", "eslint.config.", ".prettierrc", "prettier.config."}
	sourceExtensions = []string{
		".ts", ".tsx", ".js", ".jsx", ".mjs", ".py", ".go", ".rs", ".java", ".kt", ".rb", ".php", ".cs", ".swift",
	}
	skipDirs = []string{
		"node_modules", ".git", "dist", "build", "out", "coverage", "vendor", ".next", "target",
		"__pycache__", ".venv", "venv", "migrations", "fixtures", "__snapshots__",
	}

	testRE       = regexp.MustCompile(`(^|/)(__tests__|tests?|spec|e2e)/|\.(test|spec)\.[a-z]+$|_test\.(go|py)$`)
	generatedRE  = regexp.MustCompile(`\.(min|d|generated)\.[a-z]+$|(^|/)generated/`)
	toolConfigRE = regexp.MustCompile(`(^|/)[^/]+\.config\.[a-z]+$`)
)

// SkipDir reports whether the walk never enters a directory of this name: the
// listed build/vendor directories and every name starting with a dot.
func SkipDir(name string) bool {
	return strings.HasPrefix(name, ".") || slices.Contains(skipDirs, name)
}

func basename(path string) string { return path[strings.LastIndexByte(path, '/')+1:] }

func depth(path string) int { return strings.Count(path, "/") }

// IsConfigFile reports whether the path is a config file or rule doc worth
// sampling (root or depth 1 only).
func IsConfigFile(path string) bool {
	if depth(path) > 1 {
		return false
	}
	name := basename(path)
	if slices.Contains(configBasenames, name) {
		return true
	}
	return slices.ContainsFunc(configPrefixes, func(p string) bool { return strings.HasPrefix(name, p) })
}

// PickConfigFiles selects the config files, root ones first, then depth-1 ones,
// ordered by name, at most ConfigMaxFiles.
func PickConfigFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		if IsConfigFile(p) {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int {
		if d := depth(a) - depth(b); d != 0 {
			return d
		}
		return compareName(a, b)
	})
	return head(out, ConfigMaxFiles)
}

// IsSourceFile reports whether the path is a source file that is neither a
// test, nor generated, nor a tool config.
func IsSourceFile(path string) bool {
	if testRE.MatchString(path) || generatedRE.MatchString(path) || toolConfigRE.MatchString(path) {
		return false
	}
	return slices.ContainsFunc(sourceExtensions, func(ext string) bool { return strings.HasSuffix(path, ext) })
}

// PickFallbackSources spreads up to n source files across directories: round
// robin over the directories, shallow ones first, so one big folder does not
// fill the sample.
func PickFallbackSources(paths []string, n int) []string {
	var sources []string
	for _, p := range paths {
		if IsSourceFile(p) {
			sources = append(sources, p)
		}
	}
	slices.SortFunc(sources, compareCodeUnits)

	byDir := map[string][]string{}
	var dirs []string
	for _, p := range sources {
		dir := ""
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			dir = p[:i]
		}
		if _, seen := byDir[dir]; !seen {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], p)
	}
	slices.SortStableFunc(dirs, func(a, b string) int {
		if d := depth(a) - depth(b); d != 0 {
			return d
		}
		return compareName(a, b)
	})

	var out []string
	for round := 0; len(out) < n; round++ {
		added := false
		for _, dir := range dirs {
			if round >= len(byDir[dir]) {
				continue
			}
			out = append(out, byDir[dir][round])
			added = true
			if len(out) >= n {
				break
			}
		}
		if !added {
			break
		}
	}
	return out
}

// NumberLines prefixes every line with its 1-based number, right-aligned to the
// width of the line count (`  12| code`), and truncates to maxChars with a
// trailing `… (truncated, <n> more lines)`.
func NumberLines(content string, maxChars int) string {
	lines := SplitLines(content)
	width := len(strconv.Itoa(len(lines)))
	out := make([]string, 0, len(lines))
	size := 0
	for i, l := range lines {
		line := fmt.Sprintf("%*d| %s", width, i+1, l)
		n := utf16Len(line)
		if size+n+1 > maxChars {
			out = append(out, fmt.Sprintf("… (truncated, %d more lines)", len(lines)-i))
			break
		}
		out = append(out, line)
		size += n + 1
	}
	return strings.Join(out, "\n")
}

// RenderSample renders one `=== FILE: <path> (<kind>) ===` block per file with
// numbered lines within the per-file and total budgets, and reports the paths
// that made it in. The first block that would pass the total budget ends the sample.
func RenderSample(files []SampledFile) Sample {
	var blocks, included []string
	total := 0
	for _, f := range files {
		limit := SourceFileMaxChars
		if f.Kind == KindConfig {
			limit = ConfigFileMaxChars
		}
		block := fmt.Sprintf("=== FILE: %s (%s) ===\n%s", f.Path, f.Kind, NumberLines(f.Content, limit))
		n := utf16Len(block)
		if total+n > SampleTotalMaxChars {
			break
		}
		blocks = append(blocks, block)
		included = append(included, f.Path)
		total += n
	}
	return Sample{Text: strings.Join(blocks, "\n\n"), Included: included}
}
