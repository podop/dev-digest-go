package importgraph

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// Tsconfig limits. A clone is hostile input (NFR3004): every bound below keeps one small tsconfig
// from costing unbounded reads, memory or time.
const (
	// TSConfigName is the tsconfig file read at the clone root.
	TSConfigName = "tsconfig.json"
	// MaxExtendsDepth is the longest `extends` chain followed from the root tsconfig.
	MaxExtendsDepth = 10
	// MaxConfigReads bounds the tsconfig files read for one clone, the root included (a probe
	// for a missing target and its ".json" retry count as one read each). Each file is read and
	// parsed at most once, however many `extends` entries name it.
	MaxConfigReads = 32
	// MaxPathsPatterns is the number of `paths` keys kept; the rest are ignored.
	MaxPathsPatterns = 256
	// MaxSubstitutions is the number of substitutions kept per `paths` key; the rest are ignored.
	MaxSubstitutions = 16

	// maxReportedProblems is the number of dropped-config problems kept in the returned error;
	// the rest are only counted.
	maxReportedProblems = 5
)

// Problem categories of an AliasError: stable, content-free words safe to log (NFR3005).
const (
	ProblemRead              = "read"
	ProblemMalformed         = "malformed"
	ProblemNotObject         = "not_object"
	ProblemExtendsNotPath    = "extends_not_relative"
	ProblemExtendsOutside    = "extends_outside"
	ProblemExtendsMissing    = "extends_missing"
	ProblemExtendsUnreadable = "extends_unreadable"
	ProblemExtendsTooDeep    = "extends_too_deep"
	ProblemExtendsCycle      = "extends_cycle"
	ProblemTooManyConfigs    = "too_many_configs"
	ProblemPathsCapped       = "paths_capped"
)

// AliasError says why alias resolution is off or partial. Its text and its fields hold problem
// categories and clone-relative config paths only, never text from a tsconfig, so a caller may
// log them (NFR3005).
type AliasError struct {
	// Categories are the distinct Problem* words, in order of first occurrence.
	Categories []string
	// Count is the number of problems, those past the first few included.
	Count int
	// Disabled is true when no alias applies (the root tsconfig is unusable).
	Disabled bool

	msg   string
	cause error
}

func (e *AliasError) Error() string { return e.msg }
func (e *AliasError) Unwrap() error { return e.cause }

// configDirVar is TypeScript 5.5's template for the directory of the root tsconfig.
const configDirVar = "${configDir}"

// Aliases are the `compilerOptions.paths` patterns of the clone's root tsconfig, resolved the way
// TypeScript defines them (D3019). The zero value and nil resolve nothing.
type Aliases struct {
	// base is the clone-relative directory the substitutions are relative to ("." is the root).
	base string
	// outside is true when baseUrl leaves the clone: no substitution can name a clone file.
	outside bool
	// hasBase is true when a baseUrl is declared: a non-relative specifier is also looked up
	// below it, after the paths substitutions (as TypeScript and Node's resolver do).
	hasBase bool
	exact   []aliasPattern
	stars   []aliasPattern
}

type aliasPattern struct {
	key            string
	prefix, suffix string
	subs           []string
}

// LoadAliases reads the root tsconfig.json with its `extends` chain from tree. A missing root
// tsconfig is not an error. A root that is unreadable or malformed disables all aliases (empty
// Aliases and an error). A problem further along the chain (an extends that is a package, leaves
// the clone, is missing, is malformed, is circular or nests deeper than MaxExtendsDepth, or comes
// after MaxConfigReads files) drops only that parent, with its own ancestors: the options of the
// declaring file and of the parents read successfully still apply, and the returned error, to be
// logged by the caller, says what was dropped (D3019, EC3025). `paths` beyond MaxPathsPatterns
// keys or MaxSubstitutions values per key are ignored and reported the same way. The error names
// problem categories and clone-relative config paths only, never text from a tsconfig (NFR3005).
// Relative imports are unaffected either way. When ctx ends first, the error wraps its error.
func LoadAliases(ctx context.Context, tree Tree) (*Aliases, error) {
	l := &configLoader{ctx: ctx, tree: tree, memo: map[string]*loadedConfig{}}
	data, err := l.read(TSConfigName)
	if errors.Is(err, fs.ErrNotExist) {
		return &Aliases{}, nil
	}
	if err != nil {
		if l.ctxErr != nil {
			return &Aliases{}, fmt.Errorf("aliases disabled: %w", l.ctxErr)
		}
		l.report(ProblemRead, "%s: unreadable", TSConfigName)
		return &Aliases{}, l.err("aliases disabled", true, err)
	}
	opts := l.parse(TSConfigName, data, 0)
	if l.ctxErr != nil {
		return &Aliases{}, fmt.Errorf("aliases disabled: %w", l.ctxErr)
	}
	if opts == nil {
		return &Aliases{}, l.err("aliases disabled", true, nil)
	}
	aliases, capped := opts.aliases()
	if capped {
		l.report(ProblemPathsCapped, "paths capped at %d patterns and %d substitutions each, the rest ignored", MaxPathsPatterns, MaxSubstitutions)
	}
	if err := l.err("aliases partly disabled", false, nil); err != nil {
		return aliases, err
	}
	return aliases, nil
}

