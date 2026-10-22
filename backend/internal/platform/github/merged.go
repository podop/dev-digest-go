package github

import (
	"context"
	"net/url"
	"strconv"
	"sync"
)

// mergedFilesConcurrency is how many per-PR file reads run at once (GitHub's secondary rate limit).
const mergedFilesConcurrency = 5

// MergedPull is a merged pull request with the paths its first page of files touched.
type MergedPull struct {
	Number int
	Title  string
	// Author is the user's login, "unknown" when GitHub has no user.
	Author string
	// MergedAt is the timestamp exactly as GitHub sent it.
	MergedAt string
	// Files are the distinct filenames, first-seen order (a file/symlink type change is listed
	// twice by GitHub).
	Files []string
}

type apiMergedPull struct {
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	User     *apiUser `json:"user"`
	MergedAt *string  `json:"merged_at"`
}

// ListMergedPulls lists the repository's most recently updated closed pull requests (one page of
// min(max(limit, 1), 100)), keeps the merged ones and reads the first page of files of each, at
// most five requests at a time. Each request has the client's timeout and retry policy; any
// failure fails the whole read. Requests never follow a redirect. Nothing is cached.
func (c *Client) ListMergedPulls(ctx context.Context, owner, name string, limit int) ([]MergedPull, error) {
	token, err := c.currentToken()
	if err != nil {
		return nil, err
	}
	strict := c.noRedirect()
	base := repoPath(owner, name)
	q := url.Values{
		"state":     {"closed"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {strconv.Itoa(min(max(limit, 1), PageSize))},
	}
	pulls, err := getJSON[[]apiMergedPull](ctx, strict, token, base+"/pulls", q)
	if err != nil {
		return nil, err
	}
	merged := make([]apiMergedPull, 0, len(pulls))
	for _, p := range pulls {
		if p.MergedAt != nil && *p.MergedAt != "" {
			merged = append(merged, p)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		out      = make([]MergedPull, len(merged))
		sem      = make(chan struct{}, mergedFilesConcurrency)
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		panicked any
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		mu.Unlock()
	}
	for i, p := range merged {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				// A panic in a worker would kill the process: hand it to the caller.
				if r := recover(); r != nil {
					mu.Lock()
					if panicked == nil {
						panicked = r
					}
					mu.Unlock()
					cancel()
				}
			}()
			files, err := getJSON[[]struct {
				Filename string `json:"filename"`
			}](ctx, strict, token, base+"/pulls/"+strconv.Itoa(p.Number)+"/files",
				url.Values{"per_page": {strconv.Itoa(PageSize)}})
			if err != nil {
				fail(err)
				return
			}
			seen := make(map[string]struct{}, len(files))
			names := make([]string, 0, len(files))
			for _, f := range files {
				if _, dup := seen[f.Filename]; !dup {
					seen[f.Filename] = struct{}{}
					names = append(names, f.Filename)
				}
			}
			out[i] = MergedPull{Number: p.Number, Title: p.Title, Author: login(p.User), MergedAt: *p.MergedAt, Files: names}
		}()
	}
	wg.Wait()
	if panicked != nil {
		panic(panicked)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
