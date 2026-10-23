package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

func rankRow(id string, health *int, critical int, repo string, number int) domain.Row {
	return domain.Row{
		PRID: id, RepoID: "repo-" + repo, Repo: repo, Number: number, Health: health,
		Findings: domain.FindingCounts{Critical: critical},
	}
}

func TestRank(t *testing.T) {
	h := func(v int) *int { return &v }
	tests := []struct {
		name string
		in   []domain.Row
		want []string
	}{
		{
			name: "health descending",
			in:   []domain.Row{rankRow("a", h(40), 0, "api", 1), rankRow("b", h(90), 0, "api", 2), rankRow("c", h(65), 0, "api", 3)},
			want: []string{"b", "c", "a"},
		},
		{
			name: "zero health is a value, not unknown",
			in:   []domain.Row{rankRow("a", nil, 0, "api", 1), rankRow("b", h(0), 0, "api", 2), rankRow("c", h(1), 0, "api", 3)},
			want: []string{"c", "b", "a"},
		},
		{
			name: "unknown health last, ordered among themselves by the tie-breakers",
			in: []domain.Row{
				rankRow("n1", nil, 3, "api", 1), rankRow("a", h(10), 9, "web", 9),
				rankRow("n2", nil, 1, "web", 2), rankRow("n3", nil, 1, "api", 5),
			},
			want: []string{"a", "n3", "n2", "n1"},
		},
		{
			name: "equal health: fewer critical first",
			in:   []domain.Row{rankRow("a", h(80), 2, "api", 1), rankRow("b", h(80), 0, "api", 2), rankRow("c", h(80), 1, "api", 3)},
			want: []string{"b", "c", "a"},
		},
		{
			name: "health beats critical",
			in:   []domain.Row{rankRow("a", h(70), 0, "api", 1), rankRow("b", h(71), 9, "api", 2)},
			want: []string{"b", "a"},
		},
		{
			name: "equal health and critical: repo, then number",
			in: []domain.Row{
				rankRow("a", h(80), 1, "web", 1), rankRow("b", h(80), 1, "api", 10),
				rankRow("c", h(80), 1, "api", 2), rankRow("d", h(80), 1, "docs", 1),
			},
			want: []string{"c", "b", "d", "a"},
		},
		{
			name: "same repo name and number: repo id, then PR id",
			in: []domain.Row{
				{PRID: "z", RepoID: "r1", Repo: "api", Number: 1, Health: h(5)},
				{PRID: "y", RepoID: "r2", Repo: "api", Number: 1, Health: h(5)},
				{PRID: "x", RepoID: "r1", Repo: "api", Number: 1, Health: h(5)},
			},
			want: []string{"x", "z", "y"},
		},
		{name: "empty", in: nil, want: []string{}},
		{name: "single", in: []domain.Row{rankRow("a", nil, 0, "api", 1)}, want: []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			domain.Rank(tc.in)
			if diff := cmp.Diff(tc.want, rowIDs(tc.in)); diff != "" {
				t.Errorf("Rank() order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRank_InputOrderIndependent: any permutation of the input gives the same order.
func TestRank_InputOrderIndependent(t *testing.T) {
	h := func(v int) *int { return &v }
	rows := []domain.Row{
		rankRow("a", h(80), 1, "api", 1), rankRow("b", h(80), 1, "api", 2), rankRow("c", nil, 0, "api", 3),
		rankRow("d", h(95), 4, "web", 1), rankRow("e", h(80), 0, "web", 2),
	}
	want := []string{"d", "e", "a", "b", "c"}
	for shift := range rows {
		in := append(append([]domain.Row{}, rows[shift:]...), rows[:shift]...)
		domain.Rank(in)
		if diff := cmp.Diff(want, rowIDs(in)); diff != "" {
			t.Errorf("Rank() with input rotated by %d mismatch (-want +got):\n%s", shift, diff)
		}
	}
}
