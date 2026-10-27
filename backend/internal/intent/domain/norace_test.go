//go:build !race

package domain_test

// raceEnabled is true when the test binary is built with -race, which slows the code ~10x.
const raceEnabled = false
