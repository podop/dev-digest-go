package domain

// Ticket and doc link extraction (spec 7.4.2; Node intent/domain/links.ts). The Node patterns are
// JavaScript regexes; Go's RE2 differs in three ways that matter here and are handled by hand:
// \s is ASCII only (jsSpace spells out JavaScript's), (?i) folds U+017F and U+212A into "s" and
// "k" (the case-insensitive parts are written as explicit classes), and there is no lookbehind
// (the bare "#N" rule checks the byte before the match). Offsets that Node counts in UTF-16 units
// (the 24 units before a match, the same-length masking) are counted in units too.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// jsSpace is JavaScript's \s as the inside of a character class.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

const (
	// closingWindowUnits is how far before a reference a closing keyword counts.
	closingWindowUnits = 24
	// jiraReason is the skip reason of a Jira or Linear style key.
	jiraReason = "Jira/Linear-style keys are not supported (server/specs/05-intent-layer.md)"
)

// nonTicketKeyPrefixes are the upper-case prefixes of Jira-shaped matches that are standard or
// protocol names (UTF-8, SHA-256), not tickets.
var nonTicketKeyPrefixes = map[string]bool{
	"UTF": true, "SHA": true, "ISO": true, "RFC": true, "CVE": true, "HTTP": true, "HTTPS": true, "TLS": true,
	"SSL": true, "AES": true, "RSA": true, "GPT": true, "ES": true, "ECMA": true, "IPV": true, "X": true,
}

// docExtensions are the extensions a doc may be read from.
var docExtensions = []string{".md", ".mdx", ".markdown", ".txt"}

var (
	// \b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b with /i, the letters spelled out.
	closingKeyword = regexp.MustCompile(`\b(?:[cC][lL][oO][sS][eE][sSdD]?|[fF][iI][xX](?:[eE][sSdD])?|[rR][eE][sS][oO][lL][vV][eE][sSdD]?)\b`)

	issueURL  = regexp.MustCompile(`https?://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/issues/(\d+)\b`)
	crossRepo = regexp.MustCompile(`\b([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#(\d+)\b`)
	bareIssue = regexp.MustCompile(`#(\d+)\b`)
	jiraKey   = regexp.MustCompile(`\b([A-Z][A-Z0-9]{1,9}-\d{1,6})\b`)

	mdLink      = regexp.MustCompile(`\[[^\]]*\]\(([^)` + jsSpace + `]+)\)`)
	blobURL     = regexp.MustCompile(`https?://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/(?:blob|tree)/[^/]+/([^` + jsSpace + `)]+)`)
	anyURL      = regexp.MustCompile(`https?://[^` + jsSpace + `]+`)
	httpPrefix  = regexp.MustCompile(`^[hH][tT][tT][pP][sS]?://`)
	bareDocPath = regexp.MustCompile(`\b((?:[\w-]+/)*[\w.-]+\.(?:[mM][dD]|[mM][dD][xX]|[mM][aA][rR][kK][dD][oO][wW][nN]|[tT][xX][tT]))\b`)
)

// Ref is the string form of the ticket number (JavaScript String(number)): the ref of its source
// and of its fetch.
func (t TicketRef) Ref() string { return reviewer.JSNumber(t.Number) }

// skippedSet collects skipped references, keeping the first of every (ref, reason) pair. The
// dedup key is the Node string `${ref}::${reason}`, ambiguity included.
type skippedSet struct {
	list []Skipped
	seen map[string]bool
}

func newSkippedSet() *skippedSet { return &skippedSet{list: []Skipped{}, seen: map[string]bool{}} }

func (s *skippedSet) add(ref, reason string) {
	key := ref + "::" + reason
	if s.seen[key] {
		return
	}
	s.seen[key] = true
	s.list = append(s.list, Skipped{Ref: ref, Reason: reason})
}

// sameRepo compares owner and name with JavaScript's toLowerCase on both sides.
func sameRepo(owner, name string, repo Repo) bool {
	return strings.ToLower(owner) == strings.ToLower(repo.Owner) && strings.ToLower(name) == strings.ToLower(repo.Name)
}

