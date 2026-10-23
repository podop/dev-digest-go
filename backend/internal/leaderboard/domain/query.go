package domain

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Patterns of the accepted query values. They are deliberately narrower than what the
// standard library tolerates: a tz name is a relative IANA path of letters, digits, '_', '+'
// and '-' (no dots, no leading or doubled slash), so nothing but a zone name ever reaches the
// zone database lookup.
var (
	tzPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+)*$`)
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// ParseQuery validates the query string of GET /leaderboard (spec FR6) and applies the
// defaults: period=week, tz=UTC, repo=all. It never returns an error for bad input but a list
// of Issues, in a fixed order: period, tz, repo, then every extra key sorted. Only the keys
// period, tz and repo are accepted, each at most once with a non-empty value; a repeated key
// is one Issue for that key. An empty value is invalid, not "absent". The repo UUID is
// returned in lower case. tz must match an IANA zone name, be at most MaxTZLen bytes, must not
// be "Local" (the server's own zone is never a request's zone) and must be known to the zone
// database. When the list is not empty the Query must not be used. Messages never echo a
// value.
func ParseQuery(v url.Values) (Query, []Issue) {
	var (
		q      = Query{Period: PeriodWeek, TZ: DefaultTZ, Location: time.UTC, Repo: RepoAll}
		issues []Issue
	)
	bad := func(param, msg string) { issues = append(issues, Issue{Param: param, Message: msg}) }

	if raw, ok, msg := single(v, ParamPeriod); msg != "" {
		bad(ParamPeriod, msg)
		q.Period = ""
	} else if ok {
		switch p := PeriodKey(raw); p {
		case PeriodWeek, PeriodLastWeek, Period30d:
			q.Period = p
		default:
			bad(ParamPeriod, "must be one of week, last_week, 30d")
			q.Period = ""
		}
	}

	if raw, ok, msg := single(v, ParamTZ); msg != "" {
		bad(ParamTZ, msg)
		q.TZ, q.Location = "", nil
	} else if ok {
		loc, msg := loadZone(raw)
		if msg != "" {
			bad(ParamTZ, msg)
			q.TZ, q.Location = "", nil
		} else {
			q.TZ, q.Location = raw, loc
		}
	}

	if raw, ok, msg := single(v, ParamRepo); msg != "" {
		bad(ParamRepo, msg)
		q.Repo = ""
	} else if ok {
		switch {
		case raw == RepoAll:
			q.Repo = RepoAll
		case uuidPattern.MatchString(raw):
			q.Repo = strings.ToLower(raw)
		default:
			bad(ParamRepo, `must be "all" or a repository id (UUID)`)
			q.Repo = ""
		}
	}

	var extra []string
	for k := range v {
		if k != ParamPeriod && k != ParamTZ && k != ParamRepo {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		bad(k, "unknown query parameter")
	}
	return q, issues
}

// single returns the only value of key. ok is false when the key is absent (use the default);
// msg is set when the key is repeated or its value is empty.
func single(v url.Values, key string) (val string, ok bool, msg string) {
	vals, present := v[key]
	if !present {
		return "", false, ""
	}
	if len(vals) != 1 {
		return "", false, "must be given exactly once"
	}
	if vals[0] == "" {
		return "", false, "must not be empty"
	}
	return vals[0], true, ""
}

// loadZone checks the zone name strictly before it asks the zone database.
func loadZone(name string) (*time.Location, string) {
	if len(name) > MaxTZLen {
		return nil, "must be at most 64 characters"
	}
	const unknown = "must be a known IANA time zone name"
	if !tzPattern.MatchString(name) || strings.EqualFold(name, "Local") {
		return nil, unknown
	}
	loc, err := time.LoadLocation(name)
	if err != nil || loc == time.Local {
		return nil, unknown
	}
	return loc, ""
}
