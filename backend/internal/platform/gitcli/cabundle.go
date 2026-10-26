package gitcli

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNoSystemBundle means no system CA bundle file was found, so a bundle that adds an extra CA
// to the system store cannot be built (SCR5: the backend must refuse to start).
var ErrNoSystemBundle = errors.New("no system CA bundle found")

// maxBundleBytes caps how much of a CA file is read; real bundles are ~250 KiB.
const maxBundleBytes = 16 << 20

// systemBundlePaths lists where Linux distributions keep the system CA bundle, in the order Go's
// crypto/x509 looks (Debian/Ubuntu, Fedora/RHEL, openSUSE, OpenELEC, CentOS, Alpine).
var systemBundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// WriteCABundle writes dest as the system CA bundle followed by the PEM certificates in extra,
// for git's http.sslCAInfo (git replaces its trust store with that file, so the system roots
// must be inside it). The system bundle is the file sslCertFile (the caller's SSL_CERT_FILE) when
// set and usable, else the first usable file of the usual distribution paths. No system bundle is
// ErrNoSystemBundle; extra without a PEM certificate is an error. dest is replaced atomically
// (temporary file in its directory, then rename) and holds public certificates only.
func WriteCABundle(extra []byte, dest, sslCertFile string) error {
	candidates := systemBundlePaths
	if sslCertFile != "" {
		candidates = append([]string{sslCertFile}, candidates...)
	}
	return writeCABundle(extra, dest, candidates)
}

func writeCABundle(extra []byte, dest string, candidates []string) error {
	if !x509.NewCertPool().AppendCertsFromPEM(extra) {
		return errors.New("extra CA holds no PEM certificate")
	}
	system, err := readSystemBundle(candidates)
	if err != nil {
		return err
	}
	var bundle bytes.Buffer
	bundle.Grow(len(system) + len(extra) + 2)
	bundle.Write(system)
	bundle.WriteByte('\n')
	bundle.Write(extra)
	bundle.WriteByte('\n')

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create CA bundle directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".git-ca-bundle-*")
	if err != nil {
		return fmt.Errorf("create CA bundle: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(bundle.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("write CA bundle: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write CA bundle: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write CA bundle: %w", err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return fmt.Errorf("install CA bundle: %w", err)
	}
	return nil
}

// readSystemBundle returns the first candidate that is a regular file holding a PEM certificate.
func readSystemBundle(candidates []string) ([]byte, error) {
	for _, p := range candidates {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			f.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, maxBundleBytes+1))
		f.Close()
		if err != nil || len(data) > maxBundleBytes || !x509.NewCertPool().AppendCertsFromPEM(data) {
			continue
		}
		return data, nil
	}
	return nil, ErrNoSystemBundle
}
