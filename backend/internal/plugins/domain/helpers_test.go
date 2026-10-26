package domain_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/json"
	"hash/crc32"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/plugins/domain"
)

// entry is one archive entry to build.
type entry struct {
	name  string
	data  []byte
	store bool   // Store instead of Deflate
	other uint16 // a compression method other than Store/Deflate; the data is written as is
	flags uint16
}

func deflateBytes(t testing.TB, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		t.Fatalf("flate.NewWriter: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("flate write: %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("flate close: %v", err)
	}
	return buf.Bytes()
}

// buildZip writes the entries as given; CreateRaw keeps names, flags and methods untouched.
func buildZip(t testing.TB, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		method, payload := uint16(zip.Deflate), e.data
		switch {
		case e.other != 0:
			method = e.other
		case e.store:
			method = zip.Store
		default:
			payload = deflateBytes(t, e.data)
		}
		w, err := zw.CreateRaw(&zip.FileHeader{
			Name: e.name, Method: method, Flags: e.flags,
			CRC32: crc32.ChecksumIEEE(e.data), CompressedSize64: uint64(len(payload)), UncompressedSize64: uint64(len(e.data)),
		})
		if err != nil {
			t.Fatalf("CreateRaw(%q): %v", e.name, err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("write %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func ptr[T any](v T) *T { return &v }

// minimalJSON is the smallest valid bundle text.
const minimalJSON = `{"manifest":{"name":"acme","version":"1.0.0","format":"devdigest-plugin/v1"},"agents":[],"skills":[],"eval_cases":[],"conventions":[]}`

// fullJSON is a bundle with every section a plugin installs.
const fullJSON = `{
 "manifest":{"name":"  acme  ","version":"1.2.3","format":"devdigest-plugin/v1","kind":"plugin","exported_at":"2026-10-25T10:00:00.000Z","description":"d","counts":{"agents":9}},
 "agents":[
  {"name":"Reviewer","description":"x","provider":"openrouter","model":"m","system_prompt":"be strict","output_schema":{"type":"object"},"enabled":false,"skills":["style","sec"],"strategy":"auto","ci_fail_on":"any","repo_intel":false},
  {"name":"Minimal","provider":"openai","model":"gpt","system_prompt":"p"}
 ],
 "skills":[
  {"name":"style","description":"s","type":"rubric","source":"manual","body":"b1","enabled":true,"evidence_files":["a.go"]},
  {"name":"sec","body":"b2"}
 ],
 "eval_cases":[
  {"name":"c1","owner_kind":"agent","owner_ref":"Reviewer","input_diff":"diff","input_files":[{"p":1}],"input_meta":null,"expected_output":{"findings":[]},"notes":"n"},
  {"name":"c2","owner_kind":"skill","owner_ref":"style"}
 ],
 "conventions":[],
 "learnings":[{"scope":"repo","kind":"decision","content":"use pgx","confidence":0.5,"repo":"acme/api"},{"content":"tabs"}]
}`

// parseOK parses a JSON body and fails the test on error.
func parseOK(t testing.TB, text string) domain.Upload {
	t.Helper()
	up, err := domain.ParseUpload("application/json", []byte(text))
	if err != nil {
		t.Fatalf("ParseUpload(json) error = %v", err)
	}
	return up
}

// mutate parses fullJSON, applies fn to the decoded document and returns the new JSON text.
func mutate(t testing.TB, base string, fn func(doc map[string]any)) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(base), &doc); err != nil {
		t.Fatalf("decode base: %v", err)
	}
	fn(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(out)
}

func readAll(t testing.TB, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return b
}

func newDeflater(t testing.TB, w io.Writer) *flate.Writer {
	t.Helper()
	fw, err := flate.NewWriter(w, flate.BestSpeed)
	if err != nil {
		t.Fatalf("flate.NewWriter: %v", err)
	}
	return fw
}

// cmpEmpty treats a nil and an empty string slice alike.
var cmpEmpty = cmpopts.EquateEmpty()
