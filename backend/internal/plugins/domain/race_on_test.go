//go:build race

package domain_test

// raceEnabled is true under the race detector, which makes the 14 MiB timing test several times
// slower; the test then keeps its correctness checks and skips the time limit.
const raceEnabled = true
