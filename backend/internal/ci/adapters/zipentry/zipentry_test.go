package zipentry_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"hash/crc32"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/ci/adapters/zipentry"
	"dev-digest-go/backend/internal/ci/app"
)

const (
	entryName = "devdigest-result.json"
	capBytes  = 64 * 1024
)

// rawEntry describes one hand-built entry: the compressed bytes are written as they are and the
// directory declares exactly what the test says, so a lying ZIP can be made.
type rawEntry struct {
	name     string
	method   uint16
	flags    uint16
	compress []byte
	declared uint64
	crc      uint32
}

func deflate(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		t.Fatalf("flate.NewWriter = %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("flate write = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("flate close = %v", err)
	}
	return buf.Bytes()
}

// honest builds the entry a well-behaved writer would.
func honest(t *testing.T, name string, method uint16, data []byte) rawEntry {
	t.Helper()
	c := data
	if method == zip.Deflate {
		c = deflate(t, data)
	}
	return rawEntry{name: name, method: method, compress: c, declared: uint64(len(data)), crc: crc32.ChecksumIEEE(data)}
}

func build(t *testing.T, entries ...rawEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{
			Name:               e.name,
			Method:             e.method,
			Flags:              e.flags,
			CRC32:              e.crc,
			UncompressedSize64: e.declared,
			CompressedSize64:   uint64(len(e.compress)),
		}
		w, err := zw.CreateRaw(h)
		if err != nil {
			t.Fatalf("CreateRaw(%q) = %v", e.name, err)
		}
		if _, err := w.Write(e.compress); err != nil {
			t.Fatalf("write %q = %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close = %v", err)
	}
	return buf.Bytes()
}

func TestEntry(t *testing.T) {
	payload := []byte(`{"findings_count":0}`)
	exactCap := bytes.Repeat([]byte{'a'}, capBytes)
	overCap := bytes.Repeat([]byte{'a'}, capBytes+1)

	bomb := honest(t, entryName, zip.Deflate, bytes.Repeat([]byte{0}, 32<<20))
	bomb.declared = 1024 // the directory lies: it declares 1 KiB

	garbage := rawEntry{name: entryName, method: zip.Deflate, compress: []byte{0xff, 0xfe, 0xfd, 0xfc}, declared: 10, crc: 1}
	declaredBig := garbage
	declaredBig.declared = capBytes + 1

	liesSmall := honest(t, entryName, zip.Deflate, bytes.Repeat([]byte{'b'}, 2048))
	liesSmall.declared = 1024

	badCRC := honest(t, entryName, zip.Deflate, payload)
	badCRC.crc ^= 0xdeadbeef

	truncated := honest(t, entryName, zip.Deflate, bytes.Repeat([]byte("0123456789"), 500))
	truncated.compress = truncated.compress[:len(truncated.compress)/2]

	encrypted := honest(t, entryName, zip.Store, payload)
	encrypted.flags = 0x1

	unknownMethod := honest(t, entryName, zip.Store, payload)
	unknownMethod.method = 99

	tests := []struct {
		name string
		zip  []byte
		want []byte
		err  error
	}{
		{"deflated entry", build(t, honest(t, entryName, zip.Deflate, payload)), payload, nil},
		{"stored entry", build(t, honest(t, entryName, zip.Store, payload)), payload, nil},
		{"other entries are ignored", build(t, honest(t, "log.txt", zip.Deflate, []byte("x")), honest(t, entryName, zip.Deflate, payload)), payload, nil},
		{"entry of exactly the cap", build(t, honest(t, entryName, zip.Deflate, exactCap)), exactCap, nil},
		{"empty entry", build(t, honest(t, entryName, zip.Store, nil)), []byte{}, nil},
		{"entry of cap plus one", build(t, honest(t, entryName, zip.Deflate, overCap)), nil, app.ErrEntryTooLarge},
		{"declared size above the cap is refused before inflating", build(t, declaredBig), nil, app.ErrEntryTooLarge},
		{"zip bomb declared small", build(t, bomb), nil, app.ErrEntryTooLarge},
		{"not a zip", []byte("PK but not really a zip file"), nil, app.ErrEntryInvalid},
		{"empty input", nil, nil, app.ErrEntryInvalid},
		{"no entry of that name", build(t, honest(t, "other.json", zip.Deflate, payload)), nil, app.ErrEntryInvalid},
		{"name differs by case", build(t, honest(t, "DevDigest-Result.json", zip.Deflate, payload)), nil, app.ErrEntryInvalid},
		{"nested path is another name", build(t, honest(t, "dir/"+entryName, zip.Deflate, payload)), nil, app.ErrEntryInvalid},
		{"duplicate entries", build(t, honest(t, entryName, zip.Deflate, payload), honest(t, entryName, zip.Store, payload)), nil, app.ErrEntryInvalid},
		{"corrupt deflate stream", build(t, garbage), nil, app.ErrEntryInvalid},
		{"truncated deflate stream", build(t, truncated), nil, app.ErrEntryInvalid},
		{"declared size smaller than the data", build(t, liesSmall), nil, app.ErrEntryInvalid},
		{"crc mismatch", build(t, badCRC), nil, app.ErrEntryInvalid},
		{"encrypted entry", build(t, encrypted), nil, app.ErrEntryInvalid},
		{"unsupported method", build(t, unknownMethod), nil, app.ErrEntryInvalid},
	}
	r := zipentry.New()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Entry(tc.zip, entryName, capBytes)
			if !errors.Is(err, tc.err) {
				t.Fatalf("Entry() error = %v, want %v", err, tc.err)
			}
			if tc.err == nil && !bytes.Equal(got, tc.want) {
				t.Errorf("Entry() = %.40q (%d bytes), want %.40q (%d bytes)", got, len(got), tc.want, len(tc.want))
			}
			if tc.err != nil && got != nil {
				t.Errorf("Entry() returned %d bytes with error %v, want none", len(got), tc.err)
			}
		})
	}
}

func TestEntry_DamagedZIPNeverPanics(t *testing.T) {
	valid := build(t, honest(t, entryName, zip.Deflate, []byte(`{}`)))
	r := zipentry.New()
	for i := range valid {
		cut := valid[:i]
		if _, err := r.Entry(cut, entryName, capBytes); err == nil {
			t.Errorf("Entry(first %d of %d bytes) succeeded, want an error", i, len(valid))
		}
		flipped := bytes.Clone(valid)
		flipped[i] ^= 0xff
		_, _ = r.Entry(flipped, entryName, capBytes) // must not panic; the result is either way fine
	}
}

func TestEntry_ErrorsAreComparable(t *testing.T) {
	r := zipentry.New()
	_, err := r.Entry([]byte(strings.Repeat("x", 100)), entryName, capBytes)
	if !errors.Is(err, app.ErrEntryInvalid) || errors.Is(err, app.ErrEntryTooLarge) {
		t.Errorf("Entry(garbage) error = %v, want only ErrEntryInvalid", err)
	}
}
