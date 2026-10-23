// Package zipentry reads one named entry out of a downloaded artifact ZIP (spec 7.7.4). It works
// on bytes only, never writes to disk, and never trusts a size the ZIP declares: the central
// directory size is checked before anything is inflated, and the entry is then inflated through
// its raw compressed bytes under an io.LimitReader of maxBytes+1.
package zipentry

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"hash/crc32"
	"io"

	"dev-digest-go/backend/internal/ci/app"
)

// flagEncrypted is bit 0 of the general purpose flags: the entry is encrypted.
const flagEncrypted = 0x1

// Reader implements app.ArtifactReader with archive/zip.
type Reader struct{}

var _ app.ArtifactReader = (*Reader)(nil)

// New returns a Reader; it holds no state.
func New() *Reader { return &Reader{} }

// Entry returns the bytes of the one entry called name.
//
// app.ErrEntryTooLarge: the entry's declared size is above maxBytes (nothing is inflated), or it
// inflates to more than maxBytes whatever it declares. app.ErrEntryInvalid: the bytes are not a
// readable ZIP; the ZIP has no entry or more than one entry of that name; the entry is encrypted,
// uses an unsupported method, does not inflate cleanly, or its inflated length or CRC-32 differs
// from what the directory declares.
func (Reader) Entry(data []byte, name string, maxBytes int64) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, app.ErrEntryInvalid
	}
	var entry *zip.File
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		if entry != nil {
			return nil, app.ErrEntryInvalid
		}
		entry = f
	}
	if entry == nil {
		return nil, app.ErrEntryInvalid
	}
	if entry.UncompressedSize64 > uint64(max(maxBytes, 0)) {
		return nil, app.ErrEntryTooLarge
	}
	return inflate(entry, maxBytes)
}

// inflate reads the entry through OpenRaw: File.Open checks the inflated length against the
// declared one and fails with an unrelated error before the cap can apply.
func inflate(f *zip.File, maxBytes int64) ([]byte, error) {
	if f.Flags&flagEncrypted != 0 {
		return nil, app.ErrEntryInvalid
	}
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, app.ErrEntryInvalid
	}
	var src io.Reader
	switch f.Method {
	case zip.Store:
		src = raw
	case zip.Deflate:
		fr := flate.NewReader(raw)
		defer func() { _ = fr.Close() }() // Close only repeats a read error
		src = fr
	default:
		return nil, app.ErrEntryInvalid
	}
	out, err := io.ReadAll(io.LimitReader(src, max(maxBytes, 0)+1))
	if err != nil {
		return nil, app.ErrEntryInvalid
	}
	if int64(len(out)) > maxBytes {
		return nil, app.ErrEntryTooLarge
	}
	if uint64(len(out)) != f.UncompressedSize64 || crc32.ChecksumIEEE(out) != f.CRC32 {
		return nil, app.ErrEntryInvalid
	}
	return out, nil
}
