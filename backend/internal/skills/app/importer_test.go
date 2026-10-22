package app_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/skills/adapters/catalog"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
)

// fakeFetcher returns a canned document or error and records the URLs it was asked for.
type fakeFetcher struct {
	doc  domain.FetchedDocument
	err  error
	urls []string
}

func (f *fakeFetcher) Fetch(_ context.Context, u *url.URL) (domain.FetchedDocument, error) {
	f.urls = append(f.urls, u.String())
	return f.doc, f.err
}

func newImporter(t *testing.T, f *fakeFetcher) *app.Importer {
	t.Helper()
	cat, err := catalog.New()
	if err != nil {
		t.Fatalf("catalog.New error = %v", err)
	}
	return app.NewImporter(f, cat)
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func filePreview(t *testing.T, name string, content []byte) (app.Preview, error) {
	t.Helper()
	return newImporter(t, &fakeFetcher{}).Preview(t.Context(), app.ImportRequest{Kind: app.KindFile, Filename: name, ContentBase64: b64(content)})
}

func wantInvalidImport(t *testing.T, err error, message string) {
	t.Helper()
	wantAppErr(t, err, apperr.KindValidation, "invalid_import", message)
}

func makeZip(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeBase64Lenient(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"standard", "aGVsbG8gd29ybGQ=", "hello world"},
		{"missing padding", "aGVsbG8gd29ybGQ", "hello world"},
		{"url-safe alphabet", "_-8", "\xff\xef"},
		{"line breaks and spaces", "aGVs\nbG8g\r\n d29y bGQ=", "hello world"},
		{"foreign characters are skipped", "aGVs*bG8g!d29ybGQ=", "hello world"},
		{"stops at the first padding", "aGk=aGk=", "hi"},
		{"a lone trailing character adds nothing", "aGk=" + "A", "hi"},
		{"empty", "", ""},
		{"only noise", "!!!", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(app.DecodeBase64Lenient(tc.in)); got != tc.want {
				t.Errorf("DecodeBase64Lenient(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDecodeBase64Lenient_MatchesStdOnValidInput(t *testing.T) {
	for n := 0; n < 40; n++ {
		raw := bytes.Repeat([]byte{0xAB, 0x01, 0xFF}, n)[:n]
		if got := app.DecodeBase64Lenient(b64(raw)); !bytes.Equal(got, raw) {
			t.Errorf("DecodeBase64Lenient(std(%d bytes)) = % x, want % x", n, got, raw)
		}
	}
}

func TestImporter_FileMarkdown(t *testing.T) {
	src := "---\nname: \"My Skill\"\ntype: Rubric\n---\n<!-- hidden -->Check the​ tests.\n\nSecond paragraph.\n"
	got, err := filePreview(t, "My Skill.md", []byte(src))
	if err != nil {
		t.Fatalf("Preview error = %v", err)
	}
	want := app.Preview{
		Name: "my-skill", Description: "Check the tests.", Type: domain.TypeCustom,
		Body:   "Check the tests.\n\nSecond paragraph.",
		Source: domain.SourceImportedFile, SourceRef: "My Skill.md", IncludedFiles: []string{"My Skill.md"},
		IgnoredFiles: []domain.IgnoredFile{},
		Warnings: []string{
			"Removed 1 HTML comment(s) (hidden text)", "Removed 1 zero-width character(s)",
			`Name normalized to "my-skill"`, `Unknown type "Rubric" — using "custom"`,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Preview mismatch (-want +got):\n%s", diff)
	}
}

func TestImporter_FileFallbackNameAndBOM(t *testing.T) {
	got, err := filePreview(t, "No Front Matter.TXT", []byte("\xEF\xBB\xBFJust a body."))
	if err != nil {
		t.Fatalf("Preview error = %v", err)
	}
	if got.Name != "no-front-matter" || got.Body != "Just a body." || len(got.Warnings) != 0 || got.Warnings == nil {
		t.Errorf("Preview = name %q body %q warnings %v, want no-front-matter, Just a body., an empty list", got.Name, got.Body, got.Warnings)
	}
}

func TestImporter_FileErrors(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		content []byte
		base64  string // used instead of content when set
		want    string
	}{
		{name: "extension", file: "a.pdf", content: []byte("x"), want: "Upload a .md, .markdown, .txt or .zip file"},
		{name: "no extension", file: "README", content: []byte("x"), want: "Upload a .md, .markdown, .txt or .zip file"},
		{name: "hidden file", file: ".md", content: []byte("x"), want: "Upload a .md, .markdown, .txt or .zip file"},
		{name: "file one byte over", file: "a.md", content: bytes.Repeat([]byte("a"), 262145), want: "The file is 262145 bytes; the limit is 262144"},
		{name: "invalid utf-8", file: "a.md", content: []byte("ok \xff\xfe"), want: "The file is not valid UTF-8 text"},
		{name: "upload too long", file: "a.md", base64: strings.Repeat("A", 2097153), want: "The upload is larger than 2097152 base64 chars"},
		{name: "empty body", file: "a.md", content: []byte("---\nname: x\n---\n"), want: "The skill has no body"},
		{name: "body too long", file: "a.md", content: bytes.Repeat([]byte("a"), 20001), want: "The skill body is 20001 chars; the limit is 20000"},
		{name: "not a zip", file: "a.zip", content: []byte("definitely not a zip"), want: "The archive could not be read (not a valid .zip)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := b64(tc.content)
			if tc.base64 != "" {
				content = tc.base64
			}
			_, err := newImporter(t, &fakeFetcher{}).Preview(t.Context(), app.ImportRequest{Kind: app.KindFile, Filename: tc.file, ContentBase64: content})
			wantInvalidImport(t, err, tc.want)
		})
	}
}

func TestImporter_FileAtTheSizeLimitIsAccepted(t *testing.T) {
	// The padding sits in the front matter so that the body stays under its own limit.
	head, tail := "---\nnote: ", "\n---\nBody."
	content := head + strings.Repeat("a", 262144-len(head)-len(tail)) + tail
	if _, err := filePreview(t, "a.md", []byte(content)); err != nil {
		t.Errorf("Preview(262144-byte file) error = %v, want nil", err)
	}
}

func TestImporter_FileDecodesLenientBase64(t *testing.T) {
	enc := b64([]byte("Hello body."))
	enc = strings.TrimRight(enc, "=")
	enc = enc[:4] + "\n" + enc[4:]
	got, err := newImporter(t, &fakeFetcher{}).Preview(t.Context(), app.ImportRequest{Kind: app.KindFile, Filename: "a.md", ContentBase64: enc})
	if err != nil || got.Body != "Hello body." {
		t.Errorf("Preview(lenient base64) = %q, %v, want Hello body.", got.Body, err)
	}
}

func TestImporter_FileZip(t *testing.T) {
	archive := makeZip(t,
		[2]string{"pack/SKILL.md", "---\ndescription: Does it.\n---\nBody text."},
		[2]string{"pack/README.md", "readme"},
		[2]string{"pack/scripts/run.sh", "echo"},
		[2]string{"pack/logo.png", "png"},
		[2]string{"__MACOSX/x", "junk"},
		[2]string{"pack/.DS_Store", "junk"},
	)
	got, err := filePreview(t, "bundle.zip", archive)
	if err != nil {
		t.Fatalf("Preview error = %v", err)
	}
	want := app.Preview{
		Name: "pack", Description: "Does it.", Type: domain.TypeCustom, Body: "Body text.",
		Source: domain.SourceImportedFile, SourceRef: "bundle.zip", IncludedFiles: []string{"pack/SKILL.md"},
		IgnoredFiles: []domain.IgnoredFile{
			{Path: "pack/README.md", Reason: "reference_doc"},
			{Path: "pack/scripts/run.sh", Reason: "executable"},
			{Path: "pack/logo.png", Reason: "not_markdown"},
		},
		Warnings: []string{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Preview mismatch (-want +got):\n%s", diff)
	}
}

func TestImporter_FileZipRootSkillMDNamedAfterTheArchive(t *testing.T) {
	got, err := filePreview(t, "my-pack.zip", makeZip(t, [2]string{"SKILL.md", "Body."}))
	if err != nil || got.Name != "my-pack" {
		t.Errorf("Preview = name %q, %v, want my-pack", got.Name, err)
	}
}

func TestImporter_URL(t *testing.T) {
	final, _ := url.Parse("https://raw.githubusercontent.com/o/r/main/skills/x.md")
	f := &fakeFetcher{doc: domain.FetchedDocument{Bytes: []byte("# Title\n\nReview the diff."), ContentType: "text/plain", FinalURL: final}}
	got, err := newImporter(t, f).Preview(t.Context(), app.ImportRequest{
		Kind: app.KindURL, URL: "  https://github.com/o/r/blob/main/skills/x.md?plain=1  ",
	})
	if err != nil {
		t.Fatalf("Preview error = %v", err)
	}
	if diff := cmp.Diff([]string{"https://raw.githubusercontent.com/o/r/main/skills/x.md"}, f.urls); diff != "" {
		t.Errorf("fetched URLs mismatch (-want +got):\n%s", diff)
	}
	want := app.Preview{
		Name: "x", Description: "Review the diff.", Type: domain.TypeCustom, Body: "# Title\n\nReview the diff.",
		Source: domain.SourceImportedURL, SourceRef: "https://github.com/o/r/blob/main/skills/x.md?plain=1",
		IncludedFiles: []string{"/o/r/main/skills/x.md"}, IgnoredFiles: []domain.IgnoredFile{}, Warnings: []string{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Preview mismatch (-want +got):\n%s", diff)
	}
}

func TestImporter_URLFallbackNameIsTheHostForAPathWithoutAStem(t *testing.T) {
	final, _ := url.Parse("https://example.com/")
	f := &fakeFetcher{doc: domain.FetchedDocument{Bytes: []byte("Body."), FinalURL: final}}
	got, err := newImporter(t, f).Preview(t.Context(), app.ImportRequest{Kind: app.KindURL, URL: "https://example.com/"})
	if err != nil || got.Name != "example-com" {
		t.Errorf("Preview = name %q, %v, want example-com", got.Name, err)
	}
}

func TestImporter_URLZipDetection(t *testing.T) {
	archive := makeZip(t, [2]string{"SKILL.md", "Zipped body."})
	tests := []struct {
		name, finalPath, contentType string
		data                         []byte
	}{
		{"by extension", "/dl/pack.zip", "application/octet-stream", archive},
		{"by magic bytes", "/dl/download", "application/octet-stream", archive},
		{"by content type", "/dl/download", "Application/X-Zip-Compressed", archive},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			final, _ := url.Parse("https://h.example" + tc.finalPath)
			f := &fakeFetcher{doc: domain.FetchedDocument{Bytes: tc.data, ContentType: tc.contentType, FinalURL: final}}
			got, err := newImporter(t, f).Preview(t.Context(), app.ImportRequest{Kind: app.KindURL, URL: "https://h.example/start"})
			if err != nil {
				t.Fatalf("Preview error = %v", err)
			}
			if got.Body != "Zipped body." || got.Source != domain.SourceImportedURL || got.SourceRef != "https://h.example/start" ||
				diff(got.IncludedFiles, []string{"SKILL.md"}) != "" {
				t.Errorf("Preview = %+v, want the archive's SKILL.md imported from the typed URL", got)
			}
		})
	}
}

func diff[T any](a, b T) string { return cmp.Diff(a, b) }

func TestImporter_URLRejectsBeforeFetching(t *testing.T) {
	tests := []struct{ url, want string }{
		{"http://h/a.md", "Only https:// URLs can be imported"},
		{"https://h:8443/a.md", "Only the default https port (443) is allowed"},
		{"https://u:p@h/a.md", "URLs with credentials are not allowed"},
		{"https://127.0.0.1/a.md", "127.0.0.1 is not a public address"},
		{"https://0x7f.1/a.md", "127.0.0.1 is not a public address"},
		{"https://[::ffff:10.0.0.1]/a.md", "::ffff:a00:1 is not a public address"},
		{"https://10.1.2.3/a.md", "10.1.2.3 is not a public address"},
		{"not a url", "Not a valid URL"},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			f := &fakeFetcher{}
			_, err := newImporter(t, f).Preview(t.Context(), app.ImportRequest{Kind: app.KindURL, URL: tc.url})
			wantInvalidImport(t, err, tc.want)
			if len(f.urls) != 0 {
				t.Errorf("fetcher called with %v, want no connection attempt", f.urls)
			}
		})
	}
}

func TestImporter_URLFetcherErrors(t *testing.T) {
	t.Run("import errors keep their message", func(t *testing.T) {
		f := &fakeFetcher{err: domain.InvalidImport("The server answered HTTP 404")}
		_, err := newImporter(t, f).Preview(t.Context(), app.ImportRequest{Kind: app.KindURL, URL: "https://h.example/a.md"})
		wantInvalidImport(t, err, "The server answered HTTP 404")
	})
	t.Run("cancellation passes through unchanged", func(t *testing.T) {
		f := &fakeFetcher{err: context.Canceled}
		_, err := newImporter(t, f).Preview(t.Context(), app.ImportRequest{Kind: app.KindURL, URL: "https://h.example/a.md"})
		if err != context.Canceled {
			t.Errorf("Preview error = %v, want context.Canceled", err)
		}
	})
	t.Run("non UTF-8 document", func(t *testing.T) {
		final, _ := url.Parse("https://h.example/a.md")
		f := &fakeFetcher{doc: domain.FetchedDocument{Bytes: []byte("\xff\xfe"), FinalURL: final}}
		_, err := newImporter(t, f).Preview(t.Context(), app.ImportRequest{Kind: app.KindURL, URL: "https://h.example/a.md"})
		wantInvalidImport(t, err, "The file is not valid UTF-8 text")
	})
}

func TestImporter_Community(t *testing.T) {
	f := &fakeFetcher{}
	got, err := newImporter(t, f).Preview(t.Context(), app.ImportRequest{Kind: app.KindCommunity, ID: "sql-injection-gate"})
	if err != nil {
		t.Fatalf("Preview error = %v", err)
	}
	if got.Name != "sql-injection-gate" || got.Type != domain.TypeSecurity || got.Description == "" || !strings.Contains(got.Body, "${req.params.id}") {
		t.Errorf("Preview = name %q type %q body has ${req.params.id}: %v, want the catalog entry", got.Name, got.Type, strings.Contains(got.Body, "${req.params.id}"))
	}
	if got.Source != domain.SourceCommunity || got.SourceRef != "community:sql-injection-gate" ||
		diff(got.IncludedFiles, []string{"sql-injection-gate.md"}) != "" || len(got.IgnoredFiles) != 0 || len(got.Warnings) != 0 {
		t.Errorf("Preview = %+v, want community provenance and no warnings", got)
	}
	if len(f.urls) != 0 {
		t.Errorf("fetcher called with %v, want no network access", f.urls)
	}

	_, err = newImporter(t, f).Preview(t.Context(), app.ImportRequest{Kind: app.KindCommunity, ID: `no"pe`})
	wantInvalidImport(t, err, `Unknown community skill "no"pe"`)
}

func TestImporter_CommunityList(t *testing.T) {
	imp := newImporter(t, &fakeFetcher{})
	if got := imp.Community(domain.CatalogFilter{}); len(got) != 8 {
		t.Errorf("Community(no filter) = %d cards, want 8", len(got))
	}
	var tagged []string
	for _, e := range imp.Community(domain.CatalogFilter{Tag: "REACT"}) {
		tagged = append(tagged, e.ID)
	}
	if diff := cmp.Diff([]string{"react-hooks-rules", "a11y-jsx-audit"}, tagged); diff != "" {
		t.Errorf("Community(tag=REACT) mismatch (-want +got):\n%s", diff)
	}
	if got := imp.Community(domain.CatalogFilter{Lang: "sql", Q: "xyz"}); len(got) != 0 {
		t.Errorf("Community(lang=sql q=xyz) = %d cards, want 0", len(got))
	}
}
