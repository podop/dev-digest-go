// Package domain is the vocabulary of the spend context: the money unit, the calendar month the
// spend cap applies to, and the month's spend status. It imports only the standard library and
// carries no JSON or database tags: the wire shapes live in transport/http.
package domain

// Micros is an amount of US dollars in millionths (1 USD = 1_000_000). Costs are summed as
// integers so that month totals do not drift.
type Micros int64

// microsPerUSD is the number of Micros in one dollar.
const microsPerUSD = 1_000_000

// USD returns the amount in dollars, for the wire. It is the nearest float64 to the exact
// amount, so encoding/json prints at most the 6 decimals Micros holds (spec section 7, A13);
// no further rounding is needed.
func (m Micros) USD() float64 { return float64(m) / microsPerUSD }
