package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
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

// Importer turns a file, a URL or a community id into the skill core a user confirms. It stores
// nothing.
type Importer struct {
	fetcher urlFetcher
	catalog catalog
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
	ref := strings.TrimSpace(raw)
	isZip := domain.IsZipName(finalPath) || hasZipMagic(doc.Bytes) || strings.Contains(strings.ToLower(doc.ContentType), "zip")
	if isZip {
		name := domain.StemOf(finalPath)
		if name == "" {
			name = "archive.zip"
		}
		return fromArchive(doc.Bytes, name, domain.SourceImportedURL, ref)
	}
	text, ok := domain.DecodeUTF8(doc.Bytes)
	if !ok {
		return Preview{}, errNotUTF8
	}
	fallback := domain.StemOf(finalPath)
	if fallback == "" {
		fallback = target.Hostname()
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
