package parity_test

// S31 (AC5055, AC5015, AC5016, EC5030-EC5032, EC5036): the performance context against the Node
// golden performance.json: the period query (zod issues, windows), the metric, trend and usage
// folds, and the dashboard and stats bodies the real use cases and routes produce over the
// golden's emulated tables.
//
// Intentional gaps, each asserted below instead of skipped silently:
//   - AgentMetrics has no minDecisions parameter (the Go context fixes domain.MinDecisions, spec
//     7.11.3): the one Node case with another minimum is compared on every field but small_sample,
//     which is checked against the rule itself.
//   - trendSlices is unexported: the golden's slices {from, width, count} are covered through the
//     labels and values of the trends they produce.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/performance/app"
	"dev-digest-go/backend/internal/performance/domain"
	performancehttp "dev-digest-go/backend/internal/performance/transport/http"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

// ---------------------------------------------------------------------------
// Golden shapes
// ---------------------------------------------------------------------------

type pbPerfWindow struct {
	Key         string `json:"key"`
	From        string `json:"from"`
	To          string `json:"to"`
	ToInclusive bool   `json:"toInclusive"`
}

type pbPerfRun struct {
	AgentID    *string  `json:"agentId"`
	Status     string   `json:"status"`
	Source     string   `json:"source"`
	Model      *string  `json:"model"`
	DurationMs *int     `json:"durationMs"`
	CostUSD    *float64 `json:"costUsd"`
	RanAt      string   `json:"ranAt"`
}

type pbPerfCounts struct {
	AgentID  *string `json:"agentId"`
	Total    int     `json:"total"`
	Accepted int     `json:"accepted"`
	Dismiss  int     `json:"dismissed"`
}

