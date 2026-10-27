package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/skills/domain"
)

// urlFetcher downloads an import document from a URL that passed domain.ParseImportURL.
type urlFetcher interface {
	Fetch(ctx context.Context, u *url.URL) (domain.FetchedDocument, error)
}

// catalog is the community catalog that ships with the binary.
type catalog interface {
	List() []domain.CatalogEntry
	Get(id string) (domain.CatalogEntry, bool)
}

// GitLabFileConn is a GitLab connection that matched an import URL's host key: what the importer
// needs to read the URL against it and to name it in messages. It carries no token.
type GitLabFileConn struct {
	ID   string
	Name string
	// BaseURL is the connection's instance URL (https://host[:port][/prefix]).
	BaseURL string
	// Host is the connection's host key.
	Host string
}

// gitLabFiles reads files through the GitLab connections (spec B FR30). The token never leaves
// the implementation: it is looked up per call and sent to the connection's own REST root only.
type gitLabFiles interface {
	// Match returns the connection whose host key is hostKey; ok is false when there is none. An
	// error is a failure to read the connections.
	Match(ctx context.Context, hostKey string) (conn GitLabFileConn, ok bool, err error)
	// Raw returns the bytes of file at ref in project (a full project path) through conn, at most
	// limit of them. A failure is one of the ErrGitLabFile* sentinels, a *GitLabFileStatusError,
	// or another error (a transport failure or a canceled ctx).
	Raw(ctx context.Context, conn GitLabFileConn, project, file, ref string, limit int) ([]byte, error)
}

// Failures of gitLabFiles.Raw the importer words for the user.
var (
	// ErrGitLabFileNotFound is a 404: no such project, ref or file, or the token cannot see it.
	ErrGitLabFileNotFound = errors.New("gitlab file not found")
	// ErrGitLabFileTooLarge is a file longer than the limit.
	ErrGitLabFileTooLarge = errors.New("gitlab file too large")
	// ErrGitLabFileRejected is a 401 or 403: GitLab refused the connection's token.
	ErrGitLabFileRejected = errors.New("gitlab rejected the token")
	// ErrGitLabFileMoved is a 3xx answer; it is never followed.
	ErrGitLabFileMoved = errors.New("gitlab project moved")
	// ErrGitLabFileRateLimited is a 429.
	ErrGitLabFileRateLimited = errors.New("gitlab rate limit reached")
	// ErrGitLabFileAddress is a connection address the egress guard refuses (link-local).
	ErrGitLabFileAddress = errors.New("gitlab address not allowed")
	// ErrGitLabFileConnectionGone means the connection was removed or replaced between Match and
	// Raw.
	ErrGitLabFileConnectionGone = errors.New("gitlab connection no longer available")
)

// GitLabFileStatusError is any other non-success HTTP answer of the files API.
type GitLabFileStatusError struct{ Status int }

func (e *GitLabFileStatusError) Error() string { return fmt.Sprintf("gitlab file: HTTP %d", e.Status) }

// Importer turns a file, a URL or a community id into the skill core a user confirms. It stores
// nothing.
type Importer struct {
	fetcher urlFetcher
	catalog catalog
	gitlab  gitLabFiles
}

// WithGitLabFiles makes the importer read URLs that belong to a GitLab connection through that
// connection (spec B FR30) instead of the public fetcher. Without it every URL takes the public
// path. It returns i for chaining.
func (i *Importer) WithGitLabFiles(files gitLabFiles) *Importer {
	i.gitlab = files
	return i
}

// NewImporter builds an Importer.
func NewImporter(fetcher urlFetcher, catalog catalog) *Importer {
	return &Importer{fetcher: fetcher, catalog: catalog}
}

// Import kinds.
const (
	KindFile      = "file"
	KindURL       = "url"
	KindCommunity = "community"
)

// ImportRequest is a validated POST /skills/import/preview body. Only the fields of its Kind
// are set.
type ImportRequest struct {
	Kind          string
	Filename      string
	ContentBase64 string
	URL           string
	ID            string
}

// Preview is the skill core shown before anything is saved.
type Preview struct {
	Name          string
	Description   string
	Type          domain.SkillType
	Body          string
	Source        domain.Source
	SourceRef     string
	IncludedFiles []string
	IgnoredFiles  []domain.IgnoredFile
	Warnings      []string
}

// Community returns the catalog cards matching the filter, in catalog order.
func (i *Importer) Community(f domain.CatalogFilter) []domain.CatalogEntry {
	return domain.FilterCatalog(i.catalog.List(), f)
}

// Preview builds the import preview. Every failure the user can act on is a 422 invalid_import
// carrying the domain's message unchanged; a canceled ctx returns its own error.
func (i *Importer) Preview(ctx context.Context, req ImportRequest) (Preview, error) {
	var (
		p   Preview
		err error
	)
	switch req.Kind {
	case KindFile:
		p, err = i.fromFile(req.Filename, req.ContentBase64)
	case KindURL:
		p, err = i.fromURL(ctx, req.URL)
	case KindCommunity:
		p, err = i.fromCommunity(req.ID)
	default:
		err = fmt.Errorf("unknown import kind %q", req.Kind)
	}
	if err != nil {
		return Preview{}, importErr(err)
	}
	return p, nil
}

