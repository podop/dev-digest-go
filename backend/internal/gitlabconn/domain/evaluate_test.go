package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/gitlabconn/domain"
)

var today = time.Date(2026, 10, 26, 15, 30, 0, 0, time.UTC)

func date(days int) *time.Time {
	d := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
	return &d
}

func codes(ws []domain.Warning) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.Code)
	}
	return out
}

// TestEvaluate covers AC7, EC7, EC8, EC9 and D34.
func TestEvaluate(t *testing.T) {
	const ok17 = "17.5.1-ee"
	tests := []struct {
		name       string
		e          domain.Evidence
		wantOK     bool
		wantAccess domain.TokenAccess
		wantCodes  []string
	}{
		{"api + read_repository", domain.Evidence{Scopes: []string{"api", "read_repository"}, Version: ok17}, true, domain.AccessFull, []string{}},
		{"api alone", domain.Evidence{Scopes: []string{"api"}, Version: ok17}, true, domain.AccessFull, []string{}},
		{"read_api + read_repository", domain.Evidence{Scopes: []string{"read_api", "read_repository"}, Version: ok17}, true, domain.AccessReadOnly, []string{"read_only_token"}},
		{"read_api alone", domain.Evidence{Scopes: []string{"read_api"}, Version: ok17}, true, domain.AccessReadOnly, []string{"read_only_token", "no_repository_scope"}},
		{"read_api + write_repository", domain.Evidence{Scopes: []string{"read_api", "write_repository"}, Version: ok17}, true, domain.AccessReadOnly, []string{"read_only_token"}},
		{"read_user is refused", domain.Evidence{Scopes: []string{"read_user"}, Version: "16.0.0"}, false, "", []string{}},
		{"read_repository only is refused", domain.Evidence{Scopes: []string{"read_repository"}, Version: ok17}, false, "", []string{}},
		{"no scopes is refused", domain.Evidence{Scopes: []string{}, Version: ok17}, false, "", []string{}},
		{"D34 scopes unknown", domain.Evidence{Version: ok17}, true, "", []string{"scopes_unknown"}},
		{"expires null", domain.Evidence{Scopes: []string{"api"}, Version: ok17}, true, domain.AccessFull, []string{}},
		{"expires today", domain.Evidence{Scopes: []string{"api"}, ExpiresAt: date(0), Version: ok17}, true, domain.AccessFull, []string{"token_expiring"}},
		{"EC7 expires +14", domain.Evidence{Scopes: []string{"api"}, ExpiresAt: date(14), Version: ok17}, true, domain.AccessFull, []string{"token_expiring"}},
		{"EC7 expires +15", domain.Evidence{Scopes: []string{"api"}, ExpiresAt: date(15), Version: ok17}, true, domain.AccessFull, []string{}},
		{"expired yesterday", domain.Evidence{Scopes: []string{"api"}, ExpiresAt: date(-1), Version: ok17}, true, domain.AccessFull, []string{"token_expiring"}},
		{"EC8 16.11.3-ee", domain.Evidence{Scopes: []string{"api"}, Version: "16.11.3-ee"}, true, domain.AccessFull, []string{"version_unsupported"}},
		{"EC8 17.0.0", domain.Evidence{Scopes: []string{"api"}, Version: "17.0.0"}, true, domain.AccessFull, []string{}},
		{"15.9", domain.Evidence{Scopes: []string{"api"}, Version: "15.9"}, true, domain.AccessFull, []string{"version_unsupported"}},
		{"18.2.0-pre", domain.Evidence{Scopes: []string{"api"}, Version: "18.2.0-pre"}, true, domain.AccessFull, []string{}},
		{"EC8 version unknown", domain.Evidence{Scopes: []string{"api"}}, true, domain.AccessFull, []string{"version_unknown"}},
		{"unparsable version", domain.Evidence{Scopes: []string{"api"}, Version: "nightly"}, true, domain.AccessFull, []string{"version_unknown"}},
		{"huge version number", domain.Evidence{Scopes: []string{"api"}, Version: "99999999999999999999.0"}, true, domain.AccessFull, []string{"version_unknown"}},
		{
			"everything, in order",
			domain.Evidence{Scopes: []string{"read_api"}, ExpiresAt: date(3), Version: "16.0.1"},
			true, domain.AccessReadOnly,
			[]string{"read_only_token", "no_repository_scope", "token_expiring", "version_unsupported"},
		},
		{
			"unknowns come last, in order",
			domain.Evidence{ExpiresAt: date(3)},
			true, "",
			[]string{"token_expiring", "scopes_unknown", "version_unknown"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.e.Login = "ann"
			got := domain.Evaluate(tc.e, today)
			if got.OK != tc.wantOK || got.TokenAccess != tc.wantAccess {
				t.Errorf("Evaluate(%+v) = OK %v access %q, want OK %v access %q", tc.e, got.OK, got.TokenAccess, tc.wantOK, tc.wantAccess)
			}
			if diff := cmp.Diff(tc.wantCodes, codes(got.Warnings)); diff != "" {
				t.Errorf("Evaluate(%+v) warning codes mismatch (-want +got):\n%s", tc.e, diff)
			}
			if got.Warnings == nil {
				t.Errorf("Evaluate(%+v).Warnings = nil, want a non-nil slice", tc.e)
			}
			wantMsg := "Connected as @ann"
			if !tc.wantOK {
				wantMsg = domain.MsgNeedsScope
			}
			if got.Message != wantMsg {
				t.Errorf("Evaluate(%+v).Message = %q, want %q", tc.e, got.Message, wantMsg)
			}
		})
	}
}

