package domain

import (
	"errors"
	"io"
	"testing"
)

type countingReader struct{ n int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func TestInflateCapped_StopsAtCapPlusOne(t *testing.T) {
	src := &countingReader{}
	_, err := inflateCapped(src, ArchiveMaxMarkdownBytes)
	if err == nil {
		t.Fatal("inflateCapped(endless source) error = nil, want a too-large error")
	}
	if src.n > ArchiveInflateCap {
		t.Errorf("inflateCapped read %d bytes from an endless source, want at most %d", src.n, ArchiveInflateCap)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestInflateCapped_ReaderError(t *testing.T) {
	if _, err := inflateCapped(io.MultiReader(failingReader{}), 10); err == nil || err.Error() != "boom" {
		t.Errorf("inflateCapped(failing) error = %v, want boom", err)
	}
}