// importErr maps an ImportError to the 422 invalid_import application error.
func importErr(err error) error {
	var ie *domain.ImportError
	if errors.As(err, &ie) {
		return apperr.Validation(ie.Message).WithCode("invalid_import")
	}
	return err
}

func (i *Importer) fromFile(filename, contentBase64 string) (Preview, error) {
	if domain.UTF16Len(contentBase64) > domain.ImportMaxBase64Chars {
		return Preview{}, domain.InvalidImport(fmt.Sprintf("The upload is larger than %d base64 chars", domain.ImportMaxBase64Chars))
	}
	data := DecodeBase64Lenient(contentBase64)
	if domain.IsZipName(filename) {
		return fromArchive(data, filename, domain.SourceImportedFile, filename)
	}
	if !domain.IsMarkdownName(filename) {
		return Preview{}, domain.InvalidImport("Upload a .md, .markdown, .txt or .zip file")
	}
	if len(data) > domain.ArchiveMaxMarkdownBytes {
		return Preview{}, domain.InvalidImport(fmt.Sprintf("The file is %d bytes; the limit is %d", len(data), domain.ArchiveMaxMarkdownBytes))
	}
	text, ok := domain.DecodeUTF8(data)
	if !ok {
		return Preview{}, errNotUTF8
	}
	c, err := domain.SkillFromMarkdown(text, domain.StemOf(filename))
	if err != nil {
		return Preview{}, err
	}
	return previewOf(c, domain.SourceImportedFile, filename, []string{filename}, nil), nil
}

var errNotUTF8 = domain.InvalidImport("The file is not valid UTF-8 text")

func fromArchive(data []byte, archiveName string, source domain.Source, sourceRef string) (Preview, error) {
	content, err := domain.ReadArchive(data, archiveName)
	if err != nil {
		return Preview{}, err
	}
	c, err := domain.SkillFromMarkdown(content.Markdown, content.Pick.FallbackName)
	if err != nil {
		return Preview{}, err
	}
	return previewOf(c, source, sourceRef, []string{content.Pick.Chosen}, content.Pick.Ignored), nil
}

func (i *Importer) fromURL(ctx context.Context, raw string) (Preview, error) {
	if i.gitlab != nil {
		if key, ok := domain.ImportHostKey(raw); ok {
			conn, matched, err := i.gitlab.Match(ctx, key)
			if err != nil {
				return Preview{}, fmt.Errorf("match gitlab connection: %w", err)
			}
			if matched {
				return i.fromGitLab(ctx, raw, key, conn)
			}
		}
	}
	return i.fromPublic(ctx, raw)
}

// fromGitLab reads a URL of host key key through conn (spec B FR30): the typed URL is only
// parsed, the file is fetched from the connection's REST root with its token, trying one ref
// split after the other until an answer is not a 404. Nothing goes to the public fetcher.
func (i *Importer) fromGitLab(ctx context.Context, raw, key string, conn GitLabFileConn) (Preview, error) {
	link, matched, err := domain.ParseGitLabFileURL(raw, conn.BaseURL)
	if err != nil {
		return Preview{}, err
	}
	if !matched {
		// The host key is the connection's but the path prefix is not: not this connection's URL,
		// so it takes the public path and no token is sent.
		return i.fromPublic(ctx, raw)
	}
	ctx, cancel := context.WithTimeout(ctx, domain.URLImportTimeout)
	defer cancel()
	for _, split := range link.Splits() {
		data, err := i.gitlab.Raw(ctx, conn, link.Project, split.File, split.Ref, domain.URLImportMaxBytes)
		if errors.Is(err, ErrGitLabFileNotFound) {
			continue
		}
		if err != nil {
			return Preview{}, gitLabFileFailure(ctx, err, key, conn)
		}
		return previewFromDocument(data, "", "/"+split.File, hostnameOf(key), raw)
	}
	return Preview{}, domain.InvalidImport("File not found on " + key + ", or the token cannot see it")
}

// fromPublic is the public path of fromURL: validation, the public fetcher, no token.
func (i *Importer) fromPublic(ctx context.Context, raw string) (Preview, error) {
	target, err := domain.ParseImportURL(raw)
	if err != nil {
		return Preview{}, err
	}
	doc, err := i.fetcher.Fetch(ctx, target)
	if err != nil {
		return Preview{}, err
	}
	finalPath := "/"
	if doc.FinalURL != nil {
		finalPath = doc.FinalURL.EscapedPath()
	}
	return previewFromDocument(doc.Bytes, doc.ContentType, finalPath, target.Hostname(), raw)
}

