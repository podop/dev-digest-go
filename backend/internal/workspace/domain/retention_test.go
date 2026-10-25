package domain_test

import (
	"errors"
	"testing"
	"time"

	"dev-digest-go/backend/internal/workspace/domain"
)

func TestParseRetention(t *testing.T) {
	for _, days := range []int{30, 60, 90, 180, 365} {
		got, err := domain.ParseRetention(days)
		if err != nil || got != (domain.Retention{Days: days}) {
			t.Errorf("ParseRetention(%d) = %+v, %v, want {Days:%d}, nil", days, got, err, days)
		}
	}
	for _, days := range []int{-30, 0, 1, 45, 91, 366} {
		if got, err := domain.ParseRetention(days); !errors.Is(err, domain.ErrInvalidRetention) {
			t.Errorf("ParseRetention(%d) = %+v, %v, want ErrInvalidRetention", days, got, err)
		}
	}
}

func TestDecodeRetention(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		present bool
		want    domain.Retention
		wantErr bool
	}{
		{name: "absent is 90 (EC9)", present: false, want: domain.Retention{Days: 90}},
		{name: "null keeps forever (EC9)", raw: "null", present: true, want: domain.Retention{Forever: true}},
		{name: "30", raw: "30", present: true, want: domain.Retention{Days: 30}},
		{name: "365 with spaces", raw: " 365 ", present: true, want: domain.Retention{Days: 365}},
		{name: "45", raw: "45", present: true, wantErr: true},
		{name: "0", raw: "0", present: true, wantErr: true},
		{name: "string 90", raw: `"90"`, present: true, wantErr: true},
		{name: "fraction", raw: "30.0", present: true, wantErr: true},
		{name: "exponent", raw: "3e1", present: true, wantErr: true},
		{name: "bool", raw: "true", present: true, wantErr: true},
		{name: "object", raw: "{}", present: true, wantErr: true},
		{name: "empty", raw: "", present: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.DecodeRetention([]byte(tc.raw), tc.present)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalidRetention) {
					t.Fatalf("DecodeRetention(%q, %v) error = %v, want ErrInvalidRetention", tc.raw, tc.present, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("DecodeRetention(%q, %v) = %+v, %v, want %+v, nil", tc.raw, tc.present, got, err, tc.want)
			}
		})
	}
}

func TestRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 500, time.UTC)
	tests := []struct {
		name string
		r    domain.Retention
		want time.Time
	}{
		{name: "90 days", r: domain.Retention{Days: 90}, want: now.Add(-90 * 24 * time.Hour)},
		{name: "30 days", r: domain.Retention{Days: 30}, want: time.Date(2026, 9, 25, 12, 0, 0, 500, time.UTC)},
		{name: "forever has no cutoff", r: domain.Retention{Forever: true}, want: time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.Cutoff(now); !got.Equal(tc.want) {
				t.Errorf("%+v.Cutoff(%v) = %v, want %v", tc.r, now, got, tc.want)
			}
		})
	}
}

// EC8: a run started exactly at the cutoff is not before it, one second earlier is.
func TestRetentionCutoffBoundary(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cutoff := domain.DefaultRetention().Cutoff(now)
	atCutoff := now.Add(-90 * 24 * time.Hour)
	if atCutoff.Before(cutoff) {
		t.Errorf("run started at %v is before cutoff %v, want kept", atCutoff, cutoff)
	}
	if !atCutoff.Add(-time.Second).Before(cutoff) {
		t.Errorf("run started 1 s before %v is not before cutoff %v, want pruned", atCutoff, cutoff)
	}
}

func TestRetentionChoicesIsACopy(t *testing.T) {
	got := domain.RetentionChoices()
	got[0] = 1
	if again := domain.RetentionChoices(); again[0] != 30 {
		t.Errorf("RetentionChoices()[0] = %d after caller write, want 30", again[0])
	}
}
