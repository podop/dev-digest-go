package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// RawPeriod is the period query as received: the values of each query key in order. No value
// means the key is absent; more than one means it was repeated (a type error, as in Node).
type RawPeriod struct {
	Period, From, To []string
}

// PeriodIssue is one problem of the period query, in the form of a Zod issue: Code is the Zod
// code (invalid_type, invalid_enum_value, invalid_string, custom), Field the query key the issue
// is located at.
type PeriodIssue struct {
	Code, Field, Message string
}

// The Zod issue codes ParsePeriod reports.
const (
	IssueInvalidType = "invalid_type"
	IssueInvalidEnum = "invalid_enum_value"
	IssueInvalidStr  = "invalid_string"
	IssueCustom      = "custom"
)

const (
	enumList        = "'1d' | '30d' | 'custom'"
	invalidDatetime = "Invalid datetime"
)

// instantPattern is the regular expression behind zod 3.25.76 z.string().datetime({offset: true}):
// a calendar-checked date (leap years included), T, hh:mm with optional seconds and any number of
// fraction digits, then Z or a +hh:mm / +hhmm offset. RE2's \d is ASCII like JavaScript's and $
// matches only at the end of the text.
var instantPattern = regexp.MustCompile(`^((\d\d[2468][048]|\d\d[13579][26]|\d\d0[48]|[02468][048]00|[13579][26]00)-02-29|\d{4}-((0[13578]|1[02])-(0[1-9]|[12]\d|3[01])|(0[469]|11)-(0[1-9]|[12]\d|30)|(02)-(0[1-9]|1\d|2[0-8])))T([01]\d|2[0-3]):[0-5]\d(:[0-5]\d(\.\d+)?)?(Z|([+-]\d{2}:?\d{2}))$`)

// ParsePeriod validates the period query of both performance routes exactly as Node's route
// schema did and returns the query.
//
// Zod parses period, from and to in that order and keeps going after a failure: a wrong type or
// value of any of them (an unknown period, a repeated key) ends validation with the issues
// collected so far; a malformed instant is only "dirty", so the cross-field rules still run on
// the raw strings and may add their own issues (period=1d&from=garbage reports both). The
// returned error is ErrInvalidWindow for a well-formed instant JavaScript cannot represent
// (+24:00): Node accepted it and failed later with a 500.
func ParsePeriod(raw RawPeriod) (PeriodQuery, []PeriodIssue, error) {
	var issues []PeriodIssue
	aborted := false

	key := Period30d
	switch {
	case len(raw.Period) > 1:
		issues = append(issues, PeriodIssue{IssueInvalidType, "period", "Expected " + enumList + ", received array"})
		aborted = true
	case len(raw.Period) == 1:
		switch k := PeriodKey(raw.Period[0]); k {
		case Period1d, Period30d, PeriodCustom:
			key = k
		default:
			issues = append(issues, PeriodIssue{IssueInvalidEnum, "period",
				"Invalid enum value. Expected " + enumList + ", received '" + raw.Period[0] + "'"})
			aborted = true
		}
	}

	var from, to *string
	for _, f := range []struct {
		name   string
		values []string
		out    **string
	}{{"from", raw.From, &from}, {"to", raw.To, &to}} {
		switch {
		case len(f.values) > 1:
			issues = append(issues, PeriodIssue{IssueInvalidType, f.name, "Expected string, received array"})
			aborted = true
		case len(f.values) == 1:
			v := f.values[0]
			*f.out = &v
			if !instantPattern.MatchString(v) {
				issues = append(issues, PeriodIssue{IssueInvalidStr, f.name, invalidDatetime})
			}
		}
	}
	if aborted {
		return PeriodQuery{}, issues, nil
	}

	issues = append(issues, crossFieldIssues(key, from, to)...)
	if len(issues) > 0 {
		return PeriodQuery{}, issues, nil
	}
	q := PeriodQuery{Key: key}
	if key == PeriodCustom {
		f, okF := parseInstant(*from)
		t, okT := parseInstant(*to)
		if !okF || !okT {
			return PeriodQuery{}, nil, fmt.Errorf("period instant: %w", ErrInvalidWindow)
		}
		q.From, q.To = &f, &t
	}
	return q, nil, nil
}