// precededByClosingKeyword reports whether a closing keyword appears in the 24 UTF-16 units that
// precede byte offset idx of text.
func precededByClosingKeyword(text string, idx int) bool {
	return closingKeyword.MatchString(lastUnits(text[:idx], closingWindowUnits))
}

// lastUnits is the last n UTF-16 units of s. A cut inside a surrogate pair drops the pair (the
// dangling half is no word character, like the boundary it leaves).
func lastUnits(s string, n int) string {
	u, i := 0, len(s)
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if u+w > n {
			break
		}
		u += w
		i -= size
	}
	return s[i:]
}

// ExtractTickets finds the ticket references of texts (title, body, then each commit message, in
// that order; empty texts are skipped). Same-repo issue URLs, "owner/repo#N" and bare "#N" become
// numbers (closing keywords first, then first-seen order, at most TicketMax); other repositories
// and Jira-style keys become skipped entries; numbers over the limit are skipped as "#<n>".
func ExtractTickets(texts []string, repo Repo) TicketLinks {
	type entry struct {
		number  float64
		closing bool
	}
	var order []entry
	index := map[float64]int{}
	skipped := newSkippedSet()

	note := func(digits, text string, at int) {
		n := jsNumberOfDigits(digits)
		closing := precededByClosingKeyword(text, at)
		if i, ok := index[n]; ok {
			order[i].closing = order[i].closing || closing
			return
		}
		index[n] = len(order)
		order = append(order, entry{n, closing})
	}

	for _, text := range texts {
		if text == "" {
			continue
		}
		for _, m := range issueURL.FindAllStringSubmatchIndex(text, -1) {
			owner, name := text[m[2]:m[3]], text[m[4]:m[5]]
			if sameRepo(owner, name, repo) {
				note(text[m[6]:m[7]], text, m[0])
			} else {
				skipped.add(text[m[0]:m[1]], "issue reference from a different repository ("+owner+"/"+name+")")
			}
		}
		for _, m := range crossRepo.FindAllStringSubmatchIndex(text, -1) {
			owner, name := text[m[2]:m[3]], text[m[4]:m[5]]
			if sameRepo(owner, name, repo) {
				note(text[m[6]:m[7]], text, m[0])
			} else {
				skipped.add(text[m[0]:m[1]], "issue reference from a different repository ("+owner+"/"+name+")")
			}
		}
		for _, m := range bareIssue.FindAllStringSubmatchIndex(text, -1) {
			if m[0] > 0 && isWordOrSlash(text[m[0]-1]) { // the (?<![\w/]) lookbehind
				continue
			}
			note(text[m[2]:m[3]], text, m[0])
		}
		for _, m := range jiraKey.FindAllStringSubmatchIndex(text, -1) {
			key := text[m[2]:m[3]]
			if nonTicketKeyPrefixes[key[:strings.IndexByte(key, '-')]] {
				continue
			}
			skipped.add(key, jiraReason)
		}
	}

	// Closing first, then first-seen (a stable partition, as the stable sort by the flag gives).
	ordered := make([]entry, 0, len(order))
	for _, e := range order {
		if e.closing {
			ordered = append(ordered, e)
		}
	}
	for _, e := range order {
		if !e.closing {
			ordered = append(ordered, e)
		}
	}
	links := TicketLinks{Refs: []TicketRef{}}
	for i, e := range ordered {
		if i < TicketMax {
			links.Refs = append(links.Refs, TicketRef{Number: e.number, Closing: e.closing})
			continue
		}
		skipped.add("#"+reviewer.JSNumber(e.number), "ticket limit reached (max "+strconv.Itoa(TicketMax)+")")
	}
	links.Skipped = skipped.list
	return links
}

