package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
)

var base = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func run(id string, at *time.Time) *domain.Run { return &domain.Run{ID: id, RanAt: at} }

func at(d time.Duration) *time.Time { t := base.Add(d); return &t }

func inst(id, repo, agent string, installed time.Duration, last *domain.Run) domain.RepoInstallation {
	return domain.RepoInstallation{ID: id, Repo: repo, AgentName: agent, InstalledAt: base.Add(installed), NewestRun: last}
}

func TestGroupByRepo_Empty(t *testing.T) {
	t.Parallel()
	if got := domain.GroupByRepo(nil); got != nil {
		t.Errorf("GroupByRepo(nil) = %v, want nil", got)
	}
}

func TestGroupByRepo_OrdersReposInByteOrder(t *testing.T) {
	t.Parallel()
	in := []domain.RepoInstallation{
		inst("1", "acme/b", "x", 0, nil),
		inst("2", "Zeta/a", "x", 0, nil),
		inst("3", "acme/B", "x", 0, nil),
		inst("4", "acme/a", "x", 0, nil),
	}
	var got []string
	for _, g := range domain.GroupByRepo(in) {
		got = append(got, g.Repo)
	}
	want := []string{"Zeta/a", "acme/B", "acme/a", "acme/b"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GroupByRepo() repos mismatch (-want +got):\n%s", diff)
	}
}

func TestGroupByRepo_OrdersInstallations(t *testing.T) {
	t.Parallel()
	in := []domain.RepoInstallation{
		inst("d", "acme/api", "beta", 0, nil),
		inst("c", "acme/api", "alpha", time.Hour, nil),
		inst("b", "acme/api", "alpha", 0, nil),
		inst("a", "acme/api", "Zed", 5*time.Hour, nil),
		inst("e", "acme/api", "alpha", 0, nil),
	}
	groups := domain.GroupByRepo(in)
	if len(groups) != 1 {
		t.Fatalf("GroupByRepo() = %d groups, want 1", len(groups))
	}
	var got []string
	for _, i := range groups[0].Installations {
		got = append(got, i.ID)
	}
	// agent name in byte order (uppercase first), then installed_at, then id
	if diff := cmp.Diff([]string{"a", "b", "e", "c", "d"}, got); diff != "" {
		t.Errorf("installations order mismatch (-want +got):\n%s", diff)
	}
}

func TestGroupByRepo_LastRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		runs []*domain.Run
		want string // run id; "" = nil
	}{
		{name: "none reported", runs: []*domain.Run{nil, nil}, want: ""},
		{name: "newest ran_at wins", runs: []*domain.Run{run("t1", at(0)), run("t2", at(time.Hour)), nil}, want: "t2"},
		{name: "input order does not matter", runs: []*domain.Run{run("t2", at(time.Hour)), run("t1", at(0))}, want: "t2"},
		{name: "dated beats undated", runs: []*domain.Run{run("undated", nil), run("dated", at(0))}, want: "dated"},
		{name: "only undated", runs: []*domain.Run{run("u2", nil), run("u1", nil)}, want: "u1"},
		{name: "tie goes to the smaller id", runs: []*domain.Run{run("b", at(0)), run("a", at(0))}, want: "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var in []domain.RepoInstallation
			for i, r := range tc.runs {
				in = append(in, inst(string(rune('a'+i)), "acme/api", "agent", 0, r))
			}
			groups := domain.GroupByRepo(in)
			got := ""
			if groups[0].LastRun != nil {
				got = groups[0].LastRun.ID
			}
			if got != tc.want {
				t.Errorf("GroupByRepo() last run = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGroupByRepo_LastRunIsPerRepo(t *testing.T) {
	t.Parallel()
	groups := domain.GroupByRepo([]domain.RepoInstallation{
		inst("1", "acme/x", "a", 0, run("x-old", at(0))),
		inst("2", "acme/x", "b", 0, run("x-new", at(time.Hour))),
		inst("3", "acme/y", "a", 0, nil),
	})
	if len(groups) != 2 || groups[0].LastRun == nil || groups[0].LastRun.ID != "x-new" || groups[1].LastRun != nil {
		t.Errorf("GroupByRepo() = %+v, want x with run x-new then y without a run", groups)
	}
	if len(groups[0].Installations) != 2 || len(groups[1].Installations) != 1 {
		t.Errorf("installation counts = %d, %d, want 2, 1", len(groups[0].Installations), len(groups[1].Installations))
	}
}
