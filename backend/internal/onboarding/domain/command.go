package domain

// Run steps are model output derived from untrusted repository text, and the UI offers them for
// copy-paste. A step that downloads and executes, escalates privileges or destroys data is
// dropped, never shown. Pure and conservative: a false positive only costs one run step. A port
// of Node's isDangerousCommand (onboarding/domain/command-safety.ts, spec 7.10.5).
//
// The ten patterns are tested for existence only, so the JavaScript look-aheads (NAME_END,
// STDIN_TAIL) become the character they look at, consumed at the end of the pattern; NAME_END in
// front of STDIN_TAIL is implied by it (the tail starts with whitespace, a separator or the end)
// and is left out. Case-insensitive literals are spelled out (see ci) so U+017F and U+212A never
// match "s" and "k".

import (
	"regexp"
	"strings"
)

var dangerous = buildDangerous()

func alt(lits ...string) string {
	parts := make([]string, len(lits))
	for i, l := range lits {
		parts[i] = ci(l)
	}
	return "(?:" + strings.Join(parts, "|") + ")"
}

func buildDangerous() []*regexp.Regexp {
	const sp, nsp = jsSpaceRE, jsNotSpaceRE
	shells := alt("sh", "bash", "zsh", "dash", "ash", "ksh", "fish", "csh", "tcsh", "iex", "invoke-expression", "powershell", "pwsh", "cmd")
	interpreters := `(?:` + ci("python") + `[\d.]*|` + ci("nodejs") + `|` + ci("node") + `|` + ci("perl") + `|` + ci("ruby") + `)`
	downloader := alt("curl", "wget", "iwr", "irm", "invoke-webrequest", "invoke-restmethod")
	pathPrefix := `(?:[\w.~$-]*/)*`
	wrappers := `(?:` + pathPrefix + alt("sudo", "doas", "env", "xargs", "nohup", "exec", "command", "time", "nice", "stdbuf", "timeout") +
		`\b(?:` + sp + `+(?:-` + nsp + `+|\w+=` + nsp + `*))*` + sp + `+)*`
	nameEnd := `(?:$|[^\w-])`
	stdinTail := `(?:` + sp + `+-[uUbBsSeEiIoOqQ]+)*(?:` + sp + `*(?:$|[;&|)<>\n])|` + sp + `+-(?:$|` + sp + `))`
	pipeTo := `\|` + sp + `*` + wrappers + pathPrefix
	decode := `(?:-[dD]|--` + ci("decode") + `)`

	pats := []string{
		// downloaded content piped into a shell (optionally behind sudo / env / xargs, by path)...
		`\b` + downloader + `\b[^\n]*` + pipeTo + shells + nameEnd,
		// ...or into an interpreter that reads its program from stdin (`| python`, `| node -`; not `| python -m json.tool`)
		`\b` + downloader + `\b[^\n]*` + pipeTo + interpreters + stdinTail,
		// sh -c "$(curl ...)" · bash <(curl ...) · iex (iwr ...)
		`\b(?:` + shells + `|` + interpreters + `)\b[^\n]*(?:\$\(|<\(|\(|\x60)` + sp + `*` + downloader + `\b`,
		// privilege escalation
		`(?:^|[` + jsSpaceChars + `;&|(])` + ci("sudo") + `\b`,
		// rm of the filesystem root or the home directory
		`\b` + ci("rm") + sp + `+(?:-{1,2}[\w-]+` + sp + `+)+(?:--` + sp + `+)?(?:/|~/?|\$` + ci("HOME") + `/?)\*?(?:$|[` + jsSpaceChars + `;&|])`,
		// filesystem creation / raw disk reads and writes
		`\b` + ci("mkfs") + `(?:\.\w+)?\b`,
		`\b` + ci("dd") + sp + `+(?:` + nsp + `+` + sp + `+)*` + ci("if="),
		// fork bomb
		`:` + sp + `*\(` + sp + `*\)` + sp + `*\{`,
		// base64 decoded straight into a shell / eval
		`\b` + ci("base64") + `\b[^\n|]*` + decode + `\b[^\n]*` + pipeTo + shells + nameEnd,
		`\b(?:` + ci("eval") + `|` + shells + `|` + interpreters + `)\b[^\n]*\b` + ci("base64") + `\b[^\n]*` + decode + `\b`,
	}
	res := make([]*regexp.Regexp, len(pats))
	for i, p := range pats {
		res[i] = regexp.MustCompile(p)
	}
	return res
}

// --- download to a file, run that file later -------------------------------------------

