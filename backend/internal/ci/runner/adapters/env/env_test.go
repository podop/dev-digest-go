package env_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/adapters/env"
	"dev-digest-go/backend/internal/ci/runner/app"
)

func TestFiles_ReadFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	var f env.Files

	got, err := f.ReadFile(filepath.Join(dir, "f"), 4)
	if err != nil || string(got) != "abcd" {
		t.Errorf("ReadFile(max 4) = %q, %v, want abcd", got, err)
	}
	if _, err := f.ReadFile(filepath.Join(dir, "f"), 3); !errors.Is(err, app.ErrFileTooLarge) {
		t.Errorf("ReadFile(max 3) error = %v, want ErrFileTooLarge", err)
	}
}

func TestFiles_ErrorsAreWordedAsNodes(t *testing.T) {
	dir := t.TempDir()
	var f env.Files
	missing := filepath.Join(dir, "nope")

	_, err := f.ReadFile(missing, 10)
	if want := "ENOENT: no such file or directory, open '" + missing + "'"; err == nil || err.Error() != want {
		t.Errorf("ReadFile(missing) error = %v, want %q", err, want)
	}
	_, err = f.ReadDir(missing)
	if want := "ENOENT: no such file or directory, scandir '" + missing + "'"; err == nil || err.Error() != want {
		t.Errorf("ReadDir(missing) error = %v, want %q", err, want)
	}
	_, err = f.ReadFile(dir, 10)
	if want := "EISDIR: illegal operation on a directory, read"; err == nil || err.Error() != want {
		t.Errorf("ReadFile(directory) error = %v, want %q", err, want)
	}
}

func TestFiles_ReadDirIsSorted(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.yaml", "a.yml", "c"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := env.Files{}.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"a.yml", "b.yaml", "c"}, got); diff != "" {
		t.Errorf("ReadDir() mismatch (-want +got):\n%s", diff)
	}
}

func TestFiles_WriteFileReplaces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out.json")
	var f env.Files
	for _, text := range []string{"first and longer", "second"} {
		if err := f.WriteFile(p, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.ReadFile(p); string(got) != "second" {
		t.Errorf("file = %q, want second", got)
	}
	if err := f.WriteFile(filepath.Join(t.TempDir(), "no", "dir", "x"), nil); err == nil || !strings.HasPrefix(err.Error(), "ENOENT: no such file or directory, open '") {
		t.Errorf("WriteFile(missing directory) error = %v, want Node's ENOENT", err)
	}
}

func TestProcess(t *testing.T) {
	vars := map[string]string{"SET": "v", "EMPTY": ""}
	p := env.Process{LookupEnv: func(k string) (string, bool) { v, ok := vars[k]; return v, ok }, Dir: "/work"}
	for key, want := range map[string]struct {
		v  string
		ok bool
	}{"SET": {"v", true}, "EMPTY": {"", true}, "UNSET": {"", false}} {
		if v, ok := p.Lookup(key); v != want.v || ok != want.ok {
			t.Errorf("Lookup(%q) = %q, %v, want %q, %v", key, v, ok, want.v, want.ok)
		}
	}
	if got := p.Cwd(); got != "/work" {
		t.Errorf("Cwd() = %q, want /work", got)
	}
}

func TestOut(t *testing.T) {
	var so, se bytes.Buffer
	o := env.NewOut(&so, &se)
	o.Stdout("a")
	o.Stderr("b")
	o.Stdout("c")
	if so.String() != "a\nc\n" || se.String() != "b\n" {
		t.Errorf("stdout %q, stderr %q; want \"a\\nc\\n\" and \"b\\n\"", so.String(), se.String())
	}
}
