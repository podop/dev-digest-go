package domain_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/performance/domain"
)

// nodeCorpus is testdata/node.json: the inputs and the outputs of the Node implementation
// (generator: ../testdata/gen.mts). Only the period part is read here.
type nodeCorpus struct {
	Meta struct {
		Now int64 `json:"now"`
	} `json:"meta"`
	Period []periodCase `json:"period"`
}

type periodCase struct {
	Name   string                     `json:"name"`
	Query  map[string]json.RawMessage `json:"query"`
	Issues []struct {
		Code    string   `json:"code"`
		Path    []string `json:"path"`
		Message string   `json:"message"`
	} `json:"issues"`
	Parsed *struct {
		Period string `json:"period"`
	} `json:"parsed"`
	Window *struct {
		Key         string `json:"key"`
		From        int64  `json:"from"`
		To          int64  `json:"to"`
		ToInclusive bool   `json:"toInclusive"`
	} `json:"window"`
	Previous *struct {
		From        int64 `json:"from"`
		To          int64 `json:"to"`
		ToInclusive bool  `json:"toInclusive"`
	} `json:"previous"`
	Info *struct {
		Key  string `json:"key"`
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"info"`
	WindowError *string `json:"windowError"`
}

func loadCorpus(t *testing.T) nodeCorpus {
	t.Helper()
	b, err := os.ReadFile("../testdata/node.json")
	if err != nil {
		t.Fatal(err)
	}
	var c nodeCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("decode node.json: %v", err)
	}
	return c
}

// values reads a query value that is one string or an array of strings; nil when absent.
func values(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	if raw == nil {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		t.Fatalf("query value %s: %v", raw, err)
	}
	return many
}

func TestParsePeriod_MatchesNode(t *testing.T) {
	c := loadCorpus(t)
	now := time.UnixMilli(c.Meta.Now)
	for _, tc := range c.Period {
		t.Run(tc.Name, func(t *testing.T) {
			raw := domain.RawPeriod{
				Period: values(t, tc.Query["period"]), From: values(t, tc.Query["from"]), To: values(t, tc.Query["to"]),
			}
			q, issues, err := domain.ParsePeriod(raw)

			if tc.Issues != nil {
				var want []domain.PeriodIssue
				for _, i := range tc.Issues {
					want = append(want, domain.PeriodIssue{Code: i.Code, Field: i.Path[0], Message: i.Message})
				}
				if err != nil {
					t.Fatalf("ParsePeriod(%v) error = %v, want issues %v", raw, err, want)
				}
				if diff := cmp.Diff(want, issues); diff != "" {
					t.Errorf("ParsePeriod(%v) issues mismatch (-want +got):\n%s", raw, diff)
				}
				return
			}

			if tc.WindowError != nil {
				if !errors.Is(err, domain.ErrInvalidWindow) {
					t.Errorf("ParsePeriod(%v) = %+v, %v, %v; want ErrInvalidWindow (Node: %s)", raw, q, issues, err, *tc.WindowError)
				}
				return
			}
			if err != nil || len(issues) > 0 {
				t.Fatalf("ParsePeriod(%v) = issues %v, err %v; want a valid query", raw, issues, err)
			}
			win, err := domain.ResolveWindow(q, now)
			if err != nil {
				t.Fatalf("ResolveWindow(%+v) = %v", q, err)
			}
			if got := string(q.Key); got != tc.Parsed.Period {
				t.Errorf("ParsePeriod(%v).Key = %q, want %q", raw, got, tc.Parsed.Period)
			}
			got := struct {
				Key         string
				From, To    int64
				ToInclusive bool
			}{string(win.Key), win.From.UnixMilli(), win.To.UnixMilli(), win.ToInclusive}
			want := struct {
				Key         string
				From, To    int64
				ToInclusive bool
			}{tc.Window.Key, tc.Window.From, tc.Window.To, tc.Window.ToInclusive}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("ResolveWindow(%v) mismatch (-want +got):\n%s", raw, diff)
			}
			prev := win.Previous()
			if prev.From.UnixMilli() != tc.Previous.From || prev.To.UnixMilli() != tc.Previous.To || prev.ToInclusive != tc.Previous.ToInclusive {
				t.Errorf("Previous(%v) = [%d, %d] inclusive %v, want [%d, %d] inclusive %v", raw,
					prev.From.UnixMilli(), prev.To.UnixMilli(), prev.ToInclusive, tc.Previous.From, tc.Previous.To, tc.Previous.ToInclusive)
			}
			info := win.Info()
			if diff := cmp.Diff(tc.Info.From, domain.ISOMillis(info.From)); diff != "" {
				t.Errorf("Info(%v).From mismatch (-want +got):\n%s", raw, diff)
			}
			if diff := cmp.Diff(tc.Info.To, domain.ISOMillis(info.To)); diff != "" {
				t.Errorf("Info(%v).To mismatch (-want +got):\n%s", raw, diff)
			}
		})
	}
}

func TestParsePeriod_CorpusCoversEachOutcome(t *testing.T) {
	c := loadCorpus(t)
	var invalid, valid, unrepresentable int
	for _, tc := range c.Period {
		switch {
		case tc.Issues != nil:
			invalid++
		case tc.WindowError != nil:
			unrepresentable++
		default:
			valid++
		}
	}
	if invalid < 100 || valid < 20 || unrepresentable < 3 {
		t.Errorf("corpus has %d invalid, %d valid, %d unrepresentable cases; a generator change shrank it", invalid, valid, unrepresentable)
	}
}

func TestResolveWindow_Boundaries(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 123_456_789, time.UTC)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	custom := domain.PeriodQuery{Key: domain.PeriodCustom, From: &from, To: &to}

	tests := []struct {
		name          string
		q             domain.PeriodQuery
		in, out       time.Time // a probe inside and outside the window
		wantInclusive bool
	}{
		{"1d includes now", domain.PeriodQuery{Key: domain.Period1d}, now.Truncate(time.Millisecond), now.Add(time.Millisecond), true},
		{"30d includes now", domain.PeriodQuery{Key: domain.Period30d}, now.Truncate(time.Millisecond).Add(-720 * time.Hour), now.Add(-720*time.Hour - time.Millisecond), true},
		{"custom excludes to", custom, to.Add(-time.Millisecond), to, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			win, err := domain.ResolveWindow(tc.q, now)
			if err != nil {
				t.Fatal(err)
			}
			if win.ToInclusive != tc.wantInclusive {
				t.Errorf("ToInclusive = %v, want %v", win.ToInclusive, tc.wantInclusive)
			}
			if !win.Contains(tc.in) {
				t.Errorf("window %v..%v does not contain %v", win.From, win.To, tc.in)
			}
			if win.Contains(tc.out) {
				t.Errorf("window %v..%v contains %v", win.From, win.To, tc.out)
			}
			if prev := win.Previous(); !prev.Contains(win.From.Add(-time.Millisecond)) || prev.Contains(win.From) {
				t.Errorf("previous window %v..%v must end right before %v", prev.From, prev.To, win.From)
			}
		})
	}
	if win, _ := domain.ResolveWindow(domain.PeriodQuery{Key: domain.Period1d}, now); win.To.Nanosecond() != 123_000_000 {
		t.Errorf("preset to = %v, want whole milliseconds", win.To)
	}
}

func TestResolveWindow_Errors(t *testing.T) {
	for name, q := range map[string]domain.PeriodQuery{
		"custom without instants": {Key: domain.PeriodCustom},
		"unknown key":             {Key: "7d"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := domain.ResolveWindow(q, time.Now()); !errors.Is(err, domain.ErrInvalidWindow) {
				t.Errorf("ResolveWindow(%+v) error = %v, want ErrInvalidWindow", q, err)
			}
		})
	}
}

func TestISOMillis(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"usual", time.Date(2026, 1, 2, 3, 4, 5, 6_999_999, time.UTC), "2026-01-02T03:04:05.006Z"},
		{"other zone", time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 5*3600)), "2026-01-01T22:04:05.000Z"},
		{"year 0", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), "0000-01-01T00:00:00.000Z"},
		{"year -1", time.Date(-1, 12, 31, 0, 1, 0, 0, time.UTC), "-000001-12-31T00:01:00.000Z"},
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "+010000-01-01T00:00:00.000Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ISOMillis(tc.t); got != tc.want {
				t.Errorf("ISOMillis(%v) = %q, want %q", tc.t, got, tc.want)
			}
		})
	}
}

func FuzzParsePeriod(f *testing.F) {
	for _, s := range []string{"", "1d", "custom", "2026-01-01T00:00:00Z", "2026-01-01T00:00Z", "2026-02-30T00:00:00Z", "+24:00", "9999-12-31T24:00:00Z"} {
		f.Add(s, s, s)
	}
	f.Fuzz(func(t *testing.T, period, from, to string) {
		q, issues, err := domain.ParsePeriod(domain.RawPeriod{Period: []string{period}, From: []string{from}, To: []string{to}})
		if err != nil || len(issues) > 0 {
			return
		}
		win, err := domain.ResolveWindow(q, time.Unix(1_700_000_000, 0))
		if err != nil {
			t.Fatalf("ParsePeriod accepted %q %q %q but ResolveWindow failed: %v", period, from, to, err)
		}
		if win.To.UnixMilli() <= win.From.UnixMilli() {
			t.Errorf("window %v..%v is empty for %q %q %q", win.From, win.To, period, from, to)
		}
	})
}
