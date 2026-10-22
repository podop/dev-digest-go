package domain

import (
	"sync"
	"unicode/utf16"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// UsageMeter accumulates the usage every LLM response of a run reports. The engine returns usage
// only on success, so when a run fails or is cancelled midway the meter is the only record of
// what it spent (earlier map-reduce chunks, schema-invalid re-prompt attempts).
//
// The cost starts at 0 (a run that made no call billed nothing) and becomes unknown for good once
// one response is unpriced. It is safe for concurrent use: chunk calls report in parallel.
type UsageMeter struct {
	mu                  sync.Mutex
	tokensIn, tokensOut int
	costUSD             *float64
	calls               int
}

// NewUsageMeter returns a meter at zero tokens and zero cost.
func NewUsageMeter() *UsageMeter {
	zero := 0.0
	return &UsageMeter{costUSD: &zero}
}

// Add records one response. costUSD is nil when the response is unpriced.
func (m *UsageMeter) Add(tokensIn, tokensOut int, costUSD *float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.tokensIn += tokensIn
	m.tokensOut += tokensOut
	m.costUSD = AddCost(m.costUSD, costUSD)
}

// Snapshot returns the usage so far; CostUSD is nil once any response was unpriced. The returned
// cost is a copy.
func (m *UsageMeter) Snapshot() reviewer.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := reviewer.Usage{TokensIn: m.tokensIn, TokensOut: m.tokensOut}
	if m.costUSD != nil {
		c := *m.costUSD
		u.CostUSD = &c
	}
	return u
}

// Calls is the number of responses recorded.
func (m *UsageMeter) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// AddCost adds two costs; the sum is unknown (nil) when either is. The inputs are not modified.
func AddCost(total, part *float64) *float64 {
	if total == nil || part == nil {
		return nil
	}
	sum := *total + *part
	return &sum
}

// NoUsage is the usage of a run that made no call: zero tokens and a zero (known) cost.
func NoUsage() reviewer.Usage {
	zero := 0.0
	return reviewer.Usage{CostUSD: &zero}
}

// UTF16Units is the length of s in UTF-16 code units, as JavaScript's string length counts it.
func UTF16Units(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// EstimateTokens is the token estimate of a text: ceil(UTF-16 units / 4).
func EstimateTokens(s string) int { return (UTF16Units(s) + 3) / 4 }

// CompareUTF16 orders two strings by UTF-16 code units, as JavaScript's < operator does (a
// supplementary character sorts before U+E000..U+FFFF there, unlike in UTF-8 byte order). It
// returns -1, 0 or +1.
func CompareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}