type pbPerfFact struct {
	RanAt    string `json:"ranAt"`
	Severity string `json:"severity"`
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type pbPerfDataset struct {
	Agents []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		WorkspaceID string `json:"workspace_id"`
	} `json:"agents"`
	Runs []struct {
		ID            string   `json:"id"`
		WorkspaceID   string   `json:"workspace_id"`
		AgentID       *string  `json:"agent_id"`
		Status        string   `json:"status"`
		Source        string   `json:"source"`
		Model         *string  `json:"model"`
		DurationMs    *int     `json:"duration_ms"`
		CostUSD       *float64 `json:"cost_usd"`
		RanAt         string   `json:"ran_at"`
		PRID          *string  `json:"pr_id"`
		TokensIn      *int     `json:"tokens_in"`
		TokensOut     *int     `json:"tokens_out"`
		FindingsCount *int     `json:"findings_count"`
	} `json:"runs"`
	Findings []struct {
		RunID       string  `json:"run_id"`
		Severity    string  `json:"severity"`
		Category    string  `json:"category"`
		AcceptedAt  *string `json:"accepted_at"`
		DismissedAt *string `json:"dismissed_at"`
	} `json:"findings"`
	Skills []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"skills"`
	RunSkills []struct {
		RunID   string `json:"run_id"`
		SkillID string `json:"skill_id"`
	} `json:"run_skills"`
	Traces []struct {
		RunID        string          `json:"run_id"`
		MemoryPulled json.RawMessage `json:"memory_pulled"`
	} `json:"traces"`
	Pulls []struct {
		ID     string `json:"id"`
		RepoID string `json:"repo_id"`
		Number int    `json:"number"`
	} `json:"pulls"`
}

type pbPerfGolden struct {
	Constants struct {
		MinDecisions      int            `json:"minDecisions"`
		MaxCustomSpanDays int            `json:"maxCustomSpanDays"`
		PresetHours       map[string]int `json:"presetHours"`
		DeletedAgentsName string         `json:"deletedAgentsName"`
		SeveritySlices    int            `json:"severitySlices"`
		TopUsage          int            `json:"topUsage"`
		HourlyTrendMax    int            `json:"hourlyTrendMaxHours"`
		RunHistoryLimit   int            `json:"runHistoryLimit"`
		MoneyDecimals     int            `json:"moneyDecimals"`
	} `json:"constants"`
	Period struct {
		Queries []struct {
			Name   string         `json:"name"`
			Query  map[string]any `json:"query"`
			OK     bool           `json:"ok"`
			Data   map[string]any `json:"data"`
			Issues []struct {
				Code       string   `json:"code"`
				Path       []string `json:"path"`
				Message    string   `json:"message"`
				Validation string   `json:"validation"`
			} `json:"issues"`
		} `json:"queries"`
		Datetimes []struct {
			Value     string `json:"value"`
			OK        bool   `json:"ok"`
			DateParse *int64 `json:"dateParse"`
		} `json:"datetimes"`
		Windows []struct {
			Name     string         `json:"name"`
			Query    map[string]any `json:"query"`
			Now      string         `json:"now"`
			Window   pbPerfWindow   `json:"window"`
			Previous pbPerfWindow   `json:"previous"`
			Period   struct {
				Key  string `json:"key"`
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"period"`
			Error *struct {
				Name string `json:"name"`
			} `json:"error"`
		} `json:"windows"`
	} `json:"period"`
	Folds struct {
		Metrics []struct {
			Name        string        `json:"name"`
			Runs        []pbPerfRun   `json:"runs"`
			Findings    *pbPerfCounts `json:"findings"`
			MinDecision int           `json:"minDecisions"`
			Result      any           `json:"result"`
		} `json:"metrics"`
		Summaries []struct {
			Name   string       `json:"name"`
			Window pbPerfWindow `json:"window"`
			Input  []struct {
				Agent   *domain.AgentRef `json:"agent"`
				Current struct {
					Runs     []pbPerfRun   `json:"runs"`
					Findings *pbPerfCounts `json:"findings"`
				} `json:"current"`
				Previous struct {
					Runs     []pbPerfRun   `json:"runs"`
					Findings *pbPerfCounts `json:"findings"`
				} `json:"previous"`
			} `json:"input"`
			Rows        []any `json:"rows"`
			Summary     any   `json:"summary"`
			MostActive  any   `json:"mostActive"`
			CostByAgent any   `json:"costByAgent"`
		} `json:"summaries"`
		CostByModel []struct {
			Name   string      `json:"name"`
			Runs   []pbPerfRun `json:"runs"`
			Result any         `json:"result"`
		} `json:"costByModel"`
		Groups []struct {
			Name        string         `json:"name"`
			Known       []string       `json:"known"`
			Runs        []pbPerfRun    `json:"runs"`
			Counts      []pbPerfCounts `json:"counts"`
			GroupedRuns []struct {
				Key  *string `json:"key"`
				Runs int     `json:"runs"`
			} `json:"groupedRuns"`
			GroupedFindings []struct {
				Key      *string `json:"key"`
				Total    int     `json:"total"`
				Accepted int     `json:"accepted"`
				Dismiss  int     `json:"dismissed"`
			} `json:"groupedFindings"`
		} `json:"groups"`
		Trends []struct {
			Name   string       `json:"name"`
			Window pbPerfWindow `json:"window"`
			Runs   []pbPerfRun  `json:"runs"`
			Result any          `json:"result"`
		} `json:"trends"`
		Severity []struct {
			Name   string       `json:"name"`
			Window pbPerfWindow `json:"window"`
			Facts  []pbPerfFact `json:"facts"`
			Result any          `json:"result"`
		} `json:"severity"`
		Usage struct {
			Skills []struct {
				Name  string `json:"name"`
				Facts []struct {
					SkillID string `json:"skillId"`
					Name    string `json:"name"`
					Runs    int    `json:"runs"`
				} `json:"facts"`
				Result any `json:"result"`
			} `json:"skills"`
			Memories []struct {
				Name         string            `json:"name"`
				PulledPerRun []json.RawMessage `json:"pulledPerRun"`
				Result       any               `json:"result"`
			} `json:"memories"`
			Categories []struct {
				Name   string       `json:"name"`
				Facts  []pbPerfFact `json:"facts"`
				Result any          `json:"result"`
			} `json:"categories"`
		} `json:"usage"`
	} `json:"folds"`
	Datasets map[string]pbPerfDataset `json:"datasets"`
	Service  []struct {
		Name                    string            `json:"name"`
		Dataset                 string            `json:"dataset"`
		Call                    string            `json:"call"`
		Agent                   *string           `json:"agent"`
		Query                   map[string]string `json:"query"`
		Now                     string            `json:"now"`
		DashboardRowsEqualStats *bool             `json:"dashboardRowsEqualStats"`
		Status                  int               `json:"status"`
		Body                    any               `json:"body"`
		Error                   *struct {
			Name    string `json:"name"`
			Kind    string `json:"kind"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"service"`
}

func pbPerf(t *testing.T) *pbPerfGolden {
	t.Helper()
	var g pbPerfGolden
	pbLoad(t, "performance", &g)
	return &g
}

// ---------------------------------------------------------------------------
// Golden to domain, domain to golden shape
// ---------------------------------------------------------------------------

func (r pbPerfRun) fact(t testing.TB) domain.RunFact {
	return domain.RunFact{
		AgentID: r.AgentID, Status: domain.RunStatus(r.Status), Source: domain.RunSource(r.Source),
		Model: r.Model, DurationMs: r.DurationMs, CostUSD: r.CostUSD, RanAt: pbTime(t, r.RanAt),
	}
}

func pbPerfFacts(t testing.TB, rs []pbPerfRun) []domain.RunFact {
	var out []domain.RunFact
	for _, r := range rs {
		out = append(out, r.fact(t))
	}
	return out
}

func (c *pbPerfCounts) domain() *domain.FindingCounts {
	if c == nil {
		return nil
	}
	return &domain.FindingCounts{AgentID: c.AgentID, Total: c.Total, Accepted: c.Accepted, Dismissed: c.Dismiss}
}

func (w pbPerfWindow) domain(t testing.TB) domain.PeriodWindow {
	return domain.PeriodWindow{Key: domain.PeriodKey(w.Key), From: pbTime(t, w.From), To: pbTime(t, w.To), ToInclusive: w.ToInclusive}
}

func pbPerfFindingFacts(t testing.TB, fs []pbPerfFact) []domain.FindingFact {
	var out []domain.FindingFact
	for _, f := range fs {
		out = append(out, domain.FindingFact{RanAt: pbTime(t, f.RanAt), Severity: f.Severity, Category: f.Category, Count: f.Count})
	}
	return out
}

func pbIsoPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return pbISO(*t)
}

// pbMetricsShape is domain.Metrics in the shape of the Node body.
func pbMetricsShape(m domain.Metrics) map[string]any {
	return map[string]any{
		"runs": m.Runs, "runs_done": m.RunsDone, "runs_failed": m.RunsFailed, "runs_cancelled": m.RunsCancelled,
		"runs_local": m.RunsLocal, "runs_ci": m.RunsCI, "priced_runs": m.PricedRuns, "unpriced_runs": m.UnpricedRuns,
		"total_cost_usd": m.TotalCostUSD, "avg_cost_usd": m.AvgCostUSD, "avg_duration_ms": m.AvgDurationMs,
		"last_run_at": pbIsoPtr(m.LastRunAt), "findings_total": m.FindingsTotal, "accepted": m.Accepted,
		"dismissed": m.Dismissed, "pending": m.Pending, "decisions": m.Decisions, "accept_rate": m.AcceptRate,
		"small_sample": m.SmallSample,
	}
}

