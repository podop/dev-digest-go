package domain_test

import (
	"testing"
	"time"

	"dev-digest-go/backend/internal/pulls/domain"
)

func TestClampPollInterval(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"missing", 0, 5 * time.Minute},
		{"negative", -time.Minute, 5 * time.Minute},
		{"below minimum", 10 * time.Second, time.Minute},
		{"minimum", time.Minute, time.Minute},
		{"normal", 15 * time.Minute, 15 * time.Minute},
		{"maximum", 1440 * time.Minute, 1440 * time.Minute},
		{"above maximum", 2000 * time.Minute, 1440 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ClampPollInterval(tc.in); got != tc.want {
				t.Errorf("ClampPollInterval(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestReachedCutoff(t *testing.T) {
	cutoff := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := cutoff.Add(d); return &v }
	tests := []struct {
		name string
		in   *time.Time
		want bool
	}{
		{"older", at(-time.Second), true},
		{"exactly at cutoff", at(0), false},
		{"newer", at(time.Second), false},
		{"no update time", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ReachedCutoff(domain.Listed{UpdatedAt: tc.in}, cutoff); got != tc.want {
				t.Errorf("ReachedCutoff(updated %v, cutoff %v) = %v, want %v", tc.in, cutoff, got, tc.want)
			}
		})
	}
}

func TestResumeAfterRateLimit(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	due := start.Add(5 * time.Minute)
	tests := []struct {
		name  string
		reset time.Time
		want  time.Time
	}{
		{"unknown reset", time.Time{}, due},
		{"reset before due", start.Add(time.Minute), due},
		{"reset after due", start.Add(30 * time.Minute), start.Add(30 * time.Minute)},
		{"bogus far reset", start.Add(1000 * time.Hour), start.Add(domain.MaxRateLimitWait)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ResumeAfterRateLimit(start, due, tc.reset); !got.Equal(tc.want) {
				t.Errorf("ResumeAfterRateLimit(reset %v) = %v, want %v", tc.reset, got, tc.want)
			}
		})
	}
}

func TestHostPauses(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	gh := domain.Repo{}.PauseKey()
	gl := domain.Repo{Provider: "gitlab", Host: "gitlab.com"}.PauseKey()
	other := domain.Repo{Provider: "gitlab", Host: "git.corp"}.PauseKey()

	p := domain.NewHostPauses()
	if p.Paused(gl, now) {
		t.Fatal("a fresh set pauses nothing")
	}
	p.Pause(gl, now.Add(10*time.Minute))
	tests := []struct {
		name string
		key  string
		at   time.Time
		want bool
	}{
		{"paused host", gl, now, true},
		{"just before resume", gl, now.Add(10*time.Minute - time.Nanosecond), true},
		{"at resume", gl, now.Add(10 * time.Minute), false},
		{"GitHub is not paused by GitLab", gh, now, false},
		{"another instance is not paused", other, now, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Paused(tc.key, tc.at); got != tc.want {
				t.Errorf("Paused(%q, %v) = %v, want %v", tc.key, tc.at, got, tc.want)
			}
		})
	}

	t.Run("a shorter pause does not shorten", func(t *testing.T) {
		p.Pause(gl, now.Add(time.Minute))
		if got, _ := p.ResumeAt(gl); !got.Equal(now.Add(10 * time.Minute)) {
			t.Errorf("ResumeAt = %v, want %v", got, now.Add(10*time.Minute))
		}
	})
	t.Run("a longer pause extends", func(t *testing.T) {
		p.Pause(gl, now.Add(20*time.Minute))
		if got, _ := p.ResumeAt(gl); !got.Equal(now.Add(20 * time.Minute)) {
			t.Errorf("ResumeAt = %v, want %v", got, now.Add(20*time.Minute))
		}
	})
	t.Run("prune drops only ended pauses", func(t *testing.T) {
		p.Pause(gh, now.Add(time.Minute))
		p.Prune(now.Add(5 * time.Minute))
		if _, ok := p.ResumeAt(gh); ok {
			t.Error("ended GitHub pause survived Prune")
		}
		if _, ok := p.ResumeAt(gl); !ok {
			t.Error("running GitLab pause was pruned")
		}
	})
}

func TestDueAtAndIsDue(t *testing.T) {
	last := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if got, want := domain.DueAt(last, 5*time.Minute), last.Add(5*time.Minute); !got.Equal(want) {
		t.Errorf("DueAt = %v, want %v", got, want)
	}
	due := last.Add(time.Minute)
	tick := time.Minute
	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"well before", last, false},
		{"just outside the half-tick slack", due.Add(-tick/2 - time.Nanosecond), false},
		{"inside the slack", due.Add(-tick / 2), true},
		{"at due", due, true},
		{"after due", due.Add(time.Hour), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.IsDue(tc.now, due, tick); got != tc.want {
				t.Errorf("IsDue(%v, due %v) = %v, want %v", tc.now, due, got, tc.want)
			}
		})
	}
}
