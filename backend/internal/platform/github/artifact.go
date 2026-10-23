package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"dev-digest-go/backend/internal/platform/retry"
)

var (
	// ErrArtifactTooLarge is returned by DownloadArtifact when the body is longer than the cap.
	ErrArtifactTooLarge = errors.New("github: artifact is larger than the limit")
	// ErrUnsafeRedirect is returned by DownloadArtifact when the first leg answers a redirect the
	// download refuses to follow: not 302/303/307, no Location, not https, or carrying credentials.
	// It also covers a redirect from the storage leg itself.
	ErrUnsafeRedirect = errors.New("github: artifact download redirect refused")
)

// DownloadArtifact downloads an artifact zip of at most maxBytes. The first leg carries the token
// and follows no redirect on its own; an https redirect target is fetched once without any
// credential. A longer body is an error (ErrArtifactTooLarge); a body is read up to maxBytes+1.
// A refused redirect is ErrUnsafeRedirect; any other non-2xx answer is a *StatusError (410 for an
// expired artifact). Neither the token nor the storage URL appears in an error.
func (c *Client) DownloadArtifact(ctx context.Context, owner, name string, artifactID, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("github: artifact size limit must be positive")
	}
	token, err := c.currentToken()
	if err != nil {
		return nil, err
	}
	path := repoPath(owner, name) + "/actions/artifacts/" + strconv.FormatInt(artifactID, 10) + "/zip"
	first, err := retry.Value(ctx, c.retry, func(ctx context.Context) (artifactLeg, error) {
		return c.artifactRequest(ctx, c.strict, c.baseURL+path, token, maxBytes)
	})
	if err != nil {
		return nil, fmt.Errorf("github GET %s: %w", path, scrub(err, token))
	}
	if first.next == nil {
		return first.body, nil
	}
	second, err := retry.Value(ctx, c.retry, func(ctx context.Context) (artifactLeg, error) {
		return c.artifactRequest(ctx, c.storage, first.next.String(), "", maxBytes)
	})
	if err != nil {
		return nil, fmt.Errorf("github GET artifact storage: %w", scrub(err, token))
	}
	if second.next != nil {
		return nil, ErrUnsafeRedirect
	}
	return second.body, nil
}

// artifactLeg is one answered request of a download: a body, or the redirect target to follow.
type artifactLeg struct {
	body []byte
	next *url.URL
}

// artifactRequest performs one attempt bounded by c.timeout. An empty token sends no
// Authorization header (the storage leg). A 302/303/307 answer yields the vetted https target
// instead of a body.
func (c *Client) artifactRequest(ctx context.Context, hc *http.Client, target, token string, maxBytes int64) (artifactLeg, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return artifactLeg{}, stripURL(err)
	}
	req.Header.Set("User-Agent", "dev-digest-go")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return artifactLeg{}, stripURL(err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		if err != nil {
			return artifactLeg{}, stripURL(err)
		}
		if int64(len(body)) > maxBytes {
			return artifactLeg{}, ErrArtifactTooLarge
		}
		return artifactLeg{body: body}, nil
	case resp.StatusCode >= 300 && resp.StatusCode <= 399:
		next, err := redirectTarget(resp)
		if err != nil {
			return artifactLeg{}, err
		}
		return artifactLeg{next: next}, nil
	default:
		return artifactLeg{}, statusError(resp, token)
	}
}

// redirectTarget vets the Location of a redirect answer: only 302, 303 and 307 are followed, the
// target must be an absolute https URL with a host and without credentials in it.
func redirectTarget(resp *http.Response) (*url.URL, error) {
	switch resp.StatusCode {
	case http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
	default:
		return nil, ErrUnsafeRedirect
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil, ErrUnsafeRedirect
	}
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, ErrUnsafeRedirect
	}
	return u, nil
}

// stripURL drops the request URL from a transport error: the storage URL is signed, so it is a
// credential of its own. The wrapped cause (timeout, reset, TLS failure) stays for classification.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
