package domain

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"slices"
	"strings"
)

// Limits of a zip import.
const (
	// ArchiveMaxEntries is the most entries an archive may list.
	ArchiveMaxEntries = 500
	// ArchiveMaxMarkdownBytes is the largest markdown entry that is read.
	ArchiveMaxMarkdownBytes = 256 * 1024
	// ArchiveInflateCap is the most bytes ever inflated for the chosen entry: one more than
	// ArchiveMaxMarkdownBytes, so that an oversize entry is detected without inflating all of it.
	ArchiveInflateCap = ArchiveMaxMarkdownBytes + 1
)

// Reasons an archive entry is not read.
const (
	ReasonExecutable   = "executable"
	ReasonReferenceDoc = "reference_doc"
	ReasonNotMarkdown  = "not_markdown"
)

var (
	executableDirs = []string{"scripts", "bin", "hooks"}
	executableExts = []string{
		".sh", ".bash", ".zsh", ".fish", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".rb", ".pl",
		".php", ".go", ".rs", ".java", ".jar", ".exe", ".bat", ".cmd", ".ps1", ".bin", ".so", ".dylib", ".dll",
		".wasm", ".lua", ".swift", ".kt",
	}
)

// ArchiveEntry is one archive entry as the central directory lists it. Size is what the entry
// declares, not what it inflates to.
type ArchiveEntry struct {
	Path string
	Size uint64
}

// IgnoredFile is an archive entry that was not read, and why.
type IgnoredFile struct {
	Path   string
	Reason string
}

// ArchivePick is the outcome of choosing the one markdown entry of an archive.
type ArchivePick struct {
	// Chosen is the path of the only entry that is read.
	Chosen string
	// FallbackName names the skill when the front matter has no name.
	FallbackName string
	// Ignored lists every other entry, in archive order.
	Ignored []IgnoredFile
}

// ArchiveContent is a picked archive entry together with its text.
type ArchiveContent struct {
	Markdown string
	Pick     ArchivePick
}

// isNoise reports OS metadata an archiver adds; it is dropped silently.
func isNoise(path string) bool {
	return strings.HasPrefix(path, "__MACOSX/") || BaseName(path) == ".DS_Store" || strings.HasSuffix(path, "/")
}

func ignoreReason(path string) string {
	dirs := strings.Split(path, "/")
	dirs = dirs[:len(dirs)-1]
	for _, d := range dirs {
		if slices.Contains(executableDirs, strings.ToLower(d)) {
			return ReasonExecutable
		}
	}
	if slices.Contains(executableExts, FileExt(path)) {
		return ReasonExecutable
	}
	if IsMarkdownName(path) {
		return ReasonReferenceDoc
	}
	return ReasonNotMarkdown
}

