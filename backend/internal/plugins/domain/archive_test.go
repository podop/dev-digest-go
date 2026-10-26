package domain_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/plugins/domain"
)

const zipMedia = "application/zip"

func manifestEntry(name string) entry {
	return entry{name: name, data: []byte(minimalJSON)}
}

func wantInvalid(t *testing.T, err error, contains string) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	var ie *domain.InvalidError
	if !errors.As(err, &ie) {
		t.Fatalf("error %T is not *InvalidError", err)
	}
	if !strings.Contains(ie.Message(), contains) {
		t.Errorf("message = %q, want it to contain %q", ie.Message(), contains)
	}
}

func TestParseUpload_Archive(t *testing.T) {
	tests := []struct {
		name        string
		entries     []entry
		wantName    string
		wantIgnored []domain.Ignored
	}{
		{name: "manifest at the root, deflated", entries: []entry{manifestEntry(domain.BundleFile)}, wantName: "acme", wantIgnored: []domain.Ignored{}},
		{name: "manifest at the root, stored", entries: []entry{{name: domain.BundleFile, data: []byte(minimalJSON), store: true}}, wantName: "acme", wantIgnored: []domain.Ignored{}},
		{name: "inside the single top folder", entries: []entry{manifestEntry("plugin/" + domain.BundleFile), {name: "plugin/README.md", data: []byte("hi")}},
			wantName: "acme", wantIgnored: []domain.Ignored{{PathOrSection: "plugin/README.md", Reason: domain.ReasonNotPartOfPlugin}}},
		{name: "directory entries are skipped silently", entries: []entry{{name: "plugin/", store: true}, manifestEntry("plugin/" + domain.BundleFile)}, wantName: "acme", wantIgnored: []domain.Ignored{}},
		{name: "other entries are listed in archive order", entries: []entry{{name: "z.txt", data: []byte("1")}, manifestEntry(domain.BundleFile), {name: "a/b.sh", data: []byte("2")}},
			wantName: "acme", wantIgnored: []domain.Ignored{{"z.txt", domain.ReasonNotPartOfPlugin}, {"a/b.sh", domain.ReasonNotPartOfPlugin}}},
		{name: "macOS metadata beside a top folder", entries: []entry{manifestEntry("p/" + domain.BundleFile), {name: "__MACOSX/p/._x", data: []byte("1")}, {name: "p/.DS_Store", data: []byte("1")}},
			wantName: "acme", wantIgnored: []domain.Ignored{{"__MACOSX/p/._x", domain.ReasonOSMetadata}, {"p/.DS_Store", domain.ReasonOSMetadata}}},
		{name: "BOM before the JSON is cut", entries: []entry{{name: domain.BundleFile, data: append([]byte{0xEF, 0xBB, 0xBF}, minimalJSON...)}}, wantName: "acme", wantIgnored: []domain.Ignored{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up, err := domain.ParseUpload(zipMedia, buildZip(t, tc.entries...))
			if err != nil {
				t.Fatalf("ParseUpload() error = %v", err)
			}
			if up.Bundle.Manifest.Name != tc.wantName {
				t.Errorf("name = %q, want %q", up.Bundle.Manifest.Name, tc.wantName)
			}
			if diff := cmp.Diff(tc.wantIgnored, up.Ignored); diff != "" {
				t.Errorf("ignored mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseUpload_MediaTypes(t *testing.T) {
	tests := []struct {
		media   string
		body    []byte
		wantErr error
	}{
		{"application/json", []byte(minimalJSON), nil},
		{"application/json; charset=utf-8", []byte(minimalJSON), nil},
		{"APPLICATION/JSON", []byte(minimalJSON), nil},
		{"application/zip", buildZip(t, manifestEntry(domain.BundleFile)), nil},
		{"text/plain", []byte(minimalJSON), domain.ErrUnsupportedMedia},
		{"multipart/form-data; boundary=x", []byte(minimalJSON), domain.ErrUnsupportedMedia},
		{"application/x-zip-compressed", []byte("x"), domain.ErrUnsupportedMedia},
		{"", []byte(minimalJSON), domain.ErrUnsupportedMedia},
		{"not a media type;;", []byte(minimalJSON), domain.ErrUnsupportedMedia},
	}
	for _, tc := range tests {
		t.Run(tc.media, func(t *testing.T) {
			_, err := domain.ParseUpload(tc.media, tc.body)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ParseUpload(%q) error = %v, want %v", tc.media, err, tc.wantErr)
			}
		})
	}
}

func TestParseUpload_HostileArchives(t *testing.T) {
	good := manifestEntry(domain.BundleFile)
	tests := []struct {
		name string
		zip  func(t *testing.T) []byte
		want string
	}{
		{"parent traversal", func(t *testing.T) []byte { return buildZip(t, good, entry{name: "../x", data: []byte("1")}) }, "unsafe path"},
		{"traversal in the middle", func(t *testing.T) []byte { return buildZip(t, good, entry{name: "a/../../x", data: []byte("1")}) }, "unsafe path"},
		{"absolute path", func(t *testing.T) []byte { return buildZip(t, good, entry{name: "/etc/passwd", data: []byte("1")}) }, "unsafe path"},
		{"backslash", func(t *testing.T) []byte { return buildZip(t, good, entry{name: `a\b`, data: []byte("1")}) }, "unsafe path"},
		{"backslash traversal", func(t *testing.T) []byte { return buildZip(t, good, entry{name: `..\x`, data: []byte("1")}) }, "unsafe path"},
		{"drive letter", func(t *testing.T) []byte { return buildZip(t, good, entry{name: "C:/x", data: []byte("1")}) }, "unsafe path"},
		{"drive letter without slash", func(t *testing.T) []byte { return buildZip(t, good, entry{name: "d:x", data: []byte("1")}) }, "unsafe path"},
		{"control character in a name", func(t *testing.T) []byte { return buildZip(t, good, entry{name: "a\x00b", data: []byte("1")}) }, "unsafe path"},
		{"unsafe directory entry", func(t *testing.T) []byte { return buildZip(t, good, entry{name: "../d/", store: true}) }, "unsafe path"},
		{"unsafe name even with the manifest nested", func(t *testing.T) []byte {
			return buildZip(t, entry{name: "p/" + domain.BundleFile, data: []byte(minimalJSON)}, entry{name: "p/../../x", data: []byte("1")})
		}, "unsafe path"},
		{"encrypted entry", func(t *testing.T) []byte {
			return buildZip(t, good, entry{name: "secret.bin", data: []byte("xxxx"), other: zip.Store, flags: 0x1})
		}, "encrypted"},
		{"encrypted manifest", func(t *testing.T) []byte {
			return buildZip(t, entry{name: domain.BundleFile, data: []byte(minimalJSON), other: zip.Store, flags: 0x1})
		}, "encrypted"},
		{"bzip2 entry", func(t *testing.T) []byte {
			return buildZip(t, good, entry{name: "x.bz2", data: []byte("xxxx"), other: 12})
		}, "compression method 12"},
		{"two manifests, root and nested", func(t *testing.T) []byte {
			return buildZip(t, good, entry{name: "p/" + domain.BundleFile, data: []byte(minimalJSON)})
		}, "2 devdigest-plugin.json"},
		{"two manifests with one name", func(t *testing.T) []byte { return buildZip(t, good, good) }, "2 devdigest-plugin.json"},
		{"two manifests in two folders", func(t *testing.T) []byte {
			return buildZip(t, entry{name: "a/" + domain.BundleFile, data: []byte(minimalJSON)}, entry{name: "b/" + domain.BundleFile, data: []byte(minimalJSON)})
		}, "2 devdigest-plugin.json"},
		{"no manifest", func(t *testing.T) []byte { return buildZip(t, entry{name: "readme.md", data: []byte("x")}) }, "no devdigest-plugin.json"},
		{"manifest named in another case", func(t *testing.T) []byte {
			return buildZip(t, entry{name: "DevDigest-Plugin.json", data: []byte(minimalJSON)})
		}, "no devdigest-plugin.json"},
		{"empty archive", func(t *testing.T) []byte { return buildZip(t) }, "no devdigest-plugin.json"},
		{"manifest two folders deep", func(t *testing.T) []byte {
			return buildZip(t, entry{name: "a/b/" + domain.BundleFile, data: []byte(minimalJSON)})
		}, "single top folder"},
		{"manifest in a folder, other entry outside it", func(t *testing.T) []byte {
			return buildZip(t, entry{name: "p/" + domain.BundleFile, data: []byte(minimalJSON)}, entry{name: "README.md", data: []byte("x")})
		}, "single top folder"},
		{"not a zip", func(t *testing.T) []byte { return []byte("this is not a zip file at all") }, "not a valid .zip"},
		{"empty body", func(t *testing.T) []byte { return nil }, "not a valid .zip"},
		{"invalid JSON", func(t *testing.T) []byte {
			return buildZip(t, entry{name: domain.BundleFile, data: []byte(`{"manifest":`)})
		}, "not valid JSON"},
		{"bad UTF-8", func(t *testing.T) []byte {
			return buildZip(t, entry{name: domain.BundleFile, data: []byte("{\"manifest\":{\"name\":\"a\xff\"}}")})
		}, "not valid UTF-8"},
		{"checksum mismatch", func(t *testing.T) []byte {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			payload := deflateBytes(t, []byte(minimalJSON))
			w, err := zw.CreateRaw(&zip.FileHeader{Name: domain.BundleFile, Method: zip.Deflate, CRC32: crc32.ChecksumIEEE([]byte("other")), CompressedSize64: uint64(len(payload)), UncompressedSize64: uint64(len(minimalJSON))})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(payload)
			_ = zw.Close()
			return buf.Bytes()
		}, "checksum"},
		{"corrupt deflate stream", func(t *testing.T) []byte {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			garbage := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
			w, err := zw.CreateRaw(&zip.FileHeader{Name: domain.BundleFile, Method: zip.Deflate, CRC32: 1, CompressedSize64: uint64(len(garbage)), UncompressedSize64: 10})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(garbage)
			_ = zw.Close()
			return buf.Bytes()
		}, "corrupt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ParseUpload(zipMedia, tc.zip(t))
			wantInvalid(t, err, tc.want)
		})
	}
}

func TestParseUpload_EntryCountBoundary(t *testing.T) {
	build := func(n int) []byte {
		entries := []entry{manifestEntry(domain.BundleFile)}
		for i := 1; i < n; i++ {
			entries = append(entries, entry{name: fmt.Sprintf("f/%d.txt", i), data: nil, store: true})
		}
		return buildZip(t, entries...)
	}
	tests := []struct {
		name    string
		entries int
		wantErr bool
	}{
		{"500 entries are accepted", 500, false},
		{"501 entries are refused", 501, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// f/ entries sit beside the root manifest, which is allowed.
			up, err := domain.ParseUpload(zipMedia, build(tc.entries))
			if tc.wantErr {
				wantInvalid(t, err, "501 entries; the limit is 500")
				return
			}
			if err != nil {
				t.Fatalf("ParseUpload() error = %v", err)
			}
			// 499 other entries: the list keeps MaxIgnored and sums the rest.
			if got := len(up.Ignored); got != domain.MaxIgnored+1 {
				t.Fatalf("len(Ignored) = %d, want %d", got, domain.MaxIgnored+1)
			}
			if last := up.Ignored[domain.MaxIgnored]; last.Reason != "299 more ignored parts" {
				t.Errorf("last ignored = %+v, want the sum of the remaining 299", last)
			}
		})
	}
}

// paddedBundle returns bundle JSON of exactly n bytes (an ignored unknown key pads it).
func paddedBundle(t testing.TB, n int) []byte {
	t.Helper()
	const head = `{"manifest":{"name":"acme","format":"devdigest-plugin/v1"},"pad":"`
	const tail = `"}`
	fill := n - len(head) - len(tail)
	if fill < 0 {
		t.Fatalf("padded bundle of %d bytes is too small", n)
	}
	return []byte(head + strings.Repeat("x", fill) + tail)
}

func TestParseUpload_InflatedSizeBoundary(t *testing.T) {
	const limit = 20 << 20
	tests := []struct {
		name    string
		size    int
		store   bool
		wantErr bool
	}{
		{"exactly 20 MiB, deflated", limit, false, false},
		{"20 MiB + 1, stored", limit + 1, true, true},
		{"20 MiB + 1, deflated", limit + 1, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			z := buildZip(t, entry{name: domain.BundleFile, data: paddedBundle(t, tc.size), store: tc.store})
			up, err := domain.ParseUpload(zipMedia, z)
			if tc.wantErr {
				wantInvalid(t, err, "inflates past 20 MiB")
				return
			}
			if err != nil {
				t.Fatalf("ParseUpload() error = %v", err)
			}
			if got := up.Bundle.Manifest.Name; got != "acme" {
				t.Errorf("name = %q, want acme", got)
			}
		})
	}
}

func TestParseUpload_JSONBodyBoundary(t *testing.T) {
	const limit = 20 << 20
	if _, err := domain.ParseUpload("application/json", paddedBundle(t, limit)); err != nil {
		t.Errorf("a %d byte JSON body: error = %v, want nil", limit, err)
	}
	_, err := domain.ParseUpload("application/json", paddedBundle(t, limit+1))
	wantInvalid(t, err, "larger than 20 MiB")
}

// lyingBomb builds an archive whose directory declares 10 bytes while the Deflate stream inflates
// to inflated bytes of zeros: the declared size must never be trusted.
func lyingBomb(t testing.TB, inflated int) []byte {
	t.Helper()
	zeros := make([]byte, 1<<20)
	var buf bytes.Buffer
	{
		fw := newDeflater(t, &buf)
		for written := 0; written < inflated; written += len(zeros) {
			if _, err := fw.Write(zeros); err != nil {
				t.Fatalf("deflate: %v", err)
			}
		}
		if err := fw.Close(); err != nil {
			t.Fatalf("deflate close: %v", err)
		}
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: domain.BundleFile, Method: zip.Deflate, CRC32: 0, CompressedSize64: uint64(buf.Len()), UncompressedSize64: 10})
	if err != nil {
		t.Fatalf("CreateRaw: %v", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out.Bytes()
}

// TestParseUpload_BombStopsAtTheCap is spec AC9 (a) / EC1 / NFR1: a 64 MiB Deflate bomb that
// declares 10 bytes is refused, and the process allocates far less than the 64 MiB (A6).
func TestParseUpload_BombStopsAtTheCap(t *testing.T) {
	bomb := lyingBomb(t, 64<<20)
	if len(bomb) > 1<<20 {
		t.Fatalf("test bomb is %d bytes, want a small archive", len(bomb))
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := domain.ParseUpload(zipMedia, bomb)
	runtime.ReadMemStats(&after)
	wantInvalid(t, err, "inflates past 20 MiB")
	t.Logf("TotalAlloc grew by %d KiB", (after.TotalAlloc-before.TotalAlloc)>>10)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
		t.Errorf("ParseUpload(bomb) allocated %d MiB, want < 64 MiB", grown>>20)
	}
}

func TestParseUpload_HonestBombIsRefusedBeforeReading(t *testing.T) {
	z := buildZip(t, entry{name: domain.BundleFile, data: make([]byte, 21<<20)})
	_, err := domain.ParseUpload(zipMedia, z)
	wantInvalid(t, err, "inflates past 20 MiB")
}
