package domain

// Ticket and doc link extraction for a GitLab repository (spec GitLab B FR26, FR28). The GitHub
// rules in links.go stay as they are (Node parity); these are their GitLab counterparts and share
// the helpers there (closing keyword window, skipped set, Jira filter, doc rules).
//
// "The same project" is read from the repository's stored web address (Repo.WebURL, the
// connection's base address + the project path) and its host key: a URL belongs to the project
// when its host key equals Repo.Host and its path equals the path of Repo.WebURL (case-insensitive).
// A repository without a usable web address matches no URL (fail closed).
//
// SCR3: the references of one text are handled in the order of their position in it, whatever form
// they have, so the "first seen" order and the cap of TicketMax follow the text, not the forms.

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/platform/codehost"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// maxRefSegments is the most "/"-separated segments of the "<p>#<n>" form.
const maxRefSegments = 21

var (
	// https://<host>[:<port>]/<path>/-/issues/<n>, …/-/work_items/<n> or the legacy …/<path>/issues/<n>.
	glIssueURL = regexp.MustCompile(`https://([^/\s?#]+)/([^\s?#]+?)(?:/-/(?:issues|work_items)|/issues)/(\d+)\b`)
	// https://<host>[:<port>]/<path>/-/blob/<ref>/<file> and …/-/tree/<ref>/<file>; the ref is one
	// segment, the file stops at white space, ")", "?" and "#".
	glBlobURL = regexp.MustCompile(`https://([^/\s?#]+)/([^\s?#]+?)/-/(?:blob|tree)/[^/\s?#]+/([^\s)?#]+)`)
	// The digits of a "#<n>" reference; the part before the "#" is scanned by hand (no lookbehind).
	glHashRef = regexp.MustCompile(`#(\d+)\b`)
)

// glProject is the identity of the repository's project: its host key and the lower-cased path
// of its web address without slashes at either end ("" = unknown, which matches nothing).
type glProject struct {
	host, path string
}

func projectOf(repo Repo) glProject {
	p := glProject{host: strings.ToLower(repo.Host)}
	u, err := url.Parse(repo.WebURL)
	if err != nil || u.Host == "" {
		return p
	}
	key := codehost.HostKey(bracketed(u.Hostname()), u.Port())
	if p.host == "" {
		p.host = key
	}
	if key == p.host {
		p.path = strings.ToLower(strings.Trim(u.Path, "/"))
	}
	return p
}

