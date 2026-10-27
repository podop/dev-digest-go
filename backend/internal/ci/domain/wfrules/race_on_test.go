//go:build race

package wfrules_test

// raceEnabled is true under the race detector, which slows the YAML parser several times; the NFR5
// timing test then keeps its correctness checks and skips the 50 ms limit.
const raceEnabled = true