// crossFieldIssues is the refinement of the route schema.
func crossFieldIssues(key PeriodKey, from, to *string) []PeriodIssue {
	var issues []PeriodIssue
	if key != PeriodCustom {
		if from != nil {
			issues = append(issues, PeriodIssue{IssueCustom, "from", "from is only allowed with period=custom"})
		}
		if to != nil {
			issues = append(issues, PeriodIssue{IssueCustom, "to", "to is only allowed with period=custom"})
		}
		return issues
	}
	if from == nil {
		issues = append(issues, PeriodIssue{IssueCustom, "from", "from is required with period=custom"})
	}
	if to == nil {
		issues = append(issues, PeriodIssue{IssueCustom, "to", "to is required with period=custom"})
	}
	if from == nil || to == nil {
		return issues
	}
	// Date.parse of the raw strings; a NaN (unparseable) span compares false with everything.
	f, okF := parseJSDate(*from)
	t, okT := parseJSDate(*to)
	if !okF || !okT {
		return nil
	}
	switch span := t - f; {
	case span <= 0:
		issues = append(issues, PeriodIssue{IssueCustom, "to", "to must be after from"})
	case span > MaxCustomSpanDays*msPerDay:
		issues = append(issues, PeriodIssue{IssueCustom, "to", fmt.Sprintf("The range can be at most %d days", MaxCustomSpanDays)})
	}
	return issues
}

// parseInstant converts a string that matched instantPattern, truncated to milliseconds.
func parseInstant(s string) (time.Time, bool) {
	ms, ok := parseJSDate(s)
	if !ok {
		return time.Time{}, false
	}
	return msTime(ms), true
}

// parseJSDate is Date.parse (milliseconds since the epoch) for the ISO-8601 shapes V8 accepts:
// [+-]yyyyyy or yyyy, optional -mm and -dd (the day may be 1..31 in any month and rolls over),
// then optionally T, t or one space and hh:mm[:ss[.f+]] (hour 24 only for exactly midnight), then
// Z, z or a +-hh:mm / +-hhmm offset (hours <= 23, minutes <= 59); a time without a zone is UTC
// here (the servers run in UTC). The fraction is truncated to milliseconds. Free-form legacy
// dates ("Jan 1 2026") are not parsed: ok is false for them, where V8 would answer a value.
func parseJSDate(s string) (ms int64, ok bool) {
	p := scanner{s: s}
	var year int
	if p.peek('+') || p.peek('-') {
		neg := p.peek('-')
		p.i++
		y, ok := p.digits(6)
		if !ok || (neg && y == 0) {
			return 0, false
		}
		year = y
		if neg {
			year = -y
		}
	} else if y, ok := p.digits(4); ok {
		year = y
	} else {
		return 0, false
	}
	month, day := 1, 1
	if p.eat('-') {
		var ok bool
		if month, ok = p.digits(2); !ok || month < 1 || month > 12 {
			return 0, false
		}
		if p.eat('-') {
			if day, ok = p.digits(2); !ok || day < 1 || day > 31 {
				return 0, false
			}
		}
	}
	var hour, minute, second, frac int
	hasTime := false
	if p.eat('T') || p.eat('t') || p.eat(' ') {
		hasTime = true
		var ok bool
		if hour, ok = p.digits(2); !ok || hour > 24 || !p.eat(':') {
			return 0, false
		}
		if minute, ok = p.digits(2); !ok || minute > 59 {
			return 0, false
		}
		if p.eat(':') {
			if second, ok = p.digits(2); !ok || second > 59 {
				return 0, false
			}
			if p.eat('.') {
				if frac, ok = p.fraction(); !ok {
					return 0, false
				}
			}
		}
		if hour == 24 && (minute != 0 || second != 0 || frac != 0) {
			return 0, false
		}
	}
	offsetMin := 0
	switch {
	case p.eat('Z') || p.eat('z'):
	case hasTime && (p.peek('+') || p.peek('-')):
		neg := p.peek('-')
		p.i++
		hh, ok := p.digits(2)
		if !ok || hh > 23 {
			return 0, false
		}
		p.eat(':')
		mm, ok := p.digits(2)
		if !ok || mm > 59 {
			return 0, false
		}
		offsetMin = hh*60 + mm
		if neg {
			offsetMin = -offsetMin
		}
	}
	if p.i != len(s) {
		return 0, false
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, frac*int(time.Millisecond), time.UTC)
	ms = t.UnixMilli() - int64(offsetMin)*60_000
	if ms > maxTimeMs || ms < -maxTimeMs {
		return 0, false
	}
	return ms, true
}

// scanner is a cursor over an ASCII-digit grammar.
type scanner struct {
	s string
	i int
}

func (p *scanner) peek(c byte) bool { return p.i < len(p.s) && p.s[p.i] == c }

func (p *scanner) eat(c byte) bool {
	if p.peek(c) {
		p.i++
		return true
	}
	return false
}

// digits reads exactly n ASCII digits.
func (p *scanner) digits(n int) (int, bool) {
	if p.i+n > len(p.s) {
		return 0, false
	}
	v := 0
	for _, c := range []byte(p.s[p.i : p.i+n]) {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int(c-'0')
	}
	p.i += n
	return v, true
}

// fraction reads one or more digits and returns the first three as milliseconds.
func (p *scanner) fraction() (int, bool) {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	if p.i == start {
		return 0, false
	}
	digits := p.s[start:p.i]
	for len(digits) < 3 {
		digits += "0"
	}
	ms, _ := strconv.Atoi(digits[:3])
	return ms, true
}
