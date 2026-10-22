package peers

import (
	"context"
	"unicode/utf8"

	"dev-digest-go/backend/internal/onboarding/app"
	repointel "dev-digest-go/backend/internal/repointel/domain"
)

// BlobReader is the part of *platform/gitcli.Client the adapter uses.
type BlobReader interface {
	// BlobAt returns the file at a commit of an existing clone, without a checkout; a missing
	// file, an oversize file (over limit bytes), an unsafe path or sha, and a failing git are errors.
	BlobAt(ctx context.Context, owner, name, commit, path string, limit int64) ([]byte, error)
}

// Blobs implements onboarding/app.Blobs over the clone's git objects.
type Blobs struct{ g BlobReader }

var _ app.Blobs = (*Blobs)(nil)

// NewBlobs wraps the git client.
func NewBlobs(g BlobReader) *Blobs { return &Blobs{g: g} }

// Read returns the file's text, decoded as UTF-8 with the WHATWG replacement (the clone is
// untrusted bytes; Node reads it as a UTF-8 string), as bytes. Every failure of the read means
// the file is absent from the prompt: a repository without a compose file is normal, and an
// oversize file is left out, not cut.
func (b *Blobs) Read(ctx context.Context, owner, name, sha, path string, limit int64) ([]byte, bool) {
	data, err := b.g.BlobAt(ctx, owner, name, sha, path, limit)
	if err != nil {
		return nil, false
	}
	if utf8.Valid(data) {
		// DecodeText is the identity on valid UTF-8 (BOM and NUL kept): skip its copy, a blob can
		// be half a megabyte and 16 are in flight (NFR5006).
		return data, true
	}
	return []byte(repointel.DecodeText(data)), true
}
