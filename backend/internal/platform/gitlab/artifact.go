package gitlab

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/platform/retry"
)

// artifactLeg is one answered request of an artifact download: the body, or the vetted https
// target of a redirect.
type artifactLeg struct {
	body []byte
	next *url.URL
}

// JobArtifactFile downloads one file of a job's artifacts: GET /projects/:id/jobs/:job_id/
// artifacts/<file> (spec C FR34). At most limit bytes are accepted: a longer body is ErrTooLarge
// and none of it is returned (read with a cap of limit+1). A 404 is a *StatusError (the archive has
// no such file, or it expired).
//
// The first leg carries the token (PRIVATE-TOKEN, to the connection's own origin and prefix) and
// follows no redirect. A 302, 303 or 307 answer with an absolute https Location (host present, no
// credentials) is fetched exactly once more with storage, a client that never receives the token or
// any other credential; its own redirect is refused. Any other redirect, a missing or non-https
// Location, is ErrRedirect. storage is the caller's egress client for the object storage (no
// proxy, dial guard, extra CA); nil means the Client's own HTTP client, which is that kind of
// client too. Whatever is passed is copied without redirect-following or cookie jar. The token
// and the (signed) storage URL appear in no error and no log line.
func (c *Client) JobArtifactFile(ctx context.Context, conn Conn, storage *http.Client, projectID, jobID int64, file string, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("gitlab job_artifact: limit %d out of range", limit)
	}
	base, err := projectPath(projectID)
	if err != nil {
		return nil, err
	}
	if jobID <= 0 {
		return nil, fmt.Errorf("gitlab: job id %d out of range", jobID)
	}
	if !validProjectPath(file) {
		return nil, fmt.Errorf("gitlab job_artifact: invalid artifact path")
	}
	tok, err := conn.token()
	if err != nil {
		return nil, err
	}
	segs := strings.Split(file, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	path := base + "/jobs/" + strconv.FormatInt(jobID, 10) + "/artifacts/" + strings.Join(segs, "/")
	target, host, err := conn.target(path, nil)
	if err != nil {
		return nil, err
	}

	const op = "job_artifact"
	attempt := 0
	first, err := retry.Value(ctx, c.policy(), func(ctx context.Context) (artifactLeg, error) {
		attempt++
		return c.artifactLeg(ctx, c.http, op, target, host, tok, attempt, limit)
	})
	if err != nil {
		return nil, err
	}
	if first.next == nil {
		return first.body, nil
	}

	hc := c.http
	if storage != nil {
		hc = storage
	}
	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cp.Jar = nil
	attempt = 0
	second, err := retry.Value(ctx, c.policy(), func(ctx context.Context) (artifactLeg, error) {
		attempt++
		leg, err := c.artifactLeg(ctx, &cp, "job_artifact_storage", first.next.String(), first.next.Host, "", attempt, limit)
		if se, ok := asStatus(err); ok {
			// The storage's own text is not ours to relay; the status is what matters.
			se.Message = http.StatusText(se.Status)
		}
		return leg, err
	})
	if err != nil {
		return nil, err
	}
	if second.next != nil {
		return nil, fmt.Errorf("gitlab %s_storage: %w", op, ErrRedirect)
	}
	return second.body, nil
}

// artifactLeg performs one attempt: a 2xx answer yields its body (at most limit bytes), a 3xx one
// the vetted redirect target.
func (c *Client) artifactLeg(ctx context.Context, hc *http.Client, op, target, host, token string, attempt, limit int) (artifactLeg, error) {
	var leg artifactLeg
	err := c.exchange(ctx, hc, op, http.MethodGet, target, host, token, attempt, func(resp *http.Response) error {
		if resp.StatusCode >= 300 {
			next, err := artifactRedirect(resp)
			if err != nil {
				return fmt.Errorf("gitlab %s: %w", op, err)
			}
			leg.next = next
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
		if err != nil {
			return transportError(op, err, token)
		}
		if len(body) > limit {
			return fmt.Errorf("gitlab %s: %w", op, ErrTooLarge)
		}
		leg.body = body
		return nil
	})
	return leg, err
}

// artifactRedirect vets a redirect answer: only 302, 303 and 307 are followed, to an absolute
// https URL with a host and no credentials.
func artifactRedirect(resp *http.Response) (*url.URL, error) {
	switch resp.StatusCode {
	case http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
	default:
		return nil, ErrRedirect
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, ErrRedirect
	}
	return u, nil
}
