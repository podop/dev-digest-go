package domain_test

import (
	"net/url"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zone database must not depend on the host

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

const queryTestRepoID = "0b6c1f5e-8a4d-4f3b-9c21-7d5e3a9b1c44"

func TestParseQuery_Defaults(t *testing.T) {
	q, issues := domain.ParseQuery(url.Values{})
	if len(issues) != 0 {
		t.Fatalf("ParseQuery({}) issues = %v, want none", issues)
	}
	if q.Period != domain.PeriodWeek || q.TZ != "UTC" || q.Location != time.UTC || q.Repo != domain.RepoAll {
		t.Errorf("ParseQuery({}) = %+v, want week / UTC / all", q)
	}
}

func TestParseQuery_Valid(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantPeriod domain.PeriodKey
		wantTZ     string
		wantRepo   string
	}{
		{"last_week", "period=last_week", domain.PeriodLastWeek, "UTC", "all"},
		{"30d with zone", "period=30d&tz=Europe/Kyiv", domain.Period30d, "Europe/Kyiv", "all"},
		{"nested zone", "tz=America/Argentina/Buenos_Aires", domain.PeriodWeek, "America/Argentina/Buenos_Aires", "all"},
		{"etc zone with sign", "tz=Etc/GMT%2B3", domain.PeriodWeek, "Etc/GMT+3", "all"},
		{"hyphenated zone", "tz=America/Port-au-Prince", domain.PeriodWeek, "America/Port-au-Prince", "all"},
		{"explicit defaults", "period=week&tz=UTC&repo=all", domain.PeriodWeek, "UTC", "all"},
		{"repo uuid", "repo=" + queryTestRepoID, domain.PeriodWeek, "UTC", queryTestRepoID},
		{"repo uuid upper case is lowered", "repo=" + strings.ToUpper(queryTestRepoID), domain.PeriodWeek, "UTC", queryTestRepoID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatalf("url.ParseQuery(%q) error = %v", tc.query, err)
			}
			q, issues := domain.ParseQuery(v)
			if len(issues) != 0 {
				t.Fatalf("ParseQuery(%q) issues = %v, want none", tc.query, issues)
			}
			if q.Period != tc.wantPeriod || q.TZ != tc.wantTZ || q.Repo != tc.wantRepo {
				t.Errorf("ParseQuery(%q) = %+v, want %s / %s / %s", tc.query, q, tc.wantPeriod, tc.wantTZ, tc.wantRepo)
			}
			if q.Location == nil || q.Location == time.Local {
				t.Errorf("ParseQuery(%q).Location = %v, want a loaded non-Local zone", tc.query, q.Location)
			}
		})
	}
}

func TestParseQuery_Issues(t *testing.T) {
	long := strings.Repeat("a", domain.MaxTZLen+1)
	tests := []struct {
		name      string
		query     url.Values
		wantParam []string
	}{
		{"unknown period", url.Values{"period": {"month"}}, []string{"period"}},
		{"period wrong case", url.Values{"period": {"Week"}}, []string{"period"}},
		{"empty period", url.Values{"period": {""}}, []string{"period"}},
		{"unknown tz", url.Values{"tz": {"Mars/Olympus"}}, []string{"tz"}},
		{"tz Local", url.Values{"tz": {"Local"}}, []string{"tz"}},
		{"tz local lower case", url.Values{"tz": {"local"}}, []string{"tz"}},
		{"tz LOCAL", url.Values{"tz": {"LOCAL"}}, []string{"tz"}},
		{"tz 65 chars", url.Values{"tz": {long}}, []string{"tz"}},
		{"tz 65 chars with slash", url.Values{"tz": {"Europe/" + strings.Repeat("a", 58)}}, []string{"tz"}},
		{"empty tz", url.Values{"tz": {""}}, []string{"tz"}},
		{"tz dot dot", url.Values{"tz": {"../etc/passwd"}}, []string{"tz"}},
		{"tz absolute", url.Values{"tz": {"/etc/localtime"}}, []string{"tz"}},
		{"tz backslash", url.Values{"tz": {`Europe\Kyiv`}}, []string{"tz"}},
		{"tz doubled slash", url.Values{"tz": {"Europe//Kyiv"}}, []string{"tz"}},
		{"tz trailing slash", url.Values{"tz": {"Europe/"}}, []string{"tz"}},
		{"tz with dot", url.Values{"tz": {"zone.tab"}}, []string{"tz"}},
		{"tz with space", url.Values{"tz": {"Europe/Kyiv "}}, []string{"tz"}},
		{"tz with NUL", url.Values{"tz": {"UTC\x00"}}, []string{"tz"}},
		{"tz lower-case zone", url.Values{"tz": {"europe/kyiv"}}, []string{"tz"}},
		{"tz directory", url.Values{"tz": {"Europe"}}, []string{"tz"}},
		{"repo not a uuid", url.Values{"repo": {"owner/name"}}, []string{"repo"}},
		{"repo ALL", url.Values{"repo": {"ALL"}}, []string{"repo"}},
		{"repo uuid with suffix", url.Values{"repo": {queryTestRepoID + "0"}}, []string{"repo"}},
		{"repo uuid with newline", url.Values{"repo": {queryTestRepoID + "\n"}}, []string{"repo"}},
		{"repo sql", url.Values{"repo": {"x' OR '1'='1"}}, []string{"repo"}},
		{"empty repo", url.Values{"repo": {""}}, []string{"repo"}},
		{"extra key", url.Values{"limit": {"5"}}, []string{"limit"}},
		{"extra keys sorted", url.Values{"zeta": {"1"}, "alpha": {"1"}}, []string{"alpha", "zeta"}},
		{"extra key differs by case", url.Values{"Period": {"week"}}, []string{"Period"}},
		{"repeated period", url.Values{"period": {"week", "30d"}}, []string{"period"}},
		{"repeated tz", url.Values{"tz": {"UTC", "UTC"}}, []string{"tz"}},
		{"repeated repo", url.Values{"repo": {"all", "all"}}, []string{"repo"}},
		{"key without values", url.Values{"period": nil}, []string{"period"}},
		{"several, in fixed order", url.Values{"zz": {"1"}, "repo": {"x"}, "tz": {"Nope"}, "period": {"x"}}, []string{"period", "tz", "repo", "zz"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, issues := domain.ParseQuery(tc.query)
			var got []string
			for _, is := range issues {
				got = append(got, is.Param)
				if is.Message == "" {
					t.Errorf("ParseQuery(%v) issue %q has an empty message", tc.query, is.Param)
				}
			}
			if diff := cmp.Diff(tc.wantParam, got); diff != "" {
				t.Errorf("ParseQuery(%v) issue params mismatch (-want +got):\n%s", tc.query, diff)
			}
		})
	}
}

