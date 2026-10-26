package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// statsQuery is the only GraphQL document this package sends. It is a query: NFR3 forbids a
// mutation, and a test asserts the body of every request holds none.
const statsQuery = `query($ids:[ID!],$iids:[String!]){projects(ids:$ids){nodes{id mergeRequests(iids:$iids){nodes{iid diffStatsSummary{additions deletions fileCount}}}}}}`

// Stats are the diff stats of one merge request (FR26).
type Stats struct {
	Additions  int
	Deletions  int
	FilesCount int
}

// DiffStats reads the diff-stats summary of up to MaxStatsBatch merge requests of one project
// with one read-only GraphQL query: projects(ids: [gid://gitlab/Project/<id>]) (SCR4). The map
// holds an entry per iid that has a summary; a merge request GitLab does not return, or whose
// summary is all zeros (what GitLab returns for a missing one), has no entry. An answer with
// GraphQL errors is an error (EC19). More than MaxStatsBatch iids is an error without a request.
func (c *Client) DiffStats(ctx context.Context, conn Conn, projectID int64, iids []int) (map[int]Stats, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("gitlab: project id %d out of range", projectID)
	}
	iids = slices.Compact(slices.Sorted(slices.Values(iids)))
	if len(iids) > MaxStatsBatch {
		return nil, fmt.Errorf("gitlab: %d merge requests in one stats query, at most %d", len(iids), MaxStatsBatch)
	}
	out := map[int]Stats{}
	if len(iids) == 0 {
		return out, nil
	}
	want := make(map[int]bool, len(iids))
	strs := make([]string, len(iids))
	for i, iid := range iids {
		if iid <= 0 {
			return nil, fmt.Errorf("gitlab: merge request iid %d out of range", iid)
		}
		want[iid] = true
		strs[i] = strconv.Itoa(iid)
	}
	gid := "gid://gitlab/Project/" + strconv.FormatInt(projectID, 10)
	gqlURL, err := conn.graphQLURL()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"query":     statsQuery,
		"variables": map[string]any{"ids": []string{gid}, "iids": strs},
	})
	if err != nil {
		return nil, fmt.Errorf("gitlab graphql_stats: encode body: %w", err)
	}

	type summary struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
		FileCount int `json:"fileCount"`
	}
	type answer struct {
		Data *struct {
			Projects *struct {
				Nodes []struct {
					ID           string `json:"id"`
					MergeRequest *struct {
						Nodes []struct {
							IID     json.Number `json:"iid"`
							Summary *summary    `json:"diffStatsSummary"`
						} `json:"nodes"`
					} `json:"mergeRequests"`
				} `json:"nodes"`
			} `json:"projects"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	ans, err := doJSON[answer](ctx, c, request{
		conn: conn, op: "graphql_stats", method: http.MethodPost, url: gqlURL, body: body,
	})
	if err != nil {
		return nil, err
	}
	if len(ans.Errors) > 0 {
		tok, _ := conn.token()
		msg := scrub(ans.Errors[0].Message, tok)
		if len(msg) > maxMessageBytes {
			msg = strings.ToValidUTF8(msg[:maxMessageBytes], "")
		}
		return nil, fmt.Errorf("gitlab graphql_stats: %w: %s", ErrGraphQL, msg)
	}
	if ans.Data == nil || ans.Data.Projects == nil {
		return nil, fmt.Errorf("gitlab graphql_stats: %w", ErrNotAPI)
	}
	for _, p := range ans.Data.Projects.Nodes {
		if p.ID != gid || p.MergeRequest == nil {
			continue
		}
		for _, n := range p.MergeRequest.Nodes {
			iid, err := strconv.Atoi(n.IID.String())
			if err != nil || !want[iid] || n.Summary == nil {
				continue
			}
			s := Stats{Additions: n.Summary.Additions, Deletions: n.Summary.Deletions, FilesCount: n.Summary.FileCount}
			if s == (Stats{}) {
				continue
			}
			out[iid] = s
		}
	}
	return out, nil
}

// ErrGraphQL marks an answer that carries GraphQL errors (query rejected, GraphQL disabled).
var ErrGraphQL = errors.New("graphql error")

// graphQLURL is <scheme>://<host>[:port][/prefix]/api/graphql, derived from the REST root
// <…>/api/v4 of the same connection, so the token never leaves the connection's origin and
// path prefix.
func (cn Conn) graphQLURL() (string, error) {
	target, _, err := cn.target("", nil)
	if err != nil {
		return "", err
	}
	prefix, ok := strings.CutSuffix(target, "/api/v4")
	if !ok {
		return "", fmt.Errorf("%w: not a REST root", ErrBaseURL)
	}
	if _, err := url.Parse(prefix); err != nil {
		return "", ErrBaseURL
	}
	return prefix + "/api/graphql", nil
}