func pbPointsShape(ps []domain.StatPoint) []map[string]any {
	out := []map[string]any{}
	for _, p := range ps {
		out = append(out, map[string]any{"label": p.Label, "value": p.Value})
	}
	return out
}

func pbRowShape(r domain.Row) map[string]any {
	return map[string]any{
		"agent_id": r.AgentID, "agent_name": r.AgentName, "metrics": pbMetricsShape(r.Metrics),
		"previous": pbMetricsShape(r.Previous), "runs_trend": pbPointsShape(r.RunsTrend),
	}
}

func pbMostActiveShape(m *domain.MostActive) any {
	if m == nil {
		return nil
	}
	return map[string]any{"agent_id": m.AgentID, "agent_name": m.AgentName, "runs": m.Runs}
}

func pbSummaryShape(s domain.Summary) map[string]any {
	return map[string]any{
		"runs": s.Runs, "priced_runs": s.PricedRuns, "unpriced_runs": s.UnpricedRuns, "accepted": s.Accepted,
		"dismissed": s.Dismissed, "pending": s.Pending, "decisions": s.Decisions, "total_cost_usd": s.TotalCostUSD,
		"avg_accept_rate": s.AvgAcceptRate, "most_active": pbMostActiveShape(s.MostActive),
	}
}

func pbSegmentsShape(ss []domain.CostSegment) []map[string]any {
	out := []map[string]any{}
	for _, s := range ss {
		out = append(out, map[string]any{"key": s.Key, "label": s.Label, "value": s.Value, "runs": s.Runs})
	}
	return out
}

func pbUsageShape(us []domain.UsageCount) []map[string]any {
	out := []map[string]any{}
	for _, u := range us {
		out = append(out, map[string]any{"key": u.Key, "label": u.Label, "runs": u.Runs})
	}
	return out
}

// ---------------------------------------------------------------------------
// Period
// ---------------------------------------------------------------------------

// pbRawPeriod reads the golden's query object: a string value is one query value, an array a
// repeated key.
func pbRawPeriod(t testing.TB, q map[string]any) domain.RawPeriod {
	t.Helper()
	values := func(key string) []string {
		switch v := q[key].(type) {
		case nil:
			return nil
		case string:
			return []string{v}
		case []any:
			var out []string
			for _, e := range v {
				out = append(out, e.(string))
			}
			return out
		}
		t.Fatalf("query key %s has unexpected value %v", key, q[key])
		return nil
	}
	return domain.RawPeriod{Period: values("period"), From: values("from"), To: values("to")}
}

func TestPerformanceParity_Constants(t *testing.T) {
	g := pbPerf(t)
	k := g.Constants
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"MinDecisions", domain.MinDecisions, k.MinDecisions},
		{"MaxCustomSpanDays", domain.MaxCustomSpanDays, k.MaxCustomSpanDays},
		{"Preset1dHours", domain.Preset1dHours, k.PresetHours["1d"]},
		{"Preset30dHours", domain.Preset30dHours, k.PresetHours["30d"]},
		{"SeveritySlices", domain.SeveritySlices, k.SeveritySlices},
		{"TopUsage", domain.TopUsage, k.TopUsage},
		{"HourlyTrendMaxHours", domain.HourlyTrendMaxHours, k.HourlyTrendMax},
		{"RunHistoryLimit", domain.RunHistoryLimit, k.RunHistoryLimit},
		{"MoneyDecimals", domain.MoneyDecimals, k.MoneyDecimals},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (Node)", c.name, c.got, c.want)
		}
	}
	if domain.DeletedAgentsName != k.DeletedAgentsName {
		t.Errorf("DeletedAgentsName = %q, want %q (Node)", domain.DeletedAgentsName, k.DeletedAgentsName)
	}
}

