package gitcli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testCA returns a self-signed CA certificate in PEM form.
func testCA(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeTemp(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// subjects returns the common names of the certificates in a PEM bundle, in order.
func subjects(t *testing.T, bundle []byte) []string {
	t.Helper()
	var out []string
	for {
		var b *pem.Block
		b, bundle = pem.Decode(bundle)
		if b == nil {
			return out
		}
		cert, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			t.Fatalf("bundle holds an unparsable certificate: %v", err)
		}
		out = append(out, cert.Subject.CommonName)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWriteCABundle_SystemThenExtra(t *testing.T) {
	dir := t.TempDir()
	sys := writeTemp(t, dir, "sys.pem", append(testCA(t, "sys-1"), testCA(t, "sys-2")...))
	dest := filepath.Join(dir, "data", "git-ca-bundle.pem")

	if err := writeCABundle(testCA(t, "extra"), dest, []string{filepath.Join(dir, "missing.pem"), sys}); err != nil {
		t.Fatalf("writeCABundle() error: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sys-1", "sys-2", "extra"}; !equalStrings(subjects(t, got), want) {
		t.Errorf("bundle certificates = %v, want %v", subjects(t, got), want)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode = %v, %v, want 0600", fi.Mode().Perm(), err)
	}
}

func TestWriteCABundle_NoSystemBundleIsAnError(t *testing.T) {
	dir := t.TempDir()
	empty := writeTemp(t, dir, "empty.pem", nil)
	junk := writeTemp(t, dir, "junk.pem", []byte("not a certificate"))
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o750); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out", "bundle.pem")

	for name, candidates := range map[string][]string{
		"no candidates": nil,
		"missing":       {filepath.Join(dir, "missing.pem")},
		"empty file":    {empty},
		"not a PEM":     {junk},
		"a directory":   {subdir},
	} {
		t.Run(name, func(t *testing.T) {
			if err := writeCABundle(testCA(t, "extra"), dest, candidates); !errors.Is(err, ErrNoSystemBundle) {
				t.Errorf("writeCABundle() error = %v, want ErrNoSystemBundle", err)
			}
			if _, err := os.Stat(dest); err == nil {
				t.Error("a bundle was written although no system bundle exists")
			}
		})
	}
}

func TestWriteCABundle_FirstUsableCandidateWins(t *testing.T) {
	dir := t.TempDir()
	junk := writeTemp(t, dir, "junk.pem", []byte("nope"))
	first := writeTemp(t, dir, "first.pem", testCA(t, "first"))
	second := writeTemp(t, dir, "second.pem", testCA(t, "second"))
	dest := filepath.Join(dir, "b.pem")
	if err := writeCABundle(testCA(t, "extra"), dest, []string{junk, first, second}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if want := []string{"first", "extra"}; !equalStrings(subjects(t, got), want) {
		t.Errorf("bundle certificates = %v, want %v", subjects(t, got), want)
	}
}

func TestWriteCABundle_ExtraWithoutCertificateIsAnError(t *testing.T) {
	dir := t.TempDir()
	sys := writeTemp(t, dir, "sys.pem", testCA(t, "sys"))
	dest := filepath.Join(dir, "b.pem")
	for name, extra := range map[string][]byte{"nil": nil, "text": []byte("hello"), "key block": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})} {
		t.Run(name, func(t *testing.T) {
			if err := writeCABundle(extra, dest, []string{sys}); err == nil {
				t.Error("writeCABundle() = nil error, want failure")
			}
			if _, err := os.Stat(dest); err == nil {
				t.Error("a bundle was written for an invalid extra CA")
			}
		})
	}
}

func TestWriteCABundle_ReplacesAtomicallyAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	sys := writeTemp(t, dir, "sys.pem", testCA(t, "sys"))
	dest := writeTemp(t, dir, "b.pem", []byte("old content"))
	if err := writeCABundle(testCA(t, "extra"), dest, []string{sys}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if want := []string{"sys", "extra"}; !equalStrings(subjects(t, got), want) {
		t.Errorf("bundle certificates = %v, want %v", subjects(t, got), want)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "sys.pem" && e.Name() != "b.pem" {
			t.Errorf("leftover file %q", e.Name())
		}
	}
}

func TestWriteCABundle_UsesSSLCertFileThenFallsBack(t *testing.T) {
	dir := t.TempDir()
	sys := writeTemp(t, dir, "env.pem", testCA(t, "from-env"))
	dest := filepath.Join(dir, "b.pem")
	t.Setenv("SSL_CERT_FILE", sys)
	if err := WriteCABundle(testCA(t, "extra"), dest); err != nil {
		t.Fatalf("WriteCABundle() error: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if subs := subjects(t, got); len(subs) < 2 || subs[0] != "from-env" || subs[len(subs)-1] != "extra" {
		t.Errorf("bundle certificates = %v, want from-env first and extra last", subs)
	}
}
