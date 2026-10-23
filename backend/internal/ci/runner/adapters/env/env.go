// Package env holds the runner's view of its machine (ci/runner/app ports Env, Files, Out): the
// process environment, the file system the configuration is read from and the artifact is
// written to, and the job log. File errors are worded as the Node runner's were (ENOENT: no such
// file or directory, open '<path>'), so the job-log lines stay the same.
package env

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"sync"
	"syscall"

	"dev-digest-go/backend/internal/ci/runner/app"
)

// Process is the environment of the running process.
type Process struct {
	// LookupEnv is os.LookupEnv; tests inject their own.
	LookupEnv func(string) (string, bool)
	// Dir is the working directory; "" = os.Getwd at the call.
	Dir string
}

var _ app.Env = Process{}

// Lookup returns the value of a variable and whether it is set.
func (p Process) Lookup(key string) (string, bool) {
	if p.LookupEnv != nil {
		return p.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

// Cwd returns the working directory.
func (p Process) Cwd() string {
	if p.Dir != "" {
		return p.Dir
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// Files is the operating system's file system.
type Files struct{}

var _ app.Files = Files{}

// ReadFile reads path; app.ErrFileTooLarge when it is larger than max bytes. Nothing beyond
// max+1 bytes is read.
func (Files) ReadFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nodeError("open", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, nodeError("read", path, err)
	}
	if int64(len(data)) > max {
		return nil, app.ErrFileTooLarge
	}
	return data, nil
}

// ReadDir returns the names of the entries of a directory, sorted.
func (Files) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, nodeError("scandir", path, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sort.Strings(names)
	return names, nil
}

// WriteFile writes data to path, replacing it.
func (Files) WriteFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nodeError("open", path, err)
	}
	return nil
}

// nodeError words a file system error as Node's fs does: "<CODE>: <text>, <op> '<path>'". The
// read of a directory has no path in Node's text.
func nodeError(op, path string, err error) error {
	var code, text string
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code, text = "ENOENT", "no such file or directory"
	case errors.Is(err, fs.ErrPermission):
		code, text = "EACCES", "permission denied"
	case errors.Is(err, syscall.EISDIR):
		return errors.New("EISDIR: illegal operation on a directory, read")
	case errors.Is(err, syscall.ENOTDIR):
		code, text = "ENOTDIR", "not a directory"
	default:
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return fmt.Errorf("%s '%s': %w", op, path, pe.Err)
		}
		return err
	}
	return fmt.Errorf("%s: %s, %s '%s'", code, text, op, path)
}

// Out is the job log: standard output and standard error, one line per call. It is safe for
// concurrent use.
type Out struct {
	mu     sync.Mutex
	stdout io.Writer
	stderr io.Writer
}

var _ app.Out = (*Out)(nil)

// NewOut returns an Out writing to the two streams.
func NewOut(stdout, stderr io.Writer) *Out { return &Out{stdout: stdout, stderr: stderr} }

// Stdout writes a line to standard output.
func (o *Out) Stdout(line string) { o.write(o.stdout, line) }

// Stderr writes a line to standard error.
func (o *Out) Stderr(line string) { o.write(o.stderr, line) }

func (o *Out) write(w io.Writer, line string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = io.WriteString(w, line+"\n")
}