// gitLabFileFailure words a Raw failure; the text never carries the token (it is not in the
// errors, and only fixed text and the connection's name are used).
func gitLabFileFailure(ctx context.Context, err error, key string, conn GitLabFileConn) error {
	var status *GitLabFileStatusError
	switch {
	case ctx.Err() != nil:
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return domain.InvalidImport("Fetching " + key + " timed out")
		}
		return ctx.Err()
	case errors.Is(err, ErrGitLabFileTooLarge):
		return domain.InvalidImport("The document is larger than " + strconv.Itoa(domain.URLImportMaxBytes) + " bytes")
	case errors.Is(err, ErrGitLabFileRejected):
		return domain.InvalidImport("GitLab rejected the token of connection " + conn.Name)
	case errors.Is(err, ErrGitLabFileMoved):
		return domain.InvalidImport("GitLab reports that this project has moved; use its current URL")
	case errors.Is(err, ErrGitLabFileRateLimited):
		return domain.InvalidImport("GitLab rate limit reached; try again later")
	case errors.Is(err, ErrGitLabFileAddress):
		return domain.InvalidImport(key + " resolves to an address that is not allowed")
	case errors.Is(err, ErrGitLabFileConnectionGone):
		return domain.InvalidImport("GitLab connection " + conn.Name + " is no longer available")
	case errors.As(err, &status):
		return domain.InvalidImport(fmt.Sprintf("The server answered HTTP %d", status.Status))
	default:
		return domain.InvalidImport("Could not fetch " + key)
	}
}

// hostnameOf is the host name of a host key ("host" or "host:port").
func hostnameOf(key string) string {
	if u, err := url.Parse("https://" + key); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return key
}

// previewFromDocument turns downloaded bytes into the preview. finalPath names the document
// (its URL path or file path), fallbackHost names the skill when the path has no stem and ref is
// the URL as typed.
func previewFromDocument(data []byte, contentType, finalPath, fallbackHost, ref string) (Preview, error) {
	ref = strings.TrimSpace(ref)
	isZip := domain.IsZipName(finalPath) || hasZipMagic(data) || strings.Contains(strings.ToLower(contentType), "zip")
	if isZip {
		name := domain.StemOf(finalPath)
		if name == "" {
			name = "archive.zip"
		}
		return fromArchive(data, name, domain.SourceImportedURL, ref)
	}
	text, ok := domain.DecodeUTF8(data)
	if !ok {
		return Preview{}, errNotUTF8
	}
	fallback := domain.StemOf(finalPath)
	if fallback == "" {
		fallback = fallbackHost
	}
	c, err := domain.SkillFromMarkdown(text, fallback)
	if err != nil {
		return Preview{}, err
	}
	return previewOf(c, domain.SourceImportedURL, ref, []string{finalPath}, nil), nil
}

func (i *Importer) fromCommunity(id string) (Preview, error) {
	entry, ok := i.catalog.Get(id)
	if !ok {
		return Preview{}, domain.InvalidImport(`Unknown community skill "` + id + `"`)
	}
	c, err := domain.SkillFromMarkdown(entry.Body, entry.Name)
	if err != nil {
		return Preview{}, err
	}
	// The curated entry's own metadata wins over what the body parser derives.
	c.Name, c.Description, c.Type = entry.Name, entry.Description, entry.Type
	return previewOf(c, domain.SourceCommunity, "community:"+entry.ID, []string{entry.Name + ".md"}, nil), nil
}

func previewOf(c domain.Candidate, source domain.Source, sourceRef string, included []string, ignored []domain.IgnoredFile) Preview {
	if ignored == nil {
		ignored = []domain.IgnoredFile{}
	}
	warnings := c.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return Preview{
		Name: c.Name, Description: c.Description, Type: c.Type, Body: c.Body,
		Source: source, SourceRef: sourceRef, IncludedFiles: included, IgnoredFiles: ignored, Warnings: warnings,
	}
}

// hasZipMagic reports whether data starts with the zip local file header, PK\x03\x04.
func hasZipMagic(data []byte) bool { return bytes.HasPrefix(data, []byte("PK\x03\x04")) }

// DecodeBase64Lenient decodes base64 the way Node's Buffer.from(text, "base64") does: characters
// outside the alphabet are skipped, the URL-safe alphabet (- and _) is accepted next to the
// standard one, padding is optional and decoding stops at the first "=". Bits left over after the
// last full byte are dropped.
func DecodeBase64Lenient(text string) []byte {
	out := make([]byte, 0, len(text)/4*3)
	var acc uint32
	bits := 0
loop:
	for i := 0; i < len(text); i++ {
		var v byte
		switch c := text[i]; {
		case c >= 'A' && c <= 'Z':
			v = c - 'A'
		case c >= 'a' && c <= 'z':
			v = c - 'a' + 26
		case c >= '0' && c <= '9':
			v = c - '0' + 52
		case c == '+' || c == '-':
			v = 62
		case c == '/' || c == '_':
			v = 63
		case c == '=':
			break loop
		default:
			continue
		}
		acc = acc<<6 | uint32(v)
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
			acc &= 1<<bits - 1
		}
	}
	return out
}