// TestPerformanceParity_PeriodQueries runs every query of the golden through ParsePeriod: the same
// issues in the same order (zod keeps going after a format error, so period=1d&from=x reports
// both), the same accepted queries, and the 500 the unrepresentable offsets lead to.
func TestPerformanceParity_PeriodQueries(t *testing.T) {
	g := pbPerf(t)
	parsed := map[string]*int64{}
	for _, d := range g.Period.Datetimes {
		parsed[d.Value] = d.DateParse
	}
	if len(g.Period.Queries) < 30 {
		t.Fatalf("golden has %d period queries, a generator change shrank it", len(g.Period.Queries))
	}
	for _, c := range g.Period.Queries {
		t.Run(c.Name, func(t *testing.T) {
			q, issues, err := domain.ParsePeriod(pbRawPeriod(t, c.Query))
			if !c.OK {
				if err != nil {
					t.Fatalf("ParsePeriod(%v) error = %v, want issues", c.Query, err)
				}
				type issue struct{ Code, Path, Message string }
				var want, got []issue
				for _, i := range c.Issues {
					want = append(want, issue{i.Code, strings.Join(i.Path, "."), i.Message})
					if i.Code == "invalid_string" && i.Validation != "datetime" {
						t.Errorf("golden issue %v has validation %q, the test knows only datetime", i, i.Validation)
					}
				}
				for _, i := range issues {
					got = append(got, issue{i.Code, i.Field, i.Message})
				}
				pbDiff(t, fmt.Sprintf("issues of %v", c.Query), pbGeneric(t, want), pbGeneric(t, got))
				return
			}
			// Accepted by zod: the Go query carries the same period and instants, except for the
			// offsets Date cannot read, which Node answered with a 500 when it rendered them.
			unrepresentable := false
			var wantFrom, wantTo int64
			for _, key := range []string{"from", "to"} {
				raw, ok := c.Query[key].(string)
				if !ok {
					continue
				}
				ms, known := parsed[raw]
				if !known { // not one of the table's strings: a plain RFC 3339 instant
					at, perr := time.Parse(time.RFC3339Nano, raw)
					if perr != nil {
						t.Fatalf("query value %q of %s is neither in the golden's datetimes table nor RFC 3339: %v", raw, c.Name, perr)
					}
					v := at.UnixMilli()
					ms = &v
				}
				if ms == nil {
					unrepresentable = true
					continue
				}
				if key == "from" {
					wantFrom = *ms
				} else {
					wantTo = *ms
				}
			}
			if unrepresentable {
				if !errors.Is(err, domain.ErrInvalidWindow) || len(issues) != 0 {
					t.Errorf("ParsePeriod(%v) = (%+v, %v, %v), want ErrInvalidWindow and no issues (Node: 500)", c.Query, q, issues, err)
				}
				return
			}
			if err != nil || len(issues) != 0 {
				t.Fatalf("ParsePeriod(%v) = (issues %v, err %v), want accepted", c.Query, issues, err)
			}
			wantPeriod, _ := c.Data["period"].(string)
			if string(q.Key) != wantPeriod {
				t.Errorf("ParsePeriod(%v).Key = %q, want %q", c.Query, q.Key, wantPeriod)
			}
			if wantPeriod == "custom" {
				if q.From == nil || q.To == nil || q.From.UnixMilli() != wantFrom || q.To.UnixMilli() != wantTo {
					t.Errorf("ParsePeriod(%v) instants = %v..%v, want %d..%d", c.Query, q.From, q.To, wantFrom, wantTo)
				}
			} else if q.From != nil || q.To != nil {
				t.Errorf("ParsePeriod(%v) kept instants %v..%v of a preset", c.Query, q.From, q.To)
			}
		})
	}
}

// TestPerformanceParity_Datetimes feeds each datetime string of the golden as `from` of a custom
// period whose `to` is one millisecond later (when Date.parse reads the string at all): zod's
// format verdict must show as the datetime issue, and Date.parse's value as the parsed instant or,
// for the strings Date cannot read, as the 500 / skipped span check.
func TestPerformanceParity_Datetimes(t *testing.T) {
	g := pbPerf(t)
	if len(g.Period.Datetimes) < 50 {
		t.Fatalf("golden has %d datetimes, a generator change shrank it", len(g.Period.Datetimes))
	}
	for _, d := range g.Period.Datetimes {
		t.Run(d.Value, func(t *testing.T) {
			// A `to` one millisecond after the parsed `from` passes the span checks and shows the
			// parsed instant; outside the years 0000-9999 no such `to` can be written, so only
			// the format verdict is checked there.
			to := "2026-01-01T00:00:00Z"
			inRange := d.DateParse != nil && *d.DateParse >= -62167219200000 && *d.DateParse < 253402300799999
			if inRange {
				to = pbISO(time.UnixMilli(*d.DateParse + 1))
			}
			q, issues, err := domain.ParsePeriod(domain.RawPeriod{Period: []string{"custom"}, From: []string{d.Value}, To: []string{to}})
			hasFormatIssue := false
			for _, i := range issues {
				hasFormatIssue = hasFormatIssue || i.Code == "invalid_string" && i.Field == "from"
			}
			if d.DateParse != nil && !inRange {
				if hasFormatIssue == d.OK {
					t.Errorf("ParsePeriod(from=%q) issues = %v, want a datetime issue on from exactly when zod rejects it (ok=%v)", d.Value, issues, d.OK)
				}
				return
			}
			switch {
			case !d.OK:
				if !hasFormatIssue || len(issues) != 1 || err != nil {
					t.Errorf("ParsePeriod(from=%q) = (issues %v, err %v), want one invalid_string issue on from", d.Value, issues, err)
				}
			case d.DateParse == nil:
				if !errors.Is(err, domain.ErrInvalidWindow) || len(issues) != 0 {
					t.Errorf("ParsePeriod(from=%q) = (issues %v, err %v), want ErrInvalidWindow", d.Value, issues, err)
				}
			default:
				if err != nil || len(issues) != 0 || q.From == nil || q.From.UnixMilli() != *d.DateParse {
					t.Errorf("ParsePeriod(from=%q) = (%+v, issues %v, err %v), want from = %d", d.Value, q, issues, err, *d.DateParse)
				}
			}
		})
	}
}

