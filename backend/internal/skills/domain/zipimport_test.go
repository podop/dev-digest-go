package domain_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/skills/domain"
)

type zipFile struct{ name, content string }

// buildZip writes a deflated archive; a name ending in "/" is a directory entry.
func buildZip(t *testing.T, files ...zipFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatalf("Create(%q) error = %v", f.name, err)
		}
		if _, err := w.Write([]byte(f.content)); err != nil {
			t.Fatalf("Write(%q) error = %v", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close() error = %v", err)
	}
	return buf.Bytes()
}

// buildForgedZip writes one entry whose directory record declares declaredSize bytes while the
// deflate stream holds the real content.
func buildForgedZip(t *testing.T, name string, real []byte, declaredSize uint64) []byte {
	t.Helper()
	var packed bytes.Buffer
	fw, err := flate.NewWriter(&packed, flate.BestSpeed)
	if err != nil {
		t.Fatalf("flate.NewWriter() error = %v", err)
	}
	if _, err := fw.Write(real); err != nil {
		t.Fatalf("flate Write() error = %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("flate Close() error = %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               name,
		Method:             zip.Deflate,
		CRC32:              crc32.ChecksumIEEE(real),
		CompressedSize64:   uint64(packed.Len()),
		UncompressedSize64: declaredSize,
	})
	if err != nil {
		t.Fatalf("CreateRaw() error = %v", err)
	}
	if _, err := w.Write(packed.Bytes()); err != nil {
		t.Fatalf("CreateRaw Write() error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close() error = %v", err)
	}
	return buf.Bytes()
}