var (
	wrapperWords = setOf("sudo", "doas", "env", "nohup", "exec", "command", "time", "nice", "stdbuf", "timeout", "xargs")
	downloaders  = setOf("curl", "wget", "iwr", "irm", "invoke-webrequest", "invoke-restmethod")
	runnerRE     = regexp.MustCompile(`^(?:` + alt("sh", "bash", "zsh", "dash", "ash", "ksh", "fish", "csh", "tcsh", "iex", "invoke-expression", "powershell", "pwsh", "cmd") +
		`|` + ci("python") + `[\d.]*|` + ci("nodejs") + `|` + ci("node") + `|` + ci("perl") + `|` + ci("ruby") + `|` + ci("source") + `|\.)$`)
	curlOutputRE     = regexp.MustCompile(`^-[A-Za-z]*o$`)
	curlRemoteNameRE = regexp.MustCompile(`^-[A-Za-z]*O[A-Za-z]*$`)
	wgetOutputRE     = regexp.MustCompile(`^-[A-Za-z]*O$`)
	urlSchemeRE      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
	assignmentRE     = regexp.MustCompile(`^\w+=`)
)

// tokenize splits a command line into simple commands, each a list of unquoted words.
func tokenize(command string) [][]string {
	var commands [][]string
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	endWord := func() {
		if inWord {
			words = append(words, word.String())
		}
		word.Reset()
		inWord = false
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			commands = append(commands, words)
		}
		words = nil
	}
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
		case c == '"' || c == '\'':
			quote = c
			inWord = true
		case c == '\\' && i+1 < len(rs):
			i++
			word.WriteRune(rs[i])
			inWord = true
		case c == ';' || c == '&' || c == '|' || c == '\n' || c == '(' || c == ')':
			endCommand()
		case isJSSpace(c):
			endWord()
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	endCommand()
	return commands
}

// baseName is the last path segment of w without query or fragment and trailing slashes.
func baseName(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimRight(p, "/")
	return p[strings.LastIndexByte(p, '/')+1:]
}

// downloadedFiles lists the files a downloader command writes: -o f (curl), -O f (wget),
// -OutFile f, or the URL's name for curl -O and a bare wget.
func downloadedFiles(words []string) []string {
	at := -1
	for i, w := range words {
		if downloaders[asciiLower(baseName(w))] {
			at = i
			break
		}
	}
	if at < 0 {
		return nil
	}
	tool := asciiLower(baseName(words[at]))
	args := words[at+1:]
	var files []string
	remoteName := tool == "wget"
	for i, a := range args {
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		switch {
		case tool == "curl":
			switch {
			case a == "--output" || curlOutputRE.MatchString(a):
				if next != "" {
					files = append(files, next)
				}
			case strings.HasPrefix(a, "--output="):
				files = append(files, a[len("--output="):])
			case a == "--remote-name" || curlRemoteNameRE.MatchString(a):
				remoteName = true
			}
		case tool == "wget":
			switch {
			case a == "--output-document" || a == "-O" || wgetOutputRE.MatchString(a):
				if next != "" {
					files = append(files, next)
				}
				remoteName = false
			case strings.HasPrefix(a, "--output-document="):
				files = append(files, a[len("--output-document="):])
				remoteName = false
			}
		case asciiLower(a) == "-outfile" && next != "":
			files = append(files, next)
		}
	}
	if remoteName {
		for _, a := range args {
			if urlSchemeRE.MatchString(a) {
				n := baseName(a)
				if n == "" {
					n = "index.html"
				}
				files = append(files, n)
			}
		}
	}
	out := files[:0:0]
	for _, f := range files {
		if b := baseName(f); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// runsOneOf reports whether this simple command executes one of files, directly or through a
// shell/interpreter.
func runsOneOf(words []string, files map[string]bool) bool {
	i := 0
	for i < len(words) {
		w := words[i]
		if wrapperWords[asciiLower(baseName(w))] || strings.HasPrefix(w, "-") || assignmentRE.MatchString(w) {
			i++
		} else {
			break
		}
	}
	if i >= len(words) {
		return false
	}
	head := words[i]
	if files[baseName(head)] {
		return true
	}
	if !runnerRE.MatchString(baseName(head)) {
		return false
	}
	for _, a := range words[i+1:] {
		if !strings.HasPrefix(a, "-") {
			return files[baseName(a)]
		}
	}
	return false
}

func downloadsThenRuns(command string) bool {
	commands := tokenize(command)
	for i, c := range commands {
		list := downloadedFiles(c)
		if len(list) == 0 {
			continue
		}
		files := make(map[string]bool, len(list))
		for _, f := range list {
			files[f] = true
		}
		for _, later := range commands[i+1:] {
			if runsOneOf(later, files) {
				return true
			}
		}
	}
	return false
}

// IsDangerousCommand reports whether a run step must be dropped: it pipes a download into a
// shell or an interpreter that reads stdin, escalates privileges, removes the root or home
// directory, writes a file system or raw disk, forks without bound, decodes base64 into a shell,
// or downloads a file and runs it later.
func IsDangerousCommand(command string) bool {
	for _, re := range dangerous {
		if re.MatchString(command) {
			return true
		}
	}
	return downloadsThenRuns(command)
}