func TestPerformanceParity_Windows(t *testing.T) {
	g := pbPerf(t)
	for _, c := range g.Period.Windows {
		t.Run(c.Name, func(t *testing.T) {
			now := pbTime(t, c.Now)
			q, issues, err := domain.ParsePeriod(pbRawPeriod(t, c.Query))
			if len(issues) != 0 {
				t.Fatalf("ParsePeriod(%v) issues = %v", c.Query, issues)
			}
			if c.Error != nil {
				if err == nil {
					win, werr := domain.ResolveWindow(q, now)
					t.Fatalf("ParsePeriod(%v) accepted a window Node failed on (%s): %+v, %v", c.Query, c.Error.Name, win, werr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePeriod(%v) error = %v", c.Query, err)
			}
			win, err := domain.ResolveWindow(q, now)
			if err != nil {
				t.Fatalf("ResolveWindow(%v) error = %v", c.Query, err)
			}
			shape := func(w domain.PeriodWindow) pbPerfWindow {
				return pbPerfWindow{Key: string(w.Key), From: pbISO(w.From), To: pbISO(w.To), ToInclusive: w.ToInclusive}
			}
			if got := shape(win); got != c.Window {
				t.Errorf("ResolveWindow(%v, %s) = %+v, want %+v", c.Query, c.Now, got, c.Window)
			}
			if got := shape(win.Previous()); got != c.Previous {
				t.Errorf("Previous of %+v = %+v, want %+v", win, got, c.Previous)
			}
			info := win.Info()
			if got := (struct{ Key, From, To string }{string(info.Key), domain.ISOMillis(info.From), domain.ISOMillis(info.To)}); got != struct{ Key, From, To string }(c.Period) {
				t.Errorf("Info of %+v = %+v, want %+v", win, got, c.Period)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Folds
// ---------------------------------------------------------------------------

func TestPerformanceParity_Metrics(t *testing.T) {
	g := pbPerf(t)
	for _, c := range g.Folds.Metrics {
		t.Run(c.Name, func(t *testing.T) {
			m := domain.AgentMetrics(pbPerfFacts(t, c.Runs), c.Findings.domain())
			got := pbGeneric(t, pbMetricsShape(m)).(map[string]any)
			want := c.Result.(map[string]any)
			if c.MinDecision != domain.MinDecisions {
				// The Go context has the one minimum: check the rule the golden's flag follows.
				decisions := want["decisions"].(float64)
				if wantSmall := decisions > 0 && decisions < float64(c.MinDecision); want["small_sample"] != wantSmall {
					t.Fatalf("golden small_sample = %v for %v decisions and minimum %d", want["small_sample"], decisions, c.MinDecision)
				}
				delete(got, "small_sample")
				delete(want, "small_sample")
			}
			pbDiff(t, "AgentMetrics("+c.Name+")", want, got)
		})
	}
}

func TestPerformanceParity_Summaries(t *testing.T) {
	g := pbPerf(t)
	for _, c := range g.Folds.Summaries {
		t.Run(c.Name, func(t *testing.T) {
			win := c.Window.domain(t)
			rows := []domain.Row{}
			var gotRows []any
			for _, in := range c.Input {
				cur := domain.AgentFacts{Runs: pbPerfFacts(t, in.Current.Runs), Findings: in.Current.Findings.domain()}
				prev := domain.AgentFacts{Runs: pbPerfFacts(t, in.Previous.Runs), Findings: in.Previous.Findings.domain()}
				row := domain.BuildRow(in.Agent, cur, prev, win)
				rows = append(rows, row)
				gotRows = append(gotRows, pbGeneric(t, pbRowShape(row)))
			}
			if len(c.Rows) != len(gotRows) {
				t.Fatalf("BuildRow made %d rows, want %d", len(gotRows), len(c.Rows))
			}
			for i := range c.Rows {
				pbDiff(t, fmt.Sprintf("BuildRow of input %d", i), c.Rows[i], gotRows[i])
			}
			pbDiff(t, "Summarize", c.Summary, pbGeneric(t, pbSummaryShape(domain.Summarize(rows))))
			pbDiff(t, "FindMostActive", c.MostActive, pbGeneric(t, pbMostActiveShape(domain.FindMostActive(rows))))
			pbDiff(t, "CostByAgent", c.CostByAgent, pbGeneric(t, pbSegmentsShape(domain.CostByAgent(rows))))
		})
	}
}

func TestPerformanceParity_CostByModel(t *testing.T) {
	g := pbPerf(t)
	for _, c := range g.Folds.CostByModel {
		t.Run(c.Name, func(t *testing.T) {
			got := domain.CostByModel(pbPerfFacts(t, c.Runs))
			pbDiff(t, "CostByModel", c.Result, pbGeneric(t, pbSegmentsShape(got)))
		})
	}
}

// TestPerformanceParity_Groups checks the grouping of runs and finding counts by agent: unknown
// and missing agents fold into the deleted-agents key (nil).
func TestPerformanceParity_Groups(t *testing.T) {
	g := pbPerf(t)
	for _, c := range g.Folds.Groups {
		t.Run(c.Name, func(t *testing.T) {
			known := map[string]bool{}
			for _, k := range c.Known {
				known[k] = true
			}
			var counts []domain.FindingCounts
			for _, k := range c.Counts {
				counts = append(counts, *k.domain())
			}
			grouped := domain.GroupFacts(pbPerfFacts(t, c.Runs), counts, known)

			wantRuns := map[string]int{}
			for _, r := range c.GroupedRuns {
				wantRuns[pbKey(r.Key)] = r.Runs
			}
			wantFindings := map[string]domain.FindingCounts{}
			for _, f := range c.GroupedFindings {
				wantFindings[pbKey(f.Key)] = domain.FindingCounts{Total: f.Total, Accepted: f.Accepted, Dismissed: f.Dismiss}
			}
			keys := append([]*string{nil}, pbPtrs(c.Known)...)
			for _, key := range keys {
				facts := grouped.Facts(key)
				name := pbKey(key)
				if len(facts.Runs) != wantRuns[name] {
					t.Errorf("Facts(%q) has %d runs, want %d", name, len(facts.Runs), wantRuns[name])
				}
				want, hasWant := wantFindings[name]
				switch {
				case facts.Findings == nil && hasWant:
					t.Errorf("Facts(%q).Findings = nil, want %+v", name, want)
				case facts.Findings != nil && !hasWant:
					t.Errorf("Facts(%q).Findings = %+v, want none", name, *facts.Findings)
				case facts.Findings != nil && (facts.Findings.Total != want.Total || facts.Findings.Accepted != want.Accepted || facts.Findings.Dismissed != want.Dismissed):
					t.Errorf("Facts(%q).Findings = %+v, want %+v", name, *facts.Findings, want)
				}
			}
			if hasDeleted := wantRuns[""] > 0; grouped.HasDeleted() != hasDeleted {
				t.Errorf("HasDeleted() = %v, want %v", grouped.HasDeleted(), hasDeleted)
			}
		})
	}
}

// pbKey is a group key as a map key; the deleted-agents key (nil) is "".
func pbKey(k *string) string {
	if k == nil {
		return ""
	}
	return *k
}

func pbPtrs(ss []string) []*string {
	out := make([]*string, len(ss))
	for i := range ss {
		out[i] = &ss[i]
	}
	return out
}

func TestPerformanceParity_Trends(t *testing.T) {
	g := pbPerf(t)
	for _, c := range g.Folds.Trends {
		t.Run(c.Name, func(t *testing.T) {
			got := domain.RunsTrend(pbPerfFacts(t, c.Runs), c.Window.domain(t))
			pbDiff(t, "RunsTrend", c.Result, pbGeneric(t, pbPointsShape(got)))
		})
	}
}

func TestPerformanceParity_FindingsBySeverity(t *testing.T) {
	g := pbPerf(t)
	for _, c := range g.Folds.Severity {
		t.Run(c.Name, func(t *testing.T) {
			got := []map[string]any{}
			for _, b := range domain.FindingsBySeverity(pbPerfFindingFacts(t, c.Facts), c.Window.domain(t)) {
				got = append(got, map[string]any{"from": b.From, "critical": b.Critical, "warning": b.Warning, "suggestion": b.Suggestion})
			}
			pbDiff(t, "FindingsBySeverity", c.Result, pbGeneric(t, got))
		})
	}
}

func TestPerformanceParity_Usage(t *testing.T) {
	g := pbPerf(t)
	t.Run("skills", func(t *testing.T) {
		for _, c := range g.Folds.Usage.Skills {
			var facts []domain.SkillFact
			for _, f := range c.Facts {
				facts = append(facts, domain.SkillFact{SkillID: f.SkillID, Name: f.Name, Runs: f.Runs})
			}
			pbDiff(t, "TopSkills("+c.Name+")", c.Result, pbGeneric(t, pbUsageShape(domain.TopSkills(facts))))
		}
	})
	t.Run("memories", func(t *testing.T) {
		for _, c := range g.Folds.Usage.Memories {
			pbDiff(t, "TopMemories("+c.Name+")", c.Result, pbGeneric(t, pbUsageShape(domain.TopMemories(c.PulledPerRun))))
		}
	})
	t.Run("categories", func(t *testing.T) {
		for _, c := range g.Folds.Usage.Categories {
			got := []map[string]any{}
			for _, k := range domain.FindingsByCategory(pbPerfFindingFacts(t, c.Facts)) {
				got = append(got, map[string]any{"category": k.Category, "count": k.Count})
			}
			pbDiff(t, "FindingsByCategory("+c.Name+")", c.Result, pbGeneric(t, got))
		}
	})
}

// ---------------------------------------------------------------------------
// Service over the golden's emulated tables
// ---------------------------------------------------------------------------

// pbPerfReader is app.Reader over the tables of one dataset. It emulates the repository SQL the
// way the generator does (README: counted-run filter, window bounds, grouping): the Go
// repository's own tests seed the same tables against Postgres.
type pbPerfReader struct {
	ws string
	ds *pbPerfDataset
}

var _ app.Reader = pbPerfReader{}

func (r pbPerfReader) counted(win domain.PeriodWindow, agentID string) []int {
	var idx []int
	for i, run := range r.ds.Runs {
		at, err := time.Parse(time.RFC3339Nano, run.RanAt)
		if err != nil {
			panic(err) // a golden defect, not a failure of the code under test
		}
		inWindow := !at.Before(win.From) && (win.ToInclusive && !at.After(win.To) || !win.ToInclusive && at.Before(win.To))
		counted := (run.Status == "done" || run.Status == "failed" || run.Status == "cancelled") && (run.Source == "local" || run.Source == "ci")
		if run.WorkspaceID == r.ws && counted && inWindow && (agentID == "" || run.AgentID != nil && *run.AgentID == agentID) {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := r.ds.Runs[idx[a]], r.ds.Runs[idx[b]]
		ta, _ := time.Parse(time.RFC3339Nano, ra.RanAt)
		tb, _ := time.Parse(time.RFC3339Nano, rb.RanAt)
		if !ta.Equal(tb) {
			return ta.Before(tb)
		}
		return ra.ID < rb.ID
	})
	return idx
}

func (r pbPerfReader) ListAgents(_ context.Context, ws string) ([]domain.AgentRef, error) {
	var out []domain.AgentRef
	for _, a := range r.ds.Agents {
		if a.WorkspaceID == ws {
			out = append(out, domain.AgentRef{ID: a.ID, Name: a.Name})
		}
	}
	return out, nil
}

func (r pbPerfReader) FindAgent(_ context.Context, ws, id string) (domain.AgentRef, error) {
	for _, a := range r.ds.Agents {
		if a.WorkspaceID == ws && a.ID == id {
			return domain.AgentRef{ID: a.ID, Name: a.Name}, nil
		}
	}
	return domain.AgentRef{}, domain.ErrNotFound
}

func (r pbPerfReader) CountedRuns(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.RunFact, error) {
	var out []domain.RunFact
	for _, i := range r.counted(win, agentID) {
		run := r.ds.Runs[i]
		at, _ := time.Parse(time.RFC3339Nano, run.RanAt)
		out = append(out, domain.RunFact{
			AgentID: run.AgentID, Status: domain.RunStatus(run.Status), Source: domain.RunSource(run.Source),
			Model: run.Model, DurationMs: run.DurationMs, CostUSD: run.CostUSD, RanAt: at.UTC(),
		})
	}
	return out, nil
}

// findings lists the findings of the counted runs with their run, in table order.
func (r pbPerfReader) findings(win domain.PeriodWindow, agentID string) []struct{ run, finding int } {
	runOf := map[string]int{}
	for _, i := range r.counted(win, agentID) {
		runOf[r.ds.Runs[i].ID] = i
	}
	var out []struct{ run, finding int }
	for j, f := range r.ds.Findings {
		if i, ok := runOf[f.RunID]; ok {
			out = append(out, struct{ run, finding int }{i, j})
		}
	}
	return out
}

func (r pbPerfReader) FindingCounts(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.FindingCounts, error) {
	var out []domain.FindingCounts
	index := map[string]int{}
	for _, rf := range r.findings(win, agentID) {
		run, f := r.ds.Runs[rf.run], r.ds.Findings[rf.finding]
		key := "\x00nil"
		if run.AgentID != nil {
			key = *run.AgentID
		}
		at, ok := index[key]
		if !ok {
			at = len(out)
			index[key] = at
			out = append(out, domain.FindingCounts{AgentID: run.AgentID})
		}
		out[at].Total++
		if f.AcceptedAt != nil {
			out[at].Accepted++
		}
		if f.DismissedAt != nil && f.AcceptedAt == nil {
			out[at].Dismissed++
		}
	}
	return out, nil
}

func (r pbPerfReader) FindingFacts(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.FindingFact, error) {
	var out []domain.FindingFact
	index := map[string]int{}
	for _, rf := range r.findings(win, agentID) {
		run, f := r.ds.Runs[rf.run], r.ds.Findings[rf.finding]
		key := run.ID + "\x00" + f.Severity + "\x00" + f.Category
		at, ok := index[key]
		if !ok {
			ranAt, _ := time.Parse(time.RFC3339Nano, run.RanAt)
			at = len(out)
			index[key] = at
			out = append(out, domain.FindingFact{RanAt: ranAt.UTC(), Severity: f.Severity, Category: f.Category})
		}
		out[at].Count++
	}
	return out, nil
}

func (r pbPerfReader) SkillUsage(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.SkillFact, error) {
	ids := map[string]bool{}
	for _, i := range r.counted(win, agentID) {
		ids[r.ds.Runs[i].ID] = true
	}
	var order []string
	perSkill := map[string]map[string]bool{}
	for _, rs := range r.ds.RunSkills {
		if !ids[rs.RunID] {
			continue
		}
		if perSkill[rs.SkillID] == nil {
			perSkill[rs.SkillID] = map[string]bool{}
			order = append(order, rs.SkillID)
		}
		perSkill[rs.SkillID][rs.RunID] = true
	}
	var out []domain.SkillFact
	for _, id := range order {
		name := ""
		for _, s := range r.ds.Skills {
			if s.ID == id {
				name = s.Name
			}
		}
		out = append(out, domain.SkillFact{SkillID: id, Name: name, Runs: len(perSkill[id])})
	}
	return out, nil
}

func (r pbPerfReader) MemoryPulled(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]json.RawMessage, error) {
	ids := map[string]bool{}
	for _, i := range r.counted(win, agentID) {
		ids[r.ds.Runs[i].ID] = true
	}
	var out []json.RawMessage
	for _, tr := range r.ds.Traces {
		if !ids[tr.RunID] {
			continue
		}
		if len(tr.MemoryPulled) == 0 {
			out = append(out, json.RawMessage("null"))
		} else {
			out = append(out, tr.MemoryPulled)
		}
	}
	return out, nil
}

func (r pbPerfReader) RunHistory(_ context.Context, _ string, win domain.PeriodWindow, agentID string, limit int) ([]domain.RunHistoryItem, error) {
	idx := r.counted(win, agentID)
	for a, b := 0, len(idx)-1; a < b; a, b = a+1, b-1 { // newest first: ran_at desc, id desc
		idx[a], idx[b] = idx[b], idx[a]
	}
	if len(idx) > limit {
		idx = idx[:limit]
	}
	var out []domain.RunHistoryItem
	for _, i := range idx {
		run := r.ds.Runs[i]
		at, _ := time.Parse(time.RFC3339Nano, run.RanAt)
		item := domain.RunHistoryItem{
			RunID: run.ID, RanAt: at.UTC(), Status: domain.RunStatus(run.Status), Source: domain.RunSource(run.Source),
			CostUSD: run.CostUSD, FindingsCount: run.FindingsCount,
		}
		if run.TokensIn != nil || run.TokensOut != nil {
			n := 0
			if run.TokensIn != nil {
				n += *run.TokensIn
			}
			if run.TokensOut != nil {
				n += *run.TokensOut
			}
			item.Tokens = &n
		}
		for _, p := range r.ds.Pulls {
			if run.PRID != nil && p.ID == *run.PRID {
				item.PR = &domain.RunPR{ID: p.ID, RepoID: p.RepoID, Number: p.Number}
			}
		}
		for _, tr := range r.ds.Traces {
			if tr.RunID == run.ID {
				item.HasTrace = true
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// pbPerfIDs maps the golden's agent ids ("a-alpha") to UUIDs: the routes require UUID path ids.
type pbPerfIDs struct {
	toUUID   map[string]string
	fromUUID map[string]string
}

func newPbPerfIDs(names ...string) *pbPerfIDs {
	m := &pbPerfIDs{toUUID: map[string]string{}, fromUUID: map[string]string{}}
	for _, n := range names {
		if _, ok := m.toUUID[n]; !ok {
			u := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(m.toUUID)+1)
			m.toUUID[n], m.fromUUID[u] = u, n
		}
	}
	return m
}

// pbPerfAPI serves the real use cases under UUID agent ids.
type pbPerfAPI struct {
	svc *app.Service
	ids *pbPerfIDs
}

func (a pbPerfAPI) Dashboard(ctx context.Context, ws string, q domain.PeriodQuery) (domain.AgentPerf, error) {
	return a.svc.Dashboard(ctx, ws, q)
}

func (a pbPerfAPI) Stats(ctx context.Context, ws, id string, q domain.PeriodQuery) (domain.AgentStats, error) {
	if golden, ok := a.ids.fromUUID[id]; ok {
		id = golden
	}
	return a.svc.Stats(ctx, ws, id, q)
}

type pbTenants struct{ ws string }

func (p pbTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: p.ws}, nil
}

func pbGet(t *testing.T, srv *httptest.Server, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of GET %s: %v", path, err)
	}
	return resp.StatusCode, body
}

// TestPerformanceParity_Service serves the dashboard and stats of every golden case through the
// real use cases and routes, over the case's tables, and compares the decoded bodies (AC5015,
// AC5016): the 404 of an unknown agent and the 500 of an unrepresentable offset included. Every
// dashboard row must equal the same agent's stats metrics, previous and runs_trend (AC10).
func TestPerformanceParity_Service(t *testing.T) {
	g := pbPerf(t)
	if len(g.Service) < 20 {
		t.Fatalf("golden has %d service cases, a generator change shrank it", len(g.Service))
	}
	const workspace = "ws-1"
	for _, c := range g.Service {
		t.Run(c.Name, func(t *testing.T) {
			ds, ok := g.Datasets[c.Dataset]
			if !ok {
				t.Fatalf("dataset %q is not in the golden", c.Dataset)
			}
			var names []string
			for _, a := range ds.Agents {
				names = append(names, a.ID)
			}
			if c.Agent != nil {
				names = append(names, *c.Agent)
			}
			ids := newPbPerfIDs(names...)
			now := pbTime(t, c.Now)
			svc := app.NewService(pbPerfReader{ws: workspace, ds: &ds}, func() time.Time { return now })

			e := httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), AllowedHosts: []string{"127.0.0.1"}})
			performancehttp.AddRoutes(e, pbPerfAPI{svc: svc, ids: ids}, pbTenants{ws: workspace})
			srv := httptest.NewServer(e)
			t.Cleanup(srv.Close)

			qs := url.Values{}
			for k, v := range c.Query {
				qs.Set(k, v)
			}
			query := ""
			if len(qs) > 0 {
				query = "?" + qs.Encode()
			}
			path := "/agents/performance" + query
			if c.Call == "stats" {
				path = "/agents/" + ids.toUUID[*c.Agent] + "/stats" + query
			}
			status, body := pbGet(t, srv, path)

			if c.Error != nil {
				wantStatus := map[string]int{"not_found": http.StatusNotFound}[c.Error.Kind]
				if c.Error.Kind == "" { // RangeError: no application error, Node answered 500
					wantStatus = http.StatusInternalServerError
				}
				if status != wantStatus {
					t.Fatalf("GET %s = %d %s, want %d (Node error %s)", path, status, body, wantStatus, c.Error.Name)
				}
				if c.Error.Code != "" {
					var env struct {
						Error struct{ Code, Message string }
					}
					if err := json.Unmarshal(body, &env); err != nil || env.Error.Code != c.Error.Code || env.Error.Message != c.Error.Message {
						t.Errorf("GET %s body = %s, want code %q message %q", path, body, c.Error.Code, c.Error.Message)
					}
				}
				return
			}
			if status != c.Status {
				t.Fatalf("GET %s = %d %s, want %d", path, status, body, c.Status)
			}
			got := pbDecode(t, body)
			pbDiff(t, "GET "+path, c.Body, got)

			if c.DashboardRowsEqualStats != nil {
				rows := got.(map[string]any)["agents"].([]any)
				for _, row := range rows {
					row := row.(map[string]any)
					id, ok := row["agent_id"].(string)
					if !ok {
						continue // the deleted-agents row has no stats route
					}
					st, sbody := pbGet(t, srv, "/agents/"+ids.toUUID[id]+"/stats"+query)
					if st != http.StatusOK {
						t.Fatalf("stats of %s = %d %s", id, st, sbody)
					}
					stats := pbDecode(t, sbody).(map[string]any)
					for _, key := range []string{"metrics", "previous", "runs_trend"} {
						pbDiff(t, fmt.Sprintf("dashboard row %s %s against its stats", id, key), row[key], stats[key])
					}
				}
			}
		})
	}
}