// bracketed writes an IPv6 literal with its brackets, as codehost.HostKey expects.
func bracketed(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// hostKeyOf is the host key of a "host[:port]" as written in a URL; "" when it does not parse.
func hostKeyOf(hostPort string) string {
	u, err := url.Parse("https://" + hostPort)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return codehost.HostKey(bracketed(u.Hostname()), u.Port())
}

// verdict classifies a URL against the project: sameProject, otherProject (same host, another
// path) or otherHost.
type verdict int

const (
	otherHost verdict = iota
	otherProject
	sameProject
)

func (p glProject) judge(hostPort, path string) (verdict, string) {
	key := hostKeyOf(hostPort)
	if key == "" || p.host == "" || key != p.host {
		if key == "" {
			key = hostPort
		}
		return otherHost, key
	}
	if p.path != "" && strings.EqualFold(strings.Trim(path, "/"), p.path) {
		return sameProject, ""
	}
	return otherProject, path
}

// isRefByte reports whether b can be part of the "<p>" of a "<p>#<n>" reference ("/" included).
func isRefByte(b byte) bool {
	return b == '/' || b == '_' || b == '.' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// refBefore finds the "<p>" of a "<p>#<n>" reference whose "#" is at hash: 1–21 "/"-separated,
// non-empty segments of [A-Za-z0-9_.-], not preceded by a word character, "/" or ".". It takes the
// leftmost start that fits, as a regex with a lookbehind would. floor is the end of the previous
// reference. ok is false when there is none.
//
// It reads only text[floor:hash], one pass back from hash to find the leftmost start the "//" and
// segment-count rules allow, one pass forward from there, so the total over a text is linear.
func refBefore(text string, hash, floor int) (start int, ok bool) {
	i := hash
	for i > floor && isRefByte(text[i-1]) {
		i--
	}
	if i == hash || text[hash-1] == '/' { // no "<p>", or an empty last segment
		return 0, false
	}
	// A start s must leave no "//" and at most maxRefSegments-1 slashes in text[s:hash]: the
	// nearest violation walking back from hash sets the leftmost allowed start.
	lo, slashes := i, 0
	for k := hash - 1; k >= i; k-- {
		if text[k] != '/' {
			continue
		}
		if text[k+1] == '/' {
			lo = k + 1
			break
		}
		if slashes++; slashes == maxRefSegments {
			lo = k + 1
			break
		}
	}
	for s := lo; s < hash; s++ {
		if text[s] == '/' || s > 0 && isWordOrSlash(text[s-1]) || s > 0 && text[s-1] == '.' {
			continue
		}
		return s, true
	}
	return 0, false
}

// glEvent is one reference found in a text, applied in the order of start.
type glEvent struct {
	start int
	apply func()
}

// ExtractGitLabTickets is ExtractTickets for a GitLab repository (FR26). Same-project issue URLs
// (`/-/issues/<n>`, `/-/work_items/<n>`, legacy `/issues/<n>`), `<p>#<n>` references and bare
// `#<n>` become numbers (closing keywords first, then position in the text, at most TicketMax);
// other projects, other hosts, GitHub issue URLs and Jira-style keys become skipped entries.
func ExtractGitLabTickets(texts []string, repo Repo) TicketLinks {
	type entry struct {
		number  float64
		closing bool
	}
	var order []entry
	index := map[float64]int{}
	skipped := newSkippedSet()
	project := projectOf(repo)
	fullName := repo.Name
	if repo.Owner != "" {
		fullName = repo.Owner + "/" + repo.Name
	}

	for _, text := range texts {
		if text == "" {
			continue
		}
		note := func(digits string, at int) {
			n := jsNumberOfDigits(digits)
			closing := precededByClosingKeyword(text, at)
			if i, ok := index[n]; ok {
				order[i].closing = order[i].closing || closing
				return
			}
			index[n] = len(order)
			order = append(order, entry{n, closing})
		}

		var events []glEvent
		var spans [][2]int // text taken by a URL: nothing inside it is a reference of its own
		add := func(start int, f func()) { events = append(events, glEvent{start, f}) }

		// GitHub issue URLs keep today's skip reason; masked so no other form matches inside them.
		for _, m := range issueURL.FindAllStringSubmatchIndex(text, -1) {
			ref, owner, name := text[m[0]:m[1]], text[m[2]:m[3]], text[m[4]:m[5]]
			spans = append(spans, [2]int{m[0], m[1]})
			add(m[0], func() {
				skipped.add(ref, "issue reference from a different repository ("+owner+"/"+name+")")
			})
		}
		scan := maskMatches(text, issueURL) // ASCII-only matches: byte offsets are unchanged

		for _, m := range glIssueURL.FindAllStringSubmatchIndex(scan, -1) {
			ref, hostPort, path, digits := text[m[0]:m[1]], text[m[2]:m[3]], text[m[4]:m[5]], text[m[6]:m[7]]
			start := m[0]
			spans = append(spans, [2]int{m[0], m[1]})
			add(start, func() {
				switch v, what := project.judge(hostPort, path); v {
				case sameProject:
					note(digits, start)
				case otherProject:
					skipped.add(ref, "issue reference from a different project ("+what+")")
				default:
					skipped.add(ref, "issue reference from another host ("+what+")")
				}
			})
		}

		// The spans never overlap, so sorted by start the one that can hold a position is found by
		// bisection (a linear scan per reference made a text of many URLs and references quadratic).
		sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
		inURL := func(at int) bool {
			k := sort.Search(len(spans), func(i int) bool { return spans[i][0] > at })
			return k > 0 && at < spans[k-1][1]
		}

		floor := 0
		for _, m := range glHashRef.FindAllStringSubmatchIndex(scan, -1) {
			hash, end, digits := m[0], m[1], scan[m[2]:m[3]]
			prev := floor
			floor = end
			if inURL(hash) {
				continue
			}
			if s, ok := refBefore(scan, hash, prev); ok {
				p, ref := scan[s:hash], scan[s:end]
				add(s, func() {
					full := p
					if !strings.Contains(p, "/") && repo.Owner != "" {
						full = repo.Owner + "/" + p
					}
					if strings.EqualFold(full, fullName) {
						note(digits, s)
					} else {
						skipped.add(ref, "issue reference from a different project ("+p+")")
					}
				})
				continue
			}
			if hash > 0 && isWordOrSlash(scan[hash-1]) { // the (?<![\w/]) lookbehind
				continue
			}
			add(hash, func() { note(digits, hash) })
		}

		for _, m := range jiraKey.FindAllStringSubmatchIndex(scan, -1) {
			key := scan[m[2]:m[3]]
			if inURL(m[0]) || nonTicketKeyPrefixes[key[:strings.IndexByte(key, '-')]] {
				continue
			}
			add(m[0], func() { skipped.add(key, jiraReason) })
		}

		sort.SliceStable(events, func(i, j int) bool { return events[i].start < events[j].start })
		for _, e := range events {
			e.apply()
		}
	}

	// Closing first, then position order (a stable partition).
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

// ExtractGitLabDocs is ExtractDocs for a GitLab repository (FR28): markdown links, `/-/blob/<ref>/<file>`
// and `/-/tree/<ref>/<file>` URLs of the same project, and bare paths in prose. Another project is
// skipped "doc link points outside this project (<path>)"; any other http(s) URL (another host
// included) "not a blob or tree URL of this GitLab project (no arbitrary URL fetch)". Path safety,
// extensions, ordering, the cap of DocMax and the URIError on a malformed escape are those of
// ExtractDocs.
func ExtractGitLabDocs(texts []string, repo Repo) (DocLinks, error) {
	var candidates []string
	seen := map[string]bool{}
	skipped := newSkippedSet()
	project := projectOf(repo)

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
	const notBlob = "not a blob or tree URL of this GitLab project (no arbitrary URL fetch)"
	// blob handles one blob or tree match (hostPort, path, file) of the raw URL.
	blob := func(raw, hostPort, path, file string) error {
		switch v, what := project.judge(hostPort, path); v {
		case sameProject:
			decoded, err := reviewer.DecodeURIComponent(file)
			if err != nil {
				return err
			}
			consider(decoded)
		case otherProject:
			skipped.add(raw, "doc link points outside this project ("+what+")")
		default:
			skipped.add(raw, notBlob)
		}
		return nil
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
			b := glBlobURL.FindStringSubmatchIndex(target)
			if b == nil || b[0] != 0 {
				skipped.add(target, notBlob)
				continue
			}
			if err := blob(target, target[b[2]:b[3]], target[b[4]:b[5]], target[b[6]:b[7]]); err != nil {
				return DocLinks{}, err
			}
		}
		// URLs in prose (markdown links are handled above): a blob URL counts only at the start of
		// the URL, so one inside another URL's query is not read as this project's.
		unlinked := maskMatches(text, mdLink)
		for _, u := range anyURL.FindAllString(unlinked, -1) {
			if b := glBlobURL.FindStringSubmatchIndex(u); b != nil && b[0] == 0 {
				if err := blob(u[:b[1]], u[b[2]:b[3]], u[b[4]:b[5]], u[b[6]:b[7]]); err != nil {
					return DocLinks{}, err
				}
			}
		}
		// Bare paths only in prose: markdown links and URLs are masked first.
		prose := maskMatches(unlinked, anyURL)
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