func TestReadArchive_AC253(t *testing.T) {
	data := buildZip(t,
		zipFile{"pack/SKILL.md", "---\ndescription: d\n---\nThe body"},
		zipFile{"pack/README.md", "readme"},
		zipFile{"pack/scripts/run.sh", "echo hi"},
		zipFile{"pack/logo.png", "png"},
		zipFile{"__MACOSX/x", "meta"},
		zipFile{"pack/.DS_Store", "ds"},
		zipFile{"pack/docs/", ""},
	)
	got, err := domain.ReadArchive(data, "ignored.zip")
	if err != nil {
		t.Fatalf("ReadArchive() error = %v", err)
	}
	want := domain.ArchiveContent{
		Markdown: "---\ndescription: d\n---\nThe body",
		Pick: domain.ArchivePick{
			Chosen:       "pack/SKILL.md",
			FallbackName: "pack",
			Ignored: []domain.IgnoredFile{
				{Path: "pack/README.md", Reason: "reference_doc"},
				{Path: "pack/scripts/run.sh", Reason: "executable"},
				{Path: "pack/logo.png", Reason: "not_markdown"},
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ReadArchive() mismatch (-want +got):\n%s", diff)
	}
}

func TestReadArchive_Selection(t *testing.T) {
	tests := []struct {
		name         string
		files        []zipFile
		archiveName  string
		wantChosen   string
		wantFallback string
	}{
		{"root SKILL.md uses the archive stem", []zipFile{{"skill.md", "b"}, {"notes.md", "n"}}, "my-pack.zip", "skill.md", "my-pack"},
		{"single root markdown uses its stem", []zipFile{{"Guide.markdown", "b"}, {"a.png", "p"}}, "x.zip", "Guide.markdown", "Guide"},
		{"nested files are not root candidates", []zipFile{{"a.md", "b"}, {"sub/b.md", "n"}}, "x.zip", "a.md", "a"},
		{"SKILL.md wins over other markdown", []zipFile{{"a/other.md", "o"}, {"a/SKILL.MD", "b"}}, "x.zip", "a/SKILL.MD", "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ReadArchive(buildZip(t, tc.files...), tc.archiveName)
			if err != nil {
				t.Fatalf("ReadArchive() error = %v", err)
			}
			if got.Pick.Chosen != tc.wantChosen || got.Pick.FallbackName != tc.wantFallback {
				t.Errorf("chosen/fallback = %q/%q, want %q/%q", got.Pick.Chosen, got.Pick.FallbackName, tc.wantChosen, tc.wantFallback)
			}
		})
	}
}

func TestReadArchive_AC254(t *testing.T) {
	many := make([]zipFile, 501)
	for i := range many {
		many[i] = zipFile{fmt.Sprintf("f%03d.png", i), "x"}
	}
	tests := []struct {
		name    string
		data    []byte
		wantMsg string
	}{
		{"two root markdown files", buildZip(t, zipFile{"a.md", "1"}, zipFile{"b.md", "2"}),
			"The archive has 2 candidate markdown files at its root; add a SKILL.md to choose one"},
		{"no markdown", buildZip(t, zipFile{"a.png", "1"}),
			"The archive has no SKILL.md and no markdown file at its root"},
		{"501 entries", buildZip(t, many...), "The archive has 501 entries; the limit is 500"},
		{"not a zip", []byte("definitely not a zip file"), "The archive could not be read (not a valid .zip)"},
		{"empty input", nil, "The archive could not be read (not a valid .zip)"},
		{"only noise", buildZip(t, zipFile{"__MACOSX/a", "x"}, zipFile{"d/", ""}), "The archive is empty"},
		{"declares 100 inflates 300000", buildForgedZip(t, "SKILL.md", bytes.Repeat([]byte("a"), 300_000), 100),
			"SKILL.md could not be read: entry is larger than 262144 bytes"},
		{"declares more than the limit", buildForgedZip(t, "SKILL.md", []byte("small"), 262_145),
			"SKILL.md is 262145 bytes; the limit is 262144"},
		{"invalid UTF-8", buildZip(t, zipFile{"SKILL.md", "ok \xff\xfe"}),
			"SKILL.md could not be read: The encoded data was not valid for encoding utf-8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ReadArchive(tc.data, "x.zip")
			if got := importMessage(t, err); got != tc.wantMsg {
				t.Errorf("ReadArchive() message = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

func TestReadArchive_ExactLimit(t *testing.T) {
	body := strings.Repeat("a", 262_144)
	got, err := domain.ReadArchive(buildZip(t, zipFile{"SKILL.md", body}), "x.zip")
	if err != nil {
		t.Fatalf("ReadArchive(262144 bytes) error = %v", err)
	}
	if len(got.Markdown) != 262_144 {
		t.Errorf("len(Markdown) = %d, want 262144", len(got.Markdown))
	}
}

func TestReadArchive_BombIsBounded(t *testing.T) {
	// 64 MiB of zeros deflates to ~64 KiB; the declared size lies. Reading must stop at the cap.
	bomb := buildForgedZip(t, "SKILL.md", make([]byte, 64<<20), 10)
	if len(bomb) > 1<<20 {
		t.Fatalf("bomb archive is %d bytes, want a small archive", len(bomb))
	}
	_, err := domain.ReadArchive(bomb, "x.zip")
	if got, want := importMessage(t, err), "SKILL.md could not be read: entry is larger than 262144 bytes"; got != want {
		t.Errorf("ReadArchive(bomb) message = %q, want %q", got, want)
	}
}

func TestReadArchive_BOMAndChecksum(t *testing.T) {
	got, err := domain.ReadArchive(buildZip(t, zipFile{"SKILL.md", "\xEF\xBB\xBFbody"}), "x.zip")
	if err != nil || got.Markdown != "body" {
		t.Errorf("ReadArchive(BOM) = %q, %v; want %q, nil", got.Markdown, err, "body")
	}

	good := buildZip(t, zipFile{"SKILL.md", "body text that is long enough to find"})
	// Flip a bit of the stored CRC in the central directory (the last record before the end record).
	i := bytes.LastIndex(good, []byte("PK\x01\x02"))
	bad := bytes.Clone(good)
	bad[i+16] ^= 0xff
	_, err = domain.ReadArchive(bad, "x.zip")
	if got, want := importMessage(t, err), "SKILL.md could not be read: checksum error"; got != want {
		t.Errorf("ReadArchive(bad CRC) message = %q, want %q", got, want)
	}
}

func TestPickArchiveEntry_IgnoreReasons(t *testing.T) {
	entries := []domain.ArchiveEntry{
		{Path: "p/SKILL.md", Size: 1},
		{Path: "p/Scripts/x.txt", Size: 1},
		{Path: "p/hooks/y.md", Size: 1},
		{Path: "p/tool.PY", Size: 1},
		{Path: "p/a.wasm", Size: 1},
		{Path: "p/ref.txt", Size: 1},
		{Path: "p/data.json", Size: 1},
		{Path: "p/lib/bin/z.png", Size: 1},
	}
	got, err := domain.PickArchiveEntry(entries, "p.zip")
	if err != nil {
		t.Fatalf("PickArchiveEntry() error = %v", err)
	}
	want := []domain.IgnoredFile{
		{Path: "p/Scripts/x.txt", Reason: "executable"},
		{Path: "p/hooks/y.md", Reason: "executable"},
		{Path: "p/tool.PY", Reason: "executable"},
		{Path: "p/a.wasm", Reason: "executable"},
		{Path: "p/ref.txt", Reason: "reference_doc"},
		{Path: "p/data.json", Reason: "not_markdown"},
		{Path: "p/lib/bin/z.png", Reason: "executable"},
	}
	if diff := cmp.Diff(want, got.Ignored); diff != "" {
		t.Errorf("Ignored mismatch (-want +got):\n%s", diff)
	}
}

func TestPickArchiveEntry_NoSingleTopFolder(t *testing.T) {
	// Two top folders: nothing sits at the root, so there is no candidate.
	_, err := domain.PickArchiveEntry([]domain.ArchiveEntry{{Path: "a/SKILL.md", Size: 1}, {Path: "b/x.md", Size: 1}}, "x.zip")
	if got, want := importMessage(t, err), "The archive has no SKILL.md and no markdown file at its root"; got != want {
		t.Errorf("PickArchiveEntry() message = %q, want %q", got, want)
	}
}