// tsOptions are the compilerOptions that matter, merged along the extends chain.
type tsOptions struct {
	hasBase  bool
	baseDir  string // clone-relative, valid when hasBase
	baseOut  bool
	paths    *Object
	pathsDir string // directory of the file that declares paths
}

// loadedConfig is the memo entry of one config path: reading and parsing happen once per path.
type loadedConfig struct {
	inProgress bool
	opts       *tsOptions // nil when the file is missing, unreadable or malformed
}

// configLoader follows the extends graph of one clone. It is single use and not concurrent.
type configLoader struct {
	ctx    context.Context
	tree   Tree
	reads  int
	memo   map[string]*loadedConfig
	ctxErr error // set once ctx ended: the load stops

	problems   []string // the first maxReportedProblems
	dropped    int      // problems beyond them
	categories []string
}

// report records one problem. The text must hold no tsconfig content: a category and a
// clone-relative path at most.
func (l *configLoader) report(category, format string, args ...any) {
	if !slices.Contains(l.categories, category) {
		l.categories = append(l.categories, category)
	}
	if len(l.problems) >= maxReportedProblems {
		l.dropped++
		return
	}
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

// err returns the recorded problems as one bounded *AliasError, or nil. prefix leads the text.
func (l *configLoader) err(prefix string, disabled bool, cause error) error {
	if len(l.problems) == 0 {
		return nil
	}
	msg := prefix + ": " + strings.Join(l.problems, "; ")
	if l.dropped > 0 {
		msg += fmt.Sprintf("; and %d more", l.dropped)
	}
	return &AliasError{Categories: l.categories, Count: len(l.problems) + l.dropped, Disabled: disabled, msg: msg, cause: cause}
}

// read reads one config file through the tree, counting it against MaxConfigReads and stopping
// when ctx has ended.
func (l *configLoader) read(rel string) ([]byte, error) {
	if err := l.ctx.Err(); err != nil {
		l.ctxErr = err
		return nil, err
	}
	if l.reads >= MaxConfigReads {
		return nil, errTooManyConfigs
	}
	l.reads++
	return l.tree.ReadFile(rel)
}

var errTooManyConfigs = errors.New("too many tsconfig files")

// parse merges the options of the file rel (content data) with those of its extends chain; depth
// counts the extends hops taken to reach it. The options are nil only when the file itself is
// malformed; a parent that fails is left out and reported.
func (l *configLoader) parse(rel string, data []byte, depth int) *tsOptions {
	entry := &loadedConfig{inProgress: true}
	l.memo[rel] = entry
	defer func() { entry.inProgress = false }()

	doc, err := ParseJSON5(data)
	if err != nil {
		l.report(ProblemMalformed, "%s: malformed (%v)", rel, err)
		return nil
	}
	root, ok := doc.(*Object)
	if !ok {
		l.report(ProblemNotObject, "%s: not a JSON object", rel)
		return nil
	}
	dir := path.Dir(rel)
	merged := &tsOptions{}

	for _, ext := range extendsList(root) {
		if l.ctxErr != nil || l.ctx.Err() != nil {
			l.ctxErr = l.ctx.Err()
			return nil
		}
		if depth+1 > MaxExtendsDepth {
			l.report(ProblemExtendsTooDeep, "%s: extends chain deeper than %d", rel, MaxExtendsDepth)
			break
		}
		if parent := l.extended(rel, dir, ext, depth+1); parent != nil {
			merged.override(parent)
		}
	}

	if co, ok := root.Get("compilerOptions"); ok {
		if opts, ok := co.(*Object); ok {
			if v, ok := opts.Get("baseUrl"); ok {
				if s, ok := v.(string); ok {
					merged.baseDir, merged.baseOut = joinFrom(dir, s)
					merged.hasBase = true
				}
			}
			if v, ok := opts.Get("paths"); ok {
				if po, ok := v.(*Object); ok {
					merged.paths, merged.pathsDir = po, dir
				}
			}
		}
	}
	entry.opts = merged
	return merged
}

// extended loads the tsconfig an extends value of the file declared names: relative values only,
// staying inside the clone, with ".json" appended when the value names no file and has no .json
// extension (as TypeScript does). A file already loaded is reused, never read again. It returns
// nil, after reporting why, when the parent is dropped.
func (l *configLoader) extended(declared, dir, ext string, depth int) *tsOptions {
	if !isRelativeSpec(ext) {
		l.report(ProblemExtendsNotPath, "%s: extends is not a relative path", declared)
		return nil
	}
	p, outside := joinFrom(dir, ext)
	if outside {
		l.report(ProblemExtendsOutside, "%s: extends leaves the clone", declared)
		return nil
	}
	candidates := []string{p}
	if !strings.HasSuffix(p, ".json") {
		candidates = append(candidates, p+".json")
	}
	var readErr error
	for _, c := range candidates {
		if e, seen := l.memo[c]; seen {
			if e.inProgress {
				l.report(ProblemExtendsCycle, "%s: extends cycle", declared)
				return nil
			}
			if e.opts != nil {
				return e.opts
			}
			continue // missing or already reported as malformed
		}
		data, err := l.read(c)
		if err != nil {
			if l.ctxErr != nil {
				return nil
			}
			l.memo[c] = &loadedConfig{}
			readErr = err
			continue
		}
		return l.parse(c, data, depth)
	}
	switch {
	case errors.Is(readErr, errTooManyConfigs):
		l.report(ProblemTooManyConfigs, "%s: more than %d tsconfig files, extends dropped", declared, MaxConfigReads)
	case readErr == nil:
		// every candidate was a file already reported as malformed
	case errors.Is(readErr, fs.ErrNotExist):
		l.report(ProblemExtendsMissing, "%s: extends target missing", declared)
	default:
		l.report(ProblemExtendsUnreadable, "%s: extends target unreadable", declared)
	}
	return nil
}

// override applies a parent's options under the receiver's: the receiver is built from earlier
// parents, so a later parent replaces them.
func (o *tsOptions) override(parent *tsOptions) {
	if parent.hasBase {
		o.hasBase, o.baseDir, o.baseOut = true, parent.baseDir, parent.baseOut
	}
	if parent.paths != nil {
		o.paths, o.pathsDir = parent.paths, parent.pathsDir
	}
}

// extendsList reads `extends` as a string or an array of strings (TypeScript 5.0); other shapes
// are ignored.
func extendsList(root *Object) []string {
	v, ok := root.Get("extends")
	if !ok {
		return nil
	}
	switch e := v.(type) {
	case string:
		return []string{e}
	case []any:
		var out []string
		for _, item := range e {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// joinFrom joins s onto the clone-relative directory dir and cleans the result. outside is true
// when s is absolute or the result leaves the clone. A leading ${configDir} stands for the root
// directory (the root tsconfig lives there).
func joinFrom(dir, s string) (p string, outside bool) {
	if rest, ok := strings.CutPrefix(s, configDirVar); ok {
		dir, s = ".", "."+rest
	}
	if path.IsAbs(s) {
		return "", true
	}
	p = path.Join(dir, s)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", true
	}
	return p, false
}

// aliases builds the pattern tables from the merged options. capped is true when `paths` held more
// than MaxPathsPatterns usable keys or a key more than MaxSubstitutions values: the excess is
// ignored.
func (o *tsOptions) aliases() (a *Aliases, capped bool) {
	a = &Aliases{}
	if o.paths == nil && !o.hasBase {
		return a, false
	}
	if o.hasBase {
		a.base, a.outside, a.hasBase = o.baseDir, o.baseOut, true
	} else {
		a.base = o.pathsDir
	}
	if o.paths == nil {
		return a, false
	}
	kept := 0
	for _, key := range o.paths.Keys {
		raw, _ := o.paths.Get(key)
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		p := aliasPattern{key: key}
		for _, item := range list {
			if s, ok := item.(string); ok {
				if len(p.subs) == MaxSubstitutions {
					capped = true
					break
				}
				p.subs = append(p.subs, s)
			}
		}
		if len(p.subs) == 0 {
			continue
		}
		n := strings.Count(key, "*")
		if n > 1 {
			continue
		}
		if kept == MaxPathsPatterns {
			capped = true
			continue
		}
		kept++
		if n == 0 {
			a.exact = append(a.exact, p)
			continue
		}
		i := strings.IndexByte(key, '*')
		p.prefix, p.suffix = key[:i], key[i+1:]
		a.stars = append(a.stars, p)
	}
	return a, capped
}

// Candidates returns the clone-relative paths (slash-separated, "." is the root) a non-relative specifier maps to, in the order they
// are to be tried: an exact key wins, else the star pattern with the longest prefix (the first
// declared on a tie); its substitutions are tried in order. With a baseUrl the specifier itself,
// joined to the base, comes last (also when no pattern matches). Without a baseUrl, specifiers that
// match no pattern, and a nil receiver, give nothing.
func (a *Aliases) Candidates(spec string) []string {
	if a == nil || a.outside {
		return nil
	}
	var pat *aliasPattern
	star := ""
	for i := range a.exact {
		if a.exact[i].key == spec {
			pat = &a.exact[i]
			break
		}
	}
	if pat == nil {
		best := -1
		for i := range a.stars {
			s := &a.stars[i]
			if len(spec) < len(s.prefix)+len(s.suffix) || !strings.HasPrefix(spec, s.prefix) || !strings.HasSuffix(spec, s.suffix) {
				continue
			}
			if len(s.prefix) > best {
				best, pat = len(s.prefix), s
				star = spec[len(s.prefix) : len(spec)-len(s.suffix)]
			}
		}
	}
	var out []string
	if pat != nil {
		for _, sub := range pat.subs {
			sub = strings.Replace(sub, "*", star, 1)
			if p, outside := joinFrom(a.base, sub); !outside {
				out = append(out, p)
			}
		}
	}
	if a.hasBase {
		if p, outside := joinFrom(a.base, spec); !outside {
			out = append(out, p)
		}
	}
	return out
}

func isRelativeSpec(s string) bool {
	return s == "." || s == ".." || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}