func TestParseQuery_MessagesDoNotEchoValues(t *testing.T) {
	const secret = "s3cr3t-value"
	_, issues := domain.ParseQuery(url.Values{
		"period": {secret}, "tz": {secret}, "repo": {secret},
	})
	if len(issues) != 3 {
		t.Fatalf("ParseQuery issues = %v, want 3", issues)
	}
	for _, is := range issues {
		if strings.Contains(is.Message, secret) {
			t.Errorf("issue %q message %q echoes the request value", is.Param, is.Message)
		}
	}
}

func FuzzParseQuery(f *testing.F) {
	for _, s := range []string{
		"", "period=week", "period=last_week&tz=Europe/Kyiv&repo=all", "period=30d&tz=UTC",
		"tz=Local", "tz=local", "tz=../etc/passwd", "tz=/etc/localtime", "tz=Etc/GMT%2B3",
		"tz=" + strings.Repeat("a", 65), "repo=" + queryTestRepoID, "repo=%00", "period=a&period=b",
		"a=1&b=2", "tz=Europe//Kyiv", "period=&tz=&repo=", "%zz", "tz=UTC%00", "repo=ALL", "=0",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		v, err := url.ParseQuery(raw)
		if err != nil && v == nil {
			return
		}
		q, issues := domain.ParseQuery(v)
		if len(issues) != 0 {
			for _, is := range issues {
				if is.Message == "" {
					t.Errorf("ParseQuery(%q) issue %+v has an empty message", raw, is)
				}
			}
			return
		}
		switch q.Period {
		case domain.PeriodWeek, domain.PeriodLastWeek, domain.Period30d:
		default:
			t.Errorf("ParseQuery(%q).Period = %q, want a known period", raw, q.Period)
		}
		if q.Location == nil || q.Location == time.Local || len(q.TZ) == 0 || len(q.TZ) > domain.MaxTZLen {
			t.Errorf("ParseQuery(%q) TZ = %q Location = %v, want a loaded non-Local zone of <= %d chars", raw, q.TZ, q.Location, domain.MaxTZLen)
		}
		if strings.EqualFold(q.TZ, "Local") || strings.Contains(q.TZ, "..") || strings.HasPrefix(q.TZ, "/") {
			t.Errorf("ParseQuery(%q).TZ = %q, want a plain zone name", raw, q.TZ)
		}
		if q.Repo != domain.RepoAll && len(q.Repo) != 36 {
			t.Errorf("ParseQuery(%q).Repo = %q, want all or a UUID", raw, q.Repo)
		}
		if len(v) > 3 {
			t.Errorf("ParseQuery(%q) accepted %d keys, want at most 3", raw, len(v))
		}
	})
}