func isWordOrSlash(b byte) bool {
	return b == '/' || b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// jsNumberOfDigits is JavaScript's Number("123"): the correctly rounded float64, Infinity when
// the digits are out of range.
func jsNumberOfDigits(digits string) float64 {
	f, _ := strconv.ParseFloat(digits, 64) // an out-of-range value is ±Inf with ErrRange
	return f
}

// docPriority orders candidate doc paths before the cap.
func docPriority(path string) int {
	switch {
	case strings.HasPrefix(path, "docs/plans/"):
		return 0
	case strings.Contains(path, "/specs/") || strings.HasPrefix(path, "specs/"):
		return 1
	case strings.HasPrefix(path, "docs/"):
		return 2
	}
	return 3
}

// HasDocExtension reports whether path ends (case-insensitively) with .md, .mdx, .markdown or .txt.
func HasDocExtension(path string) bool {
	lower := strings.ToLower(path)
	for _, ext := range docExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// maskMatches replaces every match of re by as many spaces as it has UTF-16 units, so offsets and
// word boundaries around it are kept and its content is gone.
func maskMatches(text string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(text, func(m string) string { return strings.Repeat(" ", reviewer.Len16(m)) })
}

// ExtractDocs finds the doc references of texts (title, body): markdown links, GitHub blob and
// tree URLs of the same repository, and bare paths in prose, ordered by priority and English
// collation, at most DocMax; further candidates and unsafe or foreign links are skipped entries.
// A malformed percent escape in a link is an error (JavaScript's URIError: reviewer.ErrURIMalformed),
// which makes the derivation unavailable.
func ExtractDocs(texts []string, repo Repo) (DocLinks, error) {
	var candidates []string
	seen := map[string]bool{}
	skipped := newSkippedSet()

	consider := func(raw string) {
		path := strings.TrimPrefix(raw, "./")
		path = strings.TrimRight(path, ")]},.")
		if !HasDocExtension(path) {
			return
		}
		if strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.Contains(path, `\`) || strings.Contains(path, "\x00") {
			skipped.add(path, "unsafe path")
			return
		}
		if !seen[path] {
			seen[path] = true
			candidates = append(candidates, path)
		}
	}
	outside := func(owner, name string) string {
		return "doc link points outside this repo (" + owner + "/" + name + ")"
	}

	for _, text := range texts {
		if text == "" {
			continue
		}
		for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if !httpPrefix.MatchString(target) {
				consider(target)
				continue
			}
			b := blobURL.FindStringSubmatch(target)
			if b == nil {
				skipped.add(target, "non-GitHub or non-blob/tree URL (no arbitrary URL fetch)")
				continue
			}
			if !sameRepo(b[1], b[2], repo) {
				skipped.add(target, outside(b[1], b[2]))
				continue
			}
			decoded, err := reviewer.DecodeURIComponent(b[3])
			if err != nil {
				return DocLinks{}, err
			}
			consider(decoded)
		}
		for _, m := range blobURL.FindAllStringSubmatch(text, -1) {
			if !sameRepo(m[1], m[2], repo) {
				skipped.add(m[0], outside(m[1], m[2]))
				continue
			}
			decoded, err := reviewer.DecodeURIComponent(m[3])
			if err != nil {
				return DocLinks{}, err
			}
			consider(decoded)
		}
		// Bare paths only in prose: markdown links and URLs are masked first.
		prose := maskMatches(maskMatches(text, mdLink), anyURL)
		for _, m := range bareDocPath.FindAllStringSubmatch(prose, -1) {
			consider(m[1])
		}
	}

	prio := make(map[string]int, len(candidates))
	for _, c := range candidates {
		prio[c] = docPriority(c)
	}
	english := reviewer.EnglishComparer()
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if prio[a] != prio[b] {
			return prio[a] < prio[b]
		}
		return english(a, b) < 0
	})
	links := DocLinks{Refs: []DocRef{}}
	for i, path := range candidates {
		if i < DocMax {
			links.Refs = append(links.Refs, DocRef{Path: path})
			continue
		}
		skipped.add(path, "doc limit reached (max "+strconv.Itoa(DocMax)+")")
	}
	links.Skipped = skipped.list
	return links, nil
}
