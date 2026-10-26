package domain

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"mime"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Reasons an archive entry or a part of the bundle is listed as ignored.
const (
	ReasonNotPartOfPlugin = "not part of a plugin"
	ReasonOSMetadata      = "archive metadata"
	ReasonUnknownKey      = "unknown key"
	ReasonNeverInstalled  = "never installed"
)

// Ignored is a part of the upload that is not installed: an archive entry, an unknown key or a
// section a plugin never installs. PathOrSection is the entry path, the key path (for example
// "agents[2].color") or the section name.
type Ignored struct {
	PathOrSection string
	Reason        string
}

// Upload is a decoded upload: the bundle with its defaults applied (A3) and what was ignored (A7),
// in a stable order. It is not validated: call Validate.
type Upload struct {
	Bundle  Bundle
	Ignored []Ignored
}

// ParseUpload reads an uploaded body. media is the Content-Type: application/zip is read as an
// archive, application/json as the bundle itself, anything else is ErrUnsupportedMedia. Every
// other failure is an *InvalidError (422 invalid_plugin) naming the first broken rule.
//
// Archive rules: at most ArchiveMaxEntries entries; every entry name is checked (absolute, "..",
// backslash, drive letter, control characters); no encrypted entries; only Store and Deflate;
// exactly one devdigest-plugin.json, at the root or in the one top folder holding everything; the
// other entries are not read and are listed. Nothing is extracted to disk and at most
// MaxInflatedBytes+1 bytes are inflated, whatever the central directory declares. The JSON text
// loses a leading BOM and must be valid UTF-8.
func ParseUpload(media string, body []byte) (Upload, error) {
	mt, _, err := mime.ParseMediaType(media)
	if err != nil {
		return Upload{}, ErrUnsupportedMedia
	}
	var (
		text    []byte
		ignored []Ignored
	)
	switch mt {
	case "application/zip":
		text, ignored, err = readArchive(body)
		if err != nil {
			return Upload{}, err
		}
	case "application/json":
		if len(body) > MaxInflatedBytes {
			return Upload{}, invalid("The bundle is larger than %d MiB", MaxInflatedBytes>>20)
		}
		text = body
	default:
		return Upload{}, ErrUnsupportedMedia
	}
	text = bytes.TrimPrefix(text, []byte{0xEF, 0xBB, 0xBF})
	if !validUTF8(text) {
		return Upload{}, invalid("%s is not valid UTF-8", BundleFile)
	}
	bundle, more, err := decodeBundle(text)
	if err != nil {
		return Upload{}, err
	}
	return Upload{Bundle: bundle, Ignored: capIgnored(append(ignored, more...))}, nil
}

var drivePattern = regexp.MustCompile(`^[A-Za-z]:`)

