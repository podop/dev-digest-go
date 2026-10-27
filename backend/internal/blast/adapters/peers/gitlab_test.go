package peers_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/blast/adapters/peers"
	"dev-digest-go/backend/internal/blast/app"
	"dev-digest-go/backend/internal/blast/domain"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

// fakeGL answers the two reads; diffs may block on gate to hold requests in flight.
type fakeGL struct {
	mrs     []glclient.MergedMergeRequest
	mrErr   error
	paths   map[int][]string
	failIID int
	gate    chan struct{}

	mu       sync.Mutex
	conns    []glclient.Conn
	perPage  int
	project  int64
	diffIIDs []int
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (f *fakeGL) MergedMergeRequests(_ context.Context, conn glclient.Conn, projectID int64, perPage int) ([]glclient.MergedMergeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns = append(f.conns, conn)
	f.project, f.perPage = projectID, perPage
	return f.mrs, f.mrErr
}

func (f *fakeGL) DiffPaths(ctx context.Context, _ glclient.Conn, _ int64, iid int) ([]string, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	f.diffIIDs = append(f.diffIIDs, iid)
	f.mu.Unlock()
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if iid == f.failIID {
		return nil, errors.New("gitlab answered 500")
	}
	return f.paths[iid], nil
}

func binding(token string) glclient.ResolveFunc {
	return func(_ context.Context, host string) (glclient.Binding, bool, error) {
		if host != "git.example.com" {
			return glclient.Binding{}, false, nil
		}
		return glclient.Binding{ID: "c1", Host: host, BaseURL: "https://git.example.com", Token: token}, true, nil
	}
}

func TestGitLab_ListMergedMapsAndKeepsListOrder(t *testing.T) { // FR24, A8
	c := &fakeGL{
		mrs: []glclient.MergedMergeRequest{
			{IID: 9, Title: "Newest", Author: "ann", MergedAt: "2026-01-02T00:00:00Z"},
			{IID: 8, Title: "No merge time", Author: "unknown", MergedAt: ""},
		},
		paths: map[int][]string{9: {"a.go", "b.go"}, 8: {"c.go"}},
	}
	got, err := peers.NewGitLab(c, binding("glpat-x")).ListMerged(t.Context(), "git.example.com", 42, 30)
	if err != nil {
		t.Fatalf("ListMerged() error = %v", err)
	}
	want := []domain.MergedPR{
		{Number: 9, Title: "Newest", Author: "ann", MergedAt: "2026-01-02T00:00:00Z", Files: []string{"a.go", "b.go"}},
		{Number: 8, Title: "No merge time", Author: "unknown", MergedAt: "", Files: []string{"c.go"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListMerged() mismatch (-want +got):\n%s", diff)
	}
	if c.project != 42 || c.perPage != 30 {
		t.Errorf("client saw project %d per_page %d, want 42 and 30", c.project, c.perPage)
	}
	wantConn := glclient.Conn{BaseURL: "https://git.example.com/api/v4", Token: "glpat-x"}
	if diff := cmp.Diff([]glclient.Conn{wantConn}, c.conns); diff != "" {
		t.Errorf("connection mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLab_ListMergedNoRequestWithoutConnectionOrToken(t *testing.T) { // FR23
	for _, tc := range []struct {
		name    string
		host    string
		resolve glclient.ResolveFunc
	}{
		{"not connected", "other.example.com", binding("glpat-x")},
		{"no token", "git.example.com", binding("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeGL{mrs: []glclient.MergedMergeRequest{{IID: 1}}}
			_, err := peers.NewGitLab(c, tc.resolve).ListMerged(t.Context(), tc.host, 42, 30)
			if !errors.Is(err, app.ErrGitLabNotConnected) {
				t.Errorf("ListMerged() error = %v, want app.ErrGitLabNotConnected", err)
			}
			if len(c.conns) != 0 || len(c.diffIIDs) != 0 {
				t.Errorf("client saw %d list and %d diffs requests, want none", len(c.conns), len(c.diffIIDs))
			}
		})
	}
}

func TestGitLab_ListMergedResolveErrorFails(t *testing.T) {
	boom := errors.New("db down")
	resolve := func(context.Context, string) (glclient.Binding, bool, error) { return glclient.Binding{}, false, boom }
	c := &fakeGL{}
	if _, err := peers.NewGitLab(c, resolve).ListMerged(t.Context(), "git.example.com", 42, 30); !errors.Is(err, boom) {
		t.Errorf("ListMerged() error = %v, want %v", err, boom)
	}
	if len(c.conns) != 0 {
		t.Errorf("client saw %d requests, want none", len(c.conns))
	}
}

func TestGitLab_ListMergedListErrorFails(t *testing.T) {
	boom := errors.New("gitlab answered 500")
	c := &fakeGL{mrErr: boom}
	if _, err := peers.NewGitLab(c, binding("glpat-x")).ListMerged(t.Context(), "git.example.com", 42, 30); !errors.Is(err, boom) {
		t.Errorf("ListMerged() error = %v, want %v", err, boom)
	}
}

func TestGitLab_ListMergedDiffFailureFailsTheWholeRead(t *testing.T) { // EC19
	c := &fakeGL{paths: map[int][]string{}, failIID: 7}
	for iid := 1; iid <= 30; iid++ {
		c.mrs = append(c.mrs, glclient.MergedMergeRequest{IID: iid})
	}
	got, err := peers.NewGitLab(c, binding("glpat-x")).ListMerged(t.Context(), "git.example.com", 42, 30)
	if err == nil || got != nil {
		t.Errorf("ListMerged() = %v, %v, want nil and an error", got, err)
	}
}

func TestGitLab_ListMergedAtMostFiveDiffsInFlight(t *testing.T) { // FR24, AC21
	c := &fakeGL{paths: map[int][]string{}, gate: make(chan struct{})}
	for iid := 1; iid <= 30; iid++ {
		c.mrs = append(c.mrs, glclient.MergedMergeRequest{IID: iid, Title: fmt.Sprint("mr", iid)})
		c.paths[iid] = []string{fmt.Sprintf("f%d.go", iid)}
	}
	type result struct {
		got []domain.MergedPR
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, err := peers.NewGitLab(c, binding("glpat-x")).ListMerged(t.Context(), "git.example.com", 42, 30)
		done <- result{got, err}
	}()
	deadline := time.After(10 * time.Second)
	// Hold the first five requests until all five are in flight, then release everything.
	for c.inFlight.Load() < peers.MaxDiffsInFlight {
		select {
		case <-deadline:
			t.Fatal("five diffs requests never ran together")
		case <-time.After(time.Millisecond):
		}
	}
	close(c.gate)
	select {
	case r := <-done:
		if r.err != nil || len(r.got) != 30 {
			t.Fatalf("ListMerged() = %d items, %v, want 30 and no error", len(r.got), r.err)
		}
		for i, p := range r.got {
			if p.Number != i+1 {
				t.Errorf("item %d number = %d, want %d (list order)", i, p.Number, i+1)
			}
		}
	case <-deadline:
		t.Fatal("ListMerged() did not finish")
	}
	if peak := c.maxSeen.Load(); peak != peers.MaxDiffsInFlight {
		t.Errorf("peak diffs in flight = %d, want exactly %d", peak, peers.MaxDiffsInFlight)
	}
	if len(c.diffIIDs) != 30 {
		t.Errorf("diffs requests = %d, want 30", len(c.diffIIDs))
	}
}

func TestGitLab_ListMergedCancelledContext(t *testing.T) {
	c := &fakeGL{paths: map[int][]string{}, gate: make(chan struct{})}
	for iid := 1; iid <= 10; iid++ {
		c.mrs = append(c.mrs, glclient.MergedMergeRequest{IID: iid})
	}
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := peers.NewGitLab(c, binding("glpat-x")).ListMerged(ctx, "git.example.com", 42, 30)
		errc <- err
	}()
	for c.inFlight.Load() < peers.MaxDiffsInFlight {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ListMerged() error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ListMerged() did not return after cancel")
	}
}