// PickArchiveEntry chooses the markdown entry of a skill archive: SKILL.md at the root (or inside
// the single top folder), else the only markdown file there. archiveName names the skill when a
// SKILL.md has no folder. It fails with an ImportError on too many entries, no or several
// candidates, or a chosen entry that declares more than ArchiveMaxMarkdownBytes.
func PickArchiveEntry(entries []ArchiveEntry, archiveName string) (ArchivePick, error) {
	if len(entries) > ArchiveMaxEntries {
		return ArchivePick{}, InvalidImport(fmt.Sprintf("The archive has %d entries; the limit is %d", len(entries), ArchiveMaxEntries))
	}
	var files []ArchiveEntry
	for _, e := range entries {
		if !isNoise(e.Path) {
			files = append(files, e)
		}
	}
	if len(files) == 0 {
		return ArchivePick{}, InvalidImport("The archive is empty")
	}

	// A single top folder (`my-skill/SKILL.md`, …) counts as the root.
	tops := map[string]struct{}{}
	for _, f := range files {
		top, _, nested := strings.Cut(f.Path, "/")
		if !nested {
			top = ""
		}
		tops[top] = struct{}{}
	}
	topFolder := ""
	if len(tops) == 1 {
		for t := range tops {
			topFolder = t
		}
	}
	root := ""
	if topFolder != "" {
		root = topFolder + "/"
	}
	atRoot := func(e ArchiveEntry) bool {
		rest, ok := strings.CutPrefix(e.Path, root)
		return ok && !strings.Contains(rest, "/")
	}

	var skillMD, rootMD []ArchiveEntry
	for _, f := range files {
		if !atRoot(f) {
			continue
		}
		if strings.EqualFold(BaseName(f.Path), "skill.md") {
			skillMD = append(skillMD, f)
		}
		if IsMarkdownName(f.Path) {
			rootMD = append(rootMD, f)
		}
	}
	candidates := rootMD
	if len(skillMD) > 0 {
		candidates = skillMD
	}
	switch {
	case len(candidates) == 0:
		return ArchivePick{}, InvalidImport("The archive has no SKILL.md and no markdown file at its root")
	case len(candidates) > 1:
		return ArchivePick{}, InvalidImport(fmt.Sprintf("The archive has %d candidate markdown files at its root; add a SKILL.md to choose one", len(candidates)))
	}
	chosen := candidates[0]
	if chosen.Size > ArchiveMaxMarkdownBytes {
		return ArchivePick{}, InvalidImport(fmt.Sprintf("%s is %d bytes; the limit is %d", chosen.Path, chosen.Size, ArchiveMaxMarkdownBytes))
	}

	pick := ArchivePick{Chosen: chosen.Path, Ignored: []IgnoredFile{}}
	switch {
	case len(skillMD) == 0:
		pick.FallbackName = StemOf(chosen.Path)
	case topFolder != "":
		pick.FallbackName = topFolder
	default:
		pick.FallbackName = StemOf(archiveName)
	}
	for _, f := range files {
		if f != chosen {
			pick.Ignored = append(pick.Ignored, IgnoredFile{Path: f.Path, Reason: ignoreReason(f.Path)})
		}
	}
	return pick, nil
}

// ReadArchive reads the central directory of a zip, picks the one markdown entry and returns its
// text. Nothing is extracted to disk and at most ArchiveInflateCap bytes are inflated, whatever
// the entry declares. Every failure is an ImportError.
func ReadArchive(data []byte, archiveName string) (ArchiveContent, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return ArchiveContent{}, InvalidImport("The archive could not be read (not a valid .zip)")
	}
	entries := make([]ArchiveEntry, len(zr.File))
	for i, f := range zr.File {
		entries[i] = ArchiveEntry{Path: f.Name, Size: f.UncompressedSize64}
	}
	pick, err := PickArchiveEntry(entries, archiveName)
	if err != nil {
		return ArchiveContent{}, err
	}
	i := slices.IndexFunc(zr.File, func(f *zip.File) bool { return f.Name == pick.Chosen })
	raw, err := readEntry(zr.File[i])
	if err != nil {
		return ArchiveContent{}, InvalidImport(fmt.Sprintf("%s could not be read: %s", pick.Chosen, err))
	}
	text, ok := DecodeUTF8(raw)
	if !ok {
		return ArchiveContent{}, InvalidImport(fmt.Sprintf("%s could not be read: The encoded data was not valid for encoding utf-8", pick.Chosen))
	}
	return ArchiveContent{Markdown: text, Pick: pick}, nil
}

// readEntry inflates one entry through its raw compressed bytes, under its own cap, so a size
// declared in the directory is never trusted.
func readEntry(f *zip.File) ([]byte, error) {
	if f.Flags&0x1 != 0 {
		return nil, errors.New("encrypted entries are not supported")
	}
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, err
	}
	var src io.Reader
	switch f.Method {
	case zip.Store:
		src = raw
	case zip.Deflate:
		fr := flate.NewReader(raw)
		defer func() { _ = fr.Close() }() // Close only repeats an earlier read error
		src = fr
	default:
		return nil, fmt.Errorf("unsupported compression method %d", f.Method)
	}
	out, err := inflateCapped(src, ArchiveMaxMarkdownBytes)
	if err != nil {
		return nil, err
	}
	if f.CRC32 != 0 && crc32.ChecksumIEEE(out) != f.CRC32 {
		return nil, errors.New("checksum error")
	}
	return out, nil
}

// inflateCapped reads at most limit+1 bytes from r and fails when r holds more than limit.
func inflateCapped(r io.Reader, limit int64) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("entry is larger than %d bytes", limit)
	}
	return out, nil
}
