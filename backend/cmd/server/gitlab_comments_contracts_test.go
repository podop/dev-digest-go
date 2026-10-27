package main

import (
	"bytes"
	"net/http"
	"testing"
)

// TestContracts_GitLabComments records the answers of the GitLab merge request comment routes that the
// UI contract test validates (GitLab B, plan S24, spec AC31): the read, a new comment, a reply and
// the failure envelopes. The stub's address is replaced by a fixed host, so no port reaches a golden.
func TestContracts_GitLabComments(t *testing.T) {
	e := gbStart(t, gbOpts{})
	g := sbGoldens(t, e.s)

	const fixedURL, fixedHost = "https://gitlab.example.com", "gitlab.example.com"
	fixed := func(r response) response {
		r.Body = bytes.ReplaceAll(bytes.ReplaceAll(r.Body, []byte(e.lab.URL), []byte(fixedURL)), []byte(e.lab.host), []byte(fixedHost))
		return r
	}

	// Two threads: a replied one on a RIGHT line, an outdated one (older head) with a reply.
	e.lab.set(func(m *gbMR) {
		m.discussions = []map[string]any{
			gbDiffThread(1, 101, "a.ts", nil, gbInt(5), gbHeadSHA, "ann",
				gbNote(102, "DiscussionNote", "Good catch.", "bob", nil, "2026-10-20T11:00:00Z")),
			gbDiffThread(2, 201, "a.ts", gbInt(7), nil, gbOldSHA, "cat"),
		}
	})
	g.check("gitlab_comments_list", fixed(e.comments()), http.StatusOK)
	g.check("gitlab_comments_create", fixed(e.post(gbAt("a.ts", 12, "A new comment."))), http.StatusOK)
	g.check("gitlab_comments_reply",
		fixed(e.post(map[string]any{"path": "a.ts", "line": 5, "body": "Thanks.", "in_reply_to": 101})), http.StatusOK)

	fail := func(status int, header map[string]string, body string) {
		e.lab.setHook(func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
			if r.Method != http.MethodPost {
				return false
			}
			for k, v := range header {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return true
		})
	}
	fail(http.StatusUnprocessableEntity, nil, `{"message":"line_code can't be blank"}`)
	g.check("gitlab_comments_failed", fixed(e.post(gbAt("a.ts", 12, "x"))), http.StatusBadRequest)
	fail(http.StatusForbidden, nil, `{"message":"403 Forbidden"}`)
	g.check("gitlab_comments_denied", fixed(e.post(gbAt("a.ts", 12, "x"))), http.StatusBadRequest)
	fail(http.StatusTooManyRequests, map[string]string{"Retry-After": "37"}, `{"message":"Retry later"}`)
	g.check("gitlab_comments_rate_limited", fixed(e.post(gbAt("a.ts", 12, "x"))), http.StatusBadRequest)

	// A repository whose host has no connection.
	n := gbStart(t, gbOpts{noConnection: true, noDetail: true})
	g2 := sbGoldens(t, n.s)
	g2.check("gitlab_comments_not_connected", func() response {
		r := n.post(gbAt("a.ts", 12, "x"))
		r.Body = bytes.ReplaceAll(r.Body, []byte(n.lab.host), []byte(fixedHost))
		return r
	}(), http.StatusUnprocessableEntity)
}