// checkEntryName rejects an archive entry name that could escape a directory if it were ever
// extracted, and any control character.
func checkEntryName(name string) error {
	p := strings.TrimSuffix(name, "/")
	bad := p == "" ||
		strings.HasPrefix(p, "/") ||
		strings.Contains(p, `\`) ||
		drivePattern.MatchString(p)
	if !bad {
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				bad = true
				break
			}
		}
	}
	if !bad {
		for _, r := range p {
			if unicode.IsControl(r) {
				bad = true
				break
			}
		}
	}
	if bad {
		return invalid("The archive entry %s has an unsafe path", quoteShort(name))
	}
	return nil
}

// quoteShort quotes a hostile string for a message, cut to 80 characters.
func quoteShort(s string) string {
	r := []rune(s)
	if len(r) > 80 {
		return strconv.Quote(string(r[:80])) + "..."
	}
	return strconv.Quote(s)
}

func isMetadata(name string) bool {
	return strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store"
}

// readArchive checks the central directory and inflates the one bundle entry.
func readArchive(body []byte) (text []byte, ignored []Ignored, err error) {
	zr, zerr := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if zerr != nil && !errors.Is(zerr, zip.ErrInsecurePath) {
		return nil, nil, invalid("The archive could not be read (not a valid .zip)")
	}
	if len(zr.File) > ArchiveMaxEntries {
		return nil, nil, invalid("The archive has %d entries; the limit is %d", len(zr.File), ArchiveMaxEntries)
	}

	var files []*zip.File
	for _, f := range zr.File {
		if err := checkEntryName(f.Name); err != nil {
			return nil, nil, err
		}
		if f.Flags&0x1 != 0 {
			return nil, nil, invalid("The archive entry %s is encrypted", quoteShort(f.Name))
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return nil, nil, invalid("The archive entry %s uses compression method %d; only Store and Deflate are supported", quoteShort(f.Name), f.Method)
		}
		if !strings.HasSuffix(f.Name, "/") {
			files = append(files, f)
		}
	}

	var candidates []*zip.File
	for _, f := range files {
		if path.Base(f.Name) == BundleFile && !isMetadata(f.Name) {
			candidates = append(candidates, f)
		}
	}
	switch {
	case len(candidates) == 0:
		return nil, nil, invalid("The archive has no %s at its root or inside a single top folder", BundleFile)
	case len(candidates) > 1:
		return nil, nil, invalid("The archive has %d %s entries; exactly one is allowed", len(candidates), BundleFile)
	}
	chosen := candidates[0]
	if dir := path.Dir(chosen.Name); dir != "." {
		if strings.Contains(dir, "/") {
			return nil, nil, invalid("%s must be at the root of the archive or inside a single top folder", BundleFile)
		}
		for _, f := range files {
			if !isMetadata(f.Name) && !strings.HasPrefix(f.Name, dir+"/") {
				return nil, nil, invalid("%s must be at the root of the archive or inside the single top folder that holds every entry", BundleFile)
			}
		}
	}

	for _, f := range files {
		if f == chosen {
			continue
		}
		reason := ReasonNotPartOfPlugin
		if isMetadata(f.Name) {
			reason = ReasonOSMetadata
		}
		ignored = append(ignored, Ignored{PathOrSection: f.Name, Reason: reason})
	}

	text, err = inflateEntry(chosen)
	if err != nil {
		return nil, nil, err
	}
	return text, ignored, nil
}

// inflateEntry reads one entry through its raw compressed bytes under the MaxInflatedBytes+1 cap,
// so a size declared in the directory is never trusted. The cap covers all entries read per
// request: this is the only one.
func inflateEntry(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > MaxInflatedBytes {
		return nil, invalid("%s inflates past %d MiB", BundleFile, MaxInflatedBytes>>20)
	}
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, invalid("%s could not be read", BundleFile)
	}
	var src io.Reader
	switch f.Method {
	case zip.Store:
		src = raw
	default:
		fr := flate.NewReader(raw)
		defer func() { _ = fr.Close() }() // Close only repeats an earlier read error
		src = fr
	}
	out, err := readCapped(src, MaxInflatedBytes, f.UncompressedSize64)
	switch {
	case errors.Is(err, errTooLarge):
		return nil, invalid("%s inflates past %d MiB", BundleFile, MaxInflatedBytes>>20)
	case err != nil:
		return nil, invalid("%s could not be read (corrupt archive entry)", BundleFile)
	}
	if f.CRC32 != 0 && crc32.ChecksumIEEE(out) != f.CRC32 {
		return nil, invalid("%s could not be read (checksum error)", BundleFile)
	}
	return out, nil
}

var errTooLarge = errors.New("more bytes than the cap")

// readCapped reads r to its end but never pulls more than limit+1 bytes from it; it fails with
// errTooLarge when r holds more than limit. The buffer starts at the declared size (hint) when
// that is within the limit, else small, and grows fourfold up to limit+1, so a lying hint cannot
// cost more than the cap.
func readCapped(r io.Reader, limit int64, hint uint64) ([]byte, error) {
	lr := io.LimitReader(r, limit+1)
	start := int64(64 << 10)
	if hint <= uint64(limit) {
		start = max(int64(hint)+1, 512)
	}
	buf := make([]byte, 0, min(start, limit+1))
	for {
		if len(buf) == cap(buf) {
			if int64(cap(buf)) >= limit+1 {
				break
			}
			grown := make([]byte, len(buf), min(int64(cap(buf))*4, limit+1))
			copy(grown, buf)
			buf = grown
		}
		n, err := lr.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read entry: %w", err)
		}
	}
	if int64(len(buf)) > limit {
		return nil, errTooLarge
	}
	return buf, nil
}
