package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// apiContent is the object GitHub answers for a file; a directory is a JSON array instead.
type apiContent struct {
	Type     string  `json:"type"`
	Size     int64   `json:"size"`
	Encoding string  `json:"encoding"`
	Content  *string `json:"content"`
}

// GetContent reads the file at path in the repository at ref (a commit SHA, branch or tag)
// through the contents API and returns its decoded bytes with found true. It reports found false
// (and no error) for a 404, a directory or other non-file object, a payload without content, and
// a file whose reported size is above max (when max > 0), the last without decoding the payload.
// Any other failure, an unconfigured token included, is an error. The request never follows a
// redirect. The bytes are not UTF-8 decoded: the caller decodes them as it does git output.
func (c *Client) GetContent(ctx context.Context, owner, name, path, ref string, max int64) ([]byte, bool, error) {
	token, err := c.currentToken()
	if err != nil {
		return nil, false, err
	}
	strict := c.noRedirect()
	q := url.Values{"ref": {ref}}
	raw, err := getJSON[json.RawMessage](ctx, strict, token, repoPath(owner, name)+"/contents/"+escapePath(path), q)
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Status == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '{' {
		return nil, false, nil
	}
	var obj apiContent
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, nil
	}
	if obj.Type != "file" || obj.Content == nil {
		return nil, false, nil
	}
	if max > 0 && obj.Size > max {
		return nil, false, nil
	}
	if obj.Encoding == "base64" {
		return decodeBase64(*obj.Content), true, nil
	}
	return []byte(*obj.Content), true, nil
}

// escapePath escapes every segment of a repository path and keeps the slashes.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// decodeBase64 decodes like Node's Buffer.from(s, "base64"): characters outside the alphabet
// (the line breaks GitHub inserts) are skipped, the URL-safe alphabet is accepted, decoding stops
// at the first "=", and an incomplete trailing group is decoded as far as it goes.
func decodeBase64(s string) []byte {
	clean := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch == '=':
			i = len(s)
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '+', ch == '/':
			clean = append(clean, ch)
		case ch == '-':
			clean = append(clean, '+')
		case ch == '_':
			clean = append(clean, '/')
		}
	}
	if len(clean)%4 == 1 { // one leftover sextet carries no full byte
		clean = clean[:len(clean)-1]
	}
	out := make([]byte, base64.RawStdEncoding.DecodedLen(len(clean)))
	n, _ := base64.RawStdEncoding.Decode(out, clean)
	return out[:n]
}