func TestEvaluate_Messages(t *testing.T) {
	got := domain.Evaluate(domain.Evidence{
		Login: "ann", Scopes: []string{"read_api"}, ExpiresAt: date(14), Version: "16.11.3-ee",
	}, today)
	want := []domain.Warning{
		{Code: "read_only_token", Message: "Read-only token: sync works; posting to merge requests and GitLab CI export will fail"},
		{Code: "no_repository_scope", Message: "Without read_repository, private projects may not be cloned"},
		{Code: "token_expiring", Message: "Token expires on 2026-11-09 (in 14 days)"},
		{Code: "version_unsupported", Message: "GitLab 16.11.3-ee is older than the supported 17.0; some features may fail"},
	}
	if diff := cmp.Diff(want, got.Warnings); diff != "" {
		t.Errorf("Evaluate warnings mismatch (-want +got):\n%s", diff)
	}
}

// TestEvaluate_UTCDates: only the UTC calendar date of now and of the expiry counts.
func TestEvaluate_UTCDates(t *testing.T) {
	zone := time.FixedZone("UTC+14", 14*3600)
	nowLate := time.Date(2026, 10, 26, 23, 59, 0, 0, time.UTC)
	exp := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for _, now := range []time.Time{nowLate, nowLate.In(zone), nowLate.Add(-23*time.Hour - 59*time.Minute)} {
		got := domain.Evaluate(domain.Evidence{Scopes: []string{"api"}, ExpiresAt: &exp, Version: "17.0"}, now)
		if c := codes(got.Warnings); len(c) != 1 || c[0] != "token_expiring" {
			t.Errorf("Evaluate(expiry %v, now %v) warnings = %v, want [token_expiring]", exp, now, c)
		}
	}
	next := nowLate.Add(2 * time.Minute) // 27 Oct: expiry is 13 days away
	got := domain.Evaluate(domain.Evidence{Scopes: []string{"api"}, ExpiresAt: &exp, Version: "17.0"}, next)
	if got.Warnings[0].Message != "Token expires on 2026-11-09 (in 13 days)" {
		t.Errorf("Evaluate(now %v) message = %q", next, got.Warnings[0].Message)
	}
}

func TestErrors_AreDistinct(t *testing.T) {
	errs := []error{domain.ErrExists, domain.ErrLimit, domain.ErrNotFound}
	for i, a := range errs {
		for j, b := range errs {
			if (i == j) != errors.Is(a, b) {
				t.Errorf("errors.Is(%v, %v) = %v", a, b, errors.Is(a, b))
			}
		}
	}
	if domain.Limit != 20 {
		t.Errorf("Limit = %d, want 20", domain.Limit)
	}
}
