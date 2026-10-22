package github_test

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/retry"
)

// mergedStub serves a closed-PR list of `total` entries (every third one unmerged, PR 2 without a
// user) and per-PR file lists. onFiles, when set, runs inside each files request.
func mergedStub(t *testing.T, total int, onFiles func(w http.ResponseWriter, r *http.Request, n int) bool) *stub {
	t.Helper()
	return newStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/api/pulls":
			var list []any
			for n := total; n >= 1; n-- {
				pr := map[string]any{"number": n, "title": fmt.Sprintf("PR %d", n), "user": map[string]any{"login": "dev" + strconv.Itoa(n)}}
				if n%3 == 0 {
					pr["merged_at"] = nil
				} else {
					pr["merged_at"] = fmt.Sprintf("2026-10-%02dT10:00:00Z", n)
				}
				if n == 2 {
					pr["user"] = nil
				}
				list = append(list, pr)
			}
			writeJSON(w, 200, list)
		case strings.HasSuffix(r.URL.Path, "/files"):
			n, _ := strconv.Atoi(strings.Split(r.URL.Path, "/")[5])
			if onFiles != nil && onFiles(w, r, n) {
				return
			}
			writeJSON(w, 200, []any{
				map[string]any{"filename": fmt.Sprintf("src/f%d.go", n)},
				map[string]any{"filename": "src/shared.go"},
				map[string]any{"filename": fmt.Sprintf("src/f%d.go", n)}, // type change: listed twice
			})
		default:
			w.WriteHeader(404)
		}
	})
}

func TestListMergedPulls(t *testing.T) {
	t.Parallel()
	// 18 listed, 6 unmerged: 12 merged.
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
		release  = make(chan struct{})
		once     sync.Once
	)
	s := mergedStub(t, 18, func(w http.ResponseWriter, r *http.Request, n int) bool {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		if inFlight == 5 {
			once.Do(func() { close(release) })
		}
		mu.Unlock()
		select { // hold the first five until all five are in flight, so a cap above 5 would show
		case <-release:
		case <-time.After(5 * time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return false
	})
	got, err := newClient(s, github.Options{}).ListMergedPulls(t.Context(), "acme", "api", 30)
	if err != nil {
		t.Fatalf("ListMergedPulls() error: %v", err)
	}
	if len(got) != 12 {
		t.Fatalf("ListMergedPulls() returned %d pulls, want 12", len(got))
	}
	if s.count() != 13 {
		t.Errorf("ListMergedPulls() made %d requests, want 13 (1 list + 12 files)", s.count())
	}
	if peak != 5 {
		t.Errorf("ListMergedPulls() peak in-flight files requests = %d, want 5", peak)
	}
	// List order is kept (newest listed first); merged_at as sent; missing user is "unknown".
	wantFirst := github.MergedPull{Number: 17, Title: "PR 17", Author: "dev17", MergedAt: "2026-10-17T10:00:00Z", Files: []string{"src/f17.go", "src/shared.go"}}
	if diff := cmp.Diff(wantFirst, got[0]); diff != "" {
		t.Errorf("ListMergedPulls()[0] mismatch (-want +got):\n%s", diff)
	}
	for i, p := range got {
		if p.Number%3 == 0 {
			t.Errorf("ListMergedPulls()[%d] = PR %d, which is not merged", i, p.Number)
		}
		if p.Number == 2 && p.Author != "unknown" {
			t.Errorf("PR 2 author = %q, want unknown", p.Author)
		}
	}

	list, _ := s.request(0)
	wantQuery := map[string][]string{"state": {"closed"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"30"}}
	if diff := cmp.Diff(wantQuery, map[string][]string(list.URL.Query())); diff != "" {
		t.Errorf("list query mismatch (-want +got):\n%s", diff)
	}
	for i := 0; i < s.count(); i++ {
		req, _ := s.request(i)
		if req.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("request %d (%s) lacks the token header", i, req.URL.Path)
		}
		if i > 0 && req.URL.Query().Get("per_page") != "100" {
			t.Errorf("files request %d per_page = %q, want 100 and no page parameter", i, req.URL.Query().Get("per_page"))
		}
		if i > 0 && req.URL.Query().Has("page") {
			t.Errorf("files request %d has a page parameter", i)
		}
	}
}

func TestListMergedPulls_limitIsClamped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		limit int
		want  string
	}{{0, "1"}, {-4, "1"}, {1, "1"}, {100, "100"}, {500, "100"}} {
		s := mergedStub(t, 0, nil)
		got, err := newClient(s, github.Options{}).ListMergedPulls(t.Context(), "acme", "api", tc.limit)
		if err != nil || len(got) != 0 {
			t.Fatalf("ListMergedPulls(limit %d) = %v, %v, want an empty list", tc.limit, got, err)
		}
		list, _ := s.request(0)
		if p := list.URL.Query().Get("per_page"); p != tc.want || s.count() != 1 {
			t.Errorf("ListMergedPulls(limit %d) per_page = %q over %d requests, want %q over 1", tc.limit, p, s.count(), tc.want)
		}
	}
}

func TestListMergedPulls_aFailingFilesRequestFailsTheRead(t *testing.T) {
	t.Parallel()
	s := mergedStub(t, 9, func(w http.ResponseWriter, r *http.Request, n int) bool {
		if n == 5 {
			writeJSON(w, 404, map[string]any{"message": "Not Found"})
			return true
		}
		return false
	})
	got, err := newClient(s, github.Options{}).ListMergedPulls(t.Context(), "acme", "api", 30)
	if err == nil || got != nil {
		t.Fatalf("ListMergedPulls() = %v, %v, want nil and an error", got, err)
	}
	if !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("ListMergedPulls() error = %v, want GitHub's message", err)
	}
}

func TestListMergedPulls_retriesOneRequestOnly(t *testing.T) {
	t.Parallel()
	var failed sync.Once
	s := mergedStub(t, 4, func(w http.ResponseWriter, r *http.Request, n int) bool {
		hit := false
		if n == 1 {
			failed.Do(func() { hit = true })
		}
		if hit {
			writeJSON(w, 502, map[string]any{"message": "bad gateway"})
		}
		return hit
	})
	c := newClient(s, github.Options{Retry: &retry.Options{Retries: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}})
	got, err := c.ListMergedPulls(t.Context(), "acme", "api", 30)
	if err != nil || len(got) != 3 {
		t.Fatalf("ListMergedPulls() = %d pulls, %v, want 3, nil", len(got), err)
	}
	if s.count() != 1+3+1 {
		t.Errorf("ListMergedPulls() made %d requests, want 5 (list, 3 files, 1 replay)", s.count())
	}
}

func TestListMergedPulls_noTokenAndNoRedirect(t *testing.T) {
	t.Parallel()
	s := mergedStub(t, 3, nil)
	c := newClient(s, github.Options{Token: func() string { return "" }})
	if _, err := c.ListMergedPulls(t.Context(), "acme", "api", 30); err == nil || s.count() != 0 {
		t.Errorf("ListMergedPulls() without a token = %v after %d requests, want ErrNoToken after 0", err, s.count())
	}

	elsewhere := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []any{}) })
	redirecting := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusMovedPermanently)
	})
	_, err := newClient(redirecting, github.Options{}).ListMergedPulls(t.Context(), "acme", "api", 30)
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusMovedPermanently {
		t.Errorf("ListMergedPulls() on a redirect error = %v, want StatusError 301", err)
	}
	if elsewhere.count() != 0 {
		t.Errorf("the redirect target received %d requests, want 0", elsewhere.count())
	}
}
