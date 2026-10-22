package importgraph

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Tsconfig limits.
const (
	// TSConfigName is the tsconfig file read at the clone root.
	TSConfigName = "tsconfig.json"
	// MaxExtendsDepth is the longest `extends` chain followed from the root tsconfig.
	MaxExtendsDepth = 10
)

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
// the clone, is missing, is malformed or nests deeper than MaxExtendsDepth) drops only that
// parent, with its own ancestors: the options of the declaring file and of the parents read
// successfully still apply, and the returned error, to be logged by the caller, says what was
// dropped (D3019, EC3025). Relative imports are unaffected either way.
func LoadAliases(tree Tree) (*Aliases, error) {
	data, err := tree.ReadFile(TSConfigName)
	if errors.Is(err, fs.ErrNotExist) {
		return &Aliases{}, nil
	}
	if err != nil {
		return &Aliases{}, fmt.Errorf("aliases disabled: read %s: %w", TSConfigName, err)
	}
	opts, err := parseTSConfig(tree, TSConfigName, data, 0)
	if opts == nil {
		return &Aliases{}, fmt.Errorf("aliases disabled: %w", err)
	}
	if err != nil {
		err = fmt.Errorf("aliases partly disabled: %w", err)
	}
	return opts.aliases(), err
}

// tsOptions are the compilerOptions that matter, merged along the extends chain.
type tsOptions struct {
	hasBase  bool
	baseDir  string // clone-relative, valid when hasBase
	baseOut  bool
	paths    *Object
	pathsDir string // directory of the file that declares paths
}

// parseTSConfig merges the options of the file rel (content data) with those of its extends
// chain; depth counts the extends hops taken to reach it. The options are nil only when the file
// itself is malformed; a parent that fails is left out and its error is returned along with the
// options that could be read.
func parseTSConfig(tree Tree, rel string, data []byte, depth int) (*tsOptions, error) {
	doc, err := ParseJSON5(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	root, ok := doc.(*Object)
	if !ok {
		return nil, fmt.Errorf("%s: not a JSON object", rel)
	}
	dir := path.Dir(rel)
	merged := &tsOptions{}
	var dropped []error

	for _, ext := range extendsList(root) {
		if depth+1 > MaxExtendsDepth {
			dropped = append(dropped, fmt.Errorf("%s: extends chain deeper than %d", rel, MaxExtendsDepth))
			break
		}
		parentRel, parentData, err := readExtended(tree, dir, ext)
		if err != nil {
			dropped = append(dropped, fmt.Errorf("%s: %w", rel, err))
			continue
		}
		parent, err := parseTSConfig(tree, parentRel, parentData, depth+1)
		if err != nil {
			dropped = append(dropped, err)
		}
		if parent != nil {
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
	return merged, errors.Join(dropped...)
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

// readExtended reads the tsconfig an extends value declared in a file of directory dir names:
// relative values only, staying inside the clone, with ".json" appended when the value names no
// file and has no .json extension (as TypeScript does). It returns the clone-relative path read.
func readExtended(tree Tree, dir, ext string) (rel string, data []byte, err error) {
	if !isRelativeSpec(ext) {
		return "", nil, fmt.Errorf("extends %q is not a path inside the clone", ext)
	}
	p, outside := joinFrom(dir, ext)
	if outside {
		return "", nil, fmt.Errorf("extends %q leaves the clone", ext)
	}
	data, err = tree.ReadFile(p)
	if err != nil && !strings.HasSuffix(p, ".json") {
		p += ".json"
		data, err = tree.ReadFile(p)
	}
	if err != nil {
		return "", nil, fmt.Errorf("extends %q: %w", ext, err)
	}
	return p, data, nil
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

func (o *tsOptions) aliases() *Aliases {
	a := &Aliases{}
	if o.paths == nil && !o.hasBase {
		return a
	}
	if o.hasBase {
		a.base, a.outside, a.hasBase = o.baseDir, o.baseOut, true
	} else {
		a.base = o.pathsDir
	}
	if o.paths == nil {
		return a
	}
	for _, key := range o.paths.Keys {
		raw, _ := o.paths.Get(key)
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		p := aliasPattern{key: key}
		for _, item := range list {
			if s, ok := item.(string); ok {
				p.subs = append(p.subs, s)
			}
		}
		if len(p.subs) == 0 {
			continue
		}
		switch strings.Count(key, "*") {
		case 0:
			a.exact = append(a.exact, p)
		case 1:
			i := strings.IndexByte(key, '*')
			p.prefix, p.suffix = key[:i], key[i+1:]
			a.stars = append(a.stars, p)
		}
	}
	return a
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
